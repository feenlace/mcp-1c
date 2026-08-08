package dump

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// SearchStats.Unreadable AND SearchStats.Unscanned ARE MUTUALLY EXCLUSIVE, AND
// THAT IS A CONTRACT RATHER THAN AN OBSERVATION.
//
// The two counters describe different things. Unreadable is a GAP between a count
// and a body: hits this answer selected and then dropped, so a match was observed
// and is missing. Unscanned is a BOUND: candidates the scan never opened, so
// whether they hold a match is not known. A renderer that has both may say
// «совпадения потеряны» about the first and must not say it about the second.
//
// Today only searchSmart writes the first and only searchLineByLine writes the
// second, so no answer carries both, and a consumer can pick its wording from
// whichever is non-zero without arbitrating. NOTHING MADE THAT TRUE. It is a
// property of two functions that happen to be written that way, in a module that
// other products vendor, and a consumer's correctness rests on it: mcp-1c-advanced
// derives which sentence names the customer's base from exactly this exclusivity,
// and a bump that let both fire would re-point «совпадения потеряны» at a set
// nothing was ever observed to be lost from.
//
// THIS TEST IS WHAT MAKES IT A CONTRACT. It drives the real engine over a dump in
// which BOTH kinds of shortfall are available at once, in every mode, and fails
// the moment a mode starts reporting both. The fixture reaches both states from
// ONE change to the tree, a deleted module, because that is the state a re-dump
// under a live server passes through: the smart leg selects the hit from the index
// and cannot re-read it, the literal legs never open it at all.
func TestNoAnswerCarriesBothShortfallCounters(t *testing.T) {
	const marker = "МаркерВзаимногоИсключения"
	root := t.TempDir()
	for i := range 6 {
		dir := filepath.Join(root, "CommonModules", fmt.Sprintf("Взаимный%02d", i), "Ext")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		body := fmt.Sprintf("Процедура %s%02d() Экспорт\n    %s();\nКонецПроцедуры\n", marker, i, marker)
		if err := os.WriteFile(filepath.Join(dir, "Module.bsl"), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()
	if !idx.Ready() {
		t.Fatalf("index not ready: %v", idx.BuildError())
	}

	modes := []SearchMode{SearchModeSmart, SearchModeRegex, SearchModeExact}

	// CONTROL 1: with the dump intact every mode answers and NEITHER counter fires,
	// so a zero below would be the fixture's doing and not the property's.
	for _, mode := range modes {
		ms, st, err := idx.SearchWithStats(SearchParams{Query: marker, Mode: mode, Limit: 500})
		if err != nil {
			t.Fatalf("control failed: mode=%v refused an intact dump: %v", mode, err)
		}
		if len(ms) == 0 {
			t.Fatalf("control failed: mode=%v matches nothing on an intact dump, so the "+
				"shortfall below cannot be produced either", mode)
		}
		if st.Unreadable != 0 || st.Unscanned != 0 {
			t.Fatalf("control failed: mode=%v already reports a shortfall on an intact dump "+
				"(%+v)", mode, st)
		}
	}

	// ONE CHANGE TO THE TREE, which both kinds of shortfall are reachable from.
	if err := os.RemoveAll(filepath.Join(root, "CommonModules", "Взаимный00")); err != nil {
		t.Fatalf("removing a module: %v", err)
	}

	// CONTROL 2: every mode now reports SOMETHING, or the exclusivity below holds
	// trivially because neither counter fired at all. This is the check that keeps
	// the test from passing on an engine that simply stopped counting.
	fired := map[SearchMode]SearchStats{}
	for _, mode := range modes {
		_, st, err := idx.SearchWithStats(SearchParams{Query: marker, Mode: mode, Limit: 500})
		if err != nil {
			t.Fatalf("mode=%v: %v", mode, err)
		}
		if st.Unreadable == 0 && st.Unscanned == 0 {
			t.Fatalf("control failed: mode=%v reports neither counter after a module was "+
				"removed, so this mode's half of the property is not measured (%+v)", mode, st)
		}
		fired[mode] = st
	}

	// THE PROPERTY.
	for _, mode := range modes {
		st := fired[mode]
		if st.Unreadable != 0 && st.Unscanned != 0 {
			t.Errorf("mode=%v reports Unreadable=%d AND Unscanned=%d on one answer. A consumer "+
				"choosing its wording from whichever is non-zero now has two answers, and the "+
				"one that says matches were lost is not sound on candidates nothing opened "+
				"(%+v)", mode, st.Unreadable, st.Unscanned, st)
		}
	}

	// AND THE TWO HALVES ARE THE ONES THE DOC CLAIMS, which is what keeps the
	// exclusivity from being satisfied by an engine that stopped writing either one.
	if fired[SearchModeSmart].Unreadable == 0 {
		t.Errorf("smart no longer counts hits it selected and could not re-read, so the "+
			"«совпадения потеряны» wording has no producer (%+v)", fired[SearchModeSmart])
	}
	for _, mode := range []SearchMode{SearchModeRegex, SearchModeExact} {
		if fired[mode].Unscanned == 0 {
			t.Errorf("mode=%v no longer counts candidates it could not open, so the bound "+
				"wording has no producer (%+v)", mode, fired[mode])
		}
	}
}
