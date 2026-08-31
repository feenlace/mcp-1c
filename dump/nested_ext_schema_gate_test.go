package dump

import (
	"os"
	"path/filepath"
	"slices"
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
// dumpIndexSchemaVersion == 7 puts one literal against another and stays green
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
