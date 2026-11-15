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

func TestDocumentReadOptInAndCutoff(t *testing.T) {
	b, _ := New(token, nil)
	r := func(body string) *http.Request {
		r, _ := http.NewRequest("POST", "http://epf.invalid/document-read", strings.NewReader(body))
		return r
	}
	body := `{"contract_version":1,"operation":"describe","business":"{\"action\":\"describe\"}"}`
	if _, e := b.RoundTrip(r(body)); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("default admits Pro")
	}
	cutoff := time.Now().Add(5 * time.Second)
	if e := b.EnableDocumentRead(func(context.Context) (time.Time, error) { return cutoff, nil }); e != nil {
		t.Fatal(e)
	}
	session := pair(t, b)
	if _, e := b.RoundTrip(r(body)); !errors.Is(e, ErrHybridUnavailable) {
		t.Fatal("missing core admitted", e)
	}
	request(b, "DELETE", "/session", session, "")
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"actual-core-proof"}`)
	var paired struct {
		Session string `json:"session"`
	}
	json.Unmarshal(w.Body.Bytes(), &paired)
	session = paired.Session
	for _, bad := range []string{
		`{"contract_version":1,"operation":"prepare","business":"{\"action\":\"create\"}"}`,
		`{"contract_version":1,"operation":"describe","operation":"get","business":"{}"}`,
		`{"contract_version":1,"operation":"identity","business":"{\"action\":\"create\"}"}`,
		`{"contract_version":1,"operation":"get","business":"{\"action\":\"get\",\"uuid\":null}"}`,
	} {
		if _, e := b.RoundTrip(r(bad)); e == nil {
			t.Fatal("invalid paid request admitted")
		}
	}
	done := make(chan error, 1)
	go func() {
		out, e := b.RoundTrip(r(body))
		if out != nil {
			out.Body.Close()
		}
		done <- e
	}()
	var p *pending
	select {
	case p = <-b.queue:
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	if !p.task.ExecuteBefore.Equal(cutoff) || p.task.Core != "actual-core-proof" || p.task.Session != session {
		t.Fatal("lost cutoff/core/session")
	}
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("not handed")
	}
	cutoff = time.Now().Add(-time.Second)
	if request(b, "POST", "/result/"+p.task.ID, session, `{"identity":"native-owner","status":200,"body":{"contract_version":1}}`).Code != 204 {
		t.Fatal("late result rejected")
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if _, e := b.RoundTrip(r(body)); e == nil {
		t.Fatal("expired new launch")
	}
}

func TestDocumentReadRechecksGrant(t *testing.T) {
	b, _ := New(token, nil)
	cutoff := time.Now().Add(2 * time.Second)
	allowed := true
	b.EnableDocumentRead(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	})
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"proof"}`)
	var paired struct {
		Session string `json:"session"`
	}
	json.Unmarshal(w.Body.Bytes(), &paired)
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequest("POST", "http://epf.invalid/document-read", strings.NewReader(`{"contract_version":1,"operation":"describe","business":"{\"action\":\"describe\"}"}`))
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
	if request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`).Code != 204 {
		t.Fatal("expired grant handed")
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestPinnedDocumentWorkflowCannotSwitchPair(t *testing.T) {
	b, _ := New(token, nil)
	b.EnableDocumentRead(func(context.Context) (time.Time, error) { return time.Now().Add(time.Minute), nil })
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"same-config-user","core":"actual-core-proof"}`)
	var first struct {
		Session string `json:"session"`
	}
	json.Unmarshal(w.Body.Bytes(), &first)
	pinned, err := b.PinSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request(b, "DELETE", "/session", first.Session, "")
	if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"same-config-user","core":"actual-core-proof"}`).Code != 200 {
		t.Fatal("re-pair failed")
	}
	r, _ := http.NewRequestWithContext(pinned, "POST", "http://epf.invalid/document-read", strings.NewReader(`{"contract_version":1,"operation":"describe","business":"{\"action\":\"describe\"}"}`))
	if _, err := b.RoundTrip(r); !errors.Is(err, ErrSessionChanged) {
		t.Fatal("old workflow followed a new pair", err)
	}
	if len(b.queue) != 0 {
		t.Fatal("changed pair queued native work")
	}
	other, _ := New(token, nil)
	pair(t, other)
	r, _ = http.NewRequestWithContext(pinned, "GET", "http://epf.invalid/configuration", nil)
	if _, err := other.RoundTrip(r); !errors.Is(err, ErrSessionChanged) {
		t.Fatal("pin moved to a different bridge", err)
	}
}
