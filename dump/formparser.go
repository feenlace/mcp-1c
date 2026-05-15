package dump

import (
	"bytes"
	"encoding/xml"
	"fmt"
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

// FindFormFiles locates all Form.xml files for the given object in the dump directory.
// It returns a map of form name to absolute file path.
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

	formsDir := filepath.Join(dumpDir, dirName, objectName, "Forms")
	entries, err := os.ReadDir(formsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No forms directory - not an error.
		}
		return nil, fmt.Errorf("reading forms directory: %w", err)
	}

	result := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		formXML := filepath.Join(formsDir, entry.Name(), "Ext", "Form.xml")
		if _, statErr := os.Stat(formXML); statErr == nil {
			result[entry.Name()] = formXML
		}
	}

	return result, nil
}

// ParseFormXML parses a 1C form XML file and extracts elements, commands, and handlers.
func ParseFormXML(path string) (*FormInfo, error) {
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
	// formDepth is the depth at which we observed the root <Form> element.
	// Stays at 0 until we enter it, then becomes 1.
	formDepth := -1

	for {
		tok, err := decoder.Token()
		if err != nil {
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
