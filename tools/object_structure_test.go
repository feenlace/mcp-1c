package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestObjectStructureHandler(t *testing.T) {
	const mockResponse = `{
		"name": "РеализацияТоваровУслуг",
		"synonym": "Реализация товаров и услуг",
		"attributes": [
			{"name": "Контрагент", "synonym": "Контрагент", "type": "СправочникСсылка.Контрагенты"},
			{"name": "Сумма", "synonym": "Сумма документа", "type": "Число"}
		],
		"tabularParts": [
			{
				"name": "Товары",
				"attributes": [
					{"name": "Номенклатура", "synonym": "Номенклатура", "type": "СправочникСсылка.Номенклатура"},
					{"name": "Количество", "synonym": "Количество", "type": "Число"}
				]
			}
		]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/Document/РеализацияТоваровУслуг" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "Document",
		"object_name": "РеализацияТоваровУслуг",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
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
		"РеализацияТоваровУслуг",
		"Реализация товаров и услуг",
		"Контрагент",
		"Сумма",
		"Товары",
		"Номенклатура",
		"Количество",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestObjectStructureHandler_Register(t *testing.T) {
	const mockResponse = `{
		"name": "ТоварыНаСкладах",
		"synonym": "Товары на складах",
		"dimensions": [
			{"name": "Номенклатура", "synonym": "Номенклатура", "type": "СправочникСсылка.Номенклатура"},
			{"name": "Склад", "synonym": "Склад", "type": "СправочникСсылка.Склады"}
		],
		"resources": [
			{"name": "Количество", "synonym": "Количество", "type": "Число"}
		],
		"attributes": []
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/AccumulationRegister/ТоварыНаСкладах" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "AccumulationRegister",
		"object_name": "ТоварыНаСкладах",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	for _, want := range []string{
		"ТоварыНаСкладах", "Измерения", "Номенклатура", "Склад",
		"Ресурсы", "Количество",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestObjectStructureHandler_Enum(t *testing.T) {
	const mockResponse = `{
		"name": "СтатусыЗаказов",
		"synonym": "Статусы заказов",
		"attributes": [],
		"values": [
			{"name": "Новый", "synonym": "Новый", "comment": "Заказ только что создан"},
			{"name": "ВРаботе", "synonym": "В работе", "comment": "Заказ обрабатывается"},
			{"name": "Закрыт", "synonym": "Закрыт", "comment": ""}
		]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/Enum/СтатусыЗаказов" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "Enum",
		"object_name": "СтатусыЗаказов",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	for _, want := range []string{
		"СтатусыЗаказов",
		"Статусы заказов",
		"Значения",
		"Новый",
		"ВРаботе",
		"В работе",
		"Закрыт",
		"Заказ только что создан",
		"Заказ обрабатывается",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestObjectStructureHandler_DefinedType(t *testing.T) {
	const mockResponse = `{
		"name": "ЗначениеДоступа",
		"synonym": "Значение доступа",
		"types": [
			"СправочникСсылка.Пользователи",
			"СправочникСсылка.ВнешниеПользователи"
		]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/DefinedType/ЗначениеДоступа" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "DefinedType",
		"object_name": "ЗначениеДоступа",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	for _, want := range []string{
		"ЗначениеДоступа",
		"Значение доступа",
		"## Состав типа",
		"СправочникСсылка.Пользователи",
		"СправочникСсылка.ВнешниеПользователи",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestObjectStructureHandler_MissingArgs(t *testing.T) {
	client := onec.NewClient("http://localhost:0", "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing arguments")
	}
}

func TestObjectStructureTool(t *testing.T) {
	tool := ObjectStructureTool()
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name != "get_object_structure" {
		t.Errorf("expected tool name %q, got %q", "get_object_structure", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}
}

// TestFormatObjectStructure_TypesGuard codifies that the "## Состав типа" block
// is emitted iff len(Types) > 0, so existing objects' output is unchanged.
func TestFormatObjectStructure_TypesGuard(t *testing.T) {
	cases := []struct {
		name  string
		types []string
		want  bool
	}{
		{"nil", nil, false},
		{"empty", []string{}, false},
		{"one", []string{"СправочникСсылка.Пользователи"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := formatObjectStructure(&onec.ObjectStructure{
				Name:    "ЗначениеДоступа",
				Synonym: "Значение доступа",
				Types:   tc.types,
			})
			got := strings.Contains(out, "## Состав типа")
			if got != tc.want {
				t.Errorf("block present = %v, want %v; output:\n%s", got, tc.want, out)
			}
		})
	}
}

// TestFormatObjectStructure_DefinedTypeNoDash guards the no-тире rule: the RU
// composition block must use ASCII list markers, never em-dash / en-dash.
func TestFormatObjectStructure_DefinedTypeNoDash(t *testing.T) {
	out := formatObjectStructure(&onec.ObjectStructure{
		Name:    "ЗначениеДоступа",
		Synonym: "Значение доступа",
		Types:   []string{"СправочникСсылка.Пользователи", "СправочникСсылка.ВнешниеПользователи"},
	})
	idx := strings.Index(out, "## Состав типа")
	if idx < 0 {
		t.Fatalf("expected composition block, got:\n%s", out)
	}
	block := out[idx:]
	if strings.ContainsRune(block, '—') || strings.ContainsRune(block, '–') {
		t.Errorf("composition block contains em/en-dash, violates no-тире rule:\n%s", block)
	}
}

// TestObjectStructureTool_DefinedTypeSchema proves the tool description advertises
// DefinedType and its types field, and that object_type stays free-text (no enum).
func TestObjectStructureTool_DefinedTypeSchema(t *testing.T) {
	raw, ok := ObjectStructureTool().InputSchema.(json.RawMessage)
	if !ok {
		t.Fatalf("InputSchema type = %T, want json.RawMessage", ObjectStructureTool().InputSchema)
	}
	schema := string(raw)
	for _, want := range []string{
		"DefinedType",
		"ОпределяемыеТипы->DefinedType",
		"Для DefinedType возвращается поле types",
	} {
		if !strings.Contains(schema, want) {
			t.Errorf("object_type schema missing %q", want)
		}
	}
	if strings.Contains(schema, `"enum"`) {
		t.Errorf("object_type must remain free-text (no enum), schema:\n%s", schema)
	}
}
