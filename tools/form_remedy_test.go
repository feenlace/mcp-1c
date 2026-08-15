package tools

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
)

// A 404 under the form heading used to render as a bare «Object not found» with
// nothing to do about it. The remedy branches in renderStatusError covered the
// event log and the query and nothing else, so the one endpoint that answers 404
// for a whole CLASS of metadata answered it with no way forward.
//
// The class is real and is the customer's case: the extension resolves an object
// through a map of applied kinds, and no common kind is in it. The same file
// LISTS common forms in the metadata tree, so the model sees an object it cannot
// read, and the 404 said nothing about why.

// formStatusError builds the 404 the extension itself produces on this endpoint.
func formStatusError(code int) *onec.StatusError {
	return &onec.StatusError{
		StatusCode: code,
		BodyKind:   onec.BodyKindExtension,
		Detail:     "Object not found",
	}
}

// TestFormRemedy_AppearsOnlyOnAFormNotFound covers where the remedy fires and,
// just as much, where it must not. A remedy attached to the wrong refusal is
// worse than none: it asserts a cause the answer cannot support.
func TestFormRemedy_AppearsOnlyOnAFormNotFound(t *testing.T) {
	marker := remedyFormNotFoundMarker()

	body := renderFailure(headingForm, formStatusError(404))
	if !strings.Contains(body, marker) {
		t.Errorf("a 404 under the form heading carries no remedy:\n%s", body)
	}

	// NEGATIVE CONTROLS. Each is a refusal the remedy would be false about.
	for _, tc := range []struct {
		name    string
		heading string
		code    int
	}{
		{"the event log, whose 403 has its own two causes", headingEventLog, 403},
		{"a query rejection, which has its own remedy", headingQuery, 404},
		{"the metadata tree", headingMetadata, 404},
		{"a form endpoint answering 500, which is not a missing object", headingForm, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := renderFailure(tc.heading, formStatusError(tc.code))
			if strings.Contains(body, marker) {
				t.Errorf("the form remedy reached %s (status %d):\n%s", tc.name, tc.code, body)
			}
		})
	}
}

// TestFormRemedy_NamesNoToolRegisteredInThisEdition is a guard on the text, and
// the reason is not style.
//
// This package is VENDORED by a build that registers a different set of tools.
// There, advice naming a tool of this edition names something the caller does
// not have, and the machinery that rewrites such sentences works from a
// hand-written table of whole phrases; a sentence nobody added to that table
// travels through unchanged. So the safe remedy is one that names no tool at
// all, and this is where that is enforced, in the repository the sentence is
// written in.
func TestFormRemedy_NamesNoToolRegisteredInThisEdition(t *testing.T) {
	remedy := remedyFormNotFound

	names := make([]string, 0, len(shippedToolDefs()))
	for _, tool := range shippedToolDefs() {
		names = append(names, tool.Name)
	}
	if len(names) == 0 {
		t.Fatal("control failed: no tool names were collected, so this guard checks nothing")
	}
	t.Logf("checked against %d registered tool names: %v", len(names), names)

	for _, name := range names {
		if strings.Contains(remedy, name) {
			t.Errorf("the form remedy names the tool %q. Advice that names a tool of this "+
				"edition is wrong wherever this package is vendored into an edition that "+
				"does not register it:\n%s", name, remedy)
		}
	}

	// POSITIVE CONTROL over the guard itself: the same scan run against a
	// sentence that DOES name a tool has to fire, or the zero above is measuring
	// a scan that cannot see anything.
	planted := remedy + "\nВызовите get_form_structure ещё раз.\n"
	fired := false
	for _, name := range names {
		if strings.Contains(planted, name) {
			fired = true
		}
	}
	if !fired {
		t.Fatal("control failed: the scan did not find a tool name in a sentence that " +
			"carries one, so the check above proves nothing")
	}
}

// TestFormRemedy_CarriesNoDash scans per codepoint, with the control the house
// rule needs: a no-dash scanner once ate the dashes out of its own control class
// and reported zero on a file full of them.
func TestFormRemedy_CarriesNoDash(t *testing.T) {
	dashes := []rune{'\u2014', '\u2013', '\u2012', '\u2015', '\u2212'}

	seen := false
	for _, r := range "форма \u2014 не найдена" {
		for _, d := range dashes {
			if r == d {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("control failed: the per-codepoint scan did not see U+2014 in a string that carries it")
	}

	// EVERY new customer-facing string, not only the remedy. The rendered
	// section is included as RENDERED OUTPUT rather than as literals, because
	// its wording lives in format strings that no constant names, and a scan
	// over constants alone would be blind to exactly those.
	texts := map[string]string{
		"remedyFormNotFound": remedyFormNotFound,
		"lineDumpLegReason":  lineDumpLegReason,
		"tool description":   FormStructureTool().Description,
		"rendered dynamic list section": formatFormStructure(
			&onec.FormStructure{Name: "Ф"},
			[]dump.FormDynamicList{
				{Name: "Список", ManualQuery: true, MainTable: "Catalog.Валюты"},
				{Name: "Второй", ManualQuery: false},
			}),
	}
	for name, reason := range dumpLegReasonText {
		texts[fmt.Sprintf("dumpLegReasonText[%d]", name)] = reason
	}
	// POSITIVE CONTROL over the corpus being scanned: the rendered section really
	// is in it, so a clean result is about the text and not about an empty map.
	if !strings.Contains(texts["rendered dynamic list section"], "Динамические списки") {
		t.Fatal("control failed: the rendered section is not in the scanned set")
	}
	for name, text := range texts {
		for i, r := range text {
			for _, d := range dashes {
				if r == d {
					t.Errorf("%s carries %q at byte %d:\n%s", name, d, i, text)
				}
			}
		}
	}
}

// TestDumpLegReasonsAreAClosedSet pins the vocabulary the dump leg may report.
//
// It is closed on purpose: an open string field would carry whatever the lower
// layer's message happened to say, and those messages are exactly what must not
// travel, because one of them used to be built around the absolute path it
// failed on.
func TestDumpLegReasonsAreAClosedSet(t *testing.T) {
	want := map[dumpLegReason]string{
		dumpReasonUnknownType:      "unknown_type",
		dumpReasonNotFound:         "not_found",
		dumpReasonNotRegular:       "not_regular",
		dumpReasonUnreadable:       "unreadable",
		dumpReasonTraversalRefused: "traversal_refused",
		dumpReasonTooLarge:         "too_large",
	}
	if len(dumpLegReasonText) != len(want) {
		t.Fatalf("the reason set holds %d values, want %d. It is closed by design: "+
			"%v", len(dumpLegReasonText), len(want), dumpLegReasonText)
	}
	for reason, code := range want {
		if reason.code() != code {
			t.Errorf("reason %d has code %q, want %q", reason, reason.code(), code)
		}
		if strings.TrimSpace(dumpLegReasonText[reason]) == "" {
			t.Errorf("reason %q has no text for the reader", code)
		}
	}
	// A value outside the set has no code and no text, so a sixth reason cannot
	// be smuggled in as an empty string.
	var outside dumpLegReason = 99
	if outside.code() != "" || dumpLegReasonText[outside] != "" {
		t.Errorf("a value outside the set answered code %q text %q",
			outside.code(), dumpLegReasonText[outside])
	}
}

// TestClassifyDumpLegFailure_MapsEveryCause drives the classifier from the
// SENTINELS the dump package exports, not from message text. Text is what must
// not be trusted here: it is the channel the absolute path used to travel on.
func TestClassifyDumpLegFailure_MapsEveryCause(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want dumpLegReason
	}{
		{"unknown object type", fmt.Errorf("wrapped: %w", dump.ErrFormUnknownObjectType), dumpReasonUnknownType},
		{"name guard", fmt.Errorf("wrapped: %w", dump.ErrFormObjectNameRejected), dumpReasonTraversalRefused},
		{"forms directory unreadable", fmt.Errorf("wrapped: %w", dump.ErrFormsDirUnreadable), dumpReasonUnreadable},
		{"form file is not regular", fmt.Errorf("wrapped: %w", dump.ErrFormXMLNotRegular), dumpReasonNotRegular},
		// NOT dumpReasonUnreadable, which is where it used to land. That code's
		// text advises checking permissions on the dump directory and the
		// completeness of the dump, and an over-size file is present, permitted
		// and complete: the advice was wrong on every clause.
		{"form file over the read limit", fmt.Errorf("wrapped: %w", dump.ErrFormXMLTooLarge), dumpReasonTooLarge},
		// A name the guard refuses before any filesystem access, whatever made it
		// refuse. It is the same sentinel and the same code for all three ways in,
		// which is why that code's text names all three.
		{"a name carrying a NUL", fmt.Errorf("wrapped: %w", dump.ErrFormObjectNameRejected), dumpReasonTraversalRefused},
		{"form not present in the dump", &formNotInDumpError{requested: "Ф", available: "А, Б"}, dumpReasonNotFound},
		{"anything else", errors.New("что то ещё"), dumpReasonUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDumpLegFailure(tc.err); got != tc.want {
				t.Errorf("classified as %q, want %q", got.code(), tc.want.code())
			}
		})
	}
}

// TestFormFailure_CarriesTheReasonCodeAndNotTheLowerText is the pair the typed
// field exists for: the CLASS of the dump failure reaches the answer, and the
// message the lower layer built does not.
func TestFormFailure_CarriesTheReasonCodeAndNotTheLowerText(t *testing.T) {
	const secret = "/Users/кто-то/тайный/каталог/выгрузки"
	lower := fmt.Errorf("parsing form XML %q: %w", secret, dump.ErrFormXMLNotRegular)

	// POSITIVE CONTROL: the lower error really does carry the path, so the
	// absence below is the boundary working and not an empty error.
	if !strings.Contains(lower.Error(), secret) {
		t.Fatal("control failed: the planted lower-layer error carries no path to leak")
	}

	err := fmt.Errorf("fetching form structure from 1C: %w", formStatusError(404))
	body := renderFailure(headingForm, withDumpLegReason(err, classifyDumpLegFailure(lower)))

	if strings.Contains(body, secret) {
		t.Errorf("the absolute path from the dump leg reached the answer:\n%s", body)
	}
	if !strings.Contains(body, dumpReasonNotRegular.code()) {
		t.Errorf("the answer does not carry the reason code %q:\n%s", dumpReasonNotRegular.code(), body)
	}
}

// TestFormFailure_HasNoAbsolutePathFromTheDumpLeg is the end-to-end half, driven
// through the handler rather than the renderer, because the wrap that carried
// the path was in the handler's own call chain.
func TestFormFailure_HasNoAbsolutePathFromTheDumpLeg(t *testing.T) {
	srv := formHTTPServer(t, "ФормаСписка", "Список")
	dumpDir := t.TempDir()

	// A form_name the object does not have is the one dump failure that is
	// fatal, so it renders through the failure path with both legs' facts.
	writeDumpForm(t, dumpDir, "Catalogs", "Валюты", "ФормаСписка", listsOnlyFormXML)

	// The refusal reaches the caller as a result carrying IsError rather than as
	// a protocol error, so it is read the way the package's other failure tests
	// read it.
	result, err := callFormHandler(t, srv.URL, dumpDir, "Catalog", "Валюты", "НетТакойФормы")
	text := failureText(t, result, err)

	// POSITIVE CONTROL over the case itself: this really is the refusal, and it
	// really did enumerate the object's forms, so the path check below is being
	// run against the answer it is about.
	if !strings.Contains(text, "ФормаСписка") {
		t.Fatalf("control failed: this is not the unknown-form refusal:\n%s", text)
	}
	if strings.Contains(text, dumpDir) {
		t.Errorf("the failure carries the absolute dump path %q:\n%s", dumpDir, text)
	}

	// POSITIVE CONTROL over the detector: it does see the path when one is there.
	if !strings.Contains(fmt.Sprintf("прочитан %s/Ext/Form.xml", dumpDir), dumpDir) {
		t.Fatal("control failed: the detector cannot see the dump path in a string built " +
			"around it, so the assertion above proves nothing")
	}
}

// remedyFormNotFoundMarker is the first line of the remedy, used to detect it
// without pinning the whole text.
func remedyFormNotFoundMarker() string {
	first, _, _ := strings.Cut(remedyFormNotFound, "\n")
	if strings.TrimSpace(first) == "" {
		panic("remedyFormNotFound has no first line to match on")
	}
	return first
}
