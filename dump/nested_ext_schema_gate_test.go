package dump

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// THE FIX HAS TO REACH AN INSTALLATION THAT ALREADY HAS A CACHE, and nothing in
// the code change makes that happen by itself.
//
// A docID is PERSISTED. buildManifest writes one per file, and the unchanged half
// of a warm start reads it back out of the manifest instead of re-deriving it from
// the path, so the key an installation serves is the key its cache was built with.
// genSig hashes the relpath, mtime and size of every .bsl plus the three version
// integers and nothing else, so neither shipping a binary that reads a manifest
// differently nor the arrival of the manifest itself moves a signature. The lever
// this branch has is dumpIndexSchemaVersion: flatCacheSchemaStale compares it
// against the stamp in the manifest, and genSig folds it into the name of a
// generation directory.
//
// WHAT THIS FILE MEASURES IS THE GATE, NOT THE NUMBER. Asserting
// dumpIndexSchemaVersion == 8 puts one literal against another and stays green
// with the gate ripped out. Every arm below stamps a cache at
// dumpIndexSchemaVersion-1, whatever the current version is, and asks what the
// running binary does with it.
//
// AND IT MEASURES IT ON THE TREE OF ISSUE 46, which is not incidental. On a dump
// whose keys are the same on both sides of the bump, a green says a cache was
// rebuilt and says nothing about what the rebuild delivered. Here the two versions
// disagree about the key, so these arms can assert WHICH key is served rather than
// only that something on disk changed.
//
// HOW THE PRE-FIX CACHE IS MANUFACTURED, stated because it is not what an upgrade
// does. The flat arms below build the cache with the extension's manifest ABSENT
// and write it afterwards. The old binary is not run; what is reproduced is the
// on-disk state it left, a manifest whose stored docID for this path is the base
// configuration's key. That key is PRODUCED by the shipped derivation rather than
// written into a manifest by hand, which is the reason for taking this route
// instead of rewriting a docID directly.
//
// The base configuration's own module is deliberately not in the tree. With it
// present the pre-fix state is a real collision, and which of two colliding files
// ends up in pathByName is decided by Go map iteration order on every start but
// the cold one (mkIssue46ParityTree says the same at length). One module keeps the
// question to the key.

// mkIssue46WrappedTree writes the nested extension of issue 46 and no other
// module. The root always carries the base configuration's manifest; the
// extension's own is written only if withManifest.
func mkIssue46WrappedTree(t *testing.T, withManifest bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	if withManifest {
		mkIssue46ExtManifest(t, root)
	}
	mkBSLFile(t, root, issue46ExtPrefix+"/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
		issue46ExtBody)
	return root
}

// mkIssue46ExtManifest writes the nested extension's own Configuration.xml.
func mkIssue46ExtManifest(t *testing.T, root string) {
	t.Helper()
	mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(issue46ExtPrefix)),
		extManifestClassic, issue46ExtName)
}

// staleFlatCacheOfIssue46 builds a flat cache holding the PRE-FIX key for the
// nested extension, then makes the tree the real one by writing the extension's
// manifest. It returns the root, the cache dir and the cache path, with the
// manifest still stamped at whatever BuildCache wrote.
func staleFlatCacheOfIssue46(t *testing.T) (root, cacheDir, cpath string) {
	t.Helper()
	root = mkIssue46WrappedTree(t, false)
	cacheDir = t.TempDir()

	if err := BuildCache(root, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath, err := cachePath(root, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	sigBefore, err := GenSig(root)
	if err != nil {
		t.Fatal(err)
	}

	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: m=%v err=%v", m, err)
	}
	pre := docIDsFromManifest(m)
	if !slices.Contains(pre, issue46BaseKey) || slices.Contains(pre, issue46ExtKey) {
		t.Fatalf("premise broken: a cache built with no extension manifest persists %v, want "+
			"the base configuration's key %q and not the namespaced %q. Every assertion below "+
			"is about the difference between those two keys.", pre, issue46BaseKey, issue46ExtKey)
	}

	mkIssue46ExtManifest(t, root)

	sigAfter, err := GenSig(root)
	if err != nil {
		t.Fatal(err)
	}
	if sigAfter != sigBefore {
		t.Fatalf("GenSig moved from %s to %s when a file that is not a .bsl appeared. That it "+
			"does not move is the whole reason the schema version has to carry this fix.",
			sigBefore, sigAfter)
	}
	return root, cacheDir, cpath
}

// dropMarker writes a file the drop path removes and the reuse path leaves alone,
// so the two are told apart by an artifact on disk rather than by a log line.
func dropMarker(t *testing.T, cpath string) string {
	t.Helper()
	marker := filepath.Join(cpath, "reuse-marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return marker
}

// TestASchemaStaleFlatCacheIsRebuiltAndDeliversTheNestedExtensionKey is the flat
// half of the gate, asked of a tree whose key the fix changes.
func TestASchemaStaleFlatCacheIsRebuiltAndDeliversTheNestedExtensionKey(t *testing.T) {
	root, cacheDir, cpath := staleFlatCacheOfIssue46(t)

	// Stamp the cache one schema version back.
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: m=%v err=%v", m, err)
	}
	m.SchemaVersion = dumpIndexSchemaVersion - 1
	if err := m.Save(cpath); err != nil {
		t.Fatalf("save stale-stamped manifest: %v", err)
	}
	marker := dropMarker(t, cpath)

	idx, err := NewIndex(root, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the reopen finished: %v", err)
	}

	// ARTIFACT 1: the flat cache was emptied, so a rebuild really ran.
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Errorf("the schema-stale flat cache was reused: the marker survived (stat err=%v). "+
			"Nothing below can mean a rebuild if this one holds.", statErr)
	}

	// ARTIFACT 2: a manifest is on disk, re-stamped current and carrying the
	// NAMESPACED key.
	//
	// Waited for by file rather than read straight after readiness: the rebuilt
	// manifest is saved after the readiness flip. The wait cannot manufacture a
	// pass. It waits for existence only, and the stale-stamped manifest is deleted
	// synchronously inside NewIndex, so a cache that had been REUSED would leave
	// that old file in place, waitManifest would return it, and both checks below
	// would fail on it.
	reM := waitManifest(t, cpath, 60*time.Second)
	if reM.schemaVersion() != dumpIndexSchemaVersion {
		t.Errorf("rebuilt manifest is stamped schema %d, want the current %d",
			reM.schemaVersion(), dumpIndexSchemaVersion)
	}
	post := docIDsFromManifest(reM)
	if !slices.Contains(post, issue46ExtKey) {
		t.Errorf("the rebuilt manifest persists %v, want the namespaced key %q: the cache was "+
			"dropped but what replaced it does not carry the fix", post, issue46ExtKey)
	}
	if slices.Contains(post, issue46BaseKey) {
		t.Errorf("the rebuilt manifest still persists the pre-fix key %q: %v", issue46BaseKey, post)
	}

	// ARTIFACT 3: and that is what the index answers with, bytes included. A key
	// that resolves to the wrong file would satisfy the two checks above.
	body, ok := idx.GetContent(issue46ExtKey)
	if !ok {
		t.Fatalf("GetContent(%q) = not found after the rebuild. Names: %v",
			issue46ExtKey, idx.ModuleNames())
	}
	if body != issue46ExtBody {
		t.Errorf("GetContent(%q) returned %q, want the extension's own bytes %q",
			issue46ExtKey, body, issue46ExtBody)
	}
	if _, stillThere := idx.GetContent(issue46BaseKey); stillThere {
		t.Errorf("the pre-fix key %q still resolves after the rebuild, so the extension's "+
			"module is still reachable as the base configuration's", issue46BaseKey)
	}
}

// TestACurrentStampedFlatCacheReplaysThePreFixKey is the other half of the same
// measurement, and it is what makes the arm above about the GATE rather than about
// reopening an index.
//
// Same tree, same manufactured pre-fix cache, one difference: the stamp is left
// CURRENT. The cache is then reused, its stored docID is replayed, and the running
// binary answers with the base configuration's key for a module that belongs to an
// extension. That is the state an installation holding a warm cache would have been
// left in had this branch shipped its code change without the version bump.
func TestACurrentStampedFlatCacheReplaysThePreFixKey(t *testing.T) {
	root, cacheDir, cpath := staleFlatCacheOfIssue46(t)

	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: m=%v err=%v", m, err)
	}
	if m.schemaVersion() != dumpIndexSchemaVersion || m.zapVersion() != zapSegmentVersion {
		t.Fatalf("premise broken: a freshly built cache is stamped schema %d zap %d, want the "+
			"current %d / %d, so this control would not be reusing a current cache",
			m.schemaVersion(), m.zapVersion(), dumpIndexSchemaVersion, zapSegmentVersion)
	}
	marker := dropMarker(t, cpath)

	idx, err := NewIndex(root, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the reopen finished: %v", err)
	}

	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("the current-stamped cache was DROPPED (marker gone: %v). This control says "+
			"something only if the cache was reused.", statErr)
	}

	names := idx.ModuleNames()
	if !slices.Contains(names, issue46BaseKey) {
		t.Errorf("a reused cache serves %v, want the pre-fix key %q it has stored: if the warm "+
			"path re-derived instead of replaying, the bump would be a cold rebuild nobody needs",
			names, issue46BaseKey)
	}
	if slices.Contains(names, issue46ExtKey) {
		t.Errorf("a reused cache serves the namespaced key %q, so the code change reached an "+
			"un-rebuilt cache on its own and the arm above is measuring nothing: %v",
			issue46ExtKey, names)
	}
}

// TestASchemaStaleGenerationIsNotAdoptedAndTheRebuildCarriesTheFix is the same
// question asked of the other cache shape. A generation is selected by its gensig,
// which folds the schema version in, so a stale one is not rejected on inspection:
// the running binary computes a different signature and never looks for it.
func TestASchemaStaleGenerationIsNotAdoptedAndTheRebuildCarriesTheFix(t *testing.T) {
	root := mkIssue46WrappedTree(t, true)
	cacheDir := t.TempDir()

	oldSig, err := genSig(root, dumpIndexSchemaVersion-1, zapSegmentVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := BuildGeneration(root, cacheDir, oldSig); err != nil {
		t.Fatalf("building the generation of the previous schema version: %v", err)
	}
	// PREMISE: it really is on disk and adoptable on its own terms, or the
	// assertions below would hold because nothing was ever built.
	if !GenerationReady(root, cacheDir, oldSig) {
		t.Fatalf("premise broken: the generation stamped one version back is not READY, so " +
			"there is nothing here for the bump to leave behind")
	}
	before := generationDirNames(t, cacheDir, root)
	if !slices.Equal(before, []string{oldSig}) {
		t.Fatalf("premise broken: generations on disk = %v, want exactly the stale %s",
			before, oldSig)
	}

	newSig, err := GenSig(root)
	if err != nil {
		t.Fatal(err)
	}
	if newSig == oldSig {
		t.Fatalf("the schema version is not folded into the gensig: one version apart both "+
			"give %s, so a warm generation would be adopted straight across the bump", newSig)
	}
	if GenerationReady(root, cacheDir, newSig) {
		t.Fatalf("a generation for the current gensig %s is READY after only the stale one was "+
			"built; the bump did not force a rebuild", newSig)
	}

	if err := BuildGeneration(root, cacheDir, newSig); err != nil {
		t.Fatal(err)
	}
	// ARTIFACT: a second generation directory, beside the one it did not touch.
	after := generationDirNames(t, cacheDir, root)
	if len(after) != 2 || !slices.Contains(after, newSig) || !slices.Contains(after, oldSig) {
		t.Fatalf("generations on disk = %v, want the stale %s and a freshly built %s beside it",
			after, oldSig, newSig)
	}

	// AND THE REBUILD CARRIES THE FIX. A new directory on disk is not the claim;
	// the claim is that what an installation opens after the bump holds the
	// namespaced key.
	idx, err := OpenGenerationReadOnly(root, cacheDir, newSig)
	if err != nil {
		t.Fatalf("OpenGenerationReadOnly(%s): %v", newSig, err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the read-only open finished: %v", err)
	}
	names := idx.ModuleNames()
	if !slices.Contains(names, issue46ExtKey) {
		t.Errorf("the rebuilt generation serves %v, want the namespaced key %q", names, issue46ExtKey)
	}
	if slices.Contains(names, issue46BaseKey) {
		t.Errorf("the rebuilt generation still serves the pre-fix key %q: %v", issue46BaseKey, names)
	}
}

// issue46SearchTerm is a word the nested extension's module body carries, used to
// ask the SHARD-BACKED search whether the cache under it answers for this tree.
// Taken from issue46ExtBody rather than written out beside it, so a body that
// stops carrying the word cannot leave the arms below querying for something no
// file holds.
const issue46SearchTerm = "расширение"

// AN INTERRUPTED DROP IS THE ONE THAT MATTERS, because the gate's own answer to
// the state it leaves behind is «reuse».
//
// removeFlatCacheContents walks os.ReadDir(cpath), which returns entries sorted by
// name, so "manifest.json" is reached before any "shard_*" directory. A drop that
// stops partway therefore leaves shards standing with no manifest beside them, and
// flatCacheSchemaStale answers FALSE for exactly that shape, deliberately and for a
// documented reason: shards with no manifest are what a build leaves behind before
// it writes one, and dropping them would be a gratuitous cold rebuild.
//
// What follows from there is not a slower start but a dead search.
// loadFromManifestAndDiff's manifest==nil branch walks the dump for NAMES, saves a
// FRESH manifest stamped CURRENT, and indexes nothing into the shards. The docIDs
// in those shards stay the ones the foreign build wrote, so a hit on a key the
// bump moved is one GetContent cannot resolve and it is dropped as unreadable. On
// the tree below the bump moved the only key there is, so the answer is empty; and
// the current stamp the walk just wrote means the gate never fires again, so it is
// empty on every start after that one too. GetContent still serves a module asked
// for by name, so the dump is UNSEARCHABLE rather than unreadable, which is why
// nothing about the state looks broken.
//
// SO THE ORDER IS THE FIX: the shards go first, the manifest goes last, and the
// manifest goes only if every shard went. A shard whose removal FAILED must keep
// the manifest beside it, because that manifest is the only surviving evidence
// that those shards are foreign.
func TestAnInterruptedFlatCacheDropIsRetriedRatherThanServed(t *testing.T) {
	if !strings.Contains(issue46ExtBody, issue46SearchTerm) {
		t.Fatalf("premise broken: the extension body %q does not carry %q, so a search for it "+
			"measures nothing", issue46ExtBody, issue46SearchTerm)
	}

	// CONTROL. Same tree, same foreign stamp, no interruption: the gate drops the
	// cache whole and the cold rebuild answers the query. It is what makes the arm
	// below about the INTERRUPTION rather than about the stamp — a green there has
	// to be reachable without one, or the arm is only measuring that a stale cache
	// is rebuilt.
	t.Run("control: the same stamp with no interruption", func(t *testing.T) {
		root, cacheDir, cpath := staleFlatCacheOfIssue46(t)
		stampFlatCacheOneSchemaBack(t, cpath)
		if !flatCacheSchemaStale(cpath) {
			t.Fatalf("premise broken: a cache stamped one schema version back is not seen as " +
				"stale, so this control drops nothing")
		}
		assertIssue46IsSearchable(t, root, cacheDir, "the first start")
		assertIssue46IsSearchable(t, root, cacheDir, "the reopen after it")
	})

	t.Run("the interrupted drop", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the interruption is injected by clearing the write bit on a shard directory, " +
				"which is not what stops an unlink on Windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("root ignores the directory permission this test injects the failure with")
		}

		root, cacheDir, cpath := staleFlatCacheOfIssue46(t)
		stampFlatCacheOneSchemaBack(t, cpath)
		if !flatCacheSchemaStale(cpath) {
			t.Fatalf("premise broken: a cache stamped one schema version back is not seen as " +
				"stale, so there is nothing here for an interrupted drop to interrupt")
		}

		// THE INTERRUPTION. A shard directory whose children cannot be unlinked
		// stands in for the process that died between two entries: the removal of
		// that shard fails and every other entry is removed as usual. It is the same
		// end state — a shard the drop did not get, with the drop having run — and it
		// covers the per-entry failure (EBUSY, an open handle) in the same shot,
		// which the interruption alone would not.
		//
		// EVERY DIRECTORY IN THE SUBTREE HAS TO LOSE THE WRITE BIT, not just the top
		// one, and the difference is not cosmetic. MEASURED here first: clearing it
		// on shard_0 alone leaves its store/ subdirectory writable, so os.RemoveAll
		// descends and empties store/ while index_meta.json survives. That shard is
		// then not preserved but CORRUPTED, the next open fails on it, and the
		// corrupt-cache recovery drops the cache and cold-rebuilds — so the search
		// assertions below went green over a shard that had been destroyed by the
		// injection rather than kept by the fix. The inventory premise below is what
		// makes that impossible to repeat.
		shardDirs := cacheShardDirs(cpath)
		if len(shardDirs) == 0 {
			t.Fatalf("premise broken: the flat cache under %s holds no shard_* directory, so "+
				"there is no shard for the drop to fail on", cpath)
		}
		blocked := shardDirs[0]
		before := treeInventory(t, blocked)
		if len(before) == 0 {
			t.Fatalf("premise broken: the shard %s holds no files, so leaving it in place "+
				"proves nothing", blocked)
		}
		chmodDirs(t, blocked, 0o500)
		t.Cleanup(func() { chmodDirsBestEffort(blocked, 0o755) })

		removeFlatCacheContents(cpath)

		// PREMISE: the injection really did stop a removal, and stopped it WHOLE. A
		// shard that was removed anyway leaves an empty cache and the assertions
		// below measure nothing; a shard that was half removed is a corrupt cache,
		// which the recovery path drops on its own and which would make the search
		// assertions pass without the fix.
		if _, statErr := os.Stat(blocked); statErr != nil {
			t.Fatalf("premise broken: the blocked shard %s was removed anyway (stat err=%v), "+
				"so nothing was interrupted and the assertions below measure an empty cache",
				blocked, statErr)
		}
		if after := treeInventory(t, blocked); !slices.Equal(before, after) {
			t.Fatalf("premise broken: the drop changed the blocked shard %s instead of leaving "+
				"it alone.\n before: %v\n after:  %v\nA damaged shard is dropped by the "+
				"corrupt-cache recovery, so the assertions below would pass without the "+
				"ordering this test is about.", blocked, before, after)
		}

		// ASSERTION 1: the manifest outlived the shard the drop could not take, so
		// the cache still declares itself foreign and the drop will be retried.
		//
		// Errorf and not Fatalf: when this one fails the manifest is gone, which is
		// precisely the state the assertions below are about, and running them says
		// what that state costs instead of only that it was reached.
		if !flatCacheSchemaStale(cpath) {
			t.Errorf("after a drop that could not remove the shard %s, the cache no longer "+
				"reads as schema-stale. The manifest was taken while a foreign shard stayed, "+
				"so the only evidence of the foreign schema is gone and the next start will "+
				"serve those shards.", blocked)
		}

		// THE RETRY, DRIVEN THROUGH THE REAL GATE WITH THE OBSTRUCTION STILL THERE.
		// The assertion above reads the state a drop leaves; this reads what NewIndex
		// does with it. The gate has to fire a second time, the second drop has to
		// keep the manifest again, and the partial drop has to be reported rather
		// than left for an operator to infer from an empty search.
		rec := captureLogs(t)
		func() {
			idx, err := NewIndex(root, cacheDir, false)
			if err != nil {
				t.Fatalf("NewIndex over a cache whose drop is still blocked: %v", err)
			}
			defer idx.Close()
			<-idx.Done()
		}()
		if !flatCacheSchemaStale(cpath) {
			t.Errorf("after a start whose drop was blocked again, the cache no longer reads "+
				"as schema-stale, so the start after it would serve the foreign shards in %s",
				blocked)
		}
		errs := rec.atLevel(slog.LevelError)
		if !slices.ContainsFunc(errs, func(m string) bool {
			return strings.Contains(m, "did not take every shard")
		}) {
			t.Errorf("a drop that left a shard behind was not reported at ERROR. Messages at "+
				"ERROR: %v. WARN is dropped by the three default logger configurations this "+
				"binary runs --build-index and the MCP pipe launch under, so a quieter level "+
				"is the same as no report at all.", errs)
		}

		// The obstruction clears, as it does once the process that was holding the
		// shard is gone. This is the next start, and the question is what it serves.
		// Restored over the whole subtree for the same reason it was cleared over the
		// whole subtree: buildShard opens with an os.RemoveAll of the shard path, and
		// one directory left read-only deeper down fails the rebuild.
		chmodDirs(t, blocked, 0o755)

		// ASSERTION 2 and 3: the retry, and then the start after it. The second one
		// is not a repetition — the first start writes a manifest, and if it wrote a
		// current-stamped one over a foreign shard the gate can never fire again, so
		// a search that only works once is the self-sealing failure and not a fix.
		assertIssue46IsSearchable(t, root, cacheDir, "the start after the interruption")
		assertIssue46IsSearchable(t, root, cacheDir, "the reopen after that")
	})
}

// stampFlatCacheOneSchemaBack rewrites the flat manifest under cpath with its
// schema version moved one back, which is the foreign cache every arm here starts
// from. It stamps whatever the current version is rather than a literal, for the
// reason stated at the top of this file.
func stampFlatCacheOneSchemaBack(t *testing.T, cpath string) {
	t.Helper()
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest(%s): m=%v err=%v", cpath, m, err)
	}
	m.SchemaVersion = dumpIndexSchemaVersion - 1
	if err := m.Save(cpath); err != nil {
		t.Fatalf("saving the stale-stamped manifest: %v", err)
	}
}

// assertIssue46IsSearchable opens the index for real and asks the SHARD-BACKED
// search for a word the tree carries.
//
// The mode is smart on purpose. Regex and exact scan the files behind idx.names,
// which a warm start re-derives from the dump, so both answer correctly over
// shards that hold nothing of the kind; smart is the only mode whose answer comes
// out of the shard the drop was supposed to remove. It is also what search_code
// runs by default.
func assertIssue46IsSearchable(t *testing.T, root, cacheDir, when string) {
	t.Helper()
	idx, err := NewIndex(root, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex on %s: %v", when, err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError on %s: %v", when, err)
	}

	matches, stats, err := idx.SearchWithStats(SearchParams{Query: issue46SearchTerm})
	if err != nil {
		t.Fatalf("SearchWithStats on %s: %v", when, err)
	}
	if len(matches) == 0 {
		t.Errorf("on %s a search for %q returned no match at all: stats %+v, modules served %v. "+
			"The dump holds the word, so the shards being searched are not this tree's.",
			when, issue46SearchTerm, stats, idx.ModuleNames())
		return
	}
	if matches[0].Module != issue46ExtKey {
		t.Errorf("on %s a search for %q answered with the module %q, want the namespaced key %q",
			when, issue46SearchTerm, matches[0].Module, issue46ExtKey)
	}
}

// treeInventory lists every file under root as "relpath size", sorted, so an
// injection that was meant to PRESERVE a directory can be shown to have preserved
// it rather than assumed to have.
func treeInventory(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		out = append(out, rel+" "+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	slices.Sort(out)
	return out
}

// chmodDirs sets mode on root and on every directory beneath it. Unlinking a file
// needs the write bit on its PARENT directory, so clearing it on the top directory
// alone stops nothing deeper than one level.
func chmodDirs(t *testing.T, root string, mode os.FileMode) {
	t.Helper()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		return os.Chmod(p, mode)
	})
	if err != nil {
		t.Fatalf("chmod %s to %o: %v", root, mode, err)
	}
}

// chmodDirsBestEffort is chmodDirs for a t.Cleanup, where a failure to restore a
// mode must not be reported as a test failure of its own.
func chmodDirsBestEffort(root string, mode os.FileMode) {
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil //nolint:nilerr // best-effort restore
		}
		_ = os.Chmod(p, mode)
		return nil
	})
}
