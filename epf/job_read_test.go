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

func jobReadFixture(retry, identity bool) string {
	in := map[string]any{"contract_version": 1, "profile": "JOB-CANCEL-FTS-1", "request_id": "11111111-1111-4111-8111-111111111111", "job_id": "22222222-2222-4222-8222-222222222222", "schedule_id": "33333333-3333-4333-8333-333333333333", "deadline": time.Now().Add(20 * time.Second).UTC().Format("2006-01-02T15:04:05"), "expected_identity": "", "expected_start": ""}
	if retry {
		in["contract_version"] = 2
		in["profile"] = "JOB-RETRY-FTS-1"
		for _, k := range []string{"expected_composition", "expected_version_id", "expected_update_id", "key", "new_run_id", "new_run_start", "expected_base_token"} {
			in[k] = ""
		}
	}
	if identity {
		in["job_id"] = ""
		in["schedule_id"] = ""
	}
	raw, _ := json.Marshal(in)
	return string(raw)
}

func TestJobReadClosedBodies(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, identity := range []bool{false, true} {
			if identity && !retry {
				continue
			}
			raw := jobReadFixture(retry, identity)
			if !jobReadBody([]byte(raw), retry, identity) {
				t.Fatal("valid native body refused")
			}
			for _, bad := range []string{strings.TrimSuffix(raw, "}") + `,"mode":"start"}`, strings.Replace(raw, `"request_id":`, `"request_id":null,"request_id":`, 1), strings.Replace(raw, `"deadline":`, `"deadline":null,"unused":`, 1), strings.Replace(raw, `"expected_identity":""`, `"expected_identity":null`, 1), strings.Replace(raw, `"contract_version":`, `"contract_version":99,"ignored":`, 1)} {
				if jobReadBody([]byte(bad), retry, identity) {
					t.Fatal("malformed/unknown body admitted")
				}
			}
		}
	}
}

func TestJobReadFiniteDispatchPinsAndCollection(t *testing.T) {
	for _, path := range []string{"/job-control-read", "/job-retry-identity", "/job-retry-read"} {
		t.Run(path, func(t *testing.T) {
			b, _ := New(token, nil)
			defer b.Close()
			raw := jobReadFixture(path != "/job-control-read", path == "/job-retry-identity")
			req := func(ctx context.Context, path string) *http.Request {
				r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid"+path, strings.NewReader(raw))
				return r
			}
			if _, e := b.RoundTrip(req(context.Background(), path)); !errors.Is(e, ErrCapabilityUnavailable) {
				t.Fatal("unconfigured route admitted", e)
			}
			cutoff := time.Now().Add(10 * time.Second)
			allowed := true
			b.EnableDocumentRead(func(context.Context) (time.Time, error) {
				if !allowed {
					return time.Time{}, ErrUnavailable
				}
				return cutoff, nil
			})
			if e := b.EnableJobRead(); e != nil {
				t.Fatal(e)
			}
			session := pair(t, b)
			if _, e := b.RoundTrip(req(context.Background(), path)); !errors.Is(e, ErrHybridUnavailable) {
				t.Fatal("no-core job admitted", e)
			}
			request(b, "DELETE", "/session", session, "")
			w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","job_control_read":"job_control_read/1","job_retry_read":"job_retry_read/2"}`)
			var paired struct{ Session string }
			json.Unmarshal(w.Body.Bytes(), &paired)
			if w.Code != 200 {
				t.Fatal("pair", w.Code)
			}
			pin, e := b.PinJobRead(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			original := cutoff
			cutoff = cutoff.Add(time.Hour)
			for _, write := range []string{"/job-control-cancel", "/job-retry-start", "/job-retry-cancel", "/job-retry-read/extra"} {
				if _, e := b.RoundTrip(req(pin, write)); !errors.Is(e, ErrCapabilityUnavailable) {
					t.Fatal("mutation or unknown path admitted", write, e)
				}
			}
			ctx, cancel := context.WithCancel(pin)
			done := make(chan error, 1)
			go func() {
				r, e := b.RoundTrip(req(ctx, path))
				if r != nil {
					r.Body.Close()
				}
				done <- e
			}()
			select {
			case p := <-b.queue:
				if p.task.BodyText != raw || p.task.Body != nil || p.task.Core != "real-core" || p.task.Session != paired.Session || !p.task.ExecuteBefore.Equal(original) {
					t.Fatal("body/context/cutoff changed")
				}
				b.queue <- p
			case <-time.After(time.Second):
				t.Fatal("not queued")
			}
			poll := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`)
			var task Task
			json.Unmarshal(poll.Body.Bytes(), &task)
			if poll.Code != 200 || !jobReadCapability(task.Capability) || task.Path != path {
				t.Fatal("wrong handout")
			}
			cancel()
			e = <-done
			var unknown *UnknownOutcome
			if !errors.As(e, &unknown) {
				t.Fatal("missing observation ticket", e)
			}
			allowed = false
			payload, _ := json.Marshal(map[string]any{"identity": "native-owner", "status": 200, "body_text": `{"observed":true}`})
			if request(b, "POST", "/result/"+task.ID, paired.Session, string(payload)).Code != 204 {
				t.Fatal("collection after revocation refused")
			}
			_, body, ready, e := b.Observe(unknown)
			if e != nil || !ready || string(body) != `{"observed":true}` || len(b.queue) != 0 {
				t.Fatal("observation/replay", e)
			}
			if _, e := b.RoundTrip(req(pin, path)); e == nil {
				t.Fatal("revoked grant admitted")
			}
			allowed = true
			request(b, "DELETE", "/session", paired.Session, "")
			pair(t, b)
			if _, e := b.RoundTrip(req(pin, path)); !errors.Is(e, ErrSessionChanged) {
				t.Fatal("re-pair reused pin", e)
			}
		})
	}
}

func TestJobReadRevocationBeforeHandout(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	allowed := true
	b.EnableDocumentRead(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return time.Now().Add(time.Minute), nil
	})
	b.EnableJobRead()
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","job_control_read":"job_control_read/1"}`)
	var paired struct{ Session string }
	json.Unmarshal(w.Body.Bytes(), &paired)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/job-control-read", strings.NewReader(jobReadFixture(false, false)))
		_, e := b.RoundTrip(r)
		done <- e
	}()
	select {
	case p := <-b.queue:
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	allowed = false
	if w := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`); w.Code != 204 {
		t.Fatal("revoked handout", w.Code)
	}
	cancel()
	<-done
}
