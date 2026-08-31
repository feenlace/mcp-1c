package dump

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The keys the nested descent must NOT have moved.
//
// The descent added in this branch is the only thing in the package that can mint
// a namespace from a directory two levels below the dump root. Everything else in
// the key derivation was left alone, and «left alone» is a claim about behaviour
// that no diff can make: a later change to detectExtensionLayout, to
// belongsToSelfExtension or to anchorIndex would move these keys without touching
// a line any of the three tests below reads. That is what these are for. They are
// not coverage of the new code path; they are the pins that make a change to it
// visible.
//
// Each test therefore asserts on the SERVED keys of a real index, not on the
// layout struct alone. The layout is asserted too, but as the premise: a green
// reached with an empty layout for the wrong reason would say nothing about the
// keys, so both are stated and the failure message says which one broke.

// TestLegacyRasshireniyaKeysAreUnchangedByTheNestedProbe is the important one.
//
// The hand made «Расширения/<ext>/» container predates every part of this file and
// its keys are pinned by index.go:bslPathToModuleName, which reads parts[1] as the
// namespace. A manifest inside such a container declaring a DIFFERENT name is the
// input that separates the two sources of a namespace, so this fixture puts a real
// one there: «Расширения/X/Configuration.xml» declares СовсемДругоеИмя while the
// directory is called X. If the descent reached it, the key would move from
// «ext.X.» to «ext.СовсемДругоеИмя.» and every stored reference to the old key
// would stop resolving.
//
// TWO INDEPENDENT MECHANISMS KEEP IT WHERE IT IS, and only the first is about the
// descent:
//
//	extlayout.go:belongsToSelfExtension returns true for extensionDirName, so the
//	manifestAbsent branch of detectExtensionLayout skips «Расширения» outright and
//	probeNestedExtensions is never called for it. byPrefix stays empty. This is the
//	one that would break if the descent were widened.
//
//	index.go:anchorIndex returns 0 for such a path, because extensionDirName is a
//	dumpRootMarker and anchorShapeOK accepts «Расширения/<ext>/<Kind>/...», so
//	bslPathToModuleName strips nothing and reaches its own extensionDirName branch.
//	This one decides the key SHAPE once the layout has declined to supply a
//	namespace.
//
// Both are asserted, separately, so a break names which half moved.
func TestLegacyRasshireniyaKeysAreUnchangedByTheNestedProbe(t *testing.T) {
	const (
		legacyDir     = "X"
		manifestName  = "СовсемДругоеИмя"
		legacyKey     = "ext.X.Документ.Ном.МодульОбъекта"
		manifestKey   = "ext.СовсемДругоеИмя.Документ.Ном.МодульОбъекта"
		legacyBaseKey = "Документ.Основной.МодульОбъекта"
		legacyExtBody = "// из расширения\n"
		legacyBseBody = "// из конфигурации\n"
	)
	for _, k := range []string{legacyKey, manifestKey, legacyBaseKey} {
		if NFC(k) != k {
			t.Fatalf("test literal %q is not NFC, so it can never match an index key", k)
		}
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkBSLFile(t, root, "Documents/Основной/Ext/ObjectModule.bsl", legacyBseBody)

	// The legacy container, with a GENUINE extension manifest inside it declaring a
	// name that is not the directory name. An empty file here would assert nothing:
	// it classifies as not-an-extension under any implementation at any depth, so
	// the manifest comes from the shared builder that reproduces the real byte
	// shape (BOM, CRLF, ObjectBelonging and Name inside Properties).
	mkExtensionDump(t, filepath.Join(root, extensionDirName, legacyDir),
		extManifestClassic, manifestName)
	mkBSLFile(t, root, extensionDirName+"/"+legacyDir+"/Documents/Ном/Ext/ObjectModule.bsl",
		legacyExtBody)

	// PREMISE ONE: the descent produced no candidate for this tree.
	layout := detectExtensionLayout(root)
	if len(layout.byPrefix) != 0 {
		t.Errorf("byPrefix = %v, want empty: belongsToSelfExtension must refuse %q before "+
			"probeNestedExtensions is ever called for it", layout.byPrefix, extensionDirName)
	}
	if len(layout.byDir) != 0 {
		t.Errorf("byDir = %v, want empty: the legacy container is not an extension itself",
			layout.byDir)
	}

	// PREMISE TWO: the derivation strips nothing above the legacy container, which
	// is what leaves its own branch reachable.
	rel := extensionDirName + "/" + legacyDir + "/Documents/Ном/Ext/ObjectModule.bsl"
	if got := anchorIndex(strings.Split(rel, "/")); got != 0 {
		t.Errorf("anchorIndex(%q) = %d, want 0: the legacy container is a dump root marker "+
			"whose own shape anchorShapeOK accepts, so nothing above it may be stripped", rel, got)
	}

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the build finished: %v", err)
	}

	names := idx.ModuleNames()
	if !slices.Contains(names, legacyKey) {
		t.Errorf("the legacy key %q is gone. Names: %v", legacyKey, names)
	}
	if slices.Contains(names, manifestKey) {
		t.Errorf("the index emitted %q: the descent reached inside %q and renamed a key "+
			"that has been pinned since before extlayout.go existed. Names: %v",
			manifestKey, extensionDirName, names)
	}
	for _, n := range names {
		if strings.HasPrefix(n, "ext."+manifestName+".") {
			t.Errorf("the index emitted %q, namespaced by the manifest inside the legacy "+
				"container; inside %q the namespace comes from the DIRECTORY and always has",
				n, extensionDirName)
		}
	}
	if !slices.Contains(names, legacyBaseKey) {
		t.Errorf("the base configuration's key %q is gone. Names: %v", legacyBaseKey, names)
	}

	// The bytes behind the key, not only the key: a namespace that moved and a key
	// that resolved to the wrong file are different failures with the same key set.
	if got, ok := idx.GetContent(legacyKey); !ok {
		t.Errorf("GetContent(%q) reported the module missing", legacyKey)
	} else if got != legacyExtBody {
		t.Errorf("GetContent(%q) = %q, want %q", legacyKey, got, legacyExtBody)
	}
	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Errorf("CollapsedKeys() = {Files:%d Keys:%d Sample:%v}, want Files 0",
			st.Files, st.Keys, st.Sample)
	}
}

// TestCorrectlyPointedDumpKeysAreUnchanged is the ordinary case: a dump root
// pointed at a single configuration with no extension anywhere below it.
//
// It is the widest of the three, because it is the tree almost every installation
// actually has. The descent runs on it (the kind directories are refused by name,
// but nothing else is) and it must leave the keyspace exactly as it was: the
// expected set is written out as literals rather than derived from the production
// tables, so a change to those tables shows up here as a diff instead of following
// the code.
func TestCorrectlyPointedDumpKeysAreUnchanged(t *testing.T) {
	want := []string{
		"Документ.ПеремещениеЗапасов.МодульОбъекта",
		"Документ.РеализацияТоваров.МодульОбъекта",
		"Справочник.Номенклатура.МодульМенеджера",
	}
	for _, k := range want {
		if NFC(k) != k {
			t.Fatalf("test literal %q is not NFC, so it can never match an index key", k)
		}
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkBSLFile(t, root, "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl", "// 1\n")
	mkBSLFile(t, root, "Documents/РеализацияТоваров/Ext/ObjectModule.bsl", "// 2\n")
	mkBSLFile(t, root, "Catalogs/Номенклатура/Ext/ManagerModule.bsl", "// 3\n")

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the build finished: %v", err)
	}

	got := slices.Clone(idx.ModuleNames())
	slices.Sort(got)
	sorted := slices.Clone(want)
	slices.Sort(sorted)
	if !slices.Equal(got, sorted) {
		t.Errorf("module keys = %v, want %v: an ordinary configuration dump must key "+
			"exactly as it did before the nested descent existed", got, sorted)
	}

	// The layout has nothing to say about this tree, and saying nothing is the
	// assertion: Quiet folds every count and both booleans into one answer, so a
	// doubt invented here fails it as loudly as a namespace would.
	summary := idx.ExtensionLayout()
	if summary.Extensions != 0 {
		t.Errorf("ExtensionLayout().Extensions = %d, want 0. Full summary: %+v",
			summary.Extensions, summary)
	}
	if summary.SelfNamed {
		t.Errorf("ExtensionLayout().SelfNamed = true: a base configuration manifest "+
			"declares no extension. Full summary: %+v", summary)
	}
	if !summary.Quiet() {
		t.Errorf("ExtensionLayout().Quiet() = false: there is nothing to report about a "+
			"correctly pointed configuration dump. Full summary: %+v", summary)
	}
	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Errorf("CollapsedKeys() = {Files:%d Keys:%d Sample:%v}, want Files 0",
			st.Files, st.Keys, st.Sample)
	}
}

// TestDepthOneManifestStillUsesByDir pins the -AllExtensions container, the shape
// the descent was added BELOW and must not have taken over.
//
// byDir and byPrefix produce the same key shape, so a key assertion alone cannot
// tell which map answered, and a descent that quietly swallowed the depth one case
// would pass one. The map is therefore read directly, and the summary's Dirs is
// read too: a child contributes ONE segment there and a grandchild TWO, so the
// reported prefix is the second witness of which map holds it.
func TestDepthOneManifestStillUsesByDir(t *testing.T) {
	const (
		childDir = "ExtA"
		extName  = "РасширениеПервое"
		wantKey  = "ext.РасширениеПервое.Документ.Ном.МодульОбъекта"
		body     = "// depth one\n"
	)
	if NFC(wantKey) != wantKey {
		t.Fatalf("test literal %q is not NFC, so it can never match an index key", wantKey)
	}

	// An -AllExtensions container carries no manifest of its own: one subdirectory
	// per extension and nothing at the top.
	root := t.TempDir()
	mkExtensionDump(t, filepath.Join(root, childDir), extManifestClassic, extName)
	mkBSLFile(t, root, childDir+"/Documents/Ном/Ext/ObjectModule.bsl", body)

	layout := detectExtensionLayout(root)
	if got := layout.byDir[childDir]; got != extName {
		t.Errorf("byDir[%q] = %q, want %q: a manifest at depth one is the -AllExtensions "+
			"shape and belongs to byDir. Full layout: byDir=%v byPrefix=%v",
			childDir, got, extName, layout.byDir, layout.byPrefix)
	}
	if len(layout.byPrefix) != 0 {
		t.Errorf("byPrefix = %v, want empty: the descent runs only where the child's own "+
			"verdict was manifestAbsent, and this child declared an extension",
			layout.byPrefix)
	}

	summary := layout.summary()
	if summary.Extensions != 1 {
		t.Errorf("summary().Extensions = %d, want 1. Full summary: %+v",
			summary.Extensions, summary)
	}
	if !slices.Equal(summary.Dirs, []string{childDir}) {
		t.Errorf("summary().Dirs = %v, want [%q]: a depth one extension is reported as ONE "+
			"segment, and two segments here would mean the descent claimed it",
			summary.Dirs, childDir)
	}

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the build finished: %v", err)
	}

	names := idx.ModuleNames()
	if !slices.Contains(names, wantKey) {
		t.Errorf("the index emitted %v, want it to contain %q", names, wantKey)
	}
	if got, ok := idx.GetContent(wantKey); !ok {
		t.Errorf("GetContent(%q) reported the module missing", wantKey)
	} else if got != body {
		t.Errorf("GetContent(%q) = %q, want %q", wantKey, got, body)
	}
	for _, n := range names {
		if strings.HasPrefix(n, "ext."+childDir+".") {
			t.Errorf("the index emitted %q, which takes its namespace from the directory "+
				"name; it must come from <Name> in the manifest, which declares %q", n, extName)
		}
	}
}
