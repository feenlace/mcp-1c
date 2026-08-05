package dump

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// corruptFlatCacheBesideAnArena builds the state the recovery path is written for
// and the one it was never measured on: an immutable generation in g/ AND a
// legacy flat cache beside it whose shards cannot be opened, under a manifest
// that is readable and stamped with the CURRENT schema.
//
// The stamp matters. Of the three ways a flat cache can fail to open, two are
// already answered before the recovery branch is reached: an unreadable manifest
// is answered by flatCacheSchemaStale (R5 on this branch) and a foreign stamp by
// the schema gate, and BOTH of those drop the flat cache alone. Only the third,
// a readable current-stamped manifest over shards that do not open, reaches the
// branch under test, which is why a fixture that skipped the stamp would test
// the two paths that were already right.
//
// Nothing here is faked: the generation is built by BuildGeneration and the flat
// cache by BuildCache, so the layout is whatever the shipped code produces.
func corruptFlatCacheBesideAnArena(t *testing.T) (dumpDir, cacheDir, cpath, gensig string) {
	t.Helper()
	dumpDir = t.TempDir()
	cacheDir = t.TempDir()
	mkBSLFile(t, dumpDir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ) КонецПроцедуры\n")
	mkBSLFile(t, dumpDir, "CommonModules/ОбщийМодуль/Ext/Module.bsl",
		"Функция Версия() Возврат \"1\"; КонецФункции\n")

	var err error
	gensig, err = GenSig(dumpDir)
	if err != nil {
		t.Fatalf("GenSig: %v", err)
	}
	if err := BuildGeneration(dumpDir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	if !GenerationReady(dumpDir, cacheDir, gensig) {
		t.Fatalf("premise broken: no READY generation was built, so there is no arena to preserve")
	}

	if err := BuildCache(dumpDir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath, err = cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	shardDirs := cacheShardDirs(cpath)
	if len(shardDirs) == 0 {
		t.Fatalf("premise broken: no flat shards under %s, so the flat-cache branch is not reached", cpath)
	}

	// Garble what every scorch shard opens through, and nothing else. The
	// manifest is left exactly as BuildCache wrote it.
	for _, d := range shardDirs {
		for _, f := range []string{filepath.Join(d, "store", "root.bolt"), filepath.Join(d, "index_meta.json")} {
			if _, err := os.Stat(f); err != nil {
				continue
			}
			if err := os.WriteFile(f, []byte("CORRUPT-NOT-A-BOLT-FILE"), 0o644); err != nil {
				t.Fatalf("corrupting %s: %v", f, err)
			}
		}
	}

	if flatCacheSchemaStale(cpath) {
		t.Fatalf("premise broken: the schema gate claims this cache, so the corrupt branch is never reached")
	}
	shards, err := openCachedShards(cacheShardDirs(cpath), false, "")
	if err == nil {
		for _, s := range shards {
			s.Close()
		}
		t.Fatalf("premise broken: the corrupted flat cache still opens, so the corrupt branch is never reached")
	}
	return dumpDir, cacheDir, cpath, gensig
}

// attrValuesAt returns the string form of every attribute named key on the
// records logged at exactly lvl.
//
// It reads ATTRIBUTES rather than the formatted message on purpose: a test that
// matched a sentence would go green on a sentence, and what an operator needs
// from a destructive recovery is the list of what actually went.
func (h *levelRecorder) attrValuesAt(lvl slog.Level, key string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level != lvl {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				out = append(out, a.Value.String())
			}
			return true
		})
	}
	return out
}

// flatCacheArtefacts lists the names directly under cpath that are NOT the
// generations subtree, i.e. everything the flat cache owns.
func flatCacheArtefacts(t *testing.T, cpath string) []string {
	t.Helper()
	ents, err := os.ReadDir(cpath)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.Name() == generationsDirName {
			continue
		}
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

// TestCorruptFlatCacheDoesNotDestroyTheGenerationsArena is the defect.
//
// The recovery for a flat cache that will not open was os.RemoveAll(cpath) over
// the WHOLE per-dump cache dir, so a bad flat shard cost every immutable
// generation beside it. The two artefacts have different lifecycles and nothing
// about a bad flat shard is evidence about a generation: a generation is written
// once, sealed with READY and never mutated, while the flat cache is opened
// read-write and rewritten by the warm-start diff.
//
// Measured with real binaries on ~/Downloads/canon_vm before the fix: one
// generation before, zero after, exit code 0.
func TestCorruptFlatCacheDoesNotDestroyTheGenerationsArena(t *testing.T) {
	dumpDir, cacheDir, cpath, gensig := corruptFlatCacheBesideAnArena(t)

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	<-idx.Done()
	defer idx.Close()

	if !GenerationReady(dumpDir, cacheDir, gensig) {
		t.Fatalf("a corrupt FLAT cache destroyed the generations arena: g/%s is gone from %s "+
			"(what is left: %v). An immutable generation is a different artefact with a "+
			"different lifecycle; recovering the flat cache must cost the flat cache.",
			gensig, cpath, flatCacheArtefacts(t, cpath))
	}
}

// TestCorruptFlatCacheRecoveryNamesWhatItRemoved is the second half of the
// defect and the reason it survived: the process removed the cache and exited 0
// saying it had built one. Silence is what makes a destructive recovery
// undiscoverable, so the removal has to be reported at a severity an operator
// scanning for trouble actually sees.
//
// The assertion is on a DERIVED list, not on a sentence. The message carries the
// names removeFlatCacheContents actually unlinked, so a message that merely
// claims a removal cannot satisfy it, and a recovery that started deleting the
// arena again would have to say so in its own log line.
//
// THE LEVEL IS PART OF THE ASSERTION and it is slog.Error, because
// cmd/mcp-1c/main.go throws away anything below it in three of its five logging
// configurations (this said two of four, and both halves were short by one; the
// resolving command is in noteRetainedBuildDir's doc comment): the
// early default that the whole of `--build-index` runs under, the MCP pipe launch,
// and the devnull fallback that launch takes when it cannot open its stderr log.
// Those are the three in which this line is the operator's only evidence, and the
// offline --build-index is the very command the arena loss was measured on. A WARN
// here would satisfy a reader of this source and reach nobody.
func TestCorruptFlatCacheRecoveryNamesWhatItRemoved(t *testing.T) {
	dumpDir, cacheDir, cpath, _ := corruptFlatCacheBesideAnArena(t)
	before := flatCacheArtefacts(t, cpath)
	if !slices.Contains(before, manifestFileName) {
		t.Fatalf("premise broken: no %s under %s before the recovery (have %v)",
			manifestFileName, cpath, before)
	}

	rec := captureLogs(t)
	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	<-idx.Done()
	defer idx.Close()

	paths := rec.attrValuesAt(slog.LevelError, "path")
	if !slices.Contains(paths, cpath) {
		t.Fatalf("the recovery removed the flat cache under %s and no ERROR record names that "+
			"path at ERROR. ERROR paths seen: %v; ERROR messages seen: %v; WARN messages seen: %v",
			cpath, paths, rec.atLevel(slog.LevelError), rec.atLevel(slog.LevelWarn))
	}

	removed := rec.attrValuesAt(slog.LevelError, "removed")
	if len(removed) == 0 {
		t.Fatalf("no ERROR record names WHAT was removed from %s; a removal notice that does "+
			"not list the artefacts cannot tell an operator whether the generations went too. "+
			"ERROR messages seen: %v", cpath, rec.atLevel(slog.LevelError))
	}
	joined := strings.Join(removed, " ")
	if !strings.Contains(joined, manifestFileName) {
		t.Errorf("the removal notice %q does not name %s, which was on disk before the "+
			"recovery and is gone after it", joined, manifestFileName)
	}
	if !strings.Contains(joined, "shard_") {
		t.Errorf("the removal notice %q names no shard directory, though %v were removed",
			joined, before)
	}
	// The generations subtree must not be in the list, because it must not have
	// been removed. This is the assertion that distinguishes a message from a fact.
	for _, r := range removed {
		for _, name := range strings.Split(r, " ") {
			if strings.TrimSpace(name) == generationsDirName {
				t.Errorf("the removal notice names %q: the recovery removed the generations "+
					"subtree, which is the defect this test exists for", generationsDirName)
			}
		}
	}
}

// TestCorruptFlatCacheIsStillRebuilt is the negative control for both tests
// above: narrowing the blast radius must not turn recovery into a refusal. A
// flat cache that genuinely does not open still gets rebuilt, the index serves,
// and the rebuilt flat cache opens on the next start.
func TestCorruptFlatCacheIsStillRebuilt(t *testing.T) {
	dumpDir, cacheDir, cpath, _ := corruptFlatCacheBesideAnArena(t)

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("the rebuild after a corrupt flat cache failed: %v", err)
	}
	if !idx.Ready() {
		t.Fatal("the index is not ready after recovering from a corrupt flat cache")
	}
	if got := idx.ModuleCount(); got != 2 {
		t.Errorf("modules after the rebuild = %d, want 2 (the dump has two .bsl files)", got)
	}
	want := bslPathToModuleName("CommonModules/ОбщийМодуль/Ext/Module.bsl")
	if body, ok := idx.GetContent(want); !ok || !strings.Contains(body, "Функция Версия()") {
		t.Errorf("GetContent(%q) after the rebuild: ok=%v body=%q", want, ok, body)
	}
	idx.Close()

	shards, err := openCachedShards(cacheShardDirs(cpath), false, defaultBoltTimeout)
	if err != nil {
		t.Fatalf("the flat cache was not rebuilt into an openable state: %v", err)
	}
	for _, s := range shards {
		s.Close()
	}
}

// TestCorruptFlatCacheIsLeftAloneWhileAnotherProcessHoldsIt applies the rule
// e3f540c wrote down for the --reindex flat-cache drop to the recovery path,
// because the reason is identical: a flat cache another live process has
// memory-mapped must not be unlinked under it.
//
// It has to assert BOTH halves. Skipping only the unlink would leave the peer's
// shards to be destroyed a moment later anyway, because buildShardOffline starts
// every shard with os.RemoveAll(path) — a rebuild into a held cache dir deletes
// the same files by a different syscall. So the degrade is to build in memory:
// this start is simply not durable, which is the outcome the reindex precedent
// chose for the same situation.
func TestCorruptFlatCacheIsLeftAloneWhileAnotherProcessHoldsIt(t *testing.T) {
	dumpDir, cacheDir, cpath, gensig := corruptFlatCacheBesideAnArena(t)

	foreign := os.Getpid() + 1
	if err := os.WriteFile(filepath.Join(cpath, serveLockName),
		[]byte(strconv.Itoa(foreign)), 0o644); err != nil {
		t.Fatalf("writing a foreign serve lock: %v", err)
	}
	before := flatCacheArtefacts(t, cpath)

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	<-idx.Done()
	defer idx.Close()

	if after := flatCacheArtefacts(t, cpath); !slices.Equal(before, after) {
		t.Errorf("the flat cache held by pid %d changed under it: %v -> %v", foreign, before, after)
	}
	if !GenerationReady(dumpDir, cacheDir, gensig) {
		t.Errorf("the generations arena was destroyed while pid %d held the cache", foreign)
	}
	// The peer's lock is still the peer's.
	if pid, present := readCacheLock(cpath); !present || pid != foreign {
		t.Errorf("serve lock after the degrade = (pid %d, present %v), want the foreign pid %d",
			pid, present, foreign)
	}
	// And this process still serves, from memory.
	if err := idx.BuildError(); err != nil {
		t.Fatalf("the in-memory degrade failed to build: %v", err)
	}
	if !idx.Ready() || idx.ModuleCount() != 2 {
		t.Errorf("the in-memory degrade did not serve: ready=%v modules=%d", idx.Ready(), idx.ModuleCount())
	}
}

// TestFailedColdBuildDoesNotDestroyTheGenerationsArena covers the second
// os.RemoveAll(cpath) in this file, on the cold-build failure path.
//
// It is the same primitive with the same blast radius, and leaving it would
// re-open the hole the test above closes: the recovery now falls through to a
// rebuild, so a rebuild that then fails would delete the arena the recovery just
// preserved. It is also wrong in a second way — it fired on cpath even when the
// build never wrote there, which is exactly the in-memory degrade above.
func TestFailedColdBuildDoesNotDestroyTheGenerationsArena(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dumpDir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl", "Процедура П() КонецПроцедуры\n")

	gensig, err := GenSig(dumpDir)
	if err != nil {
		t.Fatalf("GenSig: %v", err)
	}
	if err := BuildGeneration(dumpDir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	if !GenerationReady(dumpDir, cacheDir, gensig) {
		t.Fatalf("premise broken: no arena to preserve")
	}

	// Fail the cold build DETERMINISTICALLY rather than by racing a cancel:
	// coLocateBuildScratch mkdir's <cpath>/.bleve-scratch-shard_N, so a regular
	// FILE at that name makes every shard builder fail with ENOTDIR before it
	// writes anything. cpath itself stays writable, so the cleanup branch's
	// removal is free to do its worst and the test measures the removal rather
	// than a permission error.
	for i := range 16 {
		blocker := filepath.Join(cpath, ".bleve-scratch-shard_"+strconv.Itoa(i))
		if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
			t.Fatalf("planting the scratch blocker: %v", err)
		}
	}

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	<-idx.Done()
	idx.Close()

	if idx.BuildError() == nil {
		t.Fatalf("premise broken: the cold build succeeded, so the failure-cleanup branch " +
			"was never reached")
	}
	if !GenerationReady(dumpDir, cacheDir, gensig) {
		t.Fatalf("a failed cold build destroyed the generations arena: g/%s is gone from %s "+
			"(what is left: %v)", gensig, cpath, flatCacheArtefacts(t, cpath))
	}
}

// TestRecoveryDoesNotAnnounceARemovalItDidNotMake.
//
// The removed= attribute was already honest: removeFlatCacheContents returns the
// names it actually unlinked and only those. The SENTENCE beside it was not. It
// read «removed a flat index cache that could not be opened» on every path,
// including the one where the list is empty and nothing at all was taken off disk.
// An operator reads prose, and the prose said the cache was gone while the
// attribute said nothing had moved.
//
// The empty case is reachable without contriving anything: a per-dump cache
// directory that holds only the generations arena has no flat artefact to remove,
// and removeFlatCacheContents skips g/ by name.
func TestRecoveryDoesNotAnnounceARemovalItDidNotMake(t *testing.T) {
	// A cache directory whose ONLY content is the arena the recovery must preserve.
	cpath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cpath, generationsDirName, "deadbeef"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := captureLogs(t)
	if !dropFlatCacheForRecovery(cpath, "test") {
		t.Fatal("the recovery refused a cache directory nothing else holds")
	}

	// PREMISE: nothing was unlinked, or the sentence under test is not the one this
	// case produces.
	removed := rec.attrValuesAt(slog.LevelError, "removed")
	if len(removed) != 1 || removed[0] != "" {
		t.Fatalf("removed= is %v, want exactly one empty value: this fixture is supposed "+
			"to reach the branch where the recovery unlinked nothing", removed)
	}
	// The arena is still there, which is the other half of "nothing was unlinked".
	if _, err := os.Stat(filepath.Join(cpath, generationsDirName, "deadbeef")); err != nil {
		t.Fatalf("the generation under g/ was removed after all: %v", err)
	}

	msgs := strings.Join(rec.atLevel(slog.LevelError), "\n")
	if strings.Contains(msgs, "removed a flat index cache") {
		t.Errorf("the recovery announced a removal it did not make, beside an empty "+
			"removed= attribute:\n%s", msgs)
	}
	if !strings.Contains(msgs, "nothing was unlinked") {
		t.Errorf("the recovery said nothing about having removed nothing:\n%s", msgs)
	}
}

// TestRecoveryStillAnnouncesARemovalItDidMake is the positive control for the test
// above, and it is a separate test so it can fail on its own.
//
// «Does not claim a removal» is satisfied by a recovery that has gone silent
// altogether, which is the defect the announcement was added for. So the same call
// over a directory that DOES carry a flat artefact must still say so and still name
// it.
func TestRecoveryStillAnnouncesARemovalItDidMake(t *testing.T) {
	cpath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cpath, generationsDirName, "deadbeef"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cpath, "shard_0"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := captureLogs(t)
	if !dropFlatCacheForRecovery(cpath, "test") {
		t.Fatal("the recovery refused a cache directory nothing else holds")
	}
	msgs := strings.Join(rec.atLevel(slog.LevelError), "\n")
	if !strings.Contains(msgs, "removed a flat index cache") {
		t.Errorf("a recovery that really did unlink a shard directory did not announce "+
			"it:\n%s", msgs)
	}
	if removed := rec.attrValuesAt(slog.LevelError, "removed"); !slices.Contains(removed, "shard_0") {
		t.Errorf("removed= is %v and does not name shard_0", removed)
	}
	if _, err := os.Stat(filepath.Join(cpath, generationsDirName, "deadbeef")); err != nil {
		t.Fatalf("the generation under g/ was removed: %v", err)
	}
}

// TestRecoveryCannotReachTheLogsBecauseTheyAreOneLevelUp pins the correction to
// removeFlatCacheContents' own doc comment, which listed «the stderr/server logs»
// among what it removes.
//
// It cannot remove them and never could. openLogFile in cmd/mcp-1c opens
// server.log and stderr.log against the CACHE DIRECTORY, and cpath is the per-dump
// subdirectory below it, so a ReadDir of cpath never sees them. The comment
// described a blast radius the function does not have, which is the same class of
// error as announcing a removal that did not happen: a sentence about a destruction
// that was not measured.
func TestRecoveryCannotReachTheLogsBecauseTheyAreOneLevelUp(t *testing.T) {
	cacheDir := t.TempDir()
	logPath := filepath.Join(cacheDir, "stderr.log")
	if err := os.WriteFile(logPath, []byte("серверный журнал\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The per-dump directory, resolved the way the production code resolves it, so
	// the "one level up" is the real relationship and not one this test invented.
	cpath, err := cachePath(t.TempDir(), cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	if filepath.Dir(cpath) != cacheDir {
		t.Fatalf("premise broken: cpath %s is not one level below the cache dir %s", cpath, cacheDir)
	}
	if err := os.MkdirAll(filepath.Join(cpath, "shard_0"), 0o755); err != nil {
		t.Fatal(err)
	}

	if !dropFlatCacheForRecovery(cpath, "test") {
		t.Fatal("the recovery refused a cache directory nothing else holds")
	}
	// PREMISE: the recovery really did run and really did remove something, or the
	// surviving log below is a log nothing was ever aimed at.
	if _, err := os.Stat(filepath.Join(cpath, "shard_0")); !os.IsNotExist(err) {
		t.Fatalf("premise broken: shard_0 survived the recovery (stat err %v)", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("the recovery removed %s, so it does reach the logs after all: %v", logPath, err)
	}
}
