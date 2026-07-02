package dump

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReindex_DoesNotWipeLiveGeneration is the core reindex-safety guard. Before
// this chunk, NewIndex(reindex=true) os.RemoveAll'd the whole per-dump cache dir,
// destroying g/ and any generation a concurrent read-only serve held. Now a
// reindex whose gensig COLLIDES with a generation a live reader holds must NOT
// wipe it: the held generation stays byte-for-byte intact (its READY sentinel is
// never rewritten) and the reader keeps answering throughout.
func TestReindex_DoesNotWipeLiveGeneration(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Живой/Ext/ObjectModule.bsl",
		"Процедура Живой()\n\t// маркерЖивой\nКонецПроцедуры\n")

	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}

	// A live reader holds the current generation read-only (registers in readers/).
	reader, err := OpenGenerationReadOnly(dir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()
	<-reader.Done()
	if err := reader.BuildError(); err != nil {
		t.Fatalf("reader build error: %v", err)
	}

	cpath, _ := cachePath(dir, cacheDir)
	readyPath := readySentinelPath(generationDir(cpath, gensig))
	before, err := os.Stat(readyPath)
	if err != nil {
		t.Fatalf("stat READY before reindex: %v", err)
	}

	// Reindex the SAME (unchanged) dump while the reader holds the generation. The
	// computed gensig collides with the live generation; reindex MUST NOT wipe it.
	rx, err := NewIndex(dir, cacheDir, true)
	if err != nil {
		t.Fatalf("NewIndex reindex: %v", err)
	}
	defer rx.Close()
	waitReady(t, rx, 30*time.Second)
	if err := rx.BuildError(); err != nil {
		t.Fatalf("reindex build error: %v", err)
	}

	// The live generation's READY sentinel was never rewritten (not wiped+rebuilt).
	after, err := os.Stat(readyPath)
	if err != nil {
		t.Fatalf("stat READY after reindex (generation was WIPED): %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("reindex rewrote the live generation (READY mtime changed) — it must " +
			"never wipe a generation a live reader holds")
	}

	// The held reader still answers from its untouched generation.
	m, total, err := reader.Search(SearchParams{Query: "маркерЖивой", Mode: SearchModeSmart, Limit: 10})
	if err != nil || total == 0 || len(m) == 0 {
		t.Fatalf("held reader lost content after reindex: total=%d err=%v", total, err)
	}
	// And the reindex itself serves the same generation read-only.
	if !rx.readOnly {
		t.Fatal("reindex did not serve a read-only generation")
	}
	if rx.ModuleCount() != 1 {
		t.Fatalf("reindex module count: got %d want 1", rx.ModuleCount())
	}
}

// TestReindex_AfterDriftBuildsFreshGenAndKeepsOld covers the prompt's case (a):
// reindex while a reader holds a generation, with the dump drifted in between.
// The reindex must produce a NEW generation (new gensig) and serve it, while the
// OLD generation the reader holds survives untouched (no wipe).
func TestReindex_AfterDriftBuildsFreshGenAndKeepsOld(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Старый/Ext/ObjectModule.bsl",
		"Процедура Старый()\n\t// контентОдин\nКонецПроцедуры\n")

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

	// Drift the dump so reindex computes a NEW generation.
	mkBSLFile(t, dir, "Catalogs/Новый/Ext/ObjectModule.bsl",
		"Процедура Новый()\n\t// контентДва\nКонецПроцедуры\n")
	gen2 := mustGenSig(t, dir)
	if gen2 == gen1 {
		t.Fatal("expected a new gensig after drift")
	}

	rx, err := NewIndex(dir, cacheDir, true)
	if err != nil {
		t.Fatalf("NewIndex reindex: %v", err)
	}
	defer rx.Close()
	waitReady(t, rx, 30*time.Second)
	if err := rx.BuildError(); err != nil {
		t.Fatalf("reindex build error: %v", err)
	}

	// gen1 (held by the reader) survives untouched.
	if !GenerationReady(dir, cacheDir, gen1) {
		t.Fatal("reindex wiped gen1 while a reader held it")
	}
	m1, t1, err := reader.Search(SearchParams{Query: "контентОдин", Mode: SearchModeSmart, Limit: 10})
	if err != nil || t1 == 0 || len(m1) == 0 {
		t.Fatalf("gen1 reader lost content after reindex: total=%d err=%v", t1, err)
	}

	// reindex produced and serves the NEW generation gen2 with the new content.
	if !GenerationReady(dir, cacheDir, gen2) {
		t.Fatal("reindex did not build the new generation gen2")
	}
	if !rx.readOnly || rx.ModuleCount() != 2 {
		t.Fatalf("reindex did not serve gen2 read-only with 2 modules: readOnly=%v count=%d",
			rx.readOnly, rx.ModuleCount())
	}
	m2, t2, err := rx.Search(SearchParams{Query: "контентДва", Mode: SearchModeSmart, Limit: 10})
	if err != nil || t2 == 0 || len(m2) == 0 {
		t.Fatalf("reindex generation missing the new content: total=%d err=%v", t2, err)
	}

	cpath, _ := cachePath(dir, cacheDir)
	if generationDir(cpath, gen1) == generationDir(cpath, gen2) {
		t.Fatal("gen1 and gen2 resolved to the same directory")
	}
}

// TestGCGenerations_RemovesUnheldKeepsHeld covers the prompt's case (b): GC removes
// an old, unheld generation but NEVER one a live reader holds (nor the current one).
func TestGCGenerations_RemovesUnheldKeepsHeld(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/В1/Ext/ObjectModule.bsl", "Процедура В1()\nКонецПроцедуры\n")
	gen1 := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gen1); err != nil {
		t.Fatalf("BuildGeneration gen1: %v", err)
	}
	// Hold gen1 with a live reader.
	reader, err := OpenGenerationReadOnly(dir, cacheDir, gen1)
	if err != nil {
		t.Fatalf("open gen1 reader: %v", err)
	}
	<-reader.Done()

	// Drift → gen2, build it: gen2 becomes the new "current" generation.
	mkBSLFile(t, dir, "Catalogs/В2/Ext/ObjectModule.bsl", "Процедура В2()\nКонецПроцедуры\n")
	gen2 := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gen2); err != nil {
		t.Fatalf("BuildGeneration gen2: %v", err)
	}

	// GC keeping gen2: gen1 is OLD but HELD by a live reader → must survive; gen2 is
	// the current generation → must survive. Nothing removed.
	removed, err := GCGenerations(dir, cacheDir, gen2)
	if err != nil {
		t.Fatalf("GC #1: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("GC removed a held/current generation: %v", removed)
	}
	if !GenerationReady(dir, cacheDir, gen1) || !GenerationReady(dir, cacheDir, gen2) {
		t.Fatal("GC #1 wrongly removed gen1 (held) or gen2 (current)")
	}

	// Release the gen1 reader; gen1 is now OLD and UNHELD.
	if err := reader.Close(); err != nil {
		t.Fatalf("reader.Close: %v", err)
	}

	removed, err = GCGenerations(dir, cacheDir, gen2)
	if err != nil {
		t.Fatalf("GC #2: %v", err)
	}
	if len(removed) != 1 || removed[0] != gen1 {
		t.Fatalf("GC #2 should have removed exactly gen1, got %v", removed)
	}
	if GenerationReady(dir, cacheDir, gen1) {
		t.Fatal("GC #2 did not remove the old, unheld gen1")
	}
	if !GenerationReady(dir, cacheDir, gen2) {
		t.Fatal("GC #2 wrongly removed the current gen2")
	}
}

// TestReaderRegistry_LivenessReapsDeadEntry covers the prompt's case (c): the
// readers/ registry's liveness check reaps a dead (stale-mtime) entry, treats a
// freshly registered reader as live, and clears it on deregister.
func TestReaderRegistry_LivenessReapsDeadEntry(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Рег/Ext/ObjectModule.bsl", "Процедура Рег()\nКонецПроцедуры\n")
	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	cpath, _ := cachePath(dir, cacheDir)
	genDir := generationDir(cpath, gensig)

	// No reader yet → not held.
	if generationHasLiveReader(genDir) {
		t.Fatal("generationHasLiveReader true with no registered reader")
	}

	// Fabricate a DEAD reader entry: its mtime is older than the staleness window,
	// i.e. its process crashed/exited without deregistering.
	readersDir := filepath.Join(genDir, readersDirName)
	if err := os.MkdirAll(readersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(readersDir, "999999-dead")
	if err := os.WriteFile(dead, []byte("pid=999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * readerStaleAfter)
	if err := os.Chtimes(dead, old, old); err != nil {
		t.Fatal(err)
	}

	// Liveness must report not-held AND reap the dead entry as a side effect.
	if generationHasLiveReader(genDir) {
		t.Fatal("a stale (dead) entry was counted as a live reader")
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatal("stale reader entry was not reaped by the liveness check")
	}

	// A freshly registered reader IS live, and Close deregisters it.
	reg, err := registerReader(genDir)
	if err != nil {
		t.Fatalf("registerReader: %v", err)
	}
	if !generationHasLiveReader(genDir) {
		t.Fatal("a freshly registered reader was not detected as live")
	}
	reg.Close()
	if generationHasLiveReader(genDir) {
		t.Fatal("reader still considered live after Close (deregister failed)")
	}
	// Close is idempotent.
	reg.Close()
}

// TestForceRebuildGeneration_RebuildsWhenNoLiveReader proves the serve-path fix for
// `serve --reindex`: BuildGeneration is content-addressed and no-ops on an
// already-READY gensig, so a plain rebuild of unchanged content does nothing.
// ForceRebuildGeneration must instead force a COLD rebuild (drop then build) when no
// live reader holds the generation, so an operator recovering a suspected-corrupt
// cache actually gets a fresh build.
func TestForceRebuildGeneration_RebuildsWhenNoLiveReader(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Проба/Ext/ObjectModule.bsl",
		"Процедура Проба()\n\t// маркерПроба\nКонецПроцедуры\n")

	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}

	// Plant a probe file INSIDE the current generation dir. A genuine cold rebuild
	// drops the whole generation dir and rebuilds it (temp+rename), so the probe must
	// be GONE afterwards; a no-op rebuild would leave it in place.
	cpath, _ := cachePath(dir, cacheDir)
	genDir := generationDir(cpath, gensig)
	probe := filepath.Join(genDir, "rebuild-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatalf("plant probe: %v", err)
	}

	// No live reader holds the generation → ForceRebuildGeneration must drop+rebuild.
	if err := ForceRebuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("ForceRebuildGeneration: %v", err)
	}

	if _, err := os.Stat(probe); !os.IsNotExist(err) {
		t.Fatal("ForceRebuildGeneration did not cold-rebuild: the probe planted in the " +
			"old generation dir survived, so the build no-oped instead of dropping+rebuilding")
	}
	if !GenerationReady(dir, cacheDir, gensig) {
		t.Fatal("generation is not READY after ForceRebuildGeneration")
	}

	// The freshly rebuilt generation still serves the content read-only.
	reader, err := OpenGenerationReadOnly(dir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("open rebuilt reader: %v", err)
	}
	defer reader.Close()
	<-reader.Done()
	if err := reader.BuildError(); err != nil {
		t.Fatalf("rebuilt reader build error: %v", err)
	}
	m, total, err := reader.Search(SearchParams{Query: "маркерПроба", Mode: SearchModeSmart, Limit: 10})
	if err != nil || total == 0 || len(m) == 0 {
		t.Fatalf("rebuilt generation lost content: total=%d err=%v", total, err)
	}
}

// TestForceRebuildGeneration_SkipsDropWhenLiveReaderHolds is the serve-path
// counterpart of TestReindex_DoesNotWipeLiveGeneration: when a co-located process is
// actively serving this exact generation, ForceRebuildGeneration MUST NOT drop it
// (on Windows the mmap-held shards cannot be deleted; on unix deleting them would
// corrupt the holder's view). The drop is skipped, BuildGeneration no-ops on the
// still-READY gensig, the call succeeds, and the held reader keeps answering.
func TestForceRebuildGeneration_SkipsDropWhenLiveReaderHolds(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Держим/Ext/ObjectModule.bsl",
		"Процедура Держим()\n\t// маркерДержим\nКонецПроцедуры\n")

	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}

	// A live reader holds the current generation read-only (registers in readers/).
	reader, err := OpenGenerationReadOnly(dir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()
	<-reader.Done()
	if err := reader.BuildError(); err != nil {
		t.Fatalf("reader build error: %v", err)
	}

	cpath, _ := cachePath(dir, cacheDir)
	genDir := generationDir(cpath, gensig)
	readyPath := readySentinelPath(genDir)
	before, err := os.Stat(readyPath)
	if err != nil {
		t.Fatalf("stat READY before: %v", err)
	}
	// A probe inside the held generation must SURVIVE (the drop is skipped).
	probe := filepath.Join(genDir, "held-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatalf("plant probe: %v", err)
	}

	// Force-rebuild while the reader holds the generation: the drop is skipped and
	// BuildGeneration no-ops, so this succeeds without touching the live generation.
	if err := ForceRebuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("ForceRebuildGeneration errored while a reader held the gen: %v", err)
	}

	if _, err := os.Stat(probe); err != nil {
		t.Fatal("ForceRebuildGeneration dropped a generation a live reader holds " +
			"(probe gone) — it must never yank an in-use generation")
	}
	after, err := os.Stat(readyPath)
	if err != nil {
		t.Fatalf("stat READY after (generation was WIPED): %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("ForceRebuildGeneration rewrote the live generation (READY mtime changed)")
	}

	// The held reader still answers from its untouched generation.
	m, total, err := reader.Search(SearchParams{Query: "маркерДержим", Mode: SearchModeSmart, Limit: 10})
	if err != nil || total == 0 || len(m) == 0 {
		t.Fatalf("held reader lost content after force-rebuild: total=%d err=%v", total, err)
	}
}
