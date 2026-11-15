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

func jobMutationFixture(retry, cancel bool) string {
	var in map[string]any
	json.Unmarshal([]byte(jobReadFixture(retry, false)), &in)
	in["expected_identity"] = "fixed-native-domain-owner"
	in["expected_start"] = "2026-11-07T10:00:00"
	if retry {
		in["expected_composition"] = "fixed-composition"
		in["expected_version_id"] = graphID
		in["expected_update_id"] = graphID
		in["expected_base_token"] = strings.Repeat("a", 64)
		in["key"] = "r24-" + graphID
		if cancel {
			in["new_run_id"] = graphID
			in["new_run_start"] = "2026-11-07T11:00:00"
		}
	}
	raw, _ := json.Marshal(in)
	return string(raw)
}
func TestJobMutationClosedSelectedBodies(t *testing.T) {
	for _, c := range []struct{ retry, cancel bool }{{false, true}, {true, false}, {true, true}} {
		raw := jobMutationFixture(c.retry, c.cancel)
		if !jobMutationBody([]byte(raw), c.retry, c.cancel) {
			t.Fatal("valid selected mutation refused")
		}
		for _, bad := range []string{strings.Replace(raw, `"expected_identity":"fixed-native-domain-owner"`, `"expected_identity":""`, 1), strings.Replace(raw, `"expected_start":"2026-11-07T10:00:00"`, `"expected_start":"bad"`, 1), strings.TrimSuffix(raw, "}") + `,"method":"arbitrary"}`} {
			if jobMutationBody([]byte(bad), c.retry, c.cancel) {
				t.Fatal("broadened mutation admitted")
			}
		}
		if c.retry {
			var in map[string]any
			json.Unmarshal([]byte(raw), &in)
			in["key"] = "arbitrary"
			bad, _ := json.Marshal(in)
			if jobMutationBody(bad, true, c.cancel) {
				t.Fatal("arbitrary job key admitted")
			}
		}
	}
}
func TestJobMutationGrantPinsUnknownAndLateCollection(t *testing.T) {
	for _, path := range []string{"/job-control-cancel", "/job-retry-start", "/job-retry-cancel"} {
		t.Run(path, func(t *testing.T) {
			b, _ := New(token, nil)
			defer b.Close()
			cutoff := time.Now().Add(10 * time.Second)
			allowed := true
			b.EnableDocumentRead(func(context.Context) (time.Time, error) {
				if !allowed {
					return time.Time{}, ErrUnavailable
				}
				return cutoff, nil
			})
			b.EnableJobRead()
			b.EnableJobMutation()
			session := pair(t, b)
			if _, e := b.PinJobMutation(context.Background(), path != "/job-control-cancel"); !errors.Is(e, ErrHybridUnavailable) {
				t.Fatal("old/no core mutation admitted", e)
			}
			request(b, "DELETE", "/session", session, "")
			w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","job_control":"job_control/1","job_retry":"job_retry/2"}`)
			var paired struct{ Session string }
			json.Unmarshal(w.Body.Bytes(), &paired)
			if w.Code != 200 {
				t.Fatal("pair", w.Code)
			}
			pin, e := b.PinJobMutation(context.Background(), path != "/job-control-cancel")
			if e != nil {
				t.Fatal(e)
			}
			original := cutoff
			cutoff = cutoff.Add(time.Hour)
			ctx, cancel := context.WithCancel(pin)
			done := make(chan error, 1)
			raw := jobMutationFixture(path != "/job-control-cancel", path == "/job-retry-cancel")
			go func() {
				r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid"+path, strings.NewReader(raw))
				res, e := b.RoundTrip(r)
				if res != nil {
					res.Body.Close()
				}
				done <- e
			}()
			select {
			case p := <-b.queue:
				if p.task.BodyText != raw || p.task.Core != "real-core" || p.task.Session != paired.Session || !p.task.ExecuteBefore.Equal(original) || !jobMutationCapability(p.task.Capability) {
					t.Fatal("mutation binding/cutoff lost")
				}
				b.queue <- p
			case <-time.After(time.Second):
				t.Fatal("not queued")
			}
			poll := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`)
			var task Task
			json.Unmarshal(poll.Body.Bytes(), &task)
			if poll.Code != 200 {
				t.Fatal("not handed")
			}
			cancel()
			e = <-done
			var unknown *UnknownOutcome
			if !errors.As(e, &unknown) {
				t.Fatal("handed mutation became not-delivered", e)
			}
			allowed = false
			payload, _ := json.Marshal(map[string]any{"identity": "native-owner", "status": 200, "body_text": `{"late":"original-result"}`})
			if request(b, "POST", "/result/"+task.ID, paired.Session, string(payload)).Code != 204 {
				t.Fatal("allowed original result lost")
			}
			_, body, ready, e := b.Observe(unknown)
			if e != nil || !ready || !strings.Contains(string(body), "original-result") || len(b.queue) != 0 {
				t.Fatal("collection/replay", e)
			}
			r, _ := http.NewRequestWithContext(pin, "POST", "http://epf.invalid"+path, strings.NewReader(raw))
			if _, e := b.RoundTrip(r); e == nil {
				t.Fatal("expired mutation re-admitted")
			}
		})
	}
}
func TestDocumentOwnerBaseUsesValidatedCurrentPin(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	core := map[string]any{"epf_contract": "document-read/1", "identity_contract": 1, "principal_uuid": graphID, "config_name": "Fixture", "config_version": "1", "core_owner": "MCP_HTTPService", "core_install": "22222222-2222-4222-8222-222222222222", "core_version": "1", "base": strings.Repeat("a", 64), "native_session": "7"}
	raw, _ := json.Marshal(core)
	pairRaw, _ := json.Marshal(map[string]any{"protocol": 1, "identity": "native", "core": string(raw)})
	request(b, "POST", "/pair", pairCode(b), string(pairRaw))
	pin, _ := b.PinOwnerObservation(context.Background())
	base, e := b.DocumentOwnerBase(pin, graphID, "Fixture", "1")
	if e != nil || base != strings.Repeat("a", 64) {
		t.Fatal("validated base accessor", e)
	}
	if _, e = b.DocumentOwnerBase(context.Background(), graphID, "Fixture", "1"); e == nil {
		t.Fatal("pair-only evidence admitted")
	}
	if _, e = b.DocumentOwnerBase(pin, "33333333-3333-4333-8333-333333333333", "Fixture", "1"); e == nil {
		t.Fatal("foreign principal admitted")
	}
	b.mu.Lock()
	b.core = strings.Replace(string(raw), `"native_session":"7"`, `"native_session":"8"`, 1)
	b.mu.Unlock()
	if _, e = b.DocumentOwnerBase(pin, graphID, "Fixture", "1"); e == nil {
		t.Fatal("stale native pin admitted")
	}
}
