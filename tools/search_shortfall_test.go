package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// reloadRemedyMarker is the phrase that names the remedy for a hit that was
// dropped because its module could no longer be read. Matched as a phrase and
// not through a constant, like every other marker in this package's tests, so
// these tests compile - and fail - against a build that does not carry the
// constant yet.
const reloadRemedyMarker = "reload_dump"

// limitRemedyMarker is the phrase that names the OTHER remedy: the answer was
// cut by the caller's own limit and a bigger limit brings the rest back. The two
// must never be printed for each other, because raising the limit does nothing
// for a module that is gone and re-dumping does nothing for a limit of 10.
const limitRemedyMarker = "увеличьте limit"

// searchHeaderTotal returns the number the result header claims, i.e. the N in
// `## Результаты поиска "..." (N совпадений...)`. It reads the FIRST run of
// digits on the header line, so it works for both the plain header and any
// header that qualifies the number.
func searchHeaderTotal(t *testing.T, text string) int {
	t.Helper()
	line, _, _ := strings.Cut(text, "\n")
	if !strings.HasPrefix(line, "## Результаты поиска") {
		t.Fatalf("first line is not the result header: %q", line)
	}
	start := -1
	for i, r := range line {
		if r >= '0' && r <= '9' {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("header carries no count: %q", line)
	}
	end := start
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(line[start:end])
	if err != nil {
		t.Fatalf("header count %q is not a number: %v", line[start:end], err)
	}
	return n
}

// searchRenderedMatches counts the match blocks the body actually shows. Every
// rendered match opens with a `### ` heading, and nothing else in a search
// result does.
func searchRenderedMatches(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "### ") {
			n++
		}
	}
	return n
}

// mkSearchDump writes n common modules that all contain «Процедура» and returns
// the dump root. Each module lives in its own directory, so a directory removal
// takes exactly one module away from disk while leaving the built index untouched
// - which is what a re-dump does under a running server.
func mkSearchDump(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	for i := range n {
		mkBSL(t, dir, fmt.Sprintf("CommonModules/Модуль%02d/Ext/Module.bsl", i),
			fmt.Sprintf("Процедура Тест%02d()\n    Сообщить(\"%02d\");\nКонецПроцедуры\n", i, i))
	}
	return dir
}

// dropSearchModules deletes the directories of the modules whose index is in
// idxs, exactly as a re-dump would while the server holds the old index.
func dropSearchModules(t *testing.T, dir string, idxs ...int) {
	t.Helper()
	for _, i := range idxs {
		if err := os.RemoveAll(filepath.Join(dir, "CommonModules", fmt.Sprintf("Модуль%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
}

// runSearch calls search_code through the real tool handler and returns the
// rendered body.
func runSearch(t *testing.T, index *dump.Index, query string, limit int) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{"query": query, "limit": limit})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewSearchCodeHandler(index)(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "search_code", Arguments: args},
	})
	if err != nil {
		t.Fatalf("search_code returned an error, but a partially readable dump must still answer: %v", err)
	}
	if result.IsError {
		t.Fatalf("search_code set isError, but a partially readable dump must still answer:\n%s",
			resultText(t, result))
	}
	return resultText(t, result)
}

// openSearchIndex builds an index over dir and waits for it to be ready.
func openSearchIndex(t *testing.T, dir string) *dump.Index {
	t.Helper()
	index, err := dump.NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { _ = index.Close() })
	waitReady(t, index, 30*time.Second)
	return index
}

// TestSearchCode_AllModulesGoneStillReportsTheirCount is the flagship of the
// class: the header counts matches from the index while the render path drops
// every hit whose module can no longer be read, so the answer states a number and
// then shows nothing, with no error and no explanation. The consumer is an LLM,
// which has no way to tell that "386 совпадений" followed by "Ничего не найдено"
// means the dump moved under the server rather than that the tool is broken.
//
// The dump directories are removed AFTER the index is built, which is not an
// exotic setup: reload_dump exists so a user can re-dump and reload without a
// restart, and files disappearing under a live server is that workflow's normal
// middle state.
func TestSearchCode_AllModulesGoneStillReportsTheirCount(t *testing.T) {
	dir := mkSearchDump(t, 12)
	index := openSearchIndex(t, dir)

	// Premise: with the dump intact the answer is whole.
	before := runSearch(t, index, "Процедура", 50)
	if got, want := searchRenderedMatches(before), 12; got != want {
		t.Fatalf("premise broken: expected %d rendered matches on an intact dump, got %d:\n%s",
			want, got, before)
	}
	if got := searchHeaderTotal(t, before); got != 12 {
		t.Fatalf("premise broken: expected a header count of 12 on an intact dump, got %d:\n%s",
			got, before)
	}

	dropSearchModules(t, dir, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)

	text := runSearch(t, index, "Процедура", 50)
	shown := searchRenderedMatches(text)
	if shown != 0 {
		t.Fatalf("premise broken: no module is left on disk, so nothing can be rendered, got %d:\n%s",
			shown, text)
	}

	// The defect, stated as a property: an answer may not assert a count of
	// matches it does not show without accounting for the difference.
	claimed := searchHeaderTotal(t, text)
	if claimed > shown && !strings.Contains(text, reloadRemedyMarker) {
		t.Errorf("body claims %d matches, shows %d, and never says why %d are missing "+
			"nor how to get them back:\n%s", claimed, shown, claimed-shown, text)
	}
	// "Ничего не найдено" is an answer about the QUERY: it says the code does not
	// contain what was asked for. Here the code does contain it and the files are
	// gone, which is a different fact with a different remedy.
	if strings.Contains(text, "Ничего не найдено") && claimed > 0 {
		t.Errorf("body says the query found nothing while its own header counts %d matches:\n%s",
			claimed, text)
	}
}

// TestSearchCode_DroppedHitsAreNotSoldAsLimitTruncation covers the half-deleted
// dump, where the shortfall note DOES fire but names the wrong cause. Raising
// limit cannot bring back a module whose file is gone, so a body that offers
// only that remedy sends the caller into a loop of ever larger limits against an
// answer that cannot grow.
func TestSearchCode_DroppedHitsAreNotSoldAsLimitTruncation(t *testing.T) {
	dir := mkSearchDump(t, 12)
	index := openSearchIndex(t, dir)

	// Warm every module through the reader, so the drop below is provably the
	// revalidation refusing a vanished file and not a cache that never held it.
	if got := searchRenderedMatches(runSearch(t, index, "Процедура", 50)); got != 12 {
		t.Fatalf("premise broken: expected 12 rendered matches on an intact dump, got %d", got)
	}

	dropSearchModules(t, dir, 0, 2, 4, 6, 8, 10)

	text := runSearch(t, index, "Процедура", 50)
	shown := searchRenderedMatches(text)
	claimed := searchHeaderTotal(t, text)
	if shown != 6 || claimed != 12 {
		t.Fatalf("premise broken: expected 6 of a claimed 12 to survive, got %d of %d:\n%s",
			shown, claimed, text)
	}

	if !strings.Contains(text, reloadRemedyMarker) {
		t.Errorf("6 of 12 matches were dropped because their files are gone, and the body "+
			"never names the remedy for that (%s):\n%s", reloadRemedyMarker, text)
	}
	if strings.Contains(text, limitRemedyMarker) {
		t.Errorf("nothing was cut by limit here (limit 50, 12 matches), so the body must not "+
			"offer a bigger limit as the remedy:\n%s", text)
	}
}

// TestSearchCode_PlainTruncationKeepsTheLimitRemedy is the negative control for
// the pair above. It passes both before and after the fix on purpose: it pins
// that the honest-truncation path stays exactly as it was and does not start
// telling a caller with a healthy dump to re-dump it.
func TestSearchCode_PlainTruncationKeepsTheLimitRemedy(t *testing.T) {
	dir := mkSearchDump(t, 12)
	index := openSearchIndex(t, dir)

	text := runSearch(t, index, "Процедура", 3)
	if got := searchRenderedMatches(text); got != 3 {
		t.Fatalf("premise broken: expected the limit to cut the answer to 3, got %d:\n%s", got, text)
	}
	if !strings.Contains(text, limitRemedyMarker) {
		t.Errorf("a limit-truncated answer must keep offering a bigger limit:\n%s", text)
	}
	if strings.Contains(text, reloadRemedyMarker) {
		t.Errorf("the dump is intact, so nothing must suggest re-dumping it:\n%s", text)
	}
	if got := searchHeaderTotal(t, text); got != 12 {
		t.Errorf("a limit-truncated answer must still report the full index count, got %d:\n%s",
			got, text)
	}
}

// TestSearchCode_BothCausesAtOnceNameBothRemedies is the case the two-cause
// distinction exists for: the limit cut the answer AND some of what it selected
// could not be read. One remedy alone is wrong in both directions, so the body
// has to carry both.
func TestSearchCode_BothCausesAtOnceNameBothRemedies(t *testing.T) {
	dir := mkSearchDump(t, 12)
	index := openSearchIndex(t, dir)

	if got := searchRenderedMatches(runSearch(t, index, "Процедура", 50)); got != 12 {
		t.Fatalf("premise broken: expected 12 rendered matches on an intact dump, got %d", got)
	}
	dropSearchModules(t, dir, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)

	text := runSearch(t, index, "Процедура", 4)
	if got := searchRenderedMatches(text); got != 0 {
		t.Fatalf("premise broken: every module is gone, got %d rendered:\n%s", got, text)
	}
	if !strings.Contains(text, reloadRemedyMarker) {
		t.Errorf("the four hits the limit selected were all unreadable, and the body never "+
			"names that remedy:\n%s", text)
	}
	if !strings.Contains(text, limitRemedyMarker) {
		t.Errorf("8 further matches never left the index because of limit=4, and the body "+
			"never names that remedy:\n%s", text)
	}
}
