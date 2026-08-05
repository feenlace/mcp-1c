package dump

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Guard 1: the dumpDirNames table must cover every top-level dump directory
// that has actually been observed holding .bsl modules.
// ---------------------------------------------------------------------------

// topLevelModuleDirsFixture is the measured ground truth: one top-level dump
// directory name per line. It is deliberately NOT a copy of dumpDirNames — it
// records what was seen on disk, so a directory present on disk but absent from
// the table is a finding rather than a silent English-prefix key.
const topLevelModuleDirsFixture = "testdata/dump_toplevel_module_dirs.txt"

// nonObjectKindDirs are top-level dump directories that hold modules but are NOT
// metadata object kinds, so they must NOT get a dumpDirNames entry: they carry no
// <objectName> segment and are keyed by a dedicated branch instead.
//
//   - "Ext":         the configuration's own modules (Ext/SessionModule.bsl, ...),
//     keyed as "Конфигурация.<RussianModuleName>.Модуль" by configModuleNames.
//   - "Расширения":  the extension container, stripped by bslPathToModuleName and
//     re-prefixed as "ext.<ext>." (extensionDirName).
var nonObjectKindDirs = []string{"Ext", extensionDirName}

// readTopLevelModuleDirs parses the fixture, dropping blank lines, full-line
// comments and trailing "# ..." annotations.
func readTopLevelModuleDirs(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.FromSlash(topLevelModuleDirsFixture))
	if err != nil {
		t.Fatalf("open fixture %s: %v", topLevelModuleDirsFixture, err)
	}
	defer f.Close()

	var dirs []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		dirs = append(dirs, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if len(dirs) == 0 {
		t.Fatalf("fixture %s parsed to zero entries; the parser or the file is broken",
			topLevelModuleDirsFixture)
	}
	return dirs
}

// uncoveredDirs returns the fixture entries that are neither a key of table nor a
// documented non-object-kind root. It takes the table as a parameter so the test
// can run it against a deliberately damaged copy as a positive control.
func uncoveredDirs(table map[string]string, dirs []string) []string {
	var missing []string
	for _, d := range dirs {
		if _, ok := table[d]; ok {
			continue
		}
		if slices.Contains(nonObjectKindDirs, d) {
			continue
		}
		missing = append(missing, d)
	}
	sort.Strings(missing)
	return missing
}

// TestDumpDirNamesCoversMeasuredTopLevelDirs fails when a top-level dump
// directory that really holds modules has no dumpDirNames entry. Without an
// entry baseConfigModuleName silently falls back to the raw English directory
// name as the key prefix (prefix = category), producing keys such as
// "HTTPServices.ЭДО.МодульФормы" that no resolver ever queries.
//
// The empty-difference result is only meaningful because the same checker is
// run against a table with a known entry removed and MUST report it. A green
// verdict from a checker that has not been shown to go red proves nothing.
func TestDumpDirNamesCoversMeasuredTopLevelDirs(t *testing.T) {
	dirs := readTopLevelModuleDirs(t)

	if missing := uncoveredDirs(dumpDirNames, dirs); len(missing) > 0 {
		t.Errorf("dumpDirNames has no entry for measured top-level dump directories %v.\n"+
			"Each of them yields raw-English-prefix keys (prefix = category in "+
			"baseConfigModuleName), which no resolver queries. Add the Russian name to "+
			"metadata_types.go, or, if the directory is not an object kind, document it in "+
			"nonObjectKindDirs.", missing)
	}

	// Positive control: the checker must go red on a table that is missing a
	// known-present entry. Run on a copy so the real table is untouched.
	const control = "CommonModules"
	if _, ok := dumpDirNames[control]; !ok {
		t.Fatalf("positive control is broken: %q is not in dumpDirNames to begin with", control)
	}
	damaged := make(map[string]string, len(dumpDirNames))
	for k, v := range dumpDirNames {
		if k == control {
			continue
		}
		damaged[k] = v
	}
	got := uncoveredDirs(damaged, dirs)
	if !slices.Equal(got, []string{control}) {
		t.Fatalf("positive control failed: with %q removed the checker reported %v, want exactly [%s]. "+
			"The check cannot detect a missing entry, so its green verdict above is worthless.",
			control, got, control)
	}
}

// ---------------------------------------------------------------------------
// Guard 2: key derivation is pinned to dumpIndexSchemaVersion.
// ---------------------------------------------------------------------------

// keyDigestCorpus is a fixed set of dump-relative paths covering every branch of
// bslPathToModuleName. It is built from two halves that must stay distinguishable,
// because they pin two different claims:
//
//   - unwrappedKeyDigestCorpus: paths at a CORRECTLY pointed root. Their digest is
//     pinned separately, as bslUnwrappedCorpusDigest, and that digest is the
//     evidence that the anchor scan is a no-op where it must be one.
//   - anchoredKeyDigestCorpus: paths whose key the anchor scan derives from some
//     index OTHER than 0. Without them the guard could not see the scan at all:
//     every unwrapped row anchors at 0, so removing the scan entirely leaves
//     bslUnwrappedCorpusDigest untouched.
//
// The split is structural rather than an index into one slice, so inserting a row
// in the middle of either half cannot silently move the boundary.
var keyDigestCorpus = slices.Concat(unwrappedKeyDigestCorpus, anchoredKeyDigestCorpus)

// unwrappedKeyDigestCorpus covers every branch of bslPathToModuleName at a correct
// root: each dumpDirNames prefix family, the Forms/Commands infix, the
// CommonModules/HTTPServices/WebServices plain-Module rule, the configuration
// modules under Ext/, the extension subtree, and the unknown-category fallback.
//
// It is a literal list on purpose. Deriving it from dumpDirNames would make the
// digest move together with the table and the guard would stop noticing a change.
var unwrappedKeyDigestCorpus = []string{
	"Catalogs/Номенклатура/Ext/ObjectModule.bsl",
	"Catalogs/Номенклатура/Ext/ManagerModule.bsl",
	"Catalogs/Номенклатура/Commands/Печать/Ext/CommandModule.bsl",
	"Catalogs/Номенклатура/Forms/ФормаСписка/Ext/Form/Module.bsl",
	"Documents/Реализация/Ext/ObjectModule.bsl",
	"DataProcessors/Обработка1/Ext/ObjectModule.bsl",
	"Reports/Отчет1/Ext/ObjectModule.bsl",
	"InformationRegisters/Цены/Ext/RecordSetModule.bsl",
	"AccumulationRegisters/Остатки/Ext/RecordSetModule.bsl",
	"AccountingRegisters/Хозрасчетный/Ext/RecordSetModule.bsl",
	"CalculationRegisters/Начисления/Ext/RecordSetModule.bsl",
	"ChartsOfAccounts/Основной/Ext/ObjectModule.bsl",
	"ChartsOfCharacteristicTypes/ВидыСубконто/Ext/ObjectModule.bsl",
	"ChartsOfCalculationTypes/Основные/Ext/ObjectModule.bsl",
	"ExchangePlans/Обмен/Ext/ObjectModule.bsl",
	"BusinessProcesses/Задание/Ext/ObjectModule.bsl",
	"Tasks/Задача1/Ext/ObjectModule.bsl",
	"Enums/Статусы/Ext/ManagerModule.bsl",
	"Constants/КурсВалюты/Ext/ValueManagerModule.bsl",
	"CommonModules/ОбщегоНазначения/Ext/Module.bsl",
	"CommonForms/ФормаНастроек/Ext/Form/Module.bsl",
	"CommonCommands/ОткрытьПочту/Ext/CommandModule.bsl",
	"DocumentJournals/Складской/Ext/ManagerModule.bsl",
	"HTTPServices/ЭДО/Ext/Module.bsl",
	"WebServices/Exchange/Ext/Module.bsl",
	"SettingsStorages/НастройкиНовостей/Ext/ManagerModule.bsl",
	"SettingsStorages/НастройкиНовостей/Forms/Настройка/Ext/Form/Module.bsl",
	"FilterCriteria/ДокументыПоКонтрагенту/Ext/ManagerModule.bsl",
	"Sequences/ДокументыОрганизаций/Ext/RecordSetModule.bsl",
	"Ext/ManagedApplicationModule.bsl",
	"Ext/SessionModule.bsl",
	"Ext/ExternalConnectionModule.bsl",
	"Ext/OrdinaryApplicationModule.bsl",
	"Расширения/Доработки3D/CommonModules/WA_ПовтИсп/Ext/Module.bsl",
	"Расширения/Доработки3D/HTTPServices/Сервис/Ext/Module.bsl",
	"Расширения/Доработки3D/Ext/ManagedApplicationModule.bsl",
	"Расширения/Доработки3D/DataProcessors/АРМ/Forms/Форма/Ext/Form/Module.bsl",
	"Styles/Основной/Ext/Module.bsl",
	"Module.bsl",
}

// anchoredKeyDigestCorpus is the half the anchor scan actually moves. Every row
// here derives its key from an index other than 0, so deleting the scan changes
// bslKeyCorpusDigest and this guard goes red — which is the whole reason the rows
// exist. TestAnchoredCorpusRowsReallyAnchor asserts that property directly, so a
// row that stopped anchoring could not sit here contributing nothing.
//
// Eight of the rows are wrappers and they are adversarial rather than convenient:
// four of them open with a name the scan must recognise and then REJECT on shape,
// drawn from three distinct such names ("Documents", "Ext", "Catalogs"). That is
// exactly the case a marker test alone gets wrong.
//
// The last two rows are not wrappers. They are the two residual classes the anchor
// scan is documented to move (see the anchor block in index.go and
// module_key_anchor_test.go): neither occurs in the 13575 measured paths and
// neither is a shape 1C emits, and they are pinned here so a later change to them
// moves the digest instead of passing unnoticed.
var anchoredKeyDigestCorpus = []string{
	"dump_bsl/Catalogs/Номенклатура/Ext/ObjectModule.bsl",
	"Documents/dumps/Catalogs/Номенклатура/Forms/ФормаСписка/Ext/Form/Module.bsl",
	"Ext/CommonModules/ОбщегоНазначения/Ext/Module.bsl",
	"a/b/c/d/e/Ext/SessionModule.bsl",
	"Catalogs/Спр/Расширения/Доработки3D/CommonModules/WA_ПовтИсп/Ext/Module.bsl",
	"main/HTTPServices/ЭДО/Ext/Module.bsl",
	"ext/Расширения/TestExt/Ext/SessionModule.bsl",
	"Catalogs/Спр/SettingsStorages/НастройкиНовостей/Forms/Настройка/Ext/Form/Module.bsl",
	"Catalogs/Ном/Ext/ManagedApplicationModule.bsl",
	"Catalogs/Расширения/Y/Catalogs/Ном/Ext/ObjectModule.bsl",
}

// bslKeyCorpusDigest is sha256 over "<path>\t<key>\n" for keyDigestCorpus in the
// order declared above.
//
// pinnedSchemaVersionForKeyDigest records the dumpIndexSchemaVersion this digest
// was taken under. The pair is the point of the guard: a docID derivation change
// is exactly the case the BUMP PROTOCOL in generation.go requires
// dumpIndexSchemaVersion to be bumped for, because a generation built by an older
// binary would otherwise be adopted and served under the old keys, leaving the
// change inert for every existing cache.
// bslUnwrappedCorpusDigest is the same digest taken over unwrappedKeyDigestCorpus
// ALONE, and it is the evidence for the claim that carries the anchor scan: at a
// correctly pointed root, not one key moves.
//
// It is pinned SEPARATELY and not folded into the value above because the two
// numbers answer different questions and must be able to move independently. This
// one moving means a correctly rooted dump now keys differently, which is a
// schema-version event under the BUMP PROTOCOL. The one above moving may mean only
// that the anchor scan changed, which is not.
//
// Its value is the digest the guard carried BEFORE the anchor scan existed, byte
// for byte, and that is the point: the scan was added, this number did not change.
const bslUnwrappedCorpusDigest = "7134dd1c0084959c2e8b8f7722972ca93d39954c9c401181465bfc7f156bbba0"

const (
	bslKeyCorpusDigest              = "7871387d3835a9e0ceaffce9f1083bfdfd0ec50e3b6a129faee944bc48e8ea88"
	pinnedSchemaVersionForKeyDigest = 3
)

func digestOf(paths []string) (string, string) {
	var sb strings.Builder
	for _, p := range paths {
		sb.WriteString(p)
		sb.WriteByte('\t')
		sb.WriteString(bslPathToModuleName(p))
		sb.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:]), sb.String()
}

func keyCorpusDigest() (string, string) { return digestOf(keyDigestCorpus) }

// TestBslKeyDerivationPinnedToSchemaVersion fails whenever bslPathToModuleName
// produces a different key for any corpus path, and whenever
// dumpIndexSchemaVersion moves without the digest being re-taken. It exists so a
// derivation change cannot ship while old caches are still adopted as current.
func TestBslKeyDerivationPinnedToSchemaVersion(t *testing.T) {
	if dumpIndexSchemaVersion != pinnedSchemaVersionForKeyDigest {
		t.Fatalf("dumpIndexSchemaVersion = %d but the key digest was pinned under %d. "+
			"Re-take the digest and update pinnedSchemaVersionForKeyDigest together with the bump.",
			dumpIndexSchemaVersion, pinnedSchemaVersionForKeyDigest)
	}

	got, table := keyCorpusDigest()
	if got != bslKeyCorpusDigest {
		t.Errorf("bslPathToModuleName key derivation changed.\n"+
			"digest got  = %s\ndigest want = %s\n\n"+
			"The document-ID derivation is named in the BUMP PROTOCOL in generation.go: bump "+
			"dumpIndexSchemaVersion (currently %d), then re-pin both bslKeyCorpusDigest and "+
			"pinnedSchemaVersionForKeyDigest. Without the bump every warm cache keeps serving the "+
			"old keys and the change is inert.\n\ncurrent derivation:\n%s",
			got, bslKeyCorpusDigest, dumpIndexSchemaVersion, table)
	}
}

// TestUnwrappedCorpusDigestDidNotMoveWithTheAnchorScan pins the no-op claim as a
// digest rather than as a sentence: the anchor scan was added to
// bslPathToModuleName and the keys of a correctly rooted dump are byte for byte
// what they were.
//
// This is the number the BUMP PROTOCOL cares about. bslKeyCorpusDigest above now
// also covers rows the scan deliberately moves, so it will change whenever the
// scan changes; this one changing means a real dump's keys changed, and that is
// what forces dumpIndexSchemaVersion up.
func TestUnwrappedCorpusDigestDidNotMoveWithTheAnchorScan(t *testing.T) {
	got, table := digestOf(unwrappedKeyDigestCorpus)
	if got != bslUnwrappedCorpusDigest {
		t.Errorf("the keys of a CORRECTLY ROOTED dump changed.\n"+
			"digest got  = %s\ndigest want = %s\n\n"+
			"This is the case the BUMP PROTOCOL in generation.go names: bump "+
			"dumpIndexSchemaVersion (currently %d) and re-pin both digests, or every warm "+
			"generation keeps serving the old keys.\n\ncurrent derivation:\n%s",
			got, bslUnwrappedCorpusDigest, dumpIndexSchemaVersion, table)
	}

	// And every row in that half really is at a correct root, so the digest above is
	// a statement about un-anchored derivation and not an accident.
	for _, p := range unwrappedKeyDigestCorpus {
		if i := anchorIndex(strings.Split(p, "/")); i != 0 {
			t.Errorf("%q anchors at %d, so it belongs in anchoredKeyDigestCorpus; "+
				"leaving it here makes bslUnwrappedCorpusDigest depend on the anchor scan",
				p, i)
		}
	}
}

// TestAnchoredCorpusRowsReallyAnchor is what closes the guard gap. Before the
// anchor rows existed, every corpus path anchored at 0, so DELETING the scan left
// the digest exactly where it was and the guard could not see the change at all.
//
// Each row here must anchor at a non-zero index. A row that stopped anchoring
// would sit in the corpus contributing nothing while looking like coverage, which
// is the failure mode this test exists to prevent.
func TestAnchoredCorpusRowsReallyAnchor(t *testing.T) {
	if len(anchoredKeyDigestCorpus) == 0 {
		t.Fatal("the anchored half of the corpus is empty, so removing the anchor scan " +
			"would not move bslKeyCorpusDigest and the guard is blind to it")
	}
	for _, p := range anchoredKeyDigestCorpus {
		if i := anchorIndex(strings.Split(p, "/")); i == 0 {
			t.Errorf("%q anchors at 0, so its key does not depend on the anchor scan and "+
				"it adds nothing to the guard", p)
		}
	}

	// Positive control: a correctly rooted path must NOT anchor, or the assertion
	// above would be satisfied by a scan that fires on everything.
	const rooted = "Catalogs/Номенклатура/Ext/ObjectModule.bsl"
	if i := anchorIndex(strings.Split(rooted, "/")); i != 0 {
		t.Fatalf("positive control failed: %q anchors at %d, so anchoring proves nothing",
			rooted, i)
	}

	// And the two halves must not overlap, or a row could be counted as evidence
	// for both claims at once.
	for _, p := range anchoredKeyDigestCorpus {
		if slices.Contains(unwrappedKeyDigestCorpus, p) {
			t.Errorf("%q is in both halves of the corpus", p)
		}
	}
}

// TestConfigModuleKeyFillsEveryFilterSlot pins the invariant that makes the
// three-segment "Конфигурация.<Модуль>.Модуль" shape worth its extra segment:
// every slot a consumer filters on is populated.
//
// Both consumers that split a docID — parseModuleName and PathIndex.AddEntry —
// only fill the module-type slot when a key has at least three segments. A
// two-segment key therefore leaves the type EMPTY, and a search with a non-empty
// Module filter selects none of the four configuration modules (filterModules
// skips a candidate whose parts.module differs from the requested type). It also
// made the same logical module behave differently inside an extension, where the
// "ext.<ext>." prefix pushes the key to four segments and the type slot ends up
// holding the module's own NAME.
//
// The third segment is the module type "Модуль". That is not new vocabulary: it
// is already the type a common module gets for its own Module.bsl
// ("ОбщийМодуль.ОбщегоНазначения.Модуль") and it is already one of the values
// documented for the module filter in tools/search.go. With it the category is
// "Конфигурация", the object-name slot holds the module's own Russian name, and
// the type slot holds "Модуль" — all three meaningful, and no key contains a
// literal ".bsl".
//
// The extension case is asserted alongside the base case on purpose: the point of
// the shape is that the two agree.
func TestConfigModuleKeyFillsEveryFilterSlot(t *testing.T) {
	const key = "Конфигурация.МодульСеанса.Модуль"
	if got := bslPathToModuleName("Ext/SessionModule.bsl"); got != key {
		t.Fatalf("bslPathToModuleName = %q, want %q", got, key)
	}

	parsed := parseModuleName(key)
	if parsed.category != configModulePrefix {
		t.Errorf("parseModuleName(%q).category = %q, want %q", key, parsed.category, configModulePrefix)
	}
	if parsed.name != "МодульСеанса" {
		t.Errorf("parseModuleName(%q).name = %q, want %q", key, parsed.name, "МодульСеанса")
	}
	if parsed.module != configModuleSuffix {
		t.Errorf("parseModuleName(%q).module = %q, want %q", key, parsed.module, configModuleSuffix)
	}

	// PathIndex must agree with parseModuleName; the two split docIDs
	// independently and a disagreement would make the same filter behave
	// differently depending on whether the path index is built.
	pi := NewPathIndex([]string{key})
	if ids := pi.FilterDocIDs(configModulePrefix, ""); !slices.Contains(ids, key) {
		t.Errorf("FilterDocIDs(%q, \"\") = %v, want it to contain %q", configModulePrefix, ids, key)
	}
	// The filter that returned NOTHING under the two-segment shape.
	if ids := pi.FilterDocIDs("", configModuleSuffix); !slices.Contains(ids, key) {
		t.Errorf("FilterDocIDs(\"\", %q) = %v, want it to contain %q: the module type must be a "+
			"segment of a configuration-module key", configModuleSuffix, ids, key)
	}
	if ids := pi.FilterDocIDs(configModulePrefix, configModuleSuffix); !slices.Contains(ids, key) {
		t.Errorf("FilterDocIDs(%q, %q) = %v, want it to contain %q",
			configModulePrefix, configModuleSuffix, ids, key)
	}

	// Negative control: the module's own name is NOT the module type, so
	// filtering by it must select nothing. Without this the assertion above
	// would also pass on a key whose every segment happened to match.
	if ids := pi.FilterDocIDs("", "МодульСеанса"); len(ids) != 0 {
		t.Errorf("FilterDocIDs(\"\", \"МодульСеанса\") = %v, want none", ids)
	}

	// Inside an extension the same module must land in the same slots, modulo
	// the "ext.<ext>." prefix that every extension key carries.
	const extKey = "ext.TestExt.Конфигурация.МодульСеанса.Модуль"
	if got := bslPathToModuleName("Расширения/TestExt/Ext/SessionModule.bsl"); got != extKey {
		t.Fatalf("bslPathToModuleName = %q, want %q", got, extKey)
	}
	if parsedExt := parseModuleName(extKey); parsedExt.module != configModuleSuffix {
		t.Errorf("parseModuleName(%q).module = %q, want %q", extKey, parsedExt.module, configModuleSuffix)
	}
	extPI := NewPathIndex([]string{extKey})
	if ids := extPI.FilterDocIDs("", configModuleSuffix); !slices.Contains(ids, extKey) {
		t.Errorf("FilterDocIDs(\"\", %q) = %v, want it to contain %q: an extension's configuration "+
			"module must answer the same module-type filter as the base-config one",
			configModuleSuffix, ids, extKey)
	}
}

// TestNoTwoSegmentKeysInCorpus proves the shape claim globally rather than for
// the four configuration modules alone: every path in keyDigestCorpus that a
// dump can actually produce — the corpus covers every branch of
// bslPathToModuleName — derives a key of at least three dot-separated segments,
// so no key can leave the module-type slot empty through the len(parts) >= 3
// gate in parseModuleName / PathIndex.AddEntry.
//
// rootOnlyPath is the one corpus entry outside that claim, and it is stated
// rather than quietly skipped. A .bsl sitting directly in the dump root has no
// directory segment, so bslPathToModuleName returns the relative path verbatim
// from its len(parts) < 2 early return, before any key is built. The dot in the
// result is the FILE EXTENSION, so a consumer splitting on "." does read it as
// two segments. No 1C dump lays a module out that way — the configuration's own
// modules live one level down, in the root Ext directory — and the corpus row
// exists to pin the early return, not a real shape.
func TestNoTwoSegmentKeysInCorpus(t *testing.T) {
	const rootOnlyPath = "Module.bsl"
	seenRootOnly := false
	for _, p := range keyDigestCorpus {
		key := bslPathToModuleName(p)
		if p == rootOnlyPath {
			seenRootOnly = true
			if key != rootOnlyPath {
				t.Errorf("%q -> %q, want the path back verbatim: the documented "+
					"no-directory early return changed shape", p, key)
			}
			continue
		}
		if n := len(strings.Split(key, ".")); n < 3 {
			t.Errorf("%q -> %q has %d segments, want >= 3: parseModuleName and "+
				"PathIndex.AddEntry leave the module type empty below three segments",
				p, key, n)
		}
	}
	if !seenRootOnly {
		t.Fatalf("corpus no longer contains %q; the exception carved out above is "+
			"unexercised and the loop may be skipping rows silently", rootOnlyPath)
	}

	// Positive control: the checker must go red on a key that really has two
	// segments, otherwise the green verdict above says nothing.
	if n := len(strings.Split("Конфигурация.МодульСеанса", ".")); n >= 3 {
		t.Fatalf("positive control is broken: a known two-segment key measured %d segments", n)
	}
}
