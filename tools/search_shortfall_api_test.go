package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// TestFormatSearchResult_HeaderNeverOverclaimsOnItsOwnLine pins the header, and
// it is deliberately a separate test from the footer checks. A reader, human or
// LLM, takes the first line as the answer's headline, and «(модулей с совпадениями: 386)»
// standing alone above an empty body is the claim that started this. The footer
// reconciling it three paragraphs later does not unmake the headline.
//
// So when hits were dropped the header may not be the plain result count: it has
// to name the number as the index's and print what the body actually holds next
// to it. Both halves are asserted, because deleting the count altogether would
// satisfy "does not overclaim" while destroying the information.
func TestFormatSearchResult_HeaderNeverOverclaimsOnItsOwnLine(t *testing.T) {
	matches := []dump.Match{{Module: "ОбщийМодуль.Живой.Модуль", Line: 1, Context: "Процедура Тест()"}}

	cases := map[string]struct {
		matches   []dump.Match
		stats     dump.SearchStats
		wantPlain bool
	}{
		"whole answer shown":    {matches, dump.SearchStats{Total: 1}, true},
		"cut by the limit only": {matches, dump.SearchStats{Total: 150}, true},
		"some hits unreadable":  {matches, dump.SearchStats{Total: 40, Unreadable: 9}, false},
		"every hit unreadable":  {nil, dump.SearchStats{Total: 40, Unreadable: 10}, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			text := FormatSearchResultWithStats(tc.matches, tc.stats, "Процедура", dump.SearchModeSmart, nil)
			header, _, _ := strings.Cut(text, "\n")

			plain := fmt.Sprintf("(модулей с совпадениями: %d)", tc.stats.Total)
			if got := strings.Contains(header, plain); got != tc.wantPlain {
				t.Errorf("header %q: plain count present=%v, want %v", header, got, tc.wantPlain)
			}
			if tc.wantPlain {
				return
			}
			// The qualified shape: the index owns the number, and the header says
			// how many of them the body carries.
			if !strings.Contains(header, "в индексе") {
				t.Errorf("header must attribute the count to the index, got %q", header)
			}
			if !strings.Contains(header, fmt.Sprintf("показано %d", len(tc.matches))) {
				t.Errorf("header must state how many matches the body holds (%d), got %q",
					len(tc.matches), header)
			}
			if !strings.Contains(header, fmt.Sprintf("%d", tc.stats.Total)) {
				t.Errorf("header must still report the index count %d, got %q", tc.stats.Total, header)
			}
		})
	}
}

// TestFormatSearchResult_ShortfallIsNamedNotSwallowed drives the formatter
// directly, so the two causes can be posed in combinations the index cannot
// easily be pushed into. It is the unit-level statement of the same property:
// the header, the body and the footer of one answer have to agree.
func TestFormatSearchResult_ShortfallIsNamedNotSwallowed(t *testing.T) {
	one := []dump.Match{{Module: "ОбщийМодуль.Живой.Модуль", Line: 1, Context: "Процедура Тест()"}}

	cases := map[string]struct {
		matches      []dump.Match
		stats        dump.SearchStats
		wantReload   bool
		wantLimit    bool
		wantNotFound bool
	}{
		"nothing matched at all": {
			matches: nil, stats: dump.SearchStats{Total: 0},
			wantNotFound: true,
		},
		"every selected hit was unreadable": {
			matches: nil, stats: dump.SearchStats{Total: 40, Unreadable: 10},
			wantReload: true, wantLimit: true,
		},
		"unreadable hits with nothing left over": {
			matches: one, stats: dump.SearchStats{Total: 8, Unreadable: 7},
			wantReload: true,
		},
		"plain limit truncation": {
			matches: one, stats: dump.SearchStats{Total: 150},
			wantLimit: true,
		},
		"whole answer shown": {
			matches: one, stats: dump.SearchStats{Total: 1},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			text := FormatSearchResultWithStats(tc.matches, tc.stats, "Процедура", dump.SearchModeSmart, nil)

			if got, want := strings.Contains(text, reloadRemedyMarker), tc.wantReload; got != want {
				t.Errorf("reload_dump remedy present=%v, want %v:\n%s", got, want, text)
			}
			if got, want := strings.Contains(text, limitRemedyMarker), tc.wantLimit; got != want {
				t.Errorf("limit remedy present=%v, want %v:\n%s", got, want, text)
			}
			if got, want := strings.Contains(text, "Ничего не найдено"), tc.wantNotFound; got != want {
				t.Errorf("«Ничего не найдено» present=%v, want %v:\n%s", got, want, text)
			}

			// The header may never claim more than the body accounts for.
			claimed := searchHeaderTotal(t, text)
			shown := searchRenderedMatches(text)
			if claimed > shown && tc.stats.Total > 0 && !strings.Contains(text, "Показано") {
				t.Errorf("header claims %d, body shows %d, and nothing reconciles them:\n%s",
					claimed, shown, text)
			}
		})
	}
}

// TestFormatSearchResult_LegacyEntryPointStillCompiles pins that the exported
// two-return signature keeps working for callers outside this module. It carries
// no shortfall data, so it must render exactly what it always did.
func TestFormatSearchResult_LegacyEntryPointStillCompiles(t *testing.T) {
	matches := []dump.Match{{Module: "Модуль.Тест", Line: 1, Context: "Тест"}}
	legacy := FormatSearchResult(matches, 150, "Тест", dump.SearchModeSmart, nil)
	fresh := FormatSearchResultWithStats(matches, dump.SearchStats{Total: 150}, "Тест", dump.SearchModeSmart, nil)
	if legacy != fresh {
		t.Errorf("the legacy entry point must render byte-identically to a zero-shortfall call:\n%q\n%q",
			legacy, fresh)
	}
}
