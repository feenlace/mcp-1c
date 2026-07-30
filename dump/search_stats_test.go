package dump

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkStatsDump writes n common modules that all contain «Процедура», each in its
// own directory so a single removal takes exactly one module off disk while the
// built index keeps counting it. That is the state a re-dump leaves behind while
// a server is still serving the previous index.
func mkStatsDump(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	for i := range n {
		writeBSLTB(t, dir, fmt.Sprintf("CommonModules/Модуль%02d/Ext/Module.bsl", i),
			fmt.Sprintf("Процедура Тест%02d()\n    Сообщить(\"%02d\");\nКонецПроцедуры\n", i, i))
	}
	return dir
}

func openStatsIndex(t *testing.T, dir string) *Index {
	t.Helper()
	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	waitReadyTB(t, idx, 30*time.Second)
	return idx
}

// TestSearchWithStats_CountsHitsDroppedAsUnreadable is the measurement the
// renderer needs and could not get: how many of the hits this answer selected
// were dropped because their module could no longer be read. Without it the
// caller sees only a Total from the index and a shorter slice of matches, with
// nothing to distinguish "the limit cut it" from "the files are gone".
func TestSearchWithStats_CountsHitsDroppedAsUnreadable(t *testing.T) {
	dir := mkStatsDump(t, 12)
	idx := openStatsIndex(t, dir)

	// Premise: intact dump, nothing dropped, count equals what is returned.
	matches, stats, err := idx.SearchWithStats(SearchParams{Query: "Процедура", Limit: 50})
	if err != nil {
		t.Fatalf("SearchWithStats: %v", err)
	}
	if len(matches) != 12 || stats.Total != 12 || stats.Unreadable != 0 {
		t.Fatalf("premise broken: intact dump gave %d matches, Total=%d, Unreadable=%d",
			len(matches), stats.Total, stats.Unreadable)
	}

	for i := 0; i < 12; i += 2 {
		if err := os.RemoveAll(filepath.Join(dir, "CommonModules", fmt.Sprintf("Модуль%02d", i))); err != nil {
			t.Fatal(err)
		}
	}

	matches, stats, err = idx.SearchWithStats(SearchParams{Query: "Процедура", Limit: 50})
	if err != nil {
		t.Fatalf("SearchWithStats after removal: %v", err)
	}
	if stats.Total != 12 {
		t.Errorf("Total must stay the index count, got %d", stats.Total)
	}
	if len(matches) != 6 {
		t.Errorf("expected the 6 surviving modules to be returned, got %d", len(matches))
	}
	if stats.Unreadable != 6 {
		t.Errorf("expected 6 hits reported as unreadable, got %d", stats.Unreadable)
	}
	// The three numbers have to add up, which is the property a renderer needs:
	// everything the index counted is either shown, dropped, or beyond the limit.
	if len(matches)+stats.Unreadable > stats.Total {
		t.Errorf("shown (%d) + unreadable (%d) exceeds Total (%d)",
			len(matches), stats.Unreadable, stats.Total)
	}
}

// TestSearchWithStats_UnreadableIsBoundedByTheLimit pins the scope of the
// number. Only the hits this answer selected are re-read, so only those can be
// counted; reporting a corpus-wide figure would mean claiming knowledge of pages
// the search never fetched.
func TestSearchWithStats_UnreadableIsBoundedByTheLimit(t *testing.T) {
	dir := mkStatsDump(t, 12)
	idx := openStatsIndex(t, dir)

	if _, _, err := idx.SearchWithStats(SearchParams{Query: "Процедура", Limit: 50}); err != nil {
		t.Fatalf("warm-up search: %v", err)
	}
	for i := range 12 {
		if err := os.RemoveAll(filepath.Join(dir, "CommonModules", fmt.Sprintf("Модуль%02d", i))); err != nil {
			t.Fatal(err)
		}
	}

	matches, stats, err := idx.SearchWithStats(SearchParams{Query: "Процедура", Limit: 4})
	if err != nil {
		t.Fatalf("SearchWithStats: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("premise broken: every module is gone, got %d matches", len(matches))
	}
	if stats.Total != 12 {
		t.Errorf("Total must stay the index count, got %d", stats.Total)
	}
	if stats.Unreadable != 4 {
		t.Errorf("limit 4 selects 4 hits, all unreadable, so Unreadable must be 4, got %d",
			stats.Unreadable)
	}
}

// TestSearchWithStats_LineScanReportsNoShortfall is the negative control for the
// other two search modes. There an unreadable module drops out before it can
// contribute to the count, so the count and the matches are produced from the
// same content and cannot disagree. A non-zero Unreadable here would invent a
// shortfall and tell a caller to re-dump over an answer that is already whole.
func TestSearchWithStats_LineScanReportsNoShortfall(t *testing.T) {
	dir := mkStatsDump(t, 12)
	idx := openStatsIndex(t, dir)

	for i := 0; i < 12; i += 2 {
		if err := os.RemoveAll(filepath.Join(dir, "CommonModules", fmt.Sprintf("Модуль%02d", i))); err != nil {
			t.Fatal(err)
		}
	}

	// Exact search lowers both sides, regex matches the raw line, so the two
	// modes need the query in the case each of them actually compares.
	queries := map[SearchMode]string{
		SearchModeExact: "процедура",
		SearchModeRegex: "Процедура",
	}
	for _, mode := range []SearchMode{SearchModeExact, SearchModeRegex} {
		t.Run(string(mode), func(t *testing.T) {
			matches, stats, err := idx.SearchWithStats(SearchParams{
				Query: queries[mode], Mode: mode, Limit: 50,
			})
			if err != nil {
				t.Fatalf("SearchWithStats: %v", err)
			}
			if stats.Unreadable != 0 {
				t.Errorf("%s scan counts only what it read, so Unreadable must be 0, got %d",
					mode, stats.Unreadable)
			}
			if stats.Total != len(matches) {
				t.Errorf("%s scan: Total (%d) must equal what it returns (%d) when nothing "+
					"was cut by limit", mode, stats.Total, len(matches))
			}
			if len(matches) != 6 {
				t.Errorf("%s scan: expected the 6 surviving modules, got %d", mode, len(matches))
			}
		})
	}
}

// TestSearch_TwoValueFormStillReportsTheIndexCount pins the legacy entry point:
// it must keep returning exactly the number it always did, so a caller outside
// this module sees no behaviour change from the stats work.
func TestSearch_TwoValueFormStillReportsTheIndexCount(t *testing.T) {
	dir := mkStatsDump(t, 12)
	idx := openStatsIndex(t, dir)

	if _, _, err := idx.Search(SearchParams{Query: "Процедура", Limit: 50}); err != nil {
		t.Fatalf("warm-up search: %v", err)
	}
	for i := range 12 {
		if err := os.RemoveAll(filepath.Join(dir, "CommonModules", fmt.Sprintf("Модуль%02d", i))); err != nil {
			t.Fatal(err)
		}
	}

	matches, total, err := idx.Search(SearchParams{Query: "Процедура", Limit: 50})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 12 || len(matches) != 0 {
		t.Errorf("expected the unchanged two-value behaviour (total 12, no matches), got total=%d matches=%d",
			total, len(matches))
	}
}
