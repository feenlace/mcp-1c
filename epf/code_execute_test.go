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

const codeExecuteRequest = `{"code":"Сообщить(1 + 2);","mode":"read_only","timeout":30}`

func codeExecuteOptIn(t *testing.T, b *Bridge) {
	t.Helper()
	route, ok := any(b).(interface{ EnableCodeExecute() error })
	if !ok {
		t.Fatal("finite EPF code_execute opt-in absent")
	}
	if e := route.EnableCodeExecute(); e != nil {
		t.Fatal(e)
	}
}
func TestCodeExecuteExactBytesGrantAndNoReplay(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	req := func(ctx context.Context, raw string) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/execute", strings.NewReader(raw))
		return r
	}
	if _, e := b.RoundTrip(req(context.Background(), codeExecuteRequest)); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("Community code_execute admission", e)
	}
	allowed := true
	cutoff := time.Now().Add(10 * time.Second)
	b.EnableQueryPackage(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	})
	codeExecuteOptIn(t, b)
	session := pair(t, b)
	if _, e := b.RoundTrip(req(context.Background(), codeExecuteRequest)); !errors.Is(e, ErrHybridUnavailable) {
		t.Fatal("missing core admitted", e)
	}
	request(b, "DELETE", "/session", session, "")
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","code_execute":"code_execute/1"}`)
	var paired struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &paired)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	pin, e := b.PinSession(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	originalCutoff := cutoff
	ctx, cancel := context.WithCancel(pin)
	done := make(chan error, 1)
	go func() {
		r, e := b.RoundTrip(req(ctx, codeExecuteRequest))
		if r != nil {
			r.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if p.task.BodyText != codeExecuteRequest || p.task.Body != nil || p.task.Core != "real-core" || p.task.Session != paired.Session || !p.task.ExecuteBefore.Equal(originalCutoff) {
			t.Fatal("body/context/cutoff changed")
		}
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`)
	var task Task
	json.Unmarshal(poll.Body.Bytes(), &task)
	if task.Capability != "advanced.code-execute" {
		t.Fatal("wrong native capability")
	}
	cancel()
	e = <-done
	var unknown *UnknownOutcome
	if !errors.As(e, &unknown) {
		t.Fatal("lost UnknownOutcome", e)
	}
	if _, err := b.RoundTrip(req(context.Background(), codeExecuteRequest)); !errors.Is(err, ErrUnavailable) {
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
	if _, e := b.RoundTrip(req(pin, codeExecuteRequest)); e == nil {
		t.Fatal("expired grant admitted")
	}
	allowed = true
	request(b, "DELETE", "/session", paired.Session, "")
	pair(t, b)
	if _, e := b.RoundTrip(req(pin, codeExecuteRequest)); !errors.Is(e, ErrSessionChanged) {
		t.Fatal("cross-pair read admitted", e)
	}
}

func TestCodeExecuteClosedWireAndHandoutCutoff(t *testing.T) {
	if !codeExecuteBody([]byte(codeExecuteRequest)) || !codeExecuteBody([]byte(strings.Replace(codeExecuteRequest, "read_only", "full", 1))) {
		t.Fatal("valid body refused")
	}
	for _, raw := range []string{
		strings.TrimSuffix(codeExecuteRequest, "}") + `,"mode":"full"}`,
		strings.Replace(codeExecuteRequest, `"mode":"read_only"`, `"Mode":"read_only"`, 1),
		strings.Replace(codeExecuteRequest, `"mode":"read_only"`, `"mode":null`, 1),
		strings.Replace(codeExecuteRequest, `"read_only"`, `"eval"`, 1),
		strings.Replace(codeExecuteRequest, `"timeout":30`, `"timeout":0`, 1),
		strings.Replace(codeExecuteRequest, `"timeout":30`, `"timeout":121`, 1),
		strings.Replace(codeExecuteRequest, `"timeout":30`, `"timeout":1.1`, 1),
		strings.Replace(codeExecuteRequest, `"timeout":30`, `"timeout":"30"`, 1),
		strings.Replace(codeExecuteRequest, `"Сообщить(1 + 2);"`, `"`+strings.Repeat("x", 10241)+`"`, 1),
		strings.Replace(codeExecuteRequest, `"Сообщить(1 + 2);"`, `""`, 1),
		string(append([]byte(codeExecuteRequest), 0xff)),
	} {
		if codeExecuteBody([]byte(raw)) {
			t.Fatal("invalid code wire admitted")
		}
	}
	for _, marker := range []string{`null`, `"code_execute/2"`, `"code_execute/1","code_execute":"code_execute/1"`} {
		b, _ := New(token, nil)
		if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"core","code_execute":`+marker+`}`).Code != 400 {
			t.Fatal("invalid marker admitted")
		}
		b.Close()
	}
	b, _ := New(token, nil)
	defer b.Close()
	allowed := true
	b.EnableQueryPackage(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return time.Now().Add(time.Second), nil
	})
	b.EnableCodeExecute()
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","code_execute":"code_execute/1"}`)
	var pairReply struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &pairReply)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/execute", strings.NewReader(codeExecuteRequest))
		out, e := b.RoundTrip(r)
		if out != nil {
			if out.StatusCode < 400 {
				e = errors.New("revoked handout succeeded")
			}
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
	if request(b, "POST", "/poll", pairReply.Session, `{"identity":"native-owner"}`).Code != 204 {
		t.Fatal("grant expired before handout still dispatched")
	}
	cancel()
	if e := <-done; e != nil {
		t.Fatal("handout refusal failed", e)
	}
}

func TestCodeExecuteLongWaitRetainsShortNativeAdmission(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	cutoff := time.Now().Add(5 * time.Minute)
	b.EnableQueryPackage(func(context.Context) (time.Time, error) { return cutoff, nil })
	b.EnableCodeExecute()
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","code_execute":"code_execute/1"}`)
	var paired struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &paired)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/execute", strings.NewReader(strings.Replace(codeExecuteRequest, `"timeout":30`, `"timeout":120`, 1)))
		out, e := b.RoundTrip(r)
		if out != nil {
			out.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if p.task.ExecuteBefore.After(start.Add(31*time.Second)) || p.task.CollectBefore.Before(start.Add(179*time.Second)) {
			t.Fatal("runtime wait truncated or native admission extended")
		}
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`)
	var task Task
	json.Unmarshal(poll.Body.Bytes(), &task)
	payload, _ := json.Marshal(map[string]any{"identity": "native-owner", "status": 200, "body_text": `{"result":"retained","error":"","duration_ms":95000}`})
	if request(b, "POST", "/result/"+task.ID, paired.Session, string(payload)).Code != 204 {
		t.Fatal("completion refused")
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestCodeExecutePinnedCutoffCannotRenewOrCrossPair(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	cutoff := time.Now().Add(10 * time.Second)
	if b.EnableQueryPackage(func(context.Context) (time.Time, error) { return cutoff, nil }) != nil || b.EnableCodeExecute() != nil {
		t.Fatal("enable")
	}
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","code_execute":"code_execute/1"}`)
	var paired struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &paired)
	if w.Code != 200 {
		t.Fatal("pair")
	}
	ctx, err := b.PinCodeExecute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := cutoff
	cutoff = cutoff.Add(time.Hour)
	ctx, err = b.PinCodeExecute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pinned, _ := ctx.Value(codeExecuteCutoffKey{}).(time.Time); !pinned.Equal(original) {
		t.Fatal("renewal extended pinned cutoff")
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequestWithContext(callCtx, "POST", "http://epf.invalid/execute", strings.NewReader(codeExecuteRequest))
		out, e := b.RoundTrip(r)
		if out != nil {
			out.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if !p.task.ExecuteBefore.Equal(original) || !p.task.CollectBefore.After(original) {
			t.Fatal("lost admission ceiling or late collection")
		}
		cancel()
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	<-done
	request(b, "DELETE", "/session", paired.Session, "")
	pair(t, b)
	if _, err = b.PinCodeExecute(ctx); !errors.Is(err, ErrSessionChanged) {
		t.Fatal("old operation crossed re-pair")
	}
}
