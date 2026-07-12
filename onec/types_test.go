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

// TestObjectStructure_ContentSubsystemsOmitempty proves an object without a
// Subsystem structure serializes with no "content"/"subsystems" keys, so
// existing objects' JSON stays byte-identical (omitempty, same discipline as Types).
func TestObjectStructure_ContentSubsystemsOmitempty(t *testing.T) {
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
	if strings.Contains(string(data), `"content"`) {
		t.Errorf("expected no \"content\" key for zero-value Content, got: %s", data)
	}
	if strings.Contains(string(data), `"subsystems"`) {
		t.Errorf("expected no \"subsystems\" key for zero-value Subsystems, got: %s", data)
	}
}

// TestObjectStructure_DecodeWithoutContentSubsystems proves an old-shape payload
// (no "content"/"subsystems" keys) decodes with nil Content/Subsystems; the
// fields are additive/back-compatible.
func TestObjectStructure_DecodeWithoutContentSubsystems(t *testing.T) {
	const payload = `{"name":"Контрагенты","synonym":"Контрагенты","attributes":[{"name":"ИНН","synonym":"ИНН","type":"Строка"}]}`
	var obj ObjectStructure
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if obj.Content != nil {
		t.Errorf("Content = %v, want nil", obj.Content)
	}
	if obj.Subsystems != nil {
		t.Errorf("Subsystems = %v, want nil", obj.Subsystems)
	}
}

// TestSubsystemNode_RoundTrip proves the SubsystemNode tree (member composition
// plus nested children) survives an encode/decode cycle on ObjectStructure.
func TestSubsystemNode_RoundTrip(t *testing.T) {
	in := ObjectStructure{
		Name:    "Продажи",
		Synonym: "Продажи",
		Content: []string{"Документ.РеализацияТоваровУслуг"},
		Subsystems: []SubsystemNode{
			{
				Name:     "Розница",
				FullName: "Подсистема.Продажи.Подсистема.Розница",
				Synonym:  "Розница",
				Content:  []string{"Справочник.Кассы"},
				Subsystems: []SubsystemNode{
					{
						Name:     "Касса",
						FullName: "Подсистема.Продажи.Подсистема.Розница.Подсистема.Касса",
						Synonym:  "Рабочее место кассира",
						Content:  []string{"Документ.ЧекККМ"},
					},
				},
			},
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ObjectStructure
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Subsystems) != 1 || out.Subsystems[0].Name != "Розница" {
		t.Fatalf("Subsystems roundtrip mismatch: %+v", out.Subsystems)
	}
	if out.Subsystems[0].FullName != "Подсистема.Продажи.Подсистема.Розница" {
		t.Errorf("FullName roundtrip = %q", out.Subsystems[0].FullName)
	}
	if len(out.Subsystems[0].Subsystems) != 1 || out.Subsystems[0].Subsystems[0].Name != "Касса" {
		t.Fatalf("nested Subsystems roundtrip mismatch: %+v", out.Subsystems[0].Subsystems)
	}
	if len(out.Subsystems[0].Subsystems[0].Content) != 1 || out.Subsystems[0].Subsystems[0].Content[0] != "Документ.ЧекККМ" {
		t.Errorf("nested content roundtrip mismatch: %+v", out.Subsystems[0].Subsystems[0].Content)
	}
}

// TestSubsystemForest_Decode proves the /subsystems payload (a subsystems tree
// plus an allObjects list) decodes into SubsystemForest, keys mapped correctly.
func TestSubsystemForest_Decode(t *testing.T) {
	const payload = `{
		"subsystems": [
			{
				"name": "Продажи",
				"fullName": "Подсистема.Продажи",
				"synonym": "Продажи",
				"content": ["Документ.РеализацияТоваровУслуг"],
				"subsystems": [
					{
						"name": "Розница",
						"fullName": "Подсистема.Продажи.Подсистема.Розница",
						"synonym": "Розница",
						"content": ["Справочник.Склады"]
					}
				]
			}
		],
		"allObjects": ["Документ.РеализацияТоваровУслуг", "Справочник.Склады", "Справочник.Валюты"]
	}`
	var forest SubsystemForest
	if err := json.Unmarshal([]byte(payload), &forest); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(forest.Subsystems) != 1 || forest.Subsystems[0].Name != "Продажи" {
		t.Fatalf("Subsystems mismatch: %+v", forest.Subsystems)
	}
	if len(forest.Subsystems[0].Subsystems) != 1 || forest.Subsystems[0].Subsystems[0].Name != "Розница" {
		t.Fatalf("nested subsystem mismatch: %+v", forest.Subsystems[0].Subsystems)
	}
	if len(forest.AllObjects) != 3 || forest.AllObjects[2] != "Справочник.Валюты" {
		t.Errorf("AllObjects mismatch: %v", forest.AllObjects)
	}
}

// TestSubsystemForest_RoundTrip proves SubsystemForest survives an encode/decode
// cycle with keys "subsystems"/"allObjects" preserved.
func TestSubsystemForest_RoundTrip(t *testing.T) {
	in := SubsystemForest{
		Subsystems: []SubsystemNode{
			{Name: "Финансы", FullName: "Подсистема.Финансы", Content: []string{"Документ.ПлатежноеПоручение"}},
		},
		AllObjects: []string{"Документ.ПлатежноеПоручение", "Справочник.Контрагенты"},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"subsystems"`) || !strings.Contains(string(data), `"allObjects"`) {
		t.Errorf("expected subsystems/allObjects keys, got: %s", data)
	}
	var out SubsystemForest
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Subsystems) != 1 || out.Subsystems[0].Name != "Финансы" {
		t.Errorf("Subsystems roundtrip mismatch: %+v", out.Subsystems)
	}
	if len(out.AllObjects) != 2 || out.AllObjects[0] != "Документ.ПлатежноеПоручение" {
		t.Errorf("AllObjects roundtrip mismatch: %v", out.AllObjects)
	}
}
