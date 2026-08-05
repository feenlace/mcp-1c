//go:build unix

package dump

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestASymlinkedRootIsNotDeclaredCorrect.
//
// THE TWO SIDES DISAGREED ABOUT ONE PATH. os.ReadDir follows a symbolic link, so
// the inspection reported the TARGET and answered IsRoot; filepath.WalkDir lstats
// its root and a symlink is not a directory, so the walk ended before visiting a
// single file. A path that indexes nothing was declared correct, which is the one
// thing a diagnostic must never do.
//
// The walk is not changed here and the numbers below say why it matters anyway:
// the tree really does hold modules, and really does index none of them.
func TestASymlinkedRootIsNotDeclaredCorrect(t *testing.T) {
	real := t.TempDir()
	for _, k := range []string{"Catalogs/Ном/Ext", "Documents/Зак/Ext"} {
		if err := os.MkdirAll(filepath.Join(real, k), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(real, k, "ObjectModule.bsl"),
			[]byte("Процедура П() КонецПроцедуры\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	// PREMISE: the target really is a dump root holding modules, so what follows is
	// about the link and not about an empty tree.
	if direct := InspectDumpRoot(real); !direct.IsRoot || direct.RootIsSymlink {
		t.Fatalf("premise broken: the target inspects as %+v", direct)
	}

	insp := InspectDumpRoot(link)
	if !insp.RootIsSymlink {
		t.Error("a symlinked --dump was not reported as one, so the inspection describes " +
			"the target while the indexer walks nothing")
	}

	// The measurement that makes it matter: the indexer really does index none.
	idx := &Index{dir: link, pathByName: map[string]string{}, pathToDocID: map[string]string{}}
	if err := idx.loadBSLPaths(link); err != nil {
		t.Fatalf("loadBSLPaths: %v", err)
	}
	if len(idx.names) != 0 {
		t.Fatalf("premise moved: the walk now indexes %d modules through a symlinked root, "+
			"so this test is guarding a disagreement that no longer exists", len(idx.names))
	}
	direct := &Index{dir: real, pathByName: map[string]string{}, pathToDocID: map[string]string{}}
	if err := direct.loadBSLPaths(real); err != nil {
		t.Fatal(err)
	}
	if len(direct.names) != 2 {
		t.Fatalf("control failed: the real path indexes %d modules, want 2", len(direct.names))
	}
	t.Logf("through the link: %d modules; through the real path: %d", len(idx.names), len(direct.names))
}

// TestSymlinkedChildrenAreCounted. A symlinked child is invisible to BOTH sides in
// the same way: os.ReadDir calls it a non-directory so it is never a nested root,
// and WalkDir does not descend into it. They agree, and both stay quiet, so the
// count is what carries it.
func TestSymlinkedChildrenAreCounted(t *testing.T) {
	real := t.TempDir()
	for _, k := range []string{"Catalogs", "Documents"} {
		if err := os.MkdirAll(filepath.Join(real, k), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	for _, k := range []string{"Catalogs", "Documents", "CommonModules"} {
		if err := os.MkdirAll(filepath.Join(root, k), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(real, filepath.Join(root, "снаружи")); err != nil {
		t.Fatal(err)
	}

	insp := InspectDumpRoot(root)
	if !insp.IsRoot {
		t.Fatalf("premise broken: the fixture is not a dump root: %+v", insp)
	}
	if insp.SymlinkedChildren != 1 {
		t.Errorf("SymlinkedChildren = %d, want 1", insp.SymlinkedChildren)
	}
	if insp.RootIsSymlink {
		t.Error("a real directory was reported as a symlink")
	}
	// And the symlinked child is NOT among the nested roots, which is the agreement
	// with the walker rather than an oversight.
	//
	// IT HAS TO BE ASKED BELOW A PATH THAT IS NOT ITSELF A ROOT. InspectDumpRoot
	// returns the moment it recognises one and only fills NestedRoots after that
	// return, so on the tree above the list is nil whatever the child scan does with
	// a link, and a loop over it is a loop over nothing: with the question asked
	// here it held on a build that treats a symlinked child as an ordinary
	// directory.
	parent := t.TempDir()
	writeKindDirs(t, filepath.Join(parent, "внутри"), "Catalogs", "Documents")
	if err := os.Symlink(real, filepath.Join(parent, "снаружи")); err != nil {
		t.Fatal(err)
	}
	below := InspectDumpRoot(parent)
	if below.IsRoot {
		t.Fatalf("premise broken: the parent must not be a root itself: %+v", below)
	}
	// POSITIVE CONTROL: an ordinary child root IS named, so the absence of the link
	// below is about the link and not about a list that is empty either way.
	if !slices.Contains(below.NestedRoots, "внутри") {
		t.Fatalf("NestedRoots = %v, want the ordinary child root named", below.NestedRoots)
	}
	if slices.Contains(below.NestedRoots, "снаружи") {
		t.Error("a symlinked child was listed as a nested dump root, which the indexer " +
			"would never descend into")
	}

	// CONTROL: with no symlink the same shape counts none, so the 1 above is the
	// link and not a constant.
	plain := t.TempDir()
	for _, k := range []string{"Catalogs", "Documents"} {
		if err := os.MkdirAll(filepath.Join(plain, k), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := InspectDumpRoot(plain); got.SymlinkedChildren != 0 {
		t.Errorf("SymlinkedChildren = %d on a tree with no links", got.SymlinkedChildren)
	}
}
