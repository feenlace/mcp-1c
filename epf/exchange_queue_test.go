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

const queueDescribe = `{"contract_version":1,"operation":"queue_describe","business":"{\"action\":\"queue_describe\",\"profile\":\"R12-QUEUE-ORG-1\",\"node_uuid\":\"11111111-1111-4111-8111-111111111111\",\"limit\":1}"}`

func queueOptIn(t *testing.T, b *Bridge) {
	t.Helper()
	route, ok := any(b).(interface{ EnableExchangeQueue() error })
	if !ok {
		t.Fatal("finite EPF queue opt-in absent")
	}
	if e := route.EnableExchangeQueue(); e != nil {
		t.Fatal(e)
	}
}
func TestExchangeQueueExactBytesGrantAndNoReplay(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	req := func(ctx context.Context, raw string) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/document-read", strings.NewReader(raw))
		return r
	}
	if _, e := b.RoundTrip(req(context.Background(), queueDescribe)); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("Community queue admission", e)
	}
	allowed := true
	cutoff := time.Now().Add(10 * time.Second)
	b.EnableDocumentRead(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	})
	queueOptIn(t, b)
	session := pair(t, b)
	if _, e := b.RoundTrip(req(context.Background(), queueDescribe)); !errors.Is(e, ErrHybridUnavailable) {
		t.Fatal("missing core admitted", e)
	}
	request(b, "DELETE", "/session", session, "")
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","read_exchange_queue":"queue_observation/1"}`)
	var paired struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &paired)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	pin, e := b.PinSession(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(pin)
	done := make(chan error, 1)
	go func() {
		r, e := b.RoundTrip(req(ctx, queueDescribe))
		if r != nil {
			r.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if p.task.BodyText != queueDescribe || p.task.Body != nil || p.task.Core != "real-core" || p.task.Session != paired.Session || !p.task.ExecuteBefore.Equal(cutoff) {
			t.Fatal("body/context/cutoff changed")
		}
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`)
	var task Task
	json.Unmarshal(poll.Body.Bytes(), &task)
	if task.Capability != "pro.exchange-queue" {
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
	if _, e := b.RoundTrip(req(pin, queueDescribe)); e == nil {
		t.Fatal("expired grant admitted")
	}
	allowed = true
	request(b, "DELETE", "/session", paired.Session, "")
	pair(t, b)
	if _, e := b.RoundTrip(req(pin, queueDescribe)); !errors.Is(e, ErrSessionChanged) {
		t.Fatal("cross-pair read admitted", e)
	}
}
func TestExchangeQueueMarkerAndClosedBody(t *testing.T) {
	for _, m := range []string{`null`, `"queue_observation/2"`, `"queue_observation/1","read_exchange_queue":"queue_observation/1"`} {
		b, _ := New(token, nil)
		if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"real","read_exchange_queue":`+m+`}`).Code != 400 {
			t.Fatal("invalid marker paired")
		}
	}
	b, _ := New(token, nil)
	defer b.Close()
	allowed := true
	b.EnableDocumentRead(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return time.Now().Add(time.Second), nil
	})
	queueOptIn(t, b)
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"real","read_exchange_queue":"queue_observation/1"}`)
	var session struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &session)
	for _, raw := range []string{strings.Replace(queueDescribe, `"contract_version":1`, `"contract_version":1,"code":"write"`, 1), strings.Replace(queueDescribe, `queue_describe`, `commit`, 1), strings.ReplaceAll(queueDescribe, `R12-QUEUE-ORG-1`, `unknown`)} {
		r, _ := http.NewRequest("POST", "http://epf.invalid/document-read", strings.NewReader(raw))
		if _, e := b.RoundTrip(r); e == nil {
			t.Fatal("invalid queue request admitted")
		}
	}
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequest("POST", "http://epf.invalid/document-read", strings.NewReader(queueDescribe))
		out, e := b.RoundTrip(r)
		if out != nil {
			out.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	allowed = false
	if request(b, "POST", "/poll", session.Session, `{"identity":"native"}`).Code != 204 {
		t.Fatal("expired grant handed")
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestExchangeQueueSelectedBodyLimitBeforeHandout(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	b.EnableDocumentRead(func(context.Context) (time.Time, error) { return time.Now().Add(time.Second), nil })
	queueOptIn(t, b)
	if w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"real","read_exchange_queue":"queue_observation/1"}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	oversized := queueDescribe + strings.Repeat(" ", 8193-len(queueDescribe))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/document-read", strings.NewReader(oversized))
	out, e := b.RoundTrip(r)
	if out != nil {
		out.Body.Close()
	}
	if e == nil || e.Error() != "EPF request body refused" || len(b.queue) != 0 || len(b.pending) != 0 {
		t.Fatalf("oversized queue request reached admission/handout: error=%v queued=%d pending=%d", e, len(b.queue), len(b.pending))
	}
	boundary := queueDescribe + strings.Repeat(" ", 8192-len(queueDescribe))
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/document-read", strings.NewReader(boundary))
		out, e := b.RoundTrip(r)
		if out != nil {
			out.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if p.task.Capability != "pro.exchange-queue" || p.task.BodyText != boundary {
			t.Fatal("8KiB boundary/exact body changed")
		}
	case <-time.After(time.Second):
		t.Fatal("8KiB boundary refused")
	}
	stop()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
