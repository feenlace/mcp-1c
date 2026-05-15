package dump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in testdata/ are real 1C dump files (xcf/logform schema).
// They exercise the parser against the formats produced by DumpConfigToFiles,
// not against a synthetic schema we made up.

// TestParseFormXML_EmptyForm exercises a form with no ChildItems, no Title,
// no Commands, and exactly one form-level Event handler.
func TestParseFormXML_EmptyForm(t *testing.T) {
	form := mustParseFixture(t, "empty_form.xml")

	if form.Title != "" {
		t.Errorf("expected empty title, got %q", form.Title)
	}
	if len(form.Elements) != 0 {
		t.Errorf("expected 0 elements, got %d: %+v", len(form.Elements), form.Elements)
	}
	if len(form.Commands) != 0 {
		t.Errorf("expected 0 commands, got %d: %+v", len(form.Commands), form.Commands)
	}
	if len(form.Handlers) != 1 {
		t.Fatalf("expected 1 handler, got %d: %+v", len(form.Handlers), form.Handlers)
	}
	if form.Handlers[0].Event != "OnOpen" || form.Handlers[0].Handler != "ПриОткрытии" {
		t.Errorf("unexpected handler[0]: %+v", form.Handlers[0])
	}
}

// TestParseFormXML_CommonFormPassword exercises a form with elements,
// nested AutoCommandBar buttons, a single InputField, one form-level Event,
// and a single Command.
func TestParseFormXML_CommonFormPassword(t *testing.T) {
	form := mustParseFixture(t, "common_form_password.xml")

	// Title is not present on the top level for this form.
	if form.Title != "" {
		t.Errorf("expected empty title, got %q", form.Title)
	}

	// Should at least contain the top-level InputField "НовыйПароль".
	if !hasElement(form.Elements, "НовыйПароль", "InputField") {
		t.Errorf("expected InputField %q in elements, got: %+v", "НовыйПароль", form.Elements)
	}

	// DataPath of the InputField must be the literal value from XML.
	for _, e := range form.Elements {
		if e.Name == "НовыйПароль" && e.Type == "InputField" {
			if e.DataPath != "НовыйПароль" {
				t.Errorf("expected DataPath %q for НовыйПароль, got %q", "НовыйПароль", e.DataPath)
			}
		}
	}

	// Exactly one form-level handler.
	if len(form.Handlers) != 1 {
		t.Fatalf("expected 1 handler, got %d: %+v", len(form.Handlers), form.Handlers)
	}
	if form.Handlers[0].Event != "OnCreateAtServer" || form.Handlers[0].Handler != "ПриСозданииНаСервере" {
		t.Errorf("unexpected handler[0]: %+v", form.Handlers[0])
	}

	// Exactly one Command on the top level.
	if len(form.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d: %+v", len(form.Commands), form.Commands)
	}
	if form.Commands[0].Name != "СоздатьДругой" || form.Commands[0].Action != "СоздатьДругой" {
		t.Errorf("unexpected command[0]: %+v", form.Commands[0])
	}
}

// TestParseFormXML_RegisterRecord exercises a form with three top-level
// InputField elements and one form-level Event.
func TestParseFormXML_RegisterRecord(t *testing.T) {
	form := mustParseFixture(t, "register_record_form.xml")

	if form.Title != "" {
		t.Errorf("expected empty title, got %q", form.Title)
	}

	// All three InputFields should be present.
	wantFields := map[string]string{
		"Идентификатор": "Запись.Идентификатор",
		"Дата":          "Запись.Дата",
		"Запрос":        "Запрос",
	}
	for name, dataPath := range wantFields {
		found := false
		for _, e := range form.Elements {
			if e.Name == name && e.Type == "InputField" {
				if e.DataPath != dataPath {
					t.Errorf("element %q: expected DataPath %q, got %q", name, dataPath, e.DataPath)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected InputField %q not found in elements: %+v", name, form.Elements)
		}
	}

	if len(form.Commands) != 0 {
		t.Errorf("expected 0 commands, got %d: %+v", len(form.Commands), form.Commands)
	}

	if len(form.Handlers) != 1 {
		t.Fatalf("expected 1 handler, got %d: %+v", len(form.Handlers), form.Handlers)
	}
	if form.Handlers[0].Event != "OnCreateAtServer" || form.Handlers[0].Handler != "ПриСозданииНаСервере" {
		t.Errorf("unexpected handler[0]: %+v", form.Handlers[0])
	}
}

// TestParseFormXML_CatalogList exercises a list form with a top-level
// Table holding a nested AutoCommandBar with many Buttons, plus two
// form-level Events.
func TestParseFormXML_CatalogList(t *testing.T) {
	form := mustParseFixture(t, "catalog_list_form.xml")

	// Top-level Table "Список" must be present with DataPath "Список"
	// and its own element-level OnChange handler.
	foundTable := false
	for _, e := range form.Elements {
		if e.Name == "Список" && e.Type == "Table" {
			if e.DataPath != "Список" {
				t.Errorf("expected DataPath %q for Table Список, got %q", "Список", e.DataPath)
			}
			if len(e.Events) != 1 {
				t.Fatalf("expected 1 element-level event on Table Список, got %d: %+v",
					len(e.Events), e.Events)
			}
			if e.Events[0].Event != "OnChange" || e.Events[0].Handler != "СписокПриИзменении" {
				t.Errorf("unexpected element-level event on Table Список: %+v", e.Events[0])
			}
			foundTable = true
			break
		}
	}
	if !foundTable {
		t.Errorf("expected Table %q in elements, got: %+v", "Список", form.Elements)
	}

	// Two form-level events: OnOpen and OnCreateAtServer. The element-level
	// OnChange on Table Список must NOT leak into form.Handlers.
	if len(form.Handlers) != 2 {
		t.Fatalf("expected 2 form-level handlers, got %d: %+v", len(form.Handlers), form.Handlers)
	}
	gotEvents := map[string]string{}
	for _, h := range form.Handlers {
		gotEvents[h.Event] = h.Handler
	}
	if gotEvents["OnOpen"] != "ПриОткрытии" {
		t.Errorf("expected OnOpen → ПриОткрытии, got %q", gotEvents["OnOpen"])
	}
	if gotEvents["OnCreateAtServer"] != "ПриСозданииНаСервере" {
		t.Errorf("expected OnCreateAtServer → ПриСозданииНаСервере, got %q", gotEvents["OnCreateAtServer"])
	}
	if _, leaked := gotEvents["OnChange"]; leaked {
		t.Errorf("element-level OnChange leaked into form-level Handlers: %+v", form.Handlers)
	}

	// Nested LabelFields inside Table > ChildItems must NOT inherit Table's
	// events - only the Table itself owns them.
	for _, e := range form.Elements {
		if e.Name != "Список" && len(e.Events) > 0 {
			t.Errorf("element %q (%s) unexpectedly has events: %+v", e.Name, e.Type, e.Events)
		}
	}

	// No top-level Commands block.
	if len(form.Commands) != 0 {
		t.Errorf("expected 0 commands, got %d: %+v", len(form.Commands), form.Commands)
	}
}

// TestParseFormXML_ElementLevelEvents asserts that an element's own <Events>
// child is captured on FormElementInfo.Events, that nested ChildItems keep
// their own Events independent from the parent element, and that
// FormInfo.Handlers stays scoped to the form-level <Events> block only.
func TestParseFormXML_ElementLevelEvents(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.21">
  <Events>
    <Event name="OnOpen">ПриОткрытии</Event>
  </Events>
  <ChildItems>
    <InputField name="Поле1" id="1">
      <DataPath>Объект.Поле1</DataPath>
      <Events>
        <Event name="OnChange">Поле1ПриИзменении</Event>
      </Events>
    </InputField>
    <UsualGroup name="Группа" id="2">
      <Events>
        <Event name="OnChange">ГруппаПриИзменении</Event>
      </Events>
      <ChildItems>
        <InputField name="Поле2" id="3">
          <DataPath>Объект.Поле2</DataPath>
          <Events>
            <Event name="OnChange">Поле2ПриИзменении</Event>
          </Events>
        </InputField>
        <InputField name="ПолеБезСобытий" id="4">
          <DataPath>Объект.ПолеБезСобытий</DataPath>
        </InputField>
      </ChildItems>
    </UsualGroup>
  </ChildItems>
</Form>`

	form, err := parseFormXMLData([]byte(xml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Form-level handlers untouched: exactly one OnOpen entry.
	if len(form.Handlers) != 1 || form.Handlers[0].Event != "OnOpen" ||
		form.Handlers[0].Handler != "ПриОткрытии" {
		t.Fatalf("unexpected form-level handlers: %+v", form.Handlers)
	}

	// Build a name → events map for assertions.
	byName := map[string][]FormHandlerInfo{}
	for _, e := range form.Elements {
		byName[e.Name] = e.Events
	}

	wantEvents := map[string]FormHandlerInfo{
		"Поле1":  {Event: "OnChange", Handler: "Поле1ПриИзменении"},
		"Группа": {Event: "OnChange", Handler: "ГруппаПриИзменении"},
		"Поле2":  {Event: "OnChange", Handler: "Поле2ПриИзменении"},
	}
	for name, want := range wantEvents {
		got, ok := byName[name]
		if !ok {
			t.Errorf("expected element %q in flat list, missing", name)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("element %q: expected events %+v, got %+v", name, []FormHandlerInfo{want}, got)
		}
	}

	if evs, ok := byName["ПолеБезСобытий"]; ok {
		if len(evs) != 0 {
			t.Errorf("element ПолеБезСобытий should have no events, got %+v", evs)
		}
	} else {
		t.Errorf("expected element ПолеБезСобытий in flat list")
	}
}

// TestParseFormXML_NestedChildItemsFlat confirms that nested ChildItems
// inside Tables / Groups are flattened into the top-level element list
// so callers see every element regardless of where it sits in the tree.
func TestParseFormXML_NestedChildItemsFlat(t *testing.T) {
	form := mustParseFixture(t, "catalog_list_form.xml")

	// LabelField "СписокНаименование" lives inside Table > ChildItems
	// in the fixture, deeply nested. It must still surface in the flat list.
	if !hasElement(form.Elements, "СписокНаименование", "LabelField") {
		t.Errorf("expected nested LabelField %q in elements, got: %+v",
			"СписокНаименование", elementNames(form.Elements))
	}

	// Likewise for a Button deep inside Table > AutoCommandBar > ChildItems.
	if !hasElement(form.Elements, "СписокКнопкаСоздать", "Button") {
		t.Errorf("expected nested Button %q in elements, got: %+v",
			"СписокКнопкаСоздать", elementNames(form.Elements))
	}
}

// TestParseFormXML_NoServiceNoise confirms that purely decorative
// service items (ContextMenu, ExtendedTooltip, SearchStringAddition,
// ViewStatusAddition, SearchControlAddition) are filtered out so the
// caller is not buried under hundreds of empty noise entries.
func TestParseFormXML_NoServiceNoise(t *testing.T) {
	form := mustParseFixture(t, "catalog_list_form.xml")

	noisy := map[string]bool{
		"ContextMenu":            true,
		"ExtendedTooltip":        true,
		"SearchStringAddition":   true,
		"ViewStatusAddition":     true,
		"SearchControlAddition":  true,
	}
	for _, e := range form.Elements {
		if noisy[e.Type] {
			t.Errorf("expected service element type %q to be filtered out, got element %+v", e.Type, e)
		}
	}
}

// TestParseFormXML_AcceptsSyntheticMinimal exercises a hand-written
// minimal form to confirm the parser can be driven from in-memory bytes
// (no fixture file) and exercises the optional <Title> path.
func TestParseFormXML_AcceptsSyntheticMinimal(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.21">
  <Title>
    <v8:item>
      <v8:lang>ru</v8:lang>
      <v8:content>Тестовая форма</v8:content>
    </v8:item>
  </Title>
  <Events>
    <Event name="OnOpen">ПриОткрытии</Event>
  </Events>
  <ChildItems>
    <InputField name="Поле1" id="1">
      <DataPath>Объект.Поле1</DataPath>
      <Title>
        <v8:item>
          <v8:lang>ru</v8:lang>
          <v8:content>Поле</v8:content>
        </v8:item>
      </Title>
    </InputField>
  </ChildItems>
  <Commands>
    <Command name="Сохранить" id="1">
      <Action>СохранитьВыполнить</Action>
    </Command>
  </Commands>
</Form>`

	form, err := parseFormXMLData([]byte(xml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if form.Title != "Тестовая форма" {
		t.Errorf("expected title %q, got %q", "Тестовая форма", form.Title)
	}
	if !hasElement(form.Elements, "Поле1", "InputField") {
		t.Errorf("expected InputField Поле1, got: %+v", form.Elements)
	}
	for _, e := range form.Elements {
		if e.Name == "Поле1" {
			if e.Title != "Поле" {
				t.Errorf("expected element title %q, got %q", "Поле", e.Title)
			}
			if e.DataPath != "Объект.Поле1" {
				t.Errorf("expected element DataPath %q, got %q", "Объект.Поле1", e.DataPath)
			}
			if len(e.Events) != 0 {
				t.Errorf("expected no element-level events for Поле1, got %+v", e.Events)
			}
		}
	}
	if len(form.Commands) != 1 || form.Commands[0].Name != "Сохранить" || form.Commands[0].Action != "СохранитьВыполнить" {
		t.Errorf("unexpected commands: %+v", form.Commands)
	}
	if len(form.Handlers) != 1 || form.Handlers[0].Event != "OnOpen" || form.Handlers[0].Handler != "ПриОткрытии" {
		t.Errorf("unexpected handlers: %+v", form.Handlers)
	}
}

// TestParseFormXML_EmptyDocument verifies the parser does not panic on
// a degenerate Form element.
func TestParseFormXML_EmptyDocument(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"></Form>`

	form, err := parseFormXMLData([]byte(xml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(form.Elements) != 0 || len(form.Commands) != 0 || len(form.Handlers) != 0 {
		t.Errorf("expected empty form, got %+v", form)
	}
}

func TestParseFormXML_FileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	formDir := filepath.Join(dir, "Documents", "ТестДок", "Forms", "ФормаДокумента", "Ext")
	if err := os.MkdirAll(formDir, 0o755); err != nil {
		t.Fatal(err)
	}

	xmlContent := `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildItems>
    <InputField name="Поле1" id="1">
      <DataPath>Объект.Реквизит1</DataPath>
    </InputField>
  </ChildItems>
  <Commands>
    <Command name="Команда1" id="1">
      <Action>Действие1</Action>
    </Command>
  </Commands>
</Form>`

	xmlPath := filepath.Join(formDir, "Form.xml")
	if err := os.WriteFile(xmlPath, []byte(xmlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	form, err := ParseFormXML(xmlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !hasElement(form.Elements, "Поле1", "InputField") {
		t.Errorf("expected InputField Поле1, got: %+v", form.Elements)
	}
	if len(form.Commands) != 1 || form.Commands[0].Name != "Команда1" {
		t.Errorf("unexpected commands: %+v", form.Commands)
	}
}

func TestFindFormFiles(t *testing.T) {
	dir := t.TempDir()

	for _, formName := range []string{"ФормаДокумента", "ФормаСписка"} {
		formDir := filepath.Join(dir, "Documents", "ТестДок", "Forms", formName, "Ext")
		if err := os.MkdirAll(formDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(formDir, "Form.xml"), []byte("<Form/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	forms, err := FindFormFiles(dir, "Document", "ТестДок")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(forms) != 2 {
		t.Fatalf("expected 2 forms, got %d", len(forms))
	}

	if _, ok := forms["ФормаДокумента"]; !ok {
		t.Error("expected ФормаДокумента in results")
	}
	if _, ok := forms["ФормаСписка"]; !ok {
		t.Error("expected ФормаСписка in results")
	}
}

func TestFindFormFiles_NoFormsDir(t *testing.T) {
	dir := t.TempDir()

	forms, err := FindFormFiles(dir, "Document", "НесуществующийДок")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if forms != nil {
		t.Errorf("expected nil for missing forms directory, got %v", forms)
	}
}

func TestFindFormFiles_UnknownType(t *testing.T) {
	dir := t.TempDir()

	_, err := FindFormFiles(dir, "UnknownType", "Test")
	if err == nil {
		t.Fatal("expected error for unknown object type")
	}
	if !strings.Contains(err.Error(), "unknown object type") {
		t.Errorf("expected 'unknown object type' in error, got: %v", err)
	}
}

func TestFindFormFiles_PathTraversal(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name       string
		objectName string
	}{
		{"dot-dot", "../../etc"},
		{"dot-dot-only", ".."},
		{"forward-slash", "foo/bar"},
		{"backslash", "foo\\bar"},
		{"dot-dot-backslash", "..\\secret"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FindFormFiles(dir, "Document", tc.objectName)
			if err == nil {
				t.Fatal("expected error for path traversal in object name")
			}
			if !strings.Contains(err.Error(), "path traversal") {
				t.Errorf("expected 'path traversal' in error, got: %v", err)
			}
		})
	}
}

func TestDisplayType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"InputField", "ПолеВвода"},
		{"Table", "ТаблицаФормы"},
		{"Button", "Кнопка"},
		{"UsualGroup", "ОбычнаяГруппа"},
		{"UnknownElement", "UnknownElement"},
	}

	for _, tt := range tests {
		got := DisplayType(tt.input)
		if got != tt.want {
			t.Errorf("DisplayType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// mustParseFixture loads testdata/<name> and parses it. Test fails if the
// file is missing or unparseable.
func mustParseFixture(t *testing.T, name string) *FormInfo {
	t.Helper()
	path := filepath.Join("testdata", name)
	form, err := ParseFormXML(path)
	if err != nil {
		t.Fatalf("parsing fixture %s: %v", path, err)
	}
	return form
}

// hasElement returns true if elements contain an entry matching both name and type.
func hasElement(elements []FormElementInfo, name, typ string) bool {
	for _, e := range elements {
		if e.Name == name && e.Type == typ {
			return true
		}
	}
	return false
}

// elementNames returns just the names of elements for compact error output.
func elementNames(elements []FormElementInfo) []string {
	names := make([]string, 0, len(elements))
	for _, e := range elements {
		names = append(names, e.Name+"("+e.Type+")")
	}
	return names
}
