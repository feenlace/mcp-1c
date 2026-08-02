package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

	result := formatMetadataTree(tree, nil, "")

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

	result := formatMetadataTree(tree, nil, "")

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

	result := formatMetadataTree(tree, nil, "")

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

	result := formatMetadataSummary(tree, nil)

	if !strings.Contains(result, "Определяемые типы") {
		t.Errorf("expected 'Определяемые типы' in summary, got:\n%s", result)
	}
	if !strings.Contains(result, `filter="ОпределяемыеТипы"`) {
		t.Errorf("expected filter key ОпределяемыеТипы in summary, got:\n%s", result)
	}
}

// TestMetadataHandler_WarningsSurfaced proves a degraded /metadata response is
// reported as degraded: the extension records every collection it could not read in
// the "warnings" key, and both views must render that as a diagnostics line instead
// of returning a short tree that looks complete. Before this channel existed the
// extension swallowed the failure with a bare Продолжить, which is exactly why a
// wrong collection literal stayed invisible.
func TestMetadataHandler_WarningsSurfaced(t *testing.T) {
	const body = `{
		"Справочники":["Контрагенты"],
		"warnings":["WebСервисы: Поле объекта не обнаружено","РегламентныеЗадания: Поле объекта не обнаружено"]
	}`

	for _, tc := range []struct {
		name string
		args string
	}{
		{"summary", ``},
		{"filtered", `{"filter":"Справочники"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(body)) //nolint:errcheck
			}))
			defer mockServer.Close()

			params := &mcp.CallToolParamsRaw{}
			if tc.args != "" {
				params.Arguments = json.RawMessage(tc.args)
			}
			result, err := NewMetadataHandler(onec.NewClient(mockServer.URL, "", ""))(context.Background(),
				&mcp.CallToolRequest{Params: params})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			text := result.Content[0].(*mcp.TextContent).Text

			if !strings.Contains(text, "Диагностика") {
				t.Errorf("a degraded metadata tree must be announced, got:\n%s", text)
			}
			for _, want := range []string{"WebСервисы", "РегламентныеЗадания", "пропущено коллекций: 2"} {
				if !strings.Contains(text, want) {
					t.Errorf("diagnostics must contain %q, got:\n%s", want, text)
				}
			}
			// The diagnostics channel must never be mistaken for a category.
			if strings.Contains(text, `filter="warnings"`) || strings.Contains(text, "## warnings") {
				t.Errorf("the warnings key leaked into the category list:\n%s", text)
			}
			// Scoped to the diagnostics line this test introduces. The surrounding
			// summary lines carry a pre-existing U+2014 in their "filter=" separator,
			// which is out of this change's scope.
			for _, line := range strings.Split(text, "\n") {
				if !strings.HasPrefix(line, "> Диагностика") {
					continue
				}
				if strings.ContainsAny(line, "—–―−") {
					t.Errorf("the diagnostics line contains a dash, violates the no-тире rule:\n%s", line)
				}
			}
		})
	}
}

// TestMetadataHandler_NoWarningsIsByteIdentical proves the diagnostics channel is
// strictly additive: a healthy response renders exactly as it did before the channel
// existed, so the honest path costs nothing.
func TestMetadataHandler_NoWarningsIsByteIdentical(t *testing.T) {
	tree := map[string][]string{"Справочники": {"Контрагенты"}}

	if got, want := formatMetadataSummary(tree, nil), formatMetadataSummary(tree, []string{}); got != want {
		t.Errorf("empty warnings must render identically to nil:\n%q\nvs\n%q", got, want)
	}
	if strings.Contains(formatMetadataTree(tree, nil, ""), "Диагностика") {
		t.Error("a clean tree must not emit a diagnostics line")
	}
}

// TestMetadataCategoryKeysAreRealMetadataProperties pins the display table against the
// same ground truth the BSL literals are pinned against (dump/testdata). A key here is
// a JSON key of the /metadata response, i.e. a name the extension passed to
// Метаданные[...]; if it is not a real property of ОбъектМетаданныхКонфигурация then no
// response can ever carry it, the category never renders under its human title, and the
// tool silently loses a whole section. That is the user-visible half of the wrong
// literal defect, and byte-comparing our own copies cannot see it.
func TestMetadataCategoryKeysAreRealMetadataProperties(t *testing.T) {
	const snapshotPath = "../dump/testdata/config_metadata_properties.txt"

	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("reading %s: %v", snapshotPath, err)
	}
	valid := map[string]bool{}
	for _, name := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		valid[name] = true
	}
	if len(valid) < 100 {
		t.Fatalf("%s yielded only %d names; the snapshot is truncated and this guard would be vacuous",
			snapshotPath, len(valid))
	}

	for _, cat := range metadataCategories {
		if valid[cat.key] {
			continue
		}
		t.Errorf("metadataCategories key %q (title %q) is NOT a property of ОбъектМетаданныхКонфигурация "+
			"in %s.\nEither the key is wrong, in which case /metadata never returns it and the category is "+
			"lost, or the snapshot is stale, see dump/testdata/config_metadata_properties.origin.txt.",
			cat.key, cat.title, snapshotPath)
	}
}
