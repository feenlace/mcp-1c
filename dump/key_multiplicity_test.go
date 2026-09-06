package dump

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// KeyMultiplicity: the per-key sibling of the collapsed-key REPORT.
//
// CollapsedKeys/CollapsedKeyCount say HOW MUCH of a dump collapsed; they do
// not say which key a caller is holding collapsed onto or how many files
// share it. KeyMultiplicity answers that for one docID at a time (0 = the
// index does not hold this key, 1 = unique, n = n files derived it), read
// from the SAME multiset collapsedKeysOf/keyMultiplicityOf already walk, so
// the two reports can never describe two different loads.
// ---------------------------------------------------------------------------

// TestKeyMultiplicityOfCountsExactly pins the pure multiset arithmetic and the
// nil-safety of the accessor, mirroring TestCollapsedKeysZeroValueIsSafe.
func TestKeyMultiplicityOfCountsExactly(t *testing.T) {
	got := keyMultiplicityOf([]string{"a", "a", "a", "b", "c", "c"})
	want := map[string]int{"a": 3, "b": 1, "c": 2}
	if !maps.Equal(got, want) {
		t.Errorf("keyMultiplicityOf([a a a b c c]) = %v, want %v", got, want)
	}
	if n := got["никогда-не-виданный-ключ"]; n != 0 {
		t.Errorf("an absent key read off the map = %d, want the map's zero value 0", n)
	}

	if empty := keyMultiplicityOf(nil); len(empty) != 0 {
		t.Errorf("keyMultiplicityOf(nil) = %v, want an empty map", empty)
	}
	if empty := keyMultiplicityOf([]string{}); len(empty) != 0 {
		t.Errorf("keyMultiplicityOf([]string{}) = %v, want an empty map", empty)
	}

	// Nil-safety of the accessor: a nil *Index and an Index that has loaded
	// nothing both answer 0 rather than panicking on a nil atomic. Every MCP
	// tool response can read this, including one produced while a background
	// build is still running.
	var nilIdx *Index
	if n := nilIdx.KeyMultiplicity("anything"); n != 0 {
		t.Errorf("KeyMultiplicity on a nil *Index = %d, want 0", n)
	}
	fresh := &Index{}
	if n := fresh.KeyMultiplicity("anything"); n != 0 {
		t.Errorf("KeyMultiplicity on an Index that has not loaded = %d, want 0", n)
	}
}

// TestKeyMultiplicityAgreesWithCollapsedKeys proves the per-key accessor and
// the aggregate report are one arithmetic: over several multisets, summing
// (m-1) for every key with m>1 must equal CollapsedKeys().Files, and counting
// those keys must equal CollapsedKeys().Keys.
func TestKeyMultiplicityAgreesWithCollapsedKeys(t *testing.T) {
	cases := [][]string{
		{"a", "a", "a", "b", "c", "c"},
		{"x", "y", "z"},
		nil,
		{"p", "p"},
		{"к1", "к1", "к1", "к1", "к2", "к3", "к3"},
	}
	for i, names := range cases {
		idx := bareIndex(t, t.TempDir())
		idx.noteCollapsedKeys(names)
		st := idx.CollapsedKeys()

		distinct := map[string]bool{}
		for _, n := range names {
			distinct[n] = true
		}
		var sumFiles, keysOverOne int
		for n := range distinct {
			m := idx.KeyMultiplicity(n)
			if m > 1 {
				sumFiles += m - 1
				keysOverOne++
			}
		}
		if sumFiles != st.Files {
			t.Errorf("case %d %v: sum(m-1 for m>1) = %d, want CollapsedKeys().Files = %d",
				i, names, sumFiles, st.Files)
		}
		if keysOverOne != st.Keys {
			t.Errorf("case %d %v: count(m>1) = %d, want CollapsedKeys().Keys = %d",
				i, names, keysOverOne, st.Keys)
		}
	}
}

// multiplicityFixtureFiles writes a dump under dir carrying one collided key
// (two files under an unanchored wrapper, which collapse onto one generic key
// by construction — see collapsingDump) and one unique key, and returns the
// two keys.
func multiplicityFixtureFiles(t *testing.T, dir string) (collided, unique string) {
	t.Helper()
	mkBSLFile(t, dir, "w/Прочее/A/Ext/ObjectModule.bsl", "Процедура А()\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "w/Прочее/B/Ext/ObjectModule.bsl", "Процедура Б()\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Catalogs/Уникальный/Ext/ObjectModule.bsl", "Процедура У()\nКонецПроцедуры\n")
	collided = bslPathToModuleName("w/Прочее/A/Ext/ObjectModule.bsl")
	unique = bslPathToModuleName("Catalogs/Уникальный/Ext/ObjectModule.bsl")
	if got := bslPathToModuleName("w/Прочее/B/Ext/ObjectModule.bsl"); got != collided {
		t.Fatalf("fixture is not a collision: A -> %q, B -> %q", collided, got)
	}
	return collided, unique
}

// TestKeyMultiplicityIsPublishedOnEveryInstallPath drives the accessor through
// every live install point of noteCollapsedKeys, plus the direct in-package
// call to the one census-declared writer with no product caller
// (loadBSLFiles). The arm set is asserted equal to namesWriters' recording
// entries, so a loader added later cannot skip an arm without failing here.
func TestKeyMultiplicityIsPublishedOnEveryInstallPath(t *testing.T) {
	const absentKey = "НикогдаНеСуществовавшийКлюч.МодульОбъекта"

	assertTriple := func(t *testing.T, idx *Index, collided, unique string, wantCollided int) {
		t.Helper()
		if n := idx.KeyMultiplicity(collided); n != wantCollided {
			t.Errorf("KeyMultiplicity(collided) = %d, want %d", n, wantCollided)
		}
		if n := idx.KeyMultiplicity(unique); n != 1 {
			t.Errorf("KeyMultiplicity(unique) = %d, want 1", n)
		}
		if n := idx.KeyMultiplicity(absentKey); n != 0 {
			t.Errorf("KeyMultiplicity(absent) = %d, want 0", n)
		}
	}

	armsRun := 0
	var recordedFuncs []string

	// Arms 1-3 share one dir/cache: cold build, then an unchanged warm
	// reopen, then a warm reopen whose diff ADDS a third colliding file. The
	// second call to loadFromManifestAndDiff records TWICE inside itself
	// (before the diff and after), and the "adds a collision" arm is the one
	// that closes the warm-diff hole design section 3.3 names: idx.names
	// dedup-guards the addition, so only pathToDocID (what the after-diff
	// call uses) can see the new collision.
	dir := t.TempDir()
	cacheDir := t.TempDir()
	collided, unique := multiplicityFixtureFiles(t, dir)

	armsRun++
	recordedFuncs = append(recordedFuncs, "loadBSLPaths")
	t.Run("loadBSLPaths_coldBuildWithCache", func(t *testing.T) {
		idx, err := NewIndex(dir, cacheDir, false)
		if err != nil {
			t.Fatalf("NewIndex: %v", err)
		}
		t.Cleanup(func() { idx.Close() })
		waitReady(t, idx, 60*time.Second)
		cpath, err := cachePath(dir, cacheDir)
		if err != nil {
			t.Fatalf("cachePath: %v", err)
		}
		waitManifest(t, cpath, 60*time.Second)
		assertTriple(t, idx, collided, unique, 2)
	})

	armsRun++
	recordedFuncs = append(recordedFuncs, "loadFromManifestAndDiff")
	t.Run("loadFromManifestAndDiff_warmUnchanged", func(t *testing.T) {
		idx, err := NewIndex(dir, cacheDir, false)
		if err != nil {
			t.Fatalf("NewIndex: %v", err)
		}
		t.Cleanup(func() { idx.Close() })
		waitReady(t, idx, 60*time.Second)
		assertTriple(t, idx, collided, unique, 2)
	})

	// A third file deriving the SAME collided key: the warm reopen's diff
	// sees an ADDITION that collides.
	mkBSLFile(t, dir, "w/Прочее/C/Ext/ObjectModule.bsl", "Процедура В()\nКонецПроцедуры\n")
	if got := bslPathToModuleName("w/Прочее/C/Ext/ObjectModule.bsl"); got != collided {
		t.Fatalf("fixture is not a collision: C -> %q, want %q", got, collided)
	}
	armsRun++
	recordedFuncs = append(recordedFuncs, "loadFromManifestAndDiff")
	t.Run("loadFromManifestAndDiff_warmAddsCollision", func(t *testing.T) {
		idx, err := NewIndex(dir, cacheDir, false)
		if err != nil {
			t.Fatalf("NewIndex: %v", err)
		}
		t.Cleanup(func() { idx.Close() })
		waitReady(t, idx, 60*time.Second)
		assertTriple(t, idx, collided, unique, 3)
	})

	armsRun++
	recordedFuncs = append(recordedFuncs, "loadNamesReadOnly")
	t.Run("loadNamesReadOnly_readOnlyServe", func(t *testing.T) {
		roDir := t.TempDir()
		roCache := t.TempDir()
		roCollided, roUnique := multiplicityFixtureFiles(t, roDir)
		gensig := mustGenSig(t, roDir)
		if err := BuildGeneration(roDir, roCache, gensig); err != nil {
			t.Fatalf("BuildGeneration: %v", err)
		}
		idx, err := OpenGenerationReadOnly(roDir, roCache, gensig)
		if err != nil {
			t.Fatalf("OpenGenerationReadOnly: %v", err)
		}
		t.Cleanup(func() { idx.Close() })
		waitReady(t, idx, 60*time.Second)
		assertTriple(t, idx, roCollided, roUnique, 2)
	})

	armsRun++
	recordedFuncs = append(recordedFuncs, "swapGeneration")
	t.Run("swapGeneration_reload", func(t *testing.T) {
		roDir := t.TempDir()
		roCache := t.TempDir()
		mkBSLFile(t, roDir, "Catalogs/Уникальный/Ext/ObjectModule.bsl", "Процедура У()\nКонецПроцедуры\n")
		roUnique := bslPathToModuleName("Catalogs/Уникальный/Ext/ObjectModule.bsl")
		idx := openReloadableIndex(t, roDir, roCache)

		mkBSLFile(t, roDir, "w/Прочее/A/Ext/ObjectModule.bsl", "Процедура А()\nКонецПроцедуры\n")
		mkBSLFile(t, roDir, "w/Прочее/B/Ext/ObjectModule.bsl", "Процедура Б()\nКонецПроцедуры\n")
		roCollided := bslPathToModuleName("w/Прочее/A/Ext/ObjectModule.bsl")

		rep, err := idx.Reload()
		if err != nil {
			t.Fatalf("Reload: %v", err)
		}
		if !rep.Changed {
			t.Fatalf("Reload did not report a change after adding two colliding files")
		}
		assertTriple(t, idx, roCollided, roUnique, 2)
	})

	armsRun++
	recordedFuncs = append(recordedFuncs, "loadBSLFiles")
	t.Run("loadBSLFiles_directInPackageCall", func(t *testing.T) {
		bfDir := t.TempDir()
		bfCollided, bfUnique := multiplicityFixtureFiles(t, bfDir)
		idx := bareIndex(t, bfDir)
		if err := idx.loadBSLFiles(bfDir); err != nil {
			t.Fatalf("loadBSLFiles: %v", err)
		}
		assertTriple(t, idx, bfCollided, bfUnique, 2)
	})

	if armsRun < 6 {
		t.Fatalf("only %d of the six install-point arms ran", armsRun)
	}

	distinctFuncs := append([]string(nil), recordedFuncs...)
	slices.Sort(distinctFuncs)
	distinctFuncs = slices.Compact(distinctFuncs)

	var wantArms []string
	for fn, mustRecord := range namesWriters {
		if mustRecord {
			wantArms = append(wantArms, fn)
		}
	}
	slices.Sort(wantArms)
	if !slices.Equal(distinctFuncs, wantArms) {
		t.Fatalf("arm coverage %v does not match namesWriters' recording entries %v; "+
			"a loader was added or removed without updating this test's arms",
			distinctFuncs, wantArms)
	}
}

// funcsStoringIntoCollapsed parses the package's non-test sources and returns
// the name of every function containing a call of the shape
// `<x>.collapsed.Store(...)`. It matches the SELECTOR CHAIN, not a resolved
// type, for the same reason funcsWritingNames does: the safe failure mode of
// a wrong match is one extra name a human must clear, never a silent miss.
func funcsStoringIntoCollapsed(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	found := map[string]bool{}
	files := 0
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !filepathHasGoSuffix(n) || filepathHasTestSuffix(n) {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", n), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		files++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Store" {
					return true
				}
				inner, ok := sel.X.(*ast.SelectorExpr)
				if !ok || inner.Sel.Name != "collapsed" {
					return true
				}
				found[fn.Name.Name] = true
				return true
			})
		}
	}
	if files == 0 {
		t.Fatalf("parsed zero non-test .go files; the scan measured nothing")
	}
	out := make([]string, 0, len(found))
	for fn := range found {
		out = append(out, fn)
	}
	slices.Sort(out)
	return out
}

func filepathHasGoSuffix(name string) bool { return len(name) > 3 && name[len(name)-3:] == ".go" }
func filepathHasTestSuffix(name string) bool {
	return len(name) > 8 && name[len(name)-8:] == "_test.go"
}

// TestKeyMultiplicityAndCollapsedKeysDescribeOneLoad reloads from a colliding
// tree onto a clean one and requires the aggregate report and the per-key
// accessor to move TOGETHER, never a mix of an old count and a new report (or
// vice versa). The structural half asserts that exactly one function in the
// package can ever publish idx.collapsed, so a second writer cannot be added
// beside noteCollapsedKeys without this test catching it.
func TestKeyMultiplicityAndCollapsedKeysDescribeOneLoad(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "w/Прочее/A/Ext/ObjectModule.bsl", "Процедура А()\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "w/Прочее/B/Ext/ObjectModule.bsl", "Процедура Б()\nКонецПроцедуры\n")
	collided := bslPathToModuleName("w/Прочее/A/Ext/ObjectModule.bsl")

	idx := openReloadableIndex(t, dir, cacheDir)
	if st := idx.CollapsedKeys(); st.Files != 1 {
		t.Fatalf("fixture setup: CollapsedKeys().Files = %d before reload, want 1", st.Files)
	}
	if n := idx.KeyMultiplicity(collided); n != 2 {
		t.Fatalf("fixture setup: KeyMultiplicity(collided) = %d before reload, want 2", n)
	}

	// Clean the tree onto exactly ONE file deriving the same key: the
	// collision goes away, but the key itself must still resolve, at 1, and
	// the two reports must land together.
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash("w/Прочее/B/Ext/ObjectModule.bsl"))); err != nil {
		t.Fatalf("os.Remove: %v", err)
	}

	rep := mustReload(t, idx)
	if !rep.Changed {
		t.Fatalf("Reload did not report a change after removing a colliding file")
	}
	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Errorf("CollapsedKeys().Files after reload = %d, want 0", st.Files)
	}
	if n := idx.KeyMultiplicity(collided); n != 1 {
		t.Errorf("KeyMultiplicity(collided) after reload = %d, want 1, never a mix with the "+
			"retired report", n)
	}

	writers := funcsStoringIntoCollapsed(t)
	if !slices.Equal(writers, []string{"noteCollapsedKeys"}) {
		t.Fatalf("functions storing into idx.collapsed = %v, want exactly [noteCollapsedKeys]", writers)
	}
}

// TestKeyMultiplicityIsNFCNormalised mirrors the NFD/NFC fixture class GetContent
// is tested with (dump/index.go, GetContent's NFC(id) call): a docID looked up
// in its NFD spelling (e.g. copy-pasted from a macOS path) must resolve
// against the NFC-keyed multiplicity map exactly as the NFC spelling does.
func TestKeyMultiplicityIsNFCNormalised(t *testing.T) {
	nfcKey := "Документ.Тест" + tnfcSmallShortI + ".МодульОбъекта"
	nfdKey := "Документ.Тест" + tnfdSmallShortI + ".МодульОбъекта"
	if nfcKey == nfdKey {
		t.Fatalf("fixture bug: NFC and NFD spellings are byte-identical, this test measures nothing")
	}

	idx := bareIndex(t, t.TempDir())
	idx.noteCollapsedKeys([]string{nfcKey, nfcKey, "прочий.ключ"})

	if n := idx.KeyMultiplicity(nfcKey); n != 2 {
		t.Errorf("KeyMultiplicity(NFC spelling) = %d, want 2", n)
	}
	if n := idx.KeyMultiplicity(nfdKey); n != 2 {
		t.Errorf("KeyMultiplicity(NFD spelling) = %d, want 2 (same key as the NFC spelling)", n)
	}
}

// TestKeyMultiplicityHoldsEveryKeyNotOnlyTheCollidedOnes pins the full-map
// decision: a unique key must answer 1, not 0. A map that only carried keys
// with count > 1 could not tell "never installed" (0) from "installed once"
// (1) without a second, locked lookup — exactly the cost the full map avoids.
func TestKeyMultiplicityHoldsEveryKeyNotOnlyTheCollidedOnes(t *testing.T) {
	idx := bareIndex(t, t.TempDir())
	idx.noteCollapsedKeys([]string{"единственный.ключ", "дублированный.ключ", "дублированный.ключ"})

	if n := idx.KeyMultiplicity("единственный.ключ"); n != 1 {
		t.Fatalf("KeyMultiplicity(unique key) = %d, want 1: the map must hold every key, "+
			"not only the collided ones", n)
	}
	if n := idx.KeyMultiplicity("дублированный.ключ"); n != 2 {
		t.Fatalf("KeyMultiplicity(collided key) = %d, want 2", n)
	}
}
