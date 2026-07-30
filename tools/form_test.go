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
