package epf

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const dateBusiness = `{"action":"update","document_type":"Документ.CountDocs","fields":{"Дата":{"type":"date","value":"2026-11-08T12:00:00"}},"idempotency_key":"date-test","uuid":"12345678-1234-1234-1234-123456789abc","version":"original"}`

func dateEnvelope(operation string) string {
	body := map[string]any{"contract_version": 1, "operation": operation, "business": dateBusiness}
	if operation == "commit" {
		body["identity"] = "actual-user"
		body["deadline"] = "2026-11-08T12:00:30"
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func TestDateAndReceiptAreClosedOperations(t *testing.T) {
	for _, operation := range []string{"identity", "prepare", "commit", "receipt"} {
		if !documentDateBody([]byte(dateEnvelope(operation)), operation == "commit", operation == "receipt") {
			t.Fatal(operation)
		}
	}
	for _, business := range []string{
		strings.Replace(dateBusiness, `"action":"update"`, `"action":"create"`, 1),
		strings.Replace(dateBusiness, `"type":"date"`, `"type":"date","authority":"forged"`, 1),
		strings.Replace(dateBusiness, `2026-11-08T12:00:00`, `not-a-date`, 1),
	} {
		raw, _ := json.Marshal(map[string]any{"contract_version": 1, "operation": "receipt", "business": business})
		if documentDateBody(raw, false, true) {
			t.Fatal("broadened receipt", business)
		}
	}
}

func TestReceiptAfterExpiryUsesFreshPairWithoutPaidReadmission(t *testing.T) {
	b, _ := New(token, nil)
	calls := 0
	b.EnableDocumentRead(func(context.Context) (time.Time, error) { calls++; return time.Now().Add(-time.Second), ErrUnavailable })
	if err := b.EnableDocumentDateWrite(); err != nil {
		t.Fatal(err)
	}
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native-owner","core":"actual-core-proof"}`)
	var pair struct {
		Session string `json:"session"`
	}
	json.Unmarshal(w.Body.Bytes(), &pair)
	pinned, err := b.PinSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequestWithContext(pinned, "POST", "http://epf.invalid/document-read", strings.NewReader(dateEnvelope("receipt")))
		out, e := b.RoundTrip(r)
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
		t.Fatal("receipt not queued")
	}
	if p.task.Capability != "pro.document-receipt" || p.task.Core != "actual-core-proof" || p.task.Session != pair.Session || !time.Now().Before(p.task.ExecuteBefore) {
		t.Fatal("lost observing binding")
	}
	if request(b, "POST", "/poll", pair.Session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("observation refused")
	}
	if request(b, "POST", "/result/"+p.task.ID, pair.Session, `{"identity":"native-owner","status":200,"body":{"found":false}}`).Code != 204 {
		t.Fatal("result refused")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("receipt reused paid admission")
	}
	r, _ := http.NewRequest("POST", "http://epf.invalid/document-commit", strings.NewReader(dateEnvelope("commit")))
	if _, err := b.RoundTrip(r); err == nil {
		t.Fatal("new expired mutation admitted")
	}
	if len(b.queue) != 0 {
		t.Fatal("expired mutation queued")
	}
}
