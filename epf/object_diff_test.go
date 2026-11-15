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

func TestObjectDiffOptInExactBytesAndGrant(t *testing.T) {
	b, _ := New(token, nil)
	body := `{"contract_version":"object_diff/1","run_id":"x","catalog_type":"Справочник.Items","left_uuid":"left","right_uuid":"right","row_keys":{}}`
	req := func(raw string) *http.Request {
		r, _ := http.NewRequest("POST", "http://epf.invalid/object-diff", strings.NewReader(raw))
		return r
	}
	if _, e := b.RoundTrip(req(body)); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("Community enabled paid comparison", e)
	}
	if b.EnableObjectDiff() == nil {
		t.Fatal("comparison enabled without verified-cutoff callback")
	}
	allowed := true
	cutoff := time.Now().Add(3 * time.Second)
	b.EnableQueryPackage(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	})
	if e := b.EnableObjectDiff(); e != nil {
		t.Fatal(e)
	}
	session := pair(t, b)
	if _, e := b.RoundTrip(req(body)); !errors.Is(e, ErrHybridUnavailable) {
		t.Fatal("no core/marker admitted", e)
	}
	request(b, "DELETE", "/session", session, "")
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","query_package":"query_package/1","object_diff":"object_diff/1"}`)
	var paired struct {
		Session string `json:"session"`
	}
	json.Unmarshal(w.Body.Bytes(), &paired)
	session = paired.Session
	for _, bad := range []string{strings.Replace(body, `"row_keys":{}`, `"row_keys":null`, 1), strings.Replace(body, `"row_keys":{}`, `"row_keys":{},"expected_principal":"fake"`, 1), strings.Replace(body, `"row_keys":{}`, `"row_keys":{},"principal_probe":true`, 1), strings.Replace(body, `"row_keys":{}`, `"row_keys":{},"row_keys":{}`, 1)} {
		if _, e := b.RoundTrip(req(bad)); e == nil {
			t.Fatal("invalid/probe data admitted")
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
	if task.Capability != "advanced.object-diff" {
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
func TestObjectDiffMarkerIsStrictAndOptional(t *testing.T) {
	for _, marker := range []string{`null`, `"object_diff/2"`, `"object_diff/1","object_diff":"object_diff/1"`} {
		b, _ := New(token, nil)
		if request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","object_diff":`+marker+`}`).Code != 400 {
			t.Fatal("invalid comparison marker paired")
		}
	}
}
