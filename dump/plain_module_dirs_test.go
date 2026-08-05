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

	// The kinds this repository does not claim as dump directories are excluded by
	// name, because the reason they are out is the FOLDER name and not the
	// property. Keeping them in the fixture and excluding them here is what stops
	// the exclusion from being invisible.
	notClaimed := map[string]bool{"СервисИнтеграции": true, "WebSocketКлиент": true}

	checkedIn, checkedOut := 0, 0
	for kind := range props {
		if notClaimed[kind] {
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

// TestTheTwoUnclaimedKindsAreAbsentOnPurpose. Both have a «Модуль» property and no
// «Формы», so by the rule above they would belong in plainModuleDirs, and both are
// out. The reason is not the property: it is that the map is keyed by the DUMP
// DIRECTORY name, and nothing on this machine has ever shown what 1C calls those
// directories. The Russian singular is read; the folder name is not.
//
// The repository already holds that decision for one of them, with a test that
// forbids adding it to serviceKindEnToRu on exactly these grounds. This test is the
// other half: it fails if either kind is quietly given a directory anyway.
func TestTheTwoUnclaimedKindsAreAbsentOnPurpose(t *testing.T) {
	props := kindProperties(t)
	for _, kind := range []string{"СервисИнтеграции", "WebSocketКлиент"} {
		p, ok := props[kind]
		if !ok {
			t.Fatalf("%s no longer carries «%s», so this test measures nothing", kindPropertiesFixture, kind)
		}
		if !slices.Contains(p, "Модуль") || slices.Contains(p, "Формы") {
			t.Errorf("«%s» no longer matches the plainModuleDirs rule, so the premise of this "+
				"exclusion has changed and it should be revisited", kind)
		}
		for dir, ru := range dumpDirNames {
			if ru == kind {
				t.Errorf("dumpDirNames[%q] = %q: a dump directory name was written for a kind "+
					"whose directory nothing on this machine has ever shown", dir, ru)
			}
		}
	}
}
