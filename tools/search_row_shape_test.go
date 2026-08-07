package tools

import (
	"regexp"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// THE ROW SHAPE MUST NOT DEPEND ON WHERE THE ROW CAME FROM.
//
// The BM25 score was printed under a per-row condition: `mode == smart && m.Score
// > 0`. Score is filled in by the engine window and by nothing else, so a row that
// reached the formatter by any other route carried 0 and rendered a DIFFERENT
// SHAPE from the row beside it — same tool, same answer, same mode, two layouts.
//
// That is a defect of this formatter and not of whoever supplies the rows. A
// renderer that changes shape according to which retrieval path produced a row is
// publishing an implementation detail as though it were information about the
// code, and it is the reason one question could still be answered several ways
// after both the result set and its order had been made stable.
//
// The label is GONE rather than made unconditional. Printing «score: 0.000» for a
// row the engine never scored would state a measurement that was not taken, which
// is worse than printing none: an unscored row would become indistinguishable from
// a genuinely worst-ranked one.

var scoreLabelRe = regexp.MustCompile(`score`)

// TestTheRowShapeIsTheSameWhateverFilledTheRow is the whole claim, driven through
// the shipped formatter.
func TestTheRowShapeIsTheSameWhateverFilledTheRow(t *testing.T) {
	// Two rows that differ ONLY in Score. The first is what the engine window
	// produces; the second is what any other retrieval path produces. Everything
	// else about them is identical, so any difference in the rendered rows is
	// attributable to Score and to nothing else.
	scored := dump.Match{Module: "ОбщийМодуль.Раз.Модуль", Line: 7, Context: "Процедура Раз()", Score: 0.847}
	unscored := dump.Match{Module: "ОбщийМодуль.Раз.Модуль", Line: 7, Context: "Процедура Раз()", Score: 0}

	stats := dump.SearchStats{Total: 1, Unit: dump.SearchUnitModules}
	a := FormatSearchResultWithStats([]dump.Match{scored}, stats, "Раз", dump.SearchModeSmart, nil)
	b := FormatSearchResultWithStats([]dump.Match{unscored}, stats, "Раз", dump.SearchModeSmart, nil)

	if a != b {
		t.Errorf("the same row renders differently according to whether the engine window "+
			"filled its Score, so the row shape publishes the retrieval path:\n"+
			"--- with Score 0.847 ---\n%s\n--- with Score 0 ---\n%s", a, b)
	}
}

// TestNoRowAnnouncesABM25Score across every mode. The label is the mechanism by
// which the shape varied, so its absence is asserted directly and not only through
// the equality above: two rows could agree with each other and both still carry it.
func TestNoRowAnnouncesABM25Score(t *testing.T) {
	matches := []dump.Match{
		{Module: "ОбщийМодуль.Раз.Модуль", Line: 7, Context: "Процедура Раз()", Score: 0.847, LinesMatched: 3},
		{Module: "ОбщийМодуль.Два.Модуль", Line: 0, Score: 0.512},
		{Module: "ОбщийМодуль.Три.Модуль", Line: 2, Context: "Процедура Три()", Score: 0},
	}
	for _, mode := range []dump.SearchMode{dump.SearchModeSmart, dump.SearchModeExact, dump.SearchModeRegex} {
		t.Run(string(mode), func(t *testing.T) {
			out := FormatSearchResultWithStats(matches,
				dump.SearchStats{Total: 3, Unit: dump.SearchUnitFor(mode)}, "Раз", mode, nil)
			if scoreLabelRe.MatchString(out) {
				t.Errorf("a row still announces a BM25 score:\n%s", out)
			}
			// CONTROL: the renderer really did produce rows, so the absence above
			// is the label being gone and not the answer being empty.
			if n := strings.Count(out, "### "); n != len(matches) {
				t.Fatalf("control failed: rendered %d rows, want %d:\n%s", n, len(matches), out)
			}
			// The rest of the row is untouched: the line number is still there, and
			// so is the per-module matching-line count.
			if !strings.Contains(out, "строка 7") {
				t.Errorf("the line number went with the score:\n%s", out)
			}
			if !strings.Contains(out, "строк с совпадениями в модуле: 3") {
				t.Errorf("the matching-line count went with the score:\n%s", out)
			}
			if !strings.Contains(out, "строка не определена") {
				t.Errorf("the line-0 wording went with the score:\n%s", out)
			}
		})
	}
}

// TestTheRowShapeDoesNotDependOnTheMode either. Once the score is gone the only
// per-mode branch left in a row was the one that carried it, so the same match
// renders identically in all three modes. This is stated as an outcome because it
// is what makes the shape checkable at all: one shape, not three.
func TestTheRowShapeDoesNotDependOnTheMode(t *testing.T) {
	m := []dump.Match{{Module: "ОбщийМодуль.Раз.Модуль", Line: 7, Context: "Процедура Раз()", Score: 0.847}}
	var rows []string
	for _, mode := range []dump.SearchMode{dump.SearchModeSmart, dump.SearchModeExact, dump.SearchModeRegex} {
		out := FormatSearchResultWithStats(m, dump.SearchStats{Total: 1, Unit: dump.SearchUnitFor(mode)},
			"Раз", mode, nil)
		_, row, ok := strings.Cut(out, "\n\n")
		if !ok {
			t.Fatalf("could not separate the heading from the row in:\n%s", out)
		}
		rows = append(rows, row)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i] != rows[0] {
			t.Errorf("the row differs between modes:\n--- first ---\n%s\n--- %d ---\n%s", rows[0], i, rows[i])
		}
	}
}
