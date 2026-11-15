package epf

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const reconcileIdentity = `{"contract_version":"reconcile_read/1","run_id":"11111111-1111-4111-8111-111111111111","operation":"identity"}`

func reconcileOptIn(t *testing.T, b *Bridge) {
	t.Helper()
	route, ok := any(b).(interface{ EnableReconcile() error })
	if !ok {
		t.Fatal("finite EPF reconcile opt-in absent")
	}
	if e := route.EnableReconcile(); e != nil {
		t.Fatal(e)
	}
}
func TestReconcileExactBytesGrantAndNoReplay(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	req := func(ctx context.Context, raw string) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/reconcile-read", strings.NewReader(raw))
		return r
	}
	if _, e := b.RoundTrip(req(context.Background(), reconcileIdentity)); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("Community reconcile admission", e)
	}
	allowed := true
	cutoff := time.Now().Add(10 * time.Second)
	b.EnableDocumentRead(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	})
	reconcileOptIn(t, b)
	session := pair(t, b)
	if _, e := b.RoundTrip(req(context.Background(), reconcileIdentity)); !errors.Is(e, ErrHybridUnavailable) {
		t.Fatal("missing core admitted", e)
	}
	request(b, "DELETE", "/session", session, "")
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","reconcile":"reconcile_read/1"}`)
	var paired struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &paired)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	pin, e := b.PinReconcile(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	originalCutoff := cutoff
	cutoff = cutoff.Add(time.Hour)
	ctx, cancel := context.WithCancel(pin)
	done := make(chan error, 1)
	go func() {
		r, e := b.RoundTrip(req(ctx, reconcileIdentity))
		if r != nil {
			r.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if p.task.BodyText != reconcileIdentity || p.task.Body != nil || p.task.Core != "real-core" || p.task.Session != paired.Session || !p.task.ExecuteBefore.Equal(originalCutoff) {
			t.Fatal("body/context/cutoff changed")
		}
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`)
	var task Task
	json.Unmarshal(poll.Body.Bytes(), &task)
	if task.Capability != "pro.reconcile-read" {
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
	if _, e := b.RoundTrip(req(pin, reconcileIdentity)); e == nil {
		t.Fatal("expired grant admitted")
	}
	allowed = true
	request(b, "DELETE", "/session", paired.Session, "")
	pair(t, b)
	if _, e := b.RoundTrip(req(pin, reconcileIdentity)); !errors.Is(e, ErrSessionChanged) {
		t.Fatal("cross-pair read admitted", e)
	}
}

func TestReconcileMarkerAndClosedEnvelope(t *testing.T) {
	for _, marker := range []string{`null`, `"reconcile_read/2"`, `"reconcile_read/1","reconcile":"reconcile_read/1"`} {
		b, _ := New(token, nil)
		if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"real","reconcile":`+marker+`}`).Code != 400 {
			t.Fatal("invalid reconcile marker paired")
		}
		b.Close()
	}
	for _, raw := range []string{
		strings.Replace(reconcileIdentity, `"operation":"identity"`, `"operation":"commit"`, 1),
		strings.TrimSuffix(reconcileIdentity, "}") + `,"package_text":null}`,
		strings.TrimSuffix(reconcileIdentity, "}") + `,"code":"write"}`,
		strings.TrimSuffix(reconcileIdentity, "}") + `,"operation":"identity"}`,
		strings.Replace(reconcileIdentity, `"identity"`, `"selection"`, 1),
	} {
		if reconcileBody([]byte(raw)) {
			t.Fatal("open or malformed body admitted")
		}
	}
	selected := `{"contract_version":"reconcile_read/1","run_id":"11111111-1111-4111-8111-111111111111","operation":"selection","package_text":"{\"query\":\"SELECT 1\",\"run_id\":\"11111111-1111-4111-8111-111111111111\"}"}`
	if !reconcileBody([]byte(selected)) {
		t.Fatal("fixed package rejected")
	}
	b, _ := New(token, nil)
	defer b.Close()
	b.EnableDocumentRead(func(context.Context) (time.Time, error) { return time.Now().Add(time.Minute), nil })
	b.EnableReconcile()
	req, _ := http.NewRequest("POST", "http://epf.invalid/reconcile-read", strings.NewReader(reconcileIdentity+strings.Repeat(" ", 1064961-len(reconcileIdentity))))
	if _, e := b.RoundTrip(req); e == nil || len(b.pending) != 0 || len(b.queue) != 0 {
		t.Fatal("oversize entered admission")
	}
}

func TestReconcilePreservesNativeWrapperResponseBudget(t *testing.T) {
	const limit = 1064960
	for _, n := range []int{1048577, limit, limit + 1} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			b, _ := New(token, nil)
			defer b.Close()
			b.session = "session"
			b.identity = "native"
			p := &pending{task: Task{ID: "budget", Capability: "pro.reconcile-read", CollectBefore: time.Now().Add(time.Minute)}, session: "session", identity: "native", handed: true, result: make(chan result, 1)}
			b.pending[p.task.ID] = p
			raw := `{"x":"` + strings.Repeat("a", n-8) + `"}`
			payload, _ := json.Marshal(map[string]any{"identity": "native", "status": 200, "body_text": raw})
			reply := request(b, "POST", "/result/budget", "session", string(payload))
			if n <= limit {
				if reply.Code != 204 {
					t.Fatal("valid native wrapper dropped", n, reply.Code)
				}
			} else if reply.Code != 400 {
				t.Fatal("oversize wrapper accepted", reply.Code)
			}
			if n == 1048577 {
				legacy := &pending{task: Task{ID: "legacy", Capability: "advanced.query-package", CollectBefore: time.Now().Add(time.Minute)}, session: "session", identity: "native", handed: true, result: make(chan result, 1)}
				b.pending[legacy.task.ID] = legacy
				if reply := request(b, "POST", "/result/legacy", "session", string(payload)); reply.Code != 400 {
					t.Fatal("legacy response limit changed", reply.Code)
				}
			}
		})
	}
	// JSON-string transport can almost double valid raw bytes; only this route gets that finite outer budget.
	b, _ := New(token, nil)
	defer b.Close()
	b.session = "session"
	b.identity = "native"
	p := &pending{task: Task{ID: "escaped", Capability: "pro.reconcile-read", CollectBefore: time.Now().Add(time.Minute)}, session: "session", identity: "native", handed: true, result: make(chan result, 1)}
	b.pending[p.task.ID] = p
	raw := `"` + strings.Repeat(`\\`, (limit-2)/2) + `"`
	if len(raw) != limit || !json.Valid([]byte(raw)) {
		t.Fatal("invalid boundary fixture")
	}
	payload, _ := json.Marshal(map[string]any{"identity": "native", "status": 200, "body_text": raw})
	if len(payload) <= MaxBody {
		t.Fatal("fixture does not cross original encoded limit")
	}
	if reply := request(b, "POST", "/result/escaped", "session", string(payload)); reply.Code != 204 {
		t.Fatal("valid escaped envelope dropped", reply.Code)
	}
}

func TestReconcileBudgetUsesStoredTaskAndKeepsRetainedBinding(t *testing.T) {
	raw := `{"x":"` + strings.Repeat("a", 1048577-8) + `"}`
	for _, capability := range []string{"community.query", "advanced.query-package", "pro.reconcile-read"} {
		b, _ := New(token, nil)
		p := &pending{task: Task{ID: "retained", Capability: capability, CollectBefore: time.Now().Add(time.Minute)}, session: "original", identity: "native", handed: true, result: make(chan result, 1)}
		b.pending[p.task.ID] = p
		// No active session, opt-in or live entitlement remains; only the original outstanding task owns collection.
		payload, _ := json.Marshal(map[string]any{"identity": "native", "status": 200, "body_text": raw})
		reply := request(b, "POST", "/result/retained", "original", string(payload))
		if capability == "pro.reconcile-read" {
			if reply.Code != 204 {
				t.Fatal("retained reconcile result refused", reply.Code)
			}
			_, body, ready, e := b.Observe(&UnknownOutcome{bridge: b, request: p})
			if e != nil || !ready || string(body) != raw {
				t.Fatal("retained raw result changed")
			}
		} else if reply.Code != 400 {
			t.Fatal("another task borrowed reconcile allowance", capability, reply.Code)
		}
		b.Close()
	}
	b, _ := New(token, nil)
	defer b.Close()
	if _, e := b.PinReconcile(context.Background()); e == nil {
		t.Fatal("unpaired unconfigured execution admitted")
	}
	r, _ := http.NewRequest("POST", "http://epf.invalid/reconcile-read", strings.NewReader(reconcileIdentity))
	if _, e := b.RoundTrip(r); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("unconfigured execution admitted", e)
	}
	// Mixed multibyte UTF-8, quotes and backslashes remain exact byte content.
	mixed, _ := json.Marshal(strings.Repeat("Ж🌍\"\\", 90000))
	if len(mixed) > 1064960 {
		t.Fatal("mixed fixture too large")
	}
	p := &pending{task: Task{ID: "utf8", Capability: "pro.reconcile-read", CollectBefore: time.Now().Add(time.Minute)}, session: "original", identity: "native", handed: true, result: make(chan result, 1)}
	b.pending[p.task.ID] = p
	payload, _ := json.Marshal(map[string]any{"identity": "native", "status": 200, "body_text": string(mixed)})
	if reply := request(b, "POST", "/result/utf8", "wrong-owner", string(payload)); reply.Code != 409 {
		t.Fatal("foreign completion accepted", reply.Code)
	}
	forged, _ := json.Marshal(map[string]any{"identity": "native", "status": 200, "body_text": string(mixed), "capability": "pro.reconcile-read"})
	if reply := request(b, "POST", "/result/utf8", "original", string(forged)); reply.Code != 400 {
		t.Fatal("posted capability accepted", reply.Code)
	}
	if reply := request(b, "POST", "/result/utf8", "original", string(payload)); reply.Code != 204 {
		t.Fatal("mixed UTF8 envelope refused", reply.Code)
	}
	_, body, ready, e := b.Observe(&UnknownOutcome{bridge: b, request: p})
	if e != nil || !ready || string(body) != string(mixed) {
		t.Fatal("UTF8/quote/slash bytes changed")
	}
}
