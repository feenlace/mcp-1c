package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sampleForest builds a subsystem forest exercising the analysis edge cases:
//   - Справочник.Контрагенты: in Продажи (root) AND Продажи>Розница (same root)
//   - Документ.РеализацияТоваровУслуг: in Продажи AND Финансы (different roots)
//   - Справочник.Склады: only in the nested Продажи>Розница
//   - Документ.ПоступлениеТоваровУслуг: only in Закупки
//   - Справочник.Номенклатура: only in Продажи (single membership)
//   - Справочник.Валюты, РегистрСведений.КурсыВалют: applied but orphaned
//   - Справочник.КонтрагентыПрисоединенныеФайлы: orphaned noise (must be filtered)
func sampleForest() onec.SubsystemForest {
	return onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{
				Name:     "Продажи",
				FullName: "Подсистема.Продажи",
				Content:  []string{"Справочник.Контрагенты", "Справочник.Номенклатура", "Документ.РеализацияТоваровУслуг"},
				Subsystems: []onec.SubsystemNode{
					{
						Name:     "Розница",
						FullName: "Подсистема.Продажи.Подсистема.Розница",
						Content:  []string{"Справочник.Контрагенты", "Справочник.Склады"},
					},
				},
			},
			{
				Name:     "Закупки",
				FullName: "Подсистема.Закупки",
				Content:  []string{"Документ.ПоступлениеТоваровУслуг"},
			},
			{
				Name:     "Финансы",
				FullName: "Подсистема.Финансы",
				Content:  []string{"Документ.РеализацияТоваровУслуг"},
			},
		},
		AllObjects: []string{
			"Справочник.Контрагенты",
			"Справочник.Номенклатура",
			"Справочник.Склады",
			"Справочник.Валюты",
			"Документ.РеализацияТоваровУслуг",
			"Документ.ПоступлениеТоваровУслуг",
			"РегистрСведений.КурсыВалют",
			"Справочник.КонтрагентыПрисоединенныеФайлы",
		},
	}
}

func mustContain(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("expected %q in output, got:\n%s", w, text)
		}
	}
}

func mustNotContain(t *testing.T, text string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			t.Errorf("did not expect %q in output, got:\n%s", u, text)
		}
	}
}

// ---- orphans ----

func TestComputeOrphans(t *testing.T) {
	out := computeOrphans(sampleForest())

	// Header count is 2: Справочник.Валюты + РегистрСведений.КурсыВалют.
	mustContain(t, out, "# Объекты вне подсистем (2)", "Справочник.Валюты", "РегистрСведений.КурсыВалют")

	// An object in a NESTED subsystem's Состав is not an orphan.
	mustNotContain(t, out, "Справочник.Склады")
	// Objects that are members somewhere are not orphans.
	mustNotContain(t, out, "Документ.РеализацияТоваровУслуг", "Справочник.Контрагенты")
	// Noise must be filtered even though it belongs to no subsystem.
	mustNotContain(t, out, "КонтрагентыПрисоединенныеФайлы")
}

func TestComputeOrphans_EmptyForest(t *testing.T) {
	forest := onec.SubsystemForest{
		Subsystems: nil,
		AllObjects: []string{"Справочник.Валюты", "Документ.РеализацияТоваровУслуг", "РегистрСведений.КурсыВалют"},
	}
	out := computeOrphans(forest)
	// With no subsystems, every applied object is orphaned.
	mustContain(t, out, "# Объекты вне подсистем (3)",
		"Справочник.Валюты", "Документ.РеализацияТоваровУслуг", "РегистрСведений.КурсыВалют")
}

func TestComputeOrphans_NoneEmptyMessage(t *testing.T) {
	forest := onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{Name: "Продажи", FullName: "Подсистема.Продажи", Content: []string{"Справочник.Контрагенты"}},
		},
		AllObjects: []string{"Справочник.Контрагенты"},
	}
	out := computeOrphans(forest)
	mustContain(t, out, "# Объекты вне подсистем (0)", "распределены")
}

// TestComputeOrphans_EmptyUniverse (bug #2) proves an empty applicable-object
// universe yields a distinct diagnostic message, NOT the "everything is
// distributed" wording that implies full coverage.
func TestComputeOrphans_EmptyUniverse(t *testing.T) {
	out := computeOrphans(onec.SubsystemForest{AllObjects: nil})
	mustContain(t, out, "Универсум применимых объектов пуст или недоступен.")
	mustNotContain(t, out, "распределены")
}

// TestComputeOrphans_WarningsDiagnostics (bug #3) proves a forest carrying
// non-fatal universe-builder warnings surfaces a diagnostics line so a degraded
// (partial) universe is visible.
func TestComputeOrphans_WarningsDiagnostics(t *testing.T) {
	forest := sampleForest()
	forest.Warnings = []string{"Документы: недоступно"}
	out := computeOrphans(forest)
	mustContain(t, out, "Диагностика", "Документы: недоступно")
	// The diagnostics line is customer-facing RU: no em/en dash.
	if strings.ContainsRune(out, '—') || strings.ContainsRune(out, '–') {
		t.Errorf("warnings diagnostics contains em/en-dash, violates no-тире rule:\n%s", out)
	}
}

// ---- containing ----

func TestComputeContaining_FullName(t *testing.T) {
	out := computeContaining(sampleForest(), "Справочник.Контрагенты", "")
	mustContain(t, out,
		"# Подсистемы, содержащие Справочник.Контрагенты (2)",
		"## Справочник.Контрагенты",
		"Продажи",
		"Розница",
		"корень: Продажи",
	)
}

func TestComputeContaining_ShortName(t *testing.T) {
	out := computeContaining(sampleForest(), "Контрагенты", "")
	// Short name resolves to Справочник.Контрагенты (noise ...ПрисоединенныеФайлы
	// has a different short segment and must not match).
	mustContain(t, out, "## Справочник.Контрагенты", "Продажи", "Розница")
	mustNotContain(t, out, "ПрисоединенныеФайлы")
}

func TestComputeContaining_AmbiguousShortName(t *testing.T) {
	forest := onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{Name: "Деньги", FullName: "Подсистема.Деньги", Content: []string{"Справочник.Валюты"}},
			{Name: "Курсы", FullName: "Подсистема.Курсы", Content: []string{"РегистрСведений.Валюты"}},
		},
	}
	out := computeContaining(forest, "Валюты", "")
	// Ambiguous short name lists both matches.
	mustContain(t, out, "## Справочник.Валюты", "## РегистрСведений.Валюты")
}

func TestComputeContaining_ObjectTypeDisambiguation(t *testing.T) {
	forest := onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{Name: "Деньги", FullName: "Подсистема.Деньги", Content: []string{"Справочник.Валюты"}},
			{Name: "Курсы", FullName: "Подсистема.Курсы", Content: []string{"РегистрСведений.Валюты"}},
		},
	}
	// Russian kind prefix.
	ru := computeContaining(forest, "Валюты", "Справочник")
	mustContain(t, ru, "## Справочник.Валюты")
	mustNotContain(t, ru, "## РегистрСведений.Валюты")

	// English equivalent maps to the same Russian prefix.
	en := computeContaining(forest, "Валюты", "Catalog")
	mustContain(t, en, "## Справочник.Валюты")
	mustNotContain(t, en, "## РегистрСведений.Валюты")

	// Unknown object_type must not wipe results (safe fallback keeps all matches).
	fallback := computeContaining(forest, "Валюты", "НетТакогоВида")
	mustContain(t, fallback, "## Справочник.Валюты", "## РегистрСведений.Валюты")
}

// TestComputeContaining_CaseInsensitive (bug #4) proves a short-name query in a
// different case still resolves the real member. Existing tests only use the
// canonical case; this one asserts case-folding on both sides of the compare.
func TestComputeContaining_CaseInsensitive(t *testing.T) {
	out := computeContaining(sampleForest(), "контрагенты", "")
	// Case-folded short name must still find Справочник.Контрагенты; output keeps
	// the original (canonical) case.
	mustContain(t, out, "## Справочник.Контрагенты")
	mustNotContain(t, out, "не найден")
}

// TestComputeContaining_FullNameCaseInsensitive (bug #4) proves a full-name
// query in a different case resolves the same member, output in canonical case.
func TestComputeContaining_FullNameCaseInsensitive(t *testing.T) {
	out := computeContaining(sampleForest(), "справочник.контрагенты", "")
	mustContain(t, out, "## Справочник.Контрагенты")
	mustNotContain(t, out, "не найден")
}

// TestComputeContaining_ObjectTypeCaseFold (bug #5) proves the English kind hint
// resolves regardless of case: "catalog" must narrow exactly like "Catalog".
func TestComputeContaining_ObjectTypeCaseFold(t *testing.T) {
	forest := onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{Name: "Деньги", FullName: "Подсистема.Деньги", Content: []string{"Справочник.Валюты"}},
			{Name: "Курсы", FullName: "Подсистема.Курсы", Content: []string{"РегистрСведений.Валюты"}},
		},
	}
	lower := computeContaining(forest, "Валюты", "catalog")
	mustContain(t, lower, "## Справочник.Валюты")
	mustNotContain(t, lower, "## РегистрСведений.Валюты")

	// Parity: a lowercase kind hint must narrow identically to the canonical case.
	canon := computeContaining(forest, "Валюты", "Catalog")
	if lower != canon {
		t.Errorf("case-folded object_type must narrow identically:\n--- catalog ---\n%s\n--- Catalog ---\n%s", lower, canon)
	}
}

func TestComputeContaining_ZeroMatches(t *testing.T) {
	out := computeContaining(sampleForest(), "НесуществующийОбъект", "")
	mustContain(t, out, "# Подсистемы, содержащие НесуществующийОбъект (0)", "не найден")
	mustNotContain(t, out, "## ")
}

func TestComputeContaining_FullNameNotInAnySubsystem(t *testing.T) {
	// A valid applied object that is not a member of any subsystem => zero.
	out := computeContaining(sampleForest(), "Справочник.Валюты", "")
	mustContain(t, out, "# Подсистемы, содержащие Справочник.Валюты (0)", "не найден")
}

// ---- intersections ----

func TestComputeIntersections(t *testing.T) {
	out := computeIntersections(sampleForest(), false)
	// Two objects belong to >= 2 subsystems: Контрагенты (same root) and
	// РеализацияТоваровУслуг (cross-branch).
	mustContain(t, out,
		"# Объекты в нескольких подсистемах (2)",
		"## Справочник.Контрагенты",
		"## Документ.РеализацияТоваровУслуг",
		"корень: Продажи",
		"корень: Финансы",
	)
	// Single-membership objects are excluded.
	mustNotContain(t, out, "## Справочник.Номенклатура", "## Справочник.Склады")
}

func TestComputeIntersections_CrossBranchOnly(t *testing.T) {
	out := computeIntersections(sampleForest(), true)
	// Only the object spanning two distinct roots survives.
	mustContain(t, out,
		"# Объекты в нескольких подсистемах (1)",
		"## Документ.РеализацияТоваровУслуг",
		"корень: Продажи",
		"корень: Финансы",
	)
	// The same-root intersection (Контрагенты, both under Продажи) is excluded.
	mustNotContain(t, out, "## Справочник.Контрагенты")
}

func TestComputeIntersections_RootAttribution(t *testing.T) {
	// Контрагенты sits in Продажи and Продажи>Розница: both roots are Продажи,
	// so under cross_branch_only it must be excluded (proves root attribution
	// follows the top-level ancestor, not the immediate parent).
	out := computeIntersections(sampleForest(), true)
	mustNotContain(t, out, "## Справочник.Контрагенты")

	// Under the normal view it is present with the single root Продажи.
	normal := computeIntersections(sampleForest(), false)
	mustContain(t, normal, "## Справочник.Контрагенты", "корень: Продажи")
}

// TestComputeIntersections_OmitsNoise (bug #7) proves an auto-generated noise
// object appearing in >=2 subsystems is NOT reported by intersections, mirroring
// the noise filtering computeOrphans already applies.
func TestComputeIntersections_OmitsNoise(t *testing.T) {
	noise := "Справочник.КонтрагентыПрисоединенныеФайлы"
	forest := onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{Name: "Продажи", FullName: "Подсистема.Продажи", Content: []string{noise}},
			{Name: "Закупки", FullName: "Подсистема.Закупки", Content: []string{noise}},
		},
	}
	out := computeIntersections(forest, false)
	mustNotContain(t, out, noise, "ПрисоединенныеФайлы")
	mustContain(t, out, "# Объекты в нескольких подсистемах (0)")
}

func TestComputeIntersections_None(t *testing.T) {
	forest := onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{Name: "Продажи", FullName: "Подсистема.Продажи", Content: []string{"Справочник.Контрагенты"}},
			{Name: "Закупки", FullName: "Подсистема.Закупки", Content: []string{"Документ.Поступление"}},
		},
	}
	out := computeIntersections(forest, false)
	mustContain(t, out, "# Объекты в нескольких подсистемах (0)", "более чем в одну")
}

// ---- tool + schema ----

func TestAnalyzeSubsystemsTool_Schema(t *testing.T) {
	tool := AnalyzeSubsystemsTool()
	if tool.Name != "analyze_subsystems" {
		t.Errorf("Name = %q, want analyze_subsystems", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}
	rawSchema, ok := tool.InputSchema.(json.RawMessage)
	if !ok {
		t.Fatalf("InputSchema type = %T, want json.RawMessage", tool.InputSchema)
	}
	// House convention: action is free-text validated in the handler, so the
	// schema must carry no JSON enum.
	if strings.Contains(string(rawSchema), "enum") {
		t.Errorf("schema must not contain an enum, got: %s", rawSchema)
	}
	// action must be required.
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		t.Fatalf("schema unmarshal: %v", err)
	}
	foundAction := false
	for _, r := range schema.Required {
		if r == "action" {
			foundAction = true
		}
	}
	if !foundAction {
		t.Errorf("expected action in required, got %v", schema.Required)
	}
	// No em/en dashes in the customer-facing description.
	if strings.ContainsAny(tool.Description, "—–") {
		t.Errorf("description must not contain em/en dashes: %s", tool.Description)
	}
}

// ---- handler ----

func newSubsystemsMock(t *testing.T) (*onec.Client, func()) {
	t.Helper()
	forest := sampleForest()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subsystems" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(forest)
	}))
	client := onec.NewClient(srv.URL, "", "")
	return client, srv.Close
}

func callAnalyze(t *testing.T, client *onec.Client, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	handler := NewAnalyzeSubsystemsHandler(client)
	return handler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "analyze_subsystems", Arguments: raw},
	})
}

func TestAnalyzeSubsystemsHandler_Orphans(t *testing.T) {
	client, closeFn := newSubsystemsMock(t)
	defer closeFn()

	result, err := callAnalyze(t, client, map[string]any{"action": "orphans"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := result.Content[0].(*mcp.TextContent)
	mustContain(t, tc.Text, "# Объекты вне подсистем", "Справочник.Валюты")
	mustNotContain(t, tc.Text, "КонтрагентыПрисоединенныеФайлы")
}

// TDD (broadened universe, LIVE HTTP path): the live extension now returns service-kind
// objects in allObjects, and orphans must report the out-of-subsystem ones exactly like
// applied objects. Proven end to end through the real handler -> onec.Client -> HTTP, so
// this exercises the same path production uses (not just computeOrphans in isolation).
func TestAnalyzeSubsystemsHandler_OrphansIncludesServiceKinds_LivePath(t *testing.T) {
	forest := onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{Name: "Продажи", FullName: "Подсистема.Продажи",
				Content: []string{"Справочник.Контрагенты", "ОбщийМодуль.УправлениеПечатью"}},
		},
		AllObjects: []string{
			"Справочник.Контрагенты",          // member (applied)
			"ОбщийМодуль.УправлениеПечатью",   // member (service kind) -> not an orphan
			"ОбщийМодуль.ОбщегоНазначения",    // orphan (service kind)
			"Роль.Аудитор",                    // orphan
			"Константа.ОсновнаяОрганизация",   // orphan
			"ОпределяемыйТип.ЗначениеДоступа", // orphan
			"HTTPСервис.MCPService",           // orphan
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subsystems" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(forest)
	}))
	defer srv.Close()
	client := onec.NewClient(srv.URL, "", "")

	result, err := callAnalyze(t, client, map[string]any{"action": "orphans"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := result.Content[0].(*mcp.TextContent)
	mustContain(t, tc.Text,
		"ОбщийМодуль.ОбщегоНазначения",
		"Роль.Аудитор",
		"Константа.ОсновнаяОрганизация",
		"ОпределяемыйТип.ЗначениеДоступа",
		"HTTPСервис.MCPService",
	)
	// The service kind that IS a member must not be reported as an orphan.
	mustNotContain(t, tc.Text, "ОбщийМодуль.УправлениеПечатью")
}

func TestAnalyzeSubsystemsHandler_Containing(t *testing.T) {
	client, closeFn := newSubsystemsMock(t)
	defer closeFn()

	result, err := callAnalyze(t, client, map[string]any{"action": "containing", "object": "РеализацияТоваровУслуг"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := result.Content[0].(*mcp.TextContent)
	mustContain(t, tc.Text, "## Документ.РеализацияТоваровУслуг", "Продажи", "Финансы")
}

func TestAnalyzeSubsystemsHandler_Intersections(t *testing.T) {
	client, closeFn := newSubsystemsMock(t)
	defer closeFn()

	result, err := callAnalyze(t, client, map[string]any{"action": "intersections", "cross_branch_only": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := result.Content[0].(*mcp.TextContent)
	mustContain(t, tc.Text, "# Объекты в нескольких подсистемах (1)", "## Документ.РеализацияТоваровУслуг")
	mustNotContain(t, tc.Text, "## Справочник.Контрагенты")
}

func TestAnalyzeSubsystemsHandler_UnknownAction(t *testing.T) {
	// Unknown action is rejected before any network call; a client pointed at an
	// unreachable address proves the validation short-circuits the fetch.
	client := onec.NewClient("http://127.0.0.1:0", "", "")
	_, err := callAnalyze(t, client, map[string]any{"action": "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' error, got: %v", err)
	}
}

func TestAnalyzeSubsystemsHandler_MissingAction(t *testing.T) {
	client := onec.NewClient("http://127.0.0.1:0", "", "")
	_, err := callAnalyze(t, client, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing action")
	}
	if !strings.Contains(err.Error(), "action is required") {
		t.Errorf("expected 'action is required' error, got: %v", err)
	}
}

func TestAnalyzeSubsystemsHandler_ContainingRequiresObject(t *testing.T) {
	client := onec.NewClient("http://127.0.0.1:0", "", "")
	_, err := callAnalyze(t, client, map[string]any{"action": "containing"})
	if err == nil {
		t.Fatal("expected error for containing without object")
	}
	if !strings.Contains(err.Error(), "object parameter") {
		t.Errorf("expected object-required error, got: %v", err)
	}
}
