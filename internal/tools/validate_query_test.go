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

func TestValidateQueryTool(t *testing.T) {
	tool := ValidateQueryTool()
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name != "validate_query" {
		t.Errorf("expected tool name %q, got %q", "validate_query", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestFormatValidateResult_Valid(t *testing.T) {
	r := &onec.ValidateQueryResult{
		Valid:  true,
		Errors: nil,
	}

	text := formatValidateResult(r)

	if !strings.Contains(text, "Результат проверки") {
		t.Errorf("expected header in text, got:\n%s", text)
	}
	if !strings.Contains(text, "✅ Запрос корректен") {
		t.Errorf("expected valid marker in text, got:\n%s", text)
	}
}

func TestFormatValidateResult_Invalid(t *testing.T) {
	r := &onec.ValidateQueryResult{
		Valid: false,
		Errors: []string{
			"Ожидается ключевое слово ИЗ (строка 1, позиция 25)",
			"Неизвестное имя таблицы Справочник.Контагенты (строка 1, позиция 30)",
		},
	}

	text := formatValidateResult(r)

	if !strings.Contains(text, "Результат проверки") {
		t.Errorf("expected header in text, got:\n%s", text)
	}
	if !strings.Contains(text, "❌") {
		t.Errorf("expected error marker in text, got:\n%s", text)
	}
	for _, want := range r.Errors {
		if !strings.Contains(text, want) {
			t.Errorf("expected text to contain error %q, got:\n%s", want, text)
		}
	}
}

func TestNewValidateQueryHandler_Valid(t *testing.T) {
	const mockResponse = `{"valid": true, "errors": []}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/validate-query" {
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

		var reqBody onec.ValidateQueryRequest
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("failed to unmarshal request body: %v", err)
		}
		if reqBody.Query == "" {
			t.Error("expected non-empty query in request body")
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewValidateQueryHandler(client)

	args, _ := json.Marshal(map[string]any{
		"query": "ВЫБРАТЬ Наименование ИЗ Справочник.Контрагенты",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "validate_query",
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
	if !strings.Contains(tc.Text, "✅ Запрос корректен") {
		t.Errorf("expected valid result marker, got:\n%s", tc.Text)
	}
}

func TestNewValidateQueryHandler_Invalid(t *testing.T) {
	const mockResponse = `{
		"valid": false,
		"errors": [
			"Ожидается ключевое слово ИЗ (строка 1, позиция 25)",
			"Неизвестное имя таблицы (строка 1, позиция 30)"
		]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/validate-query" {
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

		var reqBody onec.ValidateQueryRequest
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("failed to unmarshal request body: %v", err)
		}
		if reqBody.Query == "" {
			t.Error("expected non-empty query in request body")
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewValidateQueryHandler(client)

	args, _ := json.Marshal(map[string]any{
		"query": "ВЫБРАТЬ Наименование Справочник.Контагенты",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "validate_query",
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
	if !strings.Contains(tc.Text, "❌") {
		t.Errorf("expected error marker, got:\n%s", tc.Text)
	}
	for _, want := range []string{
		"Ожидается ключевое слово ИЗ",
		"Неизвестное имя таблицы",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}
