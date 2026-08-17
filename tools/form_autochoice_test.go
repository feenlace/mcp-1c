package tools

import (
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// SILENCE USED TO BE HARMLESS AND IS NOT ANY MORE.
//
// With no form_name the dump leg picks the first form in order. That was always
// so, and while the answer only carried elements, commands and handlers, a
// reader took a thin answer for a thin form. The answer now also carries a
// dynamic-list section, and an ABSENT section reads as a finding: this form
// declares no dynamic list. When the form was chosen by this code rather than by
// the caller, that finding is about a form nobody asked about.
//
// Measured on the reference dump, over the object kinds this tool can address:
// 542 objects of 1537 have a first-by-order form with zero dynamic lists while
// another form of the same object carries some, and 874 lists sit in those other
// forms. The figures are the reason for the note and are deliberately not in it.

// TestNewFormStructureHandler_AutoChosenFormIsNamedWithTheAlternatives is the
// case the whole note exists for: the first form in order carries no list and
// another form of the same object does.
func TestNewFormStructureHandler_AutoChosenFormIsNamedWithTheAlternatives(t *testing.T) {
	srv := formHTTPServer(t, "ФормаВыбора", "Выбор валюты")
	dumpDir := t.TempDir()

	// Names chosen so the byte order is unambiguous: А < Б < Я. The FIRST one
	// carries no dynamic list, the last one does.
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "АФормаВыбора",
		formXMLWithTitle("Выбор валюты", "ПолеВыбора"))
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "БФормаЭлемента",
		formXMLWithTitle("Валюта", "Наименование"))
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "ЯФормаСписка", listsOnlyFormXML)

	result, err := callFormHandler(t, srv.URL, dumpDir, "Catalog", "Валюты", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	// POSITIVE CONTROL over the case: this really is the trap. The answer is
	// about the first form in order, and it really does carry no list section, so
	// without the note its silence is the misreading the note prevents.
	if !strings.Contains(text, "# Форма: АФормаВыбора") {
		t.Fatalf("control failed: the answer is not about the first form in order:\n%s", text)
	}
	if strings.Contains(text, "## Динамические списки формы") {
		t.Fatalf("control failed: the chosen form DOES carry a list section here, so this is "+
			"not the case the note is about:\n%s", text)
	}

	for _, want := range []string{
		"Имя формы не задано",
		"`АФормаВыбора`",
		"`БФормаЭлемента`",
		"`ЯФормаСписка`",
		"отсутствие раздела не означает",
		"`form_name`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer does not carry %q:\n%s", want, text)
		}
	}

	// The reference-dump figures are the REASON for the note, not content for the
	// caller: they say nothing about the dump in front of this server.
	for _, forbidden := range []string{"542", "874", "1537"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the answer quotes the reference-dump figure %q, which is not a fact about "+
				"the dump this server was pointed at:\n%s", forbidden, text)
		}
	}
}

// TestNewFormStructureHandler_ASingleFormIsNotAChoice keeps the note off the
// answer where there was nothing to choose.
//
// Measured: 372 of the 1537 objects with forms in the reference dump have
// exactly one, so a note on this path would be noise on roughly a quarter of
// every object answered without a form_name.
func TestNewFormStructureHandler_ASingleFormIsNotAChoice(t *testing.T) {
	srv := formHTTPServer(t, "ФормаСписка", "Список валют")
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "ФормаСписка", listsOnlyFormXML)

	result, err := callFormHandler(t, srv.URL, dumpDir, "Catalog", "Валюты", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if strings.Contains(text, "Имя формы не задано") {
		t.Errorf("an object with one form offered no choice, so nothing was chosen and the "+
			"note must not appear:\n%s", text)
	}
	// POSITIVE CONTROL: the dump really did answer, so the absence above is the
	// rule and not a call that never reached the dump leg.
	if !strings.Contains(text, "## Динамические списки формы") {
		t.Errorf("control failed: the dump leg did not contribute here:\n%s", text)
	}
}

// TestNewFormStructureHandler_ANamedFormIsNotAnAutoChoice keeps the note off the
// answer where the CALLER chose. Naming a form is the whole remedy the note
// advises, so repeating the advice to somebody who took it is noise, and the
// note's first clause («имя формы не задано») would be false besides.
func TestNewFormStructureHandler_ANamedFormIsNotAnAutoChoice(t *testing.T) {
	srv := formHTTPServer(t, "ФормаВыбора", "Выбор валюты")
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "АФормаВыбора",
		formXMLWithTitle("Выбор валюты", "ПолеВыбора"))
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "ЯФормаСписка", listsOnlyFormXML)

	result, err := callFormHandler(t, srv.URL, dumpDir, "Catalog", "Валюты", "ЯФормаСписка")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if strings.Contains(text, "Имя формы не задано") {
		t.Errorf("the caller named the form, so nothing was auto chosen:\n%s", text)
	}
	// POSITIVE CONTROL: the NAMED form really is the one that was read, so the
	// object really did have more than one form to choose between.
	//
	// The control is the list section and NOT the heading, and that is measured
	// rather than a preference: ЯФормаСписка supplies only a dynamic list, and
	// mergeDumpIntoForm keys its takeover on the three collections that travel
	// with the name, so the heading here stays the one the 1C service returned.
	// Only this form carries a list, so the section is proof of which file was
	// read.
	if !strings.Contains(text, "## Динамические списки формы") {
		t.Errorf("control failed: the named form was not the one read:\n%s", text)
	}
	if !strings.Contains(text, "# Форма: ФормаВыбора") {
		t.Errorf("control failed: the heading is not the 1C service's, so this answer did not "+
			"take the path this control describes:\n%s", text)
	}
}

// TestFormAutoChosenNote_BoundsTheNameList covers the shape the corpus forces.
// One object in the reference dump carries 243 forms, and 53 of 1537 carry more
// than ten, so the list is capped and the remainder is counted rather than
// dropped in silence.
func TestFormAutoChosenNote_BoundsTheNameList(t *testing.T) {
	others := make([]string, 0, 242)
	for i := range 242 {
		others = append(others, string(rune('А'+i%32))+"Форма"+strings.Repeat("х", 1+i%3))
	}
	note := formAutoChosenNote("АПерваяФорма", others)

	if strings.Count(note, "\n") != 1 || !strings.HasSuffix(note, "\n") {
		t.Errorf("the note is not one blockquote line: %q", note)
	}
	// The cap holds, and the remainder is stated.
	if !strings.Contains(note, "и ещё 232") {
		t.Errorf("the note does not count the names it did not list (242 others, cap %d):\n%s",
			maxOtherFormNames, note)
	}
	if got := strings.Count(note, "`"); got == 0 {
		t.Fatalf("control failed: no name was rendered as a code span at all:\n%s", note)
	}

	// A short list is printed whole, with no remainder clause.
	short := formAutoChosenNote("АПервая", []string{"БВторая", "ВТретья"})
	if strings.Contains(short, "и ещё") {
		t.Errorf("two other forms fit under the cap, so nothing is left over:\n%s", short)
	}
	for _, want := range []string{"`БВторая`", "`ВТретья`"} {
		if !strings.Contains(short, want) {
			t.Errorf("the note does not name %s:\n%s", want, short)
		}
	}
}

// TestFormAutoChosenNote_HostileFormNameCannotLeaveTheBlockquote covers a form
// name carrying markdown of its own.
//
// A blockquote is a LINE construct: the «> » marker binds one line, so a break
// inside a name ends the quote and everything after it becomes free markdown in
// an answer the model reads as this server's own words. The names come off the
// filesystem, which permits every byte but the separator and NUL.
//
// The fixture is synthetic and that is measured, not assumed: across the whole
// reference dump no form directory name carries a line break or a backtick, so
// there is no specimen in the corpus to draw on.
func TestFormAutoChosenNote_HostileFormNameCannotLeaveTheBlockquote(t *testing.T) {
	note := formAutoChosenNote(
		"Фор\nма```Один",
		[]string{"Втор\u2028ая", "Треть``я", "> # Заголовок"})

	if strings.Count(note, "\n") != 1 {
		t.Errorf("a name carrying a line break broke the blockquote into %d lines: %q",
			strings.Count(note, "\n")+1, note)
	}
	for _, r := range []string{"\r", "\u2028", "\u2029", "\v", "\f", "\u0085"} {
		if strings.Contains(note, r) {
			t.Errorf("the note carries the line-break rune %q verbatim: %q", r, note)
		}
	}
	// No line of the answer may START with a heading marker because of a name.
	if strings.HasPrefix(strings.TrimPrefix(note, "> "), "#") {
		t.Errorf("a heading was forged out of a form name: %q", note)
	}
	// POSITIVE CONTROL over the fixture: the payload really did carry a break and
	// a backtick run, so the containment above is doing work.
	if !strings.Contains("Фор\nма```Один", "\n") || !strings.Contains("Фор\nма```Один", "```") {
		t.Fatal("control failed: the fixture carries neither a break nor a backtick run")
	}
	// The name is still READABLE: the rule is containment, not deletion.
	if !strings.Contains(note, "Один") || !strings.Contains(note, "Треть") {
		t.Errorf("the names were dropped rather than contained: %q", note)
	}
}

// TestFormAutoChosenNote_CarriesNoDash keeps the house rule on the one new
// customer-facing sentence, with the control that rule needs: a no-dash scanner
// once ate the dashes out of its own control class and reported zero.
func TestFormAutoChosenNote_CarriesNoDash(t *testing.T) {
	dashes := []rune{'\u2014', '\u2013', '\u2012', '\u2015', '\u2212', '\u002D'}

	seen := false
	for _, r := range "форма \u2014 выбрана" {
		for _, d := range dashes {
			if r == d {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("control failed: the per-codepoint scan did not see U+2014 in a string that " +
			"carries it")
	}

	// The names are the CALLER's data, not our prose, so the scan runs over a
	// note built from names that carry none.
	note := formAutoChosenNote("ФормаОдин", []string{"ФормаДва", "ФормаТри"})
	for i, r := range note {
		for _, d := range dashes {
			if r == d {
				t.Errorf("the note carries %q at byte %d:\n%s", d, i, note)
			}
		}
	}
}

// TestFormFromDump_ReportsTheAutoChoiceOnlyWhenThereWasOne pins the producer,
// beneath the note, so the condition cannot drift from the sentence.
func TestFormFromDump_ReportsTheAutoChoiceOnlyWhenThereWasOne(t *testing.T) {
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Catalogs", "Одна", "ЕдинственнаяФорма", listsOnlyFormXML)
	writeDumpForm(t, dumpDir, "Catalogs", "Много", "АПервая", listsOnlyFormXML)
	writeDumpForm(t, dumpDir, "Catalogs", "Много", "БВторая", listsOnlyFormXML)
	writeDumpForm(t, dumpDir, "Catalogs", "Много", "ВТретья", listsOnlyFormXML)

	single, read, _, err := formFromDump(dumpDir, "Catalog", "Одна", "")
	if err != nil {
		t.Fatalf("single form: %v", err)
	}
	if single.Name != "ЕдинственнаяФорма" {
		t.Errorf("control failed: the single form was not read: %+v", single)
	}
	if read.autoChosenForm != "" || len(read.otherForms) != 0 {
		t.Errorf("one form is not a choice, got chosen %q others %v",
			read.autoChosenForm, read.otherForms)
	}

	many, read, _, err := formFromDump(dumpDir, "Catalog", "Много", "")
	if err != nil {
		t.Fatalf("several forms: %v", err)
	}
	if many.Name != "АПервая" {
		t.Errorf("the first form in order was not the one read: %+v", many)
	}
	if read.autoChosenForm != "АПервая" {
		t.Errorf("auto-chosen form: got %q, want %q", read.autoChosenForm, "АПервая")
	}
	// A SEQUENCE, in the order the pick was made from, and never the chosen one:
	// listing the answer's own form among the alternatives is how a reader is
	// told to ask again for what they already have.
	want := []string{"БВторая", "ВТретья"}
	if len(read.otherForms) != len(want) {
		t.Fatalf("other forms: got %v, want %v", read.otherForms, want)
	}
	for i := range want {
		if read.otherForms[i] != want[i] {
			t.Errorf("other form %d: got %q, want %q", i, read.otherForms[i], want[i])
		}
	}

	named, read, _, err := formFromDump(dumpDir, "Catalog", "Много", "ВТретья")
	if err != nil {
		t.Fatalf("named form: %v", err)
	}
	if named.Name != "ВТретья" {
		t.Errorf("control failed: the named form was not read: %+v", named)
	}
	if read.autoChosenForm != "" || len(read.otherForms) != 0 {
		t.Errorf("the caller chose, so nothing was auto chosen, got %q %v",
			read.autoChosenForm, read.otherForms)
	}
}

// TestFormAutoChosenNote_IsNotClaimedByTheOtherNotes is the negative half: the
// new sentence must not appear in an answer where no dump form was read at all.
func TestFormAutoChosenNote_IsNotClaimedByTheOtherNotes(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация")

	// No dump at all: the parameter cannot take effect and no form was chosen.
	result, err := callFormHandler(t, srv.URL, "", "Catalog", "Валюты", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text := resultText(t, result); strings.Contains(text, "Имя формы не задано") {
		t.Errorf("without --dump nothing is chosen from a dump:\n%s", text)
	}

	// The dump leg failed: no form was read, so no form was chosen either.
	dumpDir := unreadableDumpForm(t, "Catalogs", "Валюты", "ФормаСписка")
	result, err = callFormHandler(t, srv.URL, dumpDir, "Catalog", "Валюты", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if !strings.Contains(text, dumpNoteMarker) {
		t.Fatalf("control failed: the dump leg did not fail here:\n%s", text)
	}
	if strings.Contains(text, "Имя формы не задано") {
		t.Errorf("the dump gave no form, so none was chosen:\n%s", text)
	}
}

// TestSuppliesStructure_StillIgnoresTheAutoChoice guards the seam the note was
// added next to: the note is about which form was read and says nothing about
// what the body contains, so it must not become an input to the predicate the
// other notes are built from.
func TestSuppliesStructure_StillIgnoresTheAutoChoice(t *testing.T) {
	empty := listsOnlyDumpForm("Ф")
	if suppliesStructure(empty, nil) {
		t.Error("a form with nothing at all must not count as supplying structure")
	}
	if !suppliesStructure(&onec.FormStructure{
		Elements: []onec.FormElement{{Name: "Поле"}},
	}, nil) {
		t.Error("control failed: a form with an element supplies structure")
	}
}
