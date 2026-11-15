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

const qualityIdentity = `{"contract_version":"quality_read/1","run_id":"11111111-1111-4111-8111-111111111111","operation":"identity","definition":null,"timeout":30}`

func qualityOptIn(t *testing.T, b *Bridge) {
	t.Helper()
	route, ok := any(b).(interface{ EnableQuality() error })
	if !ok {
		t.Fatal("finite EPF quality opt-in absent")
	}
	if e := route.EnableQuality(); e != nil {
		t.Fatal(e)
	}
}
func TestQualityExactBytesGrantAndNoReplay(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	req := func(ctx context.Context, raw string) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/quality-read", strings.NewReader(raw))
		return r
	}
	if _, e := b.RoundTrip(req(context.Background(), qualityIdentity)); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("Community quality admission", e)
	}
	allowed := true
	cutoff := time.Now().Add(10 * time.Second)
	b.EnableDocumentRead(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	})
	qualityOptIn(t, b)
	session := pair(t, b)
	if _, e := b.RoundTrip(req(context.Background(), qualityIdentity)); !errors.Is(e, ErrHybridUnavailable) {
		t.Fatal("missing core admitted", e)
	}
	request(b, "DELETE", "/session", session, "")
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","quality":"quality_read/1"}`)
	var paired struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &paired)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	pin, e := b.PinQuality(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	originalCutoff := cutoff
	cutoff = cutoff.Add(time.Hour)
	ctx, cancel := context.WithCancel(pin)
	done := make(chan error, 1)
	go func() {
		r, e := b.RoundTrip(req(ctx, qualityIdentity))
		if r != nil {
			r.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if p.task.BodyText != qualityIdentity || p.task.Body != nil || p.task.Core != "real-core" || p.task.Session != paired.Session || !p.task.ExecuteBefore.Equal(originalCutoff) {
			t.Fatal("body/context/cutoff changed")
		}
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`)
	var task Task
	json.Unmarshal(poll.Body.Bytes(), &task)
	if task.Capability != "pro.quality-read" {
		t.Fatal("wrong native capability")
	}
	cancel()
	e = <-done
	var unknown *UnknownOutcome
	if !errors.As(e, &unknown) {
		t.Fatal("lost UnknownOutcome", e)
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
	if _, e := b.RoundTrip(req(pin, qualityIdentity)); e == nil {
		t.Fatal("expired grant admitted")
	}
	allowed = true
	request(b, "DELETE", "/session", paired.Session, "")
	pair(t, b)
	if _, e := b.RoundTrip(req(pin, qualityIdentity)); !errors.Is(e, ErrSessionChanged) {
		t.Fatal("cross-pair read admitted", e)
	}
}

func TestQualityClosedBodyLimitAndMarker(t *testing.T) {
	for _, m := range []string{`null`, `"quality_read/2"`, `"quality_read/1","quality":"quality_read/1"`} {
		b, _ := New(token, nil)
		if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"core","quality":`+m+`}`).Code != 400 {
			t.Fatal("bad marker accepted")
		}
		b.Close()
	}
	if !qualityBody([]byte(qualityIdentity)) {
		t.Fatal("required identity null rejected")
	}
	for _, raw := range []string{strings.Replace(qualityIdentity, `"identity"`, `"commit"`, 1), strings.Replace(qualityIdentity, `"definition":null`, `"definition":{}`, 1), strings.TrimSuffix(qualityIdentity, "}") + `,"query":"read"}`, strings.TrimSuffix(qualityIdentity, "}") + `,"operation":"identity"}`} {
		if qualityBody([]byte(raw)) {
			t.Fatal("invalid quality body admitted")
		}
	}
	b, _ := New(token, nil)
	defer b.Close()
	b.EnableDocumentRead(func(context.Context) (time.Time, error) { return time.Now().Add(time.Minute), nil })
	b.EnableQuality()
	r, _ := http.NewRequest("POST", "http://epf.invalid/quality-read", strings.NewReader(qualityIdentity+strings.Repeat(" ", 32769-len(qualityIdentity))))
	if _, e := b.RoundTrip(r); e == nil || len(b.queue) != 0 || len(b.pending) != 0 {
		t.Fatal("oversize entered queue")
	}
	if closedReadObject([]byte(`{"body":null}`), new(any), "body") {
		t.Fatal("other native caps gained nullable request values")
	}
}
