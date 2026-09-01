package dump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A configuration extension that does not sit directly under the dump root.
//
// WHY EACH ASSERTION IS HERE:
//
//	A  is the premise. Without it a green could be reached by a fixture that never
//	   classified as an extension at all, which would make every assertion below it
//	   true of nothing.
//	B  and C are the two keys the tree must produce.
//	D  is the package's own collapse counter, which measures the loss rather than
//	   predicting it.
//	E  is load bearing and is not implied by B and C. Two keys that both resolve to
//	   ONE file would satisfy a key-set assertion and still have lost a module, so
//	   the bytes behind each key are read and compared.
//	F  are negative controls, naming the two shapes that are NOT wanted: the
//	   English-slot fiction, and a namespace taken from the directory name instead
//	   of from <Name> in the manifest.

const (
	// The base configuration's own document module.
	issue46BaseKey = "Документ.ПеремещениеЗапасов.МодульОбъекта"
	// The same logical module inside the extension. The shape is "ext." plus a
	// namespace plus the base-config key for the remainder; extlayout.go:moduleKey
	// takes that namespace from <Name> in the extension's manifest.
	issue46ExtKey = "ext.ИнтеграцияMach3.Документ.ПеремещениеЗапасов.МодульОбъекта"

	// The extension's declared name, which is NOT either of its directory names.
	issue46ExtName = "ИнтеграцияMach3"
	// The prefix, relative to the dump root, at which the extension is rooted.
	issue46ExtPrefix = "Mach3/extension"

	issue46BaseBody = "// базовый\n"
	issue46ExtBody  = "// расширение\n"
)

func TestIssue46_NestedExtensionAtDepthTwoKeepsBothModules(t *testing.T) {
	root := t.TempDir()

	// Every key this package emits has been through NFC, so a decomposed literal
	// below would never match one and the test would fail for a reason that has
	// nothing to do with the defect. Checked rather than assumed.
	for _, k := range []string{issue46BaseKey, issue46ExtKey} {
		if NFC(k) != k {
			t.Fatalf("test literal %q is not NFC, so it can never match an index key", k)
		}
	}

	// The base configuration: its own manifest, and one document module.
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkBSLFile(t, root, "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl", issue46BaseBody)

	// The extension, TWO levels down: a container directory that is not itself an
	// extension, and the extension root inside it. The manifest comes from the
	// shared builder, which reproduces the real byte shape the platform writes
	// (BOM, CRLF, ObjectBelonging and Name inside Properties).
	mkExtensionDump(t, filepath.Join(root, "Mach3", "extension"),
		extManifestClassic, issue46ExtName)
	mkBSLFile(t, root, issue46ExtPrefix+"/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
		issue46ExtBody)

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
	// ModuleNames returns idx.names, which keeps one entry per FILE and therefore
	// carries duplicates when two files collide. Count them, so the report can say
	// which of the two happened.
	seen := make(map[string]int, len(names))
	for _, n := range names {
		seen[n]++
	}

	// A. PREMISE. The tree must have been recognised as holding exactly one
	// extension, rooted at Mach3/extension.
	if len(names) != 2 {
		t.Errorf("the index holds %d module entries, want 2: the walk must have reached "+
			"both .bsl files before anything below can mean anything. Names: %v", len(names), names)
	}
	layout := idx.ExtensionLayout()
	if layout.Extensions != 1 {
		t.Errorf("ExtensionLayout().Extensions = %d, want 1: the extension rooted at %q "+
			"must be detected. Full summary: %+v", layout.Extensions, issue46ExtPrefix, layout)
	}
	foundPrefix := false
	for _, d := range layout.Dirs {
		if filepath.ToSlash(d) == issue46ExtPrefix {
			foundPrefix = true
		}
	}
	if !foundPrefix {
		t.Errorf("ExtensionLayout().Dirs = %v, want it to name %q: the layout must report "+
			"WHERE the extension is, not only that one exists", layout.Dirs, issue46ExtPrefix)
	}

	// B. The base configuration's key survives.
	if seen[issue46BaseKey] == 0 {
		t.Errorf("the base configuration's key %q is absent from the index. Names: %v",
			issue46BaseKey, names)
	}

	// C. The extension's key exists, namespaced by the name its manifest declares.
	if seen[issue46ExtKey] == 0 {
		t.Errorf("the extension's key %q is absent from the index: the module under %q "+
			"was given no namespace. Names: %v", issue46ExtKey, issue46ExtPrefix, names)
	}

	// D. Nothing was overwritten. This is the package's own measurement of the loss.
	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Errorf("CollapsedKeys() = {Files:%d Keys:%d Sample:%v}, want Files 0: a file lost "+
			"its content to an overwrite", st.Files, st.Keys, st.Sample)
	}

	// E. Both modules are still SERVABLE, and they are two different modules. A key
	// set alone cannot say this: two keys resolving to one file would pass B and C.
	baseGot, baseOK := idx.GetContent(issue46BaseKey)
	extGot, extOK := idx.GetContent(issue46ExtKey)
	if !baseOK {
		t.Errorf("GetContent(%q) reported the module missing", issue46BaseKey)
	} else if baseGot != issue46BaseBody {
		t.Errorf("GetContent(%q) = %q, want %q: this key is serving the wrong file's bytes",
			issue46BaseKey, baseGot, issue46BaseBody)
	}
	if !extOK {
		t.Errorf("GetContent(%q) reported the module missing", issue46ExtKey)
	} else if extGot != issue46ExtBody {
		t.Errorf("GetContent(%q) = %q, want %q: this key is serving the wrong file's bytes",
			issue46ExtKey, extGot, issue46ExtBody)
	}
	if baseOK && extOK && baseGot == extGot {
		t.Errorf("both keys served the same bytes %q: two keys pointing at one file is the "+
			"collapse this test exists to catch", baseGot)
	}

	// F. NEGATIVE CONTROLS.
	//
	// The English-slot fiction: this package translates the kind directory and the
	// module file name into Russian, so a key that kept "Document" and
	// "ObjectModule" is a shape it never emits, and a test written against one
	// would be green against nothing.
	const issue46EnglishFiction = "ext.ИнтеграцияMach3.Document.ПеремещениеЗапасов.ObjectModule"
	if seen[issue46EnglishFiction] != 0 {
		t.Errorf("the index emitted %q: the kind and the module file name must be the "+
			"Russian ones the package's own tables produce", issue46EnglishFiction)
	}
	// The directory-name fiction: the namespace comes from <Name> in the manifest,
	// never from a directory. "Mach3" is a directory here and no extension declares
	// it, so no key may carry it as a namespace.
	const issue46DirFiction = "ext.Mach3."
	for _, n := range names {
		if strings.HasPrefix(n, issue46DirFiction) {
			t.Errorf("the index emitted %q, which takes its namespace from the directory "+
				"name; it must come from <Name> in the manifest, which declares %q",
				n, issue46ExtName)
		}
	}
}
