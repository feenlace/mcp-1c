package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestQueryHandler(t *testing.T) {
	const mockResponse = `{
		"columns": ["Наименование", "ИНН"],
		"rows": [["ООО Ромашка", "7701234567"], ["ИП Петров", "772987654321"]],
		"total": 2,
		"truncated": false
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/query" {
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
			t.Errorf("failed to read request body: %v", err)
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		defer r.Body.Close()

		var reqBody onec.QueryRequest
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Errorf("failed to unmarshal request body: %v", err)
			http.Error(w, "failed to unmarshal body", http.StatusBadRequest)
			return
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
	handler := NewQueryHandler(client)

	args, _ := json.Marshal(map[string]any{
		"query": "ВЫБРАТЬ Наименование, ИНН ИЗ Справочник.Контрагенты",
		"limit": 50,
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "execute_query",
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
		"Наименование",
		"ИНН",
		"ООО Ромашка",
		"7701234567",
		"ИП Петров",
		"772987654321",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestQueryHandler_NonSelectRejected(t *testing.T) {
	httpCalled := false
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalled = true
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewQueryHandler(client)

	tests := []struct {
		name  string
		query string
	}{
		{"DELETE", "DELETE FROM Справочник.Контрагенты"},
		{"UPDATE", "UPDATE Справочник.Контрагенты SET Наименование = 'test'"},
		{"DROP", "DROP TABLE Справочник.Контрагенты"},
		{"УДАЛИТЬ", "УДАЛИТЬ Справочник.Контрагенты"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpCalled = false

			args, _ := json.Marshal(map[string]any{
				"query": tt.query,
			})
			req := &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name:      "execute_query",
					Arguments: args,
				},
			}

			res, err := handler(context.Background(), req)
			// The refusal is now a tool result with IsError. It still refuses,
			// and it still says which keywords are allowed.
			text := failureText(t, res, err)
			if !strings.Contains(text, "SELECT") && !strings.Contains(text, "ВЫБРАТЬ") {
				t.Errorf("expected the failure to mention SELECT or ВЫБРАТЬ, got:\n%s", text)
			}
			if httpCalled {
				t.Error("HTTP call should not have been made for non-SELECT query")
			}
		})
	}
}

func TestQueryHandler_Truncated(t *testing.T) {
	const mockResponse = `{
		"columns": ["Наименование"],
		"rows": [["Тест"]],
		"total": 500,
		"truncated": true
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewQueryHandler(client)

	args, _ := json.Marshal(map[string]any{
		"query": "ВЫБРАТЬ Наименование ИЗ Справочник.Контрагенты",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "execute_query",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}

	if !strings.Contains(tc.Text, "усечён") {
		t.Errorf("expected text to contain 'усечён', got:\n%s", tc.Text)
	}
}

func TestQueryHandler_WithParameters(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}

		var reqBody struct {
			Query      string         `json:"query"`
			Limit      int            `json:"limit"`
			Parameters map[string]any `json:"parameters"`
		}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Errorf("failed to unmarshal: %v", err)
			http.Error(w, "unmarshal error", http.StatusBadRequest)
			return
		}

		if reqBody.Parameters == nil {
			t.Error("expected non-nil parameters")
			http.Error(w, "no parameters", http.StatusBadRequest)
			return
		}
		if _, ok := reqBody.Parameters["Контрагент"]; !ok {
			t.Error("expected parameter 'Контрагент'")
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"columns":["Наименование"],"rows":[["ООО Ромашка"]],"total":1,"truncated":false}`))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewQueryHandler(client)

	args, _ := json.Marshal(map[string]any{
		"query": "ВЫБРАТЬ Наименование ИЗ Справочник.Контрагенты ГДЕ Ссылка = &Контрагент",
		"parameters": map[string]any{
			"Контрагент": "ООО Ромашка",
		},
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "execute_query",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(tc.Text, "ООО Ромашка") {
		t.Errorf("expected result to contain 'ООО Ромашка', got:\n%s", tc.Text)
	}
}

func TestQueryTool(t *testing.T) {
	tool := QueryTool()
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name != "execute_query" {
		t.Errorf("expected tool name %q, got %q", "execute_query", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}
}
