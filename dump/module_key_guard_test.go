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
// Guard 1b: the table must also cover the kinds a configuration DECLARES, not
// only the kinds that were caught holding a module.
//
// The fixture behind guard 1 records directories observed holding .bsl files. It
// is the right question for "did a module lose its Russian prefix" and the wrong
// one for "does this package know what a dump root looks like": of the 41 kinds
// the measured configuration declares, 23 hold at least one .bsl in that dump and
// EIGHTEEN hold none at all. A role, a style, a subsystem, a language and a
// picture have no module by construction, so no module-census can ever put them in
// the table, and every one of them is still a legitimate top-level directory of a
// real dump.
//
// That gap is what this guard closes, and it is load-bearing for the root
// detection and the anchor scan rather than for key derivation: dumpRootMarker
// reads dumpDirNames, so a kind the table does not know is a kind no wrapper can
// be anchored past and no directory can be recognised by.
// ---------------------------------------------------------------------------

// configChildObjectDirsFixture pairs each kind a real configuration declares in
// <ChildObjects> with the dump directory that holds it. Both columns are measured;
// see the header of the file itself for provenance.
const configChildObjectDirsFixture = "testdata/config_child_object_dirs.txt"

// configChildKindCount is the number of distinct kinds that manifest declares. It
// is pinned so a fixture truncated to nothing, or silently halved, cannot pass as
// full coverage: readConfigChildObjectDirs already refuses an empty parse, and
// this catches every partial one above it.
const configChildKindCount = 41

// readConfigChildObjectDirs parses the fixture into dir -> singular-kind pairs,
// dropping blank lines and comments.
func readConfigChildObjectDirs(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.FromSlash(configChildObjectDirsFixture))
	if err != nil {
		t.Fatalf("open fixture %s: %v", configChildObjectDirsFixture, err)
	}
	defer f.Close()

	pairs := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			t.Fatalf("fixture %s: line %q has %d fields, want <ИмяКаталога> <ВидИзМанифеста>",
				configChildObjectDirsFixture, line, len(fields))
		}
		pairs[fields[0]] = fields[1]
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if len(pairs) != configChildKindCount {
		t.Fatalf("fixture %s parsed to %d entries, want %d; the parser or the file is broken",
			configChildObjectDirsFixture, len(pairs), configChildKindCount)
	}
	return pairs
}

// TestDumpDirNamesCoversDeclaredChildObjectKinds fails when a kind a real
// configuration declares has no dumpDirNames entry.
func TestDumpDirNamesCoversDeclaredChildObjectKinds(t *testing.T) {
	pairs := readConfigChildObjectDirs(t)
	dirs := make([]string, 0, len(pairs))
	for d := range pairs {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	if missing := uncoveredDirs(dumpDirNames, dirs); len(missing) > 0 {
		t.Errorf("dumpDirNames has no entry for declared configuration child kinds %v.\n"+
			"Each of them is a legitimate top-level directory of a dump, so without an entry "+
			"dumpRootMarker does not recognise it and a dump whose top directory is one of "+
			"them cannot be anchored past a wrapper. Add the Russian name to metadata_types.go.",
			missing)
	}

	// Positive control on the same checker, with a known-present entry removed.
	const control = "Catalogs"
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
	if got := uncoveredDirs(damaged, dirs); !slices.Equal(got, []string{control}) {
		t.Fatalf("positive control failed: with %q removed the checker reported %v, want exactly [%s]",
			control, got, control)
	}
}

// TestDumpDirRussianNamesMatchTheKindTables is the guard against the one failure
// this work could not test by running it: a WRONG Russian name.
//
// A missing entry is loud (the key carries a raw English prefix that no resolver
// queries). A wrong Russian name is silent — the key looks perfectly ordinary and
// is simply never the one anybody asks for — and it reaches the customer, because
// the prefix is the visible half of every module key the server prints.
//
// So no Russian name added for a kind is written from knowledge. Each one must be
// the string this package ALREADY uses for that kind elsewhere: appliedKindEnToRu
// for the applied kinds, serviceKindEnToRu for the service ones. Those tables are
// what canonicalises subsystem membership against the live 1C platform full name,
// so agreeing with them is agreeing with the platform.
//
// Bots is the single documented exception and is asserted separately below.
func TestDumpDirRussianNamesMatchTheKindTables(t *testing.T) {
	pairs := readConfigChildObjectDirs(t)

	checked := 0
	for dir, kind := range pairs {
		want, ok := appliedKindEnToRu[kind]
		if !ok {
			want, ok = ServiceKindNameRu(kind)
		}
		if !ok {
			t.Errorf("kind %q (directory %q) has no Russian name in either appliedKindEnToRu or "+
				"serviceKindEnToRu, so dumpDirNames[%q] cannot be cross-checked against anything "+
				"and would be a name somebody typed", kind, dir, dir)
			continue
		}
		got, ok := dumpDirNames[dir]
		if !ok {
			continue // reported by the coverage test above
		}
		checked++
		if got != want {
			t.Errorf("dumpDirNames[%q] = %q but this package renders kind %q as %q everywhere else. "+
				"A prefix that disagrees with the subsystem tables is a key no resolver ever asks for.",
				dir, got, kind, want)
		}
	}
	if checked != configChildKindCount {
		t.Fatalf("only %d of %d kinds were actually compared; the loop is skipping rows and its "+
			"green verdict means nothing", checked, configChildKindCount)
	}

	// Positive control: the comparison must reject a name that is merely plausible.
	if want, _ := ServiceKindNameRu("Style"); want == "Стили" {
		t.Fatal("positive control is broken: the plural was expected to differ from the singular")
	}
}

// TestBotsIsTheOneDerivedRussianName states, in the tree, the one Russian name in
// dumpDirNames that is NOT copied from another table in this package.
//
// The repository knows the collection property «Боты» — it is in
// testdata/config_metadata_properties.txt, the snapshot of the platform type
// ОбъектМетаданныхКонфигурация that configModuleNames also draws its four names
// from. It does not know the singular anywhere, because no subsystem table lists a
// bot. The singular is therefore derived by the rule this package already applies
// and documents in subsystem_kinds.go, where ЭлементыСтиля gives ЭлементСтиля and
// ВнешниеИсточникиДанных gives ВнешнийИсточникДанных.
//
// The test exists so the derivation is visible rather than buried, and so that
// anyone who obtains the real platform full name has one place to correct.
func TestBotsIsTheOneDerivedRussianName(t *testing.T) {
	const dir = "Bots"
	got, ok := dumpDirNames[dir]
	if !ok {
		t.Fatalf("dumpDirNames has no %q entry", dir)
	}
	if got != "Бот" {
		t.Errorf("dumpDirNames[%q] = %q, want %q", dir, got, "Бот")
	}
	// The plural it is derived FROM must really be in the snapshot; without that
	// the derivation has no source at all.
	const snapshot = "testdata/config_metadata_properties.txt"
	data, err := os.ReadFile(filepath.FromSlash(snapshot))
	if err != nil {
		t.Fatalf("open %s: %v", snapshot, err)
	}
	if !slices.Contains(strings.Split(strings.TrimSpace(string(data)), "\n"), "Боты") {
		t.Fatalf("%s no longer lists «Боты», so the singular in dumpDirNames is derived from "+
			"nothing; re-take the snapshot or drop the entry", snapshot)
	}
	// And it is genuinely absent from the kind tables, or it would not be an
	// exception and this test would be describing a state that no longer holds.
	if _, ok := ServiceKindNameRu("Bot"); ok {
		t.Fatalf("serviceKindEnToRu now carries Bot; fold %q into the cross-check in "+
			"TestDumpDirRussianNamesMatchTheKindTables and delete this exception", dir)
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
	// The unknown-category fallback. It used to be the Styles row above, which the
	// completed dumpDirNames turned into a KNOWN kind; both are kept, one line
	// apart, so the corpus covers the branch on either side of the table.
	"ExternalDataProcessors/Основной/Ext/Module.bsl",
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
	// A wrapper above a kind that became a root marker only when dumpDirNames was
	// completed. Before that this row anchored at 0 and its key was
	// "wrapper.Styles.МодульФормы": the wrapper was read as the kind and the kind as
	// the object. It covers ONE of the added entries, Styles, and claims no more
	// than that; what it buys is that removing that entry from the table shows up as
	// a moved key here rather than only as one fewer name in a coverage count.
	"wrapper/Styles/Основной/Ext/Module.bsl",
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
// It held 7134dd1c… from before the anchor scan existed until dumpDirNames was
// completed, which is the whole history of the claim it carries: the scan was
// added and this number did not move.
//
// IT MOVES NOW, AND THE MOVE IS THE FINDING RATHER THAN AN INCONVENIENCE. Two
// things reached it at once and they are different in kind, so both are named:
//
//   - ONE EXISTING ROW RE-KEYED. Completing dumpDirNames turns
//     "Styles/Основной/Ext/Module.bsl" from "Styles.Основной.МодульФормы" into
//     "Стиль.Основной.МодульФормы". The other 38 rows are byte for byte unchanged,
//     verified row by row rather than inferred from the digest moving. Under the
//     BUMP PROTOCOL this is precisely the event that forces dumpIndexSchemaVersion
//     up, and it is up: 3 -> 4.
//   - ONE ROW WAS ADDED. Styles was this corpus's unknown-category row, and it
//     stopped being unknown, so ExternalDataProcessors was added to keep that
//     branch covered. A digest that moved only because a row appeared would prove
//     nothing about derivation, which is why the two causes are separated here
//     instead of being summed into one sentence.
//
// What the moved row does NOT mean is that any real dump re-keys. Styles holds no
// .bsl at all, and neither do the seventeen other kinds this change took from that
// manifest, measured on the dump they were enumerated from, so the row is a
// synthetic probe of the table and not a shape anybody has on disk. (Bots is
// outside that measurement by construction: it is not in that manifest.) The bump is justified by the wrongly-rooted
// user whose PERSISTED DocIDs would otherwise replay the collapsed keys, which is
// argued where it belongs, at dumpIndexSchemaVersion in generation.go.
const bslUnwrappedCorpusDigest = "7e0d5d125153d6af3e6601479212439c7034a87e17225df9edef8ea829809fac"

const (
	bslKeyCorpusDigest              = "0803e4ed74354c08f4606b0fbf4599ac82078508bcf97cb66a7fd63941246351"
	pinnedSchemaVersionForKeyDigest = 4
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
