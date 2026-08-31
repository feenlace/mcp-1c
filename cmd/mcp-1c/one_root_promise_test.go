package main

import (
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// The startup sentence PROMISES that nothing is lost, and the index MEASURES what
// is lost. This holds the two together on one tree.
//
// THE SENTENCE. When exactly one dump root sits below the --dump path and the
// detection recognised THAT root as an extension, the operator is told «сервер
// проиндексирует его под собственным именем, так что содержимое не потеряется».
//
// WHAT MADE IT FALSE. The descent added for issue 46 records an extension two
// levels down under a two-segment prefix with no name check, so a directory at
// depth two declaring the SAME name as the recognised root put both subtrees into
// one namespace. The recognised root's own module was then overwritten by the
// deeper file, and the same process measured that as CollapsedKeys{Files:1} while
// the sentence promised the opposite.
//
// BOTH HALVES ARE IN ONE TEST ON PURPOSE. Split across two, the promise and the
// measurement can drift apart: one of them can be moved to a different tree and
// both stay green while the pair they form stops being true of anything.
//
// WHAT THIS DOES NOT CLAIM. The same sentence can still be false by a different
// route: two IMMEDIATE children declaring one name collide exactly as they did in
// v1.18.0, which is a shape this branch neither introduced nor repairs. The
// assertions below are about THIS tree and no wider set.

// TestTheOneRootPromiseHoldsAgainstADepthTwoNamesake.
func TestTheOneRootPromiseHoldsAgainstADepthTwoNamesake(t *testing.T) {
	const extName = "Доработка"
	const objectRel = "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl"
	const rootBody, deepBody = "// опознанный корень\n", "// двумя уровнями ниже\n"

	parent := t.TempDir()
	// A is a dump root by its own manifest AND a recognised extension: the case the
	// sentence is about.
	nestedIdentityWrite(t, parent, "A/Configuration.xml", nestedIdentityExtManifest(extName))
	nestedIdentityWrite(t, parent, "A/"+objectRel, rootBody)
	// B is a wrapper with no manifest, so it is not a dump root at all, and the
	// namesake sits inside it at depth two.
	nestedIdentityWrite(t, parent, "B/Расш/Configuration.xml", nestedIdentityExtManifest(extName))
	nestedIdentityWrite(t, parent, "B/Расш/"+objectRel, deepBody)

	// PREMISE. The path is not a root, exactly one root sits below it, and that root
	// is the extension the sentence names. Without all three the branch under test
	// is never reached and the assertions below are true of nothing.
	insp := dump.InspectDumpRoot(parent)
	if insp.IsRoot {
		t.Fatalf("the parent inspected AS a root, so the nested-root sentence is unreachable")
	}
	if len(insp.NestedRoots) != 1 || insp.NestedRoots[0] != "A" {
		t.Fatalf("NestedRoots = %q, want exactly [A]: B carries no manifest and no kind "+
			"directories, so it must not be a root", insp.NestedRoots)
	}
	layout := dump.InspectExtensionLayout(parent)
	if layout.Undecided() != 0 || layout.ScanTruncated {
		t.Fatalf("the layout carries doubts (%d undecided, truncated=%v), so a second record "+
			"is published and the message read below would be the wrong one",
			layout.Undecided(), layout.ScanTruncated)
	}
	if extensionRootsAmong(insp.NestedRoots, layout) != 1 {
		t.Fatalf("the recognised extensions %q do not include the root below the path, so "+
			"the promising branch is not the one that runs", layout.Dirs)
	}

	// THE PROMISE.
	msg := nestedIdentityReport(t, parent)
	if !strings.Contains(msg, "не потеряется") {
		t.Fatalf("the message does not promise that the recognised root keeps its content, "+
			"so there is no promise left to hold:\n%s", msg)
	}

	// AND THE MEASUREMENT, ON THE SAME TREE.
	idx, err := dump.NewIndex(parent, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the build finished: %v", err)
	}

	names := idx.ModuleNames()
	if len(names) != 2 {
		t.Fatalf("the index holds %d module entries, want 2: the walk must have reached both "+
			".bsl files. Names: %v", len(names), names)
	}
	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Errorf("the startup message promised «не потеряется» and the same process measures "+
			"CollapsedKeys() = {Files:%d Keys:%d Sample:%v}. The directory at depth two "+
			"declares %q, which the recognised root already holds. Message:\n%s\nNames: %v",
			st.Files, st.Keys, st.Sample, extName, msg, names)
	}
}
