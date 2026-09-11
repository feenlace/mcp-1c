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

func TestEventLogTool(t *testing.T) {
	tool := EventLogTool()
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name != "get_event_log" {
		t.Errorf("expected tool name %q, got %q", "get_event_log", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestFormatEventLog(t *testing.T) {
	result := &onec.EventLogResult{
		Events: []onec.EventLogEntry{
			{
				Date:     "2026-03-07T10:00:00",
				Level:    "Ошибка",
				Event:    "_$Data$_.Update",
				User:     "Администратор",
				Metadata: "Документ.РеализацияТоваровУслуг",
				Comment:  "Ошибка записи документа",
			},
			{
				Date:  "2026-03-07T09:30:00",
				Level: "Информация",
				Event: "_$Session$_.Start",
				User:  "Бухгалтер",
			},
		},
		Total: 2,
	}

	text := formatEventLog(result)

	for _, want := range []string{
		"Журнал регистрации",
		"2026-03-07T10:00:00",
		"Ошибка",
		"Администратор",
		"Документ.РеализацияТоваровУслуг",
		"Ошибка записи документа",
		"Информация",
		"Бухгалтер",
		"Всего: 2",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in output, got:\n%s", want, text)
		}
	}
}

func TestFormatEventLog_Empty(t *testing.T) {
	result := &onec.EventLogResult{Events: nil, Total: 0}
	text := formatEventLog(result)
	if !strings.Contains(text, "Записей не найдено") {
		t.Errorf("expected empty message, got:\n%s", text)
	}
}

func TestFormatEventLog_OptionalFields(t *testing.T) {
	result := &onec.EventLogResult{
		Events: []onec.EventLogEntry{
			{
				Date:        "2026-03-07T10:00:00",
				Level:       "Ошибка",
				Event:       "_$Data$_.Update",
				User:        "Администратор",
				Computer:    "SERVER01",
				Metadata:    "Документ.Тест",
				Data:        "Тестовые данные",
				Comment:     "Тестовый комментарий",
				Transaction: "abc-123",
			},
		},
		Total: 1,
	}

	text := formatEventLog(result)

	for _, want := range []string{
		"Компьютер: SERVER01",
		"Метаданные: Документ.Тест",
		"Данные: Тестовые данные",
		"Комментарий: Тестовый комментарий",
		"Транзакция: abc-123",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in output, got:\n%s", want, text)
		}
	}
}

func TestNewEventLogHandler(t *testing.T) {
	const mockResponse = `{
		"events": [
			{
				"date": "2026-03-07T10:00:00",
				"level": "Ошибка",
				"event": "_$Data$_.Update",
				"user": "Администратор",
				"metadata": "Документ.РеализацияТоваровУслуг"
			}
		],
		"total": 1
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/eventlog" {
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

		var reqBody onec.EventLogRequest
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Errorf("failed to unmarshal request body: %v", err)
			http.Error(w, "failed to unmarshal body", http.StatusBadRequest)
			return
		}
		if reqBody.Limit <= 0 {
			t.Error("expected positive limit in request body")
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewEventLogHandler(client)

	args, _ := json.Marshal(map[string]any{
		"level": "Ошибка",
		"limit": 10,
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_event_log",
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

	for _, want := range []string{
		"Журнал регистрации",
		"Ошибка",
		"Администратор",
		"РеализацияТоваровУслуг",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}
