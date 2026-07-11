package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
