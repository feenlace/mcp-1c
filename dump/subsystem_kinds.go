package dump

import "strings"

// This file canonicalises 1C metadata kind prefixes for the offline dump-backed
// subsystem reader. A configuration dump writes each subsystem member (Состав)
// and each object folder with an ENGLISH kind prefix (e.g. "Document.X",
// "CommonModule.Y"), while the live 1C extension returns the same member in
// Russian via the platform full name (".ПолноеИмя()"): "Документ.X",
// "ОбщийМодуль.Y". Rendering both sides in one canonical Russian spelling is what
// makes the offline path byte-parity with the live path, and (critically) what
// makes analyze_subsystems orphans set-cancel: an applied object's universe
// string and its subsystem-membership string are identical by construction
// because both derive their Russian prefix from the same tables here.

// appliedKindEnToRu maps the English singular of each APPLIED metadata kind to its
// canonical Russian singular prefix. Derived from metadataTypes (the single source
// of truth for kind names in this package) MINUS Constant: the live extension does
// not count constants as applied objects for subsystem membership, so Constant is
// excluded from the orphans universe.
var appliedKindEnToRu map[string]string

// appliedKindRu is the set of Russian singular prefixes for the applied kinds. It
// recognises whether a qualified name's kind belongs to the orphans universe.
var appliedKindRu map[string]bool

// appliedFolderToRu maps a dump kind-folder name to the applied kind's Russian
// prefix, keyed by BOTH the plural English folder (e.g. "Documents", the modern
// layout) and the legacy singular English folder (e.g. "Document"), so the
// universe enumerator can walk either on-disk layout. Constant is absent.
var appliedFolderToRu map[string]string

func init() {
	appliedKindEnToRu = make(map[string]string, len(metadataTypes))
	appliedKindRu = make(map[string]bool, len(metadataTypes))
	appliedFolderToRu = make(map[string]string, len(metadataTypes)*2)
	for _, mt := range metadataTypes {
		if mt.SingularEng == "Constant" {
			continue // not an applied object for subsystem membership
		}
		appliedKindEnToRu[mt.SingularEng] = mt.RussianName
		appliedKindRu[mt.RussianName] = true
		appliedFolderToRu[mt.PluralEng] = mt.RussianName   // e.g. Documents -> Документ
		appliedFolderToRu[mt.SingularEng] = mt.RussianName // legacy singular folder
	}
}

// serviceKindEnToRu maps the English singular name of a SERVICE metadata kind (a
// kind that is not an applied/table object) to its canonical Russian singular
// form as emitted by the 1C platform full name.
//
// It does NOT affect orphans (the applied-object universe excludes service kinds);
// it exists so subsystem Content and analyze_subsystems containing/intersections
// render service members in Russian instead of leaking the dump's English prefix.
//
// DocumentNumerator maps to НумераторДокументов, the form the platform full name
// emits on a real base (live-verified). The shorter kind singular is deliberately
// NOT used and this must not regress.
//
// The five Common-typed kinds (CommonModule, CommonForm, CommonCommand,
// CommonTemplate, CommonPicture) are folded into this one table; there is no key
// overlap with the other service kinds.
var serviceKindEnToRu = map[string]string{
	// Common-typed kinds.
	"CommonModule":   "ОбщийМодуль",
	"CommonForm":     "ОбщаяФорма",
	"CommonCommand":  "ОбщаяКоманда",
	"CommonTemplate": "ОбщийМакет",
	"CommonPicture":  "ОбщаяКартинка",
	// Other service kinds.
	"Role":              "Роль",
	"HTTPService":       "HTTPСервис",
	"XDTOPackage":       "ПакетXDTO",
	"DefinedType":       "ОпределяемыйТип",
	"FunctionalOption":  "ФункциональнаяОпция",
	"EventSubscription": "ПодпискаНаСобытие",
	"WebService":        "WebСервис",
	"WSReference":       "WSСсылка",
	"ScheduledJob":      "РегламентноеЗадание",
	"SettingsStorage":   "ХранилищеНастроек",
	"Style":             "Стиль",
	"Language":          "Язык",
	"CommonAttribute":   "ОбщийРеквизит",
	"SessionParameter":  "ПараметрСеанса",
	"Sequence":          "Последовательность",
	"FilterCriterion":   "КритерийОтбора",
	"DocumentNumerator": "НумераторДокументов",
	"CommandGroup":      "ГруппаКоманд",
	// Document journals: structural, not an applied orphan kind, but a valid
	// subsystem Content member. The Russian singular ЖурналДокументов matches the
	// canonical name recorded in metadata_types.go for the DocumentJournals folder.
	"DocumentJournal": "ЖурналДокументов",
}

// serviceKindEnLower is the lowercased index of serviceKindEnToRu for
// case-insensitive lookup, built once at package init.
var serviceKindEnLower map[string]string

func init() {
	serviceKindEnLower = make(map[string]string, len(serviceKindEnToRu))
	for en, ru := range serviceKindEnToRu {
		serviceKindEnLower[strings.ToLower(en)] = ru
	}
}

// ServiceKindNameRu resolves the English singular name of a SERVICE metadata kind
// to its canonical Russian singular form. The lookup is case-insensitive. It
// returns ("", false) for the applied kinds (resolve those via appliedKindEnToRu)
// and for any unknown prefix, so a caller can fall through and preserve the input.
func ServiceKindNameRu(nameEn string) (string, bool) {
	ru, ok := serviceKindEnLower[strings.ToLower(nameEn)]
	return ru, ok
}

// canonicalizeContentPath converts a 1C metadata reference like
// "Document.РеализацияТоваров" or "CommonModule.X" into the canonical Russian form
// ("Документ.РеализацияТоваров", "ОбщийМодуль.X").
//
// It NFC-normalises first so an object name authored in decomposed (NFD) form in a
// macOS-unpacked dump matches the NFC universe key (without this, such an object
// would show up as a false orphan). An input with no "." is dropped (returns "").
// An unknown prefix is returned unchanged so dump fidelity is preserved.
func canonicalizeContentPath(raw string) string {
	raw = NFC(raw)
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	prefix, name := parts[0], parts[1]
	if ru, ok := appliedKindEnToRu[prefix]; ok {
		return ru + "." + name
	}
	if appliedKindRu[prefix] {
		return prefix + "." + name // already a canonical applied RU prefix
	}
	if ru, ok := ServiceKindNameRu(prefix); ok {
		return ru + "." + name
	}
	return raw
}
