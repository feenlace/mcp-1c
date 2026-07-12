package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// TestHandleObject_DefinedType exercises the offline DefinedType fixture: the
// mock server must serve a "types" composition that round-trips into
// onec.ObjectStructure with no live 1C involved.
func TestHandleObject_DefinedType(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp/object/DefinedType/ЗначениеДоступа", nil)
	rec := httptest.NewRecorder()

	handleObject(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var obj onec.ObjectStructure
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := []string{"Справочник.Пользователи", "Справочник.ВнешниеПользователи"}
	if len(obj.Types) != len(want) {
		t.Fatalf("Types = %v, want %v", obj.Types, want)
	}
	for i, w := range want {
		if obj.Types[i] != w {
			t.Errorf("Types[%d] = %q, want %q", i, obj.Types[i], w)
		}
	}
}

// TestHandleObject_DefinedType_Primitive covers a DefinedType whose composition
// mixes a reference type with a primitive (Строка): both must appear in "types".
func TestHandleObject_DefinedType_Primitive(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp/object/DefinedType/ЛюбаяСсылкаИлиСтрока", nil)
	rec := httptest.NewRecorder()

	handleObject(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var obj onec.ObjectStructure
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}

	got := map[string]bool{}
	for _, ty := range obj.Types {
		got[ty] = true
	}
	for _, want := range []string{"Справочник.Номенклатура", "Строка"} {
		if !got[want] {
			t.Errorf("types %v missing %q", obj.Types, want)
		}
	}
}

// TestHandleObject_DefinedType_Nested covers a DefinedType whose composition
// references another DefinedType. The offline fixture only proves the Go path
// round-trips whatever "types" the platform returns without error; real nested
// .Типы() expansion is a real-1C concern and is not asserted here.
func TestHandleObject_DefinedType_Nested(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp/object/DefinedType/СоставнойЧерезОпределяемый", nil)
	rec := httptest.NewRecorder()

	handleObject(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var obj onec.ObjectStructure
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}

	got := map[string]bool{}
	for _, ty := range obj.Types {
		got[ty] = true
	}
	for _, want := range []string{"ОпределяемыйТип.ЗначениеДоступа", "Справочник.Организации"} {
		if !got[want] {
			t.Errorf("types %v missing %q", obj.Types, want)
		}
	}
}

// TestHandleObject_Subsystem exercises the offline Subsystem fixture: the mock
// server must serve a "content" list plus a nested "subsystems" tree that
// round-trips into onec.ObjectStructure with no live 1C involved.
func TestHandleObject_Subsystem(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp/object/Subsystem/Продажи", nil)
	rec := httptest.NewRecorder()

	handleObject(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var obj onec.ObjectStructure
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(obj.Content) == 0 {
		t.Errorf("expected non-empty Состав for Продажи, got %v", obj.Content)
	}
	if len(obj.Subsystems) == 0 {
		t.Fatalf("expected child subsystems for Продажи, got none")
	}
	// At least one child must itself carry a nested subsystem (>=2 levels).
	nested := false
	for _, s := range obj.Subsystems {
		if len(s.Subsystems) > 0 {
			nested = true
		}
	}
	if !nested {
		t.Errorf("expected at least one nested (>=2 level) subsystem, got %+v", obj.Subsystems)
	}
}

// TestHandleSubsystems exercises the offline /subsystems fixture: the mock must
// serve a subsystem forest plus an allObjects list that round-trips into
// onec.SubsystemForest and covers every analyze_subsystems edge case with no
// live 1C: a nested subsystem, an orphan-only object, and a noise object.
func TestHandleSubsystems(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp/subsystems", nil)
	rec := httptest.NewRecorder()

	handleSubsystems(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var forest onec.SubsystemForest
	if err := json.Unmarshal(rec.Body.Bytes(), &forest); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(forest.Subsystems) == 0 {
		t.Fatal("expected root subsystems, got none")
	}
	if len(forest.AllObjects) == 0 {
		t.Fatal("expected allObjects, got none")
	}

	// At least one root carries a nested subsystem (>= 2 levels).
	nested := false
	for _, s := range forest.Subsystems {
		if len(s.Subsystems) > 0 {
			nested = true
		}
	}
	if !nested {
		t.Errorf("expected at least one nested subsystem, got %+v", forest.Subsystems)
	}

	// Collect every object listed in any subsystem's Состав.
	inSubsystem := map[string]bool{}
	var collect func(nodes []onec.SubsystemNode)
	collect = func(nodes []onec.SubsystemNode) {
		for _, n := range nodes {
			for _, o := range n.Content {
				inSubsystem[o] = true
			}
			collect(n.Subsystems)
		}
	}
	collect(forest.Subsystems)

	all := map[string]bool{}
	hasNoise := false
	for _, o := range forest.AllObjects {
		all[o] = true
		if strings.HasSuffix(o, "ПрисоединенныеФайлы") {
			hasNoise = true
		}
	}
	if !hasNoise {
		t.Errorf("expected a noise object in allObjects, got %v", forest.AllObjects)
	}

	// An orphan: present in allObjects, absent from every subsystem's Состав.
	if !all["Справочник.Валюты"] {
		t.Errorf("expected orphan Справочник.Валюты in allObjects")
	}
	if inSubsystem["Справочник.Валюты"] {
		t.Errorf("Справочник.Валюты must be an orphan (not in any subsystem)")
	}

	// A cross-branch object: present in two different subsystems' Состав.
	count := 0
	var countIn func(nodes []onec.SubsystemNode)
	countIn = func(nodes []onec.SubsystemNode) {
		for _, n := range nodes {
			for _, o := range n.Content {
				if o == "Документ.РеализацияТоваровУслуг" {
					count++
				}
			}
			countIn(n.Subsystems)
		}
	}
	countIn(forest.Subsystems)
	if count < 2 {
		t.Errorf("expected Документ.РеализацияТоваровУслуг in >= 2 subsystems, got %d", count)
	}
}

// TestHandleObject_Subsystem_Empty proves the empty-subsystem fixture serves a
// structure with no members and no children, covering the no-block render path.
func TestHandleObject_Subsystem_Empty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp/object/Subsystem/ПустаяПодсистема", nil)
	rec := httptest.NewRecorder()

	handleObject(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var obj onec.ObjectStructure
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(obj.Content) != 0 || len(obj.Subsystems) != 0 {
		t.Errorf("expected empty subsystem, got Content=%v Subsystems=%+v", obj.Content, obj.Subsystems)
	}
}
