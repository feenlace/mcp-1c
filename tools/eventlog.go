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
// THE FAR SIDE NOW CATCHES IT TOO, AND THAT IS RECENT. ЖурналРегистрацииPOST in
// extension/src/HTTPServices/MCPService/Ext/Module.bsl maps the level through a
// Соответствие of these same four names; a name that is not in it answers 400
// «unknown level: …» and names the four that are.
//
// It did not always. The version this paragraph was first written against
// applied the filter only «Если Уровень <> Неопределено», so an unmapped level
// was dropped without a word and the whole log came back as though it had been
// filtered, and the paragraph said the far side would not catch it either. The
// extension commit that changed this left the sentence behind, which is how a
// comment about another file goes wrong: nothing recompiles prose. The claim is
// now pinned by TestExtensionRefusesAnUnmappedLevel, which walks the shipped
// module and fails if the refusal goes away again.
//
// The check here is NOT thereby redundant. The two refusals are different
// answers to the caller: this one costs no round trip, names the tool's own
// schema, and still stands if the installed extension is older than the one this
// binary bundles, which is a supported pairing. The answer to a question nobody
// could honour must not look like the answer to the question that was asked.
var eventLogLevels = []string{"Ошибка", "Предупреждение", "Информация", "Примечание"}

// eventPresentationLabel is the line the readable event name is printed on.
//
// It is NOT «Событие»: the header line of every record already carries the
// technical identifier, and giving the two the same label would leave a reader
// unable to tell which of them the filter takes.
const eventPresentationLabel = "Представление события"

// EventLogTool returns the MCP tool definition for get_event_log.
func EventLogTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  "get_event_log",
		Title: "Журнал регистрации",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Прочитать журнал регистрации 1С — лог ошибок, действий пользователей и системных событий. " +
			"Фильтрация по дате, уровню важности (Ошибка/Предупреждение/Информация/Примечание), " +
			"пользователю и событию.",
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
				"event": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Имена событий. Имя это технический идентификатор, а не то, что написано в колонке Событие окна журнала регистрации: окно показывает представление вроде «Сеанс. Начало», а отбор принимает «_$Session$_.Start». Системные имена такого вида: _$Session$_.Start, _$Session$_.Finish, _$Session$_.Authentication, _$Data$_.Update, _$Access$_.Access. Список не закрыт: прикладной код пишет свои события через ЗаписьЖурналаРегистрации и называет их сам, в имени бывают точки, и такие имена тут тоже допустимы. Представление каждой записи приходит в ответе рядом с именем, так что одно по другому и находится. Имя, которого в базе нет, отклоняется с этим именем в тексте отказа, а не игнорируется."
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
			return nil, InvalidParams(argumentDecodeError(err))
		}
		// Operational, not InvalidParams: this is a VALUE the caller chose, and
		// a caller can only correct a value from text it can read. Same class
		// and same shape as the mode check in search.go NewSearchCodeHandler.
		if body.Level != "" && !slices.Contains(eventLogLevels, body.Level) {
			return nil, fmt.Errorf("неизвестный уровень важности %q (допустимо: %s)",
				body.Level, strings.Join(eventLogLevels, ", "))
		}
		// AN EXPLICITLY EMPTY LIST IS A DIFFERENT QUESTION FROM NO LIST AT ALL,
		// and the encoding cannot carry the difference. omitempty fires on
		// len == 0 rather than on nil, so an empty array leaves this process as a
		// body with no event member, and the answer comes back byte for byte the
		// answer to a call that asked for no filter. That is the very thing this
		// endpoint refuses elsewhere rather than allows: a filter that cannot be
		// applied is declined, because a dropped one is indistinguishable from an
		// absent one and the caller has no way to tell which question was
		// answered.
		//
		// The DECODE keeps the distinction that the encode loses: {} leaves Event
		// nil and {"event": []} leaves it non-nil and empty. This is therefore the
		// last place the difference can be read at all. ЖурналРегистрацииPOST
		// refuses the same state, and with the member erased it
		// was never reachable from here.
		//
		// Operational, not InvalidParams, for the same reason as the checks either
		// side of it: this is a VALUE the caller chose, and a caller can only
		// correct a value from text it can read.
		if body.Event != nil && len(body.Event) == 0 {
			return nil, fmt.Errorf("список имён событий в `event` пуст; уберите `event`, " +
				"чтобы прочитать журнал без отбора по событию")
		}
		// An empty name is the one thing about `event` this side can settle. The
		// NAMES cannot be checked here: the list of them lives in the base, and
		// ЖурналРегистрацииPOST reads it and refuses a name that is not in it. An
		// empty string is in no base's list, so refusing it here costs the caller a
		// round trip less and says the same thing.
		//
		// Operational, not InvalidParams, for the same reason as the level check
		// above: this is a VALUE the caller chose, and a caller can only correct a
		// value from text it can read.
		for i, name := range body.Event {
			if strings.TrimSpace(name) == "" {
				return nil, fmt.Errorf("имя события в позиции %d пустое; уберите его или "+
					"поставьте идентификатор, например _$Session$_.Start", i+1)
			}
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
		// The identifier is on the header line above and the phrase the 1С window
		// prints is here, under its own label. They are two names for one event and
		// only the identifier is accepted by the filter, so the reader has to be
		// able to tell which is which.
		if e.EventPresentation != "" {
			fmt.Fprintf(&b, "- %s: %s\n", eventPresentationLabel, e.EventPresentation)
		}
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
