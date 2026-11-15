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

const reportComposeRequest = `{"contract_version":"report_compose/1","run_id":"11111111-1111-4111-8111-111111111111","operation":"compile","definition":{},"parameters":{}}`

func reportComposeOptIn(t *testing.T, b *Bridge) {
	t.Helper()
	route, ok := any(b).(interface{ EnableReportCompose() error })
	if !ok {
		t.Fatal("finite EPF report_compose opt-in absent")
	}
	if e := route.EnableReportCompose(); e != nil {
		t.Fatal(e)
	}
}
func TestReportComposeExactBytesGrantAndNoReplay(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	req := func(ctx context.Context, raw string) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/report-compose", strings.NewReader(raw))
		return r
	}
	if _, e := b.RoundTrip(req(context.Background(), reportComposeRequest)); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("Community report_compose admission", e)
	}
	allowed := true
	cutoff := time.Now().Add(10 * time.Second)
	b.EnableDocumentRead(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	})
	reportComposeOptIn(t, b)
	session := pair(t, b)
	if _, e := b.RoundTrip(req(context.Background(), reportComposeRequest)); !errors.Is(e, ErrHybridUnavailable) {
		t.Fatal("missing core admitted", e)
	}
	request(b, "DELETE", "/session", session, "")
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","report_compose":"report_compose/1"}`)
	var paired struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &paired)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	pin, e := any(b).(interface {
		PinReportCompose(context.Context) (context.Context, error)
	}).PinReportCompose(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	originalCutoff := cutoff
	cutoff = cutoff.Add(time.Hour)
	ctx, cancel := context.WithCancel(pin)
	done := make(chan error, 1)
	go func() {
		r, e := b.RoundTrip(req(ctx, reportComposeRequest))
		if r != nil {
			r.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if p.task.BodyText != reportComposeRequest || p.task.Body != nil || p.task.Core != "real-core" || p.task.Session != paired.Session || !p.task.ExecuteBefore.Equal(originalCutoff) {
			t.Fatal("body/context/cutoff changed")
		}
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`)
	var task Task
	json.Unmarshal(poll.Body.Bytes(), &task)
	if task.Capability != "pro.report-compose" {
		t.Fatal("wrong native capability")
	}
	cancel()
	e = <-done
	var unknown *UnknownOutcome
	if !errors.As(e, &unknown) {
		t.Fatal("lost UnknownOutcome", e)
	}
	if _, err := b.RoundTrip(req(context.Background(), reportComposeRequest)); !errors.Is(err, ErrUnavailable) {
		t.Fatal("new report while previous outcome unknown", err)
	}
	// Cancellation after handout retains exactly one completion; no replay.
	raw := ` {"exact":"<>&\u2028","number":12345678901234567890123456789012345678,"null":null} `
	payload, _ := json.Marshal(map[string]any{"identity": "native-owner", "status": 200, "body_text": raw})
	allowed = false
	if request(b, "POST", "/result/"+task.ID, paired.Session, string(payload)).Code != 204 {
		t.Fatal("late result refused")
	}
	status, body, ready, e := b.Observe(unknown)
	if e != nil || !ready || status != 200 || string(body) != raw || len(b.queue) != 0 {
		t.Fatal("late exact result/replay", e)
	}
	if _, e := b.RoundTrip(req(pin, reportComposeRequest)); e == nil {
		t.Fatal("expired grant admitted")
	}
	allowed = true
	request(b, "DELETE", "/session", paired.Session, "")
	pair(t, b)
	if _, e := b.RoundTrip(req(pin, reportComposeRequest)); !errors.Is(e, ErrSessionChanged) {
		t.Fatal("cross-pair read admitted", e)
	}
}

func TestReportComposeFiniteBodies(t *testing.T) {
	if reportComposeBody([]byte(strings.Replace(reportComposeRequest, `"definition":{}`, `"definition":{"label":"`+string([]byte{255})+`"}`, 1))) {
		t.Fatal("invalid UTF8 report accepted")
	}
	compile := strings.Replace(reportComposeRequest, `"parameters":{}`, `"parameters":{"organization":null}`, 1)
	compose := strings.Replace(compile, `"operation":"compile"`, `"operation":"compose","schema_xml":"<schema/>","expected_identity_sha256":"`+strings.Repeat("a", 64)+`"`, 1)
	capture := strings.Replace(strings.Replace(compose, "report_compose/1", "report_replay/1", 1), `"operation":"compose"`, `"operation":"capture","expected_context_sha256":""`, 1)
	replay := strings.Replace(strings.Replace(capture, `"capture"`, `"replay"`, 1), `"expected_context_sha256":""`, `"expected_context_sha256":"`+strings.Repeat("b", 64)+`"`, 1)
	for _, raw := range []string{compile, compose, capture, replay} {
		if !reportComposeBody([]byte(raw)) {
			t.Fatal("valid contract refused")
		}
	}
	for _, raw := range []string{strings.Replace(compile, `"compile"`, `"query"`, 1), strings.TrimSuffix(compile, "}") + `,"schema_xml":null}`, strings.TrimSuffix(compile, "}") + `,"run_id":"duplicate"}`, compile + `{}`, strings.Replace(capture, `"expected_context_sha256":""`, `"expected_context_sha256":null`, 1)} {
		if reportComposeBody([]byte(raw)) {
			t.Fatal("open contract admitted")
		}
	}
}

func TestReportComposeSelectedStoredResponseBudget(t *testing.T) {
	for _, size := range []int{8 << 20, (8 << 20) + 1} {
		raw := `{"value":"` + strings.Repeat("\\", (size-12)/2) + `"}`
		raw += strings.Repeat(" ", size-len(raw))
		for _, capability := range []string{"community.query", "pro.quality-read", "pro.report-compose"} {
			b, _ := New(token, nil)
			p := &pending{task: Task{ID: "budget", Capability: capability, CollectBefore: time.Now().Add(time.Minute)}, session: "original", identity: "native", handed: true, result: make(chan result, 1)}
			b.pending[p.task.ID] = p
			payload, _ := json.Marshal(map[string]any{"identity": "native", "status": 200, "body_text": raw})
			w := request(b, "POST", "/result/budget", "original", string(payload))
			if capability == "pro.report-compose" && size == 8<<20 {
				if w.Code != 204 {
					t.Fatal("selected response boundary", w.Code)
				}
			} else if w.Code < 400 {
				t.Fatal("borrowed/oversize budget", capability, size, w.Code)
			}
			b.Close()
		}
	}
}

func TestReportComposeInputBoundary(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	b.EnableDocumentRead(func(context.Context) (time.Time, error) { return time.Now().Add(time.Minute), nil })
	b.EnableReportCompose()
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"reader","core":"core","report_compose":"report_compose/1"}`)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, size := range []int{(1 << 20) + 1, 1 << 20} {
		raw := reportComposeRequest + strings.Repeat(" ", size-len(reportComposeRequest))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/report-compose", strings.NewReader(raw))
			_, err := b.RoundTrip(r)
			done <- err
		}()
		if size > 1<<20 {
			if err := <-done; err == nil || len(b.queue) != 0 {
				t.Fatal("oversized report admitted")
			}
			cancel()
			continue
		}
		select {
		case p := <-b.queue:
			if len(p.task.BodyText) != size {
				t.Fatal("boundary bytes changed")
			}
		case <-time.After(time.Second):
			t.Fatal("exact boundary not queued")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}
