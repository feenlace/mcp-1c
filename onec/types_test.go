package onec

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestObjectStructure_TypesOmitempty proves an object without a DefinedType
// composition serializes with no "types" key, so existing objects' JSON stays
// byte-identical (omitempty).
func TestObjectStructure_TypesOmitempty(t *testing.T) {
	obj := ObjectStructure{
		Name:    "Контрагенты",
		Synonym: "Контрагенты",
		Attributes: []Attribute{
			{Name: "ИНН", Synonym: "ИНН", Type: "Строка"},
		},
	}
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"types"`) {
		t.Errorf("expected no \"types\" key for zero-value Types, got: %s", data)
	}
}

// TestObjectStructure_DecodeWithoutTypes proves an old-shape payload (no "types"
// key) decodes cleanly with Types == nil — the field is additive/back-compatible.
func TestObjectStructure_DecodeWithoutTypes(t *testing.T) {
	const payload = `{"name":"Контрагенты","synonym":"Контрагенты","attributes":[{"name":"ИНН","synonym":"ИНН","type":"Строка"}]}`
	var obj ObjectStructure
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if obj.Types != nil {
		t.Errorf("Types = %v, want nil", obj.Types)
	}
	if obj.Name != "Контрагенты" {
		t.Errorf("Name = %q, want Контрагенты", obj.Name)
	}
}

// TestObjectStructure_DecodeWithTypes proves the standard decoder populates Types
// from a "types" key with no custom UnmarshalJSON.
func TestObjectStructure_DecodeWithTypes(t *testing.T) {
	const payload = `{"name":"ЗначениеДоступа","types":["Справочник.Пользователи","Строка"]}`
	var obj ObjectStructure
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(obj.Types) != 2 || obj.Types[0] != "Справочник.Пользователи" || obj.Types[1] != "Строка" {
		t.Errorf("Types = %v, want [Справочник.Пользователи Строка]", obj.Types)
	}
}
