package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runHandlerText drives one ToolHandler and returns its rendered text payload.
func runHandlerText(t *testing.T, h mcp.ToolHandler, name string, args map[string]any) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := h(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: name, Arguments: raw},
	})
	if err != nil {
		return "", err
	}
	if len(res.Content) == 0 {
		t.Fatalf("handler %s returned no content", name)
	}
	return res.Content[0].(*mcp.TextContent).Text, nil
}

// ---- analyze_subsystems: nil source is byte-identical to today's live path ----

// TestAnalyzeSubsystemsWithSource_NilEqualsLive proves the additive WithSource
// constructor with a nil source reproduces the legacy handler exactly: same live
// HTTP fetch, same formatting, byte-for-byte. This is the parity guarantee that
// lets the paid editions inject an offline source without touching the community
// binary's behaviour.
func TestAnalyzeSubsystemsWithSource_NilEqualsLive(t *testing.T) {
	client, closeFn := newSubsystemsMock(t) // serves sampleForest() over /subsystems
	defer closeFn()

	liveH := NewAnalyzeSubsystemsHandler(client)               // legacy public ctor
	nilH := NewAnalyzeSubsystemsHandlerWithSource(client, nil) // new ctor, nil source

	for _, args := range []map[string]any{
		{"action": "orphans"},
		{"action": "containing", "object": "Контрагенты"},
		{"action": "intersections", "cross_branch_only": true},
	} {
		liveOut, err := runHandlerText(t, liveH, "analyze_subsystems", args)
		if err != nil {
			t.Fatalf("live handler %v: %v", args, err)
		}
		nilOut, err := runHandlerText(t, nilH, "analyze_subsystems", args)
		if err != nil {
			t.Fatalf("nil-source handler %v: %v", args, err)
		}
		if liveOut != nilOut {
			t.Errorf("action %v: nil-source output differs from live:\n--- live ---\n%s\n--- nil ---\n%s", args, liveOut, nilOut)
		}
	}

	// Golden: the nil-source (HTTP) path must reproduce the untouched formatter's
	// output on the same forest, byte-for-byte (this IS "today's output").
	nilOrphans, _ := runHandlerText(t, nilH, "analyze_subsystems", map[string]any{"action": "orphans"})
	if want := computeOrphans(sampleForest()); nilOrphans != want {
		t.Errorf("nil-source orphans not byte-identical to computeOrphans:\n--- got ---\n%s\n--- want ---\n%s", nilOrphans, want)
	}
}

// ---- analyze_subsystems: a non-nil source is consulted INSTEAD of HTTP ----

// srcForest is a distinct forest the offline source serves; its universe carries
// a marker object (Справочник.МаркерТолькоВДампе) absent from sampleForest, so an
// accidental HTTP path would be detectable in the output.
func srcForest() onec.SubsystemForest {
	return onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{Name: "ИсточникДампа", FullName: "Подсистема.ИсточникДампа", Content: []string{"Справочник.Номенклатура"}},
		},
		AllObjects: []string{"Справочник.Номенклатура", "Справочник.МаркерТолькоВДампе"},
	}
}

func TestAnalyzeSubsystemsWithSource_SourceBypassesHTTP(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sampleForest()) // HTTP would serve a DIFFERENT forest
	}))
	defer srv.Close()
	client := onec.NewClient(srv.URL, "", "")

	src := func(ctx context.Context) (onec.SubsystemForest, error) { return srcForest(), nil }
	h := NewAnalyzeSubsystemsHandlerWithSource(client, src)

	out, err := runHandlerText(t, h, "analyze_subsystems", map[string]any{"action": "orphans"})
	if err != nil {
		t.Fatalf("source handler: %v", err)
	}
	// The HTTP endpoint must never be contacted when a source is present.
	if hits != 0 {
		t.Errorf("expected 0 HTTP calls when a source is set, got %d", hits)
	}
	// Output must reflect the SOURCE forest, byte-for-byte, not the HTTP one.
	if want := computeOrphans(srcForest()); out != want {
		t.Errorf("source output not byte-identical to computeOrphans(srcForest):\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
	mustContain(t, out, "Справочник.МаркерТолькоВДампе")                        // unique to the source forest
	mustNotContain(t, out, "КонтрагентыПрисоединенныеФайлы", "Справочник.Валюты") // sampleForest-only (would appear only via HTTP)
}

// TestAnalyzeSubsystemsWithSource_SourceError proves a source error is surfaced
// verbatim and there is NO silent HTTP fallback (the client points at an
// unreachable address; if the handler fell back to HTTP it would fail with a
// connection error instead of the sentinel).
func TestAnalyzeSubsystemsWithSource_SourceError(t *testing.T) {
	client := onec.NewClient("http://127.0.0.1:0", "", "")
	sentinel := "дамп недоступен, требуется первичная синхронизация"
	src := func(ctx context.Context) (onec.SubsystemForest, error) {
		return onec.SubsystemForest{}, errors.New(sentinel)
	}
	h := NewAnalyzeSubsystemsHandlerWithSource(client, src)

	_, err := runHandlerText(t, h, "analyze_subsystems", map[string]any{"action": "orphans"})
	if err == nil || err.Error() != sentinel {
		t.Errorf("expected verbatim source error %q, got %v", sentinel, err)
	}
}

// ---- object_structure: nil source is byte-identical to today's live path ----

func objStructServer(obj onec.ObjectStructure, hits *int) (*onec.Client, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		if !strings.HasPrefix(r.URL.Path, "/object/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(obj)
	}))
	return onec.NewClient(srv.URL, "", ""), srv.Close
}

func sampleSubsystemObj() onec.ObjectStructure {
	return onec.ObjectStructure{
		Name:    "Продажи",
		Synonym: "Продажи",
		Content: []string{"Справочник.Контрагенты", "Документ.РеализацияТоваровУслуг"},
		Subsystems: []onec.SubsystemNode{
			{Name: "Розница", Content: []string{"Справочник.Склады"}},
		},
	}
}

func TestObjectStructureWithSource_NilEqualsLive(t *testing.T) {
	obj := sampleSubsystemObj()
	client, closeFn := objStructServer(obj, nil)
	defer closeFn()

	liveH := NewObjectStructureHandler(client)
	nilH := NewObjectStructureHandlerWithSource(client, nil)

	args := map[string]any{"object_type": "Subsystem", "object_name": "Продажи"}
	liveOut, err := runHandlerText(t, liveH, "get_object_structure", args)
	if err != nil {
		t.Fatalf("live handler: %v", err)
	}
	nilOut, err := runHandlerText(t, nilH, "get_object_structure", args)
	if err != nil {
		t.Fatalf("nil-source handler: %v", err)
	}
	if liveOut != nilOut {
		t.Errorf("nil-source output differs from live:\n--- live ---\n%s\n--- nil ---\n%s", liveOut, nilOut)
	}
	if want := formatObjectStructure(&obj); nilOut != want {
		t.Errorf("nil-source not byte-identical to formatObjectStructure:\n--- got ---\n%s\n--- want ---\n%s", nilOut, want)
	}
}

// ---- object_structure: a handled source is consulted INSTEAD of HTTP ----

func TestObjectStructureWithSource_HandledBypassesHTTP(t *testing.T) {
	httpObj := sampleSubsystemObj() // what HTTP would return
	var hits int
	client, closeFn := objStructServer(httpObj, &hits)
	defer closeFn()

	srcObj := onec.ObjectStructure{
		Name:    "ИзДампа",
		Synonym: "ИзДампа",
		Content: []string{"Справочник.МаркерТолькоВДампе"},
	}
	sub := func(ctx context.Context, objectType, objectName string) (onec.ObjectStructure, bool, error) {
		return srcObj, true, nil // handled -> offline
	}
	h := NewObjectStructureHandlerWithSource(client, sub)

	out, err := runHandlerText(t, h, "get_object_structure", map[string]any{"object_type": "Subsystem", "object_name": "X"})
	if err != nil {
		t.Fatalf("source handler: %v", err)
	}
	if hits != 0 {
		t.Errorf("expected 0 HTTP calls when the source handles the type, got %d", hits)
	}
	if want := formatObjectStructure(&srcObj); out != want {
		t.Errorf("handled output not byte-identical to formatObjectStructure(srcObj):\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
	mustContain(t, out, "Справочник.МаркерТолькоВДампе")
}

// TestObjectStructureWithSource_UnhandledFallsThroughToHTTP proves the
// type-routing contract: a source that declines (handled=false) hands the type
// back to the live HTTP path unchanged, so non-Subsystem types keep working
// against the extension exactly as today.
func TestObjectStructureWithSource_UnhandledFallsThroughToHTTP(t *testing.T) {
	httpObj := onec.ObjectStructure{
		Name:       "Товары",
		Synonym:    "Номенклатура",
		Attributes: []onec.Attribute{{Name: "Артикул", Synonym: "Артикул", Type: "Строка"}},
	}
	var hits int
	client, closeFn := objStructServer(httpObj, &hits)
	defer closeFn()

	sub := func(ctx context.Context, objectType, objectName string) (onec.ObjectStructure, bool, error) {
		return onec.ObjectStructure{}, false, nil // declines -> live HTTP owns it
	}
	h := NewObjectStructureHandlerWithSource(client, sub)

	out, err := runHandlerText(t, h, "get_object_structure", map[string]any{"object_type": "Catalog", "object_name": "Товары"})
	if err != nil {
		t.Fatalf("fall-through handler: %v", err)
	}
	if hits != 1 {
		t.Errorf("expected exactly 1 HTTP call on fall-through, got %d", hits)
	}
	if want := formatObjectStructure(&httpObj); out != want {
		t.Errorf("fall-through output not byte-identical to the live formatter:\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

// TestObjectStructureWithSource_HandledError proves a handled source error is
// surfaced verbatim with no HTTP fallback (unreachable client).
func TestObjectStructureWithSource_HandledError(t *testing.T) {
	client := onec.NewClient("http://127.0.0.1:0", "", "")
	sentinel := "подсистема Тест не найдена в дампе"
	sub := func(ctx context.Context, objectType, objectName string) (onec.ObjectStructure, bool, error) {
		return onec.ObjectStructure{}, true, errors.New(sentinel)
	}
	h := NewObjectStructureHandlerWithSource(client, sub)

	_, err := runHandlerText(t, h, "get_object_structure", map[string]any{"object_type": "Subsystem", "object_name": "Тест"})
	if err == nil || err.Error() != sentinel {
		t.Errorf("expected verbatim source error %q, got %v", sentinel, err)
	}
}
