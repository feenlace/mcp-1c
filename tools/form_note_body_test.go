package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The phrases below are matched literally rather than through the note
// constants, exactly as dumpNoteMarker and its siblings are. A test that reads
// the constant it is checking passes whatever the constant says; a test that
// spells the claim out fails when the claim stops being true.

// shownAboveClaimMarker is the clause of formNoFormRootNote that asserts the
// composition sections printed above the note came from the 1C HTTP service.
// It is a statement about the BODY, so it is only true of a body that has those
// sections.
const shownAboveClaimMarker = "показано выше в разделах состава формы"

// claimsSomethingIsShownAbove collects every wording a note in this handler uses
// to talk about the composition sections printed above it. Each is a statement
// about a body, true only of a body that HAS those sections, and each belongs to
// a different note: the first to formNoFormRootNote, the second to
// formPartialParseNote. They are listed together because the defect they guard
// is one class, not two, and a guard that knows only the note it was written for
// is how the class reached its fourth instance.
var claimsSomethingIsShownAbove = []string{
	shownAboveClaimMarker,
	"разделы состава формы выше могут содержать",
}

// serviceChoosesFormMarker is the clause of formNameNoStructureNote that denies
// form_name any influence over which form the answer describes. With --dump the
// dump form IS selected by form_name, and when 1C supplies no name of its own
// that selection is what the heading shows, so the clause is then refuted by the
// line two rows above it.
const serviceChoosesFormMarker = "на этот выбор не влияет"

// oneCNotReachedMarker is the phrase of the note that reports a failed call to
// the 1C HTTP service. Before this work the failure was swallowed whole on the
// branch where the dump succeeded: the response said 1C had returned the
// composition while the connection had in fact been refused.
const oneCNotReachedMarker = "Запрос к HTTP-сервису 1С завершился ошибкой"

// formBodyHasComposition reports whether the rendered body carries any of the
// three composition sections. Every note clause that says something about what
// is "показано выше в разделах состава формы" has to be checked against this.
func formBodyHasComposition(text string) bool {
	return strings.Contains(text, "## Элементы формы") ||
		strings.Contains(text, "## Команды формы") ||
		strings.Contains(text, "## Обработчики событий")
}

// deadBaseURL returns the URL of a local HTTP server that has been shut down, so
// a request to it is refused at once. It is the "1C is unreachable" case with no
// waiting and no network.
func deadBaseURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

// notFoundBaseURL returns a local server that answers 404 to everything, which
// is the second shape of "1C did not answer": reachable host, no service.
func notFoundBaseURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestNewFormStructureHandler_FormlessDumpWithNo1CDoesNotCredit1C is the blocker
// as reported. A dump whose Form.xml holds no <Form> plus a 1C service that
// never answered produces a body with a heading and nothing else, under a note
// stating that everything shown above came from that service. Two things are
// wrong at once: nothing is shown above, and the service was not reached.
func TestNewFormStructureHandler_FormlessDumpWithNo1CDoesNotCredit1C(t *testing.T) {
	for name, baseURL := range map[string]string{
		"connection refused":  deadBaseURL(t),
		"service returns 404": notFoundBaseURL(t),
	} {
		t.Run(name, func(t *testing.T) {
			dumpDir := t.TempDir()
			writeDumpForm(t, dumpDir, "Catalogs", "Контрагенты", "ФормаБезОписания",
				`<?xml version="1.0" encoding="UTF-8"?><Nothing><Here/></Nothing>`)

			result, err := callFormHandler(t, baseURL, dumpDir, "Catalog", "Контрагенты", "")
			if err != nil {
				t.Fatalf("a formless dump with 1C down must still answer, not fail: %v", err)
			}
			text := resultText(t, result)

			// Premise: the body carries no composition section at all.
			if formBodyHasComposition(text) {
				t.Fatalf("premise broken: the body has composition sections:\n%s", text)
			}
			if !strings.Contains(text, noFormRootNoteMarker) {
				t.Fatalf("premise broken: the formless note is not in the body:\n%s", text)
			}

			if strings.Contains(text, shownAboveClaimMarker) {
				t.Errorf("note says something is shown above in the composition sections, "+
					"and there are none:\n%s", text)
			}
			if !strings.Contains(text, oneCNotReachedMarker) {
				t.Errorf("the call to 1C failed and the body never says so, so the caller reads "+
					"the answer as one 1C took part in:\n%s", text)
			}
		})
	}
}

// TestNewFormStructureHandler_FormNameNoteDoesNotDenyTheHeadingItProduced is the
// second falsehood of the same response. With 1C silent the heading is the dump's
// form, which is the one form_name selected, while the note two lines below says
// the parameter had no influence on that choice.
func TestNewFormStructureHandler_FormNameNoteDoesNotDenyTheHeadingItProduced(t *testing.T) {
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Catalogs", "Контрагенты", "ФормаБезОписания",
		`<?xml version="1.0" encoding="UTF-8"?><Nothing><Here/></Nothing>`)

	result, err := callFormHandler(t, deadBaseURL(t), dumpDir, "Catalog", "Контрагенты", "ФормаБезОписания")
	if err != nil {
		t.Fatalf("a formless dump with 1C down must still answer, not fail: %v", err)
	}
	text := resultText(t, result)

	// Premise: the heading IS the requested form, and the form_name note fired.
	if !strings.Contains(text, "# Форма: ФормаБезОписания") {
		t.Fatalf("premise broken: the heading is not the requested form:\n%s", text)
	}
	if !strings.Contains(text, namedFormNoStructureMarker) {
		t.Fatalf("premise broken: the form_name note is not in the body:\n%s", text)
	}

	if strings.Contains(text, serviceChoosesFormMarker) {
		t.Errorf("note denies form_name any influence on the chosen form while the heading "+
			"above it is that very form_name:\n%s", text)
	}
}

// TestNewFormStructureHandler_FailedCallTo1CIsNeverSilent widens the second half
// of the blocker beyond the damaged-dump case. Whenever the dump answers, the
// handler ignores httpErr completely, so a perfectly healthy dump plus a dead 1C
// produces a response indistinguishable from one both sources agreed on. The
// caller cannot tell that the live half of the answer is missing.
func TestNewFormStructureHandler_FailedCallTo1CIsNeverSilent(t *testing.T) {
	failing := map[string]string{
		"connection refused":  deadBaseURL(t),
		"service returns 404": notFoundBaseURL(t),
	}
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv500.Close)
	failing["service returns 500"] = srv500.URL

	for name, baseURL := range failing {
		t.Run(name, func(t *testing.T) {
			dumpDir := t.TempDir()
			writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента",
				sampleFormXML())

			result, err := callFormHandler(t, baseURL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
			if err != nil {
				t.Fatalf("a healthy dump must still answer when 1C is down: %v", err)
			}
			text := resultText(t, result)

			// Premise: the dump half of the answer is intact.
			if !strings.Contains(text, "Контрагент") {
				t.Fatalf("premise broken: the dump structure is missing:\n%s", text)
			}
			if !strings.Contains(text, oneCNotReachedMarker) {
				t.Errorf("1C could not be reached and the body never says so:\n%s", text)
			}
		})
	}
}

// TestNewFormStructureHandler_FailedCallNoteDoesNotLeakAPassword guards the one
// new exposure the note creates: it prints an upstream error into a body a user
// and an LLM both read, and that error carries the request URL. A password put
// into --base therefore has to survive redaction, which net/http does for a
// *url.Error (stripPassword in net/http/client.go). The check is here and not in
// a comment because the note is what makes the redaction load bearing.
// Both credential shapes are covered because they fail on DIFFERENT paths and
// only one of them is redacted by the standard library. An ASCII userinfo parses,
// so the failure arrives as a *url.Error from the transport with the password
// already replaced; a non-ASCII one is rejected by url.Parse, which quotes the
// whole URL it was handed before a request is ever made.
func TestNewFormStructureHandler_FailedCallNoteDoesNotLeakAPassword(t *testing.T) {
	dead := deadBaseURL(t)
	for name, creds := range map[string]string{
		"ascii userinfo":     "polzovatel:SuperSecret",
		"non-ascii userinfo": "пользователь:СуперСекрет",
	} {
		t.Run(name, func(t *testing.T) {
			withCreds := strings.Replace(dead, "http://", "http://"+creds+"@", 1)

			dumpDir := t.TempDir()
			writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента", sampleFormXML())

			result, err := callFormHandler(t, withCreds, dumpDir, "Document", "РеализацияТоваровУслуг", "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			text := resultText(t, result)

			if !strings.Contains(text, oneCNotReachedMarker) {
				t.Fatalf("premise broken: the failed-call note is not in the body:\n%s", text)
			}
			user, password, _ := strings.Cut(creds, ":")
			if strings.Contains(text, password) {
				t.Errorf("the password from --base is printed in the response body:\n%s", text)
			}
			if strings.Contains(text, user) {
				t.Errorf("the user name from --base is printed in the response body:\n%s", text)
			}
		})
	}
}

// TestNewFormStructureHandler_FailedCallNoteStaysOneLine pins the other property
// of printing an upstream message: a 1C error body is arbitrary text, and a
// multi-line one would break out of the blockquote and turn one note into
// several lines of unattributed remote content.
func TestNewFormStructureHandler_FailedCallNoteStaysOneLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("строка один\nстрока два\n\nстрока три\n"))
	}))
	t.Cleanup(srv.Close)

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента", sampleFormXML())

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	var noteLine string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "> "+oneCNotReachedMarker) {
			noteLine = line
		}
	}
	if noteLine == "" {
		t.Fatalf("premise broken: the failed-call note is not a blockquote line of its own:\n%s", text)
	}
	// Premise: the upstream body really did carry line breaks.
	if !strings.Contains(noteLine, "строка один строка два строка три") {
		t.Errorf("the multi-line upstream body must be folded into the single note line, got:\n%s",
			noteLine)
	}
}

// TestNewFormStructureHandler_ReachedServiceIsNotReportedAsFailed is the negative
// control for the note above: it must fire on a failed call and only on one. An
// unconditional note would claim 1C was down in every single healthy response.
func TestNewFormStructureHandler_ReachedServiceIsNotReportedAsFailed(t *testing.T) {
	srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента", sampleFormXML())

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if strings.Contains(text, oneCNotReachedMarker) {
		t.Errorf("1C answered, so the body must not report a failed call:\n%s", text)
	}
}

// TestNewFormStructureHandler_HTTPProvenanceClauseSurvivesAFullBody is the
// positive control for shownAboveClaimMarker. The clause is correct and useful
// when the body DOES carry sections the dump could not have contributed, and a
// fix that simply deleted it would pass every "must not claim" test in this file
// while removing the one place a caller learns where the table came from.
func TestNewFormStructureHandler_HTTPProvenanceClauseSurvivesAFullBody(t *testing.T) {
	srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента", "")

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !formBodyHasComposition(text) {
		t.Fatalf("premise broken: the HTTP service supplied no sections:\n%s", text)
	}
	if !strings.Contains(text, noFormRootNoteMarker) {
		t.Fatalf("premise broken: the formless note is not in the body:\n%s", text)
	}
	if !strings.Contains(text, shownAboveClaimMarker) {
		t.Errorf("with sections on screen that only 1C can have supplied, the note must still "+
			"say where they came from:\n%s", text)
	}
}

// TestNewFormStructureHandler_PartialClauseSurvivesAFullBody is the positive
// control for the partial note's own body clause, the twin of the one above. A
// fix that deleted the sentence instead of making it conditional would pass every
// "must not claim" check while dropping the only hint that the table on screen
// may be the 1C service's rather than the truncated file's.
func TestNewFormStructureHandler_PartialClauseSurvivesAFullBody(t *testing.T) {
	srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента",
		truncatedBeforeAnyElementXML())

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !formBodyHasComposition(text) {
		t.Fatalf("premise broken: the HTTP service supplied no sections:\n%s", text)
	}
	if !strings.Contains(text, partialNoteMarker) {
		t.Fatalf("premise broken: the partial parse note is not in the body:\n%s", text)
	}
	if !strings.Contains(text, "разделы состава формы выше могут содержать") {
		t.Errorf("with sections on screen the dump did not supply, the note must still say "+
			"where they may have come from:\n%s", text)
	}
}

// TestNewFormStructureHandler_PartialNoteWithAnEmptyBodySaysSo is the negative
// half. A Form.xml that breaks before it records anything, plus a 1C that
// returned nothing, leaves a body with no composition at all, and the clause
// above is then a sentence about sections that do not exist.
func TestNewFormStructureHandler_PartialNoteWithAnEmptyBodySaysSo(t *testing.T) {
	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаДокумента",
		truncatedBeforeAnyElementXML())

	result, err := callFormHandler(t, deadBaseURL(t), dumpDir, "Document", "РеализацияТоваровУслуг", "")
	if err != nil {
		t.Fatalf("a truncated dump with 1C down must still answer: %v", err)
	}
	text := resultText(t, result)

	if formBodyHasComposition(text) {
		t.Fatalf("premise broken: the body has composition sections:\n%s", text)
	}
	if !strings.Contains(text, partialNoteMarker) {
		t.Fatalf("premise broken: the partial parse note is not in the body:\n%s", text)
	}
	if strings.Contains(text, "разделы состава формы выше могут содержать") {
		t.Errorf("note speculates about the contents of composition sections that are not "+
			"in the body:\n%s", text)
	}
}

// TestNewFormStructureHandler_ServiceChoosesFormClauseSurvivesAnHTTPHeading is
// the positive control for serviceChoosesFormMarker. When 1C DID name a form,
// the heading is its choice and not the caller's, and saying so is the whole
// point of the note.
func TestNewFormStructureHandler_ServiceChoosesFormClauseSurvivesAnHTTPHeading(t *testing.T) {
	srv := formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация товаров и услуг")

	dumpDir := t.TempDir()
	writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаЭлемента", emptyFormXML())

	result, err := callFormHandler(t, srv.URL, dumpDir, "Document", "РеализацияТоваровУслуг", "ФормаЭлемента")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)

	if !strings.Contains(text, "# Форма: ФормаДокумента") {
		t.Fatalf("premise broken: the heading is not the 1C choice:\n%s", text)
	}
	if !strings.Contains(text, serviceChoosesFormMarker) {
		t.Errorf("the heading is 1C's own pick, so the note must still say the parameter did "+
			"not choose it:\n%s", text)
	}
}

// TestNewFormStructureHandler_NoNoteClaimsAnAbsentBody is the class guard rather
// than a guard on one note. This is the fourth response in this codebase whose
// note contradicted the body printed above it, so the check is written over the
// whole matrix the handler can produce: every dump shape crossed with a 1C that
// answers, a 1C that is down, and with and without form_name.
//
// The rule it enforces is the one all four instances broke: a note may not say
// that something is shown above unless something is.
func TestNewFormStructureHandler_NoNoteClaimsAnAbsentBody(t *testing.T) {
	// Positive control over the scan itself: handed the two wordings directly, it
	// has to see them. Hand-typed on purpose, as a control over the matcher and
	// never as the assertion about the product.
	for _, control := range []string{
		"Всё, что показано выше в разделах состава формы, вернул HTTP-сервис 1С.",
		"а разделы состава формы выше могут содержать данные, которые вернул HTTP-сервис 1С",
	} {
		if !containsAny(control, claimsSomethingIsShownAbove) {
			t.Fatalf("the scan does not detect the claim it was handed directly: %q", control)
		}
	}

	dumps := map[string]string{
		"formless file":       `<?xml version="1.0" encoding="UTF-8"?><Nothing/>`,
		"empty file":          "",
		"form declaring none": emptyFormXML(),
		"truncated file":      truncatedBeforeAnyElementXML(),
		"healthy file":        sampleFormXML(),
	}
	// A 1C that answers with nothing at all is as important as one that is down:
	// it is what the bundled extension returns whenever its Попытка blocks come
	// back empty, and it also leaves the body without sections.
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "", "title": ""})
	}))
	t.Cleanup(silent.Close)

	bases := map[string]string{
		"1C down":   deadBaseURL(t),
		"1C 404":    notFoundBaseURL(t),
		"1C silent": silent.URL,
		"1C full":   formHTTPServerWithStructure(t, "ФормаДокумента", "Реализация").URL,
	}

	for dumpName, xml := range dumps {
		for baseName, baseURL := range bases {
			for _, formName := range []string{"", "ФормаЭлемента"} {
				label := dumpName + " / " + baseName + " / form_name=" + formName
				t.Run(label, func(t *testing.T) {
					dumpDir := t.TempDir()
					writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаЭлемента", xml)

					result, err := callFormHandler(t, baseURL, dumpDir,
						"Document", "РеализацияТоваровУслуг", formName)
					if err != nil {
						t.Fatalf("this combination must not be a hard error: %v", err)
					}
					text := resultText(t, result)

					if formBodyHasComposition(text) {
						return
					}
					for _, claim := range claimsSomethingIsShownAbove {
						if strings.Contains(text, claim) {
							t.Errorf("a note claims %q while the body has no composition "+
								"section at all:\n%s", claim, text)
						}
					}
				})
			}
		}
	}
}
