package dump

import (
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// C2: single-file parse (Ext layout carries nested children in <ChildObjects>).
// ---------------------------------------------------------------------------

const sampleSubsystemNested = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
	<Subsystem><Properties><Name>Продажи</Name></Properties>
	<ChildObjects>
		<Subsystem><Properties><Name>Розница</Name></Properties></Subsystem>
		<Subsystem><Properties><Name>Опт</Name></Properties></Subsystem>
	</ChildObjects></Subsystem></MetaDataObject>`

const sampleSubsystemContent = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
	<Subsystem><Properties><Name>Продажи</Name>
	<Content xmlns:xr="http://v8.1c.ru/8.3/xcf/readable">
		<xr:Item>Document.РеализацияТоваров</xr:Item>
		<xr:Item>AccumulationRegister.Продажи</xr:Item>
	</Content></Properties></Subsystem></MetaDataObject>`

const sampleSubsystemEmpty = `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject><Subsystem><Properties><Name>Пустая</Name></Properties></Subsystem></MetaDataObject>`

const sampleSubsystemMalformed = `<?xml version="1.0" encoding="UTF-8"?><MetaDataObject><Subsystem><Properties><Name>Bad</Name>`

func TestParseSubsystem_NestedChildren(t *testing.T) {
	got, err := parseSubsystemXML(strings.NewReader(sampleSubsystemNested))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Продажи" {
		t.Errorf("Name=%q, want Продажи", got.Name)
	}
	if got.Path != "Подсистема.Продажи" {
		t.Errorf("Path=%q, want Подсистема.Продажи", got.Path)
	}
	if len(got.Children) != 2 {
		t.Fatalf("Children=%d, want 2", len(got.Children))
	}
	// Children are sorted by name: Опт < Розница.
	if got.Children[0].Name != "Опт" || got.Children[1].Name != "Розница" {
		t.Errorf("sort wrong: %v", got.Children)
	}
	if got.Children[0].Path != "Подсистема.Продажи.Опт" {
		t.Errorf("nested Path=%q, want Подсистема.Продажи.Опт", got.Children[0].Path)
	}
}

func TestParseSubsystem_Content(t *testing.T) {
	got, err := parseSubsystemXML(strings.NewReader(sampleSubsystemContent))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Content) != 2 {
		t.Fatalf("Content=%d, want 2", len(got.Content))
	}
	// Content is canonicalized to Russian and sorted alphabetically.
	if got.Content[0] != "Документ.РеализацияТоваров" {
		t.Errorf("Content[0]=%q, want Документ.РеализацияТоваров", got.Content[0])
	}
	if got.Content[1] != "РегистрНакопления.Продажи" {
		t.Errorf("Content[1]=%q, want РегистрНакопления.Продажи", got.Content[1])
	}
}

func TestParseSubsystem_Empty(t *testing.T) {
	got, err := parseSubsystemXML(strings.NewReader(sampleSubsystemEmpty))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Пустая" || len(got.Children) != 0 || len(got.Content) != 0 {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestParseSubsystem_MalformedXML(t *testing.T) {
	if _, err := parseSubsystemXML(strings.NewReader(sampleSubsystemMalformed)); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParseSubsystem_Synonym(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
  <Subsystem>
    <Properties>
      <Name>Продажи</Name>
      <Synonym>
        <v8:item xmlns:v8="http://v8.1c.ru/8.1/data/core">
          <v8:lang>ru</v8:lang>
          <v8:content>Управление продажами</v8:content>
        </v8:item>
      </Synonym>
    </Properties>
  </Subsystem>
</MetaDataObject>`
	got, err := parseSubsystemXML(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.Synonym != "Управление продажами" {
		t.Errorf("Synonym = %q, want %q", got.Synonym, "Управление продажами")
	}
}

func TestParseSubsystem_NoSynonym_Empty(t *testing.T) {
	got, err := parseSubsystemXML(strings.NewReader(secSubBody("Продажи")))
	if err != nil {
		t.Fatal(err)
	}
	if got.Synonym != "" {
		t.Errorf("Synonym = %q, want empty when the dump has no <Synonym>", got.Synonym)
	}
}

// ---------------------------------------------------------------------------
// C2: layout detection (Ext vs Hierarchical) probed from Subsystems/.
// ---------------------------------------------------------------------------

func TestDetectSubsystemLayout_Hierarchical(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи.xml"), secSubBody("Продажи"))
	got, err := detectSubsystemLayout(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != layoutRoot {
		t.Errorf("layout = %v, want layoutRoot for a direct <Name>.xml", got)
	}
}

func TestDetectSubsystemLayout_Ext(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи", "Ext", "Subsystem.xml"), secSubBody("Продажи"))
	got, err := detectSubsystemLayout(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != layoutExt {
		t.Errorf("layout = %v, want layoutExt for a <N>/Ext/Subsystem.xml", got)
	}
}

func TestDetectSubsystemLayout_MissingIsEmpty(t *testing.T) {
	dir := t.TempDir() // no Subsystems/ at all
	got, err := detectSubsystemLayout(dir)
	if err != nil {
		t.Fatalf("err = %v, want nil for a missing Subsystems/", err)
	}
	if got != layoutExt {
		t.Errorf("layout = %v, want layoutExt (default) for a missing Subsystems/", got)
	}
	subs, warnings, err := ParseAllSubsystemsCtx(t.Context(), dir)
	if err != nil || len(subs) != 0 || len(warnings) != 0 {
		t.Errorf("missing Subsystems/: subs=%v warnings=%v err=%v, want empty/nil", subs, warnings, err)
	}
}

// ---------------------------------------------------------------------------
// C2: full walk over both layouts.
// ---------------------------------------------------------------------------

func TestParseAllSubsystems_HierarchicalFlat(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи.xml"), secSubBody("Продажи"))
	secWrite(t, filepath.Join(dir, "Subsystems", "Закупки.xml"), secSubBody("Закупки"))

	subs, err := ParseAllSubsystems(dir)
	if err != nil {
		t.Fatalf("ParseAllSubsystems err = %v, want nil", err)
	}
	if len(subs) != 2 {
		t.Errorf("len(subs) = %d, want 2; got %+v", len(subs), subs)
	}
	for _, want := range []string{"Продажи", "Закупки"} {
		if !containsStr(flattenNames(subs), want) {
			t.Errorf("missing subsystem %q; got %v", want, flattenNames(subs))
		}
	}
}

func TestParseAllSubsystems_HierarchicalNestedRecursive_FromDisk(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи.xml"), secSubBody("Продажи"))
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи", "Subsystems", "Розница.xml"), secSubBody("Розница"))
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи", "Subsystems", "Опт.xml"), secSubBody("Опт"))

	subs, err := ParseAllSubsystems(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Name != "Продажи" {
		t.Fatalf("want 1 root 'Продажи'; got %+v", subs)
	}
	if len(subs[0].Children) != 2 {
		t.Fatalf("len(Children) = %d, want 2; got %+v", len(subs[0].Children), subs[0].Children)
	}
	for _, c := range subs[0].Children {
		want := "Подсистема.Продажи." + c.Name
		if c.Path != want {
			t.Errorf("child %q Path = %q, want %q", c.Name, c.Path, want)
		}
	}
}

// In Hierarchical layout the in-XML <ChildObjects> text-name list must be ignored;
// children come only from disk traversal.
func TestParseAllSubsystems_Hierarchical_IgnoresChildObjectsTextNames(t *testing.T) {
	dir := t.TempDir()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
  <Subsystem>
    <Properties><Name>Продажи</Name></Properties>
    <ChildObjects>
      <Subsystem>NotOnDisk1</Subsystem>
      <Subsystem>NotOnDisk2</Subsystem>
    </ChildObjects>
  </Subsystem>
</MetaDataObject>
`
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи.xml"), body)
	subs, err := ParseAllSubsystems(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Name != "Продажи" {
		t.Fatalf("expected 1 subsystem 'Продажи'; got %+v", subs)
	}
	if len(subs[0].Children) != 0 {
		t.Errorf("Hierarchical must ignore ChildObjects text-names; got %+v", subs[0].Children)
	}
}

// In Ext layout the nested children DO come from the XML <ChildObjects> struct.
func TestParseAllSubsystems_Ext_DiscoversChildrenFromXML(t *testing.T) {
	dir := t.TempDir()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
  <Subsystem>
    <Properties><Name>Продажи</Name></Properties>
    <ChildObjects>
      <Subsystem>
        <Properties><Name>Розница</Name></Properties>
      </Subsystem>
    </ChildObjects>
  </Subsystem>
</MetaDataObject>
`
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи", "Ext", "Subsystem.xml"), body)
	subs, err := ParseAllSubsystems(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Name != "Продажи" {
		t.Fatalf("expected 1 subsystem 'Продажи'; got %+v", subs)
	}
	if len(subs[0].Children) != 1 || subs[0].Children[0].Name != "Розница" {
		t.Errorf("Ext layout: nested child 'Розница' not discovered from XML; got %+v", subs[0].Children)
	}
}

// A well-formed dump with an oversized breadth is capped; a normal dump is not.
func TestParseAllSubsystems_ContentCanonicalizedEndToEnd(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи.xml"),
		secSubBody("Продажи", "Document.РеализацияТоваров", "CommonModule.Обмен", "Catalog.Контрагенты"))
	subs, err := ParseAllSubsystems(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("want 1 subsystem; got %+v", subs)
	}
	want := []string{"Документ.РеализацияТоваров", "ОбщийМодуль.Обмен", "Справочник.Контрагенты"}
	got := subs[0].Content
	if len(got) != len(want) {
		t.Fatalf("Content = %v, want %v", got, want)
	}
	// Content is sorted; verify the full canonicalized set.
	for i := range want {
		if !containsStr(got, want[i]) {
			t.Errorf("Content missing %q; got %v", want[i], got)
		}
	}
}

// A subsystem Content referencing Constant.X and Subsystem.Y must canonicalize to
// Константа.X / Подсистема.Y end to end, so an analyze_subsystems containing /
// intersections query by the Russian name matches offline instead of false-negating.
func TestParseAllSubsystems_ConstantAndSubsystemContentCanonicalized(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Учет.xml"),
		secSubBody("Учет", "Constant.ИспользоватьНДС", "Subsystem.Продажи", "Document.Счет"))
	subs, err := ParseAllSubsystems(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("want 1 subsystem; got %+v", subs)
	}
	for _, want := range []string{"Константа.ИспользоватьНДС", "Подсистема.Продажи", "Документ.Счет"} {
		if !containsStr(subs[0].Content, want) {
			t.Errorf("Content missing canonical %q; got %v", want, subs[0].Content)
		}
	}
}
