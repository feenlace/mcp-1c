package dump

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A DECLARED NAME COLLISION BETWEEN THE TWO DEPTHS, and the regression the descent
// introduced.
//
// WHAT THE DESCENT DID. probeNestedExtensions writes byPrefix[prefix] = name with
// no name check of any kind, so a directory two levels down that declares the SAME
// <Name> as a genuine extension one level down gives both subtrees ONE namespace.
// moduleKey then derives one key from two files and the second write wins.
//
// WHY THIS IS A REGRESSION AND NOT A PREFERENCE. v1.18.0 has no byPrefix map and no
// descent at all, and its moduleKey asks byDir, then self, then bslPathToModuleName,
// so on the tree TestADepthTwoExtensionMayNotTakeADepthOneName builds the deeper
// subtree gets no namespace, the anchor scan drops its wrapper segments, and it lands
// on the base configuration's own key, which nothing else in that tree uses: two
// distinct keys, nothing assigned twice. This branch turned that into ONE key twice,
// measured CollapsedKeys{Files:1 Keys:1}, with the deeper file's bytes served under
// it. A tree that lost nothing now loses a file.
//
// THE RULE. A name recorded by the DESCENT must be unique across
// {self} + byDir + byPrefix. Where it is not, EVERY byPrefix entry carrying that
// name is dropped; self and byDir are never touched.
//
// WHY NEITHER COLLIDING PREFIX ENTRY IS KEPT. Dropping the byDir entry as well
// would send both subtrees to one base key, which is LOSSIER THAN v1.18.0. Keeping
// the first one is order dependent, and "first" here is os.ReadDir byte order,
// which a rename changes.
//
// WHY IT IS A POST PASS AND NOT A CHECK AT THE ASSIGNMENT. byDir is INCOMPLETE
// while the descent runs: the listing is byte sorted, so a wrapper named «Ааа» is
// descended into before a genuine «Настоящее» is ever read.
// TestTheNameCheckDoesNotDependOnDirectoryOrder is that difference as a test.

const (
	// The genuine extension's directory, its declared name, and the wrapper that
	// holds the impostor. «Настоящее» sorts BEFORE «Обёртка» (Н is U+041D, О is
	// U+041E), which is why the order test below uses different names to reach the
	// other order.
	collisionGenuineDir = "Настоящее"
	collisionExtName    = "Честное"
	collisionWrapperDir = "Обёртка"
	collisionImpostor   = "поддельное"

	// The object path both files sit at, so a shared namespace collapses them.
	collisionObjectRel = "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl"

	// The key the genuine extension's module must have, and the key the impostor's
	// module falls back to once its prefix entry is dropped. The second is the base
	// configuration's own key shape, reached because anchorIndex re-anchors the path
	// onto its kind directory.
	collisionExtKey  = "ext.Честное.Документ.ПеремещениеЗапасов.МодульОбъекта"
	collisionBaseKey = "Документ.ПеремещениеЗапасов.МодульОбъекта"

	collisionGenuineBody  = "// настоящее расширение\n"
	collisionImpostorBody = "// подделка\n"
)

// mkCollisionTree writes the base root, the genuine extension one level down and a
// namesake two levels down under wrapper, and returns the root.
//
// impostorName is a parameter because every assertion about a DROP needs a control
// in which the same descent RECORDS: an empty byPrefix proves the rule only if the
// same tree with a different declared name fills it.
func mkCollisionTree(t *testing.T, wrapper, impostorName string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkExtensionDump(t, filepath.Join(root, collisionGenuineDir), extManifestClassic, collisionExtName)
	mkBSLFile(t, root, collisionGenuineDir+"/"+collisionObjectRel, collisionGenuineBody)
	mkExtensionDump(t, filepath.Join(root, wrapper, collisionImpostor), extManifestClassic, impostorName)
	mkBSLFile(t, root, wrapper+"/"+collisionImpostor+"/"+collisionObjectRel, collisionImpostorBody)
	return root
}

// buildCollisionIndex builds the real index over root and waits for it.
func buildCollisionIndex(t *testing.T, root string) *Index {
	t.Helper()
	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the build finished: %v", err)
	}
	return idx
}

// collisionNameCounts counts the multiset ModuleNames returns. It keeps one entry
// per FILE, so a collapse shows up as a count above one on a single name.
func collisionNameCounts(names []string) map[string]int {
	seen := make(map[string]int, len(names))
	for _, n := range names {
		seen[n]++
	}
	return seen
}

// TestADepthTwoExtensionMayNotTakeADepthOneName is the regression itself.
func TestADepthTwoExtensionMayNotTakeADepthOneName(t *testing.T) {
	for _, k := range []string{collisionExtKey, collisionBaseKey} {
		if NFC(k) != k {
			t.Fatalf("test literal %q is not NFC, so it can never match an index key", k)
		}
	}

	// THE CONTROL, FIRST. The same tree with the deeper extension declaring a name
	// of its own: the descent reaches it and records it. Without this, an empty
	// byPrefix below would also be satisfied by a descent that never happened.
	ctrlRoot := mkCollisionTree(t, collisionWrapperDir, "Иное")
	ctrl := detectExtensionLayout(ctrlRoot)
	ctrlPrefix := collisionWrapperDir + "/" + collisionImpostor
	if got := ctrl.byPrefix[ctrlPrefix]; got != "Иное" {
		t.Fatalf("control: byPrefix[%q] = %q, want %q. The descent does not reach that "+
			"directory at all, so nothing below measures a DROP. Full layout: %+v",
			ctrlPrefix, got, "Иное", ctrl)
	}
	if ctrl.byDir[collisionGenuineDir] != collisionExtName {
		t.Fatalf("control: byDir[%q] = %q, want %q", collisionGenuineDir,
			ctrl.byDir[collisionGenuineDir], collisionExtName)
	}

	// THE TREE UNDER TEST: the deeper one declares the SAME name.
	root := mkCollisionTree(t, collisionWrapperDir, collisionExtName)
	l := detectExtensionLayout(root)

	// PREMISE. The root is a base configuration, so nothing here is decided by a
	// root-level namespace.
	if l.self != "" {
		t.Fatalf("self = %q, want empty: the root carries a CONFIGURATION's manifest and "+
			"this tree is about the two levels below it", l.self)
	}
	if len(l.doubts) != 0 {
		t.Fatalf("the layout recorded %d doubts, so some directory was never decided and "+
			"the assertions below are about a different tree: %+v", len(l.doubts), l.doubts)
	}

	// byDir IS NEVER TOUCHED.
	if got := l.byDir[collisionGenuineDir]; got != collisionExtName {
		t.Errorf("byDir[%q] = %q, want %q: the genuine extension one level down keeps its "+
			"name whatever a deeper directory declares", collisionGenuineDir, got, collisionExtName)
	}
	// AND THE COLLIDING PREFIX ENTRY IS GONE.
	if len(l.byPrefix) != 0 {
		t.Errorf("byPrefix = %v, want empty: a directory two levels down declared %q, which "+
			"the extension at %q already holds, so both would key into one namespace",
			l.byPrefix, collisionExtName, collisionGenuineDir)
	}

	idx := buildCollisionIndex(t, root)

	names := idx.ModuleNames()
	seen := collisionNameCounts(names)
	if len(names) != 2 {
		t.Fatalf("the index holds %d module entries, want 2: the walk must have reached both "+
			".bsl files before anything below can mean anything. Names: %v", len(names), names)
	}
	if len(seen) != 2 {
		t.Errorf("the two files produced %d distinct keys, want 2: %v. v1.18.0 has no "+
			"descent at all and keys these two apart, so one key twice is a loss this "+
			"branch introduced", len(seen), seen)
	}

	// THE GENUINE BYTES ARE THE ONES SERVED UNDER THE GENUINE KEY.
	if got, ok := idx.GetContent(collisionExtKey); !ok {
		t.Errorf("GetContent(%q) reported the module missing. Names: %v", collisionExtKey, names)
	} else if got != collisionGenuineBody {
		t.Errorf("GetContent(%q) = %q, want %q: this key is serving the impostor's bytes",
			collisionExtKey, got, collisionGenuineBody)
	}
	// AND THE IMPOSTOR IS STILL REACHABLE, on the key v1.18.0 gave it.
	if got, ok := idx.GetContent(collisionBaseKey); !ok {
		t.Errorf("GetContent(%q) reported the module missing, so the deeper file is served "+
			"by nothing at all. Names: %v", collisionBaseKey, names)
	} else if got != collisionImpostorBody {
		t.Errorf("GetContent(%q) = %q, want %q", collisionBaseKey, got, collisionImpostorBody)
	}

	// NOTHING WAS OVERWRITTEN. This is the package's own measurement of the loss.
	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Errorf("CollapsedKeys() = {Files:%d Keys:%d Sample:%v}, want Files 0", st.Files, st.Keys, st.Sample)
	}

	// AND THE LOSS OF THE NAMESPACE IS REPORTED WHERE IT ALREADY WAS. The dropped
	// subtree keys with its anchor moved, which is exactly what wrapped_paths.go
	// counts, so no second channel is needed to say it.
	if st := idx.WrappedPaths(); st.Files != 1 || st.Total != 2 {
		t.Errorf("WrappedPaths() = {Files:%d Total:%d}, want {Files:1 Total:2}: the dropped "+
			"subtree must be reported as wrapped", st.Files, st.Total)
	}
}

// layoutFingerprint renders everything about a layout that CAN be compared across
// two trees whose wrapper directories are named differently.
//
// byPrefix contributes its VALUES only. Its keys begin with the wrapper's name, so
// they differ by construction and comparing them would fail on the fixture rather
// than on the code. The doubts contribute their reasons for the same reason: the
// dir field of a doubt is a directory name.
func layoutFingerprint(l extensionLayout) string {
	dirs := make([]string, 0, len(l.byDir))
	for k, v := range l.byDir {
		dirs = append(dirs, k+"="+v)
	}
	slices.Sort(dirs)
	prefixNames := make([]string, 0, len(l.byPrefix))
	for _, v := range l.byPrefix {
		prefixNames = append(prefixNames, v)
	}
	slices.Sort(prefixNames)
	reasons := make([]string, 0, len(l.doubts))
	for _, d := range l.doubts {
		reasons = append(reasons, fmt.Sprintf("%d", d.reason))
	}
	slices.Sort(reasons)
	return fmt.Sprintf("self=%q byDir=[%s] prefixNames=[%s] doubts=[%s] cost=%+v",
		l.self, strings.Join(dirs, " "), strings.Join(prefixNames, " "),
		strings.Join(reasons, " "), l.cost)
}

// TestTheNameCheckDoesNotDependOnDirectoryOrder is why the check is a post pass.
//
// os.ReadDir returns entries sorted by name, so the order in which byDir fills is a
// property of the DIRECTORY NAMES. A check asked at the moment a prefix entry is
// recorded reads a byDir that is only as complete as the listing has got, and it
// therefore answers differently for two trees that differ by a rename.
func TestTheNameCheckDoesNotDependOnDirectoryOrder(t *testing.T) {
	// THE PREMISE, FIRST, or this test measures nothing: one wrapper must sort
	// BEFORE the genuine extension's directory and the other AFTER it.
	const early, late = "Ааа", "Яяя"
	if !(strings.Compare(early, collisionGenuineDir) < 0 && strings.Compare(collisionGenuineDir, late) < 0) {
		t.Fatalf("the fixture does not straddle %q: %q<%q is %v and %q<%q is %v. Both "+
			"wrappers would then be read on the same side and the two trees could not "+
			"differ in the way this test exists to catch",
			collisionGenuineDir, early, collisionGenuineDir,
			strings.Compare(early, collisionGenuineDir) < 0,
			collisionGenuineDir, late, strings.Compare(collisionGenuineDir, late) < 0)
	}

	// THE CONTROL, AND WITHOUT IT THIS TEST MEASURES NOTHING. Agreement is what is
	// asserted below, and two EMPTY answers agree: with the descent switched off
	// neither tree records a prefix entry, both fingerprints read prefixNames=[],
	// and the comparison is green. So the same two trees are built with the deeper
	// directory declaring a name of ITS OWN, on BOTH sides of the sort boundary, and
	// the descent has to record it on both. This is the shape the three tests
	// around it carry: an empty byPrefix is a DROP only where a different name fills
	// it.
	for _, wrapper := range []string{early, late} {
		ctrl := detectExtensionLayout(mkCollisionTree(t, wrapper, "Иное"))
		prefix := wrapper + "/" + collisionImpostor
		if got := ctrl.byPrefix[prefix]; got != "Иное" {
			t.Fatalf("control: byPrefix[%q] = %q, want %q. The descent does not reach that "+
				"directory at all, so the agreement below is two empty answers agreeing. "+
				"Full layout: %+v", prefix, got, "Иное", ctrl)
		}
	}

	earlyLayout := detectExtensionLayout(mkCollisionTree(t, early, collisionExtName))
	lateLayout := detectExtensionLayout(mkCollisionTree(t, late, collisionExtName))

	got, want := layoutFingerprint(earlyLayout), layoutFingerprint(lateLayout)
	if got != want {
		t.Errorf("the two trees differ only by the name of the wrapper directory, and the "+
			"detection answered differently:\n wrapper %q: %s\n wrapper %q: %s\nThe listing is "+
			"byte sorted, so this is the name check reading a byDir that the descent had "+
			"outrun.", early, got, late, want)
	}
}

// TestTwoDepthTwoSiblingsWithOneNameAreNoWorseThanBefore pins a bound, not a win.
//
// Two directories at depth two under one wrapper declaring ONE name lose a file
// whichever way the rule falls: dropping both sends them to the same base key, and
// keeping either one is the order dependence the post pass exists to avoid. THE
// SAME FILE IS LOST BY v1.18.0, which gives neither of them a namespace either, so
// this is the NOT LOSSIER bound and nothing more. It is here so that a later change
// cannot make this shape worse without saying so.
func TestTwoDepthTwoSiblingsWithOneNameAreNoWorseThanBefore(t *testing.T) {
	const wrapper, sibA, sibB = "W", "a", "b"
	const oneName = "Одно"

	build := func(t *testing.T, nameA, nameB string) string {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, extManifestClassic),
			[]byte(baseConfigManifest()), 0o644); err != nil {
			t.Fatal(err)
		}
		mkExtensionDump(t, filepath.Join(root, wrapper, sibA), extManifestClassic, nameA)
		mkBSLFile(t, root, wrapper+"/"+sibA+"/"+collisionObjectRel, "// a\n")
		mkExtensionDump(t, filepath.Join(root, wrapper, sibB), extManifestClassic, nameB)
		mkBSLFile(t, root, wrapper+"/"+sibB+"/"+collisionObjectRel, "// b\n")
		return root
	}

	// CONTROL: with two names, both siblings are recorded. An empty byPrefix below
	// means a drop only because this one is full.
	ctrl := detectExtensionLayout(build(t, oneName, "Другое"))
	if len(ctrl.byPrefix) != 2 {
		t.Fatalf("control: byPrefix = %v, want both siblings recorded. The descent does not "+
			"reach them, so nothing below measures a DROP", ctrl.byPrefix)
	}

	root := build(t, oneName, oneName)
	l := detectExtensionLayout(root)
	if len(l.byPrefix) != 0 {
		t.Errorf("byPrefix = %v, want empty: two siblings declared %q, so neither can own "+
			"that namespace and keeping either one would depend on the listing order",
			l.byPrefix, oneName)
	}

	idx := buildCollisionIndex(t, root)
	// ONE FILE LOST, WHICH IS WHAT v1.18.0 LOSES ON THIS TREE TOO.
	if st := idx.CollapsedKeys(); st.Files != 1 || st.Keys != 1 {
		t.Errorf("CollapsedKeys() = {Files:%d Keys:%d Sample:%v}, want {Files:1 Keys:1}: both "+
			"siblings key onto the base configuration's own name, which is the loss this "+
			"shape already had", st.Files, st.Keys, st.Sample)
	}
	if st := idx.WrappedPaths(); st.Files != 2 {
		t.Errorf("WrappedPaths() = {Files:%d Total:%d}, want Files 2: both subtrees are keyed "+
			"with their anchor moved and that is the channel that reports it", st.Files, st.Total)
	}
}

// TestARootExtensionKeepsItsOwnNameAgainstAGrandchild covers the shape in which the
// name the descent collides with is the ROOT's own.
//
// self is therefore part of the counted set, and that is load bearing rather than
// tidy: measured on Ext/ConfigurationModule.bsl, a path the anchor scan cannot
// re-anchor, admitting the grandchild gives ONE key twice and CollapsedKeys{Files:1}.
func TestARootExtensionKeepsItsOwnNameAgainstAGrandchild(t *testing.T) {
	const selfName = "Своё"
	const wrapper, inner = "Обёртка", "внутри"
	const configModuleRel = "Ext/ConfigurationModule.bsl"
	const ownBody, innerBody = "// корень\n", "// внутри\n"

	build := func(t *testing.T, grandchildName string) string {
		t.Helper()
		root := t.TempDir()
		mkExtensionDump(t, root, extManifestClassic, selfName)
		mkBSLFile(t, root, configModuleRel, ownBody)
		mkExtensionDump(t, filepath.Join(root, wrapper, inner), extManifestClassic, grandchildName)
		mkBSLFile(t, root, wrapper+"/"+inner+"/"+configModuleRel, innerBody)
		return root
	}

	// CONTROL: a grandchild with a name of its own IS recorded, so an empty byPrefix
	// below is a drop and not a descent that never ran.
	ctrl := detectExtensionLayout(build(t, "Иное"))
	if got := ctrl.byPrefix[wrapper+"/"+inner]; got != "Иное" {
		t.Fatalf("control: byPrefix[%q] = %q, want %q. Full layout: %+v",
			wrapper+"/"+inner, got, "Иное", ctrl)
	}

	root := build(t, selfName)
	l := detectExtensionLayout(root)
	if l.self != selfName {
		t.Fatalf("self = %q, want %q: the root's own manifest decides this and every "+
			"assertion below depends on it", l.self, selfName)
	}
	if len(l.byPrefix) != 0 {
		t.Errorf("byPrefix = %v, want empty: the grandchild declared %q, which the ROOT "+
			"itself holds, so both would key into one namespace", l.byPrefix, selfName)
	}

	idx := buildCollisionIndex(t, root)
	names := idx.ModuleNames()
	seen := collisionNameCounts(names)
	if len(names) != 2 {
		t.Fatalf("the index holds %d module entries, want 2. Names: %v", len(names), names)
	}
	if len(seen) != 2 {
		t.Errorf("the two files produced %d distinct keys, want 2: %v", len(seen), seen)
	}

	// BOTH FILES STAY INSIDE THE ROOT'S OWN NAMESPACE, and they are two modules.
	bodies := make(map[string]string, 2)
	for n := range seen {
		if !strings.HasPrefix(n, "ext."+selfName+".") {
			t.Errorf("key %q is outside the root extension's namespace %q, so the root's own "+
				"name was not the one that survived", n, "ext."+selfName+".")
		}
		body, ok := idx.GetContent(n)
		if !ok {
			t.Errorf("GetContent(%q) reported the module missing", n)
			continue
		}
		bodies[n] = body
	}
	if len(bodies) == 2 {
		var got []string
		for _, b := range bodies {
			got = append(got, b)
		}
		slices.Sort(got)
		want := []string{innerBody, ownBody}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("the two keys serve %q, want %q: one of them is serving the other "+
				"file's bytes", got, want)
		}
	}

	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Errorf("CollapsedKeys() = {Files:%d Keys:%d Sample:%v}, want Files 0: the root's own "+
			"configuration module lost its content to the grandchild's", st.Files, st.Keys, st.Sample)
	}
}
