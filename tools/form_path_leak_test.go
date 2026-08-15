package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// THE DUMP ROOT MUST NOT REACH THE ANSWER, AND UNTIL THIS FILE EXISTED NOTHING IN
// THIS PACKAGE MEASURED THAT.
//
// The removal was done in three places at once: the `parsing form XML %q` wrap in
// formFromDump, the `(dump fallback: %v)` forward on the both-legs-failed branch,
// and the OS errors ParseFormXML used to wrap. Only the third had a test. Putting
// the first two back left `go test -race ./tools` green while the absolute path
// travelled into a rendered answer, because the two sites are only dangerous
// TOGETHER: the wrap puts the path into the dump-leg error, and the forward is
// what carries that error's text into the answer the model reads.
//
// So the pair is pinned at both ends. TestFormFromDump_ParseFailureCarriesNoPath
// fires on the wrap alone, before anything renders; the handler test below fires
// on the pair, through the one render branch that prints an error chain verbatim.
//
// What the path discloses is not the file name: it names the dump root, and
// through it the OS account this server runs under.

// unreadableDumpForm materialises a Form.xml that FindFormFiles will list and
// ParseFormXML cannot read, which is the one failure that reaches ParseFormXML
// with the selected path in hand.
//
// It returns the dump root. A filesystem or a user that ignores mode 000 has no
// unreadable file to offer, and the caller skips rather than measuring nothing;
// that is the same guard dump/formparser_commonform_test.go already uses.
func unreadableDumpForm(t *testing.T, objectDir, objectName, formName string) string {
	t.Helper()
	dumpDir := t.TempDir()
	ext := filepath.Join(dumpDir, objectDir, objectName, "Forms", formName, "Ext")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ext, "Form.xml")
	if err := os.WriteFile(path, []byte(listsOnlyFormXML), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this filesystem or user ignores mode 000, so there is no unreadable form file")
	}
	return dumpDir
}

// TestFormFromDump_ParseFailureCarriesNoPath fires on the wrap ALONE.
//
// It is the half the handler test cannot cover: with the forward removed, a
// restored `parsing form XML %q` puts the dump root into an error that no branch
// currently prints, so no assertion about a rendered answer can see it. A leak
// that is one line away from being read is still a leak, and this is where it is
// caught.
func TestFormFromDump_ParseFailureCarriesNoPath(t *testing.T) {
	dumpDir := unreadableDumpForm(t, "Catalogs", "Валюты", "ФормаСписка")

	form, _, lists, err := formFromDump(dumpDir, "Catalog", "Валюты", "")
	if err == nil {
		t.Fatalf("an unreadable Form.xml must fail the dump leg, got form %+v lists %+v", form, lists)
	}

	// POSITIVE CONTROL over the case: this is the read failure and not some
	// earlier refusal that never reached ParseFormXML with a path in hand.
	if classifyDumpLegFailure(err) != dumpReasonUnreadable {
		t.Fatalf("control failed: the dump leg failed with %q, so this is not the read failure "+
			"the path used to travel on: %v", classifyDumpLegFailure(err).code(), err)
	}
	if strings.Contains(err.Error(), dumpDir) {
		t.Errorf("the dump-leg error carries the absolute dump root %q: %v", dumpDir, err)
	}
	if strings.Contains(err.Error(), "Form.xml") {
		t.Errorf("the dump-leg error names the file it failed on: %v", err)
	}

	// POSITIVE CONTROL over the detector: it does see the root when one is there.
	planted := "parsing form XML " + filepath.Join(dumpDir, "Catalogs") + ": permission denied"
	if !strings.Contains(planted, dumpDir) {
		t.Fatal("control failed: the scan did not find the dump root in a message built around " +
			"it, so the assertions above measure a blind detector")
	}
}

// TestNewFormStructureHandler_BothLegsFailedCarriesNoAbsolutePath is the pair
// seen from outside, driven through the ONE render branch that prints the error
// chain verbatim.
//
// renderFailure classifies with errors.As on the typed onec errors; a
// *onec.DecodeError matches none of them and falls to the generic default, which
// quotes err.Error() under «Причина». That is the branch the forwarded dump text
// used to arrive on, and the branches for a status, a transport or a request
// failure never print it, so a test driven through any of those would be green
// with the leak in place.
func TestNewFormStructureHandler_BothLegsFailedCarriesNoAbsolutePath(t *testing.T) {
	// 200 with a body that is not JSON: the client reads it, fails to decode it,
	// and returns *onec.DecodeError.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("не json"))
	}))
	t.Cleanup(srv.Close)

	dumpDir := unreadableDumpForm(t, "Catalogs", "Валюты", "ФормаСписка")

	result, err := callFormHandler(t, srv.URL, dumpDir, "Catalog", "Валюты", "")
	text := failureText(t, result, err)

	// POSITIVE CONTROL over the branch: this really is the generic default, so
	// the answer really does quote the whole error chain and a forwarded dump
	// message would be visible in it.
	if !strings.Contains(text, lineGeneric) {
		t.Fatalf("control failed: this answer did not come through the generic render branch, "+
			"so it never quotes the error chain and proves nothing:\n%s", text)
	}
	// POSITIVE CONTROL over the dump leg: it really did fail, and it failed on the
	// read, which is the failure that has the selected path in hand.
	if !strings.Contains(text, dumpReasonUnreadable.code()) {
		t.Fatalf("control failed: the dump leg did not fail with %q, so the path this test "+
			"looks for was never in reach:\n%s", dumpReasonUnreadable.code(), text)
	}

	if strings.Contains(text, dumpDir) {
		t.Errorf("the absolute dump root %q reached the rendered answer:\n%s", dumpDir, text)
	}
	if strings.Contains(text, "Form.xml") {
		t.Errorf("the answer names the dump file the read failed on:\n%s", text)
	}

	// POSITIVE CONTROL over the detector, on the exact text the leak produced.
	planted := "Причина: fetching form structure from 1C: ... (dump fallback: parsing form XML " +
		filepath.Join(dumpDir, "Catalogs", "Валюты", "Forms", "ФормаСписка", "Ext", "Form.xml") + ")"
	if !strings.Contains(planted, dumpDir) {
		t.Fatal("control failed: the scan did not find the dump root in the very sentence the " +
			"leak used to print, so the assertions above measure a blind detector")
	}
}

// TestNewFormStructureHandler_DumpOnlyFailureCarriesNoAbsolutePath covers the
// OTHER end of the same channel: 1С answered, so the call succeeds and the dump
// failure is reported in the response body instead of in a failure render.
//
// It is a separate case because it is a separate branch: this one never builds a
// dumpLegFailure at all, and the note it prints is written from a closed
// vocabulary rather than from the lower layer's message.
func TestNewFormStructureHandler_DumpOnlyFailureCarriesNoAbsolutePath(t *testing.T) {
	srv := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")
	dumpDir := unreadableDumpForm(t, "Catalogs", "Валюты", "ФормаСписка")

	result, err := callFormHandler(t, srv.URL, dumpDir, "Catalog", "Валюты", "")
	if err != nil {
		t.Fatalf("the 1C leg answered, so the call must succeed: %v", err)
	}
	text := resultText(t, result)

	// POSITIVE CONTROL: the dump really did fail and the body really does say so.
	if !strings.Contains(text, dumpNoteMarker) {
		t.Fatalf("control failed: the answer carries no dump failure note, so the dump leg did "+
			"not fail here:\n%s", text)
	}
	if strings.Contains(text, dumpDir) {
		t.Errorf("the absolute dump root %q reached the successful answer:\n%s", dumpDir, text)
	}
}

// TestFormStructure_HandlerAnswersAreFreeOfTheDumpRoot is the wide net over the
// same property: every outcome the handler can reach with a dump configured,
// checked for the dump root in one table.
//
// The narrow tests above each pin one branch and say why that branch is special.
// This one exists because the branch that leaks next will be a branch nobody
// wrote a test for, and a table is the only form of this check that grows with
// the code instead of with the reader's memory.
func TestFormStructure_HandlerAnswersAreFreeOfTheDumpRoot(t *testing.T) {
	badJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("не json"))
	}))
	t.Cleanup(badJSON.Close)
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "Object not found"})
	}))
	t.Cleanup(notFound.Close)
	ok := formHTTPServer(t, "ФормаДокумента", "Реализация товаров и услуг")

	for _, tc := range []struct {
		name     string
		base     string
		formName string
	}{
		{"both legs failed, 1C decode error", badJSON.URL, ""},
		{"both legs failed, 1C status error", notFound.URL, ""},
		{"dump leg only, 1C answered", ok.URL, ""},
		{"dump leg only, with a form name", ok.URL, "ФормаСписка"},
		{"both legs failed, with a form name", badJSON.URL, "ФормаСписка"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dumpDir := unreadableDumpForm(t, "Catalogs", "Валюты", "ФормаСписка")
			result, err := callFormHandler(t, tc.base, dumpDir, "Catalog", "Валюты", tc.formName)
			var text string
			switch {
			case err != nil:
				t.Fatalf("an operational failure came back as an error: %v", err)
			case result == nil:
				t.Fatal("neither a result nor an error came back")
			default:
				text = resultText(t, result)
			}
			if strings.TrimSpace(text) == "" {
				t.Fatal("control failed: the answer is empty, so scanning it proves nothing")
			}
			if strings.Contains(text, dumpDir) {
				t.Errorf("the absolute dump root %q reached the answer:\n%s", dumpDir, text)
			}
		})
	}
}

// TestOnecDecodeErrorFallsToTheGenericRenderBranch pins the premise the handler
// test above is built on, so that premise cannot rot in silence.
//
// If a future commit gave *onec.DecodeError a branch of its own in
// renderFailure, the handler test would keep passing while no longer exercising
// the branch that quotes the error chain, and the leak it guards would be
// unguarded again with every test green.
func TestOnecDecodeErrorFallsToTheGenericRenderBranch(t *testing.T) {
	const marker = "МАРКЕР_ЦЕПОЧКИ_ОШИБОК"
	body := renderFailure(headingForm, &onec.DecodeError{
		Endpoint: "/form/Catalog/Валюты",
		Err:      errString(marker),
	})

	if !strings.Contains(body, lineGeneric) {
		t.Errorf("a decode error no longer renders through the generic branch:\n%s", body)
	}
	if !strings.Contains(body, marker) {
		t.Errorf("the generic branch no longer quotes the error chain verbatim, so the leak "+
			"test built on it is measuring nothing:\n%s", body)
	}
}

// errString is an error whose text is exactly the string, with no type any
// renderer branch could match on.
type errString string

func (e errString) Error() string { return string(e) }
