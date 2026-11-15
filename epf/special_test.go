package epf

import (
	"encoding/json"
	"strings"
	"testing"
)

func specialBusinesses() []string {
	target := `"document_type":"Документ.Draft","uuid":"12345678-1234-1234-1234-123456789abc","version":"v1","idempotency_key":"special"`
	return []string{`{"action":"set_mark",` + target + `,"deleted":false}`, `{"action":"set_number",` + target + `,"number":{"type":"string","value":"N-2"},"number_context":{"contract":"draft_number_metadata/1","type":"string","length":12,"allowed_length":"variable","autonumbering":false,"check_unique":true,"periodicity":"nonperiodical","numerator":"none"}}`, `{"action":"set_amount",` + strings.Replace(target, "Документ.Draft", "Документ.ПлатежноеПоручение", 1) + `,"amount_profile":"UNF-PAYMENT-AMOUNT-1","amount":{"type":"number","value":"123.45"},"amount_context":"` + strings.Repeat("a", 64) + `"}`}
}
func TestSpecialMutationExactShapes(t *testing.T) {
	for _, business := range specialBusinesses() {
		for _, op := range []string{"identity", "prepare", "commit", "receipt"} {
			env := map[string]any{"contract_version": 1, "operation": op, "business": business}
			if op == "commit" {
				env["identity"] = "actor"
				env["deadline"] = "2026-11-09T12:00:00"
			}
			raw, _ := json.Marshal(env)
			if !documentDateBody(raw, op == "commit", op == "receipt") {
				t.Fatal("refused", op, business)
			}
		}
		for _, bad := range []string{strings.Replace(business, `"version":"v1"`, `"version":""`, 1), strings.Replace(business, `"action":`, `"fields":{},"action":`, 1), strings.Replace(business, `"action":`, `"owner":"foreign","action":`, 1), strings.Replace(business, `"action":`, `"action":"update","action":`, 1)} {
			if documentWriteBusiness([]byte(bad)) {
				t.Fatal("foreign admitted", bad)
			}
		}
	}
	for _, bad := range []string{strings.Replace(specialBusinesses()[0], `"deleted":false`, `"deleted":null`, 1), strings.Replace(specialBusinesses()[1], `"numerator":"none"`, `"numerator":"shared"`, 1), strings.Replace(specialBusinesses()[1], `"check_unique":true`, `"check_unique":false`, 1), strings.Replace(specialBusinesses()[1], `"autonumbering":false`, `"autonumbering":null`, 1), strings.Replace(specialBusinesses()[2], `"value":"123.45"`, `"value":"123.450"`, 1), strings.Replace(specialBusinesses()[2], `Документ.ПлатежноеПоручение`, `Документ.R06Draft`, 1)} {
		if documentWriteBusiness([]byte(bad)) {
			t.Fatal("invalid admitted", bad)
		}
	}
}
func TestSpecialReadOperationActionPairs(t *testing.T) {
	for _, op := range []string{"get_date", "get_number", "get_mark", "get_amount", "get_amount_state"} {
		action := op
		if op == "get_date" || op == "get_number" {
			action = "get"
		}
		b := map[string]any{"action": action, "document_type": "Документ.ПлатежноеПоручение", "uuid": "12345678-1234-1234-1234-123456789abc"}
		if op == "get_mark" {
			b["deleted"] = false
		}
		if op == "get_amount" {
			b["amount_profile"] = "UNF-PAYMENT-AMOUNT-1"
			b["amount"] = map[string]string{"type": "number", "value": "1.25"}
		}
		body, _ := json.Marshal(b)
		for _, other := range []string{op, "get", "prepare", "commit", "receipt"} {
			raw, _ := json.Marshal(map[string]any{"contract_version": 1, "operation": other, "business": string(body)})
			want := other == op || (action == "get" && other == "get")
			if documentReadBody(raw) != want {
				t.Fatal("operation/action", op, other)
			}
		}
		b["fields"] = map[string]any{}
		body, _ = json.Marshal(b)
		raw, _ := json.Marshal(map[string]any{"contract_version": 1, "operation": op, "business": string(body)})
		if documentReadBody(raw) {
			t.Fatal("foreign fields")
		}
	}
}
