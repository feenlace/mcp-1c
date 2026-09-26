package dump

import (
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// elementTagForm wraps the given ChildItems body in a minimal logform document.
func elementTagForm(childItems string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" xmlns:v8="http://v8.1c.ru/8.1/data/core" version="2.21">
  <ChildItems>` + childItems + `
  </ChildItems>
</Form>`)
}

// TestParseFormXML_PlatformFieldTagsAreRecognised covers the element tags the
// designer writes that the tables used to miss: the spreadsheet field (written
// SpreadSheetDocumentField, the table had SpreadsheetDocumentField) and three
// fields that were not listed at all.
func TestParseFormXML_PlatformFieldTagsAreRecognised(t *testing.T) {
	cases := []struct {
		tag  string
		want string
	}{
		{"SpreadSheetDocumentField", "ПолеТабличногоДокумента"},
		{"PDFDocumentField", "ПолеPDFДокумента"},
		{"GraphicalSchemaField", "ПолеГрафическойСхемы"},
		{"GeographicalSchemaField", "ПолеГеографическойСхемы"},
	}
	for _, c := range cases {
		t.Run(c.tag, func(t *testing.T) {
			body := `
    <` + c.tag + ` name="Поле" id="1">
      <DataPath>Объект.Поле</DataPath>
      <ContextMenu name="ПолеКонтекстноеМеню" id="2"/>
      <ExtendedTooltip name="ПолеРасширеннаяПодсказка" id="3"/>
    </` + c.tag + `>`
			form, err := parseFormXMLData(elementTagForm(body))
			if err != nil {
				t.Fatal(err)
			}
			if len(form.Elements) != 1 || form.Elements[0].Type != c.tag ||
				form.Elements[0].Name != "Поле" || form.Elements[0].DataPath != "Объект.Поле" {
				t.Fatalf("want one %s element Поле, got %+v", c.tag, form.Elements)
			}
			if len(form.UnknownElements) != 0 {
				t.Errorf("%s must be a known tag, got UnknownElements %+v", c.tag, form.UnknownElements)
			}
			if got := DisplayType(c.tag); got != c.want {
				t.Errorf("DisplayType(%q) = %q, want %q", c.tag, got, c.want)
			}
		})
	}
}

// TestDisplayType_OldSpreadsheetSpellingKept pins the alias kept for callers of
// the exported DisplayType that pass the old spelling.
func TestDisplayType_OldSpreadsheetSpellingKept(t *testing.T) {
	if got := DisplayType("SpreadsheetDocumentField"); got != "ПолеТабличногоДокумента" {
		t.Errorf("DisplayType(old spelling) = %q", got)
	}
}

// TestParseFormXML_UnknownElementTagIsKeptAndReported: an element whose tag
// the tables do not know is recorded under its raw tag, its own nested
// ChildItems are still read, and the form reports it in UnknownElements.
func TestParseFormXML_UnknownElementTagIsKeptAndReported(t *testing.T) {
	body := `
    <InputField name="Известное" id="1"/>
    <FutureField name="Новое1" id="2">
      <DataPath>Объект.Новое</DataPath>
      <ContextMenu name="Новое1КонтекстноеМеню" id="3"/>
      <ChildItems>
        <Button name="ВнутриНового" id="4"/>
        <FutureField name="Новое2" id="5"/>
      </ChildItems>
    </FutureField>
    <OtherFutureField name="Другое" id="6"/>`
	form, err := parseFormXMLData(elementTagForm(body))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range form.Elements {
		got = append(got, e.Type+":"+e.Name)
	}
	want := []string{"InputField:Известное", "FutureField:Новое1", "Button:ВнутриНового",
		"FutureField:Новое2", "OtherFutureField:Другое"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("elements = %v, want %v", got, want)
	}
	wantUnknown := []FormUnknownElement{{Tag: "FutureField", Count: 2}, {Tag: "OtherFutureField", Count: 1}}
	if !reflect.DeepEqual(form.UnknownElements, wantUnknown) {
		t.Errorf("UnknownElements = %+v, want %+v", form.UnknownElements, wantUnknown)
	}
	if DisplayType("FutureField") != "FutureField" {
		t.Errorf("an unknown tag must be displayed as the raw tag")
	}
	if form.ParseIncomplete || form.NoFormRoot {
		t.Errorf("an unknown tag is not a parse failure: %+v", form)
	}

	// Control: a form of known tags only reports nothing.
	known, err := parseFormXMLData(elementTagForm(`<InputField name="А" id="1"/>`))
	if err != nil {
		t.Fatal(err)
	}
	if known.UnknownElements != nil {
		t.Errorf("control: known-only form reported %+v", known.UnknownElements)
	}
}

// independentServiceTags is written out here rather than read from
// serviceElementTags, so the corpus walk below does not share the parser's
// tables. These decorations are deliberately not listed as form elements.
var independentServiceTags = map[string]bool{
	"ContextMenu": true, "ExtendedTooltip": true, "ShortTooltip": true,
	"SearchStringAddition": true, "ViewStatusAddition": true, "SearchControlAddition": true,
}

// independentElementCounts counts, per tag, every element whose parent is a
// ChildItems block and that has no service decoration among its ancestors.
// It is a plain token walk and uses none of the parser's tables.
func independentElementCounts(data []byte) (map[string]int, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	counts := map[string]int{}
	var stack []string
	serviceDepth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return counts, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			l := t.Name.Local
			if serviceDepth == 0 && len(stack) > 0 && stack[len(stack)-1] == "ChildItems" &&
				!independentServiceTags[l] {
				counts[l]++
			}
			if serviceDepth > 0 || independentServiceTags[l] {
				serviceDepth++
			}
			stack = append(stack, l)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
			if serviceDepth > 0 {
				serviceDepth--
			}
		}
	}
}

// TestParseFormXML_CorpusEveryChildItemsElementIsListed walks a real dump and
// checks that every element an independent walk finds under ChildItems is in
// the parser output with the same tag, and that the platform writes no tag the
// tables do not know.
func TestParseFormXML_CorpusEveryChildItemsElementIsListed(t *testing.T) {
	root := os.Getenv(corpusEnv)
	if root == "" {
		t.Log(corpusSkipNotice())
		t.Skip(corpusSkipReason())
	}
	files, forms, elements := 0, 0, 0
	byTag := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "Form.xml" {
			return nil
		}
		files++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "xcf/logform") {
			return nil // an ordinary (non-managed) form description
		}
		forms++
		want, err := independentElementCounts(data)
		if err != nil {
			t.Errorf("%s: independent walk: %v", path, err)
			return nil
		}
		form, err := parseFormXMLData(data)
		if err != nil {
			t.Errorf("%s: parse: %v", path, err)
			return nil
		}
		got := map[string]int{}
		for _, e := range form.Elements {
			got[e.Type]++
		}
		if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%s: parser elements by tag %v, independent walk %v", path, got, want)
		}
		if len(form.UnknownElements) != 0 {
			t.Errorf("%s: unknown element tags %+v", path, form.UnknownElements)
		}
		for tag, n := range want {
			byTag[tag] += n
			elements += n
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if forms == 0 || elements == 0 {
		t.Fatalf("control failed: %s held %d Form.xml, %d managed forms, %d elements", root, files, forms, elements)
	}
	t.Logf("%d Form.xml, %d managed forms, %d elements, %d tags: %v", files, forms, elements, len(byTag), byTag)
}

// TestParseFormXML_FormCommandBarButtonsAreListed: the form's own
// AutoCommandBar precedes its ChildItems in a designer dump. Its buttons are
// listed, and reading ChildItems afterwards does not overwrite them.
func TestParseFormXML_FormCommandBarButtonsAreListed(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <AutoCommandBar name="ФормаКоманднаяПанель" id="-1">
    <ChildItems>
      <Button name="ФормаЗаписать" id="10"/>
      <ButtonGroup name="ФормаГруппа" id="11">
        <ChildItems>
          <Button name="ФормаПечать" id="12"/>
        </ChildItems>
      </ButtonGroup>
    </ChildItems>
  </AutoCommandBar>
  <ChildItems>
    <InputField name="Поле" id="1"/>
  </ChildItems>
</Form>`)
	form, err := parseFormXMLData(data)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range form.Elements {
		got = append(got, e.Type+":"+e.Name)
	}
	want := []string{"Button:ФормаЗаписать", "ButtonGroup:ФормаГруппа", "Button:ФормаПечать", "InputField:Поле"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("elements = %v, want %v", got, want)
	}
}
