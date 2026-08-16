package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// A REMEDY POINTING AT THE WRONG THING IS WORSE THAN NONE, and two causes were
// pointed there.
//
// The `unreadable` reason tells the reader to check permissions on the dump
// directory and the completeness of the dump. That is right for a read that
// failed. It was also what a caller got for a form file OVER THE SIZE CEILING,
// which is present, permitted and complete, and for an object_name carrying a
// NUL, which never reached a file at all.
//
// Both now have an accurate answer: the size refusal has its own code and its own
// text, and the name refusal is caught on the name, before the filesystem, with a
// text that names all three ways a name is refused.

// nulObjectName is built at run time rather than written as a literal, because a
// NUL byte in a Go source file is a compile error («illegal character NUL»).
func nulObjectName() string { return "Валюты" + string(rune(0)) + "x" }

// TestObjectNameWithNulArrivesFromToolInput is the premise, measured rather than
// assumed: this is not a value only a test can construct.
//
// object_name is decoded from the JSON arguments, and JSON spells the byte as an
// escape, so a caller can send it over the wire in a perfectly well formed
// request.
func TestObjectNameWithNulArrivesFromToolInput(t *testing.T) {
	var input formInput
	body := `{"object_type":"Catalog","object_name":"Валюты\u0000x"}`
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatalf("a request carrying an escaped NUL is well formed JSON: %v", err)
	}
	if !strings.ContainsRune(input.ObjectName, 0) {
		t.Fatalf("control failed: the decoded name carries no NUL, so the case this file is "+
			"about cannot arrive: %q", input.ObjectName)
	}
	if input.ObjectName != nulObjectName() {
		t.Errorf("decoded %q, expected %q", input.ObjectName, nulObjectName())
	}
}

// TestNulObjectNameIsRefusedOnTheNameForBothFormShapes pins the fix at the dump
// layer, on BOTH path shapes, because before it the two behaved differently and
// both behaved wrongly.
//
// Measured before the guard existed, by calling the pre-guard code path
// directly, on the fixture THIS TEST builds: an object form came back as
// ErrFormsDirUnreadable, which the caller renders as advice about directory
// permissions; a COMMON form came back as no error and an empty map, which the
// caller renders as «this object has no forms in the dump».
//
// THAT ASYMMETRY IS NOT OBJECT FORM VERSUS COMMON FORM, it is an artefact of
// this fixture, which builds Catalogs/ and never creates CommonForms/ at all:
// the walk fails on whichever top-level directory is missing BEFORE it ever
// reaches the NUL-carrying component, and it is CommonForms/ that is missing
// here. On the reference dump, which carries 386 CommonForms directories, the
// pre-guard common-form case would ALSO have come back ErrFormsDirUnreadable.
// Neither outcome is right for a name no filesystem can hold in the first
// place; the guard below removes both.
func TestNulObjectNameIsRefusedOnTheNameForBothFormShapes(t *testing.T) {
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "ФормаСписка", listsOnlyFormXML)

	// POSITIVE CONTROL: the same call with a clean name finds the form, so the
	// refusals below are about the NUL and not about an empty dump.
	if forms, err := dump.FindFormFiles(dumpDir, "Catalog", "Валюты"); err != nil || len(forms) != 1 {
		t.Fatalf("control failed: the clean name found %d forms, err %v", len(forms), err)
	}

	for _, tc := range []struct{ name, objectType string }{
		{"object form", "Catalog"},
		{"common form", "CommonForm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forms, err := dump.FindFormFiles(dumpDir, tc.objectType, nulObjectName())
			if err == nil {
				t.Fatalf("a name no filesystem can hold must be refused, got %v", forms)
			}
			if got := classifyDumpLegFailure(err); got != dumpReasonTraversalRefused {
				t.Errorf("classified as %q, want %q: %v", got.code(),
					dumpReasonTraversalRefused.code(), err)
			}
			// The refusal must not carry the control byte onward into a log line.
			if strings.ContainsRune(err.Error(), 0) {
				t.Errorf("the refusal repeats the NUL back into its own message: %q", err.Error())
			}
		})
	}
}

// TestNulObjectNameAnswerAdvisesTheName drives the whole handler and reads the
// answer, because the code is only half the fix: what the caller acts on is the
// sentence.
//
// BOTH LEGS FAIL HERE, and that was measured rather than expected. A NUL in the
// name also stops the 1C leg before any request goes out: the endpoint is
// /form/{type}/{name}, and net/http refuses to build a request around a URL
// carrying a control byte. So the answer is the both-legs-failed render, and the
// dump leg's cause reaches it as a code from the closed vocabulary.
func TestNulObjectNameAnswerAdvisesTheName(t *testing.T) {
	srv := formHTTPServer(t, "ФормаСписка", "Список валют")
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "ФормаСписка", listsOnlyFormXML)

	result, err := callFormHandler(t, srv.URL, dumpDir, "Catalog", nulObjectName(), "")
	text := failureText(t, result, err)

	// POSITIVE CONTROL: the dump leg really did refuse, and refused on the NAME.
	if !strings.Contains(text, dumpReasonTraversalRefused.code()) {
		t.Fatalf("control failed: the dump leg did not report a name refusal:\n%s", text)
	}
	// The advice the caller reads points at the name, which is where the fault is.
	if !strings.Contains(text, "object_name") {
		t.Errorf("the answer does not tell the caller which argument to fix:\n%s", text)
	}
	// And NOT at the dump directory's permissions, which is where the old code
	// sent this caller.
	if strings.Contains(text, dumpLegReasonText[dumpReasonUnreadable]) {
		t.Errorf("the answer still carries the unreadable remedy, which advises checking "+
			"permissions on a directory this call never reached:\n%s", text)
	}
	if strings.ContainsRune(text, 0) {
		t.Errorf("the answer carries the NUL from the request back to the caller:\n%q", text)
	}
}

// TestDumpLegReasonTexts_AdviseTheirOwnCause is the guard on the pairing itself.
// Each of the two reworked reasons has to say the thing that is true of it and
// must not say the thing that is true of a different one.
func TestDumpLegReasonTexts_AdviseTheirOwnCause(t *testing.T) {
	tooLarge := dumpLegReasonText[dumpReasonTooLarge]
	unreadable := dumpLegReasonText[dumpReasonUnreadable]
	nameRefused := dumpLegReasonText[dumpReasonTraversalRefused]

	// POSITIVE CONTROL: the wrong advice really is the text of the reason these
	// two used to be given, so «does not contain it» is a finding.
	if !strings.Contains(unreadable, "права") {
		t.Fatal("control failed: the unreadable text no longer advises checking permissions, " +
			"so the checks below are comparing against nothing")
	}

	if strings.Contains(tooLarge, "Проверьте права") {
		t.Errorf("the over-size text advises checking permissions, and an over-size file is "+
			"present and permitted:\n%s", tooLarge)
	}
	for _, want := range []string{"размер", "не прочитан"} {
		if !strings.Contains(tooLarge, want) {
			t.Errorf("the over-size text does not name %q:\n%s", want, tooLarge)
		}
	}

	// dump.ErrFormObjectNameRejected classifies THREE routes at the dump
	// package's own level (empty, a NUL byte, path-traversal characters), but
	// this text names only the TWO reachable from this tool: an empty
	// object_name is rejected by NewFormStructureHandler itself, before
	// formFromDump - the only caller of dump.FindFormFiles - is ever reached,
	// so "empty" can never be why a caller of this tool sees this text. A
	// prior version of this test asserted "пустое" here on the premise that
	// all three routes reach this code; TestDumpReasonTraversalRefusedNamesOnlyReachableCauses
	// in form_remedy_test.go grounds why that premise is false and asserts
	// the negative this positive list now implies.
	for _, want := range []string{"разделител", "недопустимый в имени файла", "object_name"} {
		if !strings.Contains(nameRefused, want) {
			t.Errorf("the name refusal text does not cover %q:\n%s", want, nameRefused)
		}
	}
}

// TestDumpLegReasonTexts_CarryNoDash keeps the house rule on every reason text,
// with the control the rule needs: a no-dash scanner once ate the dashes out of
// its own control class and reported zero on a file full of them.
//
// TWO RULES, BECAUSE THE HOUSE RULE IS ABOUT A DASH AND NOT ABOUT A CHARACTER. A
// typographic dash is refused wherever it appears. A HYPHEN is refused only where
// it stands in FOR a dash, which is between spaces: the same codepoint spells the
// flag `--dump`, which several of these texts name and must keep naming. A scan
// that refused the codepoint outright would be demanding that the advice stop
// naming the flag it advises setting.
func TestDumpLegReasonTexts_CarryNoDash(t *testing.T) {
	dashes := []rune{'\u2014', '\u2013', '\u2012', '\u2015', '\u2212'}
	const hyphenAsDash = " - "

	// POSITIVE CONTROLS over both scans, on strings that carry what each looks
	// for. A zero from a scanner nobody has watched fire measures the scanner.
	seen := false
	for _, r := range "файл \u2014 велик" {
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
	if !strings.Contains("имя - это имя", hyphenAsDash) {
		t.Fatal("control failed: the hyphen scan did not see a spaced hyphen in a string that " +
			"carries one")
	}
	// And the negative control that makes the second rule worth having: the flag
	// spelling must NOT be caught, or the rule would forbid the advice.
	if strings.Contains("указанной в `--dump`", hyphenAsDash) {
		t.Fatal("control failed: the hyphen scan caught the flag spelling, so it is the wrong " +
			"rule")
	}

	if len(dumpLegReasonText) == 0 {
		t.Fatal("control failed: there are no reason texts to scan")
	}
	for reason, text := range dumpLegReasonText {
		for i, r := range text {
			for _, d := range dashes {
				if r == d {
					t.Errorf("dumpLegReasonText[%s] carries %q at byte %d:\n%s",
						reason.code(), d, i, text)
				}
			}
		}
		if strings.Contains(text, hyphenAsDash) {
			t.Errorf("dumpLegReasonText[%s] uses a hyphen where a dash would go:\n%s",
				reason.code(), text)
		}
	}
}
