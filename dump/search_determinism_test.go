package dump

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ONE QUESTION MUST HAVE ONE ANSWER.
//
// bleve's offline builder collects a batch into index.Batch.IndexOps, which is a
// MAP, and executeBatchLOCKED ranges it directly. Go randomises map iteration, so
// the order documents are handed to the segment builder — and therefore the
// internal document numbers they are assigned — is a fresh permutation on every
// build. Internal document number is what breaks a tie between two equal BM25
// scores when nothing else is asked for.
//
// So a corpus with tied scores answered a repeated question differently every
// time, and the shortfall note («Показано 5 из 12») made the disagreement
// invisible: each answer was internally consistent and only differed from the
// others. MEASURED across 12 separate processes each doing a fresh index build,
// comparing the FULL rendered answer byte for byte: 12 distinct answers out of 12
// runs before the sort order below was requested.
//
// The fix asks for the tie-break explicitly rather than leaving it to the engine's
// internal numbering: score descending, then document ID ascending. The document
// ID is the module key, which is stable across builds because it is derived from
// the path, so the second key is a total order over the corpus and the result
// cannot depend on the permutation.

// tiedScoreDump writes count modules with BYTE-IDENTICAL content.
//
// Identical content is the whole construction: it makes every document's BM25
// score identical, so the tie-break is the ONLY thing that can order them. A
// fixture whose scores differ would be ordered correctly by score alone and would
// pass whether or not a tie-break exists.
func tiedScoreDump(t *testing.T, count int) string {
	t.Helper()
	root := t.TempDir()
	const body = "Процедура ОбщаяПроцедура()\n\tСообщить(\"ключеваястрока\");\nКонецПроцедуры\n"
	for i := 0; i < count; i++ {
		dir := filepath.Join(root, "CommonModules", fmt.Sprintf("Модуль%02d", i), "Ext")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Module.bsl"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// answerOf renders the ordered module list of one search. It is the ORDER as well
// as the set: joining with a separator means a permutation is a different string.
func answerOf(t *testing.T, idx *Index, limit int) string {
	t.Helper()
	matches, _, err := idx.SearchWithStats(SearchParams{
		Query: "ключеваястрока",
		Mode:  SearchModeSmart,
		Limit: limit,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var b strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&b, "%s|%.6f\n", m.Module, m.Score)
	}
	return b.String()
}

// TestTiedScoresAnswerTheSameQuestionTheSameWayAcrossFreshBuilds.
//
// Each iteration gets a fresh dump AND a fresh cache directory, so each one is a
// genuinely cold build that re-runs the map range that permutes the numbering. A
// single build repeated would re-read one segment and agree with itself no matter
// what the tie-break was.
func TestTiedScoresAnswerTheSameQuestionTheSameWayAcrossFreshBuilds(t *testing.T) {
	const builds = 10
	const tied = 12
	// BELOW the number of tied documents on purpose: the tie-break then decides
	// WHICH documents are in the answer at all, not merely what order they take.
	const limit = 5

	answers := map[string]int{}
	var first string
	for i := 0; i < builds; i++ {
		root := tiedScoreDump(t, tied)
		idx, err := NewIndex(root, t.TempDir(), false)
		if err != nil {
			t.Fatalf("build %d: NewIndex: %v", i, err)
		}
		<-idx.Done()
		a := answerOf(t, idx, limit)
		idx.Close()

		if i == 0 {
			first = a
			// The premise: the answer must actually be limited by the tie, or
			// there is no tie to break and this test proves nothing.
			if n := strings.Count(a, "\n"); n != limit {
				t.Fatalf("premise moved: the answer holds %d rows, want %d", n, limit)
			}
		}
		answers[a]++
	}

	if len(answers) != 1 {
		var b strings.Builder
		for a, n := range answers {
			fmt.Fprintf(&b, "\n--- seen %d time(s) ---\n%s", n, a)
		}
		t.Errorf("%d fresh builds produced %d DISTINCT answers to one question, want 1.%s",
			builds, len(answers), b.String())
	}

	// The scores really are tied, checked rather than assumed. If they were not,
	// score alone would order the result and the tie-break would be untested.
	var scores []string
	for _, line := range strings.Split(strings.TrimSpace(first), "\n") {
		if _, s, ok := strings.Cut(line, "|"); ok {
			scores = append(scores, s)
		}
	}
	if len(scores) < 2 {
		t.Fatalf("only %d scores to compare", len(scores))
	}
	for _, s := range scores {
		if s != scores[0] {
			t.Fatalf("the fixture's scores are NOT tied (%v), so the tie-break was never "+
				"exercised and a green result here means nothing", scores)
		}
	}
}

// TestTheTieIsBrokenByDocumentIDAscending states WHICH answer the tie-break picks,
// not merely that it picks one consistently.
//
// A tie-break that was stable but arbitrary would satisfy the test above while
// leaving the choice unexplainable to a user looking at it. Sorting by document ID
// ascending after score means the answer is the first `limit` module keys in
// collation order, which is a rule that can be stated in a sentence.
func TestTheTieIsBrokenByDocumentIDAscending(t *testing.T) {
	const tied = 12
	const limit = 5
	root := tiedScoreDump(t, tied)
	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	<-idx.Done()

	matches, _, err := idx.SearchWithStats(SearchParams{
		Query: "ключеваястрока",
		Mode:  SearchModeSmart,
		Limit: limit,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// The expected ORDER is derived, and no score literal appears anywhere here.
	// A BM25 value is a function of corpus statistics; writing one down would pin
	// this test to a number nobody re-derives and would fail for a reason that has
	// nothing to do with the tie-break.
	var got, want []string
	for i, m := range matches {
		got = append(got, m.Module)
		_ = i
	}
	for i := 0; i < limit; i++ {
		want = append(want, fmt.Sprintf("ОбщийМодуль.Модуль%02d.Модуль", i))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tied scores resolved to\n  %v\nwant the lowest document IDs in order\n  %v", got, want)
	}

	// The tie is real, checked here rather than assumed, and checked WITHOUT
	// naming the value: every score in the answer equals the first one.
	for _, m := range matches {
		if m.Score != matches[0].Score {
			t.Fatalf("the scores are not tied (%v vs %v), so document ID never decided the order",
				m.Score, matches[0].Score)
		}
	}
}
