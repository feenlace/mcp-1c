package dump

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// waitReady blocks until idx.Ready() returns true or timeout expires.
func waitReady(t *testing.T, idx *Index, timeout time.Duration) {
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

func TestNewIndex(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\n\t// проверка\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Documents/Реализация/Ext/ObjectModule.bsl",
		"Процедура ОбработкаПроведения(Отказ)\n\t// проведение\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	waitReady(t, idx, 30*time.Second)

	if err := idx.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestIndex_Reindex(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Test/Ext/ObjectModule.bsl", "Процедура Тест()\nКонецПроцедуры")

	// First build — creates cache.
	idx1, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex (first build): %v", err)
	}
	waitReady(t, idx1, 30*time.Second)
	if idx1.ModuleCount() != 1 {
		t.Errorf("expected 1 module, got %d", idx1.ModuleCount())
	}
	idx1.Close()

	// Second open — uses cache.
	idx2, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex (cached): %v", err)
	}
	waitReady(t, idx2, 30*time.Second)
	if idx2.ModuleCount() != 1 {
		t.Errorf("expected 1 module from cache, got %d", idx2.ModuleCount())
	}
	idx2.Close()

	// Reindex — rebuilds.
	idx3, err := NewIndex(dir, "", true)
	if err != nil {
		t.Fatalf("NewIndex (reindex): %v", err)
	}
	waitReady(t, idx3, 30*time.Second)
	if idx3.ModuleCount() != 1 {
		t.Errorf("expected 1 module after reindex, got %d", idx3.ModuleCount())
	}
	idx3.Close()
}

func TestIndex_SearchDefaultMode(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl",
		"Процедура Тест()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

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
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)
	if !idx.Ready() {
		t.Error("expected Ready() == true after build completes")
	}
}

func TestIndex_SearchWhileBuilding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idx := &Index{
		dir:           t.TempDir(),
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]string),
		pathByName:    make(map[string]string),
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

func TestIndex_NonBlockingBuild(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "Процедура Тест()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	waitReady(t, idx, 30*time.Second)

	if idx.ModuleCount() != 1 {
		t.Errorf("expected 1 module, got %d", idx.ModuleCount())
	}

	matches, total, err := idx.Search(SearchParams{Query: "Тест", Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total == 0 || len(matches) == 0 {
		t.Error("expected at least 1 match")
	}
}

func TestIndex_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)
	if idx.ModuleCount() != 0 {
		t.Errorf("expected 0 modules, got %d", idx.ModuleCount())
	}
}

func TestIndex_CloseWhileBuilding(t *testing.T) {
	dir := t.TempDir()
	for i := range 50 {
		mkBSLFile(t, dir, fmt.Sprintf("Catalogs/Test%d/Ext/ObjectModule.bsl", i),
			fmt.Sprintf("Процедура Тест%d()\nКонецПроцедуры\n", i))
	}
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	err = idx.Close()
	if err != nil {
		t.Logf("Close returned error (acceptable): %v", err)
	}
	select {
	case <-idx.done:
	case <-time.After(10 * time.Second):
		t.Fatal("build goroutine did not exit after Close()")
	}
}

func TestIndex_IndexDoc(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "Процедура Тест()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	err = idx.IndexDoc("Справочник.Новый.МодульОбъекта", "Функция НоваяФункция()\n\tВозврат 1;\nКонецФункции\n")
	if err != nil {
		t.Fatalf("IndexDoc: %v", err)
	}

	matches, total, err := idx.Search(SearchParams{Query: "НоваяФункция", Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total == 0 || len(matches) == 0 {
		t.Error("expected to find runtime-indexed document")
	}

	idx.mu.RLock()
	_, ok := idx.contentByName["Справочник.Новый.МодульОбъекта"]
	idx.mu.RUnlock()
	if !ok {
		t.Error("expected contentByName to contain the new document")
	}

	if idx.ModuleCount() != 2 {
		t.Errorf("expected ModuleCount to be 2, got %d", idx.ModuleCount())
	}
}

func TestIndex_DeleteDoc(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "Процедура Удаляемая()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	docID := "Справочник.Тест.МодульОбъекта"

	matches, _, err := idx.Search(SearchParams{Query: "Удаляемая", Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatalf("Search before delete: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected to find document before delete")
	}

	err = idx.DeleteDoc(docID)
	if err != nil {
		t.Fatalf("DeleteDoc: %v", err)
	}

	matches, _, err = idx.Search(SearchParams{Query: "Удаляемая", Mode: SearchModeExact, Limit: 50})
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches after delete, got %d", len(matches))
	}

	idx.mu.RLock()
	_, ok := idx.contentByName[docID]
	idx.mu.RUnlock()
	if ok {
		t.Error("expected contentByName to NOT contain deleted document")
	}
}

func TestIndex_IndexDoc_NotReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idx := &Index{
		dir:           t.TempDir(),
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]string),
		pathByName:    make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	defer close(idx.done)

	err := idx.IndexDoc("test", "content")
	if err == nil {
		t.Fatal("expected error when IndexDoc on not-ready index")
	}
}

func TestIndex_DeleteDoc_NotReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idx := &Index{
		dir:           t.TempDir(),
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]string),
		pathByName:    make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	defer close(idx.done)

	err := idx.DeleteDoc("test")
	if err == nil {
		t.Fatal("expected error when DeleteDoc on not-ready index")
	}
}

func TestIndex_IndexDoc_RegexVisible(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "Процедура Тест()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	err = idx.IndexDoc("Документ.Новый.МодульОбъекта", "Функция УникальнаяФункцияRT()\n\tВозврат 42;\nКонецФункции\n")
	if err != nil {
		t.Fatalf("IndexDoc: %v", err)
	}

	matches, total, err := idx.Search(SearchParams{Query: `УникальнаяФункцияRT`, Mode: SearchModeRegex, Limit: 50})
	if err != nil {
		t.Fatalf("Search regex: %v", err)
	}
	if total == 0 || len(matches) == 0 {
		t.Error("expected regex search to find runtime-indexed document")
	}

	matches, total, err = idx.Search(SearchParams{Query: "УникальнаяФункцияRT", Mode: SearchModeExact, Limit: 50})
	if err != nil {
		t.Fatalf("Search exact: %v", err)
	}
	if total == 0 || len(matches) == 0 {
		t.Error("expected exact search to find runtime-indexed document")
	}
}

func TestIndex_IndexDoc_Dedup(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "Процедура Тест() КонецПроцедуры")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	id := "Справочник.Тест.МодульОбъекта"
	if err := idx.IndexDoc(id, "Обновлённый код"); err != nil {
		t.Fatal(err)
	}
	if err := idx.IndexDoc(id, "Ещё раз обновлённый"); err != nil {
		t.Fatal(err)
	}

	if idx.ModuleCount() != 1 {
		t.Fatalf("expected ModuleCount 1 after duplicate IndexDoc, got %d", idx.ModuleCount())
	}
}

func TestIndex_GetContent(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\n\t// проверка\nКонецПроцедуры\n")
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	content, ok := idx.GetContent("Справочник.Номенклатура.МодульОбъекта")
	if !ok {
		t.Fatal("expected GetContent to return ok=true for existing module")
	}
	if !strings.Contains(content, "ПередЗаписью") {
		t.Errorf("expected content to contain 'ПередЗаписью', got %q", content)
	}
}

func TestIndex_GetContent_NotReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idx := &Index{
		dir:           t.TempDir(),
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]string),
		pathByName:    make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	defer close(idx.done)
	_, ok := idx.GetContent("anything")
	if ok {
		t.Error("expected GetContent to return ok=false when index is not ready")
	}
}

func TestIndex_GetContent_NotFound(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "// test\n")
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)
	_, ok := idx.GetContent("Несуществующий.Модуль.МодульОбъекта")
	if ok {
		t.Error("expected GetContent to return ok=false for non-existent module")
	}
}

func TestIndex_IndexDocWithMeta(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "Процедура Тест()\nКонецПроцедуры\n")
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	err = idx.IndexDocWithMeta(
		"ext.МоёРасширение.Документы.ЗаказКлиента.МодульОбъекта",
		"Функция ОбработатьЗаказMeta()\n\tВозврат 1;\nКонецФункции\n",
		"Расширение", "МоёРасширение",
	)
	if err != nil {
		t.Fatalf("IndexDocWithMeta: %v", err)
	}

	if idx.ModuleCount() != 2 {
		t.Errorf("expected ModuleCount 2, got %d", idx.ModuleCount())
	}

	matches, total, err := idx.Search(SearchParams{Query: "ОбработатьЗаказMeta", Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatalf("Search smart: %v", err)
	}
	if total == 0 || len(matches) == 0 {
		t.Error("expected smart search to find IndexDocWithMeta document")
	}

	matches, total, err = idx.Search(SearchParams{
		Query: "ОбработатьЗаказMeta", Mode: SearchModeExact, Category: "Расширение", Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search with category filter: %v", err)
	}
	if total == 0 || len(matches) == 0 {
		t.Error("expected category-filtered search to find IndexDocWithMeta document")
	}

	content, ok := idx.GetContent("ext.МоёРасширение.Документы.ЗаказКлиента.МодульОбъекта")
	if !ok {
		t.Fatal("expected GetContent to return ok=true")
	}
	if !strings.Contains(content, "ОбработатьЗаказMeta") {
		t.Errorf("expected content to contain 'ОбработатьЗаказMeta', got %q", content)
	}
}

func TestIndex_IndexDocWithMeta_NotReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idx := &Index{
		dir:           t.TempDir(),
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]string),
		pathByName:    make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	defer close(idx.done)
	err := idx.IndexDocWithMeta("id", "content", "cat", "mod")
	if err == nil {
		t.Fatal("expected error when IndexDocWithMeta on not-ready index")
	}
}

func TestIndex_IndexDocWithMeta_Dedup(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "// test\n")
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	docID := "ext.Test.Cat.Obj.Mod"
	_ = idx.IndexDocWithMeta(docID, "v1", "Cat", "Mod")
	_ = idx.IndexDocWithMeta(docID, "v2", "Cat", "Mod")

	if idx.ModuleCount() != 2 {
		t.Errorf("expected ModuleCount 2 (1 file + 1 runtime), got %d", idx.ModuleCount())
	}
	content, ok := idx.GetContent(docID)
	if !ok {
		t.Fatal("expected GetContent ok=true")
	}
	if content != "v2" {
		t.Errorf("expected 'v2', got %q", content)
	}
}

func TestIndex_Done(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "// test\n")
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	select {
	case <-idx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for Done()")
	}
	if !idx.Ready() {
		t.Error("expected Ready() == true after Done()")
	}
}

func TestIndex_Done_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	select {
	case <-idx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for Done() on empty dir")
	}
}

func TestIndex_DeleteDoc_RemovesFromNames(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "Процедура Удаляемая()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	docID := "Справочник.Тест.МодульОбъекта"
	if idx.ModuleCount() != 1 {
		t.Fatalf("expected 1 module before delete, got %d", idx.ModuleCount())
	}

	err = idx.DeleteDoc(docID)
	if err != nil {
		t.Fatalf("DeleteDoc: %v", err)
	}

	if idx.ModuleCount() != 0 {
		t.Errorf("expected ModuleCount 0 after delete, got %d", idx.ModuleCount())
	}

	matches, total, err := idx.Search(SearchParams{Query: "Удаляемая", Mode: SearchModeExact, Limit: 50})
	if err != nil {
		t.Fatalf("Search exact after delete: %v", err)
	}
	if total != 0 || len(matches) != 0 {
		t.Errorf("expected 0 matches after delete, got total=%d matches=%d", total, len(matches))
	}

	matches, total, err = idx.Search(SearchParams{Query: "Удаляемая", Mode: SearchModeRegex, Limit: 50})
	if err != nil {
		t.Fatalf("Search regex after delete: %v", err)
	}
	if total != 0 || len(matches) != 0 {
		t.Errorf("expected 0 regex matches after delete, got total=%d matches=%d", total, len(matches))
	}
}

// TestSetShowProgress_NonTTY_StderrEmpty проверяет, что при SetShowProgress(false)
// пакет dump не пишет ничего в stderr во время индексации.
// Регрессия на Issue #14 (Kilo Code 7.x интерпретирует любой stderr как ошибку).
func TestSetShowProgress_NonTTY_StderrEmpty(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origStderr
	})

	// Читаем stderr в отдельной горутине, чтобы pipe не заблокировал запись.
	readDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		readDone <- data
	}()

	SetShowProgress(false)

	dumpDir := t.TempDir()
	mkBSLFile(t, dumpDir, "CommonModules/TestMod/Ext/Module.bsl",
		"Процедура Тест() КонецПроцедуры")

	idx, err := NewIndex(dumpDir, filepath.Join(dumpDir, ".cache"), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	waitReady(t, idx, 30*time.Second)
	idx.Close()

	// Закрываем writer, чтобы горутина чтения завершилась.
	w.Close()

	var collected []byte
	select {
	case collected = <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading stderr pipe")
	}

	if len(collected) > 0 {
		t.Errorf("expected empty stderr with SetShowProgress(false), got %d bytes: %q",
			len(collected), collected)
	}
}

// TestSetShowProgress_TTY_StderrHasProgress проверяет, что при SetShowProgress(true)
// пакет dump выводит в stderr информационные сообщения об индексации.
// Регрессия: не должно быть "немого" режима в интерактивном терминале.
func TestSetShowProgress_TTY_StderrHasProgress(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origStderr
	})

	readDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		readDone <- data
	}()

	SetShowProgress(true)
	t.Cleanup(func() { SetShowProgress(false) }) // сбрасываем флаг для других тестов

	dumpDir := t.TempDir()
	mkBSLFile(t, dumpDir, "CommonModules/TestMod/Ext/Module.bsl",
		"Процедура Тест() КонецПроцедуры")

	idx, err := NewIndex(dumpDir, filepath.Join(dumpDir, ".cache"), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	waitReady(t, idx, 30*time.Second)
	idx.Close()

	w.Close()

	var collected []byte
	select {
	case collected = <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading stderr pipe")
	}

	if len(collected) == 0 {
		t.Errorf("expected non-empty stderr with SetShowProgress(true), got empty")
	}
	// Проверяем наличие осмысленного вывода: стартовая строка "Индексация" или
	// финальная "Индексация завершена" всегда пишутся при tickerActive=true.
	if !bytes.Contains(collected, []byte("Индекс")) && !bytes.Contains(collected, []byte("модул")) {
		t.Logf("stderr content: %q (may be fine if build was faster than first tick)", collected)
	}
}
