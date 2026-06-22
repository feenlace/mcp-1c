package dump

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// chanClosed reports whether ch is already closed, without blocking.
func chanClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// assertCloseReturns fails if idx.Close() does not return within d — i.e. if its
// <-idx.done wait deadlocks because FinishServeOpen failed to close done.
func assertCloseReturns(t *testing.T, idx *Index, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- idx.Close() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("Close() did not return — likely deadlocked on <-idx.done (done never closed)")
	}
}

// waitGroupWithin fails if wg does not drain within d.
func waitGroupWithin(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %s", what, d)
	}
}

// TestServePlaceholder_FinishSuccess covers the placeholder-then-finish happy
// path: a fresh placeholder honors the not-ready contract, and FinishServeOpen
// flips Ready(), closes Done(), and makes search/content work — all IN PLACE on
// the same *Index a caller would have already handed out.
func TestServePlaceholder_FinishSuccess(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\n\t// маркерПлейсхолдер\nКонецПроцедуры\n")

	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}

	ph := NewServePlaceholder(dir)
	t.Cleanup(func() { ph.Close() })

	// --- not-ready contract before FinishServeOpen ---
	if ph.Ready() {
		t.Fatal("placeholder Ready() must be false before FinishServeOpen")
	}
	if chanClosed(ph.Done()) {
		t.Fatal("placeholder Done() must be OPEN before FinishServeOpen")
	}
	if err := ph.BuildError(); err != nil {
		t.Fatalf("placeholder BuildError() must be nil, got %v", err)
	}
	if _, _, err := ph.Search(SearchParams{Query: "маркерПлейсхолдер", Mode: SearchModeSmart, Limit: 10}); err == nil {
		t.Fatal("Search on a building placeholder must return an error, not empty results")
	} else if !strings.Contains(err.Error(), "building") {
		t.Fatalf("building-placeholder Search error should mention building, got %v", err)
	}
	if _, ok := ph.GetContent("любой.модуль"); ok {
		t.Fatal("GetContent on a building placeholder must report not-found")
	}
	if ph.GetPathIndex() != nil {
		t.Fatal("GetPathIndex on a building placeholder must be nil")
	}

	// --- finish in place ---
	ph.FinishServeOpen(cacheDir, gensig, nil)

	if !ph.Ready() {
		t.Fatalf("Ready() must be true after a successful FinishServeOpen; BuildError=%v", ph.BuildError())
	}
	if !chanClosed(ph.Done()) {
		t.Fatal("Done() must be CLOSED after FinishServeOpen")
	}
	if err := ph.BuildError(); err != nil {
		t.Fatalf("BuildError() must be nil after success, got %v", err)
	}
	if ph.GetPathIndex() == nil {
		t.Fatal("GetPathIndex() must be non-nil after a successful finish")
	}

	names := ph.ModuleNames()
	if len(names) == 0 {
		t.Fatal("ModuleNames() must be non-empty after a successful finish")
	}
	if got := ph.ModuleCount(); got != len(names) {
		t.Fatalf("ModuleCount()=%d disagrees with len(ModuleNames())=%d", got, len(names))
	}
	if content, ok := ph.GetContent(names[0]); !ok || content == "" {
		t.Fatalf("GetContent(%q) after finish: ok=%v len=%d (want non-empty content)", names[0], ok, len(content))
	}

	matches, total, err := ph.Search(SearchParams{Query: "маркерПлейсхолдер", Mode: SearchModeSmart, Limit: 10})
	if err != nil {
		t.Fatalf("Search after finish: %v", err)
	}
	if total < 1 || len(matches) < 1 {
		t.Fatalf("Search after finish found nothing (total=%d, matches=%d)", total, len(matches))
	}
}

// TestServePlaceholder_FinishFailurePrepErr covers the caller-supplied build
// failure funnel: FinishServeOpen(prepErr!=nil) must record BuildError(), keep
// Ready()==false, still close Done(), surface the error through Search(), and let
// Close() return (no deadlock).
func TestServePlaceholder_FinishFailurePrepErr(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()

	ph := NewServePlaceholder(dir)
	sentinel := errors.New("simulated background build failure")
	ph.FinishServeOpen(cacheDir, "deadbeefdeadbeef", sentinel)

	if ph.Ready() {
		t.Fatal("Ready() must stay false after a failed FinishServeOpen")
	}
	if !chanClosed(ph.Done()) {
		t.Fatal("Done() must close even on a failed FinishServeOpen (else waiters/Close hang)")
	}
	be := ph.BuildError()
	if be == nil {
		t.Fatal("BuildError() must be set after a failed FinishServeOpen")
	}
	if !errors.Is(be, sentinel) {
		t.Fatalf("BuildError() must carry the prepErr; got %v", be)
	}
	if _, _, err := ph.Search(SearchParams{Query: "x", Mode: SearchModeSmart, Limit: 1}); err == nil {
		t.Fatal("Search after a failed finish must return the build error, not empty results")
	}
	assertCloseReturns(t, ph, 10*time.Second)
}

// TestServePlaceholder_FinishMissingGeneration covers prepErr==nil but no READY
// generation on disk for gensig: FinishServeOpen must fail closed (BuildError set,
// not ready, Done closed, Close returns) rather than attach a partial generation.
func TestServePlaceholder_FinishMissingGeneration(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тест/Ext/ObjectModule.bsl", "Процедура П()\nКонецПроцедуры\n")

	ph := NewServePlaceholder(dir)
	// A gensig that was never built — generationReadyDir is false.
	ph.FinishServeOpen(cacheDir, "0000000000000000", nil)

	if ph.Ready() {
		t.Fatal("Ready() must stay false when the generation is missing")
	}
	if !chanClosed(ph.Done()) {
		t.Fatal("Done() must close when the generation is missing")
	}
	if be := ph.BuildError(); be == nil {
		t.Fatal("BuildError() must be set when the generation is missing")
	} else if !strings.Contains(be.Error(), "not ready") {
		t.Fatalf("missing-generation BuildError should explain the missing sentinel, got %v", be)
	}
	assertCloseReturns(t, ph, 10*time.Second)
}

// TestServePlaceholder_ConcurrentWaitersAndFinish is the -race guard: many
// Done()-waiters plus many readers hammer the placeholder while FinishServeOpen
// populates it in place. It proves (a) done is closed exactly once and observable
// by every waiter, (b) the release-store/acquire-load on ready publishes shards
// and names with no torn read, and (c) no reader touches an unsynchronized field.
// Run with -race for it to mean anything.
func TestServePlaceholder_ConcurrentWaitersAndFinish(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\n\t// маркерГонки\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Documents/Реализация/Ext/ObjectModule.bsl",
		"Процедура ОбработкаПроведения(Отказ)\nКонецПроцедуры\n")

	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}

	ph := NewServePlaceholder(dir)
	t.Cleanup(func() { ph.Close() })

	const waiters = 16
	const hammers = 8

	var waitersWG sync.WaitGroup
	readyObserved := make([]bool, waiters)
	errObserved := make([]error, waiters)
	for i := range waiters {
		waitersWG.Add(1)
		go func(i int) {
			defer waitersWG.Done()
			<-ph.Done() // must unblock once FinishServeOpen closes done
			readyObserved[i] = ph.Ready()
			errObserved[i] = ph.BuildError()
		}(i)
	}

	var hammersWG sync.WaitGroup
	stop := make(chan struct{})
	for range hammers {
		hammersWG.Add(1)
		go func() {
			defer hammersWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = ph.Ready()
				_, _, _ = ph.Search(SearchParams{Query: "маркерГонки", Mode: SearchModeSmart, Limit: 5})
				_ = ph.ModuleNames()
				_ = ph.ModuleCount()
				_, _ = ph.GetContent("Справочник.Номенклатура.МодульОбъекта")
				_ = ph.GetPathIndex()
				_ = ph.BuildError()
			}
		}()
	}

	// Let the readers spin against the not-ready placeholder, then finish in place.
	time.Sleep(20 * time.Millisecond)
	ph.FinishServeOpen(cacheDir, gensig, nil)

	waitGroupWithin(t, &waitersWG, 30*time.Second, "Done()-waiters")
	close(stop)
	waitGroupWithin(t, &hammersWG, 30*time.Second, "reader hammerers")

	if !ph.Ready() {
		t.Fatalf("Ready() must be true after finish; BuildError=%v", ph.BuildError())
	}
	for i := range waiters {
		if !readyObserved[i] {
			t.Fatalf("waiter %d unblocked but observed Ready()==false (err=%v)", i, errObserved[i])
		}
		if errObserved[i] != nil {
			t.Fatalf("waiter %d observed BuildError()=%v after a successful finish", i, errObserved[i])
		}
	}
}
