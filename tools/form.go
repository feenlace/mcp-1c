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
			"ВАЖНО: HTTP-endpoint 1С в серверном контексте не отдаёт состав элементов и обработчики формы - для полной структуры запусти сервер с флагом --dump (выгрузка конфигурации в файлы), тогда состав элементов, команды и обработчики берутся из Form.xml. Без --dump возвращаются только имя и заголовок формы.",
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
					"description": "Имя формы (если не указано - возвращается первая форма по алфавиту)"
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
// The 1C HTTP endpoint in the Enterprise/server context cannot enumerate the
// runtime UI tree (no access to ФормаКлиентскогоПриложения), so it returns
// only the form name and title - Elements/Commands/Handlers from it are
// always empty. The full structure is parsed from the local DumpConfigToFiles
// output (Form.xml) when --dump is configured.
//
// Behaviour:
//   - Name/Title come from HTTP if available; dump fills them in otherwise.
//   - Elements/Commands/Handlers come from dump when --dump is set; otherwise
//     the response contains only Name+Title (degraded but valid).
//   - If both HTTP and dump fail we return an error.
//   - dump-only failures (HTTP OK, dump broken) are logged at WARN so users
//     can diagnose why enrichment did not happen, but do not fail the call.
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

		return textResult(formatFormStructure(&form)), nil
	}
}

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
