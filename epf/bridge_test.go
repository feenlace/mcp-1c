package epf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/feenlace/mcp-1c/onec"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const token = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

func request(b *Bridge, method, path, auth, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:9000"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+auth)
	w := httptest.NewRecorder()
	b.Handler("127.0.0.1:9000").ServeHTTP(w, r)
	return w
}
func pairCode(b *Bridge) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pairing
}
func pair(t *testing.T, b *Bridge) string {
	t.Helper()
	b.mu.Lock()
	code := b.pairing
	b.mu.Unlock()
	w := request(b, "POST", "/pair", code, `{"protocol":1,"identity":"native-owner"}`)
	if w.Code != 200 {
		t.Fatalf("pair status %d", w.Code)
	}
	var v struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	return v.Session
}
func enqueue(t *testing.T, b *Bridge, ctx context.Context) (*pending, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequestWithContext(ctx, "GET", "http://epf.invalid/configuration", nil)
		out, e := b.RoundTrip(r)
		if out != nil {
			out.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		b.queue <- p
		return p, done
	case <-time.After(time.Second):
		t.Fatal("task not queued")
	}
	return nil, done
}
func TestPairAndBoundaries(t *testing.T) {
	b, _ := New(token, nil)
	if request(b, "POST", "/pair", "wrong", `{}`).Code != 401 {
		t.Fatal("unauthenticated pair")
	}
	for _, body := range []string{`{"protocol":1,"identity":"owner","identity":"other"}`, `{"protocol":1,"Identity":"owner"}`, `{"protocol":1,"identity":"owner","code":"execute"}`} {
		if request(b, "POST", "/pair", token, body).Code != 400 {
			t.Fatal("ambiguous pairing envelope")
		}
	}
	session := pair(t, b)
	if request(b, "POST", "/pair", token, `{"protocol":1,"identity":"other"}`).Code != 401 {
		t.Fatal("replacement accepted")
	}
	if request(b, "POST", "/poll", session, `{"identity":"other"}`).Code != 409 {
		t.Fatal("identity drift")
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:9000/poll", strings.NewReader(`{"identity":"native-owner"}`))
	r.Header.Set("Authorization", "Bearer "+session)
	r.Header.Set("Origin", "http://evil.invalid")
	w := httptest.NewRecorder()
	b.Handler(r.Host).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("browser origin accepted")
	}
	r, _ = http.NewRequest("POST", "http://epf.invalid/execute", bytes.NewBufferString(`{}`))
	if _, err := b.RoundTrip(r); err == nil {
		t.Fatal("arbitrary execution route")
	}
}
func TestLateResultDoesNotReadmitAndIsSessionBound(t *testing.T) {
	var entitled atomic.Bool
	entitled.Store(true)
	b, _ := New(token, func(context.Context, Task) error {
		if !entitled.Load() {
			return ErrUnavailable
		}
		return nil
	})
	session := pair(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, done := enqueue(t, b, ctx)
	w := request(b, "POST", "/poll", session, `{"identity":"native-owner"}`)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	entitled.Store(false)
	b.mu.Lock()
	p.task.ExecuteBefore = time.Now().Add(-time.Second)
	b.mu.Unlock()
	body := `{"identity":"native-owner","status":200,"body":{"name":"synthetic"}}`
	if request(b, "POST", "/result/"+p.task.ID, "foreign", body).Code != 409 {
		t.Fatal("foreign result accepted")
	}
	if request(b, "POST", "/result/"+p.task.ID, session, body).Code != 204 {
		t.Fatal("late entitled-at-dispatch result lost")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if request(b, "POST", "/result/"+p.task.ID, session, body).Code != 409 {
		t.Fatal("duplicate result accepted")
	}
}
func TestQueuedTaskRechecksAdmission(t *testing.T) {
	b, _ := New(token, func(context.Context, Task) error { return ErrUnavailable })
	session := pair(t, b)
	p, done := enqueue(t, b, context.Background())
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 204 {
		t.Fatal("denied task handed out")
	}
	if p.handed {
		t.Fatal("denied task admitted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestExistingClientEnvelope(t *testing.T) {
	b, _ := New(token, nil)
	session := pair(t, b)
	done := make(chan error, 1)
	go func() {
		client := onec.NewClient("http://epf.invalid", "", "")
		client.HTTPClient.Transport = b
		var out onec.QueryResult
		err := client.Post(context.Background(), "/query", onec.QueryRequest{Query: "SELECT 1", Limit: 1}, &out)
		if err == nil && (len(out.Rows) != 1 || out.Total != 1 || out.Columns[0] != "N") {
			t.Error("lost existing client native envelope")
		}
		done <- err
	}()
	var task Task
	for range 100 {
		w := request(b, "POST", "/poll", session, `{"identity":"native-owner"}`)
		if w.Code == 200 {
			_ = json.Unmarshal(w.Body.Bytes(), &task)
			break
		}
		time.Sleep(time.Millisecond)
	}
	if task.Capability != "community.query" {
		t.Fatal("wrong capability")
	}
	w := request(b, "POST", "/result/"+task.ID, session, `{"identity":"native-owner","status":200,"body":{"columns":["N"],"rows":[[1]],"total":1,"truncated":false}}`)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestStopClosesNewAdmissionButKeepsOutstandingResult(t *testing.T) {
	b, _ := New(token, nil)
	session := pair(t, b)
	p, done := enqueue(t, b, context.Background())
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("no task")
	}
	if request(b, "DELETE", "/session", session, "").Code != 204 {
		t.Fatal("stop refused")
	}
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 401 {
		t.Fatal("stopped session still dispatches")
	}
	if request(b, "POST", "/result/"+p.task.ID, session, `{"identity":"native-owner","status":200,"body":{}}`).Code != 204 {
		t.Fatal("stopped session lost admitted result")
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestCloseUnblocksOutstandingCaller(t *testing.T) {
	b, _ := New(token, nil)
	_ = pair(t, b)
	_, done := enqueue(t, b, context.Background())
	b.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("close reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock caller")
	}
}

func TestRealCancellationBeforePollNeverDispatches(t *testing.T) {
	b, _ := New(token, nil)
	session := pair(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	p, done := enqueue(t, b, ctx)
	cancel()
	if e := <-done; e == nil {
		t.Fatal("cancelled caller reported success")
	}
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 204 || p.handed {
		t.Fatal("cancelled request was dispatched after caller exit")
	}
}
func TestActualDeadlineRetainsLateTerminal(t *testing.T) {
	b, _ := New(token, nil)
	session := pair(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	p, done := enqueue(t, b, ctx)
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("task not handed before deadline")
	}
	err := <-done
	var ticket *UnknownOutcome
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &ticket) {
		t.Fatal("real expired handed caller did not retain its unknown ticket")
	}
	if _, _, complete, e := b.Observe(ticket); e != nil || complete {
		t.Fatal("unknown outcome falsely terminal before reply")
	}

	if request(b, "POST", "/result/"+p.task.ID, session, `{"identity":"native-owner","status":200,"body":{"retained":true}}`).Code != 204 {
		t.Fatal("late result refused")
	}
	b.mu.Lock()
	retained := b.pending[p.task.ID] == p
	b.mu.Unlock()
	if !retained {
		t.Fatal("accepted late result became unreachable after real caller deadline")
	}
	status, body, complete, e := b.Observe(ticket)
	if e != nil || !complete || status != 200 || string(body) != `{"retained":true}` {
		t.Fatal("original caller cannot observe late terminal")
	}
	body[0] = '['
	_, copyBody, _, _ := b.Observe(ticket)
	if string(copyBody) != `{"retained":true}` {
		t.Fatal("observer mutated retained evidence")
	}
	other, _ := New(token, nil)
	if _, _, _, e = other.Observe(ticket); e == nil {
		t.Fatal("foreign bridge observed outcome")
	}
	if _, _, _, e = b.Observe(&UnknownOutcome{}); e == nil {
		t.Fatal("forged ticket observed outcome")
	}
	if request(b, "POST", "/result/"+p.task.ID, session, `{"identity":"native-owner","status":200,"body":{"replaced":true}}`).Code != 409 {
		t.Fatal("duplicate replaced terminal")
	}
	b.mu.Lock()
	p.task.CollectBefore = time.Now().Add(-time.Second)
	b.mu.Unlock()
	if _, _, _, e = b.Observe(ticket); !errors.Is(e, ErrOutcomeUnavailable) {
		t.Fatal("expired retained result still observable")
	}

}

func TestAlreadyCancelledNeverEnqueues(t *testing.T) {
	b, _ := New(token, nil)
	session := pair(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", "http://epf.invalid/configuration", nil)
	if _, e := b.RoundTrip(r); !errors.Is(e, context.Canceled) {
		t.Fatal("already-cancelled request not refused")
	}
	if len(b.pending) != 0 || len(b.queue) != 0 || request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 204 {
		t.Fatal("already-cancelled request queued")
	}
}
func TestHandedCancellationRetainsBoundTerminal(t *testing.T) {
	b, _ := New(token, nil)
	session := pair(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	p, done := enqueue(t, b, ctx)
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("no handout")
	}
	cancel()
	err := <-done
	var ticket *UnknownOutcome
	if !errors.Is(err, context.Canceled) || !errors.As(err, &ticket) {
		t.Fatal("handed cancellation erased UNKNOWN")
	}
	request(b, "DELETE", "/session", session, "")
	newSession := pair(t, b)
	body := `{"identity":"native-owner","status":200,"body":{"late":true}}`
	if request(b, "POST", "/result/"+p.task.ID, newSession, body).Code != 409 {
		t.Fatal("new session replaced old outcome")
	}
	if request(b, "POST", "/result/"+p.task.ID, session, body).Code != 204 {
		t.Fatal("original admitted result refused")
	}
	if _, body, complete, e := b.Observe(ticket); e != nil || !complete || string(body) != `{"late":true}` {
		t.Fatal("cancelled handed result not retained")
	}
	if request(b, "POST", "/poll", newSession, `{"identity":"native-owner"}`).Code != 204 {
		t.Fatal("cancelled handed task replayed")
	}
}

func TestCancellationDuringAdmissionNeverHandsOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, _ := New(token, func(context.Context, Task) error { cancel(); return nil })
	session := pair(t, b)
	p, done := enqueue(t, b, ctx)
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 204 || p.handed {
		t.Fatal("cancellation during admission still handed out")
	}
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal("cancelled undispatched caller outcome changed")
	}
}

// Wait for the actual configured context cancellation before returning, so the
// outer http.Client timeout branch deterministically wins on the unfixed client.
type settledContextTransport struct{ *Bridge }

func (s settledContextTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	out, err := s.Bridge.RoundTrip(r)
	if err != nil {
		<-r.Context().Done()
	}
	return out, err
}

func clientRequest(t *testing.T, b *Bridge, c *onec.Client, ctx context.Context) (*pending, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { var out onec.ConfigurationInfo; done <- c.Get(ctx, "/configuration", &out) }()
	select {
	case p := <-b.queue:
		b.queue <- p
		return p, done
	case <-time.After(time.Second):
		t.Fatal("real client did not queue")
	}
	return nil, done
}

func TestOneCConfiguredTimeoutPreservesOriginalOutcome(t *testing.T) {
	b, _ := New(token, nil)
	session := pair(t, b)
	c := onec.NewClient("http://epf.invalid", "", "", onec.WithRequestTimeout(120*time.Millisecond))
	c.HTTPClient.Transport = settledContextTransport{b}
	p, done := clientRequest(t, b, c, context.Background())
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("real client not handed out before timeout")
	}
	err := <-done
	var ticket *UnknownOutcome
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &ticket) {
		t.Fatal("configured onec/http timeout erased original observation ticket")
	}
	if c.HTTPClient.Timeout != 120*time.Millisecond {
		t.Fatal("configured timeout was changed")
	}
	if request(b, "POST", "/result/"+p.task.ID, session, `{"identity":"native-owner","status":200,"body":{"name":"late-native"}}`).Code != 204 {
		t.Fatal("late native result refused")
	}
	if status, body, complete, e := b.Observe(ticket); e != nil || !complete || status != 200 || string(body) != `{"name":"late-native"}` {
		t.Fatal("actual onec client cannot observe its late terminal result")
	}
}
func TestOneCCallerCancellationPreservesOriginalOutcome(t *testing.T) {
	b, _ := New(token, nil)
	session := pair(t, b)
	c := onec.NewClient("http://epf.invalid", "", "", onec.WithRequestTimeout(5*time.Second))
	c.HTTPClient.Transport = settledContextTransport{b}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, done := clientRequest(t, b, c, ctx)
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("no native handout")
	}
	cancel()
	err := <-done
	var ticket *UnknownOutcome
	if !errors.Is(err, context.Canceled) || !errors.As(err, &ticket) {
		t.Fatal("caller cancellation lost original ticket")
	}
	if request(b, "POST", "/result/"+p.task.ID, session, `{"identity":"native-owner","status":200,"body":{}}`).Code != 204 {
		t.Fatal("cancelled admitted result refused")
	}
	if _, _, complete, e := b.Observe(ticket); e != nil || !complete {
		t.Fatal("cancelled client cannot observe terminal")
	}
}
