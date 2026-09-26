package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewFormStructureHandler_SpreadsheetFieldAndUnknownTagReachTheAnswer
// drives get_form_structure against a dump whose form holds a spreadsheet
// field, written the way the designer writes it, and an element tag the
// parser does not know. Both must be in the element table, and the unknown one
// must be named in a note with its count.
func TestNewFormStructureHandler_SpreadsheetFieldAndUnknownTagReachTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Reports", "Отчет1", "ФормаОтчета", `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildItems>
    <SpreadSheetDocumentField name="Результат" id="1">
      <DataPath>Результат</DataPath>
    </SpreadSheetDocumentField>
    <FutureField name="Новое1" id="2"/>
    <FutureField name="Новое2" id="3"/>
  </ChildItems>
</Form>`)

	result, err := callFormHandler(t, srv.URL, dumpDir, "Report", "Отчет1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	for _, want := range []string{
		"| Результат | ПолеТабличногоДокумента |",
		"| Новое1 | FutureField |",
		"| Новое2 | FutureField |",
		"элементы незнакомого серверу вида, всего 2: `FutureField` (2)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("answer lacks %q:\n%s", want, text)
		}
	}
}

// TestNewFormStructureHandler_KnownTagsCarryNoUnknownNote is the control: a
// form of known tags only gets no such note.
func TestNewFormStructureHandler_KnownTagsCarryNoUnknownNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Reports", "Отчет1", "ФормаОтчета", `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildItems>
    <SpreadSheetDocumentField name="Результат" id="1"/>
  </ChildItems>
</Form>`)

	result, err := callFormHandler(t, srv.URL, dumpDir, "Report", "Отчет1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "| Результат | ПолеТабличногоДокумента |") {
		t.Fatalf("control: spreadsheet field missing:\n%s", text)
	}
	if strings.Contains(text, "незнакомого серверу вида") {
		t.Errorf("a form of known tags must carry no unknown-element note:\n%s", text)
	}
}
