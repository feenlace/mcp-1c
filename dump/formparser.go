package dump

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FormInfo holds parsed form structure from a dump XML file.
type FormInfo struct {
	Name     string
	Title    string
	Elements []FormElementInfo
	Commands []FormCommandInfo
	Handlers []FormHandlerInfo

	// ParseIncomplete reports that the XML decoder stopped on a syntax error
	// before the end of the document, so the collections above hold only what
	// was read up to that point and the rest of the file was never seen.
	//
	// It is NOT an error flag and does not change the parse contract: a form
	// that parses "well enough" today keeps returning exactly the same data,
	// with this field set alongside it. The field exists because that
	// tolerance is otherwise invisible - parseFormXMLData reports success
	// either way, so without it no caller can tell a fully read file from one
	// the decoder abandoned halfway, and the answer built from the remains
	// looks exactly as confident as a complete one.
	//
	// SCOPE, deliberately narrow: this records a decoder SYNTAX error, nothing
	// else. A file that the decoder consumes to a normal end is never flagged
	// here, even when it is useless as a form: an empty file, a file of plain
	// text and a well formed document whose root is not <Form> all finish on
	// io.EOF and all yield an empty FormInfo. Those are reported by NoFormRoot
	// below, so do not read this field as "the file was a valid form".
	ParseIncomplete bool

	// NoFormRoot reports that the decoder consumed the whole document to its
	// normal end without the parser ever entering a <Form> element, so the file
	// was read in full and simply does not describe a form. Everything this
	// parser records (Title, Elements, Commands, Handlers) is only ever read
	// inside <Form>, so when this is set all four are guaranteed empty, and a
	// caller may say so without checking them.
	//
	// It closes the class ParseIncomplete cannot see: an empty Form.xml, a file
	// of plain text, whitespace or an XML comment alone, and a well formed
	// document whose root element is something other than <Form>. Each of those
	// finishes on a clean io.EOF, and before this field they were as silent as
	// the truncation case used to be.
	//
	// MUTUALLY EXCLUSIVE with ParseIncomplete by construction: this is set only
	// on the io.EOF branch. After a syntax error the read was ABANDONED, so a
	// <Form> further into the file would never have been reached and calling the
	// file formless would be a guess, not a finding. That exclusivity is what
	// lets each of the two describe its own cause without contradicting the
	// other, and it is pinned by a test.
	//
	// SCOPE: it records that no <Form> element was entered ANYWHERE, not that
	// the document had no root. A <Form> nested inside some other element is
	// still entered by the loop below (measured), and such a file is therefore
	// not flagged.
	NoFormRoot bool
}

// FormElementInfo represents a parsed form element.
type FormElementInfo struct {
	Name     string
	Type     string // XML element tag name (InputField, Table, etc.)
	Title    string
	DataPath string
	// Events lists this element's own direct <Events> handlers (e.g. an
	// InputField's OnChange, a Table's OnActivateRow). Handlers belonging
	// to nested ChildItems are attached to their own elements, not propagated
	// up - each element keeps only its direct events.
	Events []FormHandlerInfo
}

// FormCommandInfo represents a parsed form command.
type FormCommandInfo struct {
	Name   string
	Action string
}

// FormHandlerInfo represents a parsed form event handler.
type FormHandlerInfo struct {
	Event   string
	Handler string
}

// objectTypeToDumpDir is defined in metadata_types.go and maps 1C object type
// names (as used in the tool input) to dump directory names.

// errFormsDirUnreadable is the path-free RU refusal returned when an object's
// Forms directory cannot be read through the dump root: a symlink that escapes
// the dump at any path component (refused by os.Root), a non-directory standing
// in for a directory position, or a permission error. It replaces a wrapped OS
// error because that error carries the absolute path it failed on, which must
// never reach the caller. Customer-facing RU: no тире, no absolute path.
var errFormsDirUnreadable = errors.New("каталог форм объекта недоступен")

// errFormXMLNotRegular is the path-free RU refusal returned when a form file is
// not a plain regular file: a symlink, FIFO, socket, device or directory.
// Customer-facing RU: no тире, no absolute path.
var errFormXMLNotRegular = errors.New("файл формы имеет неверный тип")

// FindFormFiles locates all Form.xml files for the given object in the dump directory.
// It returns a map of form name to absolute file path.
//
// objectName reaches this function from the get_form_structure tool input, so the
// lookup is confined twice over. The lexical guard below keeps objectName a single
// path component (rejecting ".." and both separators, on POSIX and Windows), and
// the whole walk then runs through an os.Root opened on the dump: the OS primitive
// (openat2 RESOLVE_BENEATH on Linux, equivalents elsewhere) refuses a symlink that
// escapes the dump at ANY component, which the lexical guard cannot see. That is
// the same containment the subsystem readers use, and it closes the vector named in
// index.go: a malicious dump smuggling an outside host file in as a symlink.
//
// A form is reported only when its Form.xml is a plain regular file genuinely
// inside the dump. Because root.Lstat does not follow the final component, a
// symlinked Form.xml is neither read NOR listed, so the return value cannot serve
// as an existence oracle for paths outside the dump.
func FindFormFiles(dumpDir, objectType, objectName string) (map[string]string, error) {
	dirName, ok := objectTypeToDumpDir[objectType]
	if !ok {
		return nil, fmt.Errorf("unknown object type %q for dump lookup", objectType)
	}

	if strings.Contains(objectName, "..") ||
		strings.Contains(objectName, "/") ||
		strings.Contains(objectName, "\\") {
		return nil, fmt.Errorf("invalid object name %q: contains path traversal characters", objectName)
	}

	// A dumpDir that exists but is not a directory (FIFO, socket, device, plain
	// file) is refused BEFORE os.OpenRoot: on a writer-less FIFO the open blocks
	// forever and cannot be interrupted. Mirrors ParseAllSubsystemsCtx.
	if dumpDirIsNonDir(dumpDir) {
		return nil, errDumpDirNotDirectory
	}

	root, err := os.OpenRoot(dumpDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // Absent dump - no forms, not an error.
		}
		return nil, errFormsDirUnreadable
	}
	defer func() { _ = root.Close() }()

	relForms := filepath.Join(dirName, objectName, "Forms")
	entries, err := readDirInRoot(root, relForms)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // No forms directory - not an error.
		}
		// Containment refusal, non-directory position, or unreadable: never
		// silent, but named without disclosing the path it failed on.
		return nil, errFormsDirUnreadable
	}

	result := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		relXML := filepath.Join(relForms, entry.Name(), "Ext", "Form.xml")
		if st, statErr := root.Lstat(relXML); statErr == nil && st.Mode().IsRegular() {
			result[entry.Name()] = filepath.Join(dumpDir, relXML)
		}
	}

	return result, nil
}

// ParseFormXML parses a 1C form XML file and extracts elements, commands, and handlers.
//
// CONTRACT: path must already be a dump-contained path, normally a value returned
// by FindFormFiles, which resolves every component through an os.Root so none of
// them can escape the dump. ParseFormXML performs no containment of its own: given
// a path to a regular file anywhere it will read that file. Any caller that lets a
// user influence the path must obtain it from FindFormFiles rather than building it
// - the get_form_structure handler, the only caller in this module, does exactly
// that.
//
// The final component must be a plain regular file. That refuses a form file
// swapped for a symlink in the window between FindFormFiles vetting it and this
// read, and it refuses a writer-less FIFO whose read would otherwise block forever.
func ParseFormXML(path string) (*FormInfo, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("reading form XML: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, errFormXMLNotRegular
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading form XML: %w", err)
	}

	return parseFormXMLData(data)
}

// parseFormXMLData parses XML data from a 1C form dump file.
//
// The dump uses the xcf/logform schema:
//
//	<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" ...>
//	  <Title><v8:item><v8:lang>ru</v8:lang><v8:content>...</v8:content></v8:item></Title>
//	  <Events><Event name="OnOpen">ПриОткрытии</Event></Events>
//	  <ChildItems>
//	    <InputField name="Поле1" id="1">
//	      <DataPath>Объект.Поле1</DataPath>
//	      <Title>...localized...</Title>
//	      <Events><Event name="OnChange">Поле1ПриИзменении</Event></Events>
//	    </InputField>
//	    <UsualGroup name="Группа" id="2">
//	      <ChildItems>...recursive...</ChildItems>
//	    </UsualGroup>
//	  </ChildItems>
//	  <Commands>
//	    <Command name="Сохранить" id="1"><Action>СохранитьВыполнить</Action></Command>
//	  </Commands>
//	</Form>
//
// Notes:
//   - Element name comes from the "name" attribute, not from a <Name> child.
//   - <Event name="X">handler</Event> wraps handler name as text content.
//   - Form-level <Events> are reported as FormInfo.Handlers; element-level
//     <Events> are attached to the owning FormElementInfo.Events and are
//     NOT duplicated into FormInfo.Handlers.
//   - <ChildItems> are recursive - elements at any depth are flattened.
//   - The Go xml.Decoder resolves prefixed names so we match on Local only.
func parseFormXMLData(data []byte) (*FormInfo, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	form := &FormInfo{}

	// We track XML depth so we can disambiguate top-level vs nested sections
	// (only form-level <Events> become Handlers; element-level <Events>
	// are ignored for now to keep the flat list focused on UI structure).
	depth := 0
	// formDepth is the depth at which we observed the <Form> element, and the
	// sentinel -1 means we have not entered one yet. For the normal dump, where
	// <Form> is the document root, it becomes 1. It is also the whole basis of
	// FormInfo.NoFormRoot: if it is still -1 when the document ends, the file
	// held no form and nothing below was ever recorded.
	formDepth := -1

	for {
		tok, err := decoder.Token()
		if err != nil {
			// io.EOF is the decoder's normal end of document and is the only
			// error that means the file was read in full: xml.Decoder.Token
			// returns the io.EOF sentinel itself once the input is consumed.
			// Every other error is a *xml.SyntaxError raised part way through,
			// so the loop is abandoning a file it has not finished reading.
			// Truncation reports "unexpected EOF" as a *xml.SyntaxError and NOT
			// as io.ErrUnexpectedEOF, so an io.EOF test is the correct and
			// sufficient one here.
			//
			// This also catches breakage below the top level. Each nested
			// reader in this file swallows the decoder error and returns what
			// it collected, but the decoder latches the syntax error and
			// returns it again on every later call, so the loop above lands
			// here on its next Token() rather than on a clean EOF.
			if !errors.Is(err, io.EOF) {
				form.ParseIncomplete = true
			} else if formDepth == -1 {
				// Clean end of document and we never entered a <Form>: the file
				// was read whole and does not describe a form. Reachable for an
				// empty file, plain text, whitespace or a comment alone, and a
				// well formed document rooted at something else.
				//
				// Deliberately on the io.EOF branch ONLY. After a syntax error
				// the read was abandoned, so a <Form> beyond the break was never
				// reached and calling the file formless would be a guess. Keeping
				// it here is also what makes the two flags mutually exclusive, so
				// a caller can state each cause without the second answer
				// contradicting the first.
				form.NoFormRoot = true
			}
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			local := t.Name.Local

			// Capture root form depth so direct children can be recognised
			// regardless of any XML prologue / leading whitespace.
			if formDepth == -1 && local == "Form" {
				formDepth = depth
				continue
			}

			// Direct children of <Form>.
			if depth == formDepth+1 {
				switch local {
				case "Title":
					form.Title = readLocalizedString(decoder, &depth)
				case "Events":
					form.Handlers = parseEventsSection(decoder, &depth)
				case "ChildItems":
					form.Elements = parseChildItemsRecursive(decoder, &depth)
				case "Commands":
					form.Commands = parseCommandsSection(decoder, &depth)
				default:
					if isFormElementTag(local) {
						// Form has a direct UI child without a <ChildItems>
						// wrapper (rare but possible - AutoCommandBar lives
						// here too, but it is filtered by isFormElementTag).
						appendElement(&form.Elements, t, decoder, &depth)
					} else {
						skipElement(decoder, &depth)
					}
				}
			}

		case xml.EndElement:
			depth--
		}
	}

	return form, nil
}

// parseChildItemsRecursive reads a <ChildItems> block and flattens every
// descendant element (and its own ChildItems, recursively) into a single
// slice. Service decorations (ContextMenu, ExtendedTooltip, …) are skipped.
// Transparent containers (e.g. AutoCommandBar) are not recorded themselves
// but their nested ChildItems are descended into so buttons inside command
// bars surface in the flat list.
func parseChildItemsRecursive(decoder *xml.Decoder, depth *int) []FormElementInfo {
	var elements []FormElementInfo
	sectionDepth := *depth

	for {
		tok, err := decoder.Token()
		if err != nil {
			return elements
		}

		switch t := tok.(type) {
		case xml.StartElement:
			*depth++
			local := t.Name.Local

			switch {
			case isServiceElementTag(local):
				skipElement(decoder, depth)
			case isTransparentContainerTag(local):
				elements = append(elements, descendIntoChildItems(decoder, depth)...)
			case isFormElementTag(local):
				appendElement(&elements, t, decoder, depth)
			default:
				// Unknown tag inside ChildItems: skip without recording.
				skipElement(decoder, depth)
			}

		case xml.EndElement:
			*depth--
			if *depth < sectionDepth {
				return elements
			}
		}
	}
}

// descendIntoChildItems reads an element subtree and returns only the
// elements discovered inside its <ChildItems> child (if any). The
// surrounding element itself is not recorded. Used for transparent
// containers such as AutoCommandBar where the wrapper is noise but its
// inner buttons are meaningful.
func descendIntoChildItems(decoder *xml.Decoder, depth *int) []FormElementInfo {
	var elements []FormElementInfo
	containerDepth := *depth

	for {
		tok, err := decoder.Token()
		if err != nil {
			return elements
		}

		switch t := tok.(type) {
		case xml.StartElement:
			*depth++
			if *depth == containerDepth+1 && t.Name.Local == "ChildItems" {
				elements = append(elements, parseChildItemsRecursive(decoder, depth)...)
			} else {
				skipElement(decoder, depth)
			}

		case xml.EndElement:
			*depth--
			if *depth < containerDepth {
				return elements
			}
		}
	}
}

// appendElement parses one form element starting at its xml.StartElement and
// appends it (plus any nested ChildItems descendants) into elements.
func appendElement(elements *[]FormElementInfo, start xml.StartElement, decoder *xml.Decoder, depth *int) {
	elem, nested := parseFormElement(decoder, start, depth)
	*elements = append(*elements, elem)
	*elements = append(*elements, nested...)
}

// parseFormElement reads a single form element. It returns the element itself
// plus any descendants found inside nested <ChildItems> blocks.
func parseFormElement(decoder *xml.Decoder, start xml.StartElement, depth *int) (FormElementInfo, []FormElementInfo) {
	elem := FormElementInfo{
		Type: start.Name.Local,
		Name: attr(start, "name"),
	}
	var nested []FormElementInfo
	elemDepth := *depth

	for {
		tok, err := decoder.Token()
		if err != nil {
			return elem, nested
		}

		switch t := tok.(type) {
		case xml.StartElement:
			*depth++
			local := t.Name.Local

			// Only inspect direct children of this element.
			if *depth == elemDepth+1 {
				switch {
				case local == "Title":
					elem.Title = readLocalizedString(decoder, depth)
				case local == "DataPath":
					elem.DataPath = readCharData(decoder, depth)
				case local == "Events":
					// Element-level <Events> belong to this element only.
					// Nested ChildItems will keep their own <Events> attached
					// to their own elements via the same code path.
					elem.Events = parseEventsSection(decoder, depth)
				case local == "ChildItems":
					nested = append(nested, parseChildItemsRecursive(decoder, depth)...)
				case isTransparentContainerTag(local):
					// e.g. a Table containing <AutoCommandBar><ChildItems>...</ChildItems></AutoCommandBar>
					// - surface its buttons in the flat list.
					nested = append(nested, descendIntoChildItems(decoder, depth)...)
				default:
					skipElement(decoder, depth)
				}
			} else {
				// Defensive: any deeper start (shouldn't normally happen
				// because direct-child handlers consume their subtree)
				// is skipped to keep the depth counter balanced.
				skipElement(decoder, depth)
			}

		case xml.EndElement:
			*depth--
			if *depth < elemDepth {
				return elem, nested
			}
		}
	}
}

// parseCommandsSection reads all commands from the top-level <Commands> block.
// Each <Command name="X" id="Y"><Action>Z</Action></Command> becomes
// FormCommandInfo{Name: "X", Action: "Z"}.
func parseCommandsSection(decoder *xml.Decoder, depth *int) []FormCommandInfo {
	var commands []FormCommandInfo
	sectionDepth := *depth

	for {
		tok, err := decoder.Token()
		if err != nil {
			return commands
		}

		switch t := tok.(type) {
		case xml.StartElement:
			*depth++
			if t.Name.Local == "Command" {
				cmd := parseFormCommand(decoder, t, depth)
				if cmd.Name != "" {
					commands = append(commands, cmd)
				}
			} else {
				skipElement(decoder, depth)
			}

		case xml.EndElement:
			*depth--
			if *depth < sectionDepth {
				return commands
			}
		}
	}
}

// parseFormCommand reads a single <Command> entry. The command name comes
// from the "name" attribute; the action comes from the <Action> child text.
func parseFormCommand(decoder *xml.Decoder, start xml.StartElement, depth *int) FormCommandInfo {
	cmd := FormCommandInfo{Name: attr(start, "name")}
	cmdDepth := *depth

	for {
		tok, err := decoder.Token()
		if err != nil {
			return cmd
		}

		switch t := tok.(type) {
		case xml.StartElement:
			*depth++
			if *depth == cmdDepth+1 && t.Name.Local == "Action" {
				cmd.Action = readCharData(decoder, depth)
			} else {
				skipElement(decoder, depth)
			}

		case xml.EndElement:
			*depth--
			if *depth < cmdDepth {
				return cmd
			}
		}
	}
}

// parseEventsSection reads <Event name="X">handler</Event> entries.
// Used for both the form-level <Events> block (FormInfo.Handlers) and
// element-level <Events> (FormElementInfo.Events).
func parseEventsSection(decoder *xml.Decoder, depth *int) []FormHandlerInfo {
	var handlers []FormHandlerInfo
	sectionDepth := *depth

	for {
		tok, err := decoder.Token()
		if err != nil {
			return handlers
		}

		switch t := tok.(type) {
		case xml.StartElement:
			*depth++
			if t.Name.Local == "Event" {
				h := FormHandlerInfo{
					Event:   attr(t, "name"),
					Handler: readCharData(decoder, depth),
				}
				if h.Event != "" && h.Handler != "" {
					handlers = append(handlers, h)
				}
			} else {
				skipElement(decoder, depth)
			}

		case xml.EndElement:
			*depth--
			if *depth < sectionDepth {
				return handlers
			}
		}
	}
}

// readCharData reads the text content of the current element and consumes its
// end tag. Nested elements (if any) are skipped to keep the depth balanced.
func readCharData(decoder *xml.Decoder, depth *int) string {
	var sb strings.Builder

	for {
		tok, err := decoder.Token()
		if err != nil {
			return strings.TrimSpace(sb.String())
		}

		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.StartElement:
			*depth++
			skipElement(decoder, depth)
		case xml.EndElement:
			*depth--
			return strings.TrimSpace(sb.String())
		}
	}
}

// readLocalizedString reads a 1C localized string (v8:LocalStringType).
// It returns the first available <v8:item><v8:content> value - typically
// the Russian text. Go's xml.Decoder resolves prefixed names so we only
// inspect the Local part.
func readLocalizedString(decoder *xml.Decoder, depth *int) string {
	var result string
	titleDepth := *depth

	for {
		tok, err := decoder.Token()
		if err != nil {
			return result
		}

		switch t := tok.(type) {
		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text != "" && result == "" {
				result = text
			}
		case xml.StartElement:
			*depth++
			if t.Name.Local == "item" {
				val := readLocalizedItem(decoder, depth)
				if val != "" && result == "" {
					result = val
				}
			} else {
				skipElement(decoder, depth)
			}

		case xml.EndElement:
			*depth--
			if *depth < titleDepth {
				return result
			}
		}
	}
}

// readLocalizedItem reads a single <v8:item> entry and returns the
// <v8:content> child text.
func readLocalizedItem(decoder *xml.Decoder, depth *int) string {
	var content string
	itemDepth := *depth

	for {
		tok, err := decoder.Token()
		if err != nil {
			return content
		}

		switch t := tok.(type) {
		case xml.StartElement:
			*depth++
			if t.Name.Local == "content" {
				content = readCharData(decoder, depth)
			} else {
				skipElement(decoder, depth)
			}

		case xml.EndElement:
			*depth--
			if *depth < itemDepth {
				return content
			}
		}
	}
}

// skipElement consumes all tokens until the matching end element.
func skipElement(decoder *xml.Decoder, depth *int) {
	skipDepth := *depth

	for {
		tok, err := decoder.Token()
		if err != nil {
			return
		}

		switch tok.(type) {
		case xml.StartElement:
			*depth++
		case xml.EndElement:
			*depth--
			if *depth < skipDepth {
				return
			}
		}
	}
}

// attr returns the value of an attribute by local name, ignoring namespace.
func attr(start xml.StartElement, name string) string {
	for _, a := range start.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// formElementTags lists XML tag names that represent meaningful form elements.
// Listed elements are recorded; everything else inside ChildItems is skipped.
var formElementTags = map[string]bool{
	"InputField":               true,
	"LabelField":               true,
	"CheckBoxField":            true,
	"RadioButtonField":         true,
	"NumberField":              true,
	"TextDocumentField":        true,
	"SpreadsheetDocumentField": true,
	"PictureField":             true,
	"Table":                    true,
	"FormattedDocumentField":   true,
	"PlannerField":             true,
	"DendrogramField":          true,
	"ChartField":               true,
	"GanttChartField":          true,
	"PeriodField":              true,
	"ProgressBarField":         true,
	"TrackBarField":            true,
	"CalendarField":            true,
	"HTMLDocumentField":        true,
	"Button":                   true,
	"UsualGroup":               true,
	"Pages":                    true,
	"Page":                     true,
	"CommandBar":               true,
	"Popup":                    true,
	"ColumnGroup":              true,
	"LabelDecoration":          true,
	"PictureDecoration":        true,
	"Hyperlink":                true,
	"Addition":                 true,
	"ButtonGroup":              true,
}

func isFormElementTag(tag string) bool {
	return formElementTags[tag]
}

// serviceElementTags lists XML tag names that are purely decorative or
// auxiliary and should never appear in the user-facing element list.
// They are emitted by the 1C designer behind almost every UI control
// and would otherwise drown out the meaningful structure.
var serviceElementTags = map[string]bool{
	"ContextMenu":           true,
	"ExtendedTooltip":       true,
	"ShortTooltip":          true,
	"SearchStringAddition":  true,
	"ViewStatusAddition":    true,
	"SearchControlAddition": true,
}

func isServiceElementTag(tag string) bool {
	return serviceElementTags[tag]
}

// transparentContainerTags are wrappers whose own presence is uninteresting
// but whose nested <ChildItems> contain real UI elements (e.g. command-bar
// buttons). We descend into them without recording the wrapper itself.
var transparentContainerTags = map[string]bool{
	"AutoCommandBar": true,
}

func isTransparentContainerTag(tag string) bool {
	return transparentContainerTags[tag]
}

// elementTypeDisplayName maps XML element types to Russian display names.
var elementTypeDisplayName = map[string]string{
	"InputField":               "ПолеВвода",
	"LabelField":               "ПолеНадписи",
	"CheckBoxField":            "ФлажокПоле",
	"RadioButtonField":         "ПолеПереключателя",
	"NumberField":              "ПолеЧисла",
	"TextDocumentField":        "ПолеТекстовогоДокумента",
	"SpreadsheetDocumentField": "ПолеТабличногоДокумента",
	"PictureField":             "ПолеКартинки",
	"Table":                    "ТаблицаФормы",
	"Button":                   "Кнопка",
	"ButtonGroup":              "ГруппаКнопок",
	"UsualGroup":               "ОбычнаяГруппа",
	"Pages":                    "Страницы",
	"Page":                     "Страница",
	"CommandBar":               "КоманднаяПанель",
	"LabelDecoration":          "ДекорацияНадпись",
	"PictureDecoration":        "ДекорацияКартинка",
	"Hyperlink":                "Гиперссылка",
}

// DisplayType returns a Russian name for the element type, or the raw tag if unknown.
func DisplayType(elementType string) string {
	if name, ok := elementTypeDisplayName[elementType]; ok {
		return name
	}
	return elementType
}
