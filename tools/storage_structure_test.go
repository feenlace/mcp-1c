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

const storageMockResponse = `{
	"name": "Номенклатура",
	"full_name": "Справочник.Номенклатура",
	"tables": [
		{
			"storage_name": "_Reference441",
			"table_name": "Справочник.Номенклатура",
			"metadata": "Справочник.Номенклатура",
			"purpose": "Основная",
			"fields": [
				{"name": "Ссылка", "storage_name": "_IDRRef", "metadata": "Справочник.Номенклатура.СтандартныйРеквизит.Ссылка"},
				{"name": "Артикул", "storage_name": "_Fld52474", "metadata": "Справочник.Номенклатура.Реквизит.Артикул"},
				{"name": "Pipe|Name", "storage_name": "_Fld1", "metadata": ""}
			],
			"indexes": [
				{"name": "ByCode", "storage_name": "_Reference441_ByCode", "fields": [
					{"name": "Код", "storage_name": "_Code", "metadata": ""}
				]},
				{"name": "", "storage_name": "_Reference441_ByDescr", "fields": [
					{"name": "Наименование", "storage_name": "_Description", "metadata": ""}
				]}
			]
		},
		{
			"storage_name": "_Reference441_VT52586",
			"table_name": "Справочник.Номенклатура.Представления",
			"metadata": "Справочник.Номенклатура.ТабличнаяЧасть.Представления",
			"purpose": "ТабличнаяЧасть",
			"fields": [],
			"indexes": []
		}
	]
}`

func storageMockServer(t *testing.T) *onec.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storage/Catalog/Номенклатура" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET method, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(storageMockResponse))
	}))
	t.Cleanup(srv.Close)
	return onec.NewClient(srv.URL, "", "")
}

func callStorage(t *testing.T, client *onec.Client, args string) (*mcp.CallToolResult, error) {
	t.Helper()
	handler := NewStorageStructureHandler(client)
	return handler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_storage_structure",
			Arguments: json.RawMessage(args),
		},
	})
}

func TestStorageStructureHandler(t *testing.T) {
	result, err := callStorage(t, storageMockServer(t), `{"object_type":"Catalog","object_name":"Номенклатура"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("expected a successful result, got %+v", result)
	}
	text := result.Content[0].(*mcp.TextContent).Text

	for _, want := range []string{
		"# Хранение `Справочник.Номенклатура` в СУБД",
		"Таблиц: 2.",
		"Пустые метаданные означают, что платформа не вернула сопоставление",
		"## `_Reference441` (Основная)",
		"Имя в языке запросов: `Справочник.Номенклатура`",
		"| Артикул | _Fld52474 | Справочник.Номенклатура.Реквизит.Артикул |",
		"| Pipe\\|Name | _Fld1 |  |",
		"`_Reference441_ByCode` (ByCode): _Code",
		"- `_Reference441_ByDescr`: _Description",
		"## `_Reference441_VT52586` (ТабличнаяЧасть)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, text)
		}
	}
}

func TestStorageStructureHandler_NoTables(t *testing.T) {
	st := onec.StorageStructure{Name: "Тест", FullName: "Обработка.Тест"}
	text := FormatStorageStructure(&st)
	if !strings.Contains(text, "нет собственных таблиц") {
		t.Errorf("an object without tables must say so, got:\n%s", text)
	}
}

func TestStorageStructureHandler_RequiresBothArguments(t *testing.T) {
	for _, args := range []string{`{}`, `{"object_type":"Catalog"}`, `{"object_name":"Номенклатура"}`} {
		result, err := callStorage(t, storageMockServer(t), args)
		if err != nil {
			t.Fatalf("%s: a missing argument is an operational failure, not an error: %v", args, err)
		}
		if result == nil || !result.IsError {
			t.Fatalf("%s: expected IsError, got %+v", args, result)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, headingStorage) || !strings.Contains(text, "object_type and object_name are required") {
			t.Errorf("%s: unexpected failure text:\n%s", args, text)
		}
	}
}

func TestStorageStructureHandler_ExtensionRefusal(t *testing.T) {
	const detail = "storage structure is not available: Недостаточно прав для выполнения операции"
	result, err := callStorage(t, envelope1C(t, 500, detail), `{"object_type":"Catalog","object_name":"Номенклатура"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := failureText(t, result, err)
	if !strings.Contains(text, headingStorage) || !strings.Contains(text, "Недостаточно прав") {
		t.Errorf("the platform's own refusal text must reach the caller, got:\n%s", text)
	}
}

func TestStorageStructureTool(t *testing.T) {
	tool := StorageStructureTool()
	if tool.Name != "get_storage_structure" {
		t.Errorf("expected tool name %q, got %q", "get_storage_structure", tool.Name)
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
		t.Error("expected ReadOnlyHint annotation")
	}
	var schema struct {
		Required []string `json:"required"`
	}
	raw, ok := tool.InputSchema.(json.RawMessage)
	if !ok {
		t.Fatalf("InputSchema is %T, want json.RawMessage", tool.InputSchema)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if len(schema.Required) != 2 {
		t.Errorf("expected object_type and object_name to be required, got %v", schema.Required)
	}
}

func TestStorageStructureHandler_RejectsRouteInjection(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(storageMockResponse))
	}))
	defer srv.Close()
	client := onec.NewClient(srv.URL, "", "")
	for _, input := range [][2]string{
		{"Catalog", "Номенклатура#ignored"}, {"Catalog", "Номенклатура?ignored=1"},
		{"Catalog", "../Номенклатура"}, {"Catalog", "Номенклатура/Другая"},
		{"Catalog", "Номенклатура%23ignored"}, {"Catalog", "Номенклатура\n"},
		{"Catalog", "1Номенклатура"}, {"Catalog/Document", "Номенклатура"},
		{"Unknown", "Номенклатура"},
	} {
		t.Run(input[0]+"/"+input[1], func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"object_type": input[0], "object_name": input[1]})
			res, err := callStorage(t, client, string(args))
			if err != nil || res == nil || !res.IsError {
				t.Fatalf("invalid metadata input must fail, got result=%+v err=%v", res, err)
			}
		})
	}
	if requests != 0 {
		t.Errorf("invalid input dispatched %d requests; want none", requests)
	}
}

func TestStorageStructureHandler_ResponseContract(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{"empty object", `{}`, true},
		{"null", `null`, true},
		{"200 error", `{"error":"upstream unavailable"}`, true},
		{"missing tables", `{"name":"Номенклатура","full_name":"Справочник.Номенклатура"}`, true},
		{"null tables", `{"name":"Номенклатура","full_name":"Справочник.Номенклатура","tables":null}`, true},
		{"wrong identity", `{"name":"Другая","full_name":"Справочник.Другая","tables":[]}`, true},
		{"wrong kind", `{"name":"Номенклатура","full_name":"Документ.Номенклатура","tables":[]}`, true},
		{"incomplete table", `{"name":"Номенклатура","full_name":"Справочник.Номенклатура","tables":[{}]}`, true},
		{"missing field storage name", `{"name":"Номенклатура","full_name":"Справочник.Номенклатура","tables":[{"storage_name":"_Reference1","fields":[{}],"indexes":[]}]}`, true},
		{"missing index fields", `{"name":"Номенклатура","full_name":"Справочник.Номенклатура","tables":[{"storage_name":"_Reference1","fields":[],"indexes":[{}]}]}`, true},
		{"truncated json", `{"name":"Номенклатура","tables":[`, true},
		{"case-insensitive identity", `{"name":"номенклатура","full_name":"Справочник.номенклатура","tables":[]}`, false},
		{"valid empty", `{"name":"Номенклатура","full_name":"Справочник.Номенклатура","tables":[]}`, false},
		{"valid mapped", storageMockResponse, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer srv.Close()
			res, err := callStorage(t, onec.NewClient(srv.URL, "", ""), `{"object_type":"Catalog","object_name":"Номенклатура"}`)
			if err != nil || res == nil || res.IsError != tc.wantError {
				t.Fatalf("want IsError=%v, result=%+v err=%v", tc.wantError, res, err)
			}
		})
	}
}
