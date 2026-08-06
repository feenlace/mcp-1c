package dump

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
// THE NAME COMES FROM THE MANIFEST, NEVER FROM THE DIRECTORY, and that is measured
// on this machine rather than reported from elsewhere: ~/Downloads/canon_vm
// declares <Name>FeenlaceMCPService</Name> and ~/Downloads/mcp-modified declares
// <Name>MCP_Polling</Name>. Two of the four real extension dumps here therefore
// disagree with their own directory name, and a directory-derived namespace would
// key their modules under a name nobody can ask for.
//
// THERE ARE NO EDT ROWS. No Configuration.mdo exists on this machine, so a fixture
// written to the reported byte shape would be a fixture of a belief, and a test
// built on one claims coverage it does not have. The detector has no EDT branch at
// all; TestEDTIsNotClaimedAndNotDetected is the pin that says so out loud.

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
	body := classicExtensionManifest(extName, true)
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
	// CONTROL THAT EXERCISES THE PRODUCTION CODE. Comparing filepath.Base(root)
	// with "YAXUNIT" would compare two literals and prove nothing about the
	// detector. Removing the manifest from the SAME directory does: if the name
	// were coming from the path, it would still be there.
	if err := os.Remove(filepath.Join(root, extManifestClassic)); err != nil {
		t.Fatal(err)
	}
	if got := detectExtensionLayout(root); got.self != "" {
		t.Errorf("with the manifest deleted, self = %q: the name is coming from "+
			"somewhere other than the manifest", got.self)
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
	path := filepath.Join(dir, extManifestClassic)
	lst, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	head, complete, reason := readManifestHead(path, lst)
	if reason != 0 {
		t.Fatalf("readManifestHead refused the manifest it just wrote: reason %d", reason)
	}
	if !complete {
		t.Error("a 464 byte manifest was reported as truncated")
	}
	if bytes.HasPrefix(head, []byte("\ufeff")) {
		t.Error("readManifestHead returned a head that still begins with U+FEFF")
	}
	if !bytes.HasPrefix(head, []byte("<?xml")) {
		t.Errorf("readManifestHead does not begin at the document: %.20q", head)
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
	mkExtensionDump(t, filepath.Join(root, "dirB"), extManifestClassic, "YAXUNIT", "CommonModules")
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
	//
	// MEASURED AGAINST THE PINNED DIGEST, not against bslPathToModuleName. Writing
	// `none.moduleKey(p) != bslPathToModuleName(p)` compares two spellings of one
	// expression the moment the empty layout falls through to that call, so the
	// loop would run its 40 iterations and be incapable of failing. The literal
	// below is the number module_key_guard_test.go pins for the same corpus, so a
	// namespace appearing on an empty layout moves it.
	var sb strings.Builder
	for _, p := range unwrappedKeyDigestCorpus {
		sb.WriteString(p)
		sb.WriteByte('\t')
		sb.WriteString(none.moduleKey(p))
		sb.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(sb.String()))
	if got := hex.EncodeToString(sum[:]); got != bslUnwrappedCorpusDigest {
		t.Errorf("the empty layout moved a key over the unwrapped corpus:\ndigest %s\nwant   %s",
			got, bslUnwrappedCorpusDigest)
	}
	// Positive control for the digest itself: a NON-empty layout must move it, or
	// the comparison above would pass for every layout there is.
	sb.Reset()
	moved := extensionLayout{self: "Контроль"}
	for _, p := range unwrappedKeyDigestCorpus {
		sb.WriteString(p)
		sb.WriteByte('\t')
		sb.WriteString(moved.moduleKey(p))
		sb.WriteByte('\n')
	}
	sum = sha256.Sum256([]byte(sb.String()))
	if got := hex.EncodeToString(sum[:]); got == bslUnwrappedCorpusDigest {
		t.Error("positive control failed: a self-named layout produced the unwrapped digest")
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
	// And the key it produces is the base-configuration one, written out as a
	// literal. Comparing got.moduleKey(p) with bslPathToModuleName(p) here cannot
	// fail: the Fatalf above has already established that the layout is empty, and
	// an empty layout IS that call.
	const p = "Catalogs/Номенклатура/Ext/ObjectModule.bsl"
	if k := got.moduleKey(p); k != "Справочник.Номенклатура.МодульОбъекта" {
		t.Errorf("moduleKey(%q) = %q, want %q", p, k, "Справочник.Номенклатура.МодульОбъекта")
	}
	// The read is BOUNDED and the answer is still definite: this manifest is
	// 1 339 696 bytes, far past the window, and its <Properties> closes at 12718,
	// far inside it. So it is decided, and decided with no doubt recorded.
	if len(got.doubts) != 0 {
		t.Errorf("a base configuration produced doubts %v; its <Properties> closes "+
			"inside the read window, so the answer is definite", got.doubts)
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
	// And it does not go quiet about it. The window is a limit of the reader, not
	// a fact about the dump, so the answer is "undecided" and it is recorded.
	if l := detectExtensionLayout(dir); len(l.doubts) != 1 || l.doubts[0].reason != doubtManifestTruncated {
		t.Errorf("doubts = %v, want exactly one truncation doubt: a manifest the "+
			"reader could not finish was silently demoted to the base keyspace", l.doubts)
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

// ---------------------------------------------------------------------------
// The contract: three answers, evidence that is constrained before it is read,
// and a name that has to be usable before it becomes one.
// ---------------------------------------------------------------------------

// TestEDTIsNotClaimedAndNotDetected is the pin on a deliberate absence.
//
// The removed branch gated on a NAKED SUBSTRING, so a document merely mentioning
// "mdclassExtension:ConfigurationExtension" anywhere at all was an extension. The
// head below is a BASE configuration that mentions it in a comment, and it was
// accepted, under the configuration's own name. That false positive re-keys every
// module in the dump.
//
// The branch is gone rather than tightened because there is no Configuration.mdo
// on this machine: any strictness written for it would come from the same reported
// byte shape as the loose version, and a guard invented from an unverifiable
// specification is not a guard. NO TEST HERE CLAIMS EDT COVERAGE, and this one says
// so by asserting the opposite.
func TestEDTIsNotClaimedAndNotDetected(t *testing.T) {
	const baseWithTheMarkerInAComment = "\ufeff<?xml version=\"1.0\"?>\n" +
		"<mdclass:Configuration xmlns:mdclass=\"http://g5.1c.ru/v8/dt/metadata/mdclass\"\n" +
		"  xmlns:mdclassExtension=\"http://g5.1c.ru/v8/dt/metadata/mdclass/extension\">\n" +
		"  <name>УправлениеНебольшойФирмой</name>\n" +
		"  <objectBelonging>Adopted</objectBelonging>\n" +
		"  <comment>перенесено из mdclassExtension:ConfigurationExtension</comment>\n" +
		"</mdclass:Configuration>\n"

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Configuration.mdo"),
		[]byte(baseWithTheMarkerInAComment), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); ok {
		t.Errorf("a Configuration.mdo was read as extension %q; there is no EDT branch "+
			"and there must not be one written from a shape nothing on this machine can check", got)
	}
	if l := detectExtensionLayout(dir); !l.empty() {
		t.Errorf("a Configuration.mdo produced a layout: self=%q byDir=%v", l.self, l.byDir)
	}

	// POSITIVE CONTROL: the reader is not simply broken. The classic manifest
	// dropped into the same directory IS recognised, so the silence above is about
	// the .mdo and not about the fixture.
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic),
		[]byte(classicExtensionManifest("Настоящее", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); !ok || got != "Настоящее" {
		t.Fatalf("positive control failed: the classic manifest beside the .mdo gave (%q, %v)", got, ok)
	}
}

// TestASymlinkedManifestIsRefusedRatherThanFollowed closes the one read in this
// package that no containment covered. Every other file the index opens goes
// through pathWithinRoot; this one was a bare os.Open on a joined path, so a
// symlink named Configuration.xml pointed the namespace of a whole dump at a
// document outside the root.
func TestASymlinkedManifestIsRefusedRatherThanFollowed(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "someone_elses.xml")
	if err := os.WriteFile(target, []byte(classicExtensionManifest("ЧужоеИмя", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, extManifestClassic)); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); ok {
		t.Errorf("a symlinked manifest was followed out of the root and named the dump %q", got)
	}
	l := detectExtensionLayout(dir)
	if len(l.doubts) != 1 || l.doubts[0].reason != doubtManifestNotRegular {
		t.Errorf("doubts = %v, want one non-regular-file doubt: refusing quietly is how "+
			"the read came to have no containment in the first place", l.doubts)
	}

	// POSITIVE CONTROL: THE SAME BYTES, as a real file in the same place, are read.
	// Without this the assertion above would also pass if the reader were broken.
	if err := os.Remove(filepath.Join(dir, extManifestClassic)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); !ok || got != "ЧужоеИмя" {
		t.Fatalf("positive control failed: the same bytes as a regular file gave (%q, %v)", got, ok)
	}
}

// TestAManifestNameIsMatchedByteExactly makes the two files agree about one
// question. extlayout.go decided it by opening a joined path, which macOS answers
// case-insensitively; dumproot.go decided it by comparing a directory entry, which
// is byte-exact. So one tree was an extension to one file and an ordinary directory
// to the other, and the disagreement INVERTS on a case-sensitive volume.
func TestAManifestNameIsMatchedByteExactly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "configuration.xml"),
		[]byte(classicExtensionManifest("Строчными", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, extManifestClassic)); err != nil {
		t.Skip("this volume is case-sensitive, so the two rules cannot disagree here")
	}

	_, ok := extensionNameOf(dir)
	isRoot := InspectDumpRoot(dir).IsRoot
	if ok {
		t.Error("a lowercase configuration.xml was read as an extension manifest")
	}
	if ok != isRoot {
		t.Errorf("the two files disagree about the same tree: extensionNameOf=%v InspectDumpRoot.IsRoot=%v", ok, isRoot)
	}

	// POSITIVE CONTROL: the byte-exact name in a fresh directory IS read, and both
	// files then say the same thing.
	exact := t.TempDir()
	if err := os.WriteFile(filepath.Join(exact, extManifestClassic),
		[]byte(classicExtensionManifest("Точно", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(exact); !ok || got != "Точно" {
		t.Fatalf("positive control failed: (%q, %v)", got, ok)
	}
	if !InspectDumpRoot(exact).IsRoot {
		t.Error("the byte-exact manifest is an extension to one file and not a root to the other")
	}
}

// TestAContainerThatCarriesItsOwnManifestDoesNotSwallowItsChildren.
//
// Detection stopped at the root: if the root declared a name, no child was looked
// at, so EVERY module under EVERY child was filed under the container's name and
// the children collided with each other. That is precedence standing in for a
// decision. The children are examined now whatever the root says, and the more
// specific evidence wins.
func TestAContainerThatCarriesItsOwnManifestDoesNotSwallowItsChildren(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(classicExtensionManifest("Контейнер", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	// The container's own content, in the flat -Extension shape.
	if err := os.MkdirAll(filepath.Join(root, "CommonModules"), 0o755); err != nil {
		t.Fatal(err)
	}
	// And two nested extensions under their own directories.
	mkExtensionDump(t, filepath.Join(root, "dirA"), extManifestClassic, "ИмяА", "Catalogs")
	mkExtensionDump(t, filepath.Join(root, "dirB"), extManifestClassic, "ИмяБ", "Catalogs")

	l := detectExtensionLayout(root)
	if l.self != "Контейнер" {
		t.Errorf("self = %q, want Контейнер", l.self)
	}
	if l.byDir["dirA"] != "ИмяА" || l.byDir["dirB"] != "ИмяБ" {
		t.Fatalf("byDir = %v, want both nested extensions named", l.byDir)
	}

	const rel = "Catalogs/Ном/Ext/ObjectModule.bsl"
	a := l.moduleKey("dirA/" + rel)
	b := l.moduleKey("dirB/" + rel)
	if a == b {
		t.Errorf("both nested extensions key to %q: the container swallowed them", a)
	}
	if a != "ext.ИмяА.Справочник.Ном.МодульОбъекта" {
		t.Errorf("dirA keyed as %q", a)
	}
	if b != "ext.ИмяБ.Справочник.Ном.МодульОбъекта" {
		t.Errorf("dirB keyed as %q", b)
	}
	// What is left over is still the container's own.
	if got, want := l.moduleKey("CommonModules/X/Ext/Module.bsl"), "ext.Контейнер.ОбщийМодуль.X.Модуль"; got != want {
		t.Errorf("the container's own module keyed as %q, want %q", got, want)
	}
}

// TestAKindDirectoryUnderAnExtensionRootIsNotANestedExtension is the other half of
// the rule above: examining every child is only safe if a child that can only be
// the root extension's own content is not eligible to be a nested one.
func TestAKindDirectoryUnderAnExtensionRootIsNotANestedExtension(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(classicExtensionManifest("Плоское", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	// A planted manifest inside a metadata kind directory of the extension itself.
	kind := filepath.Join(root, "Catalogs")
	if err := os.MkdirAll(kind, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kind, extManifestClassic),
		[]byte(classicExtensionManifest("Подделка", true)), 0o644); err != nil {
		t.Fatal(err)
	}

	l := detectExtensionLayout(root)
	if _, ok := l.byDir["Catalogs"]; ok {
		t.Errorf("a metadata kind directory of the root extension was admitted as a "+
			"nested extension: byDir = %v", l.byDir)
	}
	if got, want := l.moduleKey("Catalogs/Ном/Ext/ObjectModule.bsl"),
		"ext.Плоское.Справочник.Ном.МодульОбъекта"; got != want {
		t.Errorf("moduleKey = %q, want %q", got, want)
	}

	// POSITIVE CONTROL: the same planted manifest under a NON-kind directory of a
	// root that is not itself an extension IS admitted, so the exclusion above is
	// the kind name and not a blanket refusal.
	other := t.TempDir()
	mkExtensionDump(t, filepath.Join(other, "Catalogs"), extManifestClassic, "Подделка", "Ext")
	if got := detectExtensionLayout(other); got.byDir["Catalogs"] != "Подделка" {
		t.Fatalf("positive control failed: byDir = %v", got.byDir)
	}
}

// TestTheChildScanRecordsWhereItStopped. The cap turned a lossless tree lossy and
// had no way to say so: extensions past the cap key into the base keyspace and
// collide with each other. Measured on 70 extension directories, the loss was 5
// files with a worst bucket of 6.
func TestTheChildScanRecordsWhereItStopped(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxExtensionScan+6; i++ {
		mkExtensionDump(t, filepath.Join(root, fmt.Sprintf("dir%02d", i)),
			extManifestClassic, fmt.Sprintf("Расш%02d", i), "Catalogs")
	}
	l := detectExtensionLayout(root)
	if len(l.byDir) != maxExtensionScan {
		t.Errorf("byDir has %d entries, want %d", len(l.byDir), maxExtensionScan)
	}
	if !slices.ContainsFunc(l.doubts, func(d layoutDoubt) bool { return d.reason == doubtScanTruncated }) {
		t.Errorf("doubts = %v, want a truncation doubt: 'no extension below this' and "+
			"'no extension among the first %d directories' are different answers",
			l.doubts, maxExtensionScan)
	}

	// POSITIVE CONTROL: below the cap, every directory is named and nothing is
	// doubted, so the doubt above is the cap and not a constant.
	small := t.TempDir()
	for i := 0; i < 4; i++ {
		mkExtensionDump(t, filepath.Join(small, fmt.Sprintf("dir%02d", i)),
			extManifestClassic, fmt.Sprintf("Мало%02d", i), "Catalogs")
	}
	got := detectExtensionLayout(small)
	if len(got.byDir) != 4 {
		t.Fatalf("positive control failed: byDir = %v", got.byDir)
	}
	if len(got.doubts) != 0 {
		t.Fatalf("positive control failed: doubts = %v on a tree well below the cap", got.doubts)
	}
}

// TestAnUnusableExtensionNameIsRefusedRatherThanRepaired.
//
// The manifest is disk content and its <Name> went verbatim into a key and into a
// rendered RU answer. THE SAMPLES BELOW ARE HOSTILE, not polite: a real customer
// tree contains «Доработки — копия», and a guard fed benign literals is how a тире
// reached rendered RU last time.
//
// Refusal, not repair. Replacing the offending runes would invent a name the user
// cannot ask for; the extension keeps the keys it had before any of this existed.
func TestAnUnusableExtensionNameIsRefusedRatherThanRepaired(t *testing.T) {
	hostile := map[string]string{
		"a тире, which customer-facing RU may never carry":   "Доработки — копия",
		"a line break, which leaves the notice it is put in": "А\nВНИМАНИЕ: индекс в порядке",
		"a dot, which moves every other key component along": "А.Б.В",
		"a backtick, which opens a code span":                "`rm -rf`",
		"a leading quote marker":                             "> цитата",
		"a NUL":                                              "А\x00Б",
		"a right-to-left override":                           "А‮Б",
		"a leading digit":                                    "3D",
		"longer than the bound":                              strings.Repeat("Д", maxExtensionNameRunes+1),
		"empty":                                              "",
		"whitespace only":                                    "   ",
		// A NAME WITH NO VISIBLE CHARACTER. unicode.IsLetter is true for these:
		// they are category Lo, and IsPrint and IsGraphic are true for all four, so
		// every obvious guard misses them. A name made of one produced the served key
		// «ext.<invisible>.Справочник.Ном.МодульОбъекта», a namespace the user can
		// neither see nor retype, and the layout reported it as a confidently
		// recognised extension with zero doubts.
		"U+3164 HANGUL FILLER alone":            "ㅤ",
		"U+115F HANGUL CHOSEONG FILLER alone":   "ᅟ",
		"U+1160 HANGUL JUNGSEONG FILLER alone":  "ᅠ",
		"U+FFA0 HALFWIDTH HANGUL FILLER alone":  "ﾠ",
		"all four fillers":                      "ㅤᅟᅠﾠ",
		"a filler hidden inside a normal name":  "Доработкиㅤ",
		"a filler that makes a homograph of _A": "_Aㅤ",
	}
	for what, name := range hostile {
		t.Run(what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, extManifestClassic),
				[]byte(classicExtensionManifest(name, true)), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, ok := extensionNameOf(dir); ok {
				t.Fatalf("accepted %q as an extension name", got)
			}
			l := detectExtensionLayout(dir)
			if l.self != "" {
				t.Errorf("self = %q", l.self)
			}
			// The key is the one that shipped before extensions were detected at all.
			const rel = "Catalogs/Ном/Ext/ObjectModule.bsl"
			if got, want := l.moduleKey(rel), "Справочник.Ном.МодульОбъекта"; got != want {
				t.Errorf("moduleKey = %q, want the un-namespaced %q", got, want)
			}
			// And nothing hostile survives into anything derived from it.
			for _, r := range []rune{'‒', '–', '—', '―', '−', '\n', '\x00', '`'} {
				if strings.ContainsRune(l.moduleKey(rel), r) {
					t.Errorf("the key carries U+%04X", r)
				}
			}
		})
	}

	// POSITIVE CONTROL: every extension name that exists on this machine passes,
	// so the rule above is an allowlist of what a key component may be and not a
	// refusal of everything.
	for _, name := range []string{"FeenlaceMCPService", "mcp_service", "MCP_Polling", "Доработки3D", "YAXUNIT", "_A"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, extManifestClassic),
			[]byte(classicExtensionManifest(name, true)), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, ok := extensionNameOf(dir); !ok || got != name {
			t.Errorf("a real extension name was refused: %q -> (%q, %v)", name, got, ok)
		}
	}
}

// TestAMarkerInACommentIsNotEvidence.
//
// This file criticises the branch it deleted for accepting a marker found «anywhere
// at all, a comment included», and then did the same thing one scope down: the
// surviving check is scoped to <Properties>, which constrains WHERE in the document
// a match may sit and says nothing about WHAT KIND OF NODE it is. An XML comment
// sits inside <Properties> perfectly happily.
//
// Every row below was measured against the real detector before the fix, and none
// of them is a mere false positive in the harmless direction: two mint an extension
// out of a document that declares none, and the third RENAMES a genuine extension,
// because bytes.Index takes the first hit and a commented block can be placed before
// the real one.
func TestAMarkerInACommentIsNotEvidence(t *testing.T) {
	const props = "<ObjectBelonging>Adopted</ObjectBelonging>"
	wrap := func(inner string) string {
		return "\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
			"<MetaDataObject xmlns=\"http://v8.1c.ru/8.3/MDClasses\" version=\"2.20\">\n" +
			"\t<Configuration uuid=\"aaaa\">\n" + inner + "\n\t</Configuration>\n</MetaDataObject>\n"
	}

	for what, body := range map[string]string{
		"the marker only inside a comment": wrap(
			"\t\t<Properties>\n\t\t\t<!-- было: " + props + " -->\n" +
				"\t\t\t<Name>УправлениеТорговлей</Name>\n\t\t</Properties>"),
		"the whole <Properties> commented out": wrap(
			"\t\t<!--\n\t\t<Properties>\n\t\t\t" + props + "\n" +
				"\t\t\t<Name>Призрак</Name>\n\t\t</Properties>\n\t\t-->"),
		"the marker inside CDATA": wrap(
			"\t\t<Properties>\n\t\t\t<Comment><![CDATA[" + props + "]]></Comment>\n" +
				"\t\t\t<Name>УправлениеТорговлей</Name>\n\t\t</Properties>"),
	} {
		t.Run(what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, ok := extensionNameOf(dir); ok {
				t.Errorf("a marker that is not part of the document structure minted the "+
					"extension %q", got)
			}
			if l := detectExtensionLayout(dir); l.self != "" {
				t.Errorf("self = %q", l.self)
			}
		})
	}

	// A COMMENTED BLOCK BEFORE THE REAL ONE MUST NOT RENAME THE EXTENSION. This is
	// the row that is not about false positives at all: the document declares one
	// genuine extension, and the answer has to be its name.
	dir := t.TempDir()
	shadow := wrap("\t\t<!-- <Properties>" + props + "<Name>Подставное</Name></Properties> -->\n" +
		"\t\t<Properties>\n\t\t\t" + props + "\n\t\t\t<Name>Настоящее</Name>\n\t\t</Properties>")
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); !ok || got != "Настоящее" {
		t.Errorf("extensionNameOf = (%q, %v), want (\"Настоящее\", true): a name taken from "+
			"a comment was served for a real extension", got, ok)
	}

	// POSITIVE CONTROLS. «Refuses a marker in a comment» is satisfied by a detector
	// that has stopped recognising extensions at all, so a genuine manifest, and a
	// genuine manifest that merely CONTAINS a harmless comment, must both still work.
	for what, body := range map[string]string{
		"a genuine manifest": classicExtensionManifest("Настоящее", true),
		"a genuine manifest with a harmless comment": wrap("\t\t<!-- экспортировано конфигуратором -->\n" +
			"\t\t<Properties>\n\t\t\t" + props + "\n\t\t\t<Name>СКомментарием</Name>\n\t\t</Properties>"),
	} {
		t.Run(what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok := extensionNameOf(dir); !ok {
				t.Errorf("a genuine extension manifest was refused")
			}
		})
	}
}

// TestAnUnterminatedCommentIsUndecidedAndSaysWhichKind.
//
// A comment that never closes leaves everything after it unknown, which is the
// third answer and not «no marker found». It gets its OWN counter rather than
// borrowing ReadTruncated: that one means the READ WINDOW ended mid-document, and
// what an operator does about a window that is too small is not what they do about
// a manifest that is broken. Reusing it would have made its own doc comment false,
// which is the defect class this whole branch is about.
func TestAnUnterminatedCommentIsUndecidedAndSaysWhichKind(t *testing.T) {
	dir := t.TempDir()
	body := "\ufeff<MetaDataObject>\n\t<Configuration>\n\t\t<Properties>\n" +
		"\t\t\t<!-- не закрыт\n\t\t\t<ObjectBelonging>Adopted</ObjectBelonging>\n" +
		"\t\t\t<Name>Призрак</Name>\n\t\t</Properties>\n\t</Configuration>\n</MetaDataObject>\n"
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); ok {
		t.Fatalf("an unterminated comment produced the extension %q", got)
	}
	s := detectExtensionLayout(dir).summary()
	if s.Malformed != 1 {
		t.Errorf("summary = %+v, want Malformed:1", s)
	}
	if s.ReadTruncated != 0 {
		t.Errorf("summary = %+v: a broken document was reported as a read that did not "+
			"fit in the window, and those are different problems with different remedies", s)
	}
	if s.Undecided() != 1 {
		t.Errorf("Undecided() = %d, want 1: a new reason that no counter adds up is a "+
			"doubt that both delivery channels stay silent about", s.Undecided())
	}
}

// TestAMarkerAssembledOutOfTwoHalvesIsNotMinted is why stripMarkupNoise writes a
// SPACE where a comment was rather than closing the gap.
//
// The input below carries NO marker: the string is interrupted mid-element by a
// comment. A strip that joined the surviving halves would produce one out of two
// pieces nobody wrote, turning a guard against forged evidence into a machine for
// manufacturing it.
func TestAMarkerAssembledOutOfTwoHalvesIsNotMinted(t *testing.T) {
	const split = "<Properties><Object<!--x-->Belonging>Adopted</ObjectBelonging>" +
		"<Name>Собранное</Name></Properties>"

	// PREMISE: the raw bytes really do not contain the marker, or there is nothing
	// to assemble and the test is about nothing.
	if strings.Contains(split, "<ObjectBelonging>Adopted</ObjectBelonging>") {
		t.Fatal("premise broken: the fixture already contains the marker")
	}
	// POSITIVE CONTROL: joining the halves DOES produce it, so the space below is
	// load bearing and not decoration.
	if joined := strings.ReplaceAll(split, "<!--x-->", ""); !strings.Contains(joined, "<ObjectBelonging>Adopted</ObjectBelonging>") {
		t.Fatal("control failed: closing the gap does not assemble the marker, so this " +
			"test cannot show the space doing anything")
	}

	out, ok := stripMarkupNoise([]byte(split))
	if !ok {
		t.Fatal("the comment is closed; the strip should not report an open one")
	}
	if bytes.Contains(out, []byte("<ObjectBelonging>Adopted</ObjectBelonging>")) {
		t.Errorf("the strip assembled a marker out of two halves: %q", out)
	}
	if v, _, _ := classifyManifest([]byte(split), true); v == manifestExtension {
		t.Error("a manifest whose marker exists only across a comment boundary was " +
			"accepted as an extension")
	}
}

// TestTheLegacyDirectoryNameIsPathDataAndIsNotGatedLikeAManifestName records a
// DECISION, and the measurement that decided it.
//
// «Расширения/<Имя>/» keys off the DIRECTORY while the two real shapes key off the
// MANIFEST. The obvious tidy-up is to run the directory name through
// validExtensionName too, one rule for one key slot. It was tried and it is wrong,
// for a reason that only shows up when the refusal is actually executed: a refused
// name falls through to baseConfigModuleName over the WHOLE path, so
// «Расширения/Доработки — копия/...» becomes «Расширения.Доработки — копия....». The
// offending rune is still in the key. Validation there does not remove it, it moves
// which slot it sits in, and it pays for that by moving keys customers already have.
//
// The rule the tree actually follows is the one tools/search.go states: a name read
// off a customer's disk is DATA and is CONTAINED rather than corrected.
// baseConfigModuleName validates no object name either, so a catalog directory with
// a тире in it produces a key with a тире in it and always has.
// validExtensionName governs something different: a name a MANIFEST declares, where
// accepting it is a claim this server makes about a whole tree from one file's
// contents.
//
// So this test pins the asymmetry as intended, in both directions, and pins the
// residual it leaves rather than pretending there is none.
func TestTheLegacyDirectoryNameIsPathDataAndIsNotGatedLikeAManifestName(t *testing.T) {
	const tail = "Catalogs/Ном/Ext/ObjectModule.bsl"

	// The name comes from the DIRECTORY and keeps coming from it, including for
	// names the manifest gate refuses.
	for what, name := range map[string]string{
		"an ordinary name": "Доработки3D",
		"underscored":      "MCP_Polling",
		"Cyrillic":         "МоёРасш",
		"a тире, which the manifest gate would refuse": "Доработки — копия",
		"a leading digit, likewise":                    "3D",
	} {
		t.Run(what, func(t *testing.T) {
			rel := extensionDirName + "/" + name + "/" + tail
			want := "ext." + name + ".Справочник.Ном.МодульОбъекта"
			if got := bslPathToModuleName(rel); got != want {
				t.Errorf("bslPathToModuleName(%q) = %q, want %q", rel, got, want)
			}
		})
	}

	// CONTROL THAT THE TWO RULES REALLY ARE DIFFERENT. The same «Доработки — копия»
	// declared by a MANIFEST is refused and produces no namespace, so the paragraph
	// above is describing two rules and not one rule with a hole in it.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic),
		[]byte(classicExtensionManifest("Доработки — копия", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); ok {
		t.Errorf("the manifest gate accepted %q; the two rules are supposed to differ", got)
	}

	// THE RESIDUAL, PINNED AS A RESIDUAL. An invisible name still reaches a served
	// key through a directory. This is not an oversight this test is papering over:
	// it is the same defect at base-configuration scope, older than extensions, and
	// the line below says so by showing an ordinary CATALOG doing it too. Whoever
	// closes that class should close both and delete this block.
	invisible := extensionDirName + "/ㅤ/" + tail
	if got := bslPathToModuleName(invisible); got != "ext.ㅤ.Справочник.Ном.МодульОбъекта" {
		t.Errorf("the residual moved: bslPathToModuleName(%q) = %q", invisible, got)
	}
	if got := bslPathToModuleName("Catalogs/ㅤ/Ext/ObjectModule.bsl"); got != "Справочник.ㅤ.МодульОбъекта" {
		t.Errorf("the base-configuration half of the same residual moved: %q", got)
	}
}

// TestLayoutDetectionCostIsBounded measures the budget as numbers rather than
// arguing it, the way TestInspectionCostIsBoundedAndMeasured does for the other
// file. Examining every child (which is what makes a container decidable) must not
// turn into a read of every child's listing.
func TestLayoutDetectionCostIsBounded(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic), []byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	kinds := []string{"Catalogs", "Documents", "CommonModules", "Reports", "Enums"}
	for _, k := range kinds {
		// Each kind directory holds many children, which is exactly what must NOT
		// be listed.
		for i := 0; i < 40; i++ {
			if err := os.MkdirAll(filepath.Join(root, k, fmt.Sprintf("Объект%02d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	l := detectExtensionLayout(root)
	if !l.empty() {
		t.Fatalf("a base configuration produced a layout: self=%q byDir=%v", l.self, l.byDir)
	}
	// One listing of the root, plus one listing to confirm the root manifest's name
	// byte-exactly. Not one per child.
	if l.cost.ReadDirs != 2 {
		t.Errorf("ReadDirs = %d, want 2: a listing per child directory would make the "+
			"cost of detection grow with the size of the dump", l.cost.ReadDirs)
	}
	if want := 1 + len(kinds); l.cost.Lstats != want {
		t.Errorf("Lstats = %d, want %d (the root and one per child directory)", l.cost.Lstats, want)
	}
	if l.cost.Reads != 1 {
		t.Errorf("Reads = %d, want 1: only the root carries a manifest", l.cost.Reads)
	}

	// POSITIVE CONTROL: the counters move with the tree, so the numbers above are
	// measurements and not constants.
	deeper := t.TempDir()
	for i := 0; i < 3; i++ {
		mkExtensionDump(t, filepath.Join(deeper, fmt.Sprintf("dir%d", i)), extManifestClassic,
			fmt.Sprintf("Р%d", i), "Catalogs")
	}
	got := detectExtensionLayout(deeper)
	if got.cost.Reads != 3 || got.cost.Lstats != 4 {
		t.Fatalf("positive control failed: cost = %+v, want 3 reads and 4 lstats", got.cost)
	}
}

// TestRealFlatExtensionDumpsCarryTheirManifestName runs the whole thing against
// the two real flat dumps on this machine, both of which have a directory name
// that is NOT the extension name. They are the oracle for the central claim, and
// they carry .bsl, so the served keys are real keys and not derivations.
func TestRealFlatExtensionDumpsCarryTheirManifestName(t *testing.T) {
	for root, want := range map[string]string{
		"/Users/igoroot/Downloads/canon_vm":     "FeenlaceMCPService",
		"/Users/igoroot/Downloads/mcp-modified": "MCP_Polling",
	} {
		if _, err := os.Stat(root); err != nil {
			t.Skipf("%s is absent on this machine", root)
		}
		if filepath.Base(root) == want {
			t.Fatalf("premise broken: %s is named after its extension, so it cannot "+
				"show that the name comes from the manifest", root)
		}
		l := detectExtensionLayout(root)
		if l.self != want {
			t.Errorf("detectExtensionLayout(%s).self = %q, want %q", root, l.self, want)
		}
		if len(l.doubts) != 0 {
			t.Errorf("%s produced doubts %v", root, l.doubts)
		}
	}
}

// TestAMarkerInAProcessingInstructionIsNotEvidence is the same defect as the
// comment one, in the node kind the stripper's own doc comment declared did not
// exist.
//
// It said comments and CDATA were «the two node kinds whose CONTENT is not
// markup». A PROCESSING INSTRUCTION is a third, it is ordinary well-formed XML,
// and 1C writes one into every manifest measured on this machine (the XML
// declaration shares the syntax). Every row below was measured against the
// detector before the fix, and the second is not a false positive in the harmless
// direction: it RENAMES a genuine extension, because bytes.Index takes the first
// hit and an instruction can be placed before the real element.
func TestAMarkerInAProcessingInstructionIsNotEvidence(t *testing.T) {
	const props = "<ObjectBelonging>Adopted</ObjectBelonging>"
	wrap := func(inner string) string {
		return "\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
			"<MetaDataObject xmlns=\"http://v8.1c.ru/8.3/MDClasses\" version=\"2.20\">\n" +
			"\t<Configuration uuid=\"aaaa\">\n" + inner + "\n\t</Configuration>\n</MetaDataObject>\n"
	}

	// MINTED OUT OF A BASE CONFIGURATION: the document declares no extension.
	minting := wrap("\t\t<Properties>\n\t\t\t<?note " + props + " ?>\n" +
		"\t\t\t<Name>УправлениеТорговлей</Name>\n\t\t</Properties>")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(minting), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); ok {
		t.Errorf("a marker inside a processing instruction minted the extension %q out of "+
			"a configuration that declares none", got)
	}
	if s := detectExtensionLayout(dir).summary(); !s.Quiet() {
		t.Errorf("summary = %+v, want silence: the document is an ordinary configuration", s)
	}

	// AND IT MUST NOT RENAME A GENUINE ONE. The document below declares exactly one
	// extension and the served answer has to be its name, not the instruction's.
	shadow := wrap("\t\t<?ins <Properties>" + props + "<Name>ЛОЖНОЕ</Name></Properties> ?>\n" +
		"\t\t<Properties>\n\t\t\t" + props + "\n\t\t\t<Name>Доработки</Name>\n\t\t</Properties>")
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); !ok || got != "Доработки" {
		t.Errorf("extensionNameOf = (%q, %v), want (\"Доработки\", true): a name taken from a "+
			"processing instruction was served for a real extension", got, ok)
	}
	if s := detectExtensionLayout(dir).summary(); !s.SelfNamed || s.Undecided() != 0 {
		t.Errorf("summary = %+v, want one recognised extension and no doubt", s)
	}

	// POSITIVE CONTROLS. «Refuses a marker in an instruction» is satisfied by a
	// detector that stopped recognising extensions, and by one that stopped reading
	// documents with a <? in them at all. Every real manifest on this machine carries
	// the XML declaration, so both controls below have to work.
	for what, body := range map[string]string{
		"a genuine manifest, XML declaration and all": classicExtensionManifest("Настоящее", true),
		"a genuine manifest with a harmless instruction": wrap(
			"\t\t<?mso-application progid=\"Word.Document\"?>\n" +
				"\t\t<Properties>\n\t\t\t" + props + "\n\t\t\t<Name>СИнструкцией</Name>\n\t\t</Properties>"),
	} {
		t.Run(what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok := extensionNameOf(dir); !ok {
				t.Errorf("a genuine extension manifest was refused")
			}
		})
	}
}

// TestAMarkupDeclarationIsUndecidedRatherThanScanned is the FOURTH way in, and the
// one that is not stripped at all.
//
// «<!DOCTYPE MetaDataObject [ <!ENTITY e "<Properties>...<Name>ЛОЖНОЕ</Name>..."> ]>»
// is well-formed XML. Nothing in it is broken and nothing in it is a comment, so
// the strip leaves it standing and bytes.Index reads the entity's quoted literal as
// element structure. Measured before this: the served key became ext.ЛОЖНОЕ.* while
// the layout reported Extensions:1 Undecided:0, full confidence in a name nobody
// wrote.
//
// IT IS REFUSED RATHER THAN STRIPPED because its extent is not a fixed byte
// sequence: the bracketed internal subset may hold both "]" and ">" inside quoted
// literals, so a scan cannot find where it ends, and a strip that guessed would be
// the invention this file exists to refuse. Refusing costs nothing that exists: not
// one of the six real Configuration.xml files on this machine contains "<!" at all.
func TestAMarkupDeclarationIsUndecidedRatherThanScanned(t *testing.T) {
	const props = "<ObjectBelonging>Adopted</ObjectBelonging>"
	const real = "<MetaDataObject>\n\t<Configuration>\n\t\t<Properties>\n\t\t\t" + props +
		"\n\t\t\t<Name>Доработки</Name>\n\t\t</Properties>\n\t</Configuration>\n</MetaDataObject>\n"
	const doctype = "<!DOCTYPE MetaDataObject [\n<!ENTITY e \"<Properties>" + props +
		"<Name>ЛОЖНОЕ</Name></Properties>\">\n]>\n"

	// POSITIVE CONTROL FIRST: without the declaration this exact document IS a
	// genuine extension, so the refusal below is the declaration doing something and
	// not the fixture being unreadable for some other reason.
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, extManifestClassic),
		[]byte("\ufeff<?xml version=\"1.0\"?>\n"+real), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(clean); !ok || got != "Доработки" {
		t.Fatalf("control failed: the same document without the declaration answered "+
			"(%q, %v), want (\"Доработки\", true)", got, ok)
	}

	for what, body := range map[string]string{
		"a declaration shadowing a real extension": "\ufeff<?xml version=\"1.0\"?>\n" + doctype + real,
		"a declaration and no <Properties> at all": "\ufeff<?xml version=\"1.0\"?>\n" + doctype +
			"<MetaDataObject><Configuration/></MetaDataObject>\n",
	} {
		t.Run(what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, extManifestClassic), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, ok := extensionNameOf(dir); ok {
				t.Errorf("a markup declaration produced the extension %q", got)
			}
			s := detectExtensionLayout(dir).summary()
			if s.Unscannable != 1 {
				t.Errorf("summary = %+v, want Unscannable:1", s)
			}
			if s.Malformed != 0 {
				t.Errorf("summary = %+v: a well-formed document was reported as a broken "+
					"one, and those are different problems with different remedies", s)
			}
			if s.Undecided() != 1 {
				t.Errorf("Undecided() = %d, want 1: a reason no counter adds up is a doubt "+
					"both delivery channels stay silent about", s.Undecided())
			}
		})
	}
}

// TestEveryStrippedKindIsStrippedAndSaysSoWhenItNeverCloses walks the TABLE rather
// than a list of kinds written out again here.
//
// The defect this whole area keeps producing is a list that declares itself
// complete and is not, so the assertions below are generated from the one list the
// stripper actually consults. A kind added to markupNoiseKinds without working is
// red here without anybody remembering to add a row.
func TestEveryStrippedKindIsStrippedAndSaysSoWhenItNeverCloses(t *testing.T) {
	const props = "<ObjectBelonging>Adopted</ObjectBelonging>"

	// PREMISE: the table is populated. Emptied, every loop below runs zero times and
	// a test that checks nothing is indistinguishable from one that passes.
	if len(markupNoiseKinds) < 3 {
		t.Fatalf("markupNoiseKinds holds %d kinds; the comment, CDATA and instruction "+
			"kinds are all measured vectors and none may leave without its own reason",
			len(markupNoiseKinds))
	}

	for _, k := range markupNoiseKinds {
		t.Run(k.open, func(t *testing.T) {
			if k.open == "" || k.closing == "" {
				t.Fatalf("kind %q/%q is not a delimited pair", k.open, k.closing)
			}
			naked := "\ufeff<MetaDataObject><Configuration><Properties>" + props +
				"<Name>Настоящее</Name></Properties></Configuration></MetaDataObject>"
			hidden := "\ufeff<MetaDataObject><Configuration><Properties>" +
				k.open + " " + props + " " + k.closing +
				"<Name>Настоящее</Name></Properties></Configuration></MetaDataObject>"

			// POSITIVE CONTROL: the marker really is evidence when it is NOT inside this
			// kind, so «not an extension» below is the strip working rather than the
			// fixture being inert.
			if v, name, _ := classifyManifest([]byte(naked), true); v != manifestExtension || name != "Настоящее" {
				t.Fatalf("control failed: the naked marker answered (%v, %q), want an "+
					"extension named Настоящее", v, name)
			}
			if v, _, _ := classifyManifest([]byte(hidden), true); v == manifestExtension {
				t.Errorf("a marker inside %s...%s was read as element structure", k.open, k.closing)
			}

			// OPENED AND NEVER CLOSED is the third answer and says which kind of trouble
			// it is: the document was read whole and the document itself is broken.
			unterminated := "\ufeff<MetaDataObject><Configuration><Properties>" +
				k.open + " " + props + "<Name>Призрак</Name></Properties></Configuration></MetaDataObject>"
			v, _, reason := classifyManifest([]byte(unterminated), true)
			if v != manifestUndecided || reason != doubtManifestMalformed {
				t.Errorf("an unterminated %s answered (%v, reason %d), want undecided with "+
					"doubtManifestMalformed", k.open, v, reason)
			}
		})
	}
}
