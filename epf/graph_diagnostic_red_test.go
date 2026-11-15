package epf

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Source guard only; real native catch execution is a separate acceptance requirement.
func TestGraphDiagnosticPhaseSourceIsBounded(t *testing.T) {
	b, err := os.ReadFile("source/MCPConnector/Forms/Main/Ext/Form/Module.bsl")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	branch := strings.Index(s, `ИначеЕсли Задача.method = "POST" И ((Задача.path = "/document-read"`)
	if branch < 0 {
		t.Fatal("document-read branch missing")
	}
	tail := s[branch:]
	binding := strings.Index(tail, `ГрафЭтап = "graph_binding"`)
	guard := strings.Index(tail, `Если Задача.session <> ИсходныйТокен`)
	invoke := strings.Index(tail, `ГрафЭтап = "graph_invocation"`)
	response := strings.Index(tail, `ГрафЭтап = "graph_response"`)
	if binding < 0 || guard < 0 || !(binding < guard && guard < invoke && invoke < response) {
		t.Fatalf("graph phase assignment not in reachable branch/order: %d %d %d %d", binding, guard, invoke, response)
	}

}

// The existing bridge must carry a new EPF diagnostic in body without adding
// forbidden outer result fields; this is not an actual native EPF test.
func TestLegacyBridgeCarriesGraphDiagnosticBody(t *testing.T) {
	b, _ := New(token, nil)
	cutoff := time.Now().Add(time.Minute)
	b.EnableDocumentRead(func(context.Context) (time.Time, error) { return cutoff, nil })
	b.EnableDocumentDateWrite()
	b.EnableGraph()
	paired := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"core","graph":"graph_transfer/1"}`)
	var p struct{ Session string }
	if json.Unmarshal(paired.Body.Bytes(), &p) != nil || paired.Code != 200 {
		t.Fatal("pair failed")
	}
	ctx, err := b.PinDocumentUntil(context.Background(), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *http.Response, 1)
	fail := make(chan error, 1)
	go func() {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/document-read", strings.NewReader(graphWire("graph_capability", `{"action":"graph_capability","profile":"GRAPH-COUNTDOCS-1"}`)))
		v, e := b.RoundTrip(r)
		if e != nil {
			fail <- e
			return
		}
		done <- v
	}()
	select {
	case pending := <-b.queue:
		b.queue <- pending
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", p.Session, `{"identity":"native"}`)
	var task Task
	if json.Unmarshal(poll.Body.Bytes(), &task) != nil || task.ID == "" {
		t.Fatal("poll failed")
	}
	result := request(b, "POST", "/result/"+task.ID, p.Session, `{"identity":"native","status":400,"body":{"error":"native read refused; check current user rights","phase":"graph_invocation"}}`)
	if result.Code != 204 {
		t.Fatal("legacy outer envelope refused", result.Code)
	}
	select {
	case e := <-fail:
		t.Fatal(e)
	case response := <-done:
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		if e != nil || response.StatusCode != 400 || !strings.Contains(string(raw), `"phase":"graph_invocation"`) {
			t.Fatal("body not carried")
		}
	case <-time.After(time.Second):
		t.Fatal("bridge hung")
	}
}
