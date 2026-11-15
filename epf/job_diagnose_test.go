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

func TestJobDiagnoseOptInExactBytesAndGrant(t *testing.T) {
	b, _ := New(token, nil)
	body := `{"contract_version":"JOB-DIAG-1","request_id":"11111111-1111-4111-8111-111111111111","job_kind":"background","job_id":"22222222-2222-4222-8222-222222222222","start_date":"2026-11-09T00:00:00","end_date":"2026-11-10T00:00:00"}`
	req := func(raw string) *http.Request {
		r, _ := http.NewRequest("POST", "http://epf.invalid/job-diagnose", strings.NewReader(raw))
		return r
	}
	if _, e := b.RoundTrip(req(body)); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("Community enabled paid job diagnosis", e)
	}
	if b.EnableJobDiagnose() == nil {
		t.Fatal("job diagnosis enabled without verified-cutoff callback")
	}
	allowed := true
	cutoff := time.Now().Add(3 * time.Second)
	b.EnableQueryPackage(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	})
	if e := b.EnableJobDiagnose(); e != nil {
		t.Fatal(e)
	}
	session := pair(t, b)
	if _, e := b.RoundTrip(req(body)); !errors.Is(e, ErrHybridUnavailable) {
		t.Fatal("no core/marker admitted", e)
	}
	request(b, "DELETE", "/session", session, "")
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","query_package":"query_package/1","job_diagnose":"JOB-DIAG-1"}`)
	var paired struct {
		Session string `json:"session"`
	}
	json.Unmarshal(w.Body.Bytes(), &paired)
	session = paired.Session
	for _, bad := range []string{strings.Replace(body, `"job_kind":"background"`, `"job_kind":null`, 1), strings.Replace(body, `"job_kind":"background"`, `"job_kind":"background","code":"write"`, 1), strings.Replace(body, `"job_kind":"background"`, `"job_kind":"background","job_kind":"background"`, 1)} {
		if _, e := b.RoundTrip(req(bad)); e == nil {
			t.Fatal("invalid request admitted")
		}
	}
	done := make(chan error, 1)
	rawNative := `{"read":null,"exact":"<>&\u2028"}`
	go func() {
		out, e := b.RoundTrip(req(body))
		if out != nil {
			raw, _ := io.ReadAll(out.Body)
			out.Body.Close()
			if string(raw) != rawNative {
				e = errors.New("native bytes changed")
			}
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if p.task.BodyText != body || p.task.Body != nil || !p.task.ExecuteBefore.Equal(cutoff) {
			t.Fatal("lost exact bytes/cutoff")
		}
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", session, `{"identity":"native-owner"}`)
	var task Task
	json.Unmarshal(poll.Body.Bytes(), &task)
	if task.Capability != "advanced.job-diagnose" {
		t.Fatal("wrong fixed capability")
	}
	allowed = false
	payload, _ := json.Marshal(map[string]any{"identity": "native-owner", "status": 200, "body_text": rawNative})
	if request(b, "POST", "/result/"+task.ID, session, string(payload)).Code != 204 {
		t.Fatal("late result refused")
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if _, e := b.RoundTrip(req(body)); e == nil {
		t.Fatal("invalid grant admitted")
	}
}
func TestJobDiagnoseMarkerIsStrictAndOptional(t *testing.T) {
	for _, marker := range []string{`null`, `"JOB-DIAG-2"`, `"JOB-DIAG-1","job_diagnose":"JOB-DIAG-1"`} {
		b, _ := New(token, nil)
		if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","job_diagnose":`+marker+`}`).Code != 400 {
			t.Fatal("invalid job diagnosis marker paired")
		}
	}
}

func TestJobDiagnoseRechecksGrantAtHandout(t *testing.T) {
	b, _ := New(token, nil)
	allowed := true
	b.EnableQueryPackage(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return time.Now().Add(time.Second), nil
	})
	b.EnableJobDiagnose()
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","job_diagnose":"JOB-DIAG-1"}`)
	var pairReply struct {
		Session string `json:"session"`
	}
	json.Unmarshal(w.Body.Bytes(), &pairReply)
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequest("POST", "http://epf.invalid/job-diagnose", strings.NewReader(`{"contract_version":"JOB-DIAG-1","request_id":"x","job_kind":"background","job_id":"x","start_date":"2026-11-09T00:00:00","end_date":"2026-11-10T00:00:00"}`))
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
