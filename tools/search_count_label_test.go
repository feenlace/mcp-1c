package tools

import (
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// The customer's SECOND report. He searched «Процедура» with limit 500 and read
// a count of 2150 off the header, then found line numbers that never went past
// twenty. Both halves of that come from this file's subject: the header said
// «N совпадений» while N was the number of MODULES, and smart shows one line per
// module, so the line numbers he saw were first occurrences and not the
// occurrences he was looking for.
//
// MEASURED on dumps/dump_bsl (13575 files, 13575 distinct keys), one query
// «Процедура», limit 500:
//
//	smart  total=11788   returned=500  distinct modules=500  max line=17
//	regex  total=203718  returned=500  distinct modules=36   max line=4228
//	exact  total=204795  returned=500  distinct modules=36   max line=4228
//
// Three numbers, one label. 11788 is modules; 203718 and 204795 are lines.
//
// A sentence here used to add that the smart max line above is the customer's
// «максимум 19-20» EXACTLY. It is deleted rather than repaired: the measured value
// in the table is neither of the numbers in his phrase, so «exactly» claimed an
// agreement they do not have. tools/search.go states the weaker thing that is true
// of both, that the count never went past twenty.

// searchModeNouns is what each mode's header must call its own number, and what
// it must NOT call it. Written out per mode rather than derived, so a change to
// the mapping has to be made here too and cannot slip through as a refactor.
var searchModeNouns = map[dump.SearchMode]struct{ want, forbid string }{
	dump.SearchModeSmart: {want: "модулей", forbid: "строк"},
	dump.SearchModeRegex: {want: "строк", forbid: "модулей"},
	dump.SearchModeExact: {want: "строк", forbid: "модулей"},
}

// TestSearchHeaderNamesWhatItCounted is the header pin: two different quantities
// may not carry one label.
func TestSearchHeaderNamesWhatItCounted(t *testing.T) {
	matches := []dump.Match{{Module: "ОбщийМодуль.Тест.Модуль", Line: 1, Context: "Процедура Тест()"}}

	headers := map[dump.SearchMode]string{}
	for mode, nouns := range searchModeNouns {
		text := FormatSearchResultWithStats(matches,
			dump.SearchStats{Total: 7, Unit: dump.SearchUnitFor(mode)}, "Процедура", mode, nil)
		header, _, _ := strings.Cut(text, "\n")
		headers[mode] = header

		if !strings.Contains(header, nouns.want) {
			t.Errorf("mode %s: header does not say what it counted (%q missing): %q",
				mode, nouns.want, header)
		}
		if strings.Contains(header, nouns.forbid) {
			t.Errorf("mode %s: header calls its number %q, which is what the OTHER modes count: %q",
				mode, nouns.forbid, header)
		}
		if !strings.Contains(header, "7") {
			t.Errorf("mode %s: header lost the count entirely: %q", mode, header)
		}
	}

	// The whole defect in one comparison: the same number rendered by two modes
	// used to produce byte-identical headers. Modules and lines are not the same
	// quantity, so the two headers must not read the same.
	if headers[dump.SearchModeSmart] == headers[dump.SearchModeRegex] {
		t.Errorf("smart and regex render the same header for the same number, so the "+
			"reader cannot tell modules from lines: %q", headers[dump.SearchModeSmart])
	}
}

// TestSearchShortfallNoteNamesTheSameUnitAsTheHeader keeps the footer from
// drifting back to the shared word once the header stops using it. The header
// and the «Показано X из Y» line are the two places the number is printed, and a
// reader who sees «модулей» above and «совпадений» below has learnt nothing.
func TestSearchShortfallNoteNamesTheSameUnitAsTheHeader(t *testing.T) {
	matches := []dump.Match{{Module: "ОбщийМодуль.Тест.Модуль", Line: 1, Context: "Процедура Тест()"}}

	for mode, nouns := range searchModeNouns {
		text := FormatSearchResultWithStats(matches,
			dump.SearchStats{Total: 150, Unit: dump.SearchUnitFor(mode)}, "Процедура", mode, nil)
		note, ok := lastQuotedLine(text)
		if !ok {
			t.Fatalf("mode %s: the answer carries no shortfall note at all:\n%s", mode, text)
		}
		if !strings.Contains(note, "Показано") {
			t.Fatalf("mode %s: last quoted line is not the shortfall note: %q", mode, note)
		}
		if !strings.Contains(note, nouns.want) {
			t.Errorf("mode %s: shortfall note does not name the unit (%q missing): %q",
				mode, nouns.want, note)
		}
		if strings.Contains(note, nouns.forbid) {
			t.Errorf("mode %s: shortfall note names the OTHER mode's unit (%q): %q",
				mode, nouns.forbid, note)
		}
	}
}

// lastQuotedLine returns the final «> ...» line of a rendered answer.
func lastQuotedLine(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "> ") {
			return lines[i], true
		}
	}
	return "", false
}

// TestSmartHitSaysHowManyLinesOfTheModuleMatched is the other half of the
// customer's report. Smart ranks modules and shows ONE line from each, so the
// definition on line 1198 of a module whose first hit is on line 199 is not in
// the answer and nothing said so. The count travels with the hit, and the route
// to the rest is named.
func TestSmartHitSaysHowManyLinesOfTheModuleMatched(t *testing.T) {
	many := []dump.Match{{
		Module: "ОбщийМодуль.Много.Модуль", Line: 199, Context: "Процедура Один()",
		Score: 1.5, LinesMatched: 42,
	}}
	text := FormatSearchResultWithStats(many,
		dump.SearchStats{Total: 1, Unit: dump.SearchUnitModules}, "Процедура", dump.SearchModeSmart, nil)

	heading, ok := firstHeadingLine(text)
	if !ok {
		t.Fatalf("no match heading rendered:\n%s", text)
	}
	// The whole labelled phrase, not the bare number: the heading already carries
	// «строка 199» and a «score: 1.500», so searching for "42" alone would be an
	// assertion that could pass on a heading that never mentioned the count.
	if !strings.Contains(heading, "строк с совпадениями в модуле: 42") {
		t.Errorf("the heading does not say how many lines of this module carry the query: %q", heading)
	}
	if !strings.Contains(text, "exact") || !strings.Contains(text, "regex") {
		t.Errorf("nothing names the modes that DO return every line:\n%s", text)
	}

	// A module whose only matching line is the one shown has nothing further to
	// offer, so neither the per-hit count nor the pointer appears. Without this
	// direction a formatter that printed the sentence unconditionally would pass
	// the assertions above while making every ordinary answer noisier.
	one := []dump.Match{{
		Module: "ОбщийМодуль.Один.Модуль", Line: 3, Context: "Процедура Один()",
		Score: 1.5, LinesMatched: 1,
	}}
	quiet := FormatSearchResultWithStats(one,
		dump.SearchStats{Total: 1, Unit: dump.SearchUnitModules}, "Процедура", dump.SearchModeSmart, nil)
	if strings.Contains(quiet, "строк с совпадениями в модуле") {
		t.Errorf("a module with one matching line still advertises a count of them:\n%s", quiet)
	}
	if strings.Contains(quiet, "regex") {
		t.Errorf("a complete answer still points at another mode:\n%s", quiet)
	}
}

// firstHeadingLine returns the first «### ...» line of a rendered answer.
func firstHeadingLine(text string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "### ") {
			return line, true
		}
	}
	return "", false
}

// TestSearchCountLabelsCarryNoDash scans every sentence this change introduced,
// per codepoint. A grep-based dash scan returned a false negative on this branch
// once already, so the scan is over runes and the control fires first.
func TestSearchCountLabelsCarryNoDash(t *testing.T) {
	dashes := []rune{'\u2014', '\u2013', '\u2012', '\u2015', '\u2212'}

	// POSITIVE CONTROL: the scan below finds a dash when there is one to find.
	control := "текст \u2014 продолжение"
	found := false
	for _, r := range control {
		for _, d := range dashes {
			if r == d {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("control failed: the per-codepoint scan did not see U+2014 in a string that carries it")
	}

	many := []dump.Match{{
		Module: "ОбщийМодуль.Много.Модуль", Line: 199, Context: "Процедура Один()",
		Score: 1.5, LinesMatched: 42,
	}}
	for mode := range searchModeNouns {
		for _, st := range []dump.SearchStats{
			{Total: 1, Unit: dump.SearchUnitFor(mode)},
			{Total: 150, Unit: dump.SearchUnitFor(mode)},
			{Total: 40, Unreadable: 9, Unit: dump.SearchUnitFor(mode)},
		} {
			text := FormatSearchResultWithStats(many, st, "Процедура", mode, nil)
			for i, r := range text {
				for _, d := range dashes {
					if r == d {
						t.Errorf("mode %s stats %+v: rendered RU carries %q (U+%04X) at byte %d:\n%s",
							mode, st, string(r), r, i, text)
					}
				}
			}
		}
	}
	// And the empty-body shapes, which take their own return paths.
	for mode := range searchModeNouns {
		for _, st := range []dump.SearchStats{{Total: 0}, {Total: 40, Unreadable: 10, Unit: dump.SearchUnitFor(mode)}} {
			text := FormatSearchResultWithStats(nil, st, "Процедура", mode, nil)
			for i, r := range text {
				for _, d := range dashes {
					if r == d {
						t.Errorf("mode %s empty body stats %+v: rendered RU carries %q (U+%04X) at byte %d:\n%s",
							mode, st, string(r), r, i, text)
					}
				}
			}
		}
	}
}

// TestSearchUnitFallsBackToTheModeForLegacyCallers pins the one place the unit
// may be inferred. FormatSearchResult is the exported two-value shim kept for
// callers outside this module; it has no SearchStats to carry a unit, so the
// mode decides — through the SAME mapping the engine itself uses, so the two
// cannot drift into disagreeing about what a mode counts.
func TestSearchUnitFallsBackToTheModeForLegacyCallers(t *testing.T) {
	matches := []dump.Match{{Module: "Модуль.Тест", Line: 1, Context: "Тест"}}
	for mode, nouns := range searchModeNouns {
		// No unit anywhere: neither the two-value shim nor a hand-built stats value
		// carries one, and the header must still say what it counted.
		for label, text := range map[string]string{
			"two-value shim": FormatSearchResult(matches, 150, "Тест", mode, nil),
			"stats with no unit": FormatSearchResultWithStats(matches,
				dump.SearchStats{Total: 150}, "Тест", mode, nil),
		} {
			header, _, _ := strings.Cut(text, "\n")
			if !strings.Contains(header, nouns.want) {
				t.Errorf("mode %s, %s: header carries no unit at all: %q", mode, label, header)
			}
		}
	}

	// THE UNIT IS WHAT DRIVES THE NOUN, not the mode. Deliberately crossed: a stats
	// value that says it counted lines, handed to the smart renderer. Without this
	// the noun could be picked from the mode alone and every other assertion in
	// this file would still pass, which would put the engine's own statement about
	// what it counted back out of the loop.
	crossed := FormatSearchResultWithStats(matches,
		dump.SearchStats{Total: 150, Unit: dump.SearchUnitLines}, "Тест", dump.SearchModeSmart, nil)
	header, _, _ := strings.Cut(crossed, "\n")
	if !strings.Contains(header, "строк") || strings.Contains(header, "модулей") {
		t.Errorf("a stats value stamped SearchUnitLines rendered as modules under smart, so the "+
			"noun is taken from the mode and not from what the search says it counted: %q", header)
	}
}
