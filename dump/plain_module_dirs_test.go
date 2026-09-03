package dump

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// plainModuleDirs, and the rule that decides who is in it.
//
// A kind whose object owns a plain Ext/Module.bsl but is missing from the map keys
// as "МодульФормы" instead of "Модуль": a wrong module type in the segment a user
// reads and filters on. Three members were added one at a time as each was
// noticed, which is a process with no way to tell whether it is finished. The
// property that decides it is the platform's own.

const kindPropertiesFixture = "testdata/metadata_kind_properties.txt"

// kindProperties reads the snapshot of «ОбъектМетаданных: <Вид>» property tables.
func kindProperties(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(kindPropertiesFixture))
	if err != nil {
		t.Fatalf("reading %s: %v", kindPropertiesFixture, err)
	}
	got := map[string][]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, props, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("malformed row in %s: %q", kindPropertiesFixture, line)
		}
		got[kind] = strings.Split(props, ",")
	}
	if len(got) == 0 {
		t.Fatalf("%s holds no rows, so every check below would measure nothing", kindPropertiesFixture)
	}
	return got
}

// TestPlainModuleDirsIsTheKindsWithAModuleAndNoForms turns the membership of that
// map from a list into a rule, checked in both directions against the platform
// property tables.
func TestPlainModuleDirsIsTheKindsWithAModuleAndNoForms(t *testing.T) {
	props := kindProperties(t)
	has := func(kind, prop string) bool { return slices.Contains(props[kind], prop) }

	// NESTED kinds are skipped, and the reason is categorical rather than a
	// judgement about any one of them: a nested kind has no top-level dump
	// directory at all, so there is no dumpDirNames entry it could ever be missing
	// from. Which kinds those are is read from the nested-kind fixture rather than
	// listed here, so the two files cannot drift apart.
	nested := nestedKindRussianNames(t)

	checkedIn, checkedOut := 0, 0
	for kind := range props {
		if nested[kind] {
			continue
		}
		if _, known := props[kind]; !known {
			continue
		}
		// Find the dump directory this kind is the Russian name of.
		dir := ""
		for d, ru := range dumpDirNames {
			if ru == kind {
				dir = d
				break
			}
		}
		if dir == "" {
			t.Errorf("the fixture holds %q, which is not a value of dumpDirNames; either the "+
				"table lost a kind or the fixture gained one nothing maps to", kind)
			continue
		}
		wantPlain := has(kind, "Модуль") && !has(kind, "Формы")
		if got := plainModuleDirs[dir]; got != wantPlain {
			yesno := map[bool]string{true: "yes", false: "no"}
			t.Errorf("plainModuleDirs[%q] = %v, want %v: in %s «%s» has Модуль=%s Формы=%s",
				dir, got, wantPlain, kindPropertiesFixture, kind,
				yesno[has(kind, "Модуль")], yesno[has(kind, "Формы")])
		}
		if wantPlain {
			checkedIn++
		} else {
			checkedOut++
		}
	}
	if checkedIn == 0 || checkedOut == 0 {
		t.Fatalf("the fixture covers %d kinds that must be in the map and %d that must be out; "+
			"a rule checked in one direction only is not a rule", checkedIn, checkedOut)
	}

	// EVERY non-nested kind must have been examined, and this is the assertion that
	// makes an exclusion impossible to reintroduce quietly. A skip added to the loop
	// above does not fail anything by itself: it simply checks fewer kinds, both
	// counters stay non-zero, and the test goes on reporting success over a smaller
	// set. Measured, on this very file: adding back a two-name skip left every test
	// here green. Counting the kinds the loop was supposed to reach is what turns
	// that from invisible into loud.
	want := 0
	for kind := range props {
		if !nested[kind] {
			want++
		}
	}
	if got := checkedIn + checkedOut; got != want {
		t.Errorf("the rule was checked against %d kinds, but %s holds %d that are not nested. "+
			"Some kind is being skipped, and a rule that skips its awkward cases is not a rule.",
			got, kindPropertiesFixture, want)
	}

	// Every member of the map must be covered by the fixture, or a member could be
	// added without any evidence behind it and the loop above would never see it.
	for dir := range plainModuleDirs {
		ru, ok := dumpDirNames[dir]
		if !ok {
			t.Errorf("plainModuleDirs holds %q, which dumpDirNames does not name", dir)
			continue
		}
		if _, ok := props[ru]; !ok {
			t.Errorf("plainModuleDirs holds %q («%s»), which %s does not cover: its membership "+
				"rests on nothing this tree can check", dir, ru, kindPropertiesFixture)
		}
	}
}

// TestABotsOwnModuleIsNotAFormModule is the defect itself, as a key.
func TestABotsOwnModuleIsNotAFormModule(t *testing.T) {
	const path = "Bots/ОфисМенеджер/Ext/Module.bsl"
	const want = "Бот.ОфисМенеджер.Модуль"
	if got := bslPathToModuleName(path); got != want {
		t.Errorf("bslPathToModuleName(%q) = %q, want %q", path, got, want)
	}
	// Inside an extension the same file must answer the same module-type filter.
	const extPath = "Расширения/МоёРасш/Bots/ОфисМенеджер/Ext/Module.bsl"
	const extWant = "ext.МоёРасш.Бот.ОфисМенеджер.Модуль"
	if got := bslPathToModuleName(extPath); got != extWant {
		t.Errorf("bslPathToModuleName(%q) = %q, want %q", extPath, got, extWant)
	}
	if got := parseModuleName(extWant); got.category != "Бот" || got.module != "Модуль" {
		t.Errorf("parseModuleName(%q) = %+v, want category Бот and module Модуль", extWant, got)
	}

	// NEGATIVE CONTROL: membership narrows the suffix for Module.bsl and touches
	// nothing else. A bot's other files, and a kind that really does have forms,
	// keep the keys they had.
	for path, want := range map[string]string{
		"Bots/ОфисМенеджер/Ext/ObjectModule.bsl":         "Бот.ОфисМенеджер.МодульОбъекта",
		"Bots/ОфисМенеджер/Forms/Ф/Ext/Form/Module.bsl":  "Бот.ОфисМенеджер.Форма.Ф.МодульФормы",
		"Catalogs/Ном/Forms/Ф/Ext/Form/Module.bsl":       "Справочник.Ном.Форма.Ф.МодульФормы",
		"CommonForms/ФормаНастроек/Ext/Form/Module.bsl":  "ОбщаяФорма.ФормаНастроек.МодульФормы",
		"DataProcessors/Обр/Forms/Ф/Ext/Form/Module.bsl": "Обработка.Обр.Форма.Ф.МодульФормы",
	} {
		if got := bslPathToModuleName(path); got != want {
			t.Errorf("bslPathToModuleName(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestThreeKindsThatUsedToKeepTheirEnglishPrefix is the prefix defect, as keys.
//
// All three directories were missing from dumpDirNames, so baseConfigModuleName
// fell back to the raw English directory name and the category slot of the key
// carried it verbatim. Two of them compounded it: their Ext/Module.bsl is the
// object's OWN module, and without a plainModuleDirs entry it also took the
// "МодульФормы" suffix, so both halves of the key a user reads and filters on were
// wrong at once.
//
// The rows cover the base configuration and the inside of an extension, because
// the "ext.<ext>." prefix pushes the same key past the segment threshold where
// parseModuleName starts filling the module-type slot, and a kind can key correctly
// in one place and not the other.
func TestThreeKindsThatUsedToKeepTheirEnglishPrefix(t *testing.T) {
	for _, tc := range []struct {
		path, want string
	}{
		// Own module: prefix AND suffix both move.
		{"IntegrationServices/Сервис1/Ext/Module.bsl", "СервисИнтеграции.Сервис1.Модуль"},
		{"WebSocketClients/Клиент1/Ext/Module.bsl", "WebSocketКлиент.Клиент1.Модуль"},
		// Inside an extension the same two files answer the same filter.
		{"Расширения/Доработки/IntegrationServices/Сервис1/Ext/Module.bsl",
			"ext.Доработки.СервисИнтеграции.Сервис1.Модуль"},
		{"Расширения/Доработки/WebSocketClients/Клиент1/Ext/Module.bsl",
			"ext.Доработки.WebSocketКлиент.Клиент1.Модуль"},
		// A different module file name: only the prefix was ever wrong for these,
		// and the plainModuleDirs entry must not reach them.
		{"IntegrationServices/Сервис1/Ext/ManagerModule.bsl",
			"СервисИнтеграции.Сервис1.МодульМенеджера"},
		{"ExternalDataSources/Источник1/Ext/ManagerModule.bsl",
			"ВнешнийИсточникДанных.Источник1.МодульМенеджера"},
		{"Расширения/Доработки/ExternalDataSources/Источник1/Ext/ManagerModule.bsl",
			"ext.Доработки.ВнешнийИсточникДанных.Источник1.МодульМенеджера"},
		// ExternalDataSources is deliberately NOT in plainModuleDirs: its kind has
		// no «Модуль» property at all, so the rule puts it out and a bare Module.bsl
		// keeps the generic mapping. The row pins the OUT direction of that rule.
		{"ExternalDataSources/Источник1/Ext/Module.bsl",
			"ВнешнийИсточникДанных.Источник1.МодульФормы"},
	} {
		if got := bslPathToModuleName(tc.path); got != tc.want {
			t.Errorf("bslPathToModuleName(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}

	// The module-type slot must really be filled, or no module-type filter selects
	// these however right the string looks.
	for _, tc := range []struct {
		key, category, module string
	}{
		{"СервисИнтеграции.Сервис1.Модуль", "СервисИнтеграции", "Модуль"},
		{"ext.Доработки.WebSocketКлиент.Клиент1.Модуль", "WebSocketКлиент", "Модуль"},
		{"ext.Доработки.ВнешнийИсточникДанных.Источник1.МодульМенеджера",
			"ВнешнийИсточникДанных", "МодульМенеджера"},
	} {
		got := parseModuleName(tc.key)
		if got.category != tc.category || got.module != tc.module {
			t.Errorf("parseModuleName(%q) = %+v, want category %q and module %q",
				tc.key, got, tc.category, tc.module)
		}
	}
}

// TestTheNewPrefixesAreCitedNotTyped. A missing entry is loud; a WRONG Russian name
// is silent, because the key looks ordinary and is simply never the one anybody
// asks for. None of these three kinds is in the manifest fixture that
// TestDumpDirRussianNamesMatchTheKindTables cross-checks, so that guard does not
// reach them and this one stands in its place: each name must already exist in a
// table this package built from a source, not be a string typed here.
func TestTheNewPrefixesAreCitedNotTyped(t *testing.T) {
	props := kindProperties(t)

	// Two are cited from the platform type reference, via the property snapshot.
	for dir, kind := range map[string]string{
		"IntegrationServices": "СервисИнтеграции",
		"WebSocketClients":    "WebSocketКлиент",
	} {
		if got := dumpDirNames[dir]; got != kind {
			t.Errorf("dumpDirNames[%q] = %q, want %q", dir, got, kind)
		}
		if _, ok := props[kind]; !ok {
			t.Errorf("%s no longer carries «%s», so the prefix rests on nothing this tree "+
				"can check", kindPropertiesFixture, kind)
		}
	}

	// The third is a copy of a name this package already uses for the same kind.
	const dir, kind, en = "ExternalDataSources", "ВнешнийИсточникДанных", "ExternalDataSource"
	if got := dumpDirNames[dir]; got != kind {
		t.Errorf("dumpDirNames[%q] = %q, want %q", dir, got, kind)
	}
	src, ok := ServiceKindNameRu(en)
	if !ok {
		t.Fatalf("serviceKindEnToRu no longer carries %q, so the copy has no original", en)
	}
	if src != kind {
		t.Errorf("dumpDirNames[%q] = %q but serviceKindEnToRu renders %q as %q; the entry was "+
			"supposed to be a copy of that name, not a second opinion", dir, kind, en, src)
	}

	// The pin next door stays intact: this work gives the integration service a dump
	// directory, and gives it NOTHING in the service-kind table.
	if _, ok := serviceKindEnToRu["IntegrationService"]; ok {
		t.Error("IntegrationService is in serviceKindEnToRu; the universe classification pin " +
			"forbids it and nothing here was supposed to add it")
	}
}
