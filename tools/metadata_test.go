package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMetadataHandler(t *testing.T) {
	// Start a mock 1C server.
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metadata" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"Справочники":["Контрагенты","Номенклатура"],
			"Документы":["РеализацияТоваровУслуг"],
			"Перечисления":["ВидыКонтрагентов"],
			"Обработки":["ЗагрузкаДанных"],
			"Отчеты":["ОстаткиТоваров"],
			"РегистрыСведений":["КурсыВалют"],
			"РегистрыНакопления":["ОстаткиТоваров"],
			"РегистрыБухгалтерии":["Хозрасчетный"],
			"ПланыСчетов":["Хозрасчетный"],
			"Роли":["Администратор","Бухгалтер"],
			"ОбщиеМодули":["ОбщегоНазначения"],
			"Подсистемы":["Продажи"]
		}`))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewMetadataHandler(client)

	result, err := handler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{},
	})
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

	// Without filter the handler returns a summary with category names and counts.
	for _, want := range []string{
		"Справочники", "Документы", "Перечисления",
		"Обработки", "Отчёты", "Регистры сведений",
		"Регистры накопления", "Регистры бухгалтерии",
		"Планы счетов", "Роли", "Общие модули",
		"Подсистемы",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected summary to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestMetadataTool(t *testing.T) {
	tool := MetadataTool()
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name != "get_metadata_tree" {
		t.Errorf("expected tool name %q, got %q", "get_metadata_tree", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestFormatMetadataTree_UnknownCategory(t *testing.T) {
	tree := map[string][]string{
		"Справочники": {"Контрагенты"},
		"НовыйТип":    {"ОбъектНовогоТипа", "ЕщеОдинОбъект"},
	}

	result := formatMetadataTree(tree)

	// Known category should be rendered.
	if !strings.Contains(result, "## Справочники") {
		t.Errorf("expected known category 'Справочники', got:\n%s", result)
	}
	if !strings.Contains(result, "Контрагенты") {
		t.Errorf("expected 'Контрагенты' in output, got:\n%s", result)
	}

	// Unknown category should also be rendered.
	if !strings.Contains(result, "## НовыйТип") {
		t.Errorf("expected unknown category 'НовыйТип' to be rendered, got:\n%s", result)
	}
	if !strings.Contains(result, "ОбъектНовогоТипа") {
		t.Errorf("expected 'ОбъектНовогоТипа' in output, got:\n%s", result)
	}
	if !strings.Contains(result, "ЕщеОдинОбъект") {
		t.Errorf("expected 'ЕщеОдинОбъект' in output, got:\n%s", result)
	}
}

func TestFormatMetadataTree_Order(t *testing.T) {
	tree := map[string][]string{
		"НеизвестнаяКатегория": {"Объект1"},
		"Документы":            {"ПриходнаяНакладная"},
		"Справочники":          {"Контрагенты"},
		"Роли":                 {"Администратор"},
	}

	result := formatMetadataTree(tree)

	// Known categories must appear before unknown ones.
	idxSpravochniki := strings.Index(result, "## Справочники")
	idxDocuments := strings.Index(result, "## Документы")
	idxRoles := strings.Index(result, "## Роли")
	idxUnknown := strings.Index(result, "## НеизвестнаяКатегория")

	if idxSpravochniki < 0 || idxDocuments < 0 || idxRoles < 0 || idxUnknown < 0 {
		t.Fatalf("expected all sections to be present, got:\n%s", result)
	}

	// Справочники comes before Документы (defined order).
	if idxSpravochniki >= idxDocuments {
		t.Errorf("expected 'Справочники' before 'Документы', got:\n%s", result)
	}

	// Документы comes before Роли (defined order).
	if idxDocuments >= idxRoles {
		t.Errorf("expected 'Документы' before 'Роли', got:\n%s", result)
	}

	// All known categories come before unknown ones.
	if idxRoles >= idxUnknown {
		t.Errorf("expected known categories before unknown ones, got:\n%s", result)
	}
}

// TestFormatMetadataTree_DefinedTypes proves the ОпределяемыеТипы category renders
// under its explicit display title (issue #33 fold-in: surface DefinedTypes in
// the metadata tree instead of relying on the unknown-key fallback).
func TestFormatMetadataTree_DefinedTypes(t *testing.T) {
	tree := map[string][]string{
		"ОпределяемыеТипы": {"ЗначениеДоступа", "СуммаДокумента"},
	}

	result := formatMetadataTree(tree)

	if !strings.Contains(result, "## Определяемые типы") {
		t.Errorf("expected 'Определяемые типы' section title, got:\n%s", result)
	}
	for _, want := range []string{"ЗначениеДоступа", "СуммаДокумента"} {
		if !strings.Contains(result, want) {
			t.Errorf("expected %q in output, got:\n%s", want, result)
		}
	}
}

// TestFormatMetadataSummary_DefinedTypes proves the summary view also lists the
// ОпределяемыеТипы category with its filter key.
func TestFormatMetadataSummary_DefinedTypes(t *testing.T) {
	tree := map[string][]string{
		"ОпределяемыеТипы": {"ЗначениеДоступа"},
	}

	result := formatMetadataSummary(tree)

	if !strings.Contains(result, "Определяемые типы") {
		t.Errorf("expected 'Определяемые типы' in summary, got:\n%s", result)
	}
	if !strings.Contains(result, `filter="ОпределяемыеТипы"`) {
		t.Errorf("expected filter key ОпределяемыеТипы in summary, got:\n%s", result)
	}
}
