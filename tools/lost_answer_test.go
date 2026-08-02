package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// ---------------------------------------------------------------------------
// A lost or partial answer is never presented as a complete one.
//
// Three 200-shaped answers reached the model with IsError = false and the answer
// gone. Measured before the repair, driving get_metadata_tree against a listener
// replaying each shape:
//
//	{"Справочники":[…],"errors":["потеряно 109 заданий","потеряно 16 веб-сервисов"]}
//	    summary: - **errors** (2) — filter="errors"
//	    filter=errors: "# Метаданные конфигурации 1С\n\n## errors\n
//	                    - потеряно 109 заданий\n- потеряно 16 веб-сервисов\n\n"
//	{}      summary: header, the invitation to use a filter, and nothing else
//	null    summary: byte identical to the {} case
//
// The first is the worst of the three: a report that 109 scheduled jobs and 16
// web services were LOST is rendered as a category of configuration objects, so
// the evidence of the loss is what the model reads as the data.
// ---------------------------------------------------------------------------

// metadataReplay serves one /metadata body, exactly as the far side would.
func metadataReplay(t *testing.T, body string) *onec.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return onec.NewClient(srv.URL, "", "")
}

func TestDiagnosticsArrayIsNotAMetadataCategory(t *testing.T) {
	const body = `{"Справочники":["Контрагенты"],` +
		`"errors":["потеряно 109 заданий","потеряно 16 веб-сервисов"]}`
	h := NewMetadataHandler(metadataReplay(t, body))

	summary, isErr, err := callTool(t, h, `{}`)
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if isErr {
		t.Fatalf("a 200 with a usable tree must still answer; got a failure:\n%s", summary)
	}

	if strings.Contains(summary, `filter="errors"`) {
		t.Errorf("the diagnostics key is offered as a filterable category:\n%s", summary)
	}
	if !strings.Contains(summary, "потеряно 109 заданий") {
		t.Errorf("the loss was dropped instead of reported; hiding it is the other half of "+
			"the same defect:\n%s", summary)
	}
	if !strings.Contains(summary, "Диагностика") {
		t.Errorf("the loss is not marked as diagnostics:\n%s", summary)
	}

	// CONTROL: the real category alongside it is still rendered as a category,
	// so the repair distinguishes the two rather than suppressing both.
	if !strings.Contains(summary, `filter="Справочники"`) {
		t.Errorf("the genuine category stopped being offered:\n%s", summary)
	}

	// And under the filter the caller would reach for, the losses must not be
	// rendered as the contents of a category.
	filtered, filteredIsErr, err := callTool(t, h, `{"filter":"errors"}`)
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if strings.Contains(filtered, "## errors") {
		t.Errorf("filter=errors still renders a section headed by the diagnostics key "+
			"(isError=%v):\n%s", filteredIsErr, filtered)
	}
	if !strings.Contains(filtered, "Категории \"errors\" в ответе 1С нет") {
		t.Errorf("filter=errors does not say the category is absent:\n%s", filtered)
	}
}

func TestEmptyAnswerIsNotAnEmptyConfiguration(t *testing.T) {
	shapes := []struct{ name, body string }{
		{"empty object", `{}`},
		{"null literal", `null`},
		{"diagnostics only", `{"warnings":["РегламентныеЗадания: нет права"]}`},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			h := NewMetadataHandler(metadataReplay(t, s.body))
			summary, isErr, err := callTool(t, h, `{}`)
			if err != nil {
				t.Fatalf("unexpected protocol error: %v", err)
			}
			if isErr {
				t.Fatalf("a 200 is not a failure of the call; got:\n%s", summary)
			}
			if !strings.Contains(summary, "не оказалось ни одной категории метаданных") {
				t.Errorf("an answer carrying no categories does not say so:\n%s", summary)
			}
			if strings.Contains(summary, "Для получения списка объектов вызови") {
				t.Errorf("the answer invites a filter call it has nothing to filter:\n%s", summary)
			}
		})
	}

	// CONTROL: a tree with content says none of that. Without this the
	// assertions above would pass against a renderer that printed the notice
	// unconditionally, which is a worse answer than the one being repaired.
	h := NewMetadataHandler(metadataReplay(t, `{"Справочники":["Контрагенты"]}`))
	ok, isErr, err := callTool(t, h, `{}`)
	if err != nil || isErr {
		t.Fatalf("the control call failed: err=%v isError=%v", err, isErr)
	}
	if strings.Contains(ok, "не оказалось ни одной категории метаданных") {
		t.Fatalf("the empty-answer notice is printed for a non-empty answer:\n%s", ok)
	}
	if !strings.Contains(ok, "Для получения списка объектов вызови") {
		t.Fatalf("the invitation is missing from an answer that has something to filter:\n%s", ok)
	}
}

func TestUnknownKeyIsShownWithoutBeingCalledACategory(t *testing.T) {
	// Forward compatibility is real: a later extension can add a collection this
	// build has never heard of, and dropping it would hide genuine objects. What
	// it must not do is claim the key is a metadata category.
	h := NewMetadataHandler(metadataReplay(t,
		`{"Справочники":["Контрагенты"],"ПланыОбменаНовые":["ОбменССайтом"]}`))

	summary, _, err := callTool(t, h, `{}`)
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if !strings.Contains(summary, "ПланыОбменаНовые") {
		t.Errorf("an unknown key was dropped, hiding objects the far side reported:\n%s", summary)
	}
	if !strings.Contains(summary, "этот сервер не знает") {
		t.Errorf("an unknown key is listed with no sign that it is unknown:\n%s", summary)
	}

	filtered, _, err := callTool(t, h, `{"filter":"ПланыОбменаНовые"}`)
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if !strings.Contains(filtered, "ОбменССайтом") {
		t.Errorf("filtering on an unknown key lost its contents:\n%s", filtered)
	}
	if !strings.Contains(filtered, "этот сервер не знает") {
		t.Errorf("the filtered view drops the caveat the summary carries:\n%s", filtered)
	}

	// CONTROL: a KNOWN category carries no such caveat, so the notice tracks
	// recognition rather than being printed on everything.
	known, _, err := callTool(t, h, `{"filter":"Справочники"}`)
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if strings.Contains(known, "этот сервер не знает") {
		t.Errorf("a known category was labelled unknown:\n%s", known)
	}
}

// TestClusterTwoStringsCarryNoDash runs the house dash rule over every
// customer-facing RU string this repair adds.
//
// It does not reuse dashViolations. That checker's forbidden set is a seven rune
// list that does not include U+FF0D, measured on this tree by handing it the two
// strings the controls below plant: dashViolations returns 1 for the U+2014 one
// (bytes e2 80 94) and 0 for the U+FF0D one (bytes ef bc 8d), with the bytes
// read back through []byte rather than assumed. Repairing the shipped guard is a
// separate decision that is not this repair's to take, so this checker is simply
// stricter about the text it owns and says so, rather than leaving a green here
// to imply the repo-wide guard is sound.
func TestClusterTwoStringsCarryNoDash(t *testing.T) {
	shipped := []string{
		unknownKeyNotice,
		emptyTreeNotice,
		fmt.Sprintf(emptyCategoryNotice, "Справочники"),
		formServiceCallFailedNote(errStubForDashCheck{}),
		fmt.Sprintf("неизвестный уровень важности %q (допустимо: %s)",
			"Критическая", strings.Join(eventLogLevels, ", ")),
		remedyForeignBody,
		remedyUnreachable,
		remedyRedirect,
		remedyQueryRejected,
	}
	for i, s := range shipped {
		for _, r := range s {
			if isForbiddenDash(r) {
				t.Errorf("string %d carries %q (U+%04X): %s", i, r, r, s)
			}
		}
	}

	// The controls. Both must fire, and the clean one must not, or a green above
	// means only that the loop ran.
	for _, planted := range []string{
		"адрес отклонён — уберите пароль", // U+2014, bytes e2 80 94
		"адрес отклонён － уберите пароль", // U+FF0D, bytes ef bc 8d
	} {
		found := false
		for _, r := range planted {
			if isForbiddenDash(r) {
				found = true
			}
		}
		if !found {
			t.Errorf("the checker did not fire on a planted dash in %q, so it cannot fail", planted)
		}
	}
	for _, r := range "адрес отклонён, уберите пароль" {
		if isForbiddenDash(r) {
			t.Fatalf("the checker fires on dash-free text at %q, so every green above is noise", r)
		}
	}
}

// isForbiddenDash covers the Unicode dash punctuation block, the minus sign and
// the fullwidth hyphen-minus. U+002D is deliberately absent: it is the ASCII
// hyphen the house style writes flags and compound words with.
func isForbiddenDash(r rune) bool {
	return (r >= 0x2010 && r <= 0x2015) || r == 0x2212 || r == 0xFF0D || r == 0x2043
}

// errStubForDashCheck is a dash-free error, so a violation the note reports can
// only come from the note's own words.
type errStubForDashCheck struct{}

func (errStubForDashCheck) Error() string { return "1C returned status 500" }
