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

	want := []string{"СправочникСсылка.Пользователи", "СправочникСсылка.ВнешниеПользователи"}
	if len(obj.Types) != len(want) {
		t.Fatalf("Types = %v, want %v", obj.Types, want)
	}
	for i, w := range want {
		if obj.Types[i] != w {
			t.Errorf("Types[%d] = %q, want %q", i, obj.Types[i], w)
		}
	}
}
