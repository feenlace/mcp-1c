package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// waitReady blocks until idx.Ready() returns true or timeout expires.
func waitReady(t *testing.T, idx *dump.Index, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for !idx.Ready() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for index to become ready")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestSearchCodeTool(t *testing.T) {
	tool := SearchCodeTool()
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name != "search_code" {
		t.Errorf("expected tool name %q, got %q", "search_code", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}

	// Verify schema contains all expected properties.
	schemaBytes, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshaling input schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("parsing input schema: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in schema")
	}
	for _, field := range []string{"query", "limit", "namespace", "category", "module", "mode"} {
		if _, ok := props[field]; !ok {
			t.Errorf("missing property %q in schema", field)
		}
	}
}

func TestFormatSearchResult(t *testing.T) {
	matches := []dump.Match{
		{
			Module:  "Справочник.Контрагенты.МодульОбъекта",
			Line:    42,
			Context: "Процедура ПередЗаписью(Отказ)\n    // проверка заполнения\nКонецПроцедуры",
			Score:   0.847,
		},
		{
			Module:  "Документ.РеализацияТоваров.МодульОбъекта",
			Line:    15,
			Context: "Функция ПолучитьКонтрагента()\n    Возврат Контрагент;\nКонецФункции",
			Score:   0.512,
		},
	}

	text := FormatSearchResult(matches, 2, "Контрагент", dump.SearchModeSmart, nil)

	for _, want := range []string{
		"Результаты поиска",
		"Контрагент",
		"модулей с совпадениями: 2",
		"Справочник.Контрагенты.МодульОбъекта",
		"строка 42",
		"```bsl",
		"ПередЗаписью",
		"Документ.РеализацияТоваров.МодульОбъекта",
		"строка 15",
		"ПолучитьКонтрагента",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, text)
		}
	}
}

// TestFormatSearchResult_ExactMode.
//
// THE CONTROL THIS TEST USED TO CARRY IS GONE, and it was removed rather than
// weakened. It asserted that exact mode prints no «score:» and then proved the
// assertion non-vacuous by showing the SAME match printing one in smart mode. No
// mode prints a score any more — the label was removed because it made the row
// shape depend on which retrieval path filled Match.Score — so the old control
// asserts something false and could only have been made to pass by deleting it and
// leaving the first half standing on nothing.
//
// What replaces it is a control that still fires: the renderer produced a row, so
// the silence is the label being absent and not the answer being empty. The
// stronger statement, that the row is now byte-identical across all three modes,
// is TestTheRowShapeDoesNotDependOnTheMode in search_row_shape_test.go.
func TestFormatSearchResult_ExactMode(t *testing.T) {
	matches := []dump.Match{
		{
			Module:  "Модуль.Тест",
			Line:    1,
			Context: "Тест",
			// A NON-ZERO score, deliberately. With Score left at its zero value a
			// renderer that still gated on `m.Score > 0` would fall silent for that
			// reason alone and this test would pass without touching the mode.
			Score: 0.847,
		},
	}

	text := FormatSearchResult(matches, 1, "Тест", dump.SearchModeExact, nil)

	if strings.Contains(text, "score") {
		t.Errorf("exact mode still mentions a score, got:\n%s", text)
	}

	// CONTROL: a row really was rendered.
	if !strings.Contains(text, "### ") || !strings.Contains(text, "Модуль.Тест") {
		t.Fatalf("control failed: no row was rendered, so the absence above measures nothing:\n%s", text)
	}
}

// TestFormatSearchResult_LineNotLocated covers a match the search could not
// pin to a line (Line == 0). The rendered result must neither claim "строка 0"
// nor open a code block, because an empty or borrowed block reads as the matched
// source. Modules that do carry a line keep rendering as before.
func TestFormatSearchResult_LineNotLocated(t *testing.T) {
	matches := []dump.Match{
		{
			Module: "Справочник.Контрагенты.МодульОбъекта",
			Line:   0,
			Score:  0.731,
		},
		{
			Module:  "Документ.РеализацияТоваров.МодульОбъекта",
			Line:    15,
			Context: "Функция ПолучитьКонтрагента()\n    Возврат Контрагент;\nКонецФункции",
			Score:   0.512,
		},
	}

	text := FormatSearchResult(matches, 2, "ПолучитьКонтрагента()", dump.SearchModeSmart, nil)

	if strings.Contains(text, "строка 0") {
		t.Errorf("a match with no located line must not be rendered as 'строка 0', got:\n%s", text)
	}
	if !strings.Contains(text, "строка не определена") {
		t.Errorf("expected the unlocated match to say 'строка не определена', got:\n%s", text)
	}
	if !strings.Contains(text, "Справочник.Контрагенты.МодульОбъекта") {
		t.Errorf("the unlocated match must still be listed, got:\n%s", text)
	}

	// Exactly one code block: the one belonging to the match that has a line.
	if got := strings.Count(text, "```bsl"); got != 1 {
		t.Errorf("expected exactly 1 bsl block (only the located match), got %d:\n%s", got, text)
	}
	if !strings.Contains(text, "строка 15") || !strings.Contains(text, "ПолучитьКонтрагента") {
		t.Errorf("the located match must render unchanged, got:\n%s", text)
	}
}

func TestFormatSearchResult_Empty(t *testing.T) {
	text := FormatSearchResult(nil, 0, "НесуществующаяФункция", dump.SearchModeSmart, nil)

	if !strings.Contains(text, "Ничего не найдено") {
		t.Errorf("expected 'Ничего не найдено' in text, got:\n%s", text)
	}
	if !strings.Contains(text, "модулей с совпадениями: 0") {
		t.Errorf("expected 'модулей с совпадениями: 0' in text, got:\n%s", text)
	}
}

func TestFormatSearchResult_Truncated(t *testing.T) {
	matches := []dump.Match{
		{
			Module:  "Модуль.Тест",
			Line:    1,
			Context: "Тест",
		},
	}

	text := FormatSearchResult(matches, 150, "Тест", dump.SearchModeSmart, nil)

	if !strings.Contains(text, "Показано 1 из 150 модулей") {
		t.Errorf("expected truncation message, got:\n%s", text)
	}
	if !strings.Contains(text, "увеличьте limit") {
		t.Errorf("expected limit hint in text, got:\n%s", text)
	}
}

func TestNewSearchCodeHandler(t *testing.T) {
	dir := t.TempDir()
	mkBSL(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Строка1\nСтрока2\nПроцедура ОбновитьЦены()\n    // обновление цен\nКонецПроцедуры\n")

	index, err := dump.NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer index.Close()
	waitReady(t, index, 30*time.Second)

	handler := NewSearchCodeHandler(index)

	args, _ := json.Marshal(map[string]any{
		"query": "ОбновитьЦены",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "search_code",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if tc.Text == "" {
		t.Fatal("expected non-empty text")
	}

	for _, want := range []string{
		"Справочник.Номенклатура.МодульОбъекта",
		"ОбновитьЦены",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestNewSearchCodeHandler_WithFilters(t *testing.T) {
	dir := t.TempDir()
	mkBSL(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl",
		"Процедура ОбщаяЛогика()\nКонецПроцедуры\n")
	mkBSL(t, dir, "Documents/Тест/Ext/ObjectModule.bsl",
		"Процедура ОбщаяЛогика()\nКонецПроцедуры\n")

	index, err := dump.NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer index.Close()
	waitReady(t, index, 30*time.Second)

	handler := NewSearchCodeHandler(index)

	args, _ := json.Marshal(map[string]any{
		"query":    "ОбщаяЛогика",
		"category": "Справочник",
		"mode":     "exact",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "search_code",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	// The call above asked for mode exact, whose total counts LINES.
	if !strings.Contains(tc.Text, "строк с совпадениями: 1") {
		t.Errorf("expected 1 match with category filter, got:\n%s", tc.Text)
	}
	if !strings.Contains(tc.Text, "Справочник") {
		t.Errorf("expected Справочник in result, got:\n%s", tc.Text)
	}
}

func mkBSL(t *testing.T, base, relPath, content string) {
	t.Helper()
	full := filepath.Join(base, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
