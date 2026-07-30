package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FormStructureTool returns the MCP tool definition for get_form_structure.
func FormStructureTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  "get_form_structure",
		Title: "Структура формы объекта",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Получить структуру управляемой формы объекта 1С: элементы интерфейса, команды, кнопки и обработчики событий. " +
			"Используй когда нужно понять как выглядит форма документа, справочника или обработки. " +
			"ВАЖНО: полный состав формы читается из выгрузки, поэтому для элементов, команд и обработчиков запусти сервер с флагом --dump (выгрузка конфигурации в файлы), тогда они берутся из Form.xml. Без --dump возвращается только то, что отдал HTTP-сервис 1С, и на практике это имя и заголовок формы.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"object_type": {
					"type": "string",
					"description": "Тип объекта: Document, Catalog, DataProcessor, Report и т.д. Соответствие категориям из get_metadata_tree (мн. число рус. -> ед. число англ.): Справочники->Catalog, Документы->Document, Перечисления->Enum, Обработки->DataProcessor, Отчеты->Report, РегистрыСведений->InformationRegister, РегистрыНакопления->AccumulationRegister, РегистрыБухгалтерии->AccountingRegister, РегистрыРасчета->CalculationRegister, ПланыСчетов->ChartOfAccounts, ПланыВидовХарактеристик->ChartOfCharacteristicTypes, ПланыВидовРасчета->ChartOfCalculationTypes, ПланыОбмена->ExchangePlan, БизнесПроцессы->BusinessProcess, Задачи->Task."
				},
				"object_name": {
					"type": "string",
					"description": "Имя объекта метаданных"
				},
				"form_name": {
					"type": "string",
					"description": "Имя формы. Учитывается только при запуске сервера с флагом --dump: имя ищется среди форм объекта в выгрузке, и если не указано - берётся первая форма по алфавиту. Без --dump параметр не действует, форму выбирает сам HTTP-сервис 1С."
				}
			},
			"required": ["object_type", "object_name"]
		}`),
	}
}

// formInput extends objectInput with an optional form name.
type formInput struct {
	objectInput
	FormName string `json:"form_name"`
}

// NewFormStructureHandler returns a ToolHandler that fetches form structure.
//
// What the HTTP endpoint gives us: ФормаGET in the bundled extension
// (extension/src/HTTPServices/MCPService/Ext/Module.bsl) always fills name and
// title, and then tries to enumerate Форма.Элементы, Форма.Команды and the
// form's event properties inside Попытка/Исключение blocks that swallow the
// failure and leave the arrays empty. Its own comments mark those three as
// «могут быть недоступны в режиме Предприятия». Whether, and in which
// contexts, the platform actually populates them is NOT established anywhere in
// this repository, so treat an empty Elements/Commands/Handlers from HTTP as
// the case to design for rather than as a proven impossibility. The local
// DumpConfigToFiles output (Form.xml) is the source we can rely on, and it is
// used whenever --dump is configured.
//
// Behaviour:
//   - Name/Title come from HTTP if available; dump fills them in otherwise.
//   - Elements/Commands/Handlers come from dump when --dump is set; otherwise
//     the response carries only what HTTP returned (degraded but valid).
//   - form_name is only ever passed to formFromDump: the HTTP endpoint is
//     /form/{type}/{name} and has no slot for a form name. Without --dump the
//     argument cannot take effect, so the response body says so instead of
//     silently answering about a different form.
//   - If both HTTP and dump fail we return an error.
//   - dump-only failures (HTTP OK, dump broken) are logged at WARN and do not
//     fail the call. That line is NOT visible in the default stdio mode: the
//     default logger is set to slog.LevelError in cmd/mcp-1c/main.go, and the
//     pipe branch keeps LevelError too. It surfaces only with --debug (INFO to
//     server.log) or on a terminal launch (INFO to stderr).
func NewFormStructureHandler(client *onec.Client, dumpDir string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input formInput
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, fmt.Errorf("parsing input: %w", err)
		}
		if input.ObjectType == "" || input.ObjectName == "" {
			return nil, fmt.Errorf("object_type and object_name are required")
		}

		// Hit the 1C HTTP endpoint for the form's Name and Title (Synonym).
		var form onec.FormStructure
		endpoint := fmt.Sprintf("/form/%s/%s", input.ObjectType, input.ObjectName)
		httpErr := client.Get(ctx, endpoint, &form)

		// If --dump is wired up, parse the matching Form.xml for the full
		// structure (Elements/Commands/Handlers - HTTP cannot provide them).
		if dumpDir != "" {
			dumpForm, dumpErr := formFromDump(dumpDir, input.ObjectType, input.ObjectName, input.FormName)
			switch {
			case dumpErr == nil && dumpForm != nil:
				mergeDumpIntoForm(&form, dumpForm)
			case httpErr != nil:
				// Both sources failed - return a combined error so the user
				// can see why we have nothing to show.
				return nil, fmt.Errorf("fetching form structure from 1C: %w (dump fallback: %v)", httpErr, dumpErr)
			default:
				// HTTP gave us at least Name+Title but the dump enrichment
				// did not work. Log it so users notice missing details.
				slog.Warn("Form dump enrichment failed",
					"object_type", input.ObjectType,
					"object_name", input.ObjectName,
					"form_name", input.FormName,
					"error", dumpErr)
			}
		} else if httpErr != nil {
			return nil, fmt.Errorf("fetching form structure from 1C: %w", httpErr)
		}

		text := formatFormStructure(&form)
		if dumpDir == "" && input.FormName != "" {
			text += formNameNeedsDumpNote
		}
		return textResult(text), nil
	}
}

// formNameNeedsDumpNote is appended to the response body when the caller asked
// for a specific form but the server runs without --dump. The parameter is
// dropped on that path (see NewFormStructureHandler), and the WARN log is
// invisible in the default stdio mode, so the body is the only place the user
// can learn that the answer is about a form they did not ask for.
const formNameNeedsDumpNote = "> Параметр `form_name` не применялся: выбор формы по имени работает " +
	"только при запуске сервера с флагом `--dump`. Без него имя формы в 1С не передаётся, " +
	"форму выбирает сам HTTP-сервис, и выше возвращена именно она. С `--dump` форма ищется " +
	"по имени в выгрузке конфигурации, откуда читаются также состав элементов, команды и обработчики.\n"

// formFromDump loads form structure from a DumpConfigToFiles XML file.
func formFromDump(dumpDir, objectType, objectName, formName string) (*onec.FormStructure, error) {
	formFiles, err := dump.FindFormFiles(dumpDir, objectType, objectName)
	if err != nil {
		return nil, fmt.Errorf("finding form files: %w", err)
	}
	if len(formFiles) == 0 {
		return nil, fmt.Errorf("no forms found in dump for %s.%s", objectType, objectName)
	}

	// Select the requested form or pick the first one.
	var selectedPath string
	var selectedName string
	if formName != "" {
		path, ok := formFiles[formName]
		if !ok {
			return nil, fmt.Errorf("form %q not found in dump (available: %s)", formName, joinMapKeys(formFiles))
		}
		selectedPath = path
		selectedName = formName
	} else {
		// Pick the first form alphabetically for deterministic results.
		keys := make([]string, 0, len(formFiles))
		for name := range formFiles {
			keys = append(keys, name)
		}
		slices.Sort(keys)
		selectedName = keys[0]
		selectedPath = formFiles[selectedName]
	}

	parsed, err := dump.ParseFormXML(selectedPath)
	if err != nil {
		return nil, fmt.Errorf("parsing form XML %q: %w", selectedPath, err)
	}

	return convertDumpForm(selectedName, parsed), nil
}

// convertDumpForm converts dump.FormInfo to onec.FormStructure.
func convertDumpForm(formName string, info *dump.FormInfo) *onec.FormStructure {
	form := &onec.FormStructure{
		Name:     formName,
		Title:    info.Title,
		Elements: make([]onec.FormElement, 0, len(info.Elements)),
		Commands: make([]onec.FormCommand, 0, len(info.Commands)),
		Handlers: make([]onec.FormHandler, 0, len(info.Handlers)),
	}

	for _, e := range info.Elements {
		var events []onec.FormHandler
		if len(e.Events) > 0 {
			events = make([]onec.FormHandler, 0, len(e.Events))
			for _, ev := range e.Events {
				events = append(events, onec.FormHandler{
					Event:   ev.Event,
					Handler: ev.Handler,
				})
			}
		}
		form.Elements = append(form.Elements, onec.FormElement{
			Name:     e.Name,
			Type:     dump.DisplayType(e.Type),
			Title:    e.Title,
			DataPath: e.DataPath,
			Events:   events,
		})
	}

	for _, c := range info.Commands {
		form.Commands = append(form.Commands, onec.FormCommand{
			Name:   c.Name,
			Action: c.Action,
		})
	}

	for _, h := range info.Handlers {
		form.Handlers = append(form.Handlers, onec.FormHandler{
			Event:   h.Event,
			Handler: h.Handler,
		})
	}

	return form
}

// mergeDumpIntoForm merges dump data into the HTTP response.
//
// The HTTP endpoint in Enterprise mode never returns Elements/Commands/
// Handlers (BSL has no server-side API for those collections), so the dump
// is the authoritative source for them. Name and Title are kept from HTTP
// when present (HTTP uses the configured Synonym), with dump as fallback.
func mergeDumpIntoForm(form *onec.FormStructure, dumpForm *onec.FormStructure) {
	if form.Name == "" {
		form.Name = dumpForm.Name
	}
	if form.Title == "" {
		form.Title = dumpForm.Title
	}
	// Elements/Commands/Handlers: dump wins because HTTP never populates them.
	if len(dumpForm.Elements) > 0 {
		form.Elements = dumpForm.Elements
	}
	if len(dumpForm.Commands) > 0 {
		form.Commands = dumpForm.Commands
	}
	if len(dumpForm.Handlers) > 0 {
		form.Handlers = dumpForm.Handlers
	}
}

func formatFormStructure(f *onec.FormStructure) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Форма: %s\n", f.Name)
	if f.Title != "" {
		fmt.Fprintf(&b, "**Заголовок:** %s\n", f.Title)
	}
	b.WriteByte('\n')

	if len(f.Elements) > 0 {
		b.WriteString("## Элементы формы\n\n")
		b.WriteString("| Имя | Тип | Заголовок | Путь к данным |\n")
		b.WriteString("|-----|-----|-----------|---------------|\n")
		for _, e := range f.Elements {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
				escapePipe(e.Name), escapePipe(e.Type), escapePipe(e.Title), escapePipe(e.DataPath))
		}
		b.WriteByte('\n')

		// Element-level events live one level deeper than form-level handlers
		// (### vs ##) and are only emitted when at least one element exposes
		// them - most form elements have none.
		if hasElementEvents(f.Elements) {
			b.WriteString("### События элементов\n\n")
			for _, e := range f.Elements {
				for _, ev := range e.Events {
					fmt.Fprintf(&b, "- **%s** (`%s`) → %s()\n",
						e.Name, ev.Event, ev.Handler)
				}
			}
			b.WriteByte('\n')
		}
	}

	if len(f.Commands) > 0 {
		b.WriteString("## Команды формы\n\n")
		for _, c := range f.Commands {
			fmt.Fprintf(&b, "- **%s** → %s\n", c.Name, c.Action)
		}
		b.WriteByte('\n')
	}

	if len(f.Handlers) > 0 {
		b.WriteString("## Обработчики событий\n\n")
		for _, h := range f.Handlers {
			fmt.Fprintf(&b, "- **%s** → %s()\n", h.Event, h.Handler)
		}
		b.WriteByte('\n')
	}

	return b.String()
}

// escapePipe escapes pipe characters so they do not break markdown tables.
func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}

// hasElementEvents reports whether any element in the slice carries at least
// one event handler. Used to decide whether to emit the "События элементов"
// section.
func hasElementEvents(elements []onec.FormElement) bool {
	for _, e := range elements {
		if len(e.Events) > 0 {
			return true
		}
	}
	return false
}

// joinMapKeys returns a comma-separated list of map keys.
func joinMapKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return strings.Join(keys, ", ")
}
