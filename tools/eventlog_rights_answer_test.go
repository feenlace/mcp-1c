package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// WHAT A CALLER WITHOUT THE RIGHT NOW RECEIVES.
//
// The extension answers 403 with its own envelope instead of raising inside
// ВыгрузитьЖурналРегистрации and letting the platform reply 500 with a module
// name and a line number in the body. The rendering has to make two things
// unmistakable, and neither is automatic:
//
//   - the refusal is a refusal. No rows, no total, nothing that reads as a
//     short log. A partial or unfiltered journal presented as a normal answer is
//     worse than the 500 was.
//   - the cause is the ACCOUNT, not the filter. A 403 with no advice reads as
//     "try different arguments", and there are no arguments that work.
// ---------------------------------------------------------------------------

// renderEventLogRefusal drives the real handler against a 1С that refuses, so
// what is asserted is what a caller receives and not a constant nobody proved
// was reached.
func renderEventLogRefusal(t *testing.T, status int, envelope string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(envelope))
	}))
	defer srv.Close()

	client := onec.NewClient(srv.URL, "", "")
	handler := NewEventLogHandler(client)
	args, _ := json.Marshal(map[string]any{"limit": 3})
	result, err := handler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "get_event_log", Arguments: args},
	})
	if err != nil {
		t.Fatalf("the handler returned a protocol error instead of a rendered failure: %v", err)
	}
	if result == nil || len(result.Content) == 0 {
		t.Fatal("the handler returned no content at all")
	}
	if !result.IsError {
		t.Error("a refusal came back with IsError false, so the model is told the call succeeded")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, not text", result.Content[0])
	}
	return text.Text
}

// TestEventLogRefusalIsNotAnAnswer pins the first half: a refusal carries no log.
func TestEventLogRefusalIsNotAnAnswer(t *testing.T) {
	const envelope = `{"error": "reading the event log requires the ` +
		`Администрирование right and this account does not have it; no records are returned"}`
	text := renderEventLogRefusal(t, http.StatusForbidden, envelope)

	// Nothing from the success formatter may appear. These are the exact strings
	// formatEventLog writes, so a rendering that grew a log would trip here.
	for _, forbidden := range []string{"## Журнал регистрации\n", "\nВсего: ", "- Пользователь: "} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the refusal carries %q, which only the success formatter writes:\n%s",
				forbidden, text)
		}
	}
	if !strings.Contains(text, headingEventLog) {
		t.Errorf("the refusal is not rendered under the failure heading:\n%s", text)
	}

	// CONTROL: those same strings DO appear on a success, so their absence above
	// is a property of the refusal and not of the assertion.
	ok := formatEventLog(&onec.EventLogResult{
		Events: []onec.EventLogEntry{{Date: "2026-08-03T00:00:00", Level: "Информация",
			Event: "Данные.Запись", User: "Demo"}},
		Total: 1,
	})
	for _, want := range []string{"## Журнал регистрации\n", "\nВсего: ", "- Пользователь: "} {
		if !strings.Contains(ok, want) {
			t.Errorf("CONTROL: a successful answer does not contain %q, so its absence in the "+
				"refusal proves nothing", want)
		}
	}
}

// TestEventLogRefusalBlamesTheAccountNotTheFilter pins the second half.
func TestEventLogRefusalBlamesTheAccountNotTheFilter(t *testing.T) {
	const envelope = `{"error": "reading the event log requires the ` +
		`Администрирование right and this account does not have it; no records are returned"}`
	text := renderEventLogRefusal(t, http.StatusForbidden, envelope)

	for _, want := range []string{
		"Это отказ по правам учётной записи, а не по отбору",
		"право Администрирование",
		"`--user`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the answer does not carry %q:\n%s", want, text)
		}
	}

	// The text from 1С is still shown, framed as data. Losing it would hide which
	// of the two 403s fired.
	if !strings.Contains(text, "no records are returned") {
		t.Errorf("the diagnostic from 1С is no longer shown:\n%s", text)
	}
	if !strings.Contains(text, untrustedTextNotice) {
		t.Errorf("the upstream text is shown without the notice that frames it as data:\n%s", text)
	}
}

// TestEventLogRightsRemedyStaysOnItsOwnClass is the false-positive control.
//
// Advice that appears everywhere is not advice. A 400 under the same heading is
// a value the caller CAN correct, a 403 under another heading is a different
// tool's rights problem with a different remedy, and the OTHER 403 this same
// handler answers is a filter matter reached only by an account that has the
// right.
//
// EVERY CASE VARIES ONE THING. Each rendering below is the rights refusal with a
// single field changed, so a negative result is a property of that field and not
// of a detail string that was never going to match anything.
func TestEventLogRightsRemedyStaysOnItsOwnClass(t *testing.T) {
	const marker = "Это отказ по правам учётной записи, а не по отбору"

	sameHeadingOtherStatus := renderFailure(headingEventLog, &onec.StatusError{
		StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: eventLogRightsRefusalPrefix,
	})
	if strings.Contains(sameHeadingOtherStatus, marker) {
		t.Errorf("the rights remedy leaked onto a 400, which the caller can fix by editing a "+
			"value:\n%s", sameHeadingOtherStatus)
	}

	otherHeadingSameStatus := renderFailure(headingMetadata, &onec.StatusError{
		StatusCode: 403, BodyKind: onec.BodyKindExtension, Detail: eventLogRightsRefusalPrefix,
	})
	if strings.Contains(otherHeadingSameStatus, marker) {
		t.Errorf("the rights remedy leaked onto another tool's 403:\n%s", otherHeadingSameStatus)
	}

	// The other 403 of the SAME handler. It stands after the rights gate, so the
	// caller has the right, and this is the case the status-only key got wrong.
	sameHeadingOtherCause := renderFailure(headingEventLog, &onec.StatusError{
		StatusCode: 403, BodyKind: onec.BodyKindExtension,
		Detail: eventLogUserFilterRefusalPrefix + ": Ошибка при вызове метода контекста",
	})
	if strings.Contains(sameHeadingOtherCause, marker) {
		t.Errorf("the rights remedy leaked onto the user filter 403, which only an account WITH "+
			"the right can reach:\n%s", sameHeadingOtherCause)
	}

	// A detail this side cannot place gets no cause at all. Both texts state
	// where the caller stands relative to the gate, and that cannot be said about
	// a body whose producer is unknown.
	unclassified := renderFailure(headingEventLog, &onec.StatusError{
		StatusCode: 403, BodyKind: onec.BodyKindExtension, Detail: "что угодно",
	})
	if strings.Contains(unclassified, marker) {
		t.Errorf("the rights remedy is asserted for a 403 whose diagnostic matches neither "+
			"refusal the handler can produce:\n%s", unclassified)
	}

	// A foreign 403 under the event-log heading is NOT the extension refusing:
	// nothing establishes who answered, and remedyForeignBody already says so.
	foreign := renderFailure(headingEventLog, &onec.StatusError{
		StatusCode: 403, BodyKind: onec.BodyKindForeign, ContentType: "text/html", BodyBytes: 512,
	})
	if strings.Contains(foreign, marker) {
		t.Errorf("the rights remedy is asserted for a body whose author is unknown:\n%s", foreign)
	}

	// CONTROL: the marker is findable at all. Without this every check above is
	// satisfied by a string that appears nowhere.
	hit := renderFailure(headingEventLog, &onec.StatusError{
		StatusCode: 403, BodyKind: onec.BodyKindExtension, Detail: eventLogRightsRefusalPrefix,
	})
	if !strings.Contains(hit, marker) {
		t.Fatalf("CONTROL: the marker is absent from the class it belongs to, so the three "+
			"negative results above measure nothing:\n%s", hit)
	}
}

// TestEventLogRightsRemedyDoesNotSuggestDroppingTheFilter guards the one piece of
// advice that would be actively wrong here.
//
// Measured on 1С 8.3.27 on 2026-08-03, before the extension gained the check:
// as infobase user Demo, {"limit":3} answered 500 and {"limit":3,"user":"Demo"}
// answered 500 as well. Dropping the filter was never a way round the missing
// right, so an answer that proposes it sends the reader round a loop.
func TestEventLogRightsRemedyDoesNotSuggestDroppingTheFilter(t *testing.T) {
	text := renderFailure(headingEventLog, &onec.StatusError{
		StatusCode: 403, BodyKind: onec.BodyKindExtension, Detail: eventLogRightsRefusalPrefix,
	})
	for _, wrong := range []string{"без отбора бесполезно"} {
		if !strings.Contains(text, wrong) {
			t.Errorf("the answer does not rule out retrying without the filter:\n%s", text)
		}
	}
	if strings.Contains(text, "повторите без отбора") || strings.Contains(text, "уберите отбор") {
		t.Errorf("the answer proposes dropping the filter, which does not help:\n%s", text)
	}
}
