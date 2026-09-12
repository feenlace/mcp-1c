package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A STAND-IN THAT CANNOT FAIL IS NOT A WITNESS.
//
// handleEventLog exists so a caller can be driven end to end without a 1C base.
// Its worth is entirely in the places where it says NO, because those are the
// places a defect between the caller and the base shows up. Where it answers the
// same records whatever it is asked, a filter can go missing on the way here and
// nothing notices: the answer to the question asked and the answer to a question
// nobody asked are the same bytes.
//
// The three things it owes the shipped handler are checked here: total counts
// the records of THIS answer, the user filter is applied, and a name the base
// does not list is refused with that name in the text rather than answered with
// an empty log.
// ---------------------------------------------------------------------------

// postEventLog drives handleEventLog with a JSON body and returns what it said.
func postEventLog(t *testing.T, body string) (status int, payload map[string]any, raw string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp/eventlog", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleEventLog(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	raw = rec.Body.String()
	if raw == "" {
		t.Fatalf("CONTROL: the handler wrote no body at all for %s", body)
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("the answer to %s is not JSON: %v (%s)", body, err, raw)
	}
	return res.StatusCode, payload, raw
}

// records returns the events array of a successful answer.
func records(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	arr, ok := payload["events"].([]any)
	if !ok {
		t.Fatalf("the answer carries no events array: %v", payload)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("an events member is not an object: %v", e)
		}
		out = append(out, m)
	}
	return out
}

func total(t *testing.T, payload map[string]any) int {
	t.Helper()
	v, ok := payload["total"].(float64)
	if !ok {
		t.Fatalf("the answer carries no numeric total: %v", payload)
	}
	return int(v)
}

// TestMockEventLog_TotalCountsTheRecordsItAnswers pins F2.
//
// The shipped handler builds total from the array that leaves, AFTER the limit
// has been applied, and says so in its own words: «total» считает записи ЭТОГО
// ответа. A stand-in that counted the matches instead would print a number the
// product never prints, and a reader comparing the two would call the product
// wrong.
func TestMockEventLog_TotalCountsTheRecordsItAnswers(t *testing.T) {
	// CONTROL: unlimited, so the numbers below are measured against a known one.
	_, all, _ := postEventLog(t, `{}`)
	if got, n := total(t, all), len(records(t, all)); got != n {
		t.Fatalf("CONTROL: an unlimited read answers %d records under total %d; the two cannot "+
			"disagree here and nothing below is measuring a limit", n, got)
	}
	unlimited := len(records(t, all))
	if unlimited < 2 {
		t.Fatalf("CONTROL: the stand-in holds %d records, too few for a limit to cut anything",
			unlimited)
	}

	for _, limit := range []int{1, 2} {
		_, payload, raw := postEventLog(t, `{"limit":`+itoa(limit)+`}`)
		got := records(t, payload)
		if len(got) != limit {
			t.Errorf("limit %d answered %d records, want %d:\n%s", limit, len(got), limit, raw)
		}
		if total(t, payload) != len(got) {
			t.Errorf("limit %d answered %d records under total %d. The shipped handler counts "+
				"the records of THIS answer, so a stand-in that counts the matches teaches a "+
				"reader the wrong shape:\n%s", limit, len(got), total(t, payload), raw)
		}
	}
}

// TestMockEventLog_AppliesTheUserFilter pins the first half of F3.
//
// user was accepted and never read, so every call carrying it got the whole log
// back. That is the exact shape a dropped filter has, and it is the shape this
// stand-in exists to make visible.
func TestMockEventLog_AppliesTheUserFilter(t *testing.T) {
	_, all, _ := postEventLog(t, `{}`)
	unlimited := len(records(t, all))

	status, payload, raw := postEventLog(t, `{"user":"Бухгалтер"}`)
	if status != http.StatusOK {
		t.Fatalf("a known user was refused with %d:\n%s", status, raw)
	}
	got := records(t, payload)
	if len(got) == 0 {
		t.Fatalf("the user filter selected nothing at all, so it cannot be shown to select "+
			"correctly:\n%s", raw)
	}
	if len(got) == unlimited {
		t.Errorf("filtering by user answered all %d records, which is what an unread filter "+
			"answers:\n%s", unlimited, raw)
	}
	for _, e := range got {
		if e["user"] != "Бухгалтер" {
			t.Errorf("the user filter kept a record belonging to %v:\n%s", e["user"], raw)
		}
	}
	if total(t, payload) != len(got) {
		t.Errorf("the filtered answer carries %d records under total %d:\n%s",
			len(got), total(t, payload), raw)
	}
}

// TestMockEventLog_RefusesAValueTheBaseDoesNotList pins the second half of F3.
//
// An unknown name used to come back as «no records», which is the answer to a
// different question: it says the base logged nothing of that kind, when in fact
// nothing of that kind exists to log. The shipped handler answers 400 and names
// the value, so the caller can correct it.
func TestMockEventLog_RefusesAValueTheBaseDoesNotList(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"unknown event", `{"event":["_$Session$_.Nope"]}`, "_$Session$_.Nope"},
		{"unknown user", `{"user":"Кладовщик"}`, "Кладовщик"},
		{"empty event list", `{"event":[]}`, "event is empty"},
		{"event member is not a name", `{"event":[""]}`, "event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, payload, raw := postEventLog(t, tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("%s answered %d, want 400. An answer here is indistinguishable from "+
					"an answer to a question that was asked:\n%s", tc.body, status, raw)
			}
			detail, _ := payload["error"].(string)
			if !strings.Contains(detail, tc.want) {
				t.Errorf("the refusal does not carry %q, so the caller cannot correct the "+
					"value:\n%s", tc.want, detail)
			}
		})
	}
}

// TestMockEventLog_KnownNameWithNoRecordsIsNotARefusal is the control for the
// test above, and it is the distinction the whole of F3 turns on.
//
// A name the base LISTS but has logged nothing under is an empty answer, not a
// refusal. Without this a stand-in could satisfy every line above by refusing
// every event name, and «no records» would have no way to mean anything.
func TestMockEventLog_KnownNameWithNoRecordsIsNotARefusal(t *testing.T) {
	status, payload, raw := postEventLog(t, `{"event":["_$Session$_.Finish"]}`)
	if status != http.StatusOK {
		t.Fatalf("a name this stand-in lists was refused with %d; then «no records» can never "+
			"be told from «no such name»:\n%s", status, raw)
	}
	if n := len(records(t, payload)); n != 0 {
		t.Errorf("the stand-in holds %d records under a name it was not meant to have logged "+
			"anything for, so this control measures nothing:\n%s", n, raw)
	}
	if total(t, payload) != 0 {
		t.Errorf("an empty answer carries total %d:\n%s", total(t, payload), raw)
	}

	// AND THE OTHER SIDE OF IT: a name that IS logged still comes back filtered.
	status, payload, raw = postEventLog(t, `{"event":["_$Session$_.Start"]}`)
	if status != http.StatusOK {
		t.Fatalf("a logged event name was refused with %d:\n%s", status, raw)
	}
	got := records(t, payload)
	if len(got) == 0 {
		t.Fatalf("CONTROL: a logged event name selected nothing:\n%s", raw)
	}
	for _, e := range got {
		if e["event"] != "_$Session$_.Start" {
			t.Errorf("the event filter kept a record of %v:\n%s", e["event"], raw)
		}
	}
}

// itoa keeps the bodies above readable without pulling strconv into the file for
// one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
