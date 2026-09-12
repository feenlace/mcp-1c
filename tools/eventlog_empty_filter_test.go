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
// AN EXPLICITLY EMPTY event WAS ERASED ON THE WIRE.
//
// omitempty fires on len == 0, not on nil, so {"event": []} left this process as
// a body with no event member at all. That body is byte for byte the body of a
// call that asked for no filter, and the answer to it is byte for byte the
// answer to that other question. The caller had no way to tell the two apart.
//
// That is the defect this endpoint's refusals exist to prevent, and the shipped
// handler says so in its own words: «Отбор, который применить нельзя,
// отклоняется, а не отбрасывается: молча отброшенный отбор давал ответ,
// побайтно равный ответу на запрос вообще без отбора, и отличить их вызывающий
// не мог».
//
// THE DISTINCTION SURVIVES THE DECODE, which is why this side can settle it: {}
// leaves Event nil, {"event": []} leaves it non-nil with length 0. Only the
// ENCODE loses it.
//
// The far side already refuses the state («event is empty; leave it out to read
// the log without an event filter»), and that refusal was unreachable from this
// client, because the member it fires on never arrived.
// ---------------------------------------------------------------------------

// eventLogProbe is one call driven through the real handler against a stand-in
// that filters the way the extension does.
type eventLogProbe struct {
	refused  bool
	detail   string
	answer   string
	wireBody string
	reached  bool
}

// driveEventLog runs the shipped handler against a recording stand-in.
//
// The stand-in holds three records under two event names, so a filtered answer
// and an unfiltered one DIFFER. A stand-in that answered the same records
// whatever it was asked would make every assertion below pass while the filter
// went missing, which is the failure this file is about.
func driveEventLog(t *testing.T, args map[string]any) eventLogProbe {
	t.Helper()
	var p eventLogProbe
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.reached = true
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		p.wireBody = string(b)

		var req struct {
			Event []string `json:"event"`
		}
		if err := json.Unmarshal(b, &req); err != nil {
			t.Errorf("the body this handler sent is not an event log request: %v (%s)", err, b)
		}
		records := []onec.EventLogEntry{
			{Date: "2026-03-07T14:00:00", Level: "Информация", Event: "_$Session$_.Start",
				EventPresentation: "Сеанс. Начало", User: "Администратор"},
			{Date: "2026-03-07T14:25:00", Level: "Предупреждение", Event: "_$Data$_.Post",
				User: "Бухгалтер"},
			{Date: "2026-03-07T14:30:00", Level: "Ошибка", Event: "_$Data$_.Update",
				User: "Администратор"},
		}
		if len(req.Event) > 0 {
			var kept []onec.EventLogEntry
			for _, e := range records {
				for _, want := range req.Event {
					if e.Event == want {
						kept = append(kept, e)
						break
					}
				}
			}
			records = kept
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(onec.EventLogResult{Events: records, Total: len(records)})
	}))
	defer srv.Close()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("the arguments do not marshal: %v", err)
	}
	res, err := NewEventLogHandler(onec.NewClient(srv.URL, "", ""))(context.Background(),
		&mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
			Name: "get_event_log", Arguments: raw,
		}})
	// WithToolErrors turns an operational error into a result carrying IsError
	// rather than into a returned error, so BOTH have to be read. A test that
	// watched only the returned error would report every refusal here as an
	// acceptance.
	if err != nil {
		p.refused, p.detail = true, err.Error()
		return p
	}
	if res == nil {
		t.Fatal("neither a result nor an error came back")
	}
	if res.IsError {
		p.refused = true
		p.detail = "(no text content)"
	}
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			if p.refused {
				p.detail = tc.Text
			} else {
				p.answer = tc.Text
			}
		}
	}
	return p
}

// TestEventLogHandlerRefusesAnExplicitlyEmptyEventFilter is the blocker.
//
// The three neighbouring states are driven together, because the refusal is only
// correct if the other two still behave: a handler that refused every call would
// satisfy the first assertion and break the tool.
func TestEventLogHandlerRefusesAnExplicitlyEmptyEventFilter(t *testing.T) {
	// STATE 1: no event member at all. Reads the log unfiltered.
	absent := driveEventLog(t, map[string]any{"limit": 10})
	if absent.refused {
		t.Fatalf("CONTROL: a call with no event filter was refused: %s. Every assertion below "+
			"measures the difference from THIS answer, and there is none if it does not exist",
			absent.detail)
	}
	if !absent.reached {
		t.Fatal("CONTROL: a call with no event filter never reached 1С, so nothing below is " +
			"comparing two answers")
	}
	if strings.Contains(absent.wireBody, `"event"`) {
		t.Errorf("a call with no event filter still carries an event member:\n%s", absent.wireBody)
	}
	for _, want := range []string{"_$Session$_.Start", "_$Data$_.Post", "_$Data$_.Update"} {
		if !strings.Contains(absent.answer, want) {
			t.Errorf("the unfiltered answer is missing %q, so it is not the whole log:\n%s",
				want, absent.answer)
		}
	}

	// STATE 2: a real name. Still filters.
	filtered := driveEventLog(t, map[string]any{"event": []string{"_$Session$_.Start"}, "limit": 10})
	if filtered.refused {
		t.Fatalf("a real event name was refused: %s. A check that refuses everything proves "+
			"nothing about the value it was written for", filtered.detail)
	}
	if strings.Contains(filtered.answer, "_$Data$_.Update") {
		t.Errorf("the filtered answer still carries a record the filter excluded:\n%s", filtered.answer)
	}
	if filtered.answer == absent.answer {
		t.Errorf("CONTROL: the filtered answer equals the unfiltered one, so this stand-in cannot "+
			"show a filter going missing and proves nothing about the one below:\n%s",
			filtered.answer)
	}

	// STATE 3: THE POINT OF THIS FILE. An explicitly empty array.
	empty := driveEventLog(t, map[string]any{"event": []string{}, "limit": 10})
	if !empty.refused {
		msg := "event=[] was accepted"
		if empty.answer == absent.answer {
			msg += ", and its answer is byte for byte the answer to a call that asked for no " +
				"filter at all, so the caller cannot tell which question was answered"
		}
		t.Errorf("%s.\nwire body: %s\nanswer:\n%s", msg, empty.wireBody, empty.answer)
	}
	if empty.reached {
		t.Error("a call carrying an empty event filter still reached 1С; refusing here is what " +
			"saves the round trip, and the far side's own refusal is the only other answer")
	}
	// The refusal has to say WHICH value and what to do instead, or the caller
	// cannot correct it.
	for _, want := range []string{"event", "без отбора"} {
		if !strings.Contains(empty.detail, want) {
			t.Errorf("the refusal does not carry %q, so it does not tell the caller how to "+
				"correct the call:\n%s", want, empty.detail)
		}
	}
}

// TestEventLogRequestKeepsTheEmptyEventDistinction pins the fact the refusal
// above rests on: the decode preserves what the encode loses.
//
// If this ever stops holding, the handler cannot tell an explicitly empty filter
// from an absent one and the refusal above becomes unimplementable rather than
// merely absent.
func TestEventLogRequestKeepsTheEmptyEventDistinction(t *testing.T) {
	var absent onec.EventLogRequest
	if err := json.Unmarshal([]byte(`{"limit":10}`), &absent); err != nil {
		t.Fatalf("decoding a request with no event member: %v", err)
	}
	if absent.Event != nil {
		t.Errorf("a body with no event member decoded to a non-nil Event %#v, so an absent "+
			"filter is indistinguishable from an empty one", absent.Event)
	}

	var present onec.EventLogRequest
	if err := json.Unmarshal([]byte(`{"event":[],"limit":10}`), &present); err != nil {
		t.Fatalf("decoding a request with an empty event member: %v", err)
	}
	if present.Event == nil {
		t.Error("an explicitly empty event member decoded to a nil Event, so the handler has " +
			"nothing left to refuse")
	}
	if len(present.Event) != 0 {
		t.Errorf("an explicitly empty event member decoded to %#v, not an empty slice",
			present.Event)
	}

	// CONTROL: the encode really does lose it, which is why the refusal has to
	// happen before the encode rather than be read off the wire.
	body, err := json.Marshal(present)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(body), `"event"`) {
		t.Errorf("CONTROL: the encode kept the event member (%s), so omitempty no longer erases "+
			"an empty array and the comment on onec.EventLogRequest.Event is describing "+
			"something else", body)
	}
}
