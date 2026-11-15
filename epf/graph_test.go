package epf

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

const graphID = "11111111-1111-4111-8111-111111111111"

func graphBusinessTest() string {
	return `{"business_contract":"graph_business/1","pair_binding":"` + strings.Repeat("a", 64) + `","source_identity":"source","target_identity":"target","action":"graph_import","profile":"GRAPH-COUNTDOCS-1","idempotency_key":"graph:` + graphID + `","target_base_token":"` + strings.Repeat("b", 64) + `","target_document_uuid":"22222222-2222-4222-8222-222222222222","target_organization_uuid":"33333333-3333-4333-8333-333333333333","source":{"profile":"GRAPH-COUNTDOCS-1","base_token":"` + strings.Repeat("c", 64) + `","schema":{"qualified":"native"},"document":{"uuid":"` + graphID + `","version":"v1","number":"G1","date":"2026-11-09T12:00:00","allowed":false,"organization_uuid":"44444444-4444-4444-8444-444444444444"},"organization":{"uuid":"44444444-4444-4444-8444-444444444444","version":"v2","code":"G1","description":"","allowed":false}}}`
}
func graphWire(operation, business string) string {
	raw, _ := json.Marshal(map[string]any{"contract_version": 1, "operation": operation, "business": business})
	return string(raw)
}
func TestGraphFiniteBodyAndMarker(t *testing.T) {
	good := graphBusinessTest()
	if !GraphReceiptBusiness([]byte(good)) || !graphBody([]byte(graphWire("graph_prepare", good)), false, false) || !graphBody([]byte(graphWire("receipt", good)), false, true) {
		t.Fatal("finite original rejected")
	}
	for _, bad := range []string{strings.Replace(good, `"graph_import"`, `"update"`, 1), strings.Replace(good, `"GRAPH-COUNTDOCS-1"`, `"UNF-COUNTERPARTY-1"`, 1), strings.Replace(good, `"allowed":false`, `"allowed":null`, 1), strings.Replace(good, `"allowed":false`, `"allowed":false,"allowed":true`, 1), strings.Replace(good, `"description":""`, `"description":"","code_execute":"x"`, 1), strings.Replace(good, `"schema":{"qualified":"native"}`, `"schema":null`, 1), strings.Replace(good, `"target_document_uuid":"22222222-2222-4222-8222-222222222222"`, `"target_document_uuid":"`+graphID+`"`, 1)} {
		if GraphReceiptBusiness([]byte(bad)) {
			t.Fatal("open/altered body admitted")
		}
	}
	for _, marker := range []string{`null`, `"graph_countdocs/2"`, `"graph_transfer/1","graph":"graph_transfer/1"`} {
		b, _ := New(token, nil)
		if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"core","graph":`+marker+`}`).Code != 400 {
			t.Fatal("invalid marker")
		}
		b.Close()
	}
}
func TestGraphIndependentPinsCutoffAndOwnerObservation(t *testing.T) {
	bridges := []*Bridge{}
	defer func() {
		for _, b := range bridges {
			b.Close()
		}
	}()
	var pins []context.Context
	var sessions []string
	allowed := true
	cutoff := time.Now().Add(10 * time.Second)
	original := cutoff
	for i := range 2 {
		b, _ := New(token, nil)
		bridges = append(bridges, b)
		b.EnableDocumentRead(func(context.Context) (time.Time, error) {
			if !allowed {
				return time.Time{}, ErrUnavailable
			}
			return cutoff, nil
		})
		b.EnableDocumentDateWrite()
		b.EnableGraph()
		w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"core","graph":"graph_transfer/1"}`)
		var out struct{ Session string }
		json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		sessions = append(sessions, out.Session)
		pin, e := b.PinDocumentUntil(context.Background(), original)
		if e != nil {
			t.Fatal(e)
		}
		pins = append(pins, pin)
		if i == 0 {
			cutoff = cutoff.Add(time.Hour)
		}
	}
	if got, ok := pins[1].Value(documentCutoffKey{}).(time.Time); !ok || !got.Equal(original) {
		t.Fatal("renewed grant extended target pin")
	}
	if _, e := bridges[1].PinSession(pins[0]); !errors.Is(e, ErrSessionChanged) {
		t.Fatal("cross-base pin accepted")
	}
	cutoff = cutoff.Add(time.Hour)
	run := func(b *Bridge, ctx context.Context, path, body string, paid bool) {
		done := make(chan error, 1)
		go func() {
			r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid"+path, strings.NewReader(body))
			out, e := b.RoundTrip(r)
			if out != nil {
				out.Body.Close()
			}
			done <- e
		}()
		select {
		case p := <-b.queue:
			if paid && !p.task.ExecuteBefore.Equal(original) {
				t.Fatal("original cutoff extended")
			}
			if p.task.Core != "core" || p.task.Session == "" {
				t.Fatal("missing binding")
			}
			b.queue <- p
		case <-time.After(time.Second):
			t.Fatal("not queued")
		}
		poll := request(b, "POST", "/poll", sessions[0], `{"identity":"native"}`)
		var task Task
		json.Unmarshal(poll.Body.Bytes(), &task)
		if task.ID == "" {
			t.Fatal("not handed")
		}
		// Fully completed late output is collected without a new entitlement check.
		allowed = false
		if request(b, "POST", "/result/"+task.ID, sessions[0], `{"identity":"native","status":200,"body":{"contract_version":1,"identity":"native","data":{"found":false}}}`).Code != 204 {
			t.Fatal("late completion lost")
		}
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	run(bridges[0], pins[0], "/document-read", graphWire("graph_capability", `{"action":"graph_capability","profile":"GRAPH-COUNTDOCS-1"}`), true)
	r, _ := http.NewRequestWithContext(pins[0], "POST", "http://epf.invalid/document-read", strings.NewReader(graphWire("graph_prepare", graphBusinessTest())))
	if _, e := bridges[0].RoundTrip(r); e == nil {
		t.Fatal("new paid work after expiry")
	}
	statusPin, e := bridges[0].PinSession(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	run(bridges[0], statusPin, "/document-read", graphWire("receipt", graphBusinessTest()), false)
}
