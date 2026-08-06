package dump

import (
	"os"
	"path/filepath"
	"testing"
)

// The search modes do not all count the same thing, and every answer used to come
// back in one unlabelled int. MEASURED on dumps/dump_bsl with one query
// «Процедура» and limit 500: smart 11788, regex 203718, exact 204795. Smart counts
// modules, regex and exact count lines, which is why SearchUnit has exactly those
// two values and no third. This file pins the unit at the source, so the renderer
// never has to guess which number it was given.
//
// The opening sentence used to publish a total for how many different things the
// modes count, and that total agreed neither with dump/index.go:SearchUnit beside
// it nor with this comment's own next sentence. The stale figure is not restated.

// writeSearchFixture writes one .bsl under root and returns root.
func writeSearchFixture(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// openBuiltIndex builds an index over dir and waits for it.
func openBuiltIndex(t *testing.T, dir string) *Index {
	t.Helper()
	idx, err := NewIndex(dir, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatal(err)
	}
	return idx
}

// TestSearchStatsNameTheUnitTheyCounted is the engine-side half of the label
// fix: every search says what its Total counts.
func TestSearchStatsNameTheUnitTheyCounted(t *testing.T) {
	dir := t.TempDir()
	// One module, three matching lines. That makes the two units numerically
	// DIFFERENT on the same fixture, so a mode that reported the wrong unit would
	// also report a number the other unit cannot explain.
	writeSearchFixture(t, dir, "CommonModules/Общий/Ext/Module.bsl",
		"Процедура Первая()\nКонецПроцедуры\nПроцедура Вторая()\nКонецПроцедуры\nПроцедура Третья()\nКонецПроцедуры\n")
	idx := openBuiltIndex(t, dir)

	cases := []struct {
		mode      SearchMode
		wantUnit  SearchUnit
		wantTotal int
	}{
		{SearchModeSmart, SearchUnitModules, 1},
		{SearchModeRegex, SearchUnitLines, 3},
		{SearchModeExact, SearchUnitLines, 3},
	}
	for _, tc := range cases {
		_, st, err := idx.SearchWithStats(SearchParams{Query: "Процедура", Mode: tc.mode, Limit: 50})
		if err != nil {
			t.Fatalf("%s: %v", tc.mode, err)
		}
		if st.Unit != tc.wantUnit {
			t.Errorf("%s: Unit = %q, want %q", tc.mode, st.Unit, tc.wantUnit)
		}
		if st.Total != tc.wantTotal {
			t.Errorf("%s: Total = %d, want %d (the fixture has 1 module and 3 matching lines)",
				tc.mode, st.Total, tc.wantTotal)
		}
	}
	if SearchUnitModules == SearchUnitLines {
		t.Fatal("the two units are the same value, so pinning either proves nothing")
	}
}

// TestSmartCountsEveryMatchingLineOfTheHitModule pins the number that answers
// the customer's report. Smart returns ONE line per module; the definition he
// wanted was on line 1198 of a module whose first occurrence is on line 199, so
// the answer he got could not lead him to it. The count is measured in the scan
// that already runs to pick the displayed line.
func TestSmartCountsEveryMatchingLineOfTheHitModule(t *testing.T) {
	dir := t.TempDir()
	// Occurrences on lines 1 and 5, and nothing on the lines between, so the
	// expected count is not the line count of the file.
	writeSearchFixture(t, dir, "CommonModules/Много/Ext/Module.bsl",
		"Процедура Первая()\nКонецПроцедуры\n\n\nПроцедура Вторая()\nКонецПроцедуры\n")
	writeSearchFixture(t, dir, "CommonModules/Один/Ext/Module.bsl",
		"Процедура Единственная()\nКонецПроцедуры\n")
	idx := openBuiltIndex(t, dir)

	matches, _, err := idx.SearchWithStats(SearchParams{Query: "Процедура", Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Match{}
	for _, m := range matches {
		if _, dup := got[m.Module]; dup {
			t.Fatalf("smart returned %q twice; the per-module count below would be meaningless", m.Module)
		}
		got[m.Module] = m
	}

	many, ok := got["ОбщийМодуль.Много.Модуль"]
	if !ok {
		t.Fatalf("the two-occurrence module is missing from the answer: %v", got)
	}
	if many.LinesMatched != 2 {
		t.Errorf("LinesMatched = %d for a module with two matching lines, want 2", many.LinesMatched)
	}
	if many.Line != 1 {
		t.Errorf("Line = %d, want the first occurrence (1)", many.Line)
	}

	one, ok := got["ОбщийМодуль.Один.Модуль"]
	if !ok {
		t.Fatalf("the one-occurrence module is missing from the answer: %v", got)
	}
	// The other direction: a constant 2, or a count of the whole file, would have
	// satisfied the assertion above.
	if one.LinesMatched != 1 {
		t.Errorf("LinesMatched = %d for a module with one matching line, want 1", one.LinesMatched)
	}
}

// TestCollapsedKeyIsScannedOncePerKeyNotOncePerFile is the third Group 1 defect.
//
// Two files deriving one module name are BOTH kept in idx.names (that is how the
// collapse report counts them), and the regex/exact candidate list is idx.names.
// So the scan opened the surviving file once per colliding entry: it counted its
// lines twice and printed the same code block twice. MEASURED before the fix on
// the fixture below: Total=2, two rendered matches, both quoting «Процедура Два»,
// which is the content of the file that WON the key. The loser's content is not
// in the answer at all, which is what the collapse notice is for.
func TestCollapsedKeyIsScannedOncePerKeyNotOncePerFile(t *testing.T) {
	dir := t.TempDir()
	// Two dump roots under one parent: the anchor scan keys both off Catalogs, so
	// the two files derive the same module name. This is the customer-shaped tree,
	// dash and all.
	writeSearchFixture(t, dir, "Доработки/Catalogs/Товары/Ext/ObjectModule.bsl",
		"Процедура Один()\nКонецПроцедуры\n")
	writeSearchFixture(t, dir, "Доработки — копия/Catalogs/Товары/Ext/ObjectModule.bsl",
		"Процедура Два()\nКонецПроцедуры\n")
	idx := openBuiltIndex(t, dir)

	// CONTROL: the fixture really does collapse. Without this the assertions below
	// would also pass on a tree where the two files never shared a key.
	if st := idx.CollapsedKeys(); st.Files != 1 || st.Keys != 1 {
		t.Fatalf("control failed: the fixture did not collapse (files=%d keys=%d); "+
			"the scan-once property below is untested", st.Files, st.Keys)
	}
	if n := len(idx.ModuleNames()); n != 2 {
		t.Fatalf("control failed: idx.names holds %d entries, want 2 (both colliding files)", n)
	}

	for _, mode := range []SearchMode{SearchModeExact, SearchModeRegex} {
		matches, st, err := idx.SearchWithStats(SearchParams{Query: "Процедура", Mode: mode, Limit: 50})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if st.Total != 1 {
			t.Errorf("%s: Total = %d, want 1. The index can serve exactly one of the two "+
				"colliding files, so counting the survivor once per collided entry reports "+
				"matches that no readable line produced", mode, st.Total)
		}
		if len(matches) != 1 {
			t.Errorf("%s: %d matches rendered, want 1; the same module and the same line "+
				"were printed once per collided entry: %+v", mode, len(matches), matches)
		}
	}
}
