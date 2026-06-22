package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultEventLogLimit = 50
	maxEventLogLimit     = 500
)

// EventLogTool returns the MCP tool definition for get_event_log.
func EventLogTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  "get_event_log",
		Title: "Журнал регистрации",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Прочитать журнал регистрации 1С — лог ошибок, действий пользователей и системных событий. " +
			"Фильтрация по дате, уровню важности (Ошибка/Предупреждение/Информация/Примечание) и пользователю.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"start_date": {
					"type": "string",
					"description": "Начало периода в формате ISO 8601 (например 2026-03-01T00:00:00)"
				},
				"end_date": {
					"type": "string",
					"description": "Конец периода в формате ISO 8601"
				},
				"level": {
					"type": "string",
					"description": "Уровень важности: Ошибка, Предупреждение, Информация, Примечание",
					"enum": ["Ошибка", "Предупреждение", "Информация", "Примечание"]
				},
				"user": {
					"type": "string",
					"description": "Имя пользователя 1С для фильтрации"
				},
				"limit": {
					"type": "integer",
					"description": "Максимальное количество записей (по умолчанию 50, максимум 500)"
				}
			}
		}`),
	}
}

// NewEventLogHandler returns a ToolHandler that reads the 1C event log.
func NewEventLogHandler(client *onec.Client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var body onec.EventLogRequest
		if err := json.Unmarshal(req.Params.Arguments, &body); err != nil {
			return nil, fmt.Errorf("parsing input: %w", err)
		}
		body.Limit = clampLimit(body.Limit, defaultEventLogLimit, maxEventLogLimit)

		var result onec.EventLogResult
		if err := client.Post(ctx, "/eventlog", body, &result); err != nil {
			return nil, fmt.Errorf("reading event log from 1C: %w", err)
		}

		return textResult(formatEventLog(&result)), nil
	}
}

func formatEventLog(r *onec.EventLogResult) string {
	var b strings.Builder
	b.WriteString("## Журнал регистрации\n\n")

	if len(r.Events) == 0 {
		b.WriteString("Записей не найдено.\n")
		return b.String()
	}

	for i, e := range r.Events {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		fmt.Fprintf(&b, "**%s** | %s | %s\n", e.Date, e.Level, e.Event)
		fmt.Fprintf(&b, "- Пользователь: %s\n", e.User)
		if e.Computer != "" {
			fmt.Fprintf(&b, "- Компьютер: %s\n", e.Computer)
		}
		if e.Metadata != "" {
			fmt.Fprintf(&b, "- Метаданные: %s\n", e.Metadata)
		}
		if e.Data != "" {
			fmt.Fprintf(&b, "- Данные: %s\n", e.Data)
		}
		if e.Comment != "" {
			fmt.Fprintf(&b, "- Комментарий: %s\n", e.Comment)
		}
		if e.Transaction != "" {
			fmt.Fprintf(&b, "- Транзакция: %s\n", e.Transaction)
		}
	}

	fmt.Fprintf(&b, "\nВсего: %d\n", r.Total)
	return b.String()
}
