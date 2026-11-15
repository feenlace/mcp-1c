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

const printExportRequest = `{"contract_version":1,"operation":"describe","document_type":"Документ.CountDocs"}`

func printExportOptIn(t *testing.T, b *Bridge) {
	t.Helper()
	route, ok := any(b).(interface{ EnablePrintExport() error })
	if !ok {
		t.Fatal("finite EPF print_export opt-in absent")
	}
	if e := route.EnablePrintExport(); e != nil {
		t.Fatal(e)
	}
}
func TestPrintExportExactBytesGrantAndNoReplay(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	req := func(ctx context.Context, raw string) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/print-export", strings.NewReader(raw))
		return r
	}
	if _, e := b.RoundTrip(req(context.Background(), printExportRequest)); !errors.Is(e, ErrCapabilityUnavailable) {
		t.Fatal("Community print_export admission", e)
	}
	allowed := true
	cutoff := time.Now().Add(10 * time.Second)
	b.EnableDocumentRead(func(context.Context) (time.Time, error) {
		if !allowed {
			return time.Time{}, ErrUnavailable
		}
		return cutoff, nil
	})
	printExportOptIn(t, b)
	session := pair(t, b)
	if _, e := b.RoundTrip(req(context.Background(), printExportRequest)); !errors.Is(e, ErrHybridUnavailable) {
		t.Fatal("missing core admitted", e)
	}
	request(b, "DELETE", "/session", session, "")
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"real-core","print_export":"print_export/1"}`)
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
		r, e := b.RoundTrip(req(ctx, printExportRequest))
		if r != nil {
			r.Body.Close()
		}
		done <- e
	}()
	select {
	case p := <-b.queue:
		if p.task.BodyText != printExportRequest || p.task.Body != nil || p.task.Core != "real-core" || p.task.Session != paired.Session || !p.task.ExecuteBefore.Equal(originalCutoff) {
			t.Fatal("body/context/cutoff changed")
		}
		b.queue <- p
	case <-time.After(time.Second):
		t.Fatal("not queued")
	}
	poll := request(b, "POST", "/poll", paired.Session, `{"identity":"native-owner"}`)
	var task Task
	json.Unmarshal(poll.Body.Bytes(), &task)
	if task.Capability != "pro.print-export" {
		t.Fatal("wrong native capability")
	}
	cancel()
	e = <-done
	var unknown *UnknownOutcome
	if !errors.As(e, &unknown) {
		t.Fatal("lost UnknownOutcome", e)
	}
	if _, err := b.RoundTrip(req(context.Background(), printExportRequest)); !errors.Is(err, ErrUnavailable) {
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
	if _, e := b.RoundTrip(req(pin, printExportRequest)); e == nil {
		t.Fatal("expired grant admitted")
	}
	allowed = true
	request(b, "DELETE", "/session", paired.Session, "")
	pair(t, b)
	if _, e := b.RoundTrip(req(pin, printExportRequest)); !errors.Is(e, ErrSessionChanged) {
		t.Fatal("cross-pair read admitted", e)
	}
}

func TestPrintExportFiniteBodiesAndStoredBudget(t *testing.T) {
	good := []string{printExportRequest, `{"contract_version":1,"operation":"document_list","document_type":"Документ.CountDocs","from":"2026-01-01T00:00:00","to":"2026-02-01T00:00:00","format":"xlsx"}`, `{"contract_version":1,"operation":"document","document_type":"Документ.CountDocs","uuid":"123","command":"Invoice","format":"pdf"}`, `{"contract_version":1,"operation":"report","report":"Отчет.R","variant":"Main","format":"xlsx"}`}
	for _, raw := range good {
		if !printExportBody([]byte(raw)) {
			t.Fatal("valid fixed body refused")
		}
	}
	for _, raw := range []string{strings.Replace(good[0], `"describe"`, `"execute"`, 1), strings.TrimSuffix(good[0], "}") + `,"operation":"describe"}`, strings.TrimSuffix(good[0], "}") + `,"format":null}`, strings.Replace(good[1], `"xlsx"`, `"csv"`, 1), strings.Replace(good[1], `"contract_version":1`, `"contract_version":true`, 1), strings.TrimSuffix(good[1], "}") + `,"filters":{"x":{"type":"string","value":null}}}`} {
		if printExportBody([]byte(raw)) {
			t.Fatal("open body admitted")
		}
	}
	for _, operation := range []string{"describe", "document_list", "forged"} {
		b, _ := New(token, nil)
		session := pair(t, b)
		body := good[1]
		if operation == "describe" {
			body = good[0]
		}
		if operation == "forged" {
			body = `{}`
		}
		task := Task{ID: "selected", Capability: "pro.print-export", BodyText: body, CollectBefore: time.Now().Add(time.Minute)}
		b.mu.Lock()
		b.pending[task.ID] = &pending{task: task, session: session, identity: "native-owner", handed: true, result: make(chan result, 1)}
		b.mu.Unlock()
		raw := `{"data_base64":"` + strings.Repeat("A", 2<<20) + `"}`
		payload, _ := json.Marshal(map[string]any{"identity": "native-owner", "status": 200, "body_text": raw})
		w := request(b, "POST", "/result/"+task.ID, session, string(payload))
		if (w.Code == 204) != (operation == "document_list") {
			t.Fatalf("selected budget %s status %d", operation, w.Code)
		}
		b.Close()
	}
}

func TestPrintExportNon200SelectedRawBoundary(t *testing.T) {
	for _, size := range []int{12 << 20, (12 << 20) + 1} {
		b, _ := New(token, nil)
		session := pair(t, b)
		task := Task{ID: "selected-error", Capability: "pro.print-export", BodyText: `{"contract_version":1,"operation":"document_list","document_type":"Документ.CountDocs","from":"2026-01-01T00:00:00","to":"2026-01-03T00:00:00","format":"xlsx"}`, CollectBefore: time.Now().Add(time.Minute)}
		p := &pending{task: task, session: session, identity: "native-owner", handed: true, result: make(chan result, 1)}
		b.mu.Lock()
		b.pending[task.ID] = p
		b.mu.Unlock()
		raw := `{"error":"` + strings.Repeat("x", size-len(`{"error":""}`)) + `"}`
		payload, _ := json.Marshal(map[string]any{"identity": "native-owner", "status": 500, "body": json.RawMessage(raw)})
		w := request(b, "POST", "/result/"+task.ID, session, string(payload))
		if (w.Code == 204) != (size == 12<<20) {
			t.Fatalf("non200 raw size%d status%d", size, w.Code)
		}
		b.mu.Lock()
		stored := p.terminal
		b.mu.Unlock()
		if size == 12<<20 {
			if stored == nil || len(stored.Body) != size {
				t.Fatal("exact raw bound not retained")
			}
		} else if stored != nil {
			t.Fatal("over-limit raw stored")
		}
		b.Close()
	}
}
