package tools

import (
	"context"
	"encoding/json"
	"errors"
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
					"description": "Имя формы. Учитывается только при запуске сервера с флагом --dump: имя ищется среди форм объекта в выгрузке, и если не указано - берётся первая форма по алфавиту. Если формы с таким именем у объекта нет, возвращается ошибка со списком доступных форм. Без --dump параметр не действует, форму выбирает сам HTTP-сервис 1С."
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
//   - Name/Title come from HTTP when the dump supplied no structure; when it
//     did, both come from the dump, because the two sources do not necessarily
//     describe the same form of the object (see mergeDumpIntoForm).
//   - Elements/Commands/Handlers come from dump when --dump is set; otherwise
//     the response carries only what HTTP returned (degraded but valid).
//   - form_name is only ever passed to formFromDump: the HTTP endpoint is
//     /form/{type}/{name} and has no slot for a form name. Without --dump the
//     argument cannot take effect, so the response body says so instead of
//     silently answering about a different form.
//   - With --dump, a form_name the object does not have is a hard error that
//     lists the real form names. Every other dump failure degrades, but this
//     one cannot: the alternative is a confident answer about a form nobody
//     asked about.
//   - If both HTTP and dump fail we return an error.
//   - The remaining dump-only failures (HTTP OK, dump unreadable) are logged at
//     WARN, do not fail the call, and add a note to the response body. The WARN
//     alone is NOT visible in the default stdio mode: the default logger is set
//     to slog.LevelError in cmd/mcp-1c/main.go, and the pipe branch keeps
//     LevelError too. It surfaces only with --debug (INFO to server.log) or on
//     a terminal launch (INFO to stderr).
//   - Two Form.xml outcomes are NOT among those failures: a file the parser read
//     only up to a syntax error, and a file it read whole that turned out to hold
//     no form at all. Neither produces an error or a WARN, because the parse
//     contract tolerates a broken or useless dump file on purpose rather than
//     downgrade dumps that currently work. The body carries formPartialParseNote
//     or formNoFormRootNote instead, and that note is the only signal the caller
//     gets. The two describe different causes with different remedies and are
//     mutually exclusive at the source (see dump.FormInfo.NoFormRoot), so a
//     response never carries both.
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

		// If --dump is wired up, parse the matching Form.xml: that file is
		// where this code reads Elements/Commands/Handlers from.
		//
		// dumpStructureMissing records the degraded case for the body note
		// below, because the WARN it also logs is invisible at the default
		// stdio log level.
		//
		// dumpParsedPartially records the OTHER degraded case, the one that
		// never reaches dumpErr at all: the Form.xml was opened and read, but
		// the XML decoder stopped on a syntax error part way through and the
		// parser still reported success (dump.FormInfo.ParseIncomplete). It is
		// mutually exclusive with dumpStructureMissing by construction, since
		// that one only ever gets set on the dumpErr != nil branches.
		dumpStructureMissing := false
		var dumpRead dumpFormRead
		if dumpDir != "" {
			dumpForm, readOutcome, dumpErr := formFromDump(dumpDir, input.ObjectType, input.ObjectName, input.FormName)
			switch {
			case dumpErr == nil && dumpForm != nil:
				mergeDumpIntoForm(&form, dumpForm)
				dumpRead = readOutcome
			case errors.Is(dumpErr, errFormNotInDump):
				// The caller named a form this object does not have. Returning
				// a different form's structure under that request would be a
				// confident wrong answer, so this single dump failure is fatal
				// even though HTTP succeeded. Classified with errors.Is, never
				// by matching the message. Checked before the httpErr case: the
				// dump is a local file tree, so its form list stays valid and
				// actionable even when the 1C service is unreachable.
				return nil, dumpErr
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
				dumpStructureMissing = true
			}
		} else if httpErr != nil {
			return nil, fmt.Errorf("fetching form structure from 1C: %w", httpErr)
		}

		text := formatFormStructure(&form)
		if dumpDir == "" && input.FormName != "" {
			text += formNameNeedsDumpNote
		}
		if dumpStructureMissing {
			text += formDumpUnreadableNote
			if input.FormName != "" {
				// The name lookup lives inside formFromDump, so a dump that
				// yielded no form never got to honour form_name either.
				text += formNameDumpUnreadableNote
			}
		}
		// Two independent ifs rather than an if/else. The parser guarantees the
		// two are mutually exclusive, so at most one fires; writing it as a chain
		// would additionally SWALLOW one of them if that guarantee ever broke,
		// and a note going missing is harder to notice than one too many.
		if dumpRead.partial {
			text += formPartialParseNote
		}
		if dumpRead.noFormRoot {
			text += formNoFormRootNote
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

// formDumpUnreadableNote is appended when --dump is configured and the 1C
// service answered, but the dump could not supply the form structure: the
// object has no forms in the dump, its forms directory is unreadable or is not
// a directory, or the form file itself could not be read. The response stays a
// normal result because the HTTP part of it is valid, but without this note the
// caller sees a name and a title and takes that for the whole form. The WARN
// logged alongside cannot do the job: the default stdio logger runs at
// slog.LevelError (cmd/mcp-1c/main.go).
//
// The wording deliberately does not claim that the elements, commands and
// handlers sections are missing. Whether the HTTP endpoint fills them is not
// established anywhere in this repository (see NewFormStructureHandler), and a
// note asserting they are absent is falsified the moment the body above it
// lists them.
const formDumpUnreadableNote = "> Состав формы не прочитан из выгрузки: выше только то, что вернул " +
	"HTTP-сервис 1С, поэтому элементы, команды и обработчики могут быть неполными или отсутствовать. " +
	"Возможные причины: форм этого объекта нет в выгрузке, каталог форм недоступен или файл Form.xml " +
	"не удалось прочитать. Проверьте путь, указанный в `--dump`, и полноту выгрузки конфигурации.\n"

// formNameDumpUnreadableNote extends the note above for the caller who also
// passed form_name. The name lookup happens inside formFromDump, so a dump that
// yielded no form never selected one by name, and the form shown above is
// whichever one the HTTP service picked.
//
// The wording states only that the dump did not yield a form under that name,
// and leaves the reason to the note above, which already lists all three. That
// is deliberate: exactly one of the three paths reaching this note is a failed
// read of a form file, and the other two are not. The object having no forms in
// the dump at all is the common case, and describing it as "the form could not
// be read" sends the user hunting for a corrupt file that was never there.
const formNameDumpUnreadableNote = "> Параметр `form_name` при этом не применялся: форма с таким именем " +
	"ищется в выгрузке, а выгрузка её не дала по причинам выше. Форму выбрал сам HTTP-сервис 1С, " +
	"и выше возвращена именно она.\n"

// formPartialParseNote is appended when the dump DID give us a form but the
// parser recorded that it stopped on a syntax error before the end of the file
// (dump.FormInfo.ParseIncomplete). That path returns no error at all: the parse
// contract deliberately tolerates a broken Form.xml so dumps that read "well
// enough" keep working, which also means nothing fails, nothing is logged, and
// the answer built from the surviving fragment looks exactly as confident as one
// built from a whole file. This note is the only place the caller can learn
// otherwise.
//
// The wording never says the form has no elements, commands or handlers. A
// partial parse usually keeps whatever it read before the break, so the body
// above this note routinely DOES list some, and a note denying them would be
// falsified by the table printed directly above it. "Показано только то, что
// удалось прочитать" is true whether that is nothing or almost everything.
const formPartialParseNote = "> Файл формы в выгрузке прочитан не полностью: разбор XML остановился " +
	"на ошибке в файле, поэтому показано только то, что удалось прочитать до этого места, а всё, " +
	"что записано в файле дальше, в ответ не попало. Состав формы выше может быть неполным. " +
	"Обычные причины: файл Form.xml обрезан или повреждён. Проверьте полноту выгрузки конфигурации, " +
	"указанной в `--dump`.\n"

// formNoFormRootNote is appended for the OTHER silent outcome: the Form.xml was
// opened and read to its very end, and there was no form in it
// (dump.FormInfo.NoFormRoot). An empty file, a file of plain text, a stray
// comment, or a document rooted at some other element all land here. Like the
// partial parse, this returns no error and logs nothing, so before this note the
// answer looked exactly like one built from a healthy dump.
//
// It is a SEPARATE note from formPartialParseNote on purpose. The two causes are
// different and so are their remedies: a truncated file means the dump was cut
// short, an empty or foreign file means the dump never wrote this form. Telling
// a user their file is truncated when it is merely empty sends them looking for
// the wrong damage, so neither note may borrow the other's cause. The wording
// here therefore says the file WAS read whole, which is the precise opposite of
// the partial note and is safe to state because the parser sets this flag only
// on a clean end of document.
//
// The wording is scoped to the dump and never denies what the body shows. The
// elements, commands and handlers printed above may have come from the 1C HTTP
// service, and a note claiming there are none would be falsified by the table
// right above it. What it does assert about the dump is guaranteed by the
// parser: nothing is recorded outside <Form>, so a file that never entered one
// contributed no element, command or handler. The form NAME above can still come
// from the dump directory when HTTP returned none (see mergeDumpIntoForm), which
// is why the claim is limited to the composition sections and not to the whole
// answer.
const formNoFormRootNote = "> Файл формы в выгрузке прочитан целиком, но описания формы в нём нет: " +
	"в файле ни разу не встретился элемент `Form`, поэтому из выгрузки не взяты ни элементы, " +
	"ни команды, ни обработчики. Всё, что показано выше в разделах состава формы, вернул " +
	"HTTP-сервис 1С. Обычные причины: файл Form.xml пустой либо содержит не ту разметку. " +
	"Проверьте полноту выгрузки конфигурации, указанной в `--dump`.\n"

// errFormNotInDump marks the one formFromDump failure that belongs to the
// caller rather than to the dump: they named a form the object does not have.
// It is matched with errors.Is so that the remaining failures (no forms at all,
// unreadable forms directory, containment refusal, unreadable Form.xml) keep
// their non-fatal handling without the classification depending on message text.
var errFormNotInDump = errors.New("requested form is not present in the dump")

// formNotInDumpError reports a form_name that does not match any of the
// object's forms in the dump, and carries the real names so the caller can fix
// the request in one step. It unwraps to errFormNotInDump, which keeps the
// classification separate from the message the user reads.
type formNotInDumpError struct {
	requested string
	available string
}

func (e *formNotInDumpError) Error() string {
	return fmt.Sprintf("form %q not found in dump (available: %s)", e.requested, e.available)
}

func (e *formNotInDumpError) Unwrap() error { return errFormNotInDump }

// dumpFormRead reports how the dump's Form.xml was read, for the response notes.
// Both fields describe a parse that SUCCEEDED, which is the whole point: these
// are the outcomes that never reach an error and would otherwise be invisible.
//
// They are mutually exclusive at the source (dump.FormInfo sets NoFormRoot only
// on a clean end of document), so at most one is ever true. Grouped in a struct
// rather than returned as two bare bools so a call site cannot silently swap
// them, which for two adjacent same-typed values is a matter of time.
type dumpFormRead struct {
	// partial: the decoder stopped on a syntax error before the end of the file.
	partial bool
	// noFormRoot: the file was read whole and contained no <Form> at all.
	noFormRoot bool
}

// formFromDump loads form structure from a DumpConfigToFiles XML file.
//
// The middle return value reports how the Form.xml was read: parsed only up to a
// syntax error, or read to the end but holding no form. It is a second return
// value and not a field on onec.FormStructure because that type is the shape of
// the 1C HTTP service's JSON reply, shared with the HTTP path, and a fact about
// how a local file was read does not belong on the wire type. It is not folded
// into the error either: an error here would be classified as a dump failure by
// the caller's switch and would turn a dump that parses well enough today into a
// degraded answer, which is exactly the tolerance this change must preserve.
// The signature is unexported with a single call site, so widening it ripples
// nowhere.
func formFromDump(dumpDir, objectType, objectName, formName string) (*onec.FormStructure, dumpFormRead, error) {
	formFiles, err := dump.FindFormFiles(dumpDir, objectType, objectName)
	if err != nil {
		return nil, dumpFormRead{}, fmt.Errorf("finding form files: %w", err)
	}
	if len(formFiles) == 0 {
		return nil, dumpFormRead{}, fmt.Errorf("no forms found in dump for %s.%s", objectType, objectName)
	}

	// Select the requested form or pick the first one.
	var selectedPath string
	var selectedName string
	if formName != "" {
		path, ok := formFiles[formName]
		if !ok {
			return nil, dumpFormRead{}, &formNotInDumpError{requested: formName, available: joinMapKeys(formFiles)}
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
		return nil, dumpFormRead{}, fmt.Errorf("parsing form XML %q: %w", selectedPath, err)
	}

	return convertDumpForm(selectedName, parsed), dumpFormRead{
		partial:    parsed.ParseIncomplete,
		noFormRoot: parsed.NoFormRoot,
	}, nil
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
// The two sources do not necessarily describe the same form. The HTTP endpoint
// answers about the form its BSL handler resolved (ПолучитьОсновнуюФорму in
// extension/src/HTTPServices/MCPService/Ext/Module.bsl), while formFromDump
// answers about the form named in form_name or, when none was given, the first
// one alphabetically. So whenever the dump supplied the structure, the identity
// is taken from the dump too: a header naming one form above another form's
// elements is not a partial answer, it is a wrong one.
//
// Everything else travels with the Name. The Title describes the HTTP-named
// form, and so do its Commands and Handlers, so once the body belongs to a
// different form the whole HTTP payload is replaced rather than reprinted over
// foreign contents. Replacing means replacing outright, empty collections
// included: a form that declares no commands has no commands, and backfilling
// that section from the other form is how a response ends up headed by one form
// while listing another's. If the dump form carries no Title of its own the
// response then shows no title line at all, which is the intended trade: a
// missing title leaves the answer incomplete, a borrowed one makes it false.
//
// When the two names agree, both sources describe the same form, so HTTP data
// is not foreign and the older per-collection merge applies: a non-empty dump
// collection wins, because Form.xml holds the form's declared contents in full
// while the HTTP handler builds each of the three inside a Попытка block that
// leaves the array empty on failure.
//
// When the dump supplied no structure at all, nothing in the body belongs to
// the dump form, so Name and Title stay as HTTP returned them and the dump only
// fills in what HTTP left empty.
func mergeDumpIntoForm(form *onec.FormStructure, dumpForm *onec.FormStructure) {
	dumpSuppliedStructure := len(dumpForm.Elements) > 0 ||
		len(dumpForm.Commands) > 0 ||
		len(dumpForm.Handlers) > 0

	if dumpSuppliedStructure && dumpForm.Name != "" && dumpForm.Name != form.Name {
		*form = *dumpForm
		return
	}

	if form.Name == "" {
		form.Name = dumpForm.Name
	}
	if form.Title == "" {
		form.Title = dumpForm.Title
	}
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
