package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/internal/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSearchCodeTool(t *testing.T) {
	tool := SearchCodeTool()
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name != "search_code" {
		t.Errorf("expected tool name %q, got %q", "search_code", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestFormatSearchResult(t *testing.T) {
	r := &onec.SearchResult{
		Matches: []onec.SearchMatch{
			{
				Module:  "Справочник.Контрагенты.МодульОбъекта",
				Line:    42,
				Context: "Процедура ПередЗаписью(Отказ)\n    // проверка заполнения\nКонецПроцедуры",
			},
			{
				Module:  "Документ.РеализацияТоваров.МодульОбъекта",
				Line:    15,
				Context: "Функция ПолучитьКонтрагента()\n    Возврат Контрагент;\nКонецФункции",
			},
		},
		Total: 2,
	}

	text := formatSearchResult(r, "Контрагент")

	for _, want := range []string{
		"Результаты поиска",
		"Контрагент",
		"2 совпадений",
		"Справочник.Контрагенты.МодульОбъекта",
		"строка 42",
		"```bsl",
		"ПередЗаписью",
		"Документ.РеализацияТоваров.МодульОбъекта",
		"строка 15",
		"ПолучитьКонтрагента",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, text)
		}
	}
}

func TestFormatSearchResult_Empty(t *testing.T) {
	r := &onec.SearchResult{
		Matches: []onec.SearchMatch{},
		Total:   0,
	}

	text := formatSearchResult(r, "НесуществующаяФункция")

	if !strings.Contains(text, "Ничего не найдено") {
		t.Errorf("expected 'Ничего не найдено' in text, got:\n%s", text)
	}
	if !strings.Contains(text, "0 совпадений") {
		t.Errorf("expected '0 совпадений' in text, got:\n%s", text)
	}
}

func TestFormatSearchResult_Truncated(t *testing.T) {
	r := &onec.SearchResult{
		Matches: []onec.SearchMatch{
			{
				Module:  "Модуль.Тест",
				Line:    1,
				Context: "Тест",
			},
		},
		Total: 150,
	}

	text := formatSearchResult(r, "Тест")

	if !strings.Contains(text, "Показано 1 из 150 совпадений") {
		t.Errorf("expected truncation message, got:\n%s", text)
	}
	if !strings.Contains(text, "увеличьте limit") {
		t.Errorf("expected limit hint in text, got:\n%s", text)
	}
}

func TestNewSearchCodeHandler(t *testing.T) {
	const mockResponse = `{
		"matches": [
			{
				"module": "Справочник.Номенклатура.МодульОбъекта",
				"line": 10,
				"context": "Процедура ОбновитьЦены()\n    // обновление цен\nКонецПроцедуры"
			}
		],
		"total": 1
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST method, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		defer r.Body.Close()

		var reqBody onec.SearchRequest
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("failed to unmarshal request body: %v", err)
		}
		if reqBody.Query == "" {
			t.Error("expected non-empty query in request body")
		}
		if reqBody.Limit <= 0 {
			t.Error("expected positive limit in request body")
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewSearchCodeHandler(client)

	args, _ := json.Marshal(map[string]any{
		"query": "ОбновитьЦены",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "search_code",
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
		"Справочник.Номенклатура.МодульОбъекта",
		"строка 10",
		"ОбновитьЦены",
		"1 совпадений",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}
