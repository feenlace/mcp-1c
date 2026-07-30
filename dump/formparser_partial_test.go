package dump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The parser tolerates a broken Form.xml by design: its token loop breaks on any
// decoder error and parseFormXMLData still returns success, so a dump that has
// always parsed "well enough" keeps working. The cost of that tolerance is that
// nothing downstream can tell a fully read file from one the decoder abandoned
// halfway. These tests pin the flag that records the difference, and pin the
// boundary of what it can and cannot see.

// truncatedAfterTwoFields is cut off in the middle of the third field's
// <DataPath>. The decoder stops there with a syntax error.
const truncatedAfterTwoFields = `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildItems>
    <InputField name="ПолеПервое" id="1">
      <DataPath>Объект.Первое</DataPath>
    </InputField>
    <InputField name="ПолеВторое" id="2">
      <DataPath>Объект.Второе</DataPath>
    </InputField>
    <InputField name="ПолеТретье" id="3">
      <DataPa`

// wellFormedTwoFields is the same shape, complete and closed.
const wellFormedTwoFields = `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildItems>
    <InputField name="ПолеПервое" id="1">
      <DataPath>Объект.Первое</DataPath>
    </InputField>
    <InputField name="ПолеВторое" id="2">
      <DataPath>Объект.Второе</DataPath>
    </InputField>
  </ChildItems>
</Form>`

// TestParseFormXMLData_TruncatedIsFlaggedButStillSucceeds pins both halves of
// the contract at once: the tolerance stays (no error, and the elements read
// before the cut are kept) and the stumble is now recorded.
func TestParseFormXMLData_TruncatedIsFlaggedButStillSucceeds(t *testing.T) {
	form, err := parseFormXMLData([]byte(truncatedAfterTwoFields))
	if err != nil {
		t.Fatalf("truncation must stay tolerated, not become an error: %v", err)
	}
	if !form.ParseIncomplete {
		t.Errorf("a decoder syntax error must set ParseIncomplete, got form %+v", form)
	}
	// Three, not two: the third field's own start tag was intact and only its
	// child was severed, so the element itself was already recorded. Measured
	// against this parser rather than assumed.
	if len(form.Elements) != 3 {
		t.Errorf("expected the 3 elements readable before the cut, got %d: %+v",
			len(form.Elements), form.Elements)
	}
}

// TestParseFormXMLData_WellFormedIsNotFlagged is the negative control. A flag
// that is always set carries no information and the test asserting it cannot
// fail.
func TestParseFormXMLData_WellFormedIsNotFlagged(t *testing.T) {
	form, err := parseFormXMLData([]byte(wellFormedTwoFields))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if form.ParseIncomplete {
		t.Errorf("a complete document must not be flagged, got form %+v", form)
	}
	if form.NoFormRoot {
		t.Errorf("a complete form must not be reported as holding no form, got form %+v", form)
	}
	if len(form.Elements) != 2 {
		t.Errorf("expected 2 elements, got %d: %+v", len(form.Elements), form.Elements)
	}
}

// TestParseFormXMLData_MismatchedTagIsFlagged covers the malformed case that is
// not a truncation: the file ends normally but a tag is closed by the wrong
// name, which the decoder reports mid document.
func TestParseFormXMLData_MismatchedTagIsFlagged(t *testing.T) {
	const mismatched = `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildItems>
    <InputField name="ПолеПервое" id="1"></WrongTag>
  </ChildItems>
</Form>`

	form, err := parseFormXMLData([]byte(mismatched))
	if err != nil {
		t.Fatalf("a mismatched tag must stay tolerated, not become an error: %v", err)
	}
	if !form.ParseIncomplete {
		t.Errorf("a mismatched closing tag must set ParseIncomplete, got form %+v", form)
	}
}

// TestParseFormXMLData_ErrorBelowTheTopLoopIsStillSeen guards the mechanism
// rather than the symptom. Every nested reader in this file swallows its own
// decoder error and returns what it has, so the top level loop only learns about
// the failure by calling Token() again. That works because the decoder keeps
// returning the syntax error on every later call instead of falling back to EOF.
// If that ever stopped holding, the flag would silently go dark for any breakage
// below the root, which is where real breakage lives.
func TestParseFormXMLData_ErrorBelowTheTopLoopIsStillSeen(t *testing.T) {
	// The break is inside <Commands>, consumed by parseCommandsSection, which
	// returns on error without telling anyone.
	const brokenDeepInside = `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildItems>
    <InputField name="ПолеПервое" id="1"/>
  </ChildItems>
  <Commands>
    <Command name="Провести" id="1">
      <Action>Провести</Actio`

	form, err := parseFormXMLData([]byte(brokenDeepInside))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !form.ParseIncomplete {
		t.Errorf("a syntax error swallowed by a nested reader must still reach the flag, got %+v", form)
	}
	if len(form.Elements) != 1 {
		t.Errorf("the element read before the break must survive, got %+v", form.Elements)
	}
}

// TestParseFormXMLData_CleanEndWithoutFormRootIsFlagged pins the class that
// ParseIncomplete structurally cannot see. Every input below is read to a normal
// io.EOF, so the syntax-error flag correctly stays false, and every one of them
// is useless as a form. Before NoFormRoot they were returned as an empty but
// unremarkable FormInfo and nothing downstream could tell them apart from a form
// that genuinely declares nothing.
//
// Both flags are asserted on every case, in both directions. Asserting only the
// new one would let a change that fires BOTH pass here while emitting two notes
// that contradict each other in the response body.
func TestParseFormXMLData_CleanEndWithoutFormRootIsFlagged(t *testing.T) {
	cases := map[string]string{
		"empty file":            ``,
		"plain text, not xml":   `this file is not xml at all`,
		"valid xml, wrong root": `<?xml version="1.0" encoding="UTF-8"?><NotAForm><Something/></NotAForm>`,
		"whitespace only":       "   \n\t  ",
		"xml declaration and a comment only": `<?xml version="1.0" encoding="UTF-8"?>` +
			`<!-- выгрузка не дописана -->`,
		"utf-8 bom then plain text": "\xef\xbb\xbfthis file is not xml at all",
	}

	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			form, err := parseFormXMLData([]byte(doc))
			if err != nil {
				t.Fatalf("a formless file must stay tolerated, not become an error: %v", err)
			}
			if !form.NoFormRoot {
				t.Errorf("the decoder reached a clean EOF without ever entering <Form>, "+
					"so NoFormRoot must be set: %+v", form)
			}
			if form.ParseIncomplete {
				t.Errorf("input ends on a clean EOF, so ParseIncomplete must stay false; "+
					"setting both would put two contradicting notes in one answer: %+v", form)
			}
			// The guarantee the note wording leans on: nothing is recorded outside
			// <Form>, so a formless file yields nothing at all.
			if form.Title != "" || len(form.Elements) != 0 || len(form.Commands) != 0 || len(form.Handlers) != 0 {
				t.Errorf("a file with no <Form> cannot yield form contents: %+v", form)
			}
		})
	}
}

// TestParseFormXMLData_UndefinedNamespacePrefixIsAParsedForm corrects a claim
// that was carried into this file's earlier version: that an undefined namespace
// prefix belongs to the silent class above. It does not, and this pins why, so
// the mistake is not made a third time.
//
// Go's xml.Decoder does not reject an undefined prefix. It hands back
// Name{Space:"zz", Local:"InputField"}, and this parser matches on Local only
// (see the note on parseFormXMLData), so the element is recognised and RECORDED.
// The file is a form, it parses, and it yields its element. Neither flag may
// fire on it: NoFormRoot must not, because <Form> was entered, and a note
// claiming this file holds no form would be false while the element sits in the
// body above it.
func TestParseFormXMLData_UndefinedNamespacePrefixIsAParsedForm(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>` +
		`<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"><ChildItems><zz:InputField name="A"/></ChildItems></Form>`

	form, err := parseFormXMLData([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(form.Elements) != 1 || form.Elements[0].Name != "A" {
		t.Fatalf("measured against this parser: the prefixed element is recorded by its "+
			"local name, got %+v", form.Elements)
	}
	if form.NoFormRoot {
		t.Errorf("<Form> was entered, so NoFormRoot must stay false: %+v", form)
	}
	if form.ParseIncomplete {
		t.Errorf("the document ends cleanly, so ParseIncomplete must stay false: %+v", form)
	}
}

// TestParseFormXMLData_ValidEmptyFormIsNotFlagged is THE boundary that decides
// whether the new signal means anything. A <Form> that declares no elements is a
// real, correct, completely read form, and it is indistinguishable from the
// formless files above by every other measure: same clean EOF, same zero
// elements, same empty title. Only "did we enter <Form>" separates them. A
// signal keyed on emptiness instead would put a "this file holds no form" note
// on a healthy dump.
func TestParseFormXMLData_ValidEmptyFormIsNotFlagged(t *testing.T) {
	cases := map[string]string{
		"empty form, paired tags": `<?xml version="1.0" encoding="UTF-8"?>` +
			`<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21"></Form>`,
		"empty form, self closing": `<?xml version="1.0" encoding="UTF-8"?>` +
			`<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21"/>`,
		"form with an empty ChildItems": `<?xml version="1.0" encoding="UTF-8"?>` +
			`<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"><ChildItems></ChildItems></Form>`,
	}

	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			form, err := parseFormXMLData([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(form.Elements) != 0 {
				t.Fatalf("precondition: this fixture must yield no elements, got %+v", form.Elements)
			}
			if form.NoFormRoot {
				t.Errorf("a real form that declares nothing must NOT be reported as "+
					"holding no form: %+v", form)
			}
			if form.ParseIncomplete {
				t.Errorf("a complete document must not be flagged: %+v", form)
			}
		})
	}
}

// TestParseFormXMLData_NestedFormIsEntered pins the measured scope of the
// signal, because the field name invites a stricter reading than the code
// delivers. The loop takes the FIRST <Form> it sees at any depth, so a <Form>
// wrapped in another element is entered and its contents are parsed. The flag
// therefore means "no <Form> was entered anywhere", not "the document root was
// not <Form>".
func TestParseFormXMLData_NestedFormIsEntered(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>` +
		`<Wrapper><Form><ChildItems><InputField name="Вложенное"/></ChildItems></Form></Wrapper>`

	form, err := parseFormXMLData([]byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(form.Elements) != 1 {
		t.Fatalf("measured: a nested <Form> is parsed, got %+v", form.Elements)
	}
	if form.NoFormRoot {
		t.Errorf("a <Form> below the document root is still entered, so the flag must "+
			"stay false: %+v", form)
	}
}

// TestParseFormXMLData_AbandonedReadIsNotCalledFormless is the exclusivity
// guard, and the reason NoFormRoot is set only on the io.EOF branch.
//
// This document breaks before any <Form> appears. Reporting it as "read in full,
// contains no form" would be a claim the parser has no standing to make: the
// read was abandoned, so a <Form> further along would never have been reached.
// It is a partial read and nothing more. If both flags ever fire together the
// response gains two notes, one saying the file was read whole and the other
// saying it was not, which is the exact contradiction that has already shipped
// twice in this codebase.
func TestParseFormXMLData_AbandonedReadIsNotCalledFormless(t *testing.T) {
	cases := map[string]string{
		"broken before any Form appears": `<?xml version="1.0" encoding="UTF-8"?><NotAForm><Something`,
		"truncated inside the form":      truncatedAfterTwoFields,
		"mismatched tag before the form": `<?xml version="1.0" encoding="UTF-8"?><Outer></WrongTag>`,
	}

	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			form, err := parseFormXMLData([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !form.ParseIncomplete {
				t.Fatalf("precondition: this fixture must stop on a syntax error: %+v", form)
			}
			if form.NoFormRoot {
				t.Errorf("the read was abandoned, so the file cannot be reported as "+
					"containing no form; the two flags must never both be set: %+v", form)
			}
		})
	}
}

// TestParseFormXML_TruncatedFileOnDiskIsFlagged drives the exported file level
// entry point, the one tools/form.go actually calls, so the flag is proven to
// survive the ParseFormXML wrapper and not just the in memory helper.
func TestParseFormXML_TruncatedFileOnDiskIsFlagged(t *testing.T) {
	dir := t.TempDir()
	formDir := filepath.Join(dir, "Documents", "ТестДок", "Forms", "ФормаДокумента", "Ext")
	if err := os.MkdirAll(formDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(formDir, "Form.xml")
	if err := os.WriteFile(path, []byte(truncatedAfterTwoFields), 0o644); err != nil {
		t.Fatal(err)
	}

	form, err := ParseFormXML(path)
	if err != nil {
		t.Fatalf("truncation must stay tolerated at the file level too: %v", err)
	}
	if !form.ParseIncomplete {
		t.Errorf("ParseFormXML must carry the flag out of parseFormXMLData, got %+v", form)
	}
}

// TestParseFormXML_EmptyFileOnDiskIsFlaggedAsNoForm drives the exported file
// level entry point for the new flag, the one tools/form.go actually calls, so
// it is proven to survive the ParseFormXML wrapper and not just the in memory
// helper. An empty Form.xml is a plain regular file, so nothing upstream refuses
// it: it is opened, read, parsed and reported as a success.
func TestParseFormXML_EmptyFileOnDiskIsFlaggedAsNoForm(t *testing.T) {
	dir := t.TempDir()
	formDir := filepath.Join(dir, "Documents", "ТестДок", "Forms", "ФормаДокумента", "Ext")
	if err := os.MkdirAll(formDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(formDir, "Form.xml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	form, err := ParseFormXML(path)
	if err != nil {
		t.Fatalf("an empty Form.xml must stay tolerated at the file level too: %v", err)
	}
	if !form.NoFormRoot {
		t.Errorf("ParseFormXML must carry NoFormRoot out of parseFormXMLData, got %+v", form)
	}
	if form.ParseIncomplete {
		t.Errorf("an empty file ends on a clean EOF, so ParseIncomplete must stay false: %+v", form)
	}
}

// TestParseFormXML_ShippedFixturesAreNotFlagged is the corpus level negative
// control: every fixture this package already parses must stay unflagged. A
// false positive here would put a "read only partially" note on healthy dumps,
// which is the failure mode that makes a note worthless.
func TestParseFormXML_ShippedFixturesAreNotFlagged(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".xml") {
			continue
		}
		checked++
		form, perr := ParseFormXML(filepath.Join("testdata", e.Name()))
		if perr != nil {
			t.Errorf("fixture %s failed to parse: %v", e.Name(), perr)
			continue
		}
		if form.ParseIncomplete {
			t.Errorf("fixture %s is a healthy dump file and must not be flagged", e.Name())
		}
		if form.NoFormRoot {
			t.Errorf("fixture %s is a real form file and must not be reported as holding "+
				"no form", e.Name())
		}
	}
	if checked == 0 {
		t.Fatal("POSITIVE CONTROL FAILED: no .xml fixtures were checked, this test proves nothing")
	}
	t.Logf("checked %d fixtures", checked)
}
