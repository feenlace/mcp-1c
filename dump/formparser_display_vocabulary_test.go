package dump

import (
	"sort"
	"testing"
)

// SOURCE, AND AT WHICH SHA: mcp-1c-advanced, tag v2.38.0, commit
// efbe9cebe338c8b40f07e41121dbd56ef908cdf9, read with `git show` and never
// vendored or imported here - this repository must not take a build or
// runtime dependency on that corpus, so the vocabulary below is a hand
// transcription, not a fetch. It is drawn from:
//
//   - docs/1c-syntax/types/ВидГруппыФормы.md             FormGroupType,       8 members
//   - docs/1c-syntax/types/ВидПоляФормы.md               FormFieldType,      20 members
//   - docs/1c-syntax/types/ВидКнопкиФормы.md             FormButtonType,      4 members
//   - docs/1c-syntax/types/ВидДекорацииФормы.md          FormDecorationType,  2 members
//   - docs/1c-syntax/types/ВидТаблицыФормы.md             FormTableType,       3 members (mobile client only)
//   - docs/1c-syntax/types/ВидДополненияЭлементаФормы.md FormItemAdditionType, 3 members
//   - docs/1c-syntax/types/{ГруппаФормы,ПолеФормы,ТаблицаФормы,КнопкаФормы,ДекорацияФормы}.md
//     the five base element types the six enumerations above describe.
//   - internal/syntaxdata/source/types.json, the record with name=="Кнопка"
//     and nameEn=="Button": a platform control type in its own right,
//     standing apart from the base type КнопкаФормы (FormButton) the way
//     Надпись and Картинка stand apart from ДекорацияФормы. This repo's own
//     pre-existing TestDisplayType already expected "Кнопка" for the XML tag
//     "Button" before this test was written, which is independent evidence
//     for the same conclusion.
//
// Two entries the map already carried, Table -> "ТаблицаФормы" and
// Button -> "Кнопка", resolve against this vocabulary unchanged: both are
// base or standalone platform type names, not members of a Вид enumeration. They are not touched here.
//
// NumberField has NO ENGLISH-NAMED COUNTERPART ANYWHERE IN THE CORPUS: FormFieldType
// (ВидПоляФормы) enumerates 20 field kinds and none of them is a numeric one, and
// neither "NumberField" nor "ПолеЧисла" occurs anywhere in types.json or in
// docs/1c-syntax/types/ (git grep at the sha above, case-insensitive, rc 1 for
// both, with "ПолеФлажка" as a firing control returning rc 0 in the same
// files). A numeric edit box is, at the level the platform names a field's
// kind, simply an input field: the correct value is "ПолеВвода", the same
// value InputField already carries.
func TestElementTypeDisplayNamesResolveToPlatformVocabulary(t *testing.T) {
	// platformElementVocabulary is the literal transcription described above.
	// It is a SET (map to struct{}) rather than a slice for the same reason
	// the map under test is a map: membership, not order or count, is what
	// this test asserts.
	platformElementVocabulary := map[string]struct{}{
		// Base element types (5).
		"ГруппаФормы":    {},
		"ПолеФормы":      {},
		"ТаблицаФормы":   {},
		"КнопкаФормы":    {},
		"ДекорацияФормы": {},

		// ВидГруппыФормы / FormGroupType (8).
		"ГруппаКнопок":    {},
		"ГруппаКолонок":   {},
		"КоманднаяПанель": {},
		"КонтекстноеМеню": {},
		"ОбычнаяГруппа":   {},
		"Подменю":         {},
		"Страница":        {},
		"Страницы":        {},

		// ВидПоляФормы / FormFieldType (20).
		"ПолеHTMLДокумента":             {},
		"ПолеPDFДокумента":              {},
		"ПолеВвода":                     {},
		"ПолеГеографическойСхемы":       {},
		"ПолеГрафическойСхемы":          {},
		"ПолеДендрограммы":              {},
		"ПолеДиаграммы":                 {},
		"ПолеДиаграммыГанта":            {},
		"ПолеИндикатора":                {},
		"ПолеКалендаря":                 {},
		"ПолеКартинки":                  {},
		"ПолеНадписи":                   {},
		"ПолеПереключателя":             {},
		"ПолеПериода":                   {},
		"ПолеПланировщика":              {},
		"ПолеПолосыРегулирования":       {},
		"ПолеТабличногоДокумента":       {},
		"ПолеТекстовогоДокумента":       {},
		"ПолеФлажка":                    {},
		"ПолеФорматированногоДокумента": {},

		// ВидКнопкиФормы / FormButtonType (4).
		"Гиперссылка":                {},
		"ГиперссылкаКоманднойПанели": {},
		"КнопкаКоманднойПанели":      {},
		"ОбычнаяКнопка":              {},

		// ВидДекорацииФормы / FormDecorationType (2).
		"Картинка": {},
		"Надпись":  {},

		// ВидТаблицыФормы / FormTableType (3). Unused by the map today; kept
		// because the brief that sourced this vocabulary names the enum, and a
		// vocabulary that quietly narrows itself to only today's values would
		// stop being a check against the platform.
		"Авто":     {},
		"Карточки": {},
		"Список":   {},

		// ВидДополненияЭлементаФормы / FormItemAdditionType (3). Also unused
		// by the map today, kept for the same reason.
		"ОтображениеСостоянияПросмотра": {},
		"ОтображениеСтрокиПоиска":       {},
		"УправлениеПоиском":             {},

		// Standalone platform type outside the six enumerations above; see the
		// Button paragraph in the doc comment.
		"Кнопка": {},
	}

	// ANTI-VACUITY, PART ONE: an empty map or an empty vocabulary would make
	// every assertion below trivially pass without checking anything.
	if len(elementTypeDisplayName) == 0 {
		t.Fatal("control failed: elementTypeDisplayName is empty, so this test checks nothing")
	}
	if len(platformElementVocabulary) == 0 {
		t.Fatal("control failed: platformElementVocabulary is empty, so this test checks nothing")
	}

	// ANTI-VACUITY, PART TWO: a FIRING control. The vocabulary must reject the
	// exact bytes of the four spellings this test exists to catch, or a
	// membership check that always answers true would pass every assertion
	// below without having checked anything. None of these five strings is a
	// real platform name.
	for _, bad := range []string{
		"ФлажокПоле",         // pre-fix CheckBoxField value
		"ДекорацияНадпись",   // pre-fix LabelDecoration value
		"ПолеЧисла",          // pre-fix NumberField value
		"ДекорацияКартинка",  // pre-fix PictureDecoration value
		"НесуществующийВид▮", // never a real name; not even close to one
	} {
		if _, ok := platformElementVocabulary[bad]; ok {
			t.Fatalf("control failed: %q is a known-wrong spelling and must not be in "+
				"platformElementVocabulary, or nothing below can ever fail", bad)
		}
	}

	// THE ASSERTION. Sorted for a stable, readable failure list: map iteration
	// order is not.
	tags := make([]string, 0, len(elementTypeDisplayName))
	for tag := range elementTypeDisplayName {
		tags = append(tags, tag)
	}
	sort.Strings(tags)

	for _, tag := range tags {
		name := elementTypeDisplayName[tag]
		if _, ok := platformElementVocabulary[name]; !ok {
			t.Errorf("elementTypeDisplayName[%q] = %q, which is not a member of any of the "+
				"six managed-form element-kind enumerations nor one of the five base element "+
				"types (see the vocabulary and its source in this test's doc comment). Find the "+
				"correct platform spelling in the mcp-1c-advanced corpus before changing this.",
				tag, name)
		}
	}
	t.Logf("checked %d elementTypeDisplayName entries against a %d-term platform vocabulary",
		len(tags), len(platformElementVocabulary))
}
