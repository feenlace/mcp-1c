package epf

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOrdinaryWriteTypesTablesAndClosedInputs(t *testing.T) {
	good := `{"action":"create","document_type":"Документ.Draft","idempotency_key":"test","fields":{"Text":{"type":"string","value":"text"},"N":{"type":"number","value":"123.45"},"B":{"type":"bool","value":false},"Дата":{"type":"date","value":"2026-11-09T12:00:00"},"Ref":{"type":"reference","value":{"type":"Справочник.Items","uuid":"12345678-1234-1234-1234-123456789abc"}},"Enum":{"type":"enum","value":{"type":"Перечисление.State","member":"Ready"}}},"tables":{"Rows":[{"Text":{"type":"string","value":"row"}}],"Extra":[]}}`
	if !documentWriteBusiness([]byte(good)) {
		t.Fatal("ordinary types/tables refused")
	}
	for _, op := range []string{"identity", "prepare", "commit", "receipt"} {
		envelope := map[string]any{"contract_version": 1, "operation": op, "business": good}
		if op == "commit" {
			envelope["identity"] = "owner"
			envelope["deadline"] = "2026-11-09T12:00:30"
		}
		raw, _ := json.Marshal(envelope)
		if !documentDateBody(raw, op == "commit", op == "receipt") {
			t.Fatal(op)
		}
	}
	for _, bad := range []string{
		strings.Replace(good, `"action":"create"`, `"action":"post"`, 1),
		strings.Replace(good, `"fields":`, `"uuid":"12345678-1234-1234-1234-123456789abc","fields":`, 1),
		strings.Replace(good, `"value":false`, `"Value":false`, 1),
		strings.Replace(good, `"value":false`, `"value":null`, 1),
		strings.Replace(good, `"value":false`, `"value":false,"value":true`, 1),
		strings.Replace(good, `"value":"123.45"`, `"value":123.45`, 1),
		strings.Replace(good, `"member":"Ready"`, `"member":"Ready","authority":"forged"`, 1),
		strings.Replace(good, `"Extra":[]`, `"Extra":null`, 1),
		strings.Replace(good, `"Rows":[{"Text":{"type":"string","value":"row"}}]`, `"Rows":[null]`, 1),
	} {
		if documentWriteBusiness([]byte(bad)) {
			t.Fatal("accepted malformed ordinary input")
		}
	}
	base := `{"action":"create","document_type":"Документ.Draft","idempotency_key":"size","fields":{"Text":{"type":"string","value":"` + `"}}}`
	raw := strings.Replace(base, `"value":""`, `"value":"`+strings.Repeat("x", documentWriteBusinessLimit-len(base))+`"`, 1)
	if len(raw) != documentWriteBusinessLimit || !documentWriteBusiness([]byte(raw)) {
		t.Fatal("raw boundary refused", len(raw))
	}
	if documentWriteBusiness([]byte(strings.Replace(raw, `"value":"`, `"value":"x`, 1))) {
		t.Fatal("raw boundary overflow")
	}
}

func TestDocumentWorkflowPinsOriginalCutoff(t *testing.T) {
	b, _ := New(token, nil)
	defer b.Close()
	cutoff := time.Now().Add(90 * time.Millisecond)
	b.EnableDocumentRead(func(context.Context) (time.Time, error) { return cutoff, nil })
	b.EnableDocumentDateWrite()
	w := request(b, "POST", "/pair", pairCode(b), `{"protocol":1,"identity":"native","core":"core"}`)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	ctx, err := b.PinDocument(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := ctx.Value(documentCutoffKey{}).(time.Time)
	cutoff = time.Now().Add(time.Minute)
	again, err := b.PinDocument(ctx)
	if err != nil || !again.Value(documentCutoffKey{}).(time.Time).Equal(original) {
		t.Fatal("replacement extended original grant")
	}
	time.Sleep(time.Until(original) + time.Millisecond)
	r, _ := http.NewRequestWithContext(again, "POST", "http://epf.invalid/document-read", strings.NewReader(`{"contract_version":1,"operation":"identity","business":"{\"action\":\"describe\"}"}`))
	if out, err := b.RoundTrip(r); err == nil {
		out.Body.Close()
		t.Fatal("new native call started beyond original cutoff")
	}
	if len(b.queue) != 0 {
		t.Fatal("expired workflow enqueued")
	}
}
