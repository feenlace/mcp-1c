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

// TestParseFormXMLData_CleanEndIsNeverFlagged pins the boundary of what this
// flag can see, so nobody later reads it as "the file was good".
//
// All three inputs below are useless as form files, yet the decoder finishes
// each of them on a normal io.EOF rather than a syntax error, so none of them is
// flagged. They stay silent, and closing that is a separate change: it needs a
// different signal (the root <Form> element was never entered), not this one.
// Recorded as a test so the gap is a documented property instead of a surprise.
func TestParseFormXMLData_CleanEndIsNeverFlagged(t *testing.T) {
	cases := map[string]string{
		"empty file":            ``,
		"plain text, not xml":   `this file is not xml at all`,
		"valid xml, wrong root": `<?xml version="1.0" encoding="UTF-8"?><NotAForm><Something/></NotAForm>`,
		"undefined namespace prefix": `<?xml version="1.0" encoding="UTF-8"?>` +
			`<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"><ChildItems><zz:InputField name="A"/></ChildItems></Form>`,
	}

	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			form, err := parseFormXMLData([]byte(doc))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if form.ParseIncomplete {
				t.Errorf("input ends on a clean EOF, so ParseIncomplete must stay false; "+
					"if this now fires the flag has changed meaning: %+v", form)
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
	}
	if checked == 0 {
		t.Fatal("POSITIVE CONTROL FAILED: no .xml fixtures were checked, this test proves nothing")
	}
	t.Logf("checked %d fixtures", checked)
}
