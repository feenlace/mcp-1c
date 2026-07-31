package tools

// The answer search_code gives when the index holds nothing at all.
//
// THE DEFECT, measured on the real binary through MCP over stdio and identical on
// v1.12.0 (a603feb) and on this branch's base (b74d027): smart mode answered
// «search: bleve search: cannot perform operation on empty alias» with isError set,
// while regex and exact answered «Индекс пуст: ...». The three modes disagreed about
// the same question, and the mode that disagreed is the DEFAULT one.
//
// It matters out of proportion to its size because of WHO sees it. A --dump pointing
// at a directory before DumpConfigToFiles has been run, or at the wrong directory
// altogether, is the commonest first mistake there is, and this is the answer to that
// user's first search.
//
// WHAT IS NOT PINNED HERE:
//
//   - That the empty state is not an error at all. That is the dump layer's and is
//     pinned in dump/empty_index_test.go, together with the state it must not
//     swallow: an index that LISTS modules and holds no shards is still an error.
//   - The other ten tools. None of them answers from the index, so none of them has
//     an empty-index answer to give.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// emptyModes is every mode search_code accepts, including the empty string, which is
// the one an MCP client sends when it omits the argument and which dispatches to
// smart. The defect was in smart, so the default has to be in the table.
var emptyModes = []string{"", "smart", "regex", "exact"}

// emptySearch runs search_code against index in the given mode and returns the body
// plus whether the call errored.
func emptySearch(t *testing.T, index *dump.Index, mode string) (string, bool) {
	t.Helper()
	args := map[string]any{"query": "ОбщегоНазначения"}
	if mode != "" {
		args["mode"] = mode
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewSearchCodeHandler(index)(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "search_code", Arguments: raw},
	})
	if err != nil {
		return err.Error(), true
	}
	return resultText(t, res), false
}

// emptyOpen opens an index over dumpDir and waits for it.
func emptyOpen(t *testing.T, dumpDir string) *dump.Index {
	t.Helper()
	idx, err := dump.NewIndex(dumpDir, t.TempDir(), false)
	if err != nil {
		t.Fatalf("opening %s: %v", dumpDir, err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()
	if !idx.Ready() {
		t.Fatalf("the index never became ready: %v", idx.BuildError())
	}
	return idx
}

// TestEmptyIndex_EveryModeSaysTheIndexIsEmpty is the regression, across all four
// spellings of a mode a client can send.
func TestEmptyIndex_EveryModeSaysTheIndexIsEmpty(t *testing.T) {
	idx := emptyOpen(t, t.TempDir()) // a deliberately empty dump
	if n := idx.ModuleCount(); n != 0 {
		t.Fatalf("the fixture is wrong: the empty dump indexed %d modules", n)
	}

	for _, mode := range emptyModes {
		text, isErr := emptySearch(t, idx, mode)
		if isErr {
			t.Errorf("mode %q answered an empty dump with an error: %s", mode, text)
			continue
		}
		if !strings.Contains(text, "Индекс пуст") {
			t.Errorf("mode %q does not say the index is empty: %q", mode, text)
		}
		// AND NOT WITH AN ENGINE STRING. This is the phrase the user actually got, and
		// it is matched literally rather than through a constant so a build that still
		// leaks it fails here.
		if strings.Contains(text, "empty alias") || strings.Contains(text, "bleve") {
			t.Errorf("mode %q still leaks a search-engine string: %q", mode, text)
		}
		// AND IT SAYS WHAT TO CHECK. A message that only states the problem sends the
		// user to the issue tracker; both things they can check are named.
		for _, want := range []string{"--dump", "выгрузк"} {
			if !strings.Contains(text, want) {
				t.Errorf("mode %q does not tell the user to check %q: %q", mode, want, text)
			}
		}
	}
}

// TestEmptyIndex_APopulatedIndexNeverSaysIt is the control. A message that appears
// when nothing is wrong is the same defect as a missing one, only quieter.
func TestEmptyIndex_APopulatedIndexNeverSaysIt(t *testing.T) {
	dumpDir := t.TempDir()
	mkBSL(t, dumpDir, "CommonModules/ОбщегоНазначения/Ext/Module.bsl",
		"Процедура Тест() Экспорт\n\tСообщить(\"ОбщегоНазначения\");\nКонецПроцедуры\n")
	idx := emptyOpen(t, dumpDir)
	if idx.ModuleCount() != 1 {
		t.Fatalf("the fixture indexed %d modules, want 1", idx.ModuleCount())
	}

	for _, mode := range emptyModes {
		text, isErr := emptySearch(t, idx, mode)
		if isErr {
			t.Errorf("mode %q failed on a populated index: %s", mode, text)
			continue
		}
		if strings.Contains(text, "Индекс пуст") {
			t.Errorf("mode %q called a populated index empty: %q", mode, text)
		}
		if searchRenderedMatches(text) == 0 {
			t.Errorf("mode %q rendered no matches on a populated index: %q", mode, text)
		}
	}

	// AND A QUERY THAT SIMPLY MATCHES NOTHING IS NOT AN EMPTY INDEX EITHER. The two
	// have different remedies: one is a different query, the other is a different
	// --dump, and «Индекс пуст» would send the user to re-dump a perfectly good
	// configuration.
	args, err := json.Marshal(map[string]any{"query": "СловоКоторогоТочноНетВЭтойВыгрузке"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewSearchCodeHandler(idx)(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "search_code", Arguments: args},
	})
	if err != nil {
		t.Fatalf("a query matching nothing must not error: %v", err)
	}
	if text := resultText(t, res); strings.Contains(text, "Индекс пуст") {
		t.Errorf("a query that matched nothing was reported as an empty index: %q", text)
	}
}

// TestEmptyIndex_TheMessageCarriesNoDash pins the house rule for customer-facing
// Russian on the shipped constant rather than on a copy of it.
func TestEmptyIndex_TheMessageCarriesNoDash(t *testing.T) {
	for _, r := range []rune{'—', '–', '‒', '―', '−'} {
		if strings.ContainsRune(emptyIndexMessage, r) {
			t.Errorf("the empty-index message contains %q (U+%04X), which customer-facing RU text "+
				"must not carry", string(r), r)
		}
	}
	// POSITIVE CONTROL: the check can fire.
	if !strings.ContainsRune("текст — с тире", '—') {
		t.Fatal("the control failed: the dash check cannot detect an em dash")
	}
}
