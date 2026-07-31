package dump

// An index with nothing in it, and the state that looks like it from a distance.
//
// THE DEFECT, measured on the real binary through MCP over stdio: search_code in
// smart mode against an EMPTY dump answered «search: bleve search: cannot perform
// operation on empty alias», an internal search-engine string, while regex and exact
// answered «Индекс пуст: ...». Identical on v1.12.0 (a603feb) and on this branch's
// base (b74d027), so it is not a regression. An empty dump is the commonest FIRST
// mistake a new user makes (a --dump pointing at the wrong directory, or one where
// DumpConfigToFiles was never run), so it is the worst place for an engine string.
//
// AND THE STATE IT MUST NOT SWALLOW. An empty alias is also what an index that LISTS
// modules and holds no shards has, and that one is a defect: it is what a generation
// reaped mid-open produces, and what Reload refuses to swap in
// (TestReap_ReloadRefusesAGenerationWithNoShards). The two are told apart by the
// module count, and the discriminator is asserted here in both directions.
//
// WHAT IS NOT PINNED HERE, stated rather than left to be discovered:
//
//   - The RU sentence itself, which is the tool layer's and is pinned in
//     tools/empty_index_test.go against tools/search.go:emptyIndexMessage. This file
//     pins only that the dump layer stops calling an empty index a failed search.
//   - An index whose shards were opened and then closed underneath it. That produces
//     a different bleve error (ErrorIndexClosed), which is not caught by the guard and
//     still surfaces, but no test constructs it: closing a shard behind a live Index
//     is not a state any path in this package can reach.

import (
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

// emptyIndexModes is every search mode search_code can dispatch. They are run as one
// table because the defect WAS that they disagreed: two of them called an empty index
// zero results and the third called it an engine failure.
var emptyIndexModes = []SearchMode{SearchModeSmart, SearchModeRegex, SearchModeExact}

// TestEmptyIndex_EveryModeAgreesThatNothingIsNothing is the regression.
func TestEmptyIndex_EveryModeAgreesThatNothingIsNothing(t *testing.T) {
	dumpDir := t.TempDir() // deliberately empty
	cacheDir := t.TempDir()

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("an empty dump must still open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()
	if !idx.Ready() {
		t.Fatalf("the empty-dump index never became ready: %v", idx.BuildError())
	}
	// The fixture, asserted rather than assumed: nothing was indexed, so what follows
	// is a test of the empty case and not of a populated one.
	if n := idx.ModuleCount(); n != 0 {
		t.Fatalf("the fixture is wrong: an empty dump indexed %d modules", n)
	}

	for _, mode := range emptyIndexModes {
		matches, stats, err := idx.SearchWithStats(SearchParams{Query: "ОбщегоНазначения", Mode: mode})
		if err != nil {
			t.Errorf("mode %q calls an empty index a failed search: %v", mode, err)
			continue
		}
		if len(matches) != 0 || stats.Total != 0 {
			t.Errorf("mode %q found %d matches (total %d) in an index with no modules",
				mode, len(matches), stats.Total)
		}
	}
}

// TestEmptyIndex_ListingModulesWithNoShardsIsStillAnError is the discriminator, and it
// is the reason the guard is not simply "swallow ErrorAliasEmpty".
//
// An index that knows about modules and has no shards to search them in is broken:
// that is the shape a generation reaped between the READY check and the attach leaves
// behind, and it is the shape Reload refuses to publish. Reporting it as an empty
// configuration would send the user off to re-run DumpConfigToFiles for a dump that
// is fine, and would hide a cache that is not.
func TestEmptyIndex_ListingModulesWithNoShardsIsStillAnError(t *testing.T) {
	idx := &Index{
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]cachedModule),
		pathByName:    make(map[string]string),
		pathToDocID:   make(map[string]string),
		names:         []string{"ОбщийМодуль.ОбщегоНазначения.Модуль"},
		done:          make(chan struct{}),
	}
	close(idx.done)
	idx.pathIndex = NewPathIndex(idx.names)
	idx.ready.Store(true)

	// CONTROL: the fixture really is the state this is about, an empty alias over a
	// non-empty name list. Without it a fixture that quietly had shards would make the
	// assertion below pass for the wrong reason.
	if idx.ModuleCount() == 0 {
		t.Fatal("the fixture is wrong: the index lists no modules, so it is the empty case, " +
			"not the shardless one")
	}
	if _, err := idx.alias.Search(bleve.NewSearchRequest(bleve.NewMatchAllQuery())); err == nil {
		t.Fatal("the fixture is wrong: the alias is not empty, so the guard under test is never reached")
	}

	_, _, err := idx.SearchWithStats(SearchParams{Query: "ОбщегоНазначения", Mode: SearchModeSmart})
	if err == nil {
		t.Fatal("an index that lists modules and holds no shards reported an ordinary empty result; " +
			"a reaped generation now looks to the user like a configuration that was never dumped")
	}
	if !strings.Contains(err.Error(), "bleve search") {
		t.Errorf("the error no longer says the search engine refused: %v", err)
	}
}

// TestEmptyIndex_APopulatedIndexIsUnaffected is the control that stops the guard from
// being a blanket "never report a search error". It searches a real, non-empty index
// and requires both a real answer and no silent zero.
func TestEmptyIndex_APopulatedIndexIsUnaffected(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("opening the populated dump: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()
	if !idx.Ready() {
		t.Fatalf("the index never became ready: %v", idx.BuildError())
	}
	if n := idx.ModuleCount(); n != serveFrozenModules {
		t.Fatalf("the fixture indexed %d modules, want %d", n, serveFrozenModules)
	}

	for _, mode := range emptyIndexModes {
		matches, stats, err := idx.SearchWithStats(SearchParams{Query: serveFrozenTerm, Mode: mode, Limit: 20})
		if err != nil {
			t.Errorf("mode %q failed on a populated index: %v", mode, err)
			continue
		}
		if len(matches) != serveFrozenModules || stats.Total != serveFrozenModules {
			t.Errorf("mode %q returned %d matches (total %d) on a populated index, want %d",
				mode, len(matches), stats.Total, serveFrozenModules)
		}
	}
	// AND A REAL ENGINE FAILURE IS STILL A FAILURE. An invalid regex is the one
	// search-side error a caller can produce on demand, and it must not be quietly
	// turned into "nothing found" on any index.
	if _, _, err := idx.SearchWithStats(SearchParams{Query: "([", Mode: SearchModeRegex}); err == nil {
		t.Error("an invalid regex was reported as an ordinary empty result")
	}
}
