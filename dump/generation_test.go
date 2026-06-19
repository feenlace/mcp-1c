package dump

import (
	"fmt"
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
