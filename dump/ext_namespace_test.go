package dump

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The "ext.<Имя>." namespace, seen from the side that CONSUMES a key.
//
// The deriver has produced that prefix since the Расширения layout existed, and
// not one consumer of a key knew about it. Both splitters take segment 0 as the
// category and segment 1 as the object name, so every extension module in every
// dump was filed under the literal category "ext" with the EXTENSION's name where
// an object's name belongs, and the two real segments in between were dropped on
// the floor. A caller filtering by «Справочник», which is the vocabulary
// tools/search.go documents for that filter, got nothing back for any extension.
//
// That is one defect with three copies, because the same eight-line switch is
// written out three times (parseModuleName, NewPathIndex and PathIndex.AddEntry).
// They are one function now.

// TestAnExtensionKeyFillsTheSameSlotsAsABaseKey is the whole claim: the same
// logical module answers the same filters whether it lives in the configuration
// or in an extension of it.
func TestAnExtensionKeyFillsTheSameSlotsAsABaseKey(t *testing.T) {
	tests := []struct {
		key                     string
		category, name, module_ string
	}{
		{"Справочник.Ном.МодульОбъекта", "Справочник", "Ном", "МодульОбъекта"},
		{"ext.МоёРасш.Справочник.Ном.МодульОбъекта", "Справочник", "Ном", "МодульОбъекта"},
		{"Документ.Заказ.Форма.Ф.МодульФормы", "Документ", "Заказ", "МодульФормы"},
		{"ext.МоёРасш.Документ.Заказ.Форма.Ф.МодульФормы", "Документ", "Заказ", "МодульФормы"},
		{"Конфигурация.МодульСеанса.Модуль", "Конфигурация", "МодульСеанса", "Модуль"},
		{"ext.МоёРасш.Конфигурация.МодульСеанса.Модуль", "Конфигурация", "МодульСеанса", "Модуль"},
		{"ОбщийМодуль.ОбщегоНазначения.Модуль", "ОбщийМодуль", "ОбщегоНазначения", "Модуль"},
		{"ext.МоёРасш.ОбщийМодуль.ОбщегоНазначения.Модуль", "ОбщийМодуль", "ОбщегоНазначения", "Модуль"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := parseModuleName(tt.key)
			if got.category != tt.category || got.name != tt.name || got.module != tt.module_ {
				t.Errorf("parseModuleName(%q) = {category:%q name:%q module:%q}, want {%q %q %q}",
					tt.key, got.category, got.name, got.module, tt.category, tt.name, tt.module_)
			}
			// The path index splits a docID independently. A disagreement makes the
			// same filter behave differently depending on whether it is built.
			pi := NewPathIndex([]string{tt.key})
			if ids := pi.FilterDocIDs(tt.category, tt.module_); !slices.Contains(ids, tt.key) {
				t.Errorf("NewPathIndex: FilterDocIDs(%q, %q) = %v, want it to contain %q",
					tt.category, tt.module_, ids, tt.key)
			}
			// And the runtime path, which builds entries one at a time.
			one := NewPathIndex(nil)
			one.AddEntry(tt.key)
			if ids := one.FilterDocIDs(tt.category, tt.module_); !slices.Contains(ids, tt.key) {
				t.Errorf("AddEntry: FilterDocIDs(%q, %q) = %v, want it to contain %q",
					tt.category, tt.module_, ids, tt.key)
			}
		})
	}
}

// TestTheNamespaceIsRecognisedByTheCategoryAfterIt, and by nothing weaker.
//
// "ext" alone cannot carry the decision. A dump root may hold a directory
// literally named «ext» (the customer whose tree started all of this had one), and
// an unknown top-level directory becomes the category slot verbatim, so a base
// configuration really can produce a five-segment key that begins with "ext". The
// discriminator is therefore the segment AFTER the extension name: it has to be a
// category this package emits, which is a value of dumpDirNames or the
// configuration-module prefix.
func TestTheNamespaceIsRecognisedByTheCategoryAfterIt(t *testing.T) {
	// A real base-configuration key produced by a directory named «ext». Derived
	// rather than written down, so it cannot drift away from the deriver.
	const wrapperPath = "ext/Ном/Forms/Ф/Ext/Form/Module.bsl"
	key := bslPathToModuleName(wrapperPath)
	if key != "ext.Ном.Форма.Ф.МодульФормы" {
		t.Fatalf("premise moved: bslPathToModuleName(%q) = %q", wrapperPath, key)
	}
	if n := len(strings.Split(key, ".")); n < 5 {
		t.Fatalf("premise moved: the key has %d segments, so it cannot reach the namespace branch", n)
	}
	got := parseModuleName(key)
	if got.category != "ext" || got.name != "Ном" {
		t.Errorf("parseModuleName(%q) = {category:%q name:%q}, want the raw directory as the "+
			"category: «Форма» is a form infix, not a metadata kind, so this key carries no namespace",
			key, got.category, got.name)
	}

	// And the positive side: the same shape WITH a real category after the name is
	// namespaced.
	if p := parseModuleName("ext.Ном.Справочник.Ф.МодульОбъекта"); p.category != "Справочник" {
		t.Errorf("category = %q, want Справочник", p.category)
	}
}

// TestEveryDumpDirNameIsACategoryTheSplitterKnows keeps the discriminator tied to
// the table it is derived from rather than to a copy of it. A kind added to
// metadata_types.go must not silently stop being recognised after an "ext." prefix.
func TestEveryDumpDirNameIsACategoryTheSplitterKnows(t *testing.T) {
	checked := 0
	for _, ru := range dumpDirNames {
		checked++
		key := "ext.Расш." + ru + ".Объект.МодульОбъекта"
		if got := parseModuleName(key).category; got != ru {
			t.Errorf("parseModuleName(%q).category = %q, want %q", key, got, ru)
		}
	}
	if checked == 0 {
		t.Fatal("dumpDirNames is empty, so the loop measured nothing")
	}
	if got := parseModuleName("ext.Расш." + configModulePrefix + ".МодульСеанса.Модуль").category; got != configModulePrefix {
		t.Errorf("the configuration-module prefix is not a recognised category: %q", got)
	}
	// Negative control: a plausible-looking segment that is NOT a category leaves
	// the key alone, so the loop above is not passing on a rule that accepts
	// anything.
	if got := parseModuleName("ext.Расш.Справочники.Объект.МодульОбъекта").category; got != "ext" {
		t.Errorf("category = %q for a non-category third segment, want the key left alone", got)
	}
}

// TestTheThreeSplittersAreOneFunction. They were three copies of one switch, and
// three copies drift: AddEntry already differed from Contains about NFC. A census
// over the package source keeps them merged.
func TestTheThreeSplittersAreOneFunction(t *testing.T) {
	for _, key := range []string{
		"ext.Расш.Справочник.Ном.МодульОбъекта",
		"Справочник.Ном.МодульОбъекта",
		"ext.Расш.Конфигурация.МодульСеанса.Модуль",
		"Двасегмента.Ключ",
		"Односегментный",
		"ext.Расш.Неизвестное.Ном.Что",
	} {
		p := parseModuleName(key)
		pi := NewPathIndex([]string{key})
		bulk := pi.entries[0]
		one := NewPathIndex(nil)
		one.AddEntry(key)
		inc := one.entries[0]
		if p.category != bulk.Category || p.name != bulk.ObjectName || p.module != bulk.ModuleType {
			t.Errorf("parseModuleName and NewPathIndex disagree about %q: %+v vs %+v", key, p, bulk)
		}
		if bulk != inc {
			t.Errorf("NewPathIndex and AddEntry disagree about %q: %+v vs %+v", key, bulk, inc)
		}
	}
}

// TestAnExtensionModuleIsFoundByItsRealCategoryEndToEnd drives the whole thing
// through a real Index over a real tree, because the slot fix is only worth
// anything if search_code's category filter reaches it.
func TestAnExtensionModuleIsFoundByItsRealCategoryEndToEnd(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		mkdirAllOrFatal(t, filepath.Dir(p))
		writeFileOrFatal(t, p, body)
	}
	write("Configuration.xml", classicExtensionManifest("МоёРасш", true))
	write("Catalogs/Номенклатура/Ext/ObjectModule.bsl", "Процедура ПриЗаписи() КонецПроцедуры\n")
	write("CommonModules/Общий/Ext/Module.bsl", "Процедура Служебная() КонецПроцедуры\n")

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	<-idx.Done()

	const want = "ext.МоёРасш.Справочник.Номенклатура.МодульОбъекта"
	if names := idx.ModuleNames(); !slices.Contains(names, want) {
		t.Fatalf("the extension module is not in the index: %v", names)
	}
	got, err := idx.filterModules("Справочник", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, want) {
		t.Errorf("filterModules(\"Справочник\", \"\") = %v, want it to contain %q: a caller "+
			"filtering by the documented vocabulary got nothing back for a whole extension", got, want)
	}
	// Negative control: the filter still discriminates.
	if other, err := idx.filterModules("Документ", ""); err != nil {
		t.Fatal(err)
	} else if len(other) != 0 {
		t.Errorf("filterModules(\"Документ\", \"\") = %v, want none", other)
	}
	// And the module type still selects across the namespace.
	if byType, err := idx.filterModules("", "МодульОбъекта"); err != nil {
		t.Fatal(err)
	} else if !slices.Contains(byType, want) {
		t.Errorf("filterModules(\"\", \"МодульОбъекта\") = %v, want it to contain %q", byType, want)
	}
}

// TestTwoFilesAnchoringOntoOneKeyAreCountedAndOneIsUnreachable pins the residual
// the anchor scan cannot remove, as an OUTCOME rather than as a promise.
//
// anchorIndex is a pure function of ONE path, so it cannot know that the key it
// derives is already taken; a collision check inside it is not merely absent, it
// is not expressible there. What the index can do is count the loss, and it does:
// the loser is absent from every map the index reads through, ModuleCount still
// counts it, and CollapsedKeys is what makes the difference visible.
func TestTwoFilesAnchoringOntoOneKeyAreCountedAndOneIsUnreachable(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		mkdirAllOrFatal(t, filepath.Dir(p))
		writeFileOrFatal(t, p, body)
	}
	// The configuration's own session module, and an object whose inner Ext holds
	// a file named after one of the four configuration modules. Both anchor onto
	// "Конфигурация.МодульСеанса.Модуль".
	write("Ext/SessionModule.bsl", "// настоящий модуль сеанса\n")
	write("Catalogs/Ном/Ext/SessionModule.bsl", "// самозванец\n")

	const key = "Конфигурация.МодульСеанса.Модуль"
	if got := bslPathToModuleName("Catalogs/Ном/Ext/SessionModule.bsl"); got != key {
		t.Fatalf("premise moved: the second path keys as %q, so the two do not collide", got)
	}

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	<-idx.Done()

	st := idx.CollapsedKeys()
	if st.Files != 1 || st.Keys != 1 {
		t.Errorf("CollapsedKeys = %+v, want exactly one lost file under one key: the "+
			"collision is invisible unless it is counted", st)
	}
	if !slices.Contains(st.Sample, key) {
		t.Errorf("Sample = %v, want it to name %q", st.Sample, key)
	}
	if idx.ModuleCount() != 2 {
		t.Errorf("ModuleCount = %d, want 2: the count reports files walked, which is exactly "+
			"why it must not be read alone", idx.ModuleCount())
	}
	// Exactly one of the two contents is reachable, and the other one is gone.
	content, ok := idx.GetContent(key)
	if !ok {
		t.Fatalf("GetContent(%q) found nothing", key)
	}
	if strings.Contains(content, "настоящий") == strings.Contains(content, "самозванец") {
		t.Errorf("GetContent(%q) = %q, want exactly one of the two files", key, content)
	}
}

func mkdirAllOrFatal(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFileOrFatal(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
