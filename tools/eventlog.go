package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultEventLogLimit = 50
	maxEventLogLimit     = 500
)

// eventLogLevels is the set the schema below declares under "enum", and the set
// the handler enforces. ONE list, read by both, because two lists is how a
// declared constraint quietly stops being a constraint.
//
// IT HAS TO BE ENFORCED HERE. mcp/server.go Server.AddTool takes the schema as
// an opaque json.RawMessage and wires no validation of it whatever: it checks
// the NAME, that the schema unmarshals, and that its type is "object", and then
// stores the tool. Only the generic mcp/server.go AddTool[In, Out] resolves the
// schema and validates against it, and that registration form has exactly ONE
// call site in this repository, tools/bsl_help.go RegisterBSLHelp. Measured
// with `grep -rnE 'mcp\.AddTool\(' tools server cmd prompts | grep -v _test.go`,
// which returns that one line; the mentions in comments do not match it because
// they are not call syntax. So for the other ten tools the enum in the schema is
// a hint to the model and nothing more, and a value outside it arrives at the
// handler unremarked.
//
// AND THE FAR SIDE WILL NOT CATCH IT EITHER. ЖурналРегистрацииPOST in
// extension/src/HTTPServices/MCPService/Ext/Module.bsl maps the level through a
// Соответствие of these same four names and applies the filter only «Если
// Уровень <> Неопределено», so an unmapped level is dropped without a word and
// the whole log comes back as though it had been filtered. The answer to a
// question nobody could honour must not look like the answer to the question
// that was asked.
var eventLogLevels = []string{"Ошибка", "Предупреждение", "Информация", "Примечание"}

// EventLogTool returns the MCP tool definition for get_event_log.
func EventLogTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  "get_event_log",
		Title: "Журнал регистрации",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Прочитать журнал регистрации 1С — лог ошибок, действий пользователей и системных событий. " +
			"Фильтрация по дате, уровню важности (Ошибка/Предупреждение/Информация/Примечание) и пользователю.",
		// The "enum" below and eventLogLevels are pinned to each other by
		// TestDeclaredEnumsAreEnforced, which reads this schema rather than a
		// copy of it.
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
	return WithToolErrors(headingEventLog, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var body onec.EventLogRequest
		if err := json.Unmarshal(req.Params.Arguments, &body); err != nil {
			return nil, InvalidParams(fmt.Errorf("parsing input: %w", err))
		}
		// Operational, not InvalidParams: this is a VALUE the caller chose, and
		// a caller can only correct a value from text it can read. Same class
		// and same shape as the mode check in search.go NewSearchCodeHandler.
		if body.Level != "" && !slices.Contains(eventLogLevels, body.Level) {
			return nil, fmt.Errorf("неизвестный уровень важности %q (допустимо: %s)",
				body.Level, strings.Join(eventLogLevels, ", "))
		}
		body.Limit = clampLimit(body.Limit, defaultEventLogLimit, maxEventLogLimit)

		var result onec.EventLogResult
		if err := client.Post(ctx, "/eventlog", body, &result); err != nil {
			return nil, fmt.Errorf("reading event log from 1C: %w", err)
		}

		return textResult(formatEventLog(&result)), nil
	})
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
