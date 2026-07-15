package dump

import (
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Classification completeness: every service kind the membership canonicalizer
// knows must be CLASSIFIED -- either included in the universe or explicitly
// excluded with a reason -- so a future serviceKindEnToRu addition cannot be
// silently forgotten from the orphans universe (the bug class this fix closes).
// ---------------------------------------------------------------------------

func TestUniverse_ClassifiesEveryServiceKind(t *testing.T) {
	included := map[string]bool{}
	for _, k := range universeServiceKinds {
		if included[k.singularEn] {
			t.Errorf("universeServiceKinds has a duplicate singularEn %q", k.singularEn)
		}
		included[k.singularEn] = true
	}
	for en := range serviceKindEnToRu {
		isIncluded := included[en]
		_, isExcluded := nonUniverseServiceKinds[en]
		switch {
		case isIncluded && isExcluded:
			t.Errorf("service kind %q is BOTH included and excluded; it must be exactly one", en)
		case !isIncluded && !isExcluded:
			t.Errorf("service kind %q is UNCLASSIFIED: add it to universeServiceKinds or nonUniverseServiceKinds (with a reason)", en)
		}
	}
	// Every excluded key must be a real service kind (no stale entries).
	for en := range nonUniverseServiceKinds {
		if _, ok := serviceKindEnToRu[en]; !ok {
			t.Errorf("nonUniverseServiceKinds has %q which is not a serviceKindEnToRu key (stale)", en)
		}
	}
	if got, want := len(included)+len(nonUniverseServiceKinds), len(serviceKindEnToRu); got != want {
		t.Errorf("classification count = %d (included %d + excluded %d), want %d (all serviceKindEnToRu keys)",
			got, len(included), len(nonUniverseServiceKinds), want)
	}
	// Anchor the FINALIZED set so a silent add/drop of a kind fails loudly. serviceKindEnToRu
	// has 29 keys; 27 are in the universe (applied 15 are separate) and exactly 2 are excluded
	// (Subsystem, Language). IntegrationService is intentionally absent from all three.
	if got := len(serviceKindEnToRu); got != 29 {
		t.Errorf("serviceKindEnToRu = %d keys, want 29 (finalized set)", got)
	}
	if got := len(universeServiceKinds); got != 27 {
		t.Errorf("universeServiceKinds = %d, want 27 (finalized Состав-eligible service set)", got)
	}
	if got := len(nonUniverseServiceKinds); got != 2 {
		t.Errorf("nonUniverseServiceKinds = %d, want 2 (only Subsystem + Language)", got)
	}
	if _, ok := serviceKindEnToRu["IntegrationService"]; ok {
		t.Error("IntegrationService must NOT be in serviceKindEnToRu (documented uncertain omission, unvalidated names)")
	}
}

// ---------------------------------------------------------------------------
// THE INVARIANT (prevents this bug class forever): the universe enumerator must
// cover EVERY top-level kind the membership canonicalizer can emit. i.e. if
// canonicalizeContentPath can produce "ОбщийМодуль.X", the universe must be able
// to enumerate ОбщийМодуль. This is the structural guarantee that orphans and
// containing stay consistent.
// ---------------------------------------------------------------------------

func TestUniverse_CoversEveryMembershipEmittableTopLevelKind(t *testing.T) {
	// The top-level EN prefixes the membership canonicalizer classifies as universe
	// kinds: the applied 15 (appliedKindEnToRu) plus the included service kinds.
	var topLevelEn []string
	for en := range appliedKindEnToRu {
		topLevelEn = append(topLevelEn, en)
	}
	for _, k := range universeServiceKinds {
		topLevelEn = append(topLevelEn, k.singularEn)
	}
	for _, en := range topLevelEn {
		emitted := canonicalizeContentPath(en + ".Проба")
		ru := membershipKindPrefix(emitted)
		if ru == "" {
			t.Errorf("canonicalizeContentPath(%q) = %q has no kind prefix", en+".Проба", emitted)
			continue
		}
		if !universeRuKinds[ru] {
			t.Errorf("INVARIANT VIOLATED: membership can emit %q (from %q) but the universe cannot enumerate kind %q",
				emitted, en, ru)
		}
	}
}

// The universe RU prefix for every included kind must EQUAL what the membership
// canonicalizer emits for the same English prefix. If they diverged, an object's
// universe string and its membership string would not cancel, producing false
// orphans or silent misses.
func TestUniverse_RuMatchesMembershipCanonicalizer(t *testing.T) {
	for _, k := range universeServiceKinds {
		want := membershipKindPrefix(canonicalizeContentPath(k.singularEn + ".X"))
		if got := universeFolderToRu[k.folderPlural]; got != want {
			t.Errorf("kind %s: universe folder %q -> %q, but membership canonicalizes %q -> %q; they must match",
				k.singularEn, k.folderPlural, got, k.singularEn+".X", want)
		}
		// Legacy singular folder must map to the same RU.
		if got := universeFolderToRu[k.singularEn]; got != want {
			t.Errorf("kind %s: legacy singular folder %q -> %q, want %q", k.singularEn, k.singularEn, got, want)
		}
	}
}

// End-to-end folder walk: an object written under EVERY universe kind's dump folder
// must be enumerated with the correct canonical Russian prefix. This catches a
// folder-name typo in universeServiceKinds that the pure-table tests cannot.
func TestUniverse_EnumeratesEveryIncludedKindFromItsFolder(t *testing.T) {
	dir := t.TempDir()
	want := map[string]bool{}

	// Applied kinds (via metadataTypes plural folders); Constant is covered by
	// universeServiceKinds below, so skip it here to avoid a duplicate.
	for _, mt := range metadataTypes {
		if mt.SingularEng == "Constant" {
			continue
		}
		secWrite(t, filepath.Join(dir, mt.PluralEng, "Тест.xml"), objBody("Тест"))
		want[mt.RussianName+".Тест"] = true
	}
	// Every included service / extra kind (Constant among them).
	for _, k := range universeServiceKinds {
		secWrite(t, filepath.Join(dir, k.folderPlural, "Тест.xml"), objBody("Тест"))
		want[universeKindRu(k.singularEn)+".Тест"] = true
	}

	got := map[string]bool{}
	for _, o := range EnumerateAppliedObjects(dir) {
		got[o] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("universe did not enumerate %q from its folder (folder-name mismatch?)", w)
		}
	}
}

// The FINALIZED universe must enumerate every Состав-eligible top-level RU prefix (the
// original owner-approved 18 PLUS the 9 finalization additions). The container Подсистема
// and the non-eligible Язык must NOT be enumerable. This fails if any newly-added kind is
// dropped from the universe, and if Язык is ever (wrongly) added.
func TestUniverse_IncludesFinalizedSetAndExcludesContainerAndLanguage(t *testing.T) {
	target := []string{
		// original owner-approved 18
		"Константа", "ОбщийМодуль", "ОбщаяФорма", "ОбщаяКоманда", "ГруппаКоманд",
		"ОбщийМакет", "ОбщаяКартинка", "ОпределяемыйТип", "HTTPСервис", "WebСервис",
		"ПакетXDTO", "ПараметрСеанса", "РегламентноеЗадание", "Роль",
		"ФункциональнаяОпция", "ПодпискаНаСобытие", "ЖурналДокументов", "ХранилищеНастроек",
		// finalization additions (each Состав-eligible; evidence-confirmed EN prefix)
		"Стиль", "ЭлементСтиля", "КритерийОтбора", "Последовательность", "ОбщийРеквизит",
		"Нумератор", "WSСсылка", "ПараметрФункциональнойОпции", "ВнешнийИсточникДанных",
	}
	for _, ru := range target {
		if !universeRuKinds[ru] {
			t.Errorf("universe must enumerate the Состав-eligible kind %q", ru)
		}
	}
	// The Нумератор correction must have landed: the earlier НумераторДокументов prefix must
	// no longer be a universe kind.
	if universeRuKinds["НумераторДокументов"] {
		t.Error("universe must NOT enumerate НумераторДокументов (corrected to Нумератор)")
	}
	// Container: never a member object.
	if universeRuKinds["Подсистема"] {
		t.Error("universe must NOT enumerate Подсистема (the container is not a member object)")
	}
	if _, ok := nonUniverseServiceKinds["Subsystem"]; !ok {
		t.Error("Subsystem must be classified as a non-universe (container) kind")
	}
	// Язык: not Состав-eligible; enumerating it would emit a false orphan per language.
	if universeRuKinds["Язык"] {
		t.Error("universe must NOT enumerate Язык (not Состав-eligible; would emit a false orphan per language)")
	}
	if _, ok := nonUniverseServiceKinds["Language"]; !ok {
		t.Error("Language must be classified as a non-universe kind")
	}
}

// Язык is NOT Состав-eligible: even when a dump physically carries a Languages/ folder,
// the universe must not enumerate any Язык object (else every language would be a false
// orphan). Guards the documented exclusion at the enumerator level. A finalized-set kind
// (Стиль) written into the same dump IS enumerated, proving the dump is otherwise live.
func TestUniverse_DoesNotEnumerateLanguageEvenWithFolder(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Languages", "Русский.xml"), objBody("Русский"))
	secWrite(t, filepath.Join(dir, "Language", "Английский.xml"), objBody("Английский")) // legacy singular folder too
	secWrite(t, filepath.Join(dir, "Styles", "Основной.xml"), objBody("Основной"))

	got := EnumerateAppliedObjects(dir)
	for _, o := range got {
		if strings.HasPrefix(o, "Язык.") {
			t.Errorf("universe enumerated a Язык object %q; Язык must be excluded", o)
		}
	}
	found := false
	for _, o := range got {
		if o == "Стиль.Основной" {
			found = true
		}
	}
	if !found {
		t.Errorf("universe must enumerate the finalized-set kind Стиль.Основной from the same dump, got %v", got)
	}
}

// Set-cancellation for EVERY finalization addition: an object written under the kind's
// dump folder must yield a universe string byte-identical to the membership string
// canonicalizeContentPath produces for the same object referenced (by its EN prefix) from
// a subsystem Состав. If they diverged, a distributed object of that kind would be a false
// orphan (or an out-of-subsystem one silently missed). ExternalDataSource is exercised at
// the TOP level (the only level it enumerates).
func TestUniverse_CancellationForFinalizedKinds(t *testing.T) {
	cases := []struct {
		folder string // dump folder (EN plural)
		enRef  string // EN membership ref as written in a subsystem Content
	}{
		{"Styles", "Style.Основной"},
		{"StyleItems", "StyleItem.ЦветАкцента"},
		{"FilterCriteria", "FilterCriterion.ПоКонтрагенту"},
		{"Sequences", "Sequence.ДвижениеТоваров"},
		{"CommonAttributes", "CommonAttribute.ОбластьДанных"},
		{"DocumentNumerators", "DocumentNumerator.НалоговыеДокументы"},
		{"WSReferences", "WSReference.WSОбмен"},
		{"FunctionalOptionsParameters", "FunctionalOptionsParameter.Организация"},
		{"ExternalDataSources", "ExternalDataSource.ВнешняяБаза"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		name := strings.SplitN(c.enRef, ".", 2)[1]
		secWrite(t, filepath.Join(dir, c.folder, name+".xml"), objBody(name))
		universe := EnumerateAppliedObjects(dir)
		uni := map[string]bool{}
		for _, u := range universe {
			uni[u] = true
		}
		membership := canonicalizeContentPath(c.enRef)
		if !uni[membership] {
			t.Errorf("%s: cancellation broken; membership %q (from %q) not byte-present in universe %v",
				c.folder, membership, c.enRef, universe)
		}
	}
}

// ---------------------------------------------------------------------------
// The non-silent coverage diagnostic: a universe kind that membership references
// but whose dump folder is ABSENT is NAMED in a warning (never silently dropped),
// while a present-but-empty folder and an unreferenced-absent kind stay silent
// (live parity). All warnings are path-free and contain no тире.
// ---------------------------------------------------------------------------

func TestUniverseCoverage_WarnsOnAbsentReferencedKindFolder(t *testing.T) {
	dir := t.TempDir()
	// A universe folder IS present for one kind (Documents) but ABSENT for the kind a
	// subsystem references (CommonModules).
	secWrite(t, filepath.Join(dir, "Documents", "Реализация.xml"), objBody("Реализация"))

	subs := []Subsystem{{Name: "Учет", Content: []string{"ОбщийМодуль.Скрытый", "Документ.Реализация"}}}
	_, warnings := EnumerateUniverseObjects(dir, subs)

	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one (the absent ОбщийМодуль folder)", warnings)
	}
	if !strings.Contains(warnings[0], "ОбщийМодуль") {
		t.Errorf("warning must NAME the kind ОбщийМодуль, got %q", warnings[0])
	}
	for _, w := range warnings {
		if strings.ContainsRune(w, '—') || strings.ContainsRune(w, '–') || strings.ContainsRune(w, '‐') {
			t.Errorf("coverage warning contains a dash (тире), violates the no-тире rule: %q", w)
		}
	}
}

func TestUniverseCoverage_NoWarnWhenFolderPresent(t *testing.T) {
	dir := t.TempDir()
	// CommonModules present (even empty of valid objects): a referenced kind whose
	// folder exists must NOT warn.
	secWrite(t, filepath.Join(dir, "CommonModules", "ОбщегоНазначения.xml"), objBody("ОбщегоНазначения"))

	subs := []Subsystem{{Name: "Учет", Content: []string{"ОбщийМодуль.ОбщегоНазначения"}}}
	_, warnings := EnumerateUniverseObjects(dir, subs)
	if len(warnings) != 0 {
		t.Errorf("a present folder must not warn, got %v", warnings)
	}
}

func TestUniverseCoverage_NoWarnForUnreferencedAbsentKind(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Documents", "Реализация.xml"), objBody("Реализация"))
	// CommonModules is absent, but membership references ONLY Документ (present), so the
	// absent-and-unreferenced CommonModules must NOT warn (matches the live path, which
	// never warns on a merely-empty kind).
	subs := []Subsystem{{Name: "Учет", Content: []string{"Документ.Реализация"}}}
	_, warnings := EnumerateUniverseObjects(dir, subs)
	if len(warnings) != 0 {
		t.Errorf("an absent but unreferenced kind must not warn, got %v", warnings)
	}
}

func TestUniverseCoverage_IgnoresContainerAndUnknownPrefixes(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Catalogs", "Валюты.xml"), objBody("Валюты"))
	// Подсистема (container, excluded from the universe) and an unknown prefix are
	// referenced but their folders are absent; neither is a universe kind, so neither warns.
	subs := []Subsystem{{Name: "Учет", Content: []string{"Подсистема.Вложенная", "НекийВид.Объект", "Справочник.Валюты"}}}
	_, warnings := EnumerateUniverseObjects(dir, subs)
	if len(warnings) != 0 {
		t.Errorf("container / unknown prefixes must not warn, got %v", warnings)
	}
}

func TestUniverseCoverage_NilMembershipNoWarn(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Catalogs", "Валюты.xml"), objBody("Валюты"))
	objs, warnings := EnumerateUniverseObjects(dir, nil)
	if len(warnings) != 0 {
		t.Errorf("nil membership must yield no warnings, got %v", warnings)
	}
	if len(objs) != 1 || objs[0] != "Справочник.Валюты" {
		t.Errorf("objects = %v, want [Справочник.Валюты]", objs)
	}
}
