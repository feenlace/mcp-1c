package dump

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// migration_safety_test.go covers the three defects the v3 -> v4 schema bump turns
// from latent into certain.
//
// The bump itself stays: without it the anchor fix never reaches the wrongly
// rooted users it exists for (see dumpIndexSchemaVersion and
// TestAWarmGenerationReplaysItsPersistedDocIDs). What it also does is make the
// v3 -> v4 transition, which had never happened before, happen on 100% of
// installations at once. Every path that was merely reachable becomes a path
// everybody takes.

// migWriteModule writes one .bsl under dir.
func migWriteModule(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// migCachePath resolves the per-dump cache dir.
func migCachePath(t *testing.T, dumpDir, cacheDir string) string {
	t.Helper()
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	return cpath
}

// migOpen opens an index and waits for it, failing on a build error.
func migOpen(t *testing.T, dumpDir, cacheDir string, reindex bool) *Index {
	t.Helper()
	idx, err := NewIndex(dumpDir, cacheDir, reindex)
	if err != nil {
		t.Fatal(err)
	}
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		idx.Close()
		t.Fatalf("build: %v", err)
	}
	return idx
}

// ---------------------------------------------------------------------------
// R5: an unreadable manifest read as NOT stale
// ---------------------------------------------------------------------------

// TestUnreadableFlatManifestReadsAsStale pins the schema gate's answer for a
// manifest that is THERE and cannot be used.
//
// flatCacheSchemaStale is the only thing standing between a legacy flat cache
// built under another schema and the shards being served under this binary's
// derivation. It answered the question by reading the manifest's stamp, and
// returned "not stale" whenever it could not read one at all: an I/O error, a
// corrupt file, an incompatible manifest version. The shards are then opened and
// the names are re-derived by the CURRENT binary, so the answer becomes v4 module
// names over v3 shard docIDs. Every hit Bleve returns is then a docID GetContent
// cannot resolve, which the search reports as «файлы изменились или удалены» and
// sends the user to re-run the dump. The state is a stale cache, and it is
// reported as a changed dump.
//
// Absence keeps its old meaning. A cache with shards and no manifest at all is
// the shape a build leaves behind before it writes one, and those shards were
// written by the binary that is running.
func TestUnreadableFlatManifestReadsAsStale(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	migWriteModule(t, dumpDir, "CommonModules/Общий/Ext/Module.bsl", "Процедура Тест()\nКонецПроцедуры\n")
	if err := BuildCache(dumpDir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath := migCachePath(t, dumpDir, cacheDir)
	good, err := os.ReadFile(manifestPath(cpath))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	// CONTROL, and it fires first: an intact current-schema manifest reads as NOT
	// stale. Without it every assertion below would also pass on a function that
	// answered "stale" unconditionally, which would cold-rebuild every cache on
	// every start.
	if flatCacheSchemaStale(cpath) {
		t.Fatal("control failed: a freshly built, current-schema flat cache reads as stale")
	}

	cases := map[string]func(){
		"truncated json": func() {
			if err := os.WriteFile(manifestPath(cpath), good[:len(good)/2], 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"not json at all": func() {
			if err := os.WriteFile(manifestPath(cpath), []byte("\x00\x01not a manifest"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"unreadable file": func() {
			// WriteFile applies its mode only when it CREATES the file, so the mode
			// has to be set separately or this case silently degrades into "an intact
			// manifest", which is what it did first time round.
			if err := os.WriteFile(manifestPath(cpath), good, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(manifestPath(cpath), 0o000); err != nil {
				t.Fatal(err)
			}
		},
		"incompatible manifest version": func() {
			bumped := strings.Replace(string(good), `"v":1`, `"v":999`, 1)
			if bumped == string(good) {
				t.Fatalf("could not rewrite the manifest version in %.80s", good)
			}
			if err := os.WriteFile(manifestPath(cpath), []byte(bumped), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			break_()
			t.Cleanup(func() {
				_ = os.Chmod(manifestPath(cpath), 0o644)
				if err := os.WriteFile(manifestPath(cpath), good, 0o644); err != nil {
					t.Fatal(err)
				}
			})
			// The manifest really is unusable: LoadManifest gives back nothing
			// this binary could gate on.
			m, lerr := LoadManifest(cpath)
			if lerr == nil && m != nil {
				t.Fatalf("control failed: the manifest is still usable (%d files), so the "+
					"gate below is not being asked the question this test is about", len(m.Files))
			}
			if !flatCacheSchemaStale(cpath) {
				t.Error("an unusable manifest reads as NOT stale, so shards of unknown " +
					"provenance are served under this binary's derivation")
			}
		})
	}

	// The other direction, and it is the reason "unusable" and "absent" are not
	// one answer: no manifest at all is not evidence of a foreign schema.
	if err := os.Remove(manifestPath(cpath)); err != nil {
		t.Fatal(err)
	}
	if flatCacheSchemaStale(cpath) {
		t.Error("a cache with NO manifest reads as stale; that is the shape a build " +
			"leaves before it writes one, and its shards are this binary's own")
	}
}

// TestUnusableFlatManifestIsDroppedAndRebuilt is the same fact one level up: the
// open ACTS on the gate rather than merely computing it.
func TestUnusableFlatManifestIsDroppedAndRebuilt(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	migWriteModule(t, dumpDir, "CommonModules/Общий/Ext/Module.bsl", "Процедура Тест()\nКонецПроцедуры\n")
	if err := BuildCache(dumpDir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath := migCachePath(t, dumpDir, cacheDir)
	if err := os.WriteFile(manifestPath(cpath), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := migOpen(t, dumpDir, cacheDir, false)
	defer idx.Close()

	// A usable, current-schema manifest is back, which can only have come from a
	// rebuild: the corrupt one is not repairable in place.
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("after the open the flat manifest is still unusable: m=%v err=%v", m, err)
	}
	if m.schemaVersion() != dumpIndexSchemaVersion {
		t.Errorf("rebuilt manifest is stamped schema %d, want %d", m.schemaVersion(), dumpIndexSchemaVersion)
	}
	// And the index answers, so the drop did not cost the user their search.
	matches, _, err := idx.SearchWithStats(SearchParams{Query: "Процедура", Mode: SearchModeExact, Limit: 10})
	if err != nil {
		t.Fatalf("search after the rebuild: %v", err)
	}
	if len(matches) == 0 {
		t.Error("the rebuilt index finds nothing, so the drop broke the cache instead of refreshing it")
	}
}

// ---------------------------------------------------------------------------
// R23: --reindex leaves the legacy flat cache behind
// ---------------------------------------------------------------------------

// TestReindexDiscardsTheFlatCacheItWasAskedToDiscard is the durability of
// --reindex.
//
// The generation-aware reindex builds a fresh immutable generation and never
// touches g/, which is the safety property it was written for. What it also never
// touched is the LEGACY FLAT CACHE sitting beside g/ under the same per-dump cache
// dir, and that flat cache is what NewIndex's warm-start path opens on the next
// start. So a user who ran --reindex to get rid of a poisoned cache got a correct
// generation nobody reads and the poisoned cache back on the following start.
//
// MEASURED below: the flat manifest is rewritten with a docID this derivation
// would never produce, exactly as TestAWarmGenerationReplaysItsPersistedDocIDs
// does for a generation, and the poisoned name is what the index serves again
// after a --reindex.
func TestReindexDiscardsTheFlatCacheItWasAskedToDiscard(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	migWriteModule(t, dumpDir, "CommonModules/Общий/Ext/Module.bsl", "Процедура Тест()\nКонецПроцедуры\n")
	if err := BuildCache(dumpDir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath := migCachePath(t, dumpDir, cacheDir)

	const poison = "ЛОЖНЫЙ.Ключ.Модуль"
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: m=%v err=%v", m, err)
	}
	for rel, entry := range m.Files {
		entry.DocID = poison
		m.Files[rel] = entry
	}
	if err := m.Save(cpath); err != nil {
		t.Fatal(err)
	}

	// CONTROL: the poison is live. The flat cache really does serve the fabricated
	// name, so "the poison is gone" below cannot pass by the poison never having
	// worked.
	before := migOpen(t, dumpDir, cacheDir, false)
	if !migHasName(before, poison) {
		before.Close()
		t.Fatalf("control failed: the poisoned flat manifest is not being replayed; names=%v",
			before.ModuleNames())
	}
	before.Close()

	// The reindex.
	re := migOpen(t, dumpDir, cacheDir, true)
	if migHasName(re, poison) {
		re.Close()
		t.Errorf("the reindex itself served the poisoned name: %v", re.ModuleNames())
	}
	re.Close()

	// THE NEXT ORDINARY START. This is where it came back.
	after := migOpen(t, dumpDir, cacheDir, false)
	defer after.Close()
	if migHasName(after, poison) {
		t.Errorf("the start after --reindex serves the poisoned name again, so --reindex "+
			"discarded nothing the next start reads: %v", after.ModuleNames())
	}

	// The generation the reindex built is still there. Dropping the flat cache must
	// not have taken the immutable generations with it.
	if len(generationDirNames(t, cacheDir, dumpDir)) == 0 {
		t.Error("the reindex left no generation behind at all")
	}
}

// TestReindexLeavesAForeignFlatCacheAlone is the guard on the same change. A flat
// cache another live process has memory-mapped must not be deleted under it; that
// is the whole reason the reindex stopped doing os.RemoveAll(cpath) in the first
// place, and a fix that reintroduced the unlink would be the older, worse bug.
func TestReindexLeavesAForeignFlatCacheAlone(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	migWriteModule(t, dumpDir, "CommonModules/Общий/Ext/Module.bsl", "Процедура Тест()\nКонецПроцедуры\n")
	if err := BuildCache(dumpDir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath := migCachePath(t, dumpDir, cacheDir)

	// A serve lock naming a pid that is not ours: another process holds this cache.
	if err := os.WriteFile(filepath.Join(cpath, serveLockName), []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	// CONTROL: the lock is seen as foreign.
	if pid, present := readCacheLock(cpath); !present || pid == os.Getpid() {
		t.Fatalf("control failed: the lock is not read as another process's (pid=%d present=%v)", pid, present)
	}

	shardsBefore := cacheShardDirs(cpath)
	if len(shardsBefore) == 0 {
		t.Fatal("control failed: no flat shards to protect")
	}

	idx := migOpen(t, dumpDir, cacheDir, true)
	defer idx.Close()

	if got := cacheShardDirs(cpath); len(got) != len(shardsBefore) {
		t.Errorf("the reindex removed %d of %d flat shard dirs while another process holds "+
			"the cache lock", len(shardsBefore)-len(got), len(shardsBefore))
	}
}

// migHasName reports whether the index knows a module by exactly this name.
func migHasName(idx *Index, name string) bool {
	for _, n := range idx.ModuleNames() {
		if n == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// R24: the GC removes the other binary's generation
// ---------------------------------------------------------------------------

// TestGCKeepsTheNeighbouringBinarysGeneration is the upgrade shape the bump
// creates.
//
// A gensig folds in dumpIndexSchemaVersion, so an upgraded and a not-yet-upgraded
// binary on the same dump and the same cache compute DIFFERENT signatures. The GC
// removed every adopted generation whose signature was not the one it is serving,
// as long as no reader held it, and a binary that is not running holds nothing. So
// the two took turns deleting each other's index and each start paid a full cold
// rebuild: 21 seconds on the 13575-file corpus this branch measures against.
//
// Before the bump the two binaries agreed on the signature and this never
// happened. After it, it is what an upgrade looks like.
func TestGCKeepsTheNeighbouringBinarysGeneration(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	migWriteModule(t, dumpDir, "CommonModules/Общий/Ext/Module.bsl", "Процедура Тест()\nКонецПроцедуры\n")

	// Four generations for one dump. Two carry a foreign schema stamp, the way a
	// binary on the other side of the bump writes them; two carry this binary's.
	oldForeign := migBuildGen(t, dumpDir, cacheDir, "aaaaaaaaaaaaaaa1")
	migStampGeneration(t, dumpDir, cacheDir, oldForeign, dumpIndexSchemaVersion-1)
	newForeign := migBuildGen(t, dumpDir, cacheDir, "bbbbbbbbbbbbbbb2")
	migStampGeneration(t, dumpDir, cacheDir, newForeign, dumpIndexSchemaVersion-1)
	superseded := migBuildGen(t, dumpDir, cacheDir, "ccccccccccccccc3")
	mine, err := GenSig(dumpDir)
	if err != nil {
		t.Fatal(err)
	}
	migBuildGen(t, dumpDir, cacheDir, mine)

	cpath := migCachePath(t, dumpDir, cacheDir)
	// CONTROLS, both directions, before anything is removed. Without them a stamp
	// that never actually landed on disk would make "kept" and "removed" below
	// agree with the code for the wrong reason.
	if got := generationStamp(generationDir(cpath, newForeign)); got == currentGenStamp() {
		t.Fatalf("control failed: the foreign generation is stamped with THIS binary's "+
			"schema (%+v), so nothing about it is foreign", got)
	}
	if got := generationStamp(generationDir(cpath, superseded)); got != currentGenStamp() {
		t.Fatalf("control failed: this binary's own superseded generation is stamped %+v, "+
			"want %+v", got, currentGenStamp())
	}

	removed, err := GCGenerations(dumpDir, cacheDir, mine)
	if err != nil {
		t.Fatalf("GCGenerations: %v", err)
	}

	left := generationDirNames(t, cacheDir, dumpDir)
	sort.Strings(left)
	has := func(g string) bool {
		for _, l := range left {
			if l == g {
				return true
			}
		}
		return false
	}
	if !has(mine) {
		t.Errorf("the GC removed the generation it was told to keep (%s); left=%v", mine, left)
	}
	if !has(newForeign) {
		t.Errorf("the GC removed a generation built under another index schema (%s). That is "+
			"the one a binary on the other side of the bump serves from, and deleting it makes "+
			"every one of its starts a cold rebuild. left=%v removed=%v",
			newForeign, left, removed)
	}
	// The GC is NOT switched off. This binary's own superseded generation is the
	// ordinary case it exists to reclaim, and a rule that kept that too would double
	// every cache dir for nothing.
	if has(superseded) {
		t.Errorf("the GC kept this binary's OWN superseded generation (%s), so nothing is "+
			"ever reclaimed; left=%v", superseded, left)
	}
	// And foreign generations do not accumulate: only the newest per stamp is kept.
	if has(oldForeign) {
		t.Errorf("the GC kept BOTH foreign generations (%s and %s), so a peer that rebuilds "+
			"grows this cache without bound; left=%v", oldForeign, newForeign, left)
	}
}

// migBuildGen builds and adopts a generation under an explicit gensig and returns
// it. The READY sentinel's timestamp orders same-stamp generations, so each build
// is separated far enough to be ordered on a coarse filesystem clock.
func migBuildGen(t *testing.T, dumpDir, cacheDir, gensig string) string {
	t.Helper()
	if err := BuildGeneration(dumpDir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration(%s): %v", gensig, err)
	}
	cpath := migCachePath(t, dumpDir, cacheDir)
	if !generationReadyDir(generationDir(cpath, gensig)) {
		t.Fatalf("generation %s is not READY", gensig)
	}
	time.Sleep(1100 * time.Millisecond)
	return gensig
}

// migStampGeneration rewrites a generation's manifest to declare another index
// schema, which is what a binary on the other side of the bump leaves behind.
func migStampGeneration(t *testing.T, dumpDir, cacheDir, gensig string, schema int) {
	t.Helper()
	genDir := generationDir(migCachePath(t, dumpDir, cacheDir), gensig)
	m, err := LoadManifest(genDir)
	if err != nil || m == nil {
		t.Fatalf("generation %s carries no readable manifest to restamp: m=%v err=%v", gensig, m, err)
	}
	m.SchemaVersion = schema
	if err := m.Save(genDir); err != nil {
		t.Fatal(err)
	}
}
