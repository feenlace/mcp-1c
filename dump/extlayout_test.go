package dump

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// extlayout_test.go covers the two dump shapes the 1C platform actually emits for
// configuration extensions, and the base configuration that must not be mistaken
// for either.
//
// WHY THIS EXISTS AT ALL. The package keyed extensions off a directory literally
// named "Расширения", and 1C never writes that directory. It came from one
// customer's hand made folder. What the platform writes is:
//
//	-AllExtensions <dir>      <dir>/<ExtensionName>/<Kind>/...   one subdir each
//	-Extension <Name> <dir>   <dir>/Configuration.xml + kinds at the top level
//
// Both were verified against real output; ~/Downloads/extdump_vm holds two roots
// of the flat kind side by side.
//
// THE NAME COMES FROM THE MANIFEST, NEVER FROM THE DIRECTORY, because in real EDT
// projects the two are reported to disagree (src/cfe/yaxunit holding YAXUNIT), and
// a directory-derived namespace would then key a module under a name nobody can
// ask for. The fixture below is deliberately built with that disagreement, since
// the two real dumps on this machine happen to have matching names and cannot
// exercise it. No Configuration.mdo exists on this machine either, so the EDT rows
// here are fixtures of the reported byte shape and not measurements of one.

// classicExtensionManifest is the real byte shape of an extension's
// Configuration.xml, taken from ~/Downloads/extdump_vm/FeenlaceMCPService: UTF-8
// WITH a BOM, CRLF, the marker inside <Properties>, and <Name> right after it.
func classicExtensionManifest(name string, withPurpose bool) string {
	purpose := ""
	if withPurpose {
		purpose = "\t\t\t<ConfigurationExtensionPurpose>Customization</ConfigurationExtensionPurpose>\r\n"
	}
	return "\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n" +
		"<MetaDataObject xmlns=\"http://v8.1c.ru/8.3/MDClasses\" version=\"2.20\">\r\n" +
		"\t<Configuration uuid=\"4903dffe-5f70-488e-882c-436c910a1d05\">\r\n" +
		"\t\t<InternalInfo>\r\n\t\t\t<xr:ContainedObject/>\r\n\t\t</InternalInfo>\r\n" +
		"\t\t<Properties>\r\n" +
		"\t\t\t<ObjectBelonging>Adopted</ObjectBelonging>\r\n" +
		"\t\t\t<Name>" + name + "</Name>\r\n" +
		purpose +
		"\t\t</Properties>\r\n" +
		"\t</Configuration>\r\n</MetaDataObject>\r\n"
}

// edtExtensionManifest is the EDT shape: camelCase property names and the
// xsi:type attribute that says the mdclass is an extension.
func edtExtensionManifest(name string) string {
	return "\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<mdclass:Configuration xmlns:mdclass=\"http://g5.1c.ru/v8/dt/metadata/mdclass\" " +
		"xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" uuid=\"c1f8\">\n" +
		"  <name>" + name + "</name>\n" +
		"  <objectBelonging>Adopted</objectBelonging>\n" +
		"  <extension xsi:type=\"mdclassExtension:ConfigurationExtension\">\n" +
		"    <configurationExtensionPurpose>Customization</configurationExtensionPurpose>\n" +
		"  </extension>\n" +
		"</mdclass:Configuration>\n"
}

// baseConfigManifest is a CONFIGURATION's own Configuration.xml. Measured on
// dumps/dump_2: 1 339 696 bytes, and it contains neither ObjectBelonging nor
// ConfigurationExtensionPurpose anywhere. That absence is the discriminator.
func baseConfigManifest() string {
	return "\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<MetaDataObject xmlns=\"http://v8.1c.ru/8.3/MDClasses\" version=\"2.20\">\n" +
		"\t<Configuration uuid=\"aaaa\">\n\t\t<Properties>\n" +
		"\t\t\t<Name>УправлениеТорговлей</Name>\n" +
		"\t\t</Properties>\n\t</Configuration>\n</MetaDataObject>\n"
}

func mkExtensionDump(t *testing.T, dir, manifestName, extName string, kinds ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body string
	switch manifestName {
	case extManifestEDT:
		body = edtExtensionManifest(extName)
	default:
		body = classicExtensionManifest(extName, true)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, k := range kinds {
		if err := os.MkdirAll(filepath.Join(dir, k), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestExtensionIsIdentifiedFromTheManifestNotTheDirectory is the central claim.
func TestExtensionIsIdentifiedFromTheManifestNotTheDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cfe_yaxunit_source_dir")
	mkExtensionDump(t, root, extManifestClassic, "YAXUNIT", "Catalogs", "CommonModules")

	got := detectExtensionLayout(root)
	if got.self != "YAXUNIT" {
		t.Errorf("self = %q, want %q: the extension name must come from <Name> in the "+
			"manifest, not from the directory (%q)", got.self, "YAXUNIT", filepath.Base(root))
	}
	if len(got.byDir) != 0 {
		t.Errorf("byDir = %v, want empty: a flat extension root is not a container of them", got.byDir)
	}
	// Negative control: the directory name really does differ, so the assertion
	// above is not satisfied by the two happening to agree.
	if filepath.Base(root) == "YAXUNIT" {
		t.Fatal("control broken: the fixture directory is named after the extension")
	}
}

// TestExtensionMarkersAcrossBothFormats pins what makes something an extension,
// and in particular what does NOT.
func TestExtensionMarkersAcrossBothFormats(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		body     string
		wantName string
	}{
		{"classic XML with the purpose element", extManifestClassic,
			classicExtensionManifest("РасширениеА", true), "РасширениеА"},
		{"classic XML with NO purpose element: the pre 2.16 export", extManifestClassic,
			classicExtensionManifest("РасширениеБ", false), "РасширениеБ"},
		{"EDT mdo, camelCase and xsi:type", extManifestEDT,
			edtExtensionManifest("YAXUNIT"), "YAXUNIT"},
		{"a base configuration is NOT an extension", extManifestClassic,
			baseConfigManifest(), ""},
		{"an empty manifest is not an extension", extManifestClassic, "", ""},
		{"the purpose element ALONE does not make an extension", extManifestClassic,
			"\ufeff<MetaDataObject><Configuration><Properties>" +
				"<Name>X</Name><ConfigurationExtensionPurpose>Patch</ConfigurationExtensionPurpose>" +
				"</Properties></Configuration></MetaDataObject>", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tt.file), []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok := extensionNameOf(dir)
			if tt.wantName == "" {
				if ok {
					t.Errorf("extensionNameOf = (%q, true), want not an extension", got)
				}
				return
			}
			if !ok || got != tt.wantName {
				t.Errorf("extensionNameOf = (%q, %v), want (%q, true)", got, ok, tt.wantName)
			}
		})
	}
}

// TestPurposeElementIsNotTheGate states the compatibility fact as a test rather
// than as a comment. ConfigurationExtensionPurpose arrived in format 2.16
// defaulting to Patch, so an older export has none at all, and reading "no purpose
// element" as "not an extension" would drop every extension exported before that.
func TestPurposeElementIsNotTheGate(t *testing.T) {
	dir := t.TempDir()
	body := classicExtensionManifest("Старое", false)
	if strings.Contains(body, "ConfigurationExtensionPurpose") {
		t.Fatal("premise broken: the fixture for a pre 2.16 export contains the purpose element")
	}
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); !ok || got != "Старое" {
		t.Errorf("extensionNameOf = (%q, %v), want (\"Старое\", true)", got, ok)
	}
}

// TestBOMDoesNotHideTheMarker pins the encoding trap: 1C writes UTF-8 WITH a byte
// order mark, and bytes.TrimSpace does not strip U+FEFF.
//
// THE FIRST HALF OF THIS TEST WOULD PASS WITHOUT THE STRIP, and saying so is the
// point rather than a caveat. Every match in extlayout.go is a bytes.Index or a
// bytes.Contains, which are position independent, so a leading U+FEFF cannot hide
// a marker sitting inside the document however the head is trimmed. That half is a
// regression guard on the recognition, not evidence that the strip does anything.
//
// The second half is what makes the strip itself testable: manifestHead is
// asserted to hand back bytes that begin AT THE DOCUMENT. Without that assertion
// the TrimPrefix could be deleted and this whole test would stay green, which is
// the definition of a test that cannot fail.
func TestBOMDoesNotHideTheMarker(t *testing.T) {
	withBOM := classicExtensionManifest("СБОМ", true)
	if !strings.HasPrefix(withBOM, "\ufeff") {
		t.Fatal("premise broken: the fixture has no BOM, so this test proves nothing")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(withBOM), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); !ok || got != "СБОМ" {
		t.Errorf("a manifest with a BOM was not recognised: (%q, %v)", got, ok)
	}
	// And a name that would carry the BOM through into the key does not: the name
	// is read from inside the document, so it can never begin with U+FEFF.
	if got, _ := extensionNameOf(dir); strings.ContainsRune(got, '\ufeff') {
		t.Errorf("the extension name carries a BOM: %q", got)
	}

	// The strip, asserted directly. bytes.TrimSpace does NOT remove U+FEFF, so a
	// head that still carried it would break any check anchored at the start,
	// including one added later by somebody who read the doc comment and believed
	// the head begins at the document.
	head, ok := manifestHead(filepath.Join(dir, extManifestClassic))
	if !ok {
		t.Fatal("manifestHead could not read the manifest it just wrote")
	}
	if bytes.HasPrefix(head, []byte("\ufeff")) {
		t.Error("manifestHead returned a head that still begins with U+FEFF")
	}
	if !bytes.HasPrefix(head, []byte("<?xml")) {
		t.Errorf("manifestHead does not begin at the document: %.20q", head)
	}
	// Positive control: the bytes on disk really do start with the BOM, so the two
	// assertions above are about the strip and not about a fixture that never had
	// one.
	raw, err := os.ReadFile(filepath.Join(dir, extManifestClassic))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("\ufeff")) {
		t.Fatal("positive control failed: the file on disk has no BOM")
	}
}

// TestAllExtensionsShapeNamesEveryChild covers -AllExtensions output.
func TestAllExtensionsShapeNamesEveryChild(t *testing.T) {
	root := t.TempDir()
	mkExtensionDump(t, filepath.Join(root, "dirA"), extManifestClassic, "РасширениеА", "Catalogs")
	mkExtensionDump(t, filepath.Join(root, "dirB"), extManifestEDT, "YAXUNIT", "CommonModules")
	// A sibling that is not an extension at all.
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := detectExtensionLayout(root)
	if got.self != "" {
		t.Errorf("self = %q, want empty: the container is not itself an extension", got.self)
	}
	want := map[string]string{"dirA": "РасширениеА", "dirB": "YAXUNIT"}
	if len(got.byDir) != len(want) {
		t.Fatalf("byDir = %v, want %v", got.byDir, want)
	}
	for d, n := range want {
		if got.byDir[d] != n {
			t.Errorf("byDir[%q] = %q, want %q", d, got.byDir[d], n)
		}
	}
	if _, ok := got.byDir["notes"]; ok {
		t.Error("a directory with no extension manifest was admitted as an extension")
	}
}

// TestExtensionKeysAreNamespacedInBothShapes is the outcome the whole cluster is
// for: an extension module gets an ext.<Имя>. key instead of merging into the base
// configuration, in either layout, and the key is the SAME one the Расширения
// layout has always produced.
func TestExtensionKeysAreNamespacedInBothShapes(t *testing.T) {
	const want = "ext.РасширениеА.Справочник.Номенклатура.МодульОбъекта"

	flat := extensionLayout{self: "РасширениеА"}
	if got := flat.moduleKey("Catalogs/Номенклатура/Ext/ObjectModule.bsl"); got != want {
		t.Errorf("flat -Extension shape: %q, want %q", got, want)
	}

	all := extensionLayout{byDir: map[string]string{"dirA": "РасширениеА"}}
	if got := all.moduleKey("dirA/Catalogs/Номенклатура/Ext/ObjectModule.bsl"); got != want {
		t.Errorf("-AllExtensions shape: %q, want %q", got, want)
	}

	// The historical layout keeps working, and produces the same key, without any
	// layout at all: real customers have such trees because they made them.
	none := extensionLayout{}
	if got := none.moduleKey("Расширения/РасширениеА/Catalogs/Номенклатура/Ext/ObjectModule.bsl"); got != want {
		t.Errorf("Расширения layout: %q, want %q", got, want)
	}

	// A child that is NOT a known extension is untouched by the -AllExtensions
	// layout, so a container holding one extension does not re prefix its siblings.
	if got, want := all.moduleKey("other/Catalogs/Номенклатура/Ext/ObjectModule.bsl"),
		bslPathToModuleName("other/Catalogs/Номенклатура/Ext/ObjectModule.bsl"); got != want {
		t.Errorf("an unknown sibling keyed as %q, want %q", got, want)
	}

	// An empty layout changes nothing whatsoever: this is the property that keeps
	// every base configuration on the keys it already had.
	for _, p := range unwrappedKeyDigestCorpus {
		if got, want := none.moduleKey(p), bslPathToModuleName(p); got != want {
			t.Errorf("empty layout moved %q: %q, want %q", p, got, want)
		}
	}
}

// TestACommonFormNamedРасширенияIsNotAnExtensionContainer keeps the older
// invariant true. A real configuration contains a CommonForm literally named
// «Расширения», and nothing here may read that word as a container.
func TestACommonFormNamedРасширенияIsNotAnExtensionContainer(t *testing.T) {
	root := t.TempDir()
	// A directory named Расширения at the top of the dump with NO manifest inside.
	if err := os.MkdirAll(filepath.Join(root, extensionDirName, "Что-то"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := detectExtensionLayout(root)
	if _, ok := got.byDir[extensionDirName]; ok {
		t.Error("a directory named Расширения with no manifest was admitted as an extension; " +
			"the detection must key on the manifest and never on the name")
	}
	// And the key for the CommonForm keeps its shape.
	const form = "CommonForms/Расширения/Ext/Form/Module.bsl"
	if got := (extensionLayout{}).moduleKey(form); got != "ОбщаяФорма.Расширения.МодульФормы" {
		t.Errorf("moduleKey(%q) = %q, want %q", form, got, "ОбщаяФорма.Расширения.МодульФормы")
	}
}

// TestBaseConfigurationManifestIsNeverReadAsAnExtension runs against the real
// 1 339 696 byte manifest on this machine, not a fixture. A miss here would key
// every module of a whole configuration under an ext. prefix.
func TestBaseConfigurationManifestIsNeverReadAsAnExtension(t *testing.T) {
	const base = "/Users/igoroot/GolandProjects/mcp/dumps/dump_2"
	if _, err := os.Stat(filepath.Join(base, "Configuration.xml")); err != nil {
		t.Skip("dumps/dump_2 is absent on this machine")
	}
	got := detectExtensionLayout(base)
	if got.self != "" || len(got.byDir) != 0 {
		t.Fatalf("a real base configuration was read as an extension layout: self=%q byDir=%v",
			got.self, got.byDir)
	}
	// And the layout it produces changes no key at all.
	const p = "Catalogs/Номенклатура/Ext/ObjectModule.bsl"
	if k, want := got.moduleKey(p), bslPathToModuleName(p); k != want {
		t.Errorf("moduleKey = %q, want %q", k, want)
	}
}

// TestRealExtensionDumpsOnDiskAreRecognised runs the detector against the two real
// extension dumps produced by 8.3.27.2130.
func TestRealExtensionDumpsOnDiskAreRecognised(t *testing.T) {
	const container = "/Users/igoroot/Downloads/extdump_vm"
	if _, err := os.Stat(container); err != nil {
		t.Skip("extdump_vm is absent on this machine")
	}
	// Each root on its own is the flat -Extension shape.
	for dir, want := range map[string]string{
		"FeenlaceMCPService": "FeenlaceMCPService",
		"mcp_service":        "mcp_service",
	} {
		got := detectExtensionLayout(filepath.Join(container, dir))
		if got.self != want {
			t.Errorf("detectExtensionLayout(%s).self = %q, want %q", dir, got.self, want)
		}
	}
	// The container of the two is the -AllExtensions shape.
	got := detectExtensionLayout(container)
	if got.self != "" {
		t.Errorf("the container reported itself as extension %q", got.self)
	}
	if len(got.byDir) != 2 {
		t.Fatalf("byDir = %v, want both extensions", got.byDir)
	}
}

// TestManifestReadingIsBoundedAndDegrades pins the cost rule. The manifests that
// matter are small (3754 and 3536 bytes for the two real ones), a configuration's
// is not (1 339 696 bytes), and its ConfigDumpInfo.xml is 20 591 903 bytes and is
// never opened here at all.
func TestManifestReadingIsBoundedAndDegrades(t *testing.T) {
	dir := t.TempDir()
	// A manifest whose marker sits beyond the window: the answer degrades to "not
	// an extension", which is exactly today's behaviour, and never to a failure.
	padded := "\ufeff<MetaDataObject>" + strings.Repeat(" ", maxManifestHeadBytes+1024) +
		"<Properties><ObjectBelonging>Adopted</ObjectBelonging><Name>X</Name></Properties></MetaDataObject>"
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(padded), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); ok {
		t.Errorf("a marker beyond the %d byte window was found anyway (%q); the read is not bounded",
			maxManifestHeadBytes, got)
	}
	// Positive control: the same manifest with the padding removed IS recognised,
	// so the miss above is the window and not the parser.
	small := strings.Replace(padded, strings.Repeat(" ", maxManifestHeadBytes+1024), "", 1)
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(small), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); !ok || got != "X" {
		t.Fatalf("positive control failed: the unpadded manifest gave (%q, %v)", got, ok)
	}
	// A directory with no manifest at all is silent, not an error.
	if _, ok := extensionNameOf(t.TempDir()); ok {
		t.Error("a directory with no manifest was reported as an extension")
	}
}

// TestExtensionModulesNoLongerOverwriteTheBaseConfiguration is the end to end
// measurement of the defect this cluster exists for, taken through a real Index
// rather than through the key function.
//
// The tree is the customer's: a base configuration and an extension of it under
// one --dump, with a module of the SAME kind and name in both. Before extension
// layouts existed, the anchor scan gave both files the identical key and the
// second one read silently overwrote the first.
func TestExtensionModulesNoLongerOverwriteTheBaseConfiguration(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The base configuration, as a proper dump root.
	write("main/Catalogs/Номенклатура/Ext/ObjectModule.bsl", "// базовый модуль\n")
	write("main/Configuration.xml", baseConfigManifest())
	// The extension beside it, in -AllExtensions shape, with a colliding module.
	write("ext/Catalogs/Номенклатура/Ext/ObjectModule.bsl", "// модуль расширения\n")
	write("ext/Configuration.xml", classicExtensionManifest("МоёРасширение", true))

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	<-idx.Done()

	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Errorf("CollapsedKeys().Files = %d (%v), want 0: an extension module is still "+
			"overwriting the base configuration module it collides with", st.Files, st.Sample)
	}
	names := idx.ModuleNames()
	wantBase := "Справочник.Номенклатура.МодульОбъекта"
	wantExt := "ext.МоёРасширение.Справочник.Номенклатура.МодульОбъекта"
	has := func(n string) bool {
		for _, s := range names {
			if s == n {
				return true
			}
		}
		return false
	}
	if !has(wantBase) {
		t.Errorf("the base configuration module is missing from %v; want %q", names, wantBase)
	}
	if !has(wantExt) {
		t.Errorf("the extension module is missing from %v; want %q", names, wantExt)
	}
	// And both are readable, which is the property a distinct key is FOR.
	for name, want := range map[string]string{
		wantBase: "базовый модуль",
		wantExt:  "модуль расширения",
	} {
		got, ok := idx.GetContent(name)
		if !ok {
			t.Errorf("GetContent(%q) found nothing", name)
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("GetContent(%q) = %q, want it to contain %q", name, got, want)
		}
	}
}
