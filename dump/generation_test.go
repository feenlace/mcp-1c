package dump

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
)

// mustGenSig computes GenSig(dir) or fails the test.
func mustGenSig(t *testing.T, dir string) string {
	t.Helper()
	s, err := GenSig(dir)
	if err != nil {
		t.Fatalf("GenSig(%s): %v", dir, err)
	}
	if s == "" {
		t.Fatalf("GenSig(%s) returned empty signature", dir)
	}
	return s
}

// TestGeneration_ConcurrentReadOnlyOpens is the core regression guard: N
// processes/handles open the SAME built generation READ-ONLY simultaneously and
// all answer queries. If read_only (bbolt LOCK_SH) were not in effect, the 2nd
// open would block on the exclusive flock and the open phase would hang — caught
// by the timeout select. (Design §11 test 1 / the make-or-break in-process hang.)
func TestGeneration_ConcurrentReadOnlyOpens(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\n\t// маркерАльфа\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Documents/Реализация/Ext/ObjectModule.bsl",
		"Процедура ОбработкаПроведения(Отказ)\nКонецПроцедуры\n")

	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	if !GenerationReady(dir, cacheDir, gensig) {
		t.Fatal("generation not READY after BuildGeneration")
	}

	const n = 4
	openDone := make(chan []*Index, 1)
	openErr := make(chan error, 1)
	go func() {
		opened := make([]*Index, 0, n)
		for i := range n {
			// Each open is HELD (not closed) so all N hold their LOCK_SH at once.
			idx, err := OpenGenerationReadOnly(dir, cacheDir, gensig)
			if err != nil {
				openErr <- fmt.Errorf("reader %d open: %w", i, err)
				return
			}
			opened = append(opened, idx)
		}
		openDone <- opened
	}()

	var idxs []*Index
	select {
	case idxs = <-openDone:
	case err := <-openErr:
		t.Fatal(err)
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent read-only opens HUNG — read_only LOCK_SH not in effect")
	}
	t.Cleanup(func() {
		for _, idx := range idxs {
			idx.Close()
		}
	})

	for i, idx := range idxs {
		select {
		case <-idx.Done():
		case <-time.After(30 * time.Second):
			t.Fatalf("reader %d did not become ready", i)
		}
		if err := idx.BuildError(); err != nil {
			t.Fatalf("reader %d build error: %v", i, err)
		}
		if !idx.readOnly {
			t.Fatalf("reader %d was not opened read-only", i)
		}
		matches, total, err := idx.Search(SearchParams{Query: "маркерАльфа", Mode: SearchModeSmart, Limit: 50})
		if err != nil {
			t.Fatalf("reader %d search: %v", i, err)
		}
		if total == 0 || len(matches) == 0 {
			t.Fatalf("reader %d expected a match for маркерАльфа, got total=%d", i, total)
		}
	}
}

// TestGeneration_BuildWhileReaderHoldsOldGen verifies build-then-swap: a new
// generation is built (temp → READY → adopt) while a reader holds the OLD
// generation read-only. The reader must keep answering queries throughout (its
// immutable shards are never touched), the old generation must survive, and the
// new generation must be a separate, adopted directory with the new content.
func TestGeneration_BuildWhileReaderHoldsOldGen(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Старый/Ext/ObjectModule.bsl",
		"Процедура Старая()\n\t// контентВерсииОдин\nКонецПроцедуры\n")

	gen1 := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gen1); err != nil {
		t.Fatalf("BuildGeneration gen1: %v", err)
	}

	reader, err := OpenGenerationReadOnly(dir, cacheDir, gen1)
	if err != nil {
		t.Fatalf("open gen1 reader: %v", err)
	}
	defer reader.Close()
	<-reader.Done()
	if err := reader.BuildError(); err != nil {
		t.Fatalf("gen1 reader build error: %v", err)
	}

	// Drift the dump so the next build produces a DIFFERENT generation.
	mkBSLFile(t, dir, "Catalogs/Новый/Ext/ObjectModule.bsl",
		"Процедура Новая()\n\t// контентВерсииДва\nКонецПроцедуры\n")
	gen2 := mustGenSig(t, dir)
	if gen2 == gen1 {
		t.Fatal("expected a new gensig after dump drift")
	}

	// Build gen2 concurrently while the gen1 reader is held open.
	buildErr := make(chan error, 1)
	go func() { buildErr <- BuildGeneration(dir, cacheDir, gen2) }()

	buildTimeout := time.After(40 * time.Second)
	for building := true; building; {
		// The held reader must never block while the new generation builds.
		qDone := make(chan struct{})
		go func() {
			_, _, _ = reader.Search(SearchParams{Query: "контентВерсииОдин", Mode: SearchModeSmart, Limit: 10})
			close(qDone)
		}()
		select {
		case <-qDone:
		case <-time.After(10 * time.Second):
			t.Fatal("reader.Search blocked while a new generation was being built")
		}

		select {
		case err := <-buildErr:
			if err != nil {
				t.Fatalf("BuildGeneration gen2: %v", err)
			}
			building = false
		case <-buildTimeout:
			t.Fatal("concurrent BuildGeneration did not complete in time")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	// gen1 is untouched and still serves its original content.
	if !GenerationReady(dir, cacheDir, gen1) {
		t.Fatal("gen1 disappeared after gen2 build (generations must be immutable)")
	}
	m1, t1, err := reader.Search(SearchParams{Query: "контентВерсииОдин", Mode: SearchModeSmart, Limit: 10})
	if err != nil || t1 == 0 || len(m1) == 0 {
		t.Fatalf("gen1 reader lost its content: total=%d err=%v", t1, err)
	}

	// gen2 is a separate adopted generation carrying the new content.
	if !GenerationReady(dir, cacheDir, gen2) {
		t.Fatal("gen2 not READY after build")
	}
	r2, err := OpenGenerationReadOnly(dir, cacheDir, gen2)
	if err != nil {
		t.Fatalf("open gen2: %v", err)
	}
	defer r2.Close()
	<-r2.Done()
	m2, t2, err := r2.Search(SearchParams{Query: "контентВерсииДва", Mode: SearchModeSmart, Limit: 10})
	if err != nil || t2 == 0 || len(m2) == 0 {
		t.Fatalf("gen2 missing its new content: total=%d err=%v", t2, err)
	}

	cpath, _ := cachePath(dir, cacheDir)
	if generationDir(cpath, gen1) == generationDir(cpath, gen2) {
		t.Fatal("gen1 and gen2 resolved to the same directory")
	}
}

// TestGeneration_PartialBuildNotAdopted verifies a generation WITHOUT a READY
// sentinel (a crashed / in-progress build) is never adopted: GenerationReady is
// false, OpenGenerationReadOnly refuses it, and OpenForServe falls back to the
// legacy flat build instead of opening the partial directory.
func TestGeneration_PartialBuildNotAdopted(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "Процедура Т()\nКонецПроцедуры\n")
	gensig := mustGenSig(t, dir)

	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}

	// Fabricate a generation directory WITHOUT a READY sentinel.
	partial := generationDir(cpath, gensig)
	if err := os.MkdirAll(filepath.Join(partial, "shard_0"), 0o755); err != nil {
		t.Fatal(err)
	}

	if GenerationReady(dir, cacheDir, gensig) {
		t.Fatal("GenerationReady returned true for a generation without READY")
	}
	if _, err := OpenGenerationReadOnly(dir, cacheDir, gensig); err == nil {
		t.Fatal("OpenGenerationReadOnly adopted a generation without READY")
	}

	// OpenForServe must NOT adopt the partial generation — it falls back to the
	// legacy flat (read-write) NewIndex build.
	idx, err := OpenForServe(dir, cacheDir)
	if err != nil {
		t.Fatalf("OpenForServe: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)
	if idx.readOnly {
		t.Fatal("OpenForServe opened the partial generation read-only instead of falling back")
	}

	// Writing the sentinel makes the generation adoptable.
	if err := writeReadySentinel(partial, gensig); err != nil {
		t.Fatalf("writeReadySentinel: %v", err)
	}
	if !GenerationReady(dir, cacheDir, gensig) {
		t.Fatal("GenerationReady still false after writing READY")
	}
}

// TestGeneration_BoltTimeoutBoundsConflictingOpen guards the bolt_timeout
// STRING-type trap: a read-only open of a shard whose root.bolt is held under an
// exclusive lock must FAIL within the bound, never wait forever. If bolt_timeout
// were passed as a non-string it would silently revert to Timeout=0 (the
// original infinite hang). (Design §11 test 2.)
func TestGeneration_BoltTimeoutBoundsConflictingOpen(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	for i := range 3 {
		mkBSLFile(t, dir, fmt.Sprintf("Catalogs/Т%d/Ext/ObjectModule.bsl", i),
			fmt.Sprintf("Процедура Т%d()\nКонецПроцедуры\n", i))
	}
	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}

	cpath, _ := cachePath(dir, cacheDir)
	shardDirs := cacheShardDirs(generationDir(cpath, gensig))
	if len(shardDirs) == 0 {
		t.Fatal("no shard dirs in built generation")
	}
	target := shardDirs[0]

	// Hold the shard READ-WRITE (bbolt LOCK_EX), simulating a writer / old binary.
	rw, err := bleve.Open(target)
	if err != nil {
		t.Fatalf("RW open: %v", err)
	}
	defer rw.Close()

	type res struct {
		err error
		dur time.Duration
	}
	done := make(chan res, 1)
	go func() {
		start := time.Now()
		shards, openErr := openCachedShards([]string{target}, true, "500ms")
		for _, s := range shards {
			if s != nil {
				s.Close()
			}
		}
		done <- res{err: openErr, dur: time.Since(start)}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("expected the read-only open to fail against a held LOCK_EX, got nil error")
		}
		if r.dur > 10*time.Second {
			t.Fatalf("read-only open took %s — bolt_timeout not honored (string-type trap?)", r.dur)
		}
		t.Logf("conflicting read-only open failed as expected after %s: %v", r.dur, r.err)
	case <-time.After(20 * time.Second):
		t.Fatal("read-only open HUNG against a held LOCK_EX — bolt_timeout string not threaded (Timeout=0)")
	}
}

// TestGenSig_DeterministicAndDriftSensitive verifies the content signature is
// stable for unchanged content and changes on add / modify (so a rebuild yields
// a new generation directory rather than mutating one in use).
func TestGenSig_DeterministicAndDriftSensitive(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/А/Ext/ObjectModule.bsl", "Процедура А()\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Catalogs/Б/Ext/ObjectModule.bsl", "Процедура Б()\nКонецПроцедуры\n")

	s1 := mustGenSig(t, dir)
	if s2 := mustGenSig(t, dir); s1 != s2 {
		t.Fatalf("GenSig not deterministic: %q vs %q", s1, s2)
	}

	mkBSLFile(t, dir, "Catalogs/В/Ext/ObjectModule.bsl", "Процедура В()\nКонецПроцедуры\n")
	s3 := mustGenSig(t, dir)
	if s3 == s1 {
		t.Fatal("GenSig unchanged after adding a file")
	}

	mkBSLFile(t, dir, "Catalogs/А/Ext/ObjectModule.bsl",
		"Процедура А()\n\t// расширенное содержимое меняет размер\nКонецПроцедуры\n")
	if s4 := mustGenSig(t, dir); s4 == s3 {
		t.Fatal("GenSig unchanged after modifying a file")
	}
}

// TestOpenForServe_PrefersReadyGeneration verifies the serve read path opens a
// READY generation read-only and that the read-only base rejects runtime writes
// (the per-process extension overlay is a later chunk).
func TestOpenForServe_PrefersReadyGeneration(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Сервис/Ext/ObjectModule.bsl",
		"Процедура Сервис()\n\t// маркерСервис\nКонецПроцедуры\n")
	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}

	idx, err := OpenForServe(dir, cacheDir)
	if err != nil {
		t.Fatalf("OpenForServe: %v", err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("build error: %v", err)
	}
	if !idx.readOnly {
		t.Fatal("OpenForServe did not open the READY generation read-only")
	}

	m, total, err := idx.Search(SearchParams{Query: "маркерСервис", Mode: SearchModeSmart, Limit: 10})
	if err != nil || total == 0 || len(m) == 0 {
		t.Fatalf("read-only serve search failed: total=%d err=%v", total, err)
	}

	if err := idx.IndexDoc("Справочник.Любой.МодульОбъекта", "Процедура X()\nКонецПроцедуры\n"); err == nil {
		t.Fatal("expected IndexDoc to be rejected on a read-only index")
	}
}

// TestBuildGeneration_IdempotentWhenReady verifies a second BuildGeneration of an
// already-READY gensig is a no-op (content-addressed: same gensig = same content).
func TestBuildGeneration_IdempotentWhenReady(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Идемп/Ext/ObjectModule.bsl", "Процедура И()\nКонецПроцедуры\n")
	gensig := mustGenSig(t, dir)

	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration #1: %v", err)
	}
	cpath, _ := cachePath(dir, cacheDir)
	readyPath := readySentinelPath(generationDir(cpath, gensig))
	st1, err := os.Stat(readyPath)
	if err != nil {
		t.Fatalf("stat READY after #1: %v", err)
	}

	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration #2 (should be no-op): %v", err)
	}
	st2, err := os.Stat(readyPath)
	if err != nil {
		t.Fatalf("stat READY after #2: %v", err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("second BuildGeneration rewrote the generation (not idempotent)")
	}

	// No leftover .building-* temp dirs in the generations arena.
	entries, err := os.ReadDir(generationsDir(cpath))
	if err != nil {
		t.Fatalf("ReadDir generations: %v", err)
	}
	for _, e := range entries {
		if len(e.Name()) >= len(buildTmpPrefix) && e.Name()[:len(buildTmpPrefix)] == buildTmpPrefix {
			t.Fatalf("leftover build temp dir: %s", e.Name())
		}
	}
}

// TestReapStaleBuildDirs_RemovesStaleKeepsFresh is the FIX #4 (premortem-5)
// regression: an interrupted build (SIGKILL/OOM/power-loss) leaks a partial
// .building-* generation dir that GCGenerations deliberately skips, so nothing ever
// reaps it and disk grows to ENOSPC. The reaper must remove an ABANDONED (stale,
// no recent writes) temp dir while LEAVING a CONCURRENT leader's fresh in-progress
// temp dir untouched, and a normal build must still succeed afterwards.
func TestReapStaleBuildDirs_RemovesStaleKeepsFresh(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Реап/Ext/ObjectModule.bsl", "Процедура Р()\nКонецПроцедуры\n")

	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	gensDir := generationsDir(cpath)
	if err := os.MkdirAll(gensDir, 0o755); err != nil {
		t.Fatalf("mkdir gensDir: %v", err)
	}

	// STALE: a partial generation an interrupted builder left behind. Age every
	// entry in its tree past the staleness window (the reaper gates on the NEWEST
	// mtime anywhere in the tree, so all of them must be old to count as abandoned).
	staleDir := filepath.Join(gensDir, buildTmpPrefix+"deadbeef-stale")
	staleShard := filepath.Join(staleDir, "shard_0")
	if err := os.MkdirAll(staleShard, 0o755); err != nil {
		t.Fatalf("mkdir staleShard: %v", err)
	}
	staleSeg := filepath.Join(staleShard, "segment.zap")
	if err := os.WriteFile(staleSeg, []byte("partial"), 0o644); err != nil {
		t.Fatalf("write stale seg: %v", err)
	}
	old := time.Now().Add(-2 * buildDirStaleAfter)
	for _, p := range []string{staleSeg, staleShard, staleDir} { // children-first
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	// FRESH: a CONCURRENT build-leader mid-build that just streamed a segment. Its
	// tree carries a current mtime, so it MUST survive the reaper.
	freshDir := filepath.Join(gensDir, buildTmpPrefix+"cafef00d-fresh")
	if err := os.MkdirAll(filepath.Join(freshDir, "shard_0"), 0o755); err != nil {
		t.Fatalf("mkdir freshDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(freshDir, "shard_0", "segment.zap"), []byte("live"), 0o644); err != nil {
		t.Fatalf("write fresh seg: %v", err)
	}

	// The defect: GCGenerations alone never reaps a .building-* (it skips the
	// prefix), so without the reaper the stale partial leaks forever.
	if _, err := GCGenerations(dir, cacheDir, ""); err != nil {
		t.Fatalf("GCGenerations: %v", err)
	}
	if _, err := os.Stat(staleDir); err != nil {
		t.Fatalf("precondition: GCGenerations should leave .building-* (the leak); stat: %v", err)
	}

	removed, err := ReapStaleBuildDirs(dir, cacheDir)
	if err != nil {
		t.Fatalf("ReapStaleBuildDirs: %v", err)
	}
	if len(removed) != 1 || removed[0] != filepath.Base(staleDir) {
		t.Fatalf("removed = %v, want only the stale dir %q", removed, filepath.Base(staleDir))
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale build temp dir still present after reap: %v", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("fresh in-progress build temp dir was reaped (must survive): %v", err)
	}

	// A normal build still succeeds: the reaper left the arena usable and never
	// touched a real generation. (BuildGeneration creates and adopts its own temp
	// dir; the surviving synthetic fresh dir does not interfere.)
	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration after reap: %v", err)
	}
	if !generationReadyDir(generationDir(cpath, gensig)) {
		t.Fatalf("generation %s not READY after post-reap build", gensig)
	}
}

// snapshotShardTree records "mtimeNano:size" per file under each shard dir, keyed
// by "<shardBase>/<relpath>". A directory MOVE (adoption) preserves every inner
// file's mtime and size exactly; a REBUILD writes fresh files with new mtimes, so
// an unequal snapshot before/after proves shards were rebuilt rather than adopted.
func snapshotShardTree(t *testing.T, shardDirs []string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, sd := range shardDirs {
		base := filepath.Base(sd)
		err := filepath.WalkDir(sd, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(sd, path)
			if rerr != nil {
				return rerr
			}
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			snap[base+"/"+filepath.ToSlash(rel)] = fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
			return nil
		})
		if err != nil {
			t.Fatalf("snapshotShardTree %s: %v", sd, err)
		}
	}
	return snap
}

// sameSnapshot reports whether two shard-tree snapshots are identical.
func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestGenSig_SchemaZapBumpChangesSignatureAndIsolatesGenerations is the schema-key
// regression guard (design #2/#6): folding the schema and zap-format versions into
// the gensig means a bump of either yields a DIFFERENT signature, and a reader
// computing the bumped signature never finds — and OpenGenerationReadOnly never
// adopts — a generation built under the previous schema. It also confirms
// GenSig == genSig with the current versions, so the production path and the test
// seam agree.
func TestGenSig_SchemaZapBumpChangesSignatureAndIsolatesGenerations(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Схема/Ext/ObjectModule.bsl",
		"Процедура С()\n\t// маркерСхема\nКонецПроцедуры\n")

	g1, err := genSig(dir, dumpIndexSchemaVersion, zapSegmentVersion)
	if err != nil {
		t.Fatalf("genSig (current versions): %v", err)
	}
	if got := mustGenSig(t, dir); got != g1 {
		t.Fatalf("GenSig (%q) != genSig with current versions (%q)", got, g1)
	}

	gSchema, err := genSig(dir, dumpIndexSchemaVersion+1, zapSegmentVersion)
	if err != nil {
		t.Fatalf("genSig (schema+1): %v", err)
	}
	if gSchema == g1 {
		t.Fatal("schema-version bump did not change the gensig")
	}

	gZap, err := genSig(dir, dumpIndexSchemaVersion, zapSegmentVersion+1)
	if err != nil {
		t.Fatalf("genSig (zap+1): %v", err)
	}
	if gZap == g1 {
		t.Fatal("zap-version bump did not change the gensig")
	}
	if gZap == gSchema {
		t.Fatal("schema and zap bumps collided to the same gensig")
	}

	// Build a generation for the CURRENT-schema signature.
	if err := BuildGeneration(dir, cacheDir, g1); err != nil {
		t.Fatalf("BuildGeneration g1: %v", err)
	}
	if !GenerationReady(dir, cacheDir, g1) {
		t.Fatal("current-schema generation not READY after build")
	}

	// A binary running a bumped schema computes gSchema and MUST NOT see or adopt
	// the g1 generation built under the previous schema.
	if GenerationReady(dir, cacheDir, gSchema) {
		t.Fatal("a generation built under the current schema was visible under a bumped-schema signature")
	}
	if _, err := OpenGenerationReadOnly(dir, cacheDir, gSchema); err == nil {
		t.Fatal("OpenGenerationReadOnly adopted a generation under a mismatched-schema signature")
	}
	// The current-schema generation remains adoptable.
	if !GenerationReady(dir, cacheDir, g1) {
		t.Fatal("current-schema generation vanished")
	}
}

// TestMigrateFlatToGeneration_AdoptsExistingShardsWithoutRebuild verifies the
// migration shim adopts an existing flat cache by MOVING its shards into the
// generation layout — no rebuild storm. The flat shards must be gone from the
// cache root afterwards and the generation must hold the byte-for-byte identical
// shard tree (proving a rename, not a re-index), and it must serve read-only.
func TestMigrateFlatToGeneration_AdoptsExistingShardsWithoutRebuild(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Миграция/Ext/ObjectModule.bsl",
		"Процедура Мигр()\n\t// маркерМиграция\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Documents/Перенос/Ext/ObjectModule.bsl",
		"Процедура Пер()\nКонецПроцедуры\n")

	// Build a LEGACY flat cache (shard_* directly under the per-dump cache dir).
	if err := BuildCache(dir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache (flat): %v", err)
	}
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	flatShards := cacheShardDirs(cpath)
	if len(flatShards) == 0 {
		t.Fatal("expected a flat shard layout after BuildCache")
	}
	gensig := mustGenSig(t, dir)
	if generationReadyDir(generationDir(cpath, gensig)) {
		t.Fatal("a generation already existed before migration")
	}
	before := snapshotShardTree(t, flatShards)

	g, migrated, err := migrateFlatToGeneration(dir, cacheDir)
	if err != nil {
		t.Fatalf("migrateFlatToGeneration: %v", err)
	}
	if !migrated {
		t.Fatal("migration reported migrated=false for an adoptable flat cache")
	}
	if g != gensig {
		t.Fatalf("migration gensig = %q, want %q", g, gensig)
	}

	// MOVED, not rebuilt: nothing remains under the cache root, and the generation
	// holds the identical shard tree.
	if remaining := cacheShardDirs(cpath); len(remaining) != 0 {
		t.Fatalf("flat shards still present after adoption (not moved): %v", remaining)
	}
	genDir := generationDir(cpath, gensig)
	if !generationReadyDir(genDir) {
		t.Fatal("generation not READY after adoption")
	}
	genShards := cacheShardDirs(genDir)
	if len(genShards) != len(flatShards) {
		t.Fatalf("generation has %d shards, flat had %d", len(genShards), len(flatShards))
	}
	if after := snapshotShardTree(t, genShards); !sameSnapshot(before, after) {
		t.Fatal("shard tree changed during migration — shards were rebuilt, not adopted")
	}

	// The adopted generation opens read-only and answers queries.
	idx, err := OpenGenerationReadOnly(dir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("OpenGenerationReadOnly after adoption: %v", err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("adopted generation build error: %v", err)
	}
	if !idx.readOnly {
		t.Fatal("adopted generation not opened read-only")
	}
	m, total, err := idx.Search(SearchParams{Query: "маркерМиграция", Mode: SearchModeSmart, Limit: 10})
	if err != nil || total == 0 || len(m) == 0 {
		t.Fatalf("adopted generation search failed: total=%d err=%v", total, err)
	}
}

// TestMigrateFlatToGeneration_UnstampedLegacyManifest covers the real-world upgrade
// path: a flat cache built by a binary from BEFORE schema/zap stamping existed has a
// manifest with neither field (they unmarshal to 0), so the baseline accessors map it
// to the FROZEN baseline schema/zap. Whether such a cache is adopted-by-move or
// rebuilt then depends on whether the running binary is STILL on that baseline:
//   - baseline == current: the unstamped cache is format-compatible and adopted by a
//     metadata move (no rebuild storm);
//   - baseline <  current (a schema/zap bump has shipped — e.g. the v2 NFC docID
//     normalisation): the unstamped cache is a different, older schema and is REBUILT
//     via the one-time build fallback. Adopting it would serve incompatibly-keyed
//     shards (e.g. pre-NFC NFD docIDs) under a current-schema signature; instead the
//     flat shards are left in place beside a fresh generation.
func TestMigrateFlatToGeneration_UnstampedLegacyManifest(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Легаси/Ext/ObjectModule.bsl",
		"Процедура Лег()\n\t// маркерЛегаси\nКонецПроцедуры\n")

	if err := BuildCache(dir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache (flat): %v", err)
	}
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}

	// Rewrite the manifest WITHOUT schema/zap stamps (omitempty drops the zero
	// fields), reproducing a pre-stamping legacy manifest on disk.
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: m=%v err=%v", m, err)
	}
	m.SchemaVersion = 0
	m.ZapVersion = 0
	if err := m.Save(cpath); err != nil {
		t.Fatalf("re-save unstamped manifest: %v", err)
	}
	// Confirm the on-disk manifest is genuinely unstamped and maps to the frozen
	// baseline via the accessors.
	reloaded, err := LoadManifest(cpath)
	if err != nil || reloaded == nil {
		t.Fatalf("reload manifest: m=%v err=%v", reloaded, err)
	}
	if reloaded.SchemaVersion != 0 || reloaded.ZapVersion != 0 {
		t.Fatalf("expected an unstamped manifest, got schema=%d zap=%d", reloaded.SchemaVersion, reloaded.ZapVersion)
	}
	if reloaded.schemaVersion() != baselineSchemaVersion || reloaded.zapVersion() != baselineZapVersion {
		t.Fatalf("baseline accessors wrong: schema=%d zap=%d", reloaded.schemaVersion(), reloaded.zapVersion())
	}

	flatShards := cacheShardDirs(cpath)
	if len(flatShards) == 0 {
		t.Fatal("expected flat shards before migration")
	}
	before := snapshotShardTree(t, flatShards)

	g, migrated, err := migrateFlatToGeneration(dir, cacheDir)
	if err != nil {
		t.Fatalf("migrateFlatToGeneration: %v", err)
	}
	if !migrated {
		t.Fatal("migration should produce a generation (adopt or build fallback)")
	}

	onBaseline := dumpIndexSchemaVersion == baselineSchemaVersion && zapSegmentVersion == baselineZapVersion
	if onBaseline {
		// Adopted (moved), not rebuilt.
		if remaining := cacheShardDirs(cpath); len(remaining) != 0 {
			t.Fatalf("flat shards not moved on unstamped-baseline adoption: %v", remaining)
		}
		genShards := cacheShardDirs(generationDir(cpath, g))
		if after := snapshotShardTree(t, genShards); !sameSnapshot(before, after) {
			t.Fatal("unstamped-baseline adoption rebuilt the shards instead of moving them")
		}
		return
	}

	// Schema/zap has been bumped past the baseline: the unstamped cache is
	// schema-stale and must be rebuilt via the build fallback, NOT adopted by move —
	// the flat shards stay in place beside a fresh generation.
	if remaining := cacheShardDirs(cpath); len(remaining) != len(flatShards) {
		t.Fatalf("flat shards were moved on a schema-stale unstamped cache "+
			"(should rebuild, not adopt): had %d, now %d", len(flatShards), len(remaining))
	}
	if !generationReadyDir(generationDir(cpath, g)) {
		t.Fatal("no READY generation after build fallback")
	}
	idx, err := OpenGenerationReadOnly(dir, cacheDir, g)
	if err != nil {
		t.Fatalf("open rebuilt generation: %v", err)
	}
	defer idx.Close()
	<-idx.Done()
	mtc, total, err := idx.Search(SearchParams{Query: "маркерЛегаси", Mode: SearchModeSmart, Limit: 10})
	if err != nil || total == 0 || len(mtc) == 0 {
		t.Fatalf("rebuilt generation search failed: total=%d err=%v", total, err)
	}
}

// TestMigrateFlatToGeneration_SchemaMismatchRebuildsInsteadOfAdopting verifies the
// adoption safety gate: a flat cache whose manifest records a different index
// schema than the running binary is NOT adopted (adopting would serve
// incompatibly-formatted shards under a current-schema signature). Instead the
// shim builds a fresh generation once, leaving the flat shards untouched in place.
func TestMigrateFlatToGeneration_SchemaMismatchRebuildsInsteadOfAdopting(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Несовм/Ext/ObjectModule.bsl",
		"Процедура Н()\n\t// маркерНесовм\nКонецПроцедуры\n")

	if err := BuildCache(dir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache (flat): %v", err)
	}
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}

	// Tamper the flat manifest so its schema version no longer matches the binary,
	// simulating a flat cache built by a binary with an older/newer index schema.
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: m=%v err=%v", m, err)
	}
	m.SchemaVersion = dumpIndexSchemaVersion + 999
	if err := m.Save(cpath); err != nil {
		t.Fatalf("re-save tampered manifest: %v", err)
	}

	flatBefore := cacheShardDirs(cpath)
	if len(flatBefore) == 0 {
		t.Fatal("expected flat shards before migration")
	}

	g, migrated, err := migrateFlatToGeneration(dir, cacheDir)
	if err != nil {
		t.Fatalf("migrateFlatToGeneration: %v", err)
	}
	if !migrated {
		t.Fatal("migration should still produce a generation via the build fallback")
	}

	// Build fallback (NOT adoption): the flat shards stay in place and a fresh
	// generation exists alongside them.
	if remaining := cacheShardDirs(cpath); len(remaining) != len(flatBefore) {
		t.Fatalf("flat shards were moved on a schema mismatch (should rebuild, not adopt): had %d, now %d",
			len(flatBefore), len(remaining))
	}
	if !generationReadyDir(generationDir(cpath, g)) {
		t.Fatal("no READY generation after build fallback")
	}

	idx, err := OpenGenerationReadOnly(dir, cacheDir, g)
	if err != nil {
		t.Fatalf("open rebuilt generation: %v", err)
	}
	defer idx.Close()
	<-idx.Done()
	mtc, total, err := idx.Search(SearchParams{Query: "маркерНесовм", Mode: SearchModeSmart, Limit: 10})
	if err != nil || total == 0 || len(mtc) == 0 {
		t.Fatalf("rebuilt generation search failed: total=%d err=%v", total, err)
	}
}

// TestMigrateFlatToGeneration_FlatStillOpensWhenBypassed is the backward-compat
// guard: when the migration shim is bypassed (the legacy NewIndex path is used
// directly), an existing flat cache must still open read-WRITE and serve, and no
// generation is created as a side effect.
func TestMigrateFlatToGeneration_FlatStillOpensWhenBypassed(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Совмест/Ext/ObjectModule.bsl",
		"Процедура Сов()\n\t// маркерСовмест\nКонецПроцедуры\n")

	if err := BuildCache(dir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache (flat): %v", err)
	}
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	if len(cacheShardDirs(cpath)) == 0 {
		t.Fatal("expected a flat cache")
	}

	// Bypass migration: NewIndex must still open the flat cache read-WRITE.
	idx, err := NewIndex(dir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex (flat reopen): %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)
	if idx.readOnly {
		t.Fatal("legacy flat NewIndex unexpectedly opened read-only")
	}
	m, total, err := idx.Search(SearchParams{Query: "маркерСовмест", Mode: SearchModeSmart, Limit: 10})
	if err != nil || total == 0 || len(m) == 0 {
		t.Fatalf("flat cache search failed: total=%d err=%v", total, err)
	}
	if generationReadyDir(generationDir(cpath, mustGenSig(t, dir))) {
		t.Fatal("a generation was created by the bypassed (flat) path")
	}
}

// TestOpenForServe_MigratesAndServesFlatCacheReadOnly is the end-to-end wiring
// check: OpenForServe on a dump that has only a legacy flat cache migrates it
// (adoption) and serves the resulting generation READ-ONLY, with the flat shards
// moved into the generation.
func TestOpenForServe_MigratesAndServesFlatCacheReadOnly(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Серв/Ext/ObjectModule.bsl",
		"Процедура Серв()\n\t// маркерСервМигр\nКонецПроцедуры\n")

	if err := BuildCache(dir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache (flat): %v", err)
	}
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	if len(cacheShardDirs(cpath)) == 0 {
		t.Fatal("expected a flat cache before serve")
	}

	idx, err := OpenForServe(dir, cacheDir)
	if err != nil {
		t.Fatalf("OpenForServe: %v", err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("serve build error: %v", err)
	}
	if !idx.readOnly {
		t.Fatal("OpenForServe did not migrate the flat cache to a read-only generation")
	}
	if remaining := cacheShardDirs(cpath); len(remaining) != 0 {
		t.Fatalf("flat shards remain after OpenForServe migration: %v", remaining)
	}
	if !generationReadyDir(generationDir(cpath, mustGenSig(t, dir))) {
		t.Fatal("no READY generation after OpenForServe migration")
	}
	m, total, err := idx.Search(SearchParams{Query: "маркерСервМигр", Mode: SearchModeSmart, Limit: 10})
	if err != nil || total == 0 || len(m) == 0 {
		t.Fatalf("served generation search failed: total=%d err=%v", total, err)
	}
}
