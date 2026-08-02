package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFormStructureTool(t *testing.T) {
	tool := FormStructureTool()
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name != "get_form_structure" {
		t.Errorf("expected tool name %q, got %q", "get_form_structure", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestFormatFormStructure(t *testing.T) {
	f := &onec.FormStructure{
		Name:  "ФормаДокумента",
		Title: "Реализация товаров и услуг",
		Elements: []onec.FormElement{
			{
				Name:     "Контрагент",
				Type:     "ПолеВвода",
				Title:    "Контрагент",
				DataPath: "Объект.Контрагент",
				Events: []onec.FormHandler{
					{Event: "OnChange", Handler: "КонтрагентПриИзменении"},
				},
			},
			{
				Name:     "Сумма",
				Type:     "ПолеВвода",
				Title:    "Сумма документа",
				DataPath: "Объект.СуммаДокумента",
			},
			{
				Name:     "Товары",
				Type:     "ТаблицаФормы",
				Title:    "Товары",
				DataPath: "Объект.Товары",
				Events: []onec.FormHandler{
					{Event: "OnActivateRow", Handler: "ТоварыПриАктивизацииСтроки"},
				},
			},
		},
		Commands: []onec.FormCommand{
			{Name: "Провести", Action: "Провести"},
			{Name: "ПечатьНакладной", Action: "ПечатьНакладной"},
		},
		Handlers: []onec.FormHandler{
			{Event: "ПриОткрытии", Handler: "ПриОткрытии"},
			{Event: "ПередЗаписью", Handler: "ПередЗаписью"},
		},
	}

	text := formatFormStructure(f)

	for _, want := range []string{
		"# Форма: ФормаДокумента",
		"Реализация товаров и услуг",
		"## Элементы формы",
		"| Контрагент | ПолеВвода | Контрагент | Объект.Контрагент |",
		"| Сумма | ПолеВвода | Сумма документа | Объект.СуммаДокумента |",
		"| Товары | ТаблицаФормы | Товары | Объект.Товары |",
		"### События элементов",
		"**Контрагент** (`OnChange`) → КонтрагентПриИзменении()",
		"**Товары** (`OnActivateRow`) → ТоварыПриАктивизацииСтроки()",
		"## Команды формы",
		"**Провести**",
		"**ПечатьНакладной**",
		"## Обработчики событий",
		"**ПриОткрытии** → ПриОткрытии()",
		"**ПередЗаписью** → ПередЗаписью()",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, text)
		}
	}
}

// TestFormatFormStructure_NoElementEvents covers the rendering path where
// the form has elements but none of them carry events - the "События
// элементов" section must be omitted entirely.
func TestFormatFormStructure_NoElementEvents(t *testing.T) {
	f := &onec.FormStructure{
		Name: "Ф",
		Elements: []onec.FormElement{
			{Name: "Поле", Type: "ПолеВвода", DataPath: "Объект.Поле"},
		},
	}

	text := formatFormStructure(f)

	if !strings.Contains(text, "## Элементы формы") {
		t.Errorf("expected elements section, got:\n%s", text)
	}
	if strings.Contains(text, "### События элементов") {
		t.Errorf("expected no element-events section when no events are set, got:\n%s", text)
	}
}

func TestFormatFormStructure_Empty(t *testing.T) {
	f := &onec.FormStructure{
		Name: "ПустаяФорма",
	}

	text := formatFormStructure(f)

	if !strings.Contains(text, "# Форма: ПустаяФорма") {
		t.Errorf("expected form name in text, got:\n%s", text)
	}
	for _, section := range []string{
		"## Элементы формы",
		"### События элементов",
		"## Команды формы",
		"## Обработчики событий",
	} {
		if strings.Contains(text, section) {
			t.Errorf("expected no %q section for empty form, got:\n%s", section, text)
		}
	}
}

func TestNewFormStructureHandler(t *testing.T) {
	mockForm := onec.FormStructure{
		Name:  "ФормаДокумента",
		Title: "Реализация товаров и услуг",
		Elements: []onec.FormElement{
			{
				Name:     "Контрагент",
				Type:     "ПолеВвода",
				Title:    "Контрагент",
				DataPath: "Объект.Контрагент",
			},
		},
		Commands: []onec.FormCommand{
			{Name: "Провести", Action: "Провести"},
		},
		Handlers: []onec.FormHandler{
			{Event: "ПриОткрытии", Handler: "ПриОткрытии"},
		},
	}
	mockResponse, _ := json.Marshal(mockForm)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/form/Document/РеализацияТоваровУслуг" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET method, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(mockResponse)
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewFormStructureHandler(client, "")

	args, _ := json.Marshal(map[string]any{
		"object_type": "Document",
		"object_name": "РеализацияТоваровУслуг",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_form_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if tc.Text == "" {
		t.Fatal("expected non-empty text")
	}

	for _, want := range []string{
		"ФормаДокумента",
		"Реализация товаров и услуг",
		"Контрагент",
		"ПолеВвода",
		"Провести",
		"ПриОткрытии",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestNewFormStructureHandler_DumpFallback(t *testing.T) {
	// Mock server that returns empty elements/commands/handlers
	// (simulates Enterprise mode limitation).
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":     "ФормаДокумента",
			"title":    "Реализация товаров и услуг",
			"elements": []any{},
			"commands": []any{},
			"handlers": []any{},
		})
	}))
	defer mockServer.Close()

	// Create dump directory with a Form.xml.
	dumpDir := t.TempDir()
	formXMLDir := filepath.Join(dumpDir, "Documents", "РеализацияТоваровУслуг", "Forms", "ФормаДокумента", "Ext")
	if err := os.MkdirAll(formXMLDir, 0o755); err != nil {
		t.Fatal(err)
	}
	formXML := sampleFormXML()
	if err := os.WriteFile(filepath.Join(formXMLDir, "Form.xml"), []byte(formXML), 0o644); err != nil {
		t.Fatal(err)
	}

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewFormStructureHandler(client, dumpDir)

	args, _ := json.Marshal(map[string]any{
		"object_type": "Document",
		"object_name": "РеализацияТоваровУслуг",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_form_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)

	// The form name should come from the HTTP response.
	if !strings.Contains(tc.Text, "ФормаДокумента") {
		t.Errorf("expected form name from HTTP, got:\n%s", tc.Text)
	}

	// Elements, commands, handlers should come from the dump.
	for _, want := range []string{
		"## Элементы формы",
		"Контрагент",
		"Объект.Контрагент",
		"### События элементов",
		"**Контрагент** (`OnChange`) → КонтрагентПриИзменении()",
		"## Команды формы",
		"Провести",
		"## Обработчики событий",
		"ПриОткрытии",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q from dump fallback, got:\n%s", want, tc.Text)
		}
	}
}

func TestNewFormStructureHandler_DumpOnly(t *testing.T) {
	// Mock server that returns 500 error.
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer mockServer.Close()

	// Create dump directory with a Form.xml.
	dumpDir := t.TempDir()
	formXMLDir := filepath.Join(dumpDir, "Documents", "РеализацияТоваровУслуг", "Forms", "ФормаДокумента", "Ext")
	if err := os.MkdirAll(formXMLDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(formXMLDir, "Form.xml"), []byte(sampleFormXML()), 0o644); err != nil {
		t.Fatal(err)
	}

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewFormStructureHandler(client, dumpDir)

	args, _ := json.Marshal(map[string]any{
		"object_type": "Document",
		"object_name": "РеализацияТоваровУслуг",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_form_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)

	// Form name comes from the dump directory name.
	if !strings.Contains(tc.Text, "ФормаДокумента") {
		t.Errorf("expected form name, got:\n%s", tc.Text)
	}

	// All sections should be populated from the dump.
	for _, want := range []string{
		"Контрагент",
		"ПолеВвода",
		"Провести",
		"ПриОткрытии",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

// TestNewFormStructureHandler_FormNameWithoutDumpIsExplained pins the response
// BODY, not a log line: form_name only ever reaches formFromDump, so without
// --dump it is dropped and the caller gets some other form with no hint that
// their argument was ignored. The existing WARN is not a substitute - the
// default stdio logger is at ERROR level (cmd/mcp-1c/main.go), so nothing about
// this reaches the user unless the body says it.
func TestNewFormStructureHandler_FormNameWithoutDumpIsExplained(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":     "ФормаЭлемента",
			"title":    "Контрагент",
			"elements": []any{},
			"commands": []any{},
			"handlers": []any{},
		})
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewFormStructureHandler(client, "") // no --dump

	args, _ := json.Marshal(map[string]any{
		"object_type": "Catalog",
		"object_name": "Контрагенты",
		"form_name":   "ФормаСписка",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "get_form_structure", Arguments: args},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}

	// The body must name the ignored parameter, the flag that would make it
	// work, and still carry what was actually returned instead.
	for _, want := range []string{"form_name", "--dump", "ФормаЭлемента"} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("response body must mention %q, got:\n%s", want, tc.Text)
		}
	}
	// It must stay a normal result, not an error.
	if result.IsError {
		t.Errorf("expected a normal result, got IsError=true:\n%s", tc.Text)
	}
}

// TestNewFormStructureHandler_NoFormNameNoDumpNote is the other half of the
// pair: the note is CONDITIONAL. A caller who never passed form_name has
// nothing to be told, so an unconditional note would be noise in every single
// no-dump response.
func TestNewFormStructureHandler_NoFormNameNoDumpNote(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":     "ФормаЭлемента",
			"title":    "Контрагент",
			"elements": []any{},
			"commands": []any{},
			"handlers": []any{},
		})
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewFormStructureHandler(client, "") // no --dump

	args, _ := json.Marshal(map[string]any{
		"object_type": "Catalog",
		"object_name": "Контрагенты",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "get_form_structure", Arguments: args},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := result.Content[0].(*mcp.TextContent)

	for _, unwanted := range []string{"form_name", "--dump"} {
		if strings.Contains(tc.Text, unwanted) {
			t.Errorf("response body must NOT mention %q when form_name was not passed, got:\n%s",
				unwanted, tc.Text)
		}
	}
	if !strings.Contains(tc.Text, "ФормаЭлемента") {
		t.Errorf("expected the form itself in the body, got:\n%s", tc.Text)
	}
}

// sampleFormXML returns a minimal 1C form XML for testing. The schema
// matches what DumpConfigToFiles produces (xcf/logform): element names
// live in the "name" attribute, the UI tree is under <ChildItems>, and
// event handlers are <Event name="X">handler</Event> entries inside
// a form-level <Events> block.
func sampleFormXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"
      xmlns:v8="http://v8.1c.ru/8.1/data/core"
      version="2.21">
  <Title>
    <v8:item>
      <v8:lang>ru</v8:lang>
      <v8:content>Реализация товаров и услуг</v8:content>
    </v8:item>
  </Title>
  <Events>
    <Event name="OnOpen">ПриОткрытии</Event>
    <Event name="BeforeWrite">ПередЗаписью</Event>
  </Events>
  <ChildItems>
    <InputField name="Контрагент" id="1">
      <DataPath>Объект.Контрагент</DataPath>
      <Title>
        <v8:item>
          <v8:lang>ru</v8:lang>
          <v8:content>Контрагент</v8:content>
        </v8:item>
      </Title>
      <Events>
        <Event name="OnChange">КонтрагентПриИзменении</Event>
      </Events>
    </InputField>
    <InputField name="СуммаДокумента" id="2">
      <DataPath>Объект.СуммаДокумента</DataPath>
      <Title>
        <v8:item>
          <v8:lang>ru</v8:lang>
          <v8:content>Сумма</v8:content>
        </v8:item>
      </Title>
    </InputField>
    <Table name="Товары" id="3">
      <DataPath>Объект.Товары</DataPath>
      <Title>
        <v8:item>
          <v8:lang>ru</v8:lang>
          <v8:content>Товары</v8:content>
        </v8:item>
      </Title>
    </Table>
  </ChildItems>
  <Commands>
    <Command name="Провести" id="1">
      <Action>Провести</Action>
    </Command>
    <Command name="ПечатьНакладной" id="2">
      <Action>ПечатьНакладной</Action>
    </Command>
  </Commands>
</Form>`
}

// formXMLWithTitle builds a minimal dump Form.xml carrying exactly one input
// field and an optional form-level <Title>. Every form in a test dump gets a
// distinguishable element name and title, so a response cannot pass an
// assertion while actually describing a different form.
func formXMLWithTitle(title, elementName string) string {
	titleBlock := ""
	if title != "" {
		titleBlock = `
  <Title>
    <v8:item>
      <v8:lang>ru</v8:lang>
      <v8:content>` + title + `</v8:content>
    </v8:item>
  </Title>`
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"
      xmlns:v8="http://v8.1c.ru/8.1/data/core"
      version="2.21">` + titleBlock + `
  <ChildItems>
    <InputField name="` + elementName + `" id="1">
      <DataPath>Объект.` + elementName + `</DataPath>
    </InputField>
  </ChildItems>
</Form>`
}

// writeDumpForm materialises one form inside a DumpConfigToFiles-shaped tree:
// <dump>/<objectDir>/<objectName>/Forms/<formName>/Ext/Form.xml.
func writeDumpForm(t *testing.T, dumpDir, objectDir, objectName, formName, xml string) {
	t.Helper()
	dir := filepath.Join(dumpDir, objectDir, objectName, "Forms", formName, "Ext")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Form.xml"), []byte(xml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// formHTTPServer serves a 1C-shaped form response with the given name and
// title and no Elements/Commands/Handlers, which is what the bundled extension
// returns whenever its Попытка blocks come back empty.
func formHTTPServer(t *testing.T, name, title string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":     name,
			"title":    title,
			"elements": []any{},
			"commands": []any{},
			"handlers": []any{},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// callFormHandler runs get_form_structure against the given base URL and dump
// directory. formName is omitted from the arguments entirely when empty, so the
// "argument absent" and "argument empty" cases stay distinguishable.
func callFormHandler(t *testing.T, baseURL, dumpDir, objectType, objectName, formName string) (*mcp.CallToolResult, error) {
	t.Helper()
	args := map[string]any{"object_type": objectType, "object_name": objectName}
	if formName != "" {
		args["form_name"] = formName
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewFormStructureHandler(onec.NewClient(baseURL, "", ""), dumpDir)
	return handler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "get_form_structure", Arguments: raw},
	})
}

// resultText extracts the single text block of a tool result.
func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatal("expected non-empty result content")
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	return tc.Text
}

// dumpNoteMarker is the distinguishing phrase of the note added when --dump is
// configured but the dump could not supply the structure. Tests match the
// phrase rather than the constant so they compile, and fail, against a build
// that does not have the constant yet.
const dumpNoteMarker = "Состав формы не прочитан из выгрузки"

// TestNewFormStructureHandler_UnknownFormNameIsHardError pins the one dump
// failure that must NOT degrade: the caller named a form the object does not
// have. Returning some other form's structure under that request is a confident
// wrong answer, and the WARN that used to be the only trace is invisible at the
// default stdio log level (slog.LevelError in cmd/mcp-1c/main.go).
func TestNewFormStructureHandler_UnknownFormNameIsHardError(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаВыбора",
		formXMLWithTitle("Выбор реализации", "ПолеВыбора"))
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаЭлемента",
		formXMLWithTitle("Реализация", "ПолеЭлемента"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "ФормаСписка")
	// It is still FATAL, and it is now fatal in the shape the caller can read:
	// a result with IsError rather than a protocol error. What the message has to
	// carry is unchanged, and it is the enumerated form names that make it
	// actionable.
	text := failureText(t, result, err)
	for _, want := range []string{"ФормаСписка", "ФормаВыбора", "ФормаЭлемента"} {
		if !strings.Contains(text, want) {
			t.Errorf("the failure must name the requested form and list the available ones, missing %q:\n%s",
				want, text)
		}
	}
}

// TestNewFormStructureHandler_ExistingFormNameSelectsThatForm is the other half
// of the pair: a form_name that DOES resolve must succeed, and both the header
// and the body must describe that form and no other.
func TestNewFormStructureHandler_ExistingFormNameSelectsThatForm(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаВыбора",
		formXMLWithTitle("Выбор реализации", "ПолеВыбора"))
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаЭлемента",
		formXMLWithTitle("Карточка реализации", "ПолеЭлемента"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "ФормаЭлемента")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	for _, want := range []string{"# Форма: ФормаЭлемента", "Карточка реализации", "ПолеЭлемента"} {
		if !strings.Contains(text, want) {
			t.Errorf("response must describe the requested form, missing %q:\n%s", want, text)
		}
	}
	// Nothing belonging to another form may appear: not the alphabetically
	// first form's element, not the HTTP-named form, not its title.
	for _, unwanted := range []string{"ПолеВыбора", "ФормаДокумента", "Реализация товаров и услуг"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("response must not carry %q from another form:\n%s", unwanted, text)
		}
	}
}

// TestNewFormStructureHandler_DumpFormNameReplacesHTTPName covers the default
// path with no form_name at all. formFromDump picks the alphabetically first
// form while the HTTP endpoint answers about the object's main form, so keeping
// the HTTP name would label one form's structure with another form's identity.
// The title travels with the name: the HTTP Title describes the HTTP-named
// form and must not be shown above a different form's elements.
func TestNewFormStructureHandler_DumpFormNameReplacesHTTPName(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаВыбора",
		formXMLWithTitle("Выбор реализации", "ПолеВыбора"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	for _, want := range []string{"# Форма: ФормаВыбора", "Выбор реализации", "ПолеВыбора"} {
		if !strings.Contains(text, want) {
			t.Errorf("response must describe the form the dump parsed, missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"ФормаДокумента", "Реализация товаров и услуг"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("response must not carry %q from the HTTP-named form:\n%s", unwanted, text)
		}
	}
}

// TestNewFormStructureHandler_DumpFormDoesNotInheritForeignSections pins the
// rest of the body once the identity has moved to the dump form. Merging each
// collection on its own leaves the sections the dump form does not declare
// filled in from the HTTP form, so a response headed by one form still lists
// another form's commands and handlers. A form that declares no commands has
// no commands: an empty dump collection is an answer, not a gap to backfill.
func TestNewFormStructureHandler_DumpFormDoesNotInheritForeignSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":     "ФормаДокумента",
			"title":    "Реализация товаров и услуг",
			"elements": []any{},
			"commands": []map[string]any{{"name": "ПровестиИЗакрыть", "action": "ПровестиИЗакрыть"}},
			"handlers": []map[string]any{{"event": "ПриОткрытии", "handler": "ПриОткрытии"}},
		})
	}))
	defer srv.Close()

	// The dump form declares one element and nothing else.
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаВыбора",
		formXMLWithTitle("Выбор реализации", "ПолеВыбора"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, "# Форма: ФормаВыбора") {
		t.Fatalf("response must name the form the dump parsed:\n%s", text)
	}
	for _, unwanted := range []string{
		"ПровестиИЗакрыть", "## Команды формы",
		"ПриОткрытии", "## Обработчики событий",
	} {
		if strings.Contains(text, unwanted) {
			t.Errorf("response headed by ФормаВыбора must not carry %q from ФормаДокумента:\n%s",
				unwanted, text)
		}
	}
	if !strings.Contains(text, "ПолеВыбора") {
		t.Errorf("the dump form's own contents must survive:\n%s", text)
	}
}

// TestNewFormStructureHandler_DumpFormWithoutTitleDropsHTTPTitle fixes the
// decision for the case the previous test cannot reach: the dump form has no
// <Title> of its own. An absent title makes the response incomplete; the HTTP
// title would make it wrong, because it belongs to a different form. So the
// title is omitted rather than inherited.
func TestNewFormStructureHandler_DumpFormWithoutTitleDropsHTTPTitle(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаВыбора",
		formXMLWithTitle("", "ПолеВыбора"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, "# Форма: ФормаВыбора") {
		t.Errorf("response must name the form the dump parsed:\n%s", text)
	}
	// "Заголовок" on its own is also the elements table's column header, so the
	// assertion has to name the title LINE that formatFormStructure emits.
	if strings.Contains(text, "**Заголовок:**") {
		t.Errorf("a form with no title of its own must show no title line at all:\n%s", text)
	}
	if strings.Contains(text, "Реализация товаров и услуг") {
		t.Errorf("the HTTP title belongs to another form and must not be reused:\n%s", text)
	}
}

// TestNewFormStructureHandler_UnreadableFormsDirAddsBodyNote drives the dump
// failure that is NOT the caller's fault: the object's Forms position is not a
// directory, so FindFormFiles refuses it. HTTP still answered, so this stays a
// normal result, but the body has to say the structure is missing. The WARN
// alone cannot: the default stdio logger is at ERROR level.
func TestNewFormStructureHandler_UnreadableFormsDirAddsBodyNote(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	objectDir := filepath.Join(dumpDir, "Documents", "РеализацияТоваровУслуг")
	if err := os.MkdirAll(objectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A plain file standing where the Forms directory belongs.
	if err := os.WriteFile(filepath.Join(objectDir, "Forms"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("a dump failure other than form-not-found must not fail the call: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, dumpNoteMarker) {
		t.Errorf("body must say the structure could not be read from the dump:\n%s", text)
	}
	if !strings.Contains(text, "ФормаДокумента") {
		t.Errorf("the valid HTTP data must still be returned:\n%s", text)
	}
}

// TestNewFormStructureHandler_NoFormsInDumpAddsBodyNote covers the other
// non-fatal branch of formFromDump: the dump simply has no forms for this
// object. Same contract as the unreadable case.
func TestNewFormStructureHandler_NoFormsInDumpAddsBodyNote(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	// A dump that exists and is readable but holds forms for a different object.
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Catalogs", "Контрагенты", "ФормаСписка",
		formXMLWithTitle("Контрагенты", "ПолеСписка"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("an object with no forms in the dump must not fail the call: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, dumpNoteMarker) {
		t.Errorf("body must say the structure could not be read from the dump:\n%s", text)
	}
	if !strings.Contains(text, "ФормаДокумента") {
		t.Errorf("the valid HTTP data must still be returned:\n%s", text)
	}
}

// TestNewFormStructureHandler_UnreadableDumpNamesTheIgnoredFormName is the
// form_name half of the degraded path. When the dump cannot be read at all, the
// name lookup never happens, so the form shown is whichever one the HTTP
// service chose. The body has to admit that, exactly as the no-dump note does.
func TestNewFormStructureHandler_UnreadableDumpNamesTheIgnoredFormName(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	objectDir := filepath.Join(dumpDir, "Documents", "РеализацияТоваровУслуг")
	if err := os.MkdirAll(objectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objectDir, "Forms"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "ФормаСписка")
	if err != nil {
		t.Fatalf("a dump failure other than form-not-found must not fail the call: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, dumpNoteMarker) {
		t.Errorf("body must say the structure could not be read from the dump:\n%s", text)
	}
	if !strings.Contains(text, "form_name") {
		t.Errorf("body must say the form_name lookup did not happen:\n%s", text)
	}
}

// TestNewFormStructureHandler_GoodDumpNoFormNameHasNoNotes is a negative
// control. A note that appears on the healthy path is noise, and a test
// asserting a note in the general case is a test that cannot fail.
func TestNewFormStructureHandler_GoodDumpNoFormNameHasNoNotes(t *testing.T) {
	srv := formHTTPServer(t, "ФормаВыбора", "Выбор реализации")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаВыбора",
		formXMLWithTitle("Выбор реализации", "ПолеВыбора"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, "ПолеВыбора") {
		t.Fatalf("expected the parsed structure in the body:\n%s", text)
	}
	for _, unwanted := range []string{dumpNoteMarker, partialNoteMarker, noFormRootNoteMarker, "form_name", "--dump"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("a healthy dump response must not carry %q:\n%s", unwanted, text)
		}
	}
}

// TestNewFormStructureHandler_GoodDumpValidFormNameHasNoNotes is the second
// negative control: a form_name that resolves is not a degraded path either.
func TestNewFormStructureHandler_GoodDumpValidFormNameHasNoNotes(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаВыбора",
		formXMLWithTitle("Выбор реализации", "ПолеВыбора"))
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаЭлемента",
		formXMLWithTitle("Карточка реализации", "ПолеЭлемента"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "ФормаЭлемента")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, "ПолеЭлемента") {
		t.Fatalf("expected the parsed structure in the body:\n%s", text)
	}
	for _, unwanted := range []string{dumpNoteMarker, partialNoteMarker, noFormRootNoteMarker, "form_name", "--dump"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("a healthy dump response must not carry %q:\n%s", unwanted, text)
		}
	}
}

// partialNoteMarker is the distinguishing phrase of the note added when the
// dump's Form.xml WAS opened and parsed but the XML decoder stopped on a syntax
// error before the end of the document. Matched as a phrase rather than through
// the constant, exactly as dumpNoteMarker is, so these tests compile against a
// build that does not carry the constant yet and fail on behaviour instead of
// failing to build.
const partialNoteMarker = "прочитан не полностью"

// truncatedFormXML builds a Form.xml that is cut off mid tag after `keep`
// complete <InputField> entries. The parser tolerates this today: the decoder
// stops on a syntax error, the token loop breaks, and parseFormXMLData still
// reports success, which is the silent failure these tests pin.
//
// The element count the parser actually yields is keep+1, not keep: the field
// after the last complete one has its own start tag intact and only its
// <DataPath> child is severed, so appendElement has already recorded it. That
// was measured against the real parser, not assumed, and the callers below use
// the measured names.
func truncatedFormXML(keep int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildItems>
`)
	for i := 1; i <= keep; i++ {
		name := truncatedFieldNames[i-1]
		b.WriteString(`    <InputField name="` + name + `" id="` + string(rune('0'+i)) + `">
      <DataPath>Объект.` + name + `</DataPath>
    </InputField>
`)
	}
	// The severed tail: a start tag that opened and a child tag cut in half.
	b.WriteString(`    <InputField name="` + truncatedFieldNames[keep] + `" id="9">
      <DataPa`)
	return b.String()
}

// truncatedFieldNames are the element names truncatedFormXML hands out, in
// order. Distinct per position so a body assertion cannot pass while describing
// a different element.
var truncatedFieldNames = []string{"ПолеПервое", "ПолеВторое", "ПолеТретье", "ПолеЧетвёртое"}

// TestNewFormStructureHandler_TruncatedFormXMLAddsPartialNote is the headline
// case. The Form.xml is cut off, the parser tolerates it and reports success, so
// nothing upstream fails and nothing is logged. Without a note in the body the
// caller reads a confident answer about a form whose file was never fully read.
func TestNewFormStructureHandler_TruncatedFormXMLAddsPartialNote(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	// keep=0: nothing complete before the cut, so the dump yields one element
	// whose start tag survived and no commands or handlers at all.
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента",
		truncatedFormXML(0))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("a tolerated malformed Form.xml must not become a hard error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, partialNoteMarker) {
		t.Errorf("body must say the form file was read only partially:\n%s", text)
	}
	if !strings.Contains(text, "ФормаДокумента") {
		t.Errorf("the valid part of the answer must still be returned:\n%s", text)
	}
	// The unreadable-dump note describes a different failure (the dump gave us
	// nothing at all). Here the file WAS read, so that note must stay away or
	// the two contradict each other.
	if strings.Contains(text, dumpNoteMarker) {
		t.Errorf("a partially parsed form is not an unreadable dump, both notes present:\n%s", text)
	}
}

// TestNewFormStructureHandler_PartialNoteDoesNotDenyShownElements guards the
// trap that a note is only useful while it agrees with the body above it. A
// partial parse routinely yields SOME elements, and a note asserting the form
// has none is then falsified by the table printed right above it.
func TestNewFormStructureHandler_PartialNoteDoesNotDenyShownElements(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	// keep=2 complete fields plus the severed third, measured as 3 elements.
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента",
		truncatedFormXML(2))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("a tolerated malformed Form.xml must not become a hard error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, partialNoteMarker) {
		t.Fatalf("body must say the form file was read only partially:\n%s", text)
	}
	// The elements that DID survive the cut have to be in the body: the point of
	// tolerating the file is that its readable part still answers the question.
	for _, want := range truncatedFieldNames[:3] {
		if !strings.Contains(text, want) {
			t.Errorf("element %q parsed before the cut must still be listed:\n%s", want, want+text)
		}
	}
	// And the note must not deny them. Each phrase below is one a note author
	// could plausibly reach for, and every one of them would be false here.
	for _, denial := range []string{
		"элементы отсутствуют",
		"нет элементов",
		"состав формы пуст",
		"не удалось прочитать ни одного",
		"элементы не прочитаны",
		"состав формы не прочитан",
	} {
		if strings.Contains(text, denial) {
			t.Errorf("note claims %q while the body above lists elements:\n%s", denial, text)
		}
	}
}

// truncatedBeforeAnyElementXML is a Form.xml cut off INSIDE the form but before
// a single element is recorded. The <Form> start tag is complete, so the parser
// enters the form (NoFormRoot stays false) and then hits the syntax error
// (ParseIncomplete becomes true) with nothing collected: the dump contributes no
// element, no command and no handler, and the merge therefore leaves every
// composition section exactly as the HTTP service returned it.
func truncatedBeforeAnyElementXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildIte`
}

// formHTTPServerWithStructure serves a 1C form reply that DOES fill all three
// composition sections, so a body built from it alone is provably HTTP content.
func formHTTPServerWithStructure(t *testing.T, name, title string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":  name,
			"title": title,
			"elements": []any{
				map[string]any{"name": "ЭлементОтСервиса", "type": "ПолеВвода", "dataPath": "Объект.Контрагент"},
			},
			"commands": []any{map[string]any{"name": "КомандаОтСервиса", "action": "Провести"}},
			"handlers": []any{map[string]any{"event": "OnOpen", "handler": "ОбработчикОтСервиса"}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// partialNoteFileProvenanceClaims are wordings that assert the composition
// printed above the note is what was read from the dump file. Every one of them
// is false for a body the dump did not contribute to, which is a body a partial
// parse routinely produces. The first entry is the wording this note carried
// when the defect was found; the rest are the variants a note author reaches for
// next.
var partialNoteFileProvenanceClaims = []string{
	"показано только то, что удалось прочитать",
	"выше показано только прочитанное из файла",
	"всё, что показано выше, прочитано из файла",
	"показанное выше взято из выгрузки",
}

// TestNewFormStructureHandler_PartialNoteDoesNotClaimFileProvenance is the
// headline honesty case for the partial note, and it uses the body that
// falsifies a provenance claim: the Form.xml breaks before it records anything,
// so every element, command and handler on screen came from the 1C HTTP
// service and nothing came from the partially read file.
func TestNewFormStructureHandler_PartialNoteDoesNotClaimFileProvenance(t *testing.T) {
	srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента",
		truncatedBeforeAnyElementXML())

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("a tolerated malformed Form.xml must not become a hard error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, partialNoteMarker) {
		t.Fatalf("body must say the form file was read only partially:\n%s", text)
	}
	// The premise this test reasons about, asserted rather than assumed: every
	// composition section on screen is the HTTP service's.
	for _, want := range []string{"ЭлементОтСервиса", "КомандаОтСервиса", "ОбработчикОтСервиса"} {
		if !strings.Contains(text, want) {
			t.Fatalf("premise broken: %q from the HTTP service is not in the body:\n%s", want, text)
		}
	}
	for _, unwanted := range truncatedFieldNames {
		if strings.Contains(text, unwanted) {
			t.Fatalf("premise broken: %q from a dump file is in the body, so it is not pure HTTP:\n%s",
				unwanted, text)
		}
	}

	// Positive control for the scan below: it must be able to see the claim when
	// handed the wording this note actually shipped with. Hand-typed on purpose,
	// as a control over the matcher, never as the assertion about the product.
	const shipped = "поэтому показано только то, что удалось прочитать до этого места"
	if !containsAny(shipped, partialNoteFileProvenanceClaims) {
		t.Fatal("the provenance scan does not detect the claim it was handed directly")
	}

	for _, claim := range partialNoteFileProvenanceClaims {
		if strings.Contains(text, claim) {
			t.Errorf("note claims %q, but nothing above it came from the file:\n%s", claim, text)
		}
	}
	// And the positive half: the note has to leave room for what actually
	// happened, namely that the sections above are the HTTP service's.
	if !strings.Contains(text, "HTTP-сервис") {
		t.Errorf("the note never mentions the other source, so a reader takes the "+
			"composition above for dump content:\n%s", text)
	}
}

// TestNewFormStructureHandler_PartialNoteDoesNotClaimHTTPOnlyProvenance is the
// same wording checked against the OPPOSITE body: a partial parse that did
// contribute elements. The sibling note (formNoFormRootNote) may state flatly
// that everything above came from the HTTP service, because a file that never
// entered a <Form> contributed nothing; copying that sentence into this note
// would be false here, and this test is what stops the copy.
func TestNewFormStructureHandler_PartialNoteDoesNotClaimHTTPOnlyProvenance(t *testing.T) {
	srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	// keep=2 complete fields plus the severed third, measured as 3 elements.
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента",
		truncatedFormXML(2))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("a tolerated malformed Form.xml must not become a hard error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, partialNoteMarker) {
		t.Fatalf("body must say the form file was read only partially:\n%s", text)
	}
	// Premise: the dump DID contribute the elements it read before the cut.
	for _, want := range truncatedFieldNames[:3] {
		if !strings.Contains(text, want) {
			t.Fatalf("premise broken: element %q read before the cut is not in the body:\n%s", want, text)
		}
	}

	// Claims that everything above is HTTP content. Each is false for this body.
	httpOnlyClaims := []string{
		"Всё, что показано выше в разделах состава формы, вернул HTTP-сервис 1С",
		"из выгрузки ничего не взято",
		"выгрузка не дала ничего",
	}
	const control = "Всё, что показано выше в разделах состава формы, вернул HTTP-сервис 1С."
	if !containsAny(control, httpOnlyClaims) {
		t.Fatal("the HTTP-only scan does not detect the claim it was handed directly")
	}
	for _, claim := range httpOnlyClaims {
		if strings.Contains(text, claim) {
			t.Errorf("note claims %q while the body above lists elements read from the dump:\n%s",
				claim, text)
		}
	}
}

// containsAny reports whether s contains at least one of the substrings. Used to
// give the phrase scans in this file a positive control.
func containsAny(s string, substrings []string) bool {
	for _, sub := range substrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestNewFormStructureHandler_WellFormedDumpHasNoPartialNote is the negative
// control for the partial note specifically. It is deliberately separate from
// the two broader no-notes controls so that a mutation making the note
// unconditional names this test when it fails.
func TestNewFormStructureHandler_WellFormedDumpHasNoPartialNote(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента",
		sampleFormXML())

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, "Контрагент") {
		t.Fatalf("expected the parsed structure in the body:\n%s", text)
	}
	if strings.Contains(text, partialNoteMarker) {
		t.Errorf("a complete Form.xml must not carry the partial parse note:\n%s", text)
	}
}

// noFormRootNoteMarker is the distinguishing phrase of the note added when the
// dump's Form.xml WAS opened and read to its end but held no form at all.
// Matched as a phrase rather than through the constant, exactly as the two
// markers above are, so these tests compile against a build that does not carry
// the constant yet and fail on behaviour instead of failing to build.
const noFormRootNoteMarker = "описания формы в нём нет"

// formlessFiles are Form.xml contents that a dump can plausibly contain and that
// hold no form: the classes the syntax-error flag structurally cannot see,
// because each one is read to a clean end of document.
var formlessFiles = map[string]string{
	"empty file":           "",
	"plain text":           "выгрузка не удалась",
	"wrong root element":   `<?xml version="1.0" encoding="UTF-8"?><ExternalDataProcessor><Name>Отчёт</Name></ExternalDataProcessor>`,
	"xml declaration only": `<?xml version="1.0" encoding="UTF-8"?>`,
}

// TestNewFormStructureHandler_FormlessFormXMLAddsItsOwnNote is the headline case
// for the second silent class. The Form.xml exists, is a regular file, opens and
// reads cleanly, and simply is not a form. The parser reports success, so no
// error is raised and nothing is logged, and without a note the caller reads a
// confident answer about a form whose file described nothing.
func TestNewFormStructureHandler_FormlessFormXMLAddsItsOwnNote(t *testing.T) {
	for name, content := range formlessFiles {
		t.Run(name, func(t *testing.T) {
			srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

			dumpDir := t.TempDir()
			writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента", content)

			result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
			if err != nil {
				t.Fatalf("a formless Form.xml must not become a hard error: %v", err)
			}
			text := resultText(t, result)

			if !strings.Contains(text, noFormRootNoteMarker) {
				t.Errorf("body must say the form file held no form:\n%s", text)
			}
			// The partial note describes the OTHER cause and prescribes the other
			// remedy. This file was not truncated, it was read whole, so claiming
			// the read stopped early would send the user after damage that is not
			// there. It also literally contradicts this note's own first clause.
			if strings.Contains(text, partialNoteMarker) {
				t.Errorf("the file was read in full, so the partial parse note must not "+
					"appear alongside a note saying exactly that:\n%s", text)
			}
			// Nor is this the "dump gave us nothing" case: the dump WAS found, the
			// form directory WAS listed and the file WAS read.
			if strings.Contains(text, dumpNoteMarker) {
				t.Errorf("the dump was read, so the unreadable-dump note must stay away:\n%s", text)
			}
			// The HTTP half of the answer is still valid and must survive.
			if !strings.Contains(text, "ФормаДокумента") {
				t.Errorf("the valid part of the answer must still be returned:\n%s", text)
			}
		})
	}
}

// TestNewFormStructureHandler_FormlessNoteDoesNotDenyHTTPSections is the wording
// guard, the twin of the one on the partial note. When the dump holds no form
// the response body is whatever the 1C HTTP service returned, and that can be a
// full structure. A note phrased as "there are no elements" is then falsified by
// the table printed directly above it, which is the defect this codebase has
// already hit twice.
func TestNewFormStructureHandler_FormlessNoteDoesNotDenyHTTPSections(t *testing.T) {
	// An HTTP service that DOES fill all three sections.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":  "ФормаДокумента",
			"title": "Реализация товаров и услуг",
			"elements": []any{
				map[string]any{"name": "ЭлементОтСервиса", "type": "ПолеВвода", "dataPath": "Объект.Контрагент"},
			},
			"commands": []any{map[string]any{"name": "КомандаОтСервиса", "action": "Провести"}},
			"handlers": []any{map[string]any{"event": "OnOpen", "handler": "ОбработчикОтСервиса"}},
		})
	}))
	t.Cleanup(srv.Close)

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента", "")

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, noFormRootNoteMarker) {
		t.Fatalf("body must say the form file held no form:\n%s", text)
	}
	// Everything the HTTP service supplied is still in the body.
	for _, want := range []string{"ЭлементОтСервиса", "КомандаОтСервиса", "ОбработчикОтСервиса"} {
		if !strings.Contains(text, want) {
			t.Errorf("HTTP-supplied %q must still be listed:\n%s", want, text)
		}
	}
	// And the note must not deny them. Each phrase is one a note author could
	// plausibly reach for, and every one of them would be false here.
	for _, denial := range []string{
		"элементы отсутствуют",
		"нет элементов",
		"состав формы пуст",
		"форма не содержит элементов",
		"элементы не прочитаны",
		"ничего не прочитано",
	} {
		if strings.Contains(text, denial) {
			t.Errorf("note claims %q while the body above lists elements:\n%s", denial, text)
		}
	}
}

// TestNewFormStructureHandler_ParseNotesAreNeverBothPresent pins the invariant
// at the level the user actually sees. The two notes make opposite claims about
// the same read, one that the file was read whole and one that it was not, so a
// response carrying both is self-contradicting whatever else it gets right. The
// exclusivity is guaranteed inside the parser; this checks it survives all the
// way out to the response body, for a file of each kind.
func TestNewFormStructureHandler_ParseNotesAreNeverBothPresent(t *testing.T) {
	cases := map[string]struct {
		xml         string
		wantPartial bool
	}{
		"truncated inside the form is only partial": {truncatedFormXML(1), true},
		// The case that actually exercises exclusivity here. A file truncated
		// INSIDE <Form> can never trip the formless flag, because the form was
		// already entered, so it cannot detect a parser that sets both. This one
		// is cut off before any <Form> appears, which is precisely the input for
		// which both conditions are live at once.
		"truncated before the form is only partial": {
			`<?xml version="1.0" encoding="UTF-8"?><ExternalDataProcessor><Name`, true,
		},
		"empty file is only formless": {"", false},
		"plain text is only formless": {"это не xml", false},
		"complete form has neither":   {sampleFormXML(), false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")
			dumpDir := t.TempDir()
			writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента", tc.xml)

			result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			text := resultText(t, result)

			gotPartial := strings.Contains(text, partialNoteMarker)
			gotFormless := strings.Contains(text, noFormRootNoteMarker)
			if gotPartial && gotFormless {
				t.Fatalf("both parse notes present, they contradict each other:\n%s", text)
			}
			if gotPartial != tc.wantPartial {
				t.Errorf("partial note present = %v, want %v:\n%s", gotPartial, tc.wantPartial, text)
			}
			// A complete form is the only case here that must carry no note at
			// all; the two broken ones must each carry exactly their own.
			if name == "complete form has neither" && gotFormless {
				t.Errorf("a healthy form must carry no note:\n%s", text)
			}
		})
	}
}

// emptyFormXML is a Form.xml that is complete, well formed, and declares no
// contents at all. The parser reads it to a clean end INSIDE a <Form>, so
// neither silent-outcome flag is set: no partial parse, no missing form root.
// The dump simply yields a form with nothing in it.
func emptyFormXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
</Form>`
}

// namedFormNoStructureMarker is the distinguishing phrase of the note added when
// form_name DID resolve to a form in the dump but that form contributed no
// composition. Matched as a phrase rather than through the constant, exactly as
// the other markers in this file are.
const namedFormNoStructureMarker = "не дал состава формы"

// TestNewFormStructureHandler_KnownFormNameWithoutStructureIsReported closes the
// asymmetry: an UNKNOWN form_name is a hard error listing the real names, while
// a KNOWN form_name whose form yields nothing used to return the HTTP service's
// answer with no sign that the parameter changed nothing. Both are the same user
// intent ("answer about THIS form"), and only one of them was answered honestly.
//
// It stays a normal result on purpose: only the unknown-name case is fatal.
func TestNewFormStructureHandler_KnownFormNameWithoutStructureIsReported(t *testing.T) {
	cases := map[string]string{
		"form declares nothing":       emptyFormXML(),
		"file holds no form at all":   "",
		"file breaks before contents": truncatedBeforeAnyElementXML(),
	}

	for name, xml := range cases {
		t.Run(name, func(t *testing.T) {
			srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

			dumpDir := t.TempDir()
			writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаЭлемента", xml)

			result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "ФормаЭлемента")
			if err != nil {
				t.Fatalf("a known form_name that yields no structure must not be a hard error: %v", err)
			}
			text := resultText(t, result)

			// Premise: the composition on screen is the HTTP service's, chosen by
			// the service and not by form_name.
			if !strings.Contains(text, "ЭлементОтСервиса") {
				t.Fatalf("premise broken: the HTTP element is missing from the body:\n%s", text)
			}

			if !strings.Contains(text, namedFormNoStructureMarker) {
				t.Errorf("body never says the form_name gave no structure:\n%s", text)
			}
			if !strings.Contains(text, "form_name") {
				t.Errorf("body never names the parameter that did not take effect:\n%s", text)
			}
		})
	}
}

// TestNewFormStructureHandler_KnownFormNameWithoutStructureShowsAnotherForm is
// the sharpest version of the same case: the answer on screen is headed by the
// form the HTTP service picked, which is NOT the one the caller named. Without
// the note the response is a confident answer about a form nobody asked about,
// which is the exact harm the unknown-name hard error exists to prevent.
func TestNewFormStructureHandler_KnownFormNameWithoutStructureShowsAnotherForm(t *testing.T) {
	srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаЭлемента", emptyFormXML())

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "ФормаЭлемента")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	// Premise: the header names the HTTP form, not the requested one.
	if !strings.Contains(text, "# Форма: ФормаДокумента") {
		t.Fatalf("premise broken: the body is not headed by the HTTP-chosen form:\n%s", text)
	}
	if !strings.Contains(text, namedFormNoStructureMarker) {
		t.Fatalf("body never says the form_name gave no structure:\n%s", text)
	}
	// And it has to say WHO chose the form that is shown, otherwise the caller
	// cannot tell that the header belongs to someone else's choice.
	if !strings.Contains(text, "HTTP-сервис") {
		t.Errorf("body never says the shown form was chosen by the 1C HTTP service:\n%s", text)
	}
}

// TestNewFormStructureHandler_HealthyFormNameHasNoEmptyStructureNote is the
// negative control: a form_name that resolves to a form with contents is not a
// degraded path and must carry no note at all.
func TestNewFormStructureHandler_HealthyFormNameHasNoEmptyStructureNote(t *testing.T) {
	srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаЭлемента",
		formXMLWithTitle("Карточка реализации", "ПолеЭлемента"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "ФормаЭлемента")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, "ПолеЭлемента") {
		t.Fatalf("premise broken: the dump structure is missing from the body:\n%s", text)
	}
	if strings.Contains(text, namedFormNoStructureMarker) {
		t.Errorf("a form_name that produced a full structure must carry no note:\n%s", text)
	}
}

// TestNewFormStructureHandler_NoFormNameKeepsTheEmptyFormSilent pins the scope of
// the note: it answers for a form_name that did not take effect, so a caller who
// named no form must not receive it. Without this control the note would also
// fire for the default alphabetical pick, where no parameter was ignored.
func TestNewFormStructureHandler_NoFormNameKeepsTheEmptyFormSilent(t *testing.T) {
	srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаЭлемента", emptyFormXML())

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if strings.Contains(text, namedFormNoStructureMarker) {
		t.Errorf("no form_name was passed, so no note about form_name may appear:\n%s", text)
	}
}

// TestNewFormStructureHandler_NoFormsInDumpWordingFitsAbsence checks the note
// pair the zero-forms case actually produces. The object has no forms in the
// dump at all AND the caller named one, which routes through the same degraded
// branch as an unreadable dump. The form_name half of that pair must not
// describe the situation as a failure to read a form, because there was no form
// to read: that wording sends the user looking for a corrupt file that does not
// exist.
func TestNewFormStructureHandler_NoFormsInDumpWordingFitsAbsence(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	// A readable dump that simply holds no forms for the requested object.
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Catalogs", "Контрагенты", "ФормаСписка",
		formXMLWithTitle("Контрагенты", "ПолеСписка"))

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "ФормаСписка")
	if err != nil {
		t.Fatalf("an object with no forms in the dump must not fail the call: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, "form_name") {
		t.Fatalf("body must say the form_name lookup did not happen:\n%s", text)
	}
	// "прочитать её не удалось" asserts a read failure on a form that was never
	// there. The absence case has to be covered by the wording, not contradicted
	// by it.
	if strings.Contains(text, "прочитать её не удалось") {
		t.Errorf("form_name note blames a failed read of a form that does not exist in the dump:\n%s", text)
	}
	// The partial parse note belongs to a file that WAS opened. Nothing was.
	if strings.Contains(text, partialNoteMarker) {
		t.Errorf("no form file was opened, so the partial parse note must not appear:\n%s", text)
	}
}
