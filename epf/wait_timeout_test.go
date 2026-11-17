package epf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/onec"
)

func pairWait(t *testing.T, b *Bridge, seconds string) string {
	t.Helper()
	raw := `{"protocol":1,"identity":"native-owner"`
	if seconds != "" {
		raw += `,"wait_timeout_seconds":` + seconds
	}
	w := request(b, "POST", "/pair", pairCode(b), raw+`}`)
	if w.Code != 200 {
		t.Fatalf("pair: %d %s", w.Code, w.Body.String())
	}
	var out struct{ Session string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Session
}

func TestPairWaitValidationAndAcknowledgement(t *testing.T) {
	const maxSeconds = int64((time.Duration(1<<63-1) - time.Minute) / time.Second)
	for _, value := range []string{"0", "-1", "1.5", "1e3", "null", `"600"`, "9223372036854775808", fmt.Sprint(maxSeconds + 1), `30,"wait_timeout_seconds":60`} {
		t.Run(value, func(t *testing.T) {
			b, _ := New(token, nil)
			defer b.Close()
			w := request(b, "POST", "/pair", token, `{"protocol":1,"identity":"native-owner","wait_timeout_seconds":`+value+`}`)
			if w.Code != 400 || pairCode(b) != token {
				t.Fatalf("invalid value consumed pairing: %d", w.Code)
			}
			pair(t, b)
		})
	}
	for _, tc := range []struct {
		seconds string
		limit   time.Duration
		want    float64
	}{
		{"", 0, 30}, {"600", 0, 600}, {fmt.Sprint(maxSeconds), 0, float64(maxSeconds)}, {"600", 45 * time.Second, 45}, {"600", 1500 * time.Millisecond, 1.5},
	} {
		b, _ := New(token, nil)
		b.SetDefaultWaitLimit(tc.limit)
		raw := `{"protocol":1,"identity":"native-owner"`
		if tc.seconds != "" {
			raw += `,"wait_timeout_seconds":` + tc.seconds
		}
		w := request(b, "POST", "/pair", token, raw+`}`)
		var out struct {
			Requested int64   `json:"wait_timeout_seconds"`
			Effective float64 `json:"effective_default_wait_timeout_seconds"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Effective != tc.want || out.Requested <= 0 {
			t.Fatalf("ack: %d %s", w.Code, w.Body.String())
		}
		b.Close()
	}
}

func TestSessionWaitStartFenceAndReconnectSnapshot(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	session := pairWait(t, b, "600")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := time.Now()
	p, done := enqueue(t, b, ctx)
	if d := p.task.CollectBefore.Sub(before); d < 660*time.Second || d > 661*time.Second {
		t.Fatalf("response budget = %s", d)
	}
	if d := p.task.ExecuteBefore.Sub(before); d < 30*time.Second || d > 31*time.Second {
		t.Fatalf("start budget = %s", d)
	}
	oldCollection := p.task.CollectBefore
	request(b, "DELETE", "/session", session, "")
	newSession := pairWait(t, b, "60")
	if !p.task.CollectBefore.Equal(oldCollection) {
		t.Fatal("reconnect mutated pending budget")
	}
	cancel()
	<-done
	request(b, "POST", "/poll", newSession, `{"identity":"native-owner"}`)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	before = time.Now()
	p2, done2 := enqueue(t, b, ctx2)
	if d := p2.task.CollectBefore.Sub(before); d < 120*time.Second || d > 121*time.Second {
		t.Fatalf("new session budget = %s", d)
	}
	cancel2()
	<-done2
	other, _ := New(token, nil)
	defer other.Close()
	pair(t, other)
	ctx3, cancel3 := context.WithCancel(context.Background())
	defer cancel3()
	p3, done3 := enqueue(t, other, ctx3)
	if d := p3.task.CollectBefore.Sub(time.Now()); d < 89*time.Second || d > 91*time.Second {
		t.Fatalf("bridge settings leaked: %s", d)
	}
	cancel3()
	<-done3
}

func TestManagedClientWaitKeepsPositiveBaseAndCallerDeadline(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	session := pairWait(t, b, "600")
	c := onec.NewClient("http://epf.invalid", "", "", onec.WithTransportManagedTimeout())
	c.HTTPClient.Transport = b
	base := *c.HTTPClient
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, done := clientRequest(t, b, c, ctx)
	if _, ok := p.origin.Deadline(); ok {
		t.Fatal("implicit five-minute deadline survived")
	}
	if d := p.task.CollectBefore.Sub(time.Now()); d < 659*time.Second || d > 661*time.Second {
		t.Fatal("bridge wait truncated", d)
	}
	cancel()
	<-done
	if (c.HTTPClient.Timeout != base.Timeout || c.HTTPClient.Transport != base.Transport || c.HTTPClient.Jar != base.Jar || reflect.ValueOf(c.HTTPClient.CheckRedirect).Pointer() != reflect.ValueOf(base.CheckRedirect).Pointer()) || base.Timeout <= 0 {
		t.Fatal("shared positive HTTP client mutated")
	}
	request(b, "POST", "/poll", session, `{"identity":"native-owner"}`)
	onec.WithRequestTimeout(base.Timeout)(c)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	p2, done2 := clientRequest(t, b, c, ctx2)
	if d, ok := p2.origin.Deadline(); !ok || time.Until(d) > base.Timeout {
		t.Fatal("equal-value explicit timeout lost")
	}
	cancel2()
	<-done2
}

func TestWaitErrorPreservesActualBudgetAndUnknownOutcome(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	b.SetDefaultWaitLimit(25 * time.Millisecond)
	session := pairWait(t, b, "600")
	c := onec.NewClient("http://epf.invalid", "", "", onec.WithTransportManagedTimeout())
	c.HTTPClient.Transport = b
	p, done := clientRequest(t, b, c, context.Background())
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("not handed")
	}
	err := <-done
	var we *WaitTimeoutError
	var unknown *UnknownOutcome
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &we) || !errors.As(err, &unknown) || we.Budget != 25*time.Millisecond || we.EarlierDeadline {
		t.Fatalf("wrong wait chain: %v", err)
	}
	if request(b, "POST", "/result/"+p.task.ID, session, `{"identity":"native-owner","status":200,"body":{}}`).Code != 204 {
		t.Fatal("late result rejected")
	}
	if _, _, ready, e := b.Observe(unknown); !ready || e != nil {
		t.Fatal("unknown observation lost")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, done = clientRequest(t, b, c, ctx)
	err = <-done
	if !errors.As(err, &we) || !we.EarlierDeadline || we.Budget > 10*time.Millisecond {
		t.Fatalf("earlier caller deadline misattributed: %v", err)
	}
}

func TestWaitSnapshotAfterBodyAndExpiredAdmission(t *testing.T) {
	// A body reader is outside the critical section, so pairing may change before insertion.
	b, _ := New(token, nil)
	defer b.Close()
	old := pairWait(t, b, "600")
	pin, _ := b.PinSession(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	body := &waitBody{entered: entered, release: release}
	r, _ := http.NewRequestWithContext(pin, "POST", "http://epf.invalid/query", body)
	done := make(chan error, 1)
	go func() { _, err := b.RoundTrip(r); done <- err }()
	<-entered
	request(b, "DELETE", "/session", old, "")
	pairWait(t, b, "60")
	close(release)
	if err := <-done; !errors.Is(err, ErrSessionChanged) {
		t.Fatal("stale session admitted", err)
	}
	if len(b.pending) != 0 {
		t.Fatal("stale work inserted")
	}
}

type waitBody struct {
	entered, release chan struct{}
	read             bool
}

func (b *waitBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	close(b.entered)
	<-b.release
	return strings.NewReader(`{}`).Read(p)
}
func (*waitBody) Close() error { return nil }

func TestFormWaitCompatibilityAndVisibleAcknowledgement(t *testing.T) {
	raw, err := os.ReadFile("source/MCPConnector/Forms/Main/Ext/Form/Module.bsl")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	nondefault := strings.Index(s, `Если Ответ.КодСостояния = 400 И ЗапрошеноОжидание <> 30`)
	fallback := strings.Index(s, `Если Ответ.КодСостояния = 400 И КонвертПодключения.Свойство("job_retry")`)
	if nondefault < 0 || fallback < nondefault {
		t.Fatal("nondefault rejection strips capabilities")
	}
	for _, want := range []string{`WaitTimeoutSeconds = 30`, `ЗапрошеноОжидание <> 30 Тогда КонвертПодключения.Вставить("wait_timeout_seconds"`, `Данные.Свойство("effective_default_wait_timeout_seconds"`, `ConnectionState = AcceptedConnectionState`, `Элементы.WaitTimeoutSeconds.Доступность = SessionToken = ""`, `WaitTimeoutSeconds <> Цел(WaitTimeoutSeconds)`} {
		if !strings.Contains(s, want) {
			t.Fatal("form contract missing", want)
		}
	}
}

func TestAdmissionCutoffExpiresWhileWaitingForBridgeLock(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	cutoff := time.Now().Add(60 * time.Millisecond)
	b.EnableQueryPackage(func(context.Context) (time.Time, error) {
		// This callback must run without b.mu: a callback may inspect bridge state.
		b.mu.Lock()
		b.mu.Unlock()
		close(entered)
		<-release
		return cutoff, nil
	})
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"core","query_package":"query_package/1","wait_timeout_seconds":600}`)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	r, _ := http.NewRequest("POST", "http://epf.invalid/query-package", strings.NewReader(`{"query":"SELECT 1","run_id":"123","parameters":[],"timeout":1}`))
	done := make(chan error, 1)
	go func() { _, err := b.RoundTrip(r); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback did not run")
	}
	b.mu.Lock()
	close(release)
	<-time.After(time.Until(cutoff) + time.Millisecond)
	b.mu.Unlock()
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired admission inserted", err)
	}
	if len(b.pending) != 0 || len(b.queue) != 0 {
		t.Fatal("expired work queued")
	}
}

func TestCodeExecuteExplicitBudgetOverridesConnectionDefault(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	b.SetDefaultWaitLimit(45 * time.Second)
	b.EnableQueryPackage(func(context.Context) (time.Time, error) { return time.Now().Add(120 * time.Second), nil })
	b.EnableCodeExecute()
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"core","code_execute":"code_execute/1","wait_timeout_seconds":600}`)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	c := onec.NewClient("http://epf.invalid", "", "", onec.WithRequestTimeout(45*time.Second))
	c.HTTPClient.Transport = b
	// The existing tool contract makes an explicit operation-budget client clone.
	clone := *c
	hc := *c.HTTPClient
	clone.HTTPClient = &hc
	onec.WithRequestTimeout(90 * time.Second)(&clone)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	before := time.Now()
	go func() {
		var out any
		done <- clone.Post(ctx, "/execute", json.RawMessage(strings.Replace(codeExecuteRequest, `"timeout":30`, `"timeout":90`, 1)), &out)
	}()
	select {
	case p := <-b.queue:
		if d := p.task.CollectBefore.Sub(before); d < 149*time.Second || d > 151*time.Second {
			t.Fatal("code budget capped by session/base", d)
		}
		if d := p.task.ExecuteBefore.Sub(before); d < 29*time.Second || d > 31*time.Second {
			t.Fatal("code start fence extended", d)
		}
	case <-time.After(time.Second):
		t.Fatal("code not queued")
	}
	cancel()
	<-done
	if c.HTTPClient.Timeout != 45*time.Second {
		t.Fatal("base timeout changed")
	}
}
