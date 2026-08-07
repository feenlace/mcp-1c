package dump

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ===========================================================================
// THE LINE SCAN KNOWS WHAT IT COULD NOT OPEN AND USED TO THROW IT AWAY.
//
// searchLineByLine walks EVERY candidate the filters select and reads each one
// through contentForScan. A candidate whose file is gone, or is there and cannot
// be opened, is dropped with a bare `return` inside the worker: it contributes
// no matches and no count, and the answer that comes back is internally
// consistent and silently smaller than the corpus the caller asked about.
//
// SearchStats.Unreadable IS NOT THE PLACE TO PUT IT, and that is the mistake this
// field exists to avoid rather than repeat. Unreadable means «hits this answer
// selected and then dropped», so it describes a gap between a count and a body;
// there is no such gap here, which is what TestSearchWithStats_LineScanReportsNoShortfall
// pins and what this file must not disturb. Unscanned means something else: how
// many CANDIDATES were never examined at all. Whether any of them holds a match is
// not knowable from an unread file, so the number is a BOUND on what the scan
// could see and never a claim that matches were lost.
//
// IT COSTS NOTHING. The refusal is already discovered on the path that returns
// it; the counter rides the same per-candidate result the match count rides, and
// is summed in the same ordered merge, so it adds no I/O, no syscall and no
// second pass, and it stays deterministic under the parallel chunking.
// ===========================================================================

// unscannedFixture writes n readable modules under CommonModules and returns the
// index over them.
func unscannedFixture(t *testing.T, n int) (*Index, string) {
	t.Helper()
	dir := mkStatsDump(t, n)
	return openStatsIndex(t, dir), dir
}

// removeModules deletes the module directories with the given indices and returns
// how many it removed.
func removeModules(t *testing.T, dir string, idxs ...int) int {
	t.Helper()
	for _, i := range idxs {
		if err := os.RemoveAll(filepath.Join(dir, "CommonModules", fmt.Sprintf("Модуль%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
	return len(idxs)
}

// TestTheLineScanReportsTheCandidatesItCouldNotOpen is the whole point: the scan
// visited 12 candidates, opened 9 and could not open 3, and the answer now carries
// that third number instead of losing it.
func TestTheLineScanReportsTheCandidatesItCouldNotOpen(t *testing.T) {
	idx, dir := unscannedFixture(t, 12)

	// POSITIVE CONTROL, and it runs first: with every file in place the scan opens
	// everything and the new counter is 0. Without it a field that is hardwired to
	// its own expected value would pass the assertion below.
	for _, mode := range []SearchMode{SearchModeExact, SearchModeRegex} {
		_, st, err := idx.SearchWithStats(SearchParams{
			Query: queryFor(mode), Mode: mode, Limit: 50,
		})
		if err != nil {
			t.Fatalf("control %s: %v", mode, err)
		}
		if st.Unscanned != 0 {
			t.Fatalf("control failed: %s over an intact dump reports Unscanned=%d, want 0",
				mode, st.Unscanned)
		}
		if st.Total != 12 {
			t.Fatalf("control failed: %s over an intact dump reports Total=%d, want 12",
				mode, st.Total)
		}
	}

	gone := removeModules(t, dir, 0, 2, 4)

	for _, mode := range []SearchMode{SearchModeExact, SearchModeRegex} {
		t.Run(string(mode), func(t *testing.T) {
			matches, st, err := idx.SearchWithStats(SearchParams{
				Query: queryFor(mode), Mode: mode, Limit: 50,
			})
			if err != nil {
				t.Fatalf("SearchWithStats: %v", err)
			}
			if st.Unscanned != gone {
				t.Errorf("%d candidates could not be opened and the scan reports Unscanned=%d, "+
					"want %d. Their lines were never examined and nothing else in this result "+
					"says so: Total is %d and %d matches came back, and those two agree with "+
					"each other over a corpus that is %d modules short.",
					gone, st.Unscanned, gone, st.Total, len(matches), gone)
			}
			// Unreadable stays 0: it means something else and this must not move it.
			if st.Unreadable != 0 {
				t.Errorf("%s: Unreadable moved to %d. The line scan reports no shortfall "+
					"between its count and its body, and Unscanned is not that quantity",
					mode, st.Unreadable)
			}
			if st.Total != 12-gone {
				t.Errorf("%s: Total=%d, want %d. The scan counts only what it read",
					mode, st.Total, 12-gone)
			}
		})
	}
}

// TestUnscannedCountsCandidatesAndNeverClaimsALostMatch is the honesty property,
// and it is the one a previous round of the consumer shipped wrong.
//
// The query matches NOTHING anywhere in the corpus, and three files are missing.
// A field that meant «matches that were lost» would have to be 0 here, because no
// match exists to lose. Unscanned is 3, because three candidates were not looked
// at, and the two statements are different. A consumer that renders this number
// must render it as the second one.
func TestUnscannedCountsCandidatesAndNeverClaimsALostMatch(t *testing.T) {
	idx, dir := unscannedFixture(t, 12)
	gone := removeModules(t, dir, 0, 2, 4)

	for _, mode := range []SearchMode{SearchModeExact, SearchModeRegex} {
		t.Run(string(mode), func(t *testing.T) {
			matches, st, err := idx.SearchWithStats(SearchParams{
				Query: "ЭтогоНетНигдеВКорпусе", Mode: mode, Limit: 50,
			})
			if err != nil {
				t.Fatalf("SearchWithStats: %v", err)
			}
			if st.Total != 0 || len(matches) != 0 {
				t.Fatalf("control failed: the query is supposed to match nothing, got Total=%d "+
					"matches=%d", st.Total, len(matches))
			}
			if st.Unscanned != gone {
				t.Errorf("%s: a query that matches nothing reports Unscanned=%d, want %d. The "+
					"number counts candidates the scan could not open, which does not depend "+
					"on whether the query would have matched them", mode, st.Unscanned, gone)
			}
		})
	}
}

// TestUnscannedCoversThePresentButUnopenableArm is the second of the two real
// states, and it costs 50 ms the first time where the deleted arm costs 44 µs.
// Both are refusals of contentForScan and both have to be counted, because the
// customer-visible consequence is identical: the module was not examined.
func TestUnscannedCoversThePresentButUnopenableArm(t *testing.T) {
	idx, dir := unscannedFixture(t, 6)
	lockFile(t, filepath.Join(dir, "CommonModules", "Модуль03", "Ext", "Module.bsl"))

	_, st, err := idx.SearchWithStats(SearchParams{
		Query: "Процедура", Mode: SearchModeRegex, Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchWithStats: %v", err)
	}
	if st.Unscanned != 1 {
		t.Errorf("a module that is present and cannot be opened reports Unscanned=%d, want 1",
			st.Unscanned)
	}
	if st.Total != 5 {
		t.Errorf("Total=%d, want 5", st.Total)
	}
}

// TestUnscannedIsScopedToWhatTheFiltersSelected: the number describes THIS scan's
// candidate list and not the whole index. A namespace filter that excluded a
// missing module must not have it counted.
func TestUnscannedIsScopedToWhatTheFiltersSelected(t *testing.T) {
	dir := t.TempDir()
	writeBSLTB(t, dir, "CommonModules/Базовый/Ext/Module.bsl",
		"Процедура Базовая()\n    Сообщить(\"игла\");\nКонецПроцедуры\n")
	writeBSLTB(t, dir, "Расширения/Доработки/CommonModules/РасшМодуль/Ext/Module.bsl",
		"Процедура Расширенная()\n    Сообщить(\"игла\");\nКонецПроцедуры\n")
	idx := openStatsIndex(t, dir)

	// CONTROL: both keys exist and the namespace really does split them.
	extKeys := 0
	for _, n := range idx.ModuleNames() {
		if len(n) > 4 && n[:4] == "ext." {
			extKeys++
		}
	}
	if extKeys != 1 || idx.ModuleCount() != 2 {
		t.Fatalf("control failed: %d modules, %d of them ext.*, want 2 and 1",
			idx.ModuleCount(), extKeys)
	}

	// Remove the BASE module only.
	if err := os.RemoveAll(filepath.Join(dir, "CommonModules", "Базовый")); err != nil {
		t.Fatal(err)
	}

	_, st, err := idx.SearchWithStats(SearchParams{
		Query: "игла", Mode: SearchModeRegex, Namespace: "ext", Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchWithStats: %v", err)
	}
	if st.Unscanned != 0 {
		t.Errorf("the extension namespace holds one module and it is readable, so Unscanned "+
			"must be 0, got %d. The missing module is outside the filter and this scan never "+
			"looked at it", st.Unscanned)
	}

	// POSITIVE CONTROL: the same index with no namespace filter DOES count it, so
	// the zero above is a scope and not a broken counter.
	_, all, err := idx.SearchWithStats(SearchParams{
		Query: "игла", Mode: SearchModeRegex, Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchWithStats (unfiltered): %v", err)
	}
	if all.Unscanned != 1 {
		t.Errorf("unfiltered, the missing base module must be counted: Unscanned=%d, want 1",
			all.Unscanned)
	}
}

// TestSmartSearchLeavesUnscannedZero: smart re-reads only the hits inside its own
// window and reports those through Unreadable, and its Total comes from the index
// rather than from a scan, so it has no candidate list to report about. A number
// invented for it would be a census of the corpus rather than a fact about the
// answer.
func TestSmartSearchLeavesUnscannedZero(t *testing.T) {
	idx, dir := unscannedFixture(t, 12)
	removeModules(t, dir, 0, 2, 4)

	_, st, err := idx.SearchWithStats(SearchParams{
		Query: "Процедура", Mode: SearchModeSmart, Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchWithStats: %v", err)
	}
	if st.Unscanned != 0 {
		t.Errorf("smart search reports Unscanned=%d, want 0: it has no candidate scan and its "+
			"shortfall is already Unreadable=%d", st.Unscanned, st.Unreadable)
	}
	if st.Unreadable != 3 {
		t.Errorf("control failed: smart search must still report Unreadable=3, got %d",
			st.Unreadable)
	}
}

// queryFor gives each literal mode the query in the case it actually compares:
// exact lowers both sides, regex matches the raw line.
func queryFor(mode SearchMode) string {
	if mode == SearchModeExact {
		return "процедура"
	}
	return "Процедура"
}
