package epf

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const packageCore = "real-core"

func TestQueryPackageExactBytesAndPaidBoundary(t *testing.T) {
	b, _ := New(token, nil)
	body := `{"query":"SELECT \"<>&\u2028\"","run_id":"123","parameters":[],"timeout":1}`
	req := func(raw string) *http.Request {
		r, _ := http.NewRequest("POST", "http://epf.invalid/query-package", strings.NewReader(raw))
		return r
	}
	if _, err := b.RoundTrip(req(body)); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatal("Community enabled paid route", err)
	}
	allowed := true
	cutoff := time.Now().Add(3 * time.Second)
	if err := b.EnableQueryPackage(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	}); err != nil {
		t.Fatal(err)
	}
	session := pair(t, b)
	if _, err := b.RoundTrip(req(body)); !errors.Is(err, ErrHybridUnavailable) {
		t.Fatal("no-core package admitted", err)
	}
	request(b, "DELETE", "/session", session, "")
	paired := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","query_package":"query_package/1"}`)
	var pairReply struct {
		Session string `json:"session"`
	}
	json.Unmarshal(paired.Body.Bytes(), &pairReply)
	session = pairReply.Session
	for _, raw := range []string{`{"query":"SELECT 1","run_id":"x","extra":1}`, `{"query":"SELECT 1","run_id":null}`, `{"query":"SELECT 1","query":"SELECT 2","run_id":"x"}`, `{"query":"SELECT 1","run_id":"x","parameters":[{"name":"P","value":{"type":"string","value":null}}]}`} {
		if _, err := b.RoundTrip(req(raw)); err == nil {
			t.Fatal("invalid boundary admitted")
		}
	}
	done := make(chan error, 1)
	native := `{"unicode":"<>&\u2028","n":9007199254740993.01}`
	go func() {
		out, err := b.RoundTrip(req(body))
		if out != nil {
			raw, _ := io.ReadAll(out.Body)
			out.Body.Close()
			if string(raw) != native {
				err = errors.New("native result bytes changed")
			}
		}
		done <- err
	}()
	select {
	case p := <-b.queue:
		if p.task.BodyText != body || len(p.task.Body) != 0 || !p.task.ExecuteBefore.Equal(cutoff) || p.task.Core != packageCore || p.task.Session != session {
			t.Fatal("native binding/bytes lost")
		}
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", session, `{"identity":"native-owner"}`)
	var task Task
	if err := json.Unmarshal(poll.Body.Bytes(), &task); err != nil || task.BodyText != body {
		t.Fatal("task envelope changed native bytes", err)
	}
	allowed = false
	envelope, _ := json.Marshal(map[string]any{"identity": "native-owner", "status": 200, "body_text": native})
	if code := request(b, "POST", "/result/"+task.ID, session, string(envelope)).Code; code != 204 {
		t.Fatal("late collection refused", code)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := b.RoundTrip(req(body)); err == nil {
		t.Fatal("invalid grant admitted")
	}
	// FREE remains usable when only the paid callback is closed.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p, freeDone := enqueue(t, b, ctx)
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("FREE blocked")
	}
	if request(b, "POST", "/result/"+p.task.ID, session, `{"identity":"native-owner","status":200,"body":{}}`).Code != 204 {
		t.Fatal("FREE completion blocked")
	}
	if err := <-freeDone; err != nil {
		t.Fatal(err)
	}
}

func TestQueryPackageRechecksGrantAtHandout(t *testing.T) {
	b, _ := New(token, nil)
	allowed := true
	b.EnableQueryPackage(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return time.Now().Add(time.Second), nil
	})
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","query_package":"query_package/1"}`)
	var pairReply struct {
		Session string `json:"session"`
	}
	json.Unmarshal(w.Body.Bytes(), &pairReply)
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequest("POST", "http://epf.invalid/query-package", strings.NewReader(`{"query":"SELECT 1","run_id":"x"}`))
		out, err := b.RoundTrip(r)
		if out != nil {
			out.Body.Close()
		}
		done <- err
	}()
	select {
	case p := <-b.queue:
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	allowed = false
	if request(b, "POST", "/poll", pairReply.Session, `{"identity":"native-owner"}`).Code != 204 {
		t.Fatal("expired grant handed")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestQueryPackagePairContract(t *testing.T) {
	for _, marker := range []string{`null`, `"query_package/2"`, `"query_package/1","query_package":"query_package/1"`} {
		b, _ := New(token, nil)
		if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","query_package":`+marker+`}`).Code != 400 {
			t.Fatal("invalid package contract paired")
		}
	}
	b, _ := New(token, nil)
	b.EnableQueryPackage(func(context.Context) (time.Time, error) { return time.Now().Add(time.Second), nil })
	if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"document-only-core"}`).Code != 200 {
		t.Fatal("legacy core cannot pair for FREE/document reads")
	}
	r, _ := http.NewRequest("POST", "http://epf.invalid/query-package", strings.NewReader(`{"query":"SELECT 1","run_id":"x"}`))
	if _, err := b.RoundTrip(r); !errors.Is(err, ErrHybridUnavailable) {
		t.Fatal("legacy core received package", err)
	}
	if len(b.queue) != 0 {
		t.Fatal("legacy core package was queued")
	}
}
