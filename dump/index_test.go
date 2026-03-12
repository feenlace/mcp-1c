package dump

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

// mkBSLFile creates a .bsl file at the given relative path under base.
// Same as mkBSL in searcher_test.go, but with a different name to avoid
// collision during the transition period (both test files coexist until Task 6).
func mkBSLFile(t *testing.T, base, relPath, content string) {
	t.Helper()
	full := filepath.Join(base, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewIndex(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\n\t// проверка\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Documents/Реализация/Ext/ObjectModule.bsl",
		"Процедура ОбработкаПроведения(Отказ)\n\t// проведение\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	if idx.ModuleCount() != 2 {
		t.Errorf("expected 2 modules, got %d", idx.ModuleCount())
	}
	if idx.Dir() != dir {
		t.Errorf("expected dir %q, got %q", dir, idx.Dir())
	}
}

func TestIndex_SearchSmart(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Строка1\nПроцедура ОбновитьЦены()\n\t// обновление цен\nКонецПроцедуры\nСтрока5\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	matches, total, err := idx.Search(SearchParams{
		Query: "ОбновитьЦены",
		Mode:  SearchModeSmart,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if total == 0 {
		t.Fatal("expected at least 1 match")
	}
	if len(matches) == 0 {
		t.Fatal("expected at least 1 match result")
	}
	if !strings.Contains(matches[0].Module, "Справочник.Номенклатура") {
		t.Errorf("expected module containing 'Справочник.Номенклатура', got %q", matches[0].Module)
	}
	if matches[0].Score <= 0 {
		t.Errorf("expected positive score in smart mode, got %f", matches[0].Score)
	}
}

func TestIndex_SearchSmartSynonym(t *testing.T) {
	dir := t.TempDir()
	// Module content uses Russian function name.
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl",
		"Результат = СтрНайти(Строка, Подстрока);\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	// Search using English function name — should find via synonym.
	matches, total, err := idx.Search(SearchParams{
		Query: "StrFind",
		Mode:  SearchModeSmart,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if total == 0 {
		t.Error("expected synonym search to find match for 'StrFind' -> 'СтрНайти'")
	}
	if len(matches) == 0 {
		t.Fatal("expected at least 1 match result")
	}
}

func TestIndex_SearchRegex(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl",
		"Процедура Обработка1()\nКонецПроцедуры\nПроцедура Обработка2()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	matches, total, err := idx.Search(SearchParams{
		Query: `Обработка\d+`,
		Mode:  SearchModeRegex,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if total != 2 {
		t.Errorf("expected 2 regex matches, got %d", total)
	}
	if len(matches) != 2 {
		t.Errorf("expected 2 match results, got %d", len(matches))
	}
}

func TestIndex_SearchRegexInvalid(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl",
		"Процедура Тест()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	_, _, err = idx.Search(SearchParams{
		Query: "[invalid",
		Mode:  SearchModeRegex,
		Limit: 50,
	})
	if err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
}

func TestIndex_SearchExact(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Строка1\nПроцедура ОбновитьЦены()\n\t// обновление цен\nКонецПроцедуры\nСтрока5\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	matches, total, err := idx.Search(SearchParams{
		Query: "ОбновитьЦены",
		Mode:  SearchModeExact,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if total != 1 {
		t.Errorf("expected 1 exact match, got %d", total)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match result, got %d", len(matches))
	}
	if matches[0].Line != 2 {
		t.Errorf("expected line 2, got %d", matches[0].Line)
	}
	if !strings.Contains(matches[0].Module, "Справочник.Номенклатура.МодульОбъекта") {
		t.Errorf("expected module 'Справочник.Номенклатура.МодульОбъекта', got %q", matches[0].Module)
	}
}

func TestIndex_SearchCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl",
		"ПРОЦЕДУРА Тестирование()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	// Exact mode: case-insensitive by design.
	matches, total, err := idx.Search(SearchParams{
		Query: "процедура",
		Mode:  SearchModeExact,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 case-insensitive match, got %d", total)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
}

func TestIndex_SearchLimit(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl",
		"Строка1\nСтрока2\nСтрока3\nСтрока4\nСтрока5\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	matches, total, err := idx.Search(SearchParams{
		Query: "Строка",
		Mode:  SearchModeExact,
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 5 {
		t.Errorf("expected 5 total matches, got %d", total)
	}
	if len(matches) != 2 {
		t.Errorf("expected 2 matches (limited), got %d", len(matches))
	}
}

func TestIndex_SearchCategoryFilter(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ОбщаяЛогика()\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Documents/Реализация/Ext/ObjectModule.bsl",
		"Процедура ОбщаяЛогика()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	matches, total, err := idx.Search(SearchParams{
		Query:    "ОбщаяЛогика",
		Mode:     SearchModeExact,
		Category: "Справочник",
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 match (filtered by category), got %d", total)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match result, got %d", len(matches))
	}
	if !strings.Contains(matches[0].Module, "Справочник") {
		t.Errorf("expected Справочник module, got %q", matches[0].Module)
	}
}

func TestIndex_SearchModuleFilter(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl",
		"Процедура Общая()\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ManagerModule.bsl",
		"Процедура Общая()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	matches, _, err := idx.Search(SearchParams{
		Query:  "Общая",
		Mode:   SearchModeExact,
		Module: "МодульМенеджера",
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match (filtered by module type), got %d", len(matches))
	}
	if !strings.Contains(matches[0].Module, "МодульМенеджера") {
		t.Errorf("expected МодульМенеджера, got %q", matches[0].Module)
	}
}

func TestBslPathToModuleName_CommonModulesFix(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		// Existing tests (from searcher_test.go).
		{"Catalogs/Номенклатура/Ext/ObjectModule.bsl", "Справочник.Номенклатура.МодульОбъекта"},
		{"Documents/Реализация/Ext/ObjectModule.bsl", "Документ.Реализация.МодульОбъекта"},
		{"DataProcessors/Обработка1/Ext/ObjectModule.bsl", "Обработка.Обработка1.МодульОбъекта"},
		{"Documents/Док/Forms/ФормаДок/Ext/Module.bsl", "Документ.Док.Форма.ФормаДок.МодульФормы"},

		// BUG FIX: CommonModules should get "Модуль", not "МодульФормы".
		{"CommonModules/ОбщийМодуль1/Ext/Module.bsl", "ОбщийМодуль.ОбщийМодуль1.Модуль"},
	}

	for _, tt := range tests {
		got := bslPathToModuleName(tt.path)
		if got != tt.want {
			t.Errorf("bslPathToModuleName(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestIndex_Close(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "// empty\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}

	if err := idx.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestIndex_Reindex(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Test/Ext/ObjectModule.bsl", "Процедура Тест()\nКонецПроцедуры")

	// First build — creates cache.
	idx1, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex (first build): %v", err)
	}
	if idx1.ModuleCount() != 1 {
		t.Errorf("expected 1 module, got %d", idx1.ModuleCount())
	}
	idx1.Close()

	// Second open — uses cache.
	idx2, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex (cached): %v", err)
	}
	if idx2.ModuleCount() != 1 {
		t.Errorf("expected 1 module from cache, got %d", idx2.ModuleCount())
	}
	idx2.Close()

	// Reindex — rebuilds.
	idx3, err := NewIndex(dir, true)
	if err != nil {
		t.Fatalf("NewIndex (reindex): %v", err)
	}
	if idx3.ModuleCount() != 1 {
		t.Errorf("expected 1 module after reindex, got %d", idx3.ModuleCount())
	}
	idx3.Close()
}

func TestIndex_SearchDefaultMode(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl",
		"Процедура Тест()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	// Empty mode should default to smart.
	matches, _, err := idx.Search(SearchParams{
		Query: "Тест",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) == 0 {
		t.Error("expected at least 1 match with default (smart) mode")
	}
}

func TestIndex_Ready(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "// test\n")
	idx, err := NewIndex(dir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	if !idx.Ready() {
		t.Error("expected Ready() == true after blocking NewIndex")
	}
}

func TestIndex_SearchWhileBuilding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idx := &Index{
		dir:           t.TempDir(),
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	defer close(idx.done)
	_, _, err := idx.Search(SearchParams{Query: "test", Mode: SearchModeSmart, Limit: 50})
	if err == nil {
		t.Fatal("expected error when searching while index is building")
	}
	if !strings.Contains(err.Error(), "building") {
		t.Errorf("expected 'building' in error message, got: %v", err)
	}
}
