package dump

// This file broadens the analyze_subsystems ORPHANS universe (the object set
// action=orphans subtracts subsystem membership from) beyond the 15 applied kinds to
// the FULL set of top-level, independently-enumerable, Состав-eligible metadata kinds.
// Before this, the universe walked appliedFolderToRu (15 applied kinds) while
// membership (canonicalizeContentPath) recognised ~19 kinds, so a common module, role,
// defined type or constant that belonged to NO subsystem was silently never reported
// as an orphan even though `containing` recognised it. The universe is now consistent
// with membership by construction.
//
// SINGLE-SOURCE RU rule (what makes orphans set-cancel): every kind's universe Russian
// prefix is taken from the SAME table the membership canonicalizer uses -- for the
// applied 15 that is appliedFolderToRu (derived from metadataTypes), and for the extra
// kinds here it is serviceKindEnToRu (which also maps Constant -> Константа). So an
// object's universe string ("<Вид>.<Имя>") and its subsystem-membership string are
// byte-identical, which is exactly what makes orphans = universe - membership correct.
// If the two ever diverged, a distributed object would be miscounted as an orphan (or
// an out-of-subsystem object silently missed).
//
// The applied-kind tables (metadataTypes / appliedKindEnToRu / appliedKindRu /
// appliedFolderToRu) are deliberately NOT expanded: they also drive membership
// canonicalization and stay pinned at 15. This universe is a separate, additive
// derivation used only by the enumerator.

// universeKind describes one top-level, independently-enumerable, Состав-eligible
// metadata kind added to the orphans universe on top of the applied 15.
type universeKind struct {
	// singularEn is the English singular kind name and the key into serviceKindEnToRu
	// (the RU source of truth). The legacy singular dump folder equals this string.
	singularEn string
	// folderPlural is the modern dump kind-folder name (a 1C ConfigurationDump writes
	// each kind under this directory), e.g. "CommonModules".
	folderPlural string
	// bslCollection is the live-config Метаданные.<X> collection property the extension
	// ПодсистемыGET enumerates for this kind, e.g. "ОбщиеМодули". Recorded here so the
	// live BSL universe list and this Go table can be proven in sync
	// (subsystem_universe_bslsync_test.go).
	bslCollection string
}

// universeServiceKinds is the owner-approved set of top-level service / extra kinds
// included in the orphans universe on top of the applied 15. Константа is included
// (the owner reversed the earlier deliberate exclusion). Provenance for every row was
// grounded in source (No-Invention), not memory:
//   - RU prefix: serviceKindEnToRu (the same table membership uses; Constant included).
//   - Dump folder: the authoritative 1C EN->dir map (metadataTypeDirMap) and real dump
//     fixtures; XDTOPackages from the in-repo 1C syntax corpus.
//   - BSL collection: the 1C "ОбъектМетаданныхКонфигурация" syntax corpus collection
//     properties, matching the extension's own ИменаКоллекций where present.
//
// Splits worth noting (do NOT "fix" them): ScheduledJob's dump folder / BSL collection
// is РегулярныеЗадания (the extension's existing choice) while its .ПолноеИмя() prefix
// is РегламентноеЗадание; WebService's BSL collection is Cyrillic ВебСервисы while its
// RU prefix is Latin WebСервис. Both are carried through unchanged so universe and
// membership stay byte-identical.
var universeServiceKinds = []universeKind{
	{"Constant", "Constants", "Константы"},
	{"CommonModule", "CommonModules", "ОбщиеМодули"},
	{"CommonForm", "CommonForms", "ОбщиеФормы"},
	{"CommonCommand", "CommonCommands", "ОбщиеКоманды"},
	{"CommandGroup", "CommandGroups", "ГруппыКоманд"},
	{"CommonTemplate", "CommonTemplates", "ОбщиеМакеты"},
	{"CommonPicture", "CommonPictures", "ОбщиеКартинки"},
	{"DefinedType", "DefinedTypes", "ОпределяемыеТипы"},
	{"HTTPService", "HTTPServices", "HTTPСервисы"},
	{"WebService", "WebServices", "ВебСервисы"},
	{"XDTOPackage", "XDTOPackages", "ПакетыXDTO"},
	{"SessionParameter", "SessionParameters", "ПараметрыСеанса"},
	{"ScheduledJob", "ScheduledJobs", "РегулярныеЗадания"},
	{"Role", "Roles", "Роли"},
	{"FunctionalOption", "FunctionalOptions", "ФункциональныеОпции"},
	{"EventSubscription", "EventSubscriptions", "ПодпискиНаСобытия"},
	{"DocumentJournal", "DocumentJournals", "ЖурналыДокументов"},
	{"SettingsStorage", "SettingsStorages", "ХранилищаНастроек"},
}

// nonUniverseServiceKinds lists every serviceKindEnToRu key deliberately KEPT OUT of
// the orphans universe, each with the reason. It exists so the classification invariant
// (TestUniverse_ClassifiesEveryServiceKind) can assert EVERY service kind is accounted
// for -- included in universeServiceKinds or excluded here -- so a future serviceKindEnToRu
// addition cannot be silently forgotten from the universe (the exact bug class this fix
// closes). Subsystem is the container itself, never a member object; the remaining kinds
// are top-level but were NOT in the owner-approved universe set and their Состав
// eligibility could not be confirmed from local sources, so they are excluded pending an
// owner decision rather than guessed into the universe.
var nonUniverseServiceKinds = map[string]string{
	"Subsystem":         "the container itself, not a member object",
	"WSReference":       "not in the owner-approved universe set; Состав eligibility unconfirmed locally",
	"Style":             "not in the owner-approved universe set; Состав eligibility unconfirmed locally",
	"Language":          "not in the owner-approved universe set; Состав eligibility unconfirmed locally",
	"CommonAttribute":   "not in the owner-approved universe set; Состав eligibility unconfirmed locally",
	"Sequence":          "not in the owner-approved universe set; Состав eligibility unconfirmed locally",
	"FilterCriterion":   "not in the owner-approved universe set; Состав eligibility unconfirmed locally",
	"DocumentNumerator": "not in the owner-approved universe set; Состав eligibility unconfirmed locally",
}

// universeFolderToRu maps a dump kind-folder name (both the modern plural AND the legacy
// singular spelling) to the canonical Russian prefix an object of that kind receives in
// BOTH the universe and membership. It is appliedFolderToRu (the applied 15) PLUS every
// kind in universeServiceKinds. It is what EnumerateAppliedObjects walks.
var universeFolderToRu map[string]string

// universeRuKinds is the set of Russian prefixes the universe can enumerate (the values
// of universeFolderToRu). The coverage diagnostic uses it to tell whether a kind
// referenced by subsystem membership is one the universe is supposed to cover.
var universeRuKinds map[string]bool

func init() {
	universeFolderToRu = make(map[string]string, len(appliedFolderToRu)+len(universeServiceKinds)*2)
	// Applied 15 (appliedFolderToRu already keys plural + legacy singular).
	for folder, ru := range appliedFolderToRu {
		universeFolderToRu[folder] = ru
	}
	// Constant + the service kinds, keyed by BOTH the modern plural and the legacy
	// singular folder (a ConfigurationDump may use either), RU taken from the SAME table
	// membership uses.
	for _, k := range universeServiceKinds {
		ru := universeKindRu(k.singularEn)
		universeFolderToRu[k.folderPlural] = ru
		universeFolderToRu[k.singularEn] = ru // legacy singular folder
	}
	universeRuKinds = make(map[string]bool, len(universeFolderToRu))
	for _, ru := range universeFolderToRu {
		universeRuKinds[ru] = true
	}
}

// universeKindRu returns the canonical Russian prefix for a universe kind's English
// singular from serviceKindEnToRu, the SAME source canonicalizeContentPath uses for
// service kinds (and for Constant, which serviceKindEnToRu also maps to Константа). It
// panics at package init if a kind is unmapped, so a table typo fails the build instead
// of silently enumerating a prefix membership can never produce.
func universeKindRu(singularEn string) string {
	if ru, ok := serviceKindEnToRu[singularEn]; ok {
		return ru
	}
	panic("dump: universe kind " + singularEn + " has no serviceKindEnToRu mapping")
}

// membershipKindPrefix returns the kind segment before the first dot of a canonical full
// name, e.g. "ОбщийМодуль.X" -> "ОбщийМодуль". Empty when there is no dot.
func membershipKindPrefix(full string) string {
	for i := 0; i < len(full); i++ {
		if full[i] == '.' {
			return full[:i]
		}
	}
	return ""
}

// membershipKinds collects the set of canonical Russian kind prefixes referenced by the
// Состав of every subsystem in the tree (recursively). It is the evidence the coverage
// diagnostic uses: a kind that appears here is one the configuration actually uses in a
// subsystem, so its universe folder is expected to be present in a complete dump.
func membershipKinds(subs []Subsystem) map[string]bool {
	kinds := make(map[string]bool)
	var walk func([]Subsystem)
	walk = func(ss []Subsystem) {
		for _, s := range ss {
			for _, c := range s.Content {
				if p := membershipKindPrefix(c); p != "" {
					kinds[p] = true
				}
			}
			walk(s.Children)
		}
	}
	walk(subs)
	return kinds
}
