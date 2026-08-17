package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
)

// A dynamic list is the fourth thing a form can supply, alongside elements,
// commands and handlers, and it arrives ONLY from the dump. The 1C HTTP service
// has no field for it: ФормаGET walks Форма.Элементы and Форма.Команды and
// nothing else.
//
// The section prints a summary and never a query text. That is a rule about this
// renderer and is pinned below with a positive control, because "the text is not
// here" is trivially true of an empty answer.

// listsOnlyDumpForm is a dump-side form that supplies no elements, no commands
// and no handlers, which is the shape the whole fourth term is about.
func listsOnlyDumpForm(name string) *onec.FormStructure {
	return &onec.FormStructure{
		Name:     name,
		Elements: []onec.FormElement{},
		Commands: []onec.FormCommand{},
		Handlers: []onec.FormHandler{},
	}
}

// TestMergeDumpIntoForm_ListsOnlyDumpDoesNotCarryOffTheServiceTitle is written
// first because it is the one thing the fourth term can break.
//
// mergeDumpIntoForm replaces the WHOLE response when the dump supplied a body
// and the two sources name different forms, and it does that because Title,
// Commands and Handlers all describe the HTTP-named form: printing them under
// another form's heading is not a partial answer but a wrong one. Dynamic lists
// do not join that set. They are not on the wire type, the service never sends
// them, and letting them trigger the wholesale replace would throw away a real
// 1C Title in exchange for a list summary.
//
// So the replace stays keyed on the three name-bound collections, and the lists
// are merged on their own.
func TestMergeDumpIntoForm_ListsOnlyDumpDoesNotCarryOffTheServiceTitle(t *testing.T) {
	form := &onec.FormStructure{
		Name:  "ФормаДокумента",
		Title: "Реализация товаров и услуг",
		Elements: []onec.FormElement{
			{Name: "Контрагент", Type: "ПолеВвода", DataPath: "Объект.Контрагент"},
		},
	}
	// A DIFFERENT form name, which is exactly the condition the wholesale
	// replace tests. With the lists counted as a body, this call would replace
	// the response and the title above would be gone.
	dumpForm := listsOnlyDumpForm("ФормаСписка")
	dumpLists := []dump.FormDynamicList{
		{Name: "Список", ManualQuery: true, MainTable: "Catalog.Валюты", QueryText: "ВЫБРАТЬ 1"},
	}

	lists := mergeDumpIntoForm(form, dumpForm, dumpLists)

	if form.Title != "Реализация товаров и услуг" {
		t.Errorf("the 1C title was replaced by a dump form that supplied only lists: got %q", form.Title)
	}
	if form.Name != "ФормаДокумента" {
		t.Errorf("the 1C form name was replaced by a dump form that supplied only lists: got %q", form.Name)
	}
	if len(form.Elements) != 1 {
		t.Errorf("the 1C elements were dropped by a dump form that supplied only lists: %+v", form.Elements)
	}
	if len(lists) != 1 || lists[0].Name != "Список" {
		t.Errorf("the dump's dynamic lists were not carried into the answer: %+v", lists)
	}
}

// TestMergeDumpIntoForm_RealBodyStillTakesOverIsTheControl is the other half of
// the pair. Without it the test above is satisfied by a merge that never
// replaces anything, which would be a different defect.
func TestMergeDumpIntoForm_RealBodyStillTakesOverIsTheControl(t *testing.T) {
	form := &onec.FormStructure{
		Name:  "ФормаДокумента",
		Title: "Реализация товаров и услуг",
	}
	dumpForm := &onec.FormStructure{
		Name:     "ФормаСписка",
		Title:    "Список реализаций",
		Elements: []onec.FormElement{{Name: "Список", Type: "ТаблицаФормы"}},
	}

	mergeDumpIntoForm(form, dumpForm, nil)

	if form.Name != "ФормаСписка" || form.Title != "Список реализаций" {
		t.Errorf("control failed: a dump form WITH a body must still take over the response, "+
			"got name %q title %q", form.Name, form.Title)
	}
}

// TestSuppliesStructure_ADynamicListIsComposition covers the predicate both the
// merge decision and the response notes are built from. A form whose only
// content is a dynamic list DOES supply composition: the answer gets a section
// it would not otherwise have.
func TestSuppliesStructure_ADynamicListIsComposition(t *testing.T) {
	empty := listsOnlyDumpForm("Ф")
	lists := []dump.FormDynamicList{{Name: "Список"}}

	if suppliesStructure(empty, nil) {
		t.Error("a form with nothing at all must not count as supplying structure")
	}
	if !suppliesStructure(empty, lists) {
		t.Error("a form whose only content is a dynamic list DOES supply structure: the answer " +
			"carries a section it would not have without it")
	}

	// The narrower predicate keeps its old meaning, and the two must not be
	// confused: only the three name-bound collections travel with the identity.
	if suppliesNameBoundStructure(empty) {
		t.Error("a lists-only form supplies no name-bound structure, so it must not trigger " +
			"the wholesale replace in mergeDumpIntoForm")
	}
}

// TestNewFormStructureHandler_ListsOnlyFormGetsNoNoStructureNote is the note
// half of the same fact. formNameNoStructureNote says in so many words that
// "в разделы состава формы выше из выгрузки не попало ничего", which is refuted
// by the list section printed directly above it.
func TestNewFormStructureHandler_ListsOnlyFormGetsNoNoStructureNote(t *testing.T) {
	srv := formHTTPServer(t, "ФормаСписка", "Список валют")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "ФормаСписка", listsOnlyFormXML)

	result, err := callFormHandler(t, srv.URL, dumpDir, "Catalog", "Валюты", "ФормаСписка")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if strings.Contains(text, "Параметр `form_name` не дал состава формы") {
		t.Errorf("the dump DID supply composition, a dynamic list section, so the note "+
			"denying it must not appear:\n%s", text)
	}
	if !strings.Contains(text, "## Динамические списки формы") {
		t.Errorf("the list section is missing from the answer:\n%s", text)
	}
}

// listsOnlyFormXML is a form that declares one dynamic list and nothing else:
// no ChildItems, no Commands, no Events. Real forms of this shape exist; more to
// the point, it isolates the fourth term.
const listsOnlyFormXML = `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"
      xmlns:v8="http://v8.1c.ru/8.1/data/core"
      xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.21">
  <Attributes>
    <Attribute name="Список" id="1">
      <Settings xsi:type="DynamicList">
        <ManualQuery>true</ManualQuery>
        <QueryText>ВЫБРАТЬ
	Валюты.Ссылка
ИЗ
	Справочник.Валюты КАК Валюты</QueryText>
        <MainTable>Catalog.Валюты</MainTable>
      </Settings>
    </Attribute>
  </Attributes>
</Form>`

// TestFormatFormStructure_DynamicListSection covers what the section prints.
func TestFormatFormStructure_DynamicListSection(t *testing.T) {
	lists := []dump.FormDynamicList{
		{Name: "Список", ManualQuery: true, MainTable: "Catalog.Валюты", QueryText: "ВЫБРАТЬ 1"},
		{Name: "Курсы", ManualQuery: false, MainTable: "InformationRegister.КурсыВалют"},
		{Name: "БезТаблицы", ManualQuery: true},
	}
	text := formatFormStructure(&onec.FormStructure{Name: "ФормаСписка"}, lists)

	for _, want := range []string{
		"## Динамические списки формы",
		// The count comes from the slice, so it cannot drift from the rows.
		"Списков: 3",
		"| Имя реквизита | Произвольный запрос | Основная таблица |",
		"| `Список` | да | `Catalog.Валюты` |",
		"| `Курсы` | нет | `InformationRegister.КурсыВалют` |",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// A list with no main table is a row all the same: dropping it would hide
	// 154 real lists of 1918, 8 of them in common forms.
	if !strings.Contains(text, "| `БезТаблицы` | да |") {
		t.Errorf("a list with no main table must still get a row:\n%s", text)
	}
}

// TestFormatFormStructure_NoListsNoSection keeps the section out of the answer
// for the two thirds of forms that declare no list. Measured: 1628 of 5665
// Form.xml files carry any list at all.
func TestFormatFormStructure_NoListsNoSection(t *testing.T) {
	text := formatFormStructure(&onec.FormStructure{
		Name:     "ФормаДокумента",
		Elements: []onec.FormElement{{Name: "Поле", Type: "ПолеВвода"}},
	}, nil)

	if strings.Contains(text, "Динамические списки") {
		t.Errorf("a form with no dynamic lists must get no such section:\n%s", text)
	}
	// POSITIVE CONTROL: the renderer did produce an answer, so the absence above
	// is the rule and not an empty render.
	if !strings.Contains(text, "## Элементы формы") {
		t.Errorf("control failed: the renderer produced no element section either:\n%s", text)
	}
}

// TestFormatFormStructure_NeverPrintsTheQueryText is the rule this section is
// bounded by. It is checked with a marker planted INSIDE the query text, and
// with a control proving the marker is really there, because "the text is not
// printed" is trivially true of a renderer that printed nothing.
func TestFormatFormStructure_NeverPrintsTheQueryText(t *testing.T) {
	const marker = "МАРКЕР_ТЕКСТА_ЗАПРОСА"
	lists := []dump.FormDynamicList{
		{Name: "Список", ManualQuery: true, MainTable: "Catalog.Валюты",
			QueryText: "ВЫБРАТЬ " + marker + " ИЗ Справочник.Валюты"},
	}

	// POSITIVE CONTROL over the fixture: the marker and the query keyword really
	// are in the input, so a renderer that leaked them would be caught.
	if !strings.Contains(lists[0].QueryText, marker) || !strings.Contains(lists[0].QueryText, "ВЫБРАТЬ") {
		t.Fatal("control failed: the fixture carries no query text to leak")
	}

	text := formatFormStructure(&onec.FormStructure{Name: "ФормаСписка"}, lists)

	if strings.Contains(text, marker) {
		t.Errorf("the query text reached the rendered answer:\n%s", text)
	}
	if strings.Contains(text, "ВЫБРАТЬ") {
		t.Errorf("a query keyword reached the rendered answer:\n%s", text)
	}
	// And the row for that list IS printed: the rule is "no text", not "no list".
	if !strings.Contains(text, "| `Список` | да | `Catalog.Валюты` |") {
		t.Errorf("the list itself must still be summarised:\n%s", text)
	}
}

// TestFormatFormStructure_HostileFieldsCannotBreakTheTable covers a name or a
// main table that carries markdown of its own.
//
// The fixture has to be synthetic and that is measured, not assumed: across all
// 1918 dynamic lists in the reference dump, 0 of 1918 names and 0 of 1764
// non-empty main tables contain a line break, a backtick, a pipe, a hash, a
// less-than or a greater-than. There is no live exploit, so a fixture drawn from
// the corpus would prove nothing.
//
// The containment is the package's EXISTING one. inlineCode computes its
// delimiter from the longest backtick run in the payload and neutralises the
// runes a renderer treats as a mandatory line break; a second implementation of
// that count is how one of the two copies keeps a defect the other lost.
func TestFormatFormStructure_HostileFieldsCannotBreakTheTable(t *testing.T) {
	lists := []dump.FormDynamicList{
		{
			Name:      "Спи```сок",
			MainTable: "Catalog.\nВалюты | ## Заголовок",
		},
	}
	text := formatFormStructure(&onec.FormStructure{Name: "Ф"}, lists)

	// The row stays one line: a break inside a cell ends the table and the rest
	// of the payload becomes free markdown in an answer a model reads.
	var row string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "сок") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("the hostile list produced no row at all:\n%s", text)
	}
	if strings.Contains(row, "\n") || strings.Contains(row, "\r") {
		t.Errorf("the row carries a line break: %q", row)
	}

	// The payload's "## Заголовок" IS still visible in the cell, and that is
	// correct: it is the value, and hiding a value the caller asked about would
	// be the worse answer. What must not happen is it BEGINNING a line, because
	// that is the only position in which markdown reads it as a heading. So the
	// rule is checked where it lives, on line starts, and not on containment of
	// the characters.
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		if line != "## Динамические списки формы" && line != "# Форма: Ф" {
			t.Errorf("a heading was forged from the payload: %q", line)
		}
	}
	if !strings.Contains(row, "Заголовок") {
		t.Errorf("control failed: the payload text was dropped from the cell rather than "+
			"neutralised, so this test is measuring deletion and not containment: %q", row)
	}
	// An unescaped pipe from the payload would add a column.
	if strings.Count(row, "|")-strings.Count(row, `\|`) != 4 {
		t.Errorf("the row has the wrong number of unescaped cell separators, so the payload "+
			"added or removed a column: %q", row)
	}
	// The code span cannot be closed from inside: the delimiter is longer than
	// the longest run the payload carries.
	if strings.Contains(row, "````") == false && strings.Contains(row, "```сок") {
		t.Errorf("the payload's own backtick run closed its code span: %q", row)
	}
}

// TestOnecFormStructureCarriesNoDynamicListField pins where the fact does NOT
// live. onec.FormStructure is the shape of the 1C HTTP service's JSON reply; it
// is the decode target of a non-strict decoder and is shared with the HTTP path.
// A fact about how a local file was read does not belong on the wire type, and
// this package already recorded that reasoning once, for dumpFormRead.
func TestOnecFormStructureCarriesNoDynamicListField(t *testing.T) {
	typ := reflect.TypeOf(onec.FormStructure{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		lower := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
		if strings.Contains(lower, "dynamic") || strings.Contains(lower, "list") {
			t.Errorf("onec.FormStructure gained field %q (json %q): the dynamic lists travel "+
				"beside the wire type, not on it", f.Name, f.Tag.Get("json"))
		}
	}
	// POSITIVE CONTROL: the scan really is looking at fields.
	if typ.NumField() == 0 {
		t.Fatal("control failed: onec.FormStructure has no fields, so the scan proves nothing")
	}
}

// TestNewFormStructureHandler_FarSideDynamicListsAreNotPrinted covers the far
// side inventing the key. The decoder is not strict (onec.Client does not call
// DisallowUnknownFields), so an unknown key is dropped in silence; this pins
// that it stays dropped and never reaches the rendered answer.
func TestNewFormStructureHandler_FarSideDynamicListsAreNotPrinted(t *testing.T) {
	const marker = "МАРКЕР_С_ТОЙ_СТОРОНЫ"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":  "ФормаСписка",
			"title": "Список валют",
			"dynamic_lists": []any{
				map[string]any{"name": marker, "query_text": "ВЫБРАТЬ " + marker},
			},
		})
	}))
	t.Cleanup(srv.Close)

	result, err := callFormHandler(t, srv.URL, "", "Catalog", "Валюты", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if strings.Contains(text, marker) {
		t.Errorf("a dynamic_lists key invented by the far side reached the answer:\n%s", text)
	}
	// POSITIVE CONTROL: the rest of that same reply DID arrive, so the absence
	// above is the key being ignored and not the call having failed.
	if !strings.Contains(text, "ФормаСписка") {
		t.Errorf("control failed: nothing from the reply reached the answer:\n%s", text)
	}
}

// TestNewFormStructureHandler_CommonFormIsAnsweredFromTheDump is the whole point
// of this change seen from outside.
//
// The 1C HTTP service answers 404 for a common form: the extension's object map
// holds applied kinds only and ОбщиеФормы is not among them. Until now the dump
// leg could not answer either, because the type was unknown to it, so both legs
// failed and the call returned an error. Now the dump answers and the call
// succeeds, with a note saying the service did not take part.
func TestNewFormStructureHandler_CommonFormIsAnsweredFromTheDump(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "Object not found"})
	}))
	t.Cleanup(srv.Close)

	dumpDir := t.TempDir()
	extDir := filepath.Join(dumpDir, "CommonForms", "ФайлыВТоме", "Ext")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "Form.xml"), []byte(listsOnlyFormXML), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, objectType := range []string{"ОбщаяФорма", "CommonForm"} {
		t.Run(objectType, func(t *testing.T) {
			result, err := callFormHandler(t, srv.URL, dumpDir, objectType, "ФайлыВТоме", "")
			if err != nil {
				t.Fatalf("a common form must no longer fail outright: %v", err)
			}
			text := resultText(t, result)

			if !strings.Contains(text, "# Форма: ФайлыВТоме") {
				t.Errorf("the answer does not name the common form:\n%s", text)
			}
			if !strings.Contains(text, "## Динамические списки формы") {
				t.Errorf("the answer carries no dynamic list section:\n%s", text)
			}
			if !strings.Contains(text, "| `Список` | да | `Catalog.Валюты` |") {
				t.Errorf("the list summary is missing:\n%s", text)
			}
			if strings.Contains(text, "ВЫБРАТЬ") {
				t.Errorf("the query text must not appear in this answer:\n%s", text)
			}
			// The service really did fail, and the answer says so rather than
			// crediting 1C with what the dump supplied.
			if !strings.Contains(text, "Запрос к HTTP-сервису 1С завершился ошибкой") {
				t.Errorf("the answer does not say the 1C service call failed:\n%s", text)
			}
		})
	}
}
