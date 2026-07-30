package dump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeBSLTB writes content to base/relPath, creating parent directories, and
// returns the absolute path. It is the testing.TB twin of mkBSLFile so the same
// fixture can be built from a benchmark.
func writeBSLTB(tb testing.TB, base, relPath, content string) string {
	tb.Helper()
	full := filepath.Join(base, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		tb.Fatal(err)
	}
	return full
}

// waitReadyTB blocks until idx.Ready() returns true or timeout expires.
// testing.TB twin of waitReady, usable from benchmarks.
func waitReadyTB(tb testing.TB, idx *Index, timeout time.Duration) {
	tb.Helper()
	deadline := time.After(timeout)
	for !idx.Ready() {
		select {
		case <-deadline:
			tb.Fatal("timed out waiting for index to become ready")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// setModTime pins path's modification time to want. Tests use it instead of
// sleeping past the filesystem timestamp granularity: a real edit moves mtime
// the same way, forcing it only removes clock flakiness from the fixture.
func setModTime(tb testing.TB, path string, want time.Time) {
	tb.Helper()
	// A zero atime leaves the access time untouched (os.Chtimes contract).
	if err := os.Chtimes(path, time.Time{}, want); err != nil {
		tb.Fatalf("os.Chtimes(%q): %v", path, err)
	}
}

// statTB stats path and fails the test if it cannot be stat'ed.
func statTB(tb testing.TB, path string) os.FileInfo {
	tb.Helper()
	info, err := os.Stat(path)
	if err != nil {
		tb.Fatalf("os.Stat(%q): %v", path, err)
	}
	return info
}

const (
	revalRelPath = "Catalogs/Номенклатура/Ext/ObjectModule.bsl"
	revalDocID   = "Справочник.Номенклатура.МодульОбъекта"
)

// TestGetContent_RevalidatesAfterOverwrite pins the contract that a cached
// module is re-read when the file behind it changes on disk. Each case exercises
// one leg of the mtime-or-size comparison so a fix that only looks at one of them
// cannot pass the whole table.
func TestGetContent_RevalidatesAfterOverwrite(t *testing.T) {
	cases := []struct {
		name            string
		before          string
		after           string
		keepModTime     bool
		wantSameSize    bool
		wantSameModTime bool
	}{
		{
			name:   "modtime and size both move",
			before: "Процедура Старая()\nКонецПроцедуры\n",
			after:  "Процедура Новая()\n\t// добавленная строка\nКонецПроцедуры\n",
		},
		{
			name:            "size moves while modtime is preserved",
			before:          "Процедура Старая()\nКонецПроцедуры\n",
			after:           "Процедура Новая()\n\t// добавленная строка\nКонецПроцедуры\n",
			keepModTime:     true,
			wantSameModTime: true,
		},
		{
			name:         "modtime moves while size stays identical",
			before:       "Процедура Ноль()\nКонецПроцедуры\n",
			after:        "Процедура Один()\nКонецПроцедуры\n",
			wantSameSize: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			full := writeBSLTB(t, dir, revalRelPath, tc.before)

			idx, err := NewIndex(dir, "", false)
			if err != nil {
				t.Fatalf("NewIndex: %v", err)
			}
			defer idx.Close()
			waitReadyTB(t, idx, 30*time.Second)

			got, ok := idx.GetContent(revalDocID)
			if !ok {
				t.Fatalf("GetContent(%q) = not found before the rewrite; the fixture never loaded", revalDocID)
			}
			if got != tc.before {
				t.Fatalf("first GetContent returned %q, want the fixture %q", got, tc.before)
			}

			infoBefore := statTB(t, full)
			if err := os.WriteFile(full, []byte(tc.after), 0o644); err != nil {
				t.Fatalf("rewriting %q: %v", full, err)
			}
			if tc.keepModTime {
				setModTime(t, full, infoBefore.ModTime())
			} else {
				setModTime(t, full, infoBefore.ModTime().Add(2*time.Second))
			}

			// Prove the fixture built the state this case is named after, so a
			// case that silently degenerated into another one is caught here and
			// not mistaken for a passing assertion below.
			infoAfter := statTB(t, full)
			gotSameSize := infoAfter.Size() == infoBefore.Size()
			gotSameModTime := infoAfter.ModTime().UnixMilli() == infoBefore.ModTime().UnixMilli()
			if gotSameSize != tc.wantSameSize {
				t.Fatalf("fixture: size identical after rewrite = %v, want %v (%d -> %d bytes)",
					gotSameSize, tc.wantSameSize, infoBefore.Size(), infoAfter.Size())
			}
			if gotSameModTime != tc.wantSameModTime {
				t.Fatalf("fixture: modtime identical after rewrite = %v, want %v (%d -> %d ms)",
					gotSameModTime, tc.wantSameModTime,
					infoBefore.ModTime().UnixMilli(), infoAfter.ModTime().UnixMilli())
			}

			got, ok = idx.GetContent(revalDocID)
			if !ok {
				t.Fatalf("GetContent(%q) = not found after the rewrite", revalDocID)
			}
			if got != tc.after {
				t.Errorf("GetContent served a stale copy after the file changed on disk:\n got: %q\nwant: %q", got, tc.after)
			}
		})
	}
}

// TestGetContent_DeletedFileIsNotServedFromCache pins the deliberate fail-closed
// half of revalidation: once the file is gone the module is reported as missing
// rather than served from the copy that happens to still be in memory. Serving a
// deleted module would be the same staleness bug in a different disguise, and it
// matches how an uncached module whose file is unreadable is already treated.
func TestGetContent_DeletedFileIsNotServedFromCache(t *testing.T) {
	dir := t.TempDir()
	full := writeBSLTB(t, dir, revalRelPath, "Процедура Старая()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReadyTB(t, idx, 30*time.Second)

	if _, ok := idx.GetContent(revalDocID); !ok {
		t.Fatalf("GetContent(%q) = not found before the delete; the cache was never warmed", revalDocID)
	}

	if err := os.Remove(full); err != nil {
		t.Fatalf("removing %q: %v", full, err)
	}

	if content, ok := idx.GetContent(revalDocID); ok {
		t.Errorf("GetContent still served %q after its file was deleted: %q", revalDocID, content)
	}
}

// TestContentForScan_RevalidatesAfterOverwrite covers the second cache-first
// reader: the regex/exact scan path. It warms the cache through GetContent first,
// because after a cold build contentByName is empty (see buildShards) and the
// scan would otherwise read from disk anyway and prove nothing.
func TestContentForScan_RevalidatesAfterOverwrite(t *testing.T) {
	const oldToken = "СтарыйМаркерПоиска"
	const newToken = "СвежийМаркерПоиска"

	dir := t.TempDir()
	full := writeBSLTB(t, dir, revalRelPath, "Процедура "+oldToken+"()\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReadyTB(t, idx, 30*time.Second)

	if _, ok := idx.GetContent(revalDocID); !ok {
		t.Fatalf("GetContent(%q) = not found; the cache was never warmed", revalDocID)
	}

	// Positive control: while the file still holds the old token, the exact scan
	// finds it. Without this, a fix that broke exact search outright would make
	// the stale-token assertion below pass for the wrong reason.
	ms, _, err := idx.Search(SearchParams{Query: oldToken, Mode: SearchModeExact, Limit: 50})
	if err != nil {
		t.Fatalf("exact search before the rewrite: %v", err)
	}
	if len(ms) == 0 {
		t.Fatalf("fixture did not fire: exact search found no %q while the file still contained it", oldToken)
	}

	infoBefore := statTB(t, full)
	if err := os.WriteFile(full, []byte("Процедура "+newToken+"()\nКонецПроцедуры\n"), 0o644); err != nil {
		t.Fatalf("rewriting %q: %v", full, err)
	}
	setModTime(t, full, infoBefore.ModTime().Add(2*time.Second))

	ms, _, err = idx.Search(SearchParams{Query: newToken, Mode: SearchModeExact, Limit: 50})
	if err != nil {
		t.Fatalf("exact search for the new token: %v", err)
	}
	if len(ms) == 0 {
		t.Errorf("exact search did not see %q: the scan was served the stale cached copy", newToken)
	}

	ms, _, err = idx.Search(SearchParams{Query: oldToken, Mode: SearchModeExact, Limit: 50})
	if err != nil {
		t.Fatalf("exact search for the removed token: %v", err)
	}
	if len(ms) != 0 {
		t.Errorf("exact search still reports %q, which the file no longer contains: %+v", oldToken, ms)
	}
}

// TestSearchSmart_DoesNotReportLineItDidNotFind pins the invariant that a smart
// hit never quotes source that does not contain the query. The bleve shard is
// left holding a token the file no longer has; the reader must either locate the
// term for real or say it could not, but never label the head of the file as the
// match.
//
// The content cache is deliberately NOT warmed here: after a cold build
// contentByName is empty, so both the current and the fixed reader see the
// rewritten file. What is stale in this test is the bleve index, not the cache.
func TestSearchSmart_DoesNotReportLineItDidNotFind(t *testing.T) {
	const token = "Абракадабрариум"

	dir := t.TempDir()
	full := writeBSLTB(t, dir, revalRelPath,
		"Процедура "+token+"()\n\tВозврат;\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReadyTB(t, idx, 30*time.Second)

	infoBefore := statTB(t, full)
	const rewritten = "Процедура СовершенноДругаяПроцедура()\n\tВозврат;\nКонецПроцедуры\n"
	if err := os.WriteFile(full, []byte(rewritten), 0o644); err != nil {
		t.Fatalf("rewriting %q: %v", full, err)
	}
	setModTime(t, full, infoBefore.ModTime().Add(2*time.Second))
	if strings.Contains(strings.ToLower(rewritten), strings.ToLower(token)) {
		t.Fatalf("fixture is wrong: the rewritten file still contains %q", token)
	}

	matches, _, err := idx.Search(SearchParams{Query: token, Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatalf("smart search: %v", err)
	}

	var hit *Match
	for i := range matches {
		if matches[i].Module == revalDocID {
			hit = &matches[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("fixture did not fire: bleve no longer returns %q for the vanished token %q, "+
			"so this test cannot observe the reporting path at all", revalDocID, token)
	}

	if hit.Line > 0 && !strings.Contains(strings.ToLower(hit.Context), strings.ToLower(token)) {
		t.Errorf("smart search reported %s line %d as a hit for %q, but the quoted context does not contain it:\n%s",
			hit.Module, hit.Line, token, hit.Context)
	}
	if hit.Line == 0 && hit.Context != "" {
		t.Errorf("a hit whose line could not be located must not quote file content, got:\n%s", hit.Context)
	}
}

// TestSearchSmart_UnlocatableLineOnUnchangedFile pins the same invariant on a
// file nothing ever touched, and pins that the module is still reported.
//
// A punctuated query is enough to reach the unlocatable branch: bleve analyses
// the query with the BSL analyzer (unicode tokenizer, so "ПередЗаписью()" is the
// token "передзаписью" and matches), while the line picker scans for the raw
// strings.Fields token "передзаписью()", which no line contains. So an
// unlocatable line is an ordinary outcome on a current dump and not a symptom of
// a changed file, which is why such a hit is reported rather than dropped:
// dropping it would lose a genuine match for any query carrying punctuation.
func TestSearchSmart_UnlocatableLineOnUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	writeBSLTB(t, dir, revalRelPath,
		"// шапка модуля\n// вторая строка\n// третья строка\n// четвёртая\n"+
			"Процедура ПередЗаписью(Отказ)\n\tВозврат;\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReadyTB(t, idx, 30*time.Second)

	const query = "ПередЗаписью()"
	matches, _, err := idx.Search(SearchParams{Query: query, Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatalf("smart search: %v", err)
	}

	var hit *Match
	for i := range matches {
		if matches[i].Module == revalDocID {
			hit = &matches[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("smart search dropped %q for query %q; a module that matches the index "+
			"must stay in the result even when the exact line cannot be located", revalDocID, query)
	}

	if hit.Line > 0 && !strings.Contains(strings.ToLower(hit.Context), strings.ToLower(query)) {
		t.Errorf("smart search reported %s line %d as a hit for %q, but the quoted context does not contain it:\n%s",
			hit.Module, hit.Line, query, hit.Context)
	}
	if hit.Line == 0 && hit.Context != "" {
		t.Errorf("a hit whose line could not be located must not quote file content, got:\n%s", hit.Context)
	}
}
