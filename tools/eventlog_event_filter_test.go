package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// THE EVENT FILTER, AND WHY ITS SCHEMA CARRIES NO ENUM.
//
// level has an enum because its vocabulary is closed: four names, and the
// handler enforces them against the same list the schema declares. event has no
// enum because its vocabulary is OPEN. Application code writes its own events
// with ЗаписьЖурналаРегистрации and picks the name itself; dots in the name are
// ordinary, and the identifiers the platform writes are of the same shape. A
// closed list here would refuse legitimate values, and this server has no way to
// know the list of a base it has not asked.
//
// The check that DOES exist is on the far side, where the list can be asked for:
// ЖурналРегистрацииPOST reads the base's own event values before it applies the
// filter and refuses a name that is not among them, so a misspelling stops the
// call instead of quietly widening it.
// ---------------------------------------------------------------------------

// eventLogSchema returns the parsed input schema of get_event_log.
func eventLogSchema(t *testing.T) map[string]any {
	t.Helper()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	// InputSchema is declared as any and holds a json.RawMessage, so it is taken
	// back to bytes rather than asserted to a concrete type: an assertion would
	// break on the day the field is filled with a struct, which is a change to
	// the registration and not to the schema this test is about.
	raw, err := json.Marshal(EventLogTool().InputSchema)
	if err != nil {
		t.Fatalf("the shipped get_event_log schema does not marshal: %v", err)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("the shipped get_event_log schema does not parse: %v", err)
	}
	out := map[string]any{}
	for k, v := range schema.Properties {
		var m map[string]any
		if err := json.Unmarshal(v, &m); err != nil {
			t.Fatalf("property %q does not parse: %v", k, err)
		}
		out[k] = m
	}
	return out
}

// TestEventLogSchemaDeclaresAnOpenEventFilter reads the SHIPPED schema rather
// than a copy of it.
func TestEventLogSchemaDeclaresAnOpenEventFilter(t *testing.T) {
	props := eventLogSchema(t)

	// CONTROL: the walk found the schema's properties. Every assertion below is
	// satisfied by an empty map without this.
	if _, ok := props["level"]; !ok {
		t.Fatal("CONTROL: the schema declares no level property, so this walk is not reading " +
			"the get_event_log schema")
	}

	declared, ok := props["event"]
	if !ok {
		t.Fatal("get_event_log declares no event property, so a model has no way to ask for one " +
			"event and reads the whole log instead")
	}
	prop, _ := declared.(map[string]any)

	if got := prop["type"]; got != "array" {
		t.Errorf("event is declared as type %v, want array. An incident is normally read across "+
			"several events at once, and the platform's own filter key takes a list", got)
	}
	items, _ := prop["items"].(map[string]any)
	if items == nil || items["type"] != "string" {
		t.Errorf("event items are declared as %v, want type string", prop["items"])
	}

	// THE POINT OF THIS FILE. An enum here would refuse a legitimate name.
	if _, hasEnum := prop["enum"]; hasEnum {
		t.Errorf("event declares an enum %v. The vocabulary is open: application code writes its "+
			"own events with ЗаписьЖурналаРегистрации and names them itself, so a closed list "+
			"refuses values the base accepts", prop["enum"])
	}
	// CONTROL for the line above: level DOES declare one, so «no enum» is a fact
	// about this property and not about the reader.
	level, _ := props["level"].(map[string]any)
	if _, hasEnum := level["enum"]; !hasEnum {
		t.Error("CONTROL: level declares no enum either, so finding none on event says nothing " +
			"about event")
	}

	// The description has to carry what the enum cannot: the shape of the names,
	// some real ones, and the fact that custom names are allowed.
	desc, _ := prop["description"].(string)
	for _, want := range []string{"_$Session$_.Start", "ЗаписьЖурналаРегистрации"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the event description does not name %q. Without an enum the description is "+
				"the only place a model can learn what a name looks like:\n%s", want, desc)
		}
	}
}

// TestEventLogHandlerSendsTheEventFilter drives the real handler against a
// server that reads the body, so the filter is pinned where it leaves this
// process rather than where it is assigned.
func TestEventLogHandlerSendsTheEventFilter(t *testing.T) {
	var got onec.EventLogRequest
	var rawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		rawBody = string(b)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("the body this handler sent is not an EventLogRequest: %v (%s)", err, b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"events":[],"total":0}`))
	}))
	defer srv.Close()

	args, _ := json.Marshal(map[string]any{
		"event": []string{"_$Session$_.Start", "_$Data$_.Update"},
		"limit": 10,
	})
	_, err := NewEventLogHandler(onec.NewClient(srv.URL, "", ""))(context.Background(),
		&mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
			Name: "get_event_log", Arguments: args,
		}})
	if err != nil {
		t.Fatalf("the call was refused: %v", err)
	}

	// CONTROL: the server was reached and the body read. Without this an empty
	// Event slice below is indistinguishable from a request that never happened.
	if rawBody == "" {
		t.Fatal("CONTROL: the handler sent no body at all")
	}

	if len(got.Event) != 2 || got.Event[0] != "_$Session$_.Start" || got.Event[1] != "_$Data$_.Update" {
		t.Errorf("the event filter arrived as %q. A filter this side drops never reaches the "+
			"base, and the base then answers the log the caller did not ask for", got.Event)
	}
	// It must travel under the name the extension reads.
	if !strings.Contains(rawBody, `"event"`) {
		t.Errorf("the body carries no \"event\" member; ЖурналРегистрацииPOST reads the filter "+
			"under that name:\n%s", rawBody)
	}
}

// TestEventLogHandlerOmitsAnAbsentEventFilter is the control for the test above.
//
// A handler that always sent an event member would make the assertion there pass
// while breaking every call that asks for no event filter: the extension refuses
// an empty event outright, so an empty array on the wire turns a plain read of
// the log into a 400.
func TestEventLogHandlerOmitsAnAbsentEventFilter(t *testing.T) {
	var rawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rawBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"events":[],"total":0}`))
	}))
	defer srv.Close()

	args, _ := json.Marshal(map[string]any{"limit": 10})
	if _, err := NewEventLogHandler(onec.NewClient(srv.URL, "", ""))(context.Background(),
		&mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
			Name: "get_event_log", Arguments: args,
		}}); err != nil {
		t.Fatalf("the call was refused: %v", err)
	}

	if rawBody == "" {
		t.Fatal("CONTROL: the handler sent no body at all")
	}
	if strings.Contains(rawBody, `"event"`) {
		t.Errorf("a call with no event filter still carries an event member, and the extension "+
			"refuses an empty one:\n%s", rawBody)
	}
}

// TestEventLogHandlerRefusesAnEmptyEventName is the one check this side CAN
// make.
//
// The name itself cannot be validated here: the list lives in the base. An empty
// string is different. It is not a name in any base, it reaches the far side as a
// value that cannot be resolved, and it costs a round trip to learn that.
func TestEventLogHandlerRefusesAnEmptyEventName(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"events":[],"total":0}`))
	}))
	defer srv.Close()

	handler := NewEventLogHandler(onec.NewClient(srv.URL, "", ""))
	// WithToolErrors turns an operational error into a result carrying IsError,
	// not into a returned error, so BOTH have to be read. A test that watched only
	// the returned error would report every refusal on this path as an acceptance.
	call := func(v any) (refused bool, detail string) {
		args, _ := json.Marshal(map[string]any{"event": v, "limit": 10})
		res, err := handler(context.Background(), &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{Name: "get_event_log", Arguments: args},
		})
		if err != nil {
			return true, err.Error()
		}
		if res != nil && res.IsError {
			if len(res.Content) > 0 {
				if tc, ok := res.Content[0].(*mcp.TextContent); ok {
					return true, tc.Text
				}
			}
			return true, "(no text content)"
		}
		return false, ""
	}

	for _, bad := range [][]string{{""}, {"_$Session$_.Start", ""}, {"   "}} {
		refused, detail := call(bad)
		if !refused {
			t.Errorf("event=%q was accepted; an empty name selects nothing and the caller learns "+
				"that only after a round trip", bad)
			continue
		}
		// The refusal has to say WHICH value, or the caller cannot correct it.
		if !strings.Contains(detail, "имя события") {
			t.Errorf("event=%q was refused with text that does not name the problem:\n%s",
				bad, detail)
		}
	}
	if reached {
		t.Error("a refused call still reached 1С; the point of refusing here is that it does not")
	}

	// THE CONTROL. A handler that refused every event value would satisfy every
	// line above while making the filter unusable.
	if refused, detail := call([]string{"_$Session$_.Start"}); refused {
		t.Errorf("a real event name was refused too: %s. A check that refuses everything proves "+
			"nothing about the values it was written for", detail)
	}
	if !reached {
		t.Error("CONTROL: the accepted call did not reach 1С either, so the server was never the " +
			"thing being measured")
	}
}

// TestFormatEventLogShowsTheReadableEventName pins that the phrase the 1С event
// log window shows reaches the reader.
//
// Without it the reader is handed _$Data$_.Update and their own window says
// «Данные. Изменение», and nothing connects the two. That matters twice over
// here, because the identifier is also the only thing the filter accepts.
func TestFormatEventLogShowsTheReadableEventName(t *testing.T) {
	text := formatEventLog(&onec.EventLogResult{
		Events: []onec.EventLogEntry{{
			Date:              "2026-03-07T10:00:00",
			Level:             "Ошибка",
			Event:             "_$Data$_.Update",
			EventPresentation: "Данные. Изменение",
			User:              "Администратор",
		}},
		Total: 1,
	})

	for _, want := range []string{"_$Data$_.Update", "Данные. Изменение"} {
		if !strings.Contains(text, want) {
			t.Errorf("the rendered answer does not carry %q:\n%s", want, text)
		}
	}

	// AND IT MUST NOT INVENT ONE. A record whose representation is empty has to
	// render without the line, not with an empty one, exactly as computer,
	// metadata and the rest already do.
	bare := formatEventLog(&onec.EventLogResult{
		Events: []onec.EventLogEntry{{
			Date: "2026-03-07T10:00:00", Level: "Ошибка", Event: "_$Data$_.Update",
			User: "Администратор",
		}},
		Total: 1,
	})
	if strings.Contains(bare, eventPresentationLabel) {
		t.Errorf("a record with no representation still prints the %s line:\n%s",
			eventPresentationLabel, bare)
	}
	// CONTROL: the label is the one the filled record uses, so the absence above
	// is measured against something that does appear.
	if !strings.Contains(text, eventPresentationLabel) {
		t.Errorf("the filled record does not print %s either, so finding it missing from the "+
			"bare one proves nothing:\n%s", eventPresentationLabel, text)
	}
}
