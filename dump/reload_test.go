package dump

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// openReloadableIndex builds a generation for dir and opens it read-only, which
// is exactly what the serve path does. It returns an index Reload can act on.
func openReloadableIndex(t *testing.T, dir, cacheDir string) *Index {
	t.Helper()
	gensig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	idx, err := OpenGenerationReadOnly(dir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("OpenGenerationReadOnly: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	waitReady(t, idx, 60*time.Second)
	if err := idx.BuildError(); err != nil {
		t.Fatalf("build error after open: %v", err)
	}
	return idx
}

// countAllModes runs the same query in all three search modes and returns the
// reported total for each. The three are checked TOGETHER on purpose: the freezes
// this reload exists to clear are independent, so a fix that clears one and not
// the others must fail here rather than look green.
func countAllModes(t *testing.T, idx *Index, query string) (smart, regex, exact int) {
	t.Helper()
	for _, tc := range []struct {
		mode SearchMode
		out  *int
	}{
		{SearchModeSmart, &smart},
		{SearchModeRegex, &regex},
		{SearchModeExact, &exact},
	} {
		_, total, err := idx.Search(SearchParams{Query: query, Mode: tc.mode, Limit: 50})
		if err != nil {
			t.Fatalf("search %s %q: %v", tc.mode, query, err)
		}
		*tc.out = total
	}
	return smart, regex, exact
}

func mustReload(t *testing.T, idx *Index) ReloadReport {
	t.Helper()
	rep, err := idx.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return rep
}

// fixtureDump writes a small but structurally real dump and returns its dir.
func fixtureDump(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\n\t// базовыйМаркер\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Documents/Реализация/Ext/ObjectModule.bsl",
		"Процедура ОбработкаПроведения(Отказ)\n\t// базовыйМаркер\nКонецПроцедуры\n")
	return dir
}

// TestReload_PicksUpAddedModule is acceptance step 1: a .bsl added after the
// index was opened must become findable in ALL THREE modes after a reload.
//
// Without the reload this is red three times over: regex and exact iterate the
// frozen name list and never look at the new file, and smart has no bleve
// document for it.
func TestReload_PicksUpAddedModule(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	idx := openReloadableIndex(t, dir, cacheDir)

	const marker = "УникальныйМаркерДобавленногоМодуля"
	if s, r, e := countAllModes(t, idx, marker); s != 0 || r != 0 || e != 0 {
		t.Fatalf("marker present before it was written: smart=%d regex=%d exact=%d", s, r, e)
	}

	mkBSLFile(t, dir, "CommonModules/НовыйМодуль/Ext/Module.bsl",
		"Процедура "+marker+"() Экспорт\nКонецПроцедуры\n")

	// Still frozen: no reload yet. This is the defect the tool exists to fix, and
	// asserting it here keeps the test honest about what the reload changed.
	if s, r, e := countAllModes(t, idx, marker); s != 0 || r != 0 || e != 0 {
		t.Fatalf("added module was already visible WITHOUT a reload: smart=%d regex=%d exact=%d", s, r, e)
	}

	rep := mustReload(t, idx)
	if !rep.Changed || !rep.Rebuilt {
		t.Fatalf("reload after adding a file: Changed=%v Rebuilt=%v, want both true", rep.Changed, rep.Rebuilt)
	}
	if rep.ModulesBefore != 2 || rep.ModulesAfter != 3 {
		t.Fatalf("module counts: before=%d after=%d, want 2 and 3", rep.ModulesBefore, rep.ModulesAfter)
	}

	smart, regex, exact := countAllModes(t, idx, marker)
	if smart == 0 || regex == 0 || exact == 0 {
		t.Fatalf("added module not found after reload: smart=%d regex=%d exact=%d", smart, regex, exact)
	}
}

// TestReload_PicksUpDeletedModule is acceptance step 2: after deleting the file
// and reloading, the marker must be gone from all three modes.
//
// Note which mode is actually red without the reload: regex and exact already
// report zero the moment the file stops reading, but SMART keeps counting the
// deleted document because it is still in the shard.
func TestReload_PicksUpDeletedModule(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	const marker = "УникальныйМаркерУдаляемогоМодуля"
	mkBSLFile(t, dir, "CommonModules/ВременныйМодуль/Ext/Module.bsl",
		"Процедура "+marker+"() Экспорт\nКонецПроцедуры\n")

	idx := openReloadableIndex(t, dir, cacheDir)
	if s, r, e := countAllModes(t, idx, marker); s == 0 || r == 0 || e == 0 {
		t.Fatalf("marker not indexed at open: smart=%d regex=%d exact=%d", s, r, e)
	}

	if err := os.RemoveAll(filepath.Join(dir, "CommonModules", "ВременныйМодуль")); err != nil {
		t.Fatal(err)
	}

	// Smart still counts the deleted document; that is the freeze under test.
	if s, _, _ := countAllModes(t, idx, marker); s == 0 {
		t.Fatal("smart search stopped counting the deleted module WITHOUT a reload; " +
			"this test can no longer prove the reload does it")
	}

	rep := mustReload(t, idx)
	if !rep.Changed {
		t.Fatal("reload after deleting a file reported no change")
	}
	if rep.ModulesAfter != rep.ModulesBefore-1 {
		t.Fatalf("module counts: before=%d after=%d, want after == before-1", rep.ModulesBefore, rep.ModulesAfter)
	}

	smart, regex, exact := countAllModes(t, idx, marker)
	if smart != 0 || regex != 0 || exact != 0 {
		t.Fatalf("deleted module still found after reload: smart=%d regex=%d exact=%d", smart, regex, exact)
	}
}

// TestReload_PicksUpModifiedModule is acceptance step 3: after editing a module,
// the old text must be gone and the new text found, in all three modes.
func TestReload_PicksUpModifiedModule(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	const oldText = "СтарыйТекстМодуля"
	const newText = "НовыйТекстМодуля"
	rel := "CommonModules/ИзменяемыйМодуль/Ext/Module.bsl"
	mkBSLFile(t, dir, rel, "Процедура Тест() Экспорт\n\t// "+oldText+"\nКонецПроцедуры\n")

	idx := openReloadableIndex(t, dir, cacheDir)
	if s, r, e := countAllModes(t, idx, oldText); s == 0 || r == 0 || e == 0 {
		t.Fatalf("old text not indexed at open: smart=%d regex=%d exact=%d", s, r, e)
	}
	if s, r, e := countAllModes(t, idx, newText); s != 0 || r != 0 || e != 0 {
		t.Fatalf("new text present before the edit: smart=%d regex=%d exact=%d", s, r, e)
	}

	mkBSLFile(t, dir, rel, "Процедура Тест() Экспорт\n\t// "+newText+"\nКонецПроцедуры\n")
	// The signature compares mtime+size; both strings are the same length, so pin
	// mtime explicitly rather than trusting filesystem timestamp granularity.
	setModTime(t, filepath.Join(dir, filepath.FromSlash(rel)), time.Now().Add(2*time.Second))

	// Smart is the frozen mode here: it still scores the old text and does not
	// know the new one. Regex and exact already read the file per query.
	if s, _, _ := countAllModes(t, idx, oldText); s == 0 {
		t.Fatal("smart search stopped matching the old text WITHOUT a reload; " +
			"this test can no longer prove the reload does it")
	}

	rep := mustReload(t, idx)
	if !rep.Changed {
		t.Fatal("reload after editing a file reported no change")
	}

	if s, r, e := countAllModes(t, idx, oldText); s != 0 || r != 0 || e != 0 {
		t.Fatalf("old text still found after reload: smart=%d regex=%d exact=%d", s, r, e)
	}
	if s, r, e := countAllModes(t, idx, newText); s == 0 || r == 0 || e == 0 {
		t.Fatalf("new text not found after reload: smart=%d regex=%d exact=%d", s, r, e)
	}
}

// TestReload_UnchangedDumpDoesNoWork guards the honesty requirement: when nothing
// changed on disk the reload must say so and must NOT rebuild.
func TestReload_UnchangedDumpDoesNoWork(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	idx := openReloadableIndex(t, dir, cacheDir)

	before := idx.ModuleCount()
	rep := mustReload(t, idx)
	if rep.Changed || rep.Rebuilt {
		t.Fatalf("unchanged dump: Changed=%v Rebuilt=%v, want both false", rep.Changed, rep.Rebuilt)
	}
	if rep.ModulesBefore != before || rep.ModulesAfter != before {
		t.Fatalf("unchanged dump: before=%d after=%d, want both %d", rep.ModulesBefore, rep.ModulesAfter, before)
	}
	if rep.SigBefore == "" || rep.SigBefore != rep.SigAfter {
		t.Fatalf("unchanged dump: SigBefore=%q SigAfter=%q, want equal and non-empty", rep.SigBefore, rep.SigAfter)
	}
	if idx.ModuleCount() != before {
		t.Fatalf("module count moved on a no-op reload: %d -> %d", before, idx.ModuleCount())
	}
}

// TestReload_ReusesExistingGeneration checks the Rebuilt flag actually measures
// something. It reproduces the real case where a rebuild is unnecessary: the
// generation for the dump's NEW state is already on disk, built by --build-index
// or by a co-located process, so the reload only has to open and attach it.
//
// (Reverting the dump to an EARLIER state does not exercise this: the reload that
// moved away from that state already GC'd its generation, since no reader held it
// any more. That is correct behaviour, not a missing optimisation.)
func TestReload_ReusesExistingGeneration(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	idx := openReloadableIndex(t, dir, cacheDir)

	mkBSLFile(t, dir, "CommonModules/Заранее/Ext/Module.bsl",
		"Процедура Заранее() Экспорт\nКонецПроцедуры\n")
	newSig := mustGenSig(t, dir)

	// Stand in for another process (or an offline --build-index) that already
	// built the generation for the dump's current state.
	if err := BuildGeneration(dir, cacheDir, newSig); err != nil {
		t.Fatalf("pre-building the target generation: %v", err)
	}
	if !GenerationReady(dir, cacheDir, newSig) {
		t.Fatal("the pre-built generation is not READY, the reuse path cannot be exercised")
	}

	rep := mustReload(t, idx)
	if !rep.Changed {
		t.Fatal("reload onto a pre-built generation reported no change")
	}
	if rep.Rebuilt {
		t.Fatal("reload rebuilt a generation that was already READY on disk")
	}
	if rep.SigAfter != newSig {
		t.Fatalf("reload attached %s, want the pre-built %s", rep.SigAfter, newSig)
	}
	if rep.ModulesAfter != rep.ModulesBefore+1 {
		t.Fatalf("module counts: before=%d after=%d", rep.ModulesBefore, rep.ModulesAfter)
	}
}

// TestReload_BuildFailureKeepsOldIndexServing is the failure-injection proof
// required by the brief: when the rebuild fails halfway, the previously working
// index must still serve, in all three modes, with the same module count.
//
// The failure is injected at the build step through reloadBuildGeneration, which
// is the ONLY point where a reload can fail after doing real work and before
// touching the live index.
func TestReload_BuildFailureKeepsOldIndexServing(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	idx := openReloadableIndex(t, dir, cacheDir)

	const marker = "МаркерДоСбоя"
	mkBSLFile(t, dir, "CommonModules/ДоСбоя/Ext/Module.bsl",
		"Процедура "+marker+"() Экспорт\nКонецПроцедуры\n")
	// Reload once so the marker IS in the index; the injected failure then has
	// something real to preserve.
	mustReload(t, idx)
	beforeCount := idx.ModuleCount()
	smartBefore, regexBefore, exactBefore := countAllModes(t, idx, marker)
	if smartBefore == 0 || regexBefore == 0 || exactBefore == 0 {
		t.Fatalf("fixture did not index the marker: smart=%d regex=%d exact=%d",
			smartBefore, regexBefore, exactBefore)
	}
	sigBefore := idx.gensig

	// Now change the dump again AND make the build fail.
	mkBSLFile(t, dir, "CommonModules/ПослеСбоя/Ext/Module.bsl",
		"Процедура ПослеСбоя() Экспорт\nКонецПроцедуры\n")

	injected := errors.New("внедрённый сбой сборки поколения")
	orig := reloadBuildGeneration
	var called atomic.Bool
	reloadBuildGeneration = func(dumpDir, cache, gensig string) (*readerRegistration, error) {
		called.Store(true)
		return nil, injected
	}
	defer func() { reloadBuildGeneration = orig }()

	rep, err := idx.Reload()
	if !called.Load() {
		t.Fatal("the injected build never ran, so this test proves nothing")
	}
	if err == nil {
		t.Fatal("Reload reported success despite a failing build")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("Reload lost the underlying build error: %v", err)
	}
	if rep.Changed {
		t.Fatal("a failed reload reported Changed=true")
	}

	// The whole point: the previously working index is still the one serving.
	if got := idx.ModuleCount(); got != beforeCount {
		t.Fatalf("module count changed after a failed reload: %d -> %d", beforeCount, got)
	}
	if idx.gensig != sigBefore {
		t.Fatalf("attached generation changed after a failed reload: %s -> %s", sigBefore, idx.gensig)
	}
	smart, regex, exact := countAllModes(t, idx, marker)
	if smart != smartBefore || regex != regexBefore || exact != exactBefore {
		t.Fatalf("search results changed after a failed reload: smart %d->%d regex %d->%d exact %d->%d",
			smartBefore, smart, regexBefore, regex, exactBefore, exact)
	}

	// And a reload that succeeds afterwards still works: the failure left no
	// wedged state behind.
	reloadBuildGeneration = orig
	if rep := mustReload(t, idx); !rep.Changed || rep.ModulesAfter != beforeCount+1 {
		t.Fatalf("recovery reload: Changed=%v after=%d, want true and %d",
			rep.Changed, rep.ModulesAfter, beforeCount+1)
	}
}

// TestReload_BuildPanicKeepsOldIndexServing is the same failure-injection proof
// for the failure mode an error return cannot cover: the build PANICS.
//
// It matters because of where a reload runs. Reload is called from the
// reload_dump tool handler, and the MCP SDK does not recover panics raised
// inside a handler, so an unrecovered panic here does not fail one call, it
// takes the whole server process down and with it every other client session.
// cmd/mcp-1c/serve.go already wraps the identical BuildGeneration call in a
// recover for exactly this reason; this test pins the same protection on the
// reload path.
//
// The panic is injected at the build step, the only point where a reload does
// real work before touching the live index, so a recovered panic must leave the
// previous index serving exactly as a returned error does.
func TestReload_BuildPanicKeepsOldIndexServing(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	idx := openReloadableIndex(t, dir, cacheDir)

	const marker = "МаркерДоПаники"
	mkBSLFile(t, dir, "CommonModules/ДоПаники/Ext/Module.bsl",
		"Процедура "+marker+"() Экспорт\nКонецПроцедуры\n")
	mustReload(t, idx)
	beforeCount := idx.ModuleCount()
	smartBefore, regexBefore, exactBefore := countAllModes(t, idx, marker)
	if smartBefore == 0 || regexBefore == 0 || exactBefore == 0 {
		t.Fatalf("fixture did not index the marker: smart=%d regex=%d exact=%d",
			smartBefore, regexBefore, exactBefore)
	}
	sigBefore := idx.gensig

	mkBSLFile(t, dir, "CommonModules/ПослеПаники/Ext/Module.bsl",
		"Процедура ПослеПаники() Экспорт\nКонецПроцедуры\n")

	const panicText = "внедрённая паника сборки поколения"
	orig := reloadBuildGeneration
	var called atomic.Bool
	reloadBuildGeneration = func(dumpDir, cache, gensig string) (*readerRegistration, error) {
		called.Store(true)
		panic(panicText)
	}
	defer func() { reloadBuildGeneration = orig }()

	rep, err := idx.Reload()
	if !called.Load() {
		t.Fatal("the injected build never ran, so this test proves nothing")
	}
	if err == nil {
		t.Fatal("Reload reported success despite a panicking build")
	}
	if !strings.Contains(err.Error(), panicText) {
		t.Errorf("the error does not carry what the build panicked with, so the operator "+
			"cannot tell why the reload failed: %v", err)
	}
	if rep.Changed {
		t.Fatal("a panicking reload reported Changed=true")
	}

	// The whole point: the previously working index is still the one serving.
	if got := idx.ModuleCount(); got != beforeCount {
		t.Fatalf("module count changed after a panicking reload: %d -> %d", beforeCount, got)
	}
	if idx.gensig != sigBefore {
		t.Fatalf("attached generation changed after a panicking reload: %s -> %s", sigBefore, idx.gensig)
	}
	smart, regex, exact := countAllModes(t, idx, marker)
	if smart != smartBefore || regex != regexBefore || exact != exactBefore {
		t.Fatalf("search results changed after a panicking reload: smart %d->%d regex %d->%d exact %d->%d",
			smartBefore, smart, regexBefore, regex, exactBefore, exact)
	}

	// And the index is not wedged: the reload lock was released and a later
	// reload still succeeds. A recover that leaves reloadMu held would turn one
	// panic into a permanently un-reloadable server.
	reloadBuildGeneration = orig
	if rep := mustReload(t, idx); !rep.Changed || rep.ModulesAfter != beforeCount+1 {
		t.Fatalf("recovery reload: Changed=%v after=%d, want true and %d",
			rep.Changed, rep.ModulesAfter, beforeCount+1)
	}
}

// TestReload_RefusesToSwapInAnEmptyIndex guards the "never worse than before"
// rule against the realistic accident: the dump directory momentarily reads as
// empty (an unmounted share, an interrupted dump) and would otherwise build a
// perfectly valid, perfectly useless empty generation.
func TestReload_RefusesToSwapInAnEmptyIndex(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	idx := openReloadableIndex(t, dir, cacheDir)
	before := idx.ModuleCount()
	if before == 0 {
		t.Fatal("fixture index is empty, the guard under test cannot fire")
	}

	for _, sub := range []string{"Catalogs", "Documents"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := idx.Reload()
	if err == nil {
		t.Fatalf("Reload swapped in an empty index (after=%d)", rep.ModulesAfter)
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("unexpected error, want the emptiness guard: %v", err)
	}
	if got := idx.ModuleCount(); got != before {
		t.Fatalf("module count changed despite the refusal: %d -> %d", before, got)
	}
	if _, _, err := idx.Search(SearchParams{Query: "базовыйМаркер", Mode: SearchModeSmart}); err != nil {
		t.Fatalf("index stopped serving after the refusal: %v", err)
	}
}

// TestReload_RefusedWhileNotReady checks a reload cannot race the initial build.
func TestReload_RefusedWhileNotReady(t *testing.T) {
	dir := fixtureDump(t)
	ph := NewServePlaceholder(dir)
	if _, err := ph.Reload(); !errors.Is(err, ErrReloadNotReady) {
		t.Fatalf("Reload on a not-ready index: %v, want ErrReloadNotReady", err)
	}
	ph.FinishServeOpen(t.TempDir(), "нет-такого-поколения", nil)
	<-ph.Done()
	if _, err := ph.Reload(); err == nil {
		t.Fatal("Reload on an index whose build failed reported success")
	}
	ph.Close()
	if _, err := ph.Reload(); !errors.Is(err, ErrReloadClosed) {
		t.Fatalf("Reload on a closed index: %v, want ErrReloadClosed", err)
	}
}

// TestReload_SecondCallerIsToldNotQueued checks reloads are not queued: a second
// concurrent caller gets ErrReloadInProgress instead of paying for a duplicate
// rebuild of the same generation.
func TestReload_SecondCallerIsToldNotQueued(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	idx := openReloadableIndex(t, dir, cacheDir)
	mkBSLFile(t, dir, "CommonModules/Ещё/Ext/Module.bsl", "Процедура Ещё() Экспорт\nКонецПроцедуры\n")

	release := make(chan struct{})
	entered := make(chan struct{})
	orig := reloadBuildGeneration
	reloadBuildGeneration = func(dumpDir, cache, gensig string) (*readerRegistration, error) {
		close(entered)
		<-release
		return orig(dumpDir, cache, gensig)
	}
	defer func() { reloadBuildGeneration = orig }()

	done := make(chan error, 1)
	go func() { _, err := idx.Reload(); done <- err }()
	<-entered

	if _, err := idx.Reload(); !errors.Is(err, ErrReloadInProgress) {
		t.Fatalf("second concurrent Reload: %v, want ErrReloadInProgress", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first Reload: %v", err)
	}
}

// TestReload_ConcurrentSearchesNeverPanicOrEmpty is the single most important
// property in this unit: a reload landing under a stream of searches must not
// panic (a panic in a search goroutine would kill the whole process) and must not
// turn a query that was returning results into an empty one.
//
// The queries used are all against modules that exist BEFORE and AFTER the
// reload, so any zero is a real regression and not a legitimately vanished hit.
func TestReload_ConcurrentSearchesNeverPanicOrEmpty(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	// Enough modules that a search takes long enough to overlap the swap.
	for i := range 40 {
		mkBSLFile(t, dir, fmt.Sprintf("CommonModules/Модуль%02d/Ext/Module.bsl", i),
			fmt.Sprintf("Процедура Проц%02d() Экспорт\n\t// базовыйМаркер\nКонецПроцедуры\n", i))
	}
	idx := openReloadableIndex(t, dir, cacheDir)

	queries := []SearchParams{
		{Query: "базовыйМаркер", Mode: SearchModeSmart, Limit: 50},
		{Query: "базовыйМаркер", Mode: SearchModeExact, Limit: 50},
		{Query: "Процедура", Mode: SearchModeRegex, Limit: 50},
		{Query: "ПередЗаписью", Mode: SearchModeSmart, Limit: 50},
		// FILTERED queries are not decoration. filterModules returns early on an
		// unfiltered search and never touches the PathIndex, so a load made only of
		// unfiltered queries never exercises the *PathIndex pointer the swap
		// replaces — and a test that never runs that line cannot detect a race on
		// it. One filtered query per mode puts it under load.
		{Query: "базовыйМаркер", Mode: SearchModeExact, Category: "ОбщийМодуль", Limit: 50},
		{Query: "Процедура", Mode: SearchModeRegex, Category: "ОбщийМодуль", Limit: 50},
		{Query: "базовыйМаркер", Mode: SearchModeSmart, Category: "ОбщийМодуль", Limit: 50},
	}
	// Assert every query has hits BEFORE the reload, so "0 afterwards" is
	// unambiguously a regression rather than a query that never matched.
	for _, p := range queries {
		if _, total, err := idx.Search(p); err != nil || total == 0 {
			t.Fatalf("baseline %s %q: total=%d err=%v; the load test needs a non-empty baseline",
				p.Mode, p.Query, total, err)
		}
	}

	var (
		stop     atomic.Bool
		searches atomic.Int64
		empties  atomic.Int64
		failures = make(chan string, 64)
		wg       sync.WaitGroup
	)
	for w := range 8 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					select {
					case failures <- fmt.Sprintf("PANIC in search goroutine %d: %v\n%s", w, r, debug.Stack()):
					default:
					}
				}
			}()
			for !stop.Load() {
				p := queries[(w+int(searches.Load()))%len(queries)]
				_, total, err := idx.Search(p)
				searches.Add(1)
				if err != nil {
					select {
					case failures <- fmt.Sprintf("search %s %q errored during reload: %v", p.Mode, p.Query, err):
					default:
					}
					continue
				}
				if total == 0 {
					empties.Add(1)
					select {
					case failures <- fmt.Sprintf("search %s %q returned EMPTY during reload", p.Mode, p.Query):
					default:
					}
				}
			}
		}(w)
	}

	// Let the load build up, then change the dump and reload underneath it,
	// SEVERAL times. One swap is enough to expose a broken retire order, but not
	// enough to reliably expose an unsynchronised read of a swapped pointer: the
	// race detector compares an unlocked read against the shadow of the last few
	// accesses to that word, and a single write is easily evicted by the stream of
	// legitimate reads around it. Repeating the swap makes the detector's job
	// deterministic instead of lucky.
	const reloads = 4
	var rep ReloadReport
	for i := range reloads {
		time.Sleep(120 * time.Millisecond)
		mkBSLFile(t, dir, fmt.Sprintf("CommonModules/ПодНагрузкой%d/Ext/Module.bsl", i),
			"Процедура ПодНагрузкой() Экспорт\n\t// базовыйМаркер\nКонецПроцедуры\n")
		rep = mustReload(t, idx)
		if !rep.Changed {
			t.Fatalf("reload %d under load reported no change", i)
		}
	}
	time.Sleep(120 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	close(failures)

	var problems []string
	for f := range failures {
		problems = append(problems, f)
	}
	if len(problems) > 0 {
		t.Fatalf("%d problem(s) under load (%d searches, %d empty):\n%s",
			len(problems), searches.Load(), empties.Load(), strings.Join(problems, "\n"))
	}
	if searches.Load() < 100 {
		t.Fatalf("only %d concurrent searches ran; too few to have overlapped the reloads", searches.Load())
	}
	t.Logf("ran %d concurrent searches across %d reloads, last one %d -> %d modules in %v",
		searches.Load(), reloads, rep.ModulesBefore, rep.ModulesAfter, rep.Elapsed)
}

// TestReload_ContentCacheKeepsRuntimeDocuments checks the swap drops file-backed
// cache entries (whose revision the new generation may contradict) while keeping
// documents a caller pushed in at runtime, which have no file to revalidate.
func TestReload_ContentCacheKeepsRuntimeDocuments(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()
	idx := openReloadableIndex(t, dir, cacheDir)

	// One file-backed entry, populated the ordinary way.
	names := idx.ModuleNames()
	if len(names) == 0 {
		t.Fatal("fixture index has no modules")
	}
	if _, ok := idx.GetContent(names[0]); !ok {
		t.Fatalf("GetContent(%q) failed on the fixture", names[0])
	}
	// One runtime entry, of the kind IndexDocWithMeta leaves behind.
	idx.contentMu.Lock()
	idx.contentByName["ext.Тест.Модуль"] = cachedModule{content: "Процедура Расширение()"}
	idx.contentMu.Unlock()

	mkBSLFile(t, dir, "CommonModules/Свежий/Ext/Module.bsl", "Процедура Свежий() Экспорт\nКонецПроцедуры\n")
	mustReload(t, idx)

	idx.contentMu.RLock()
	_, fileKept := idx.contentByName[names[0]]
	runtimeEntry, runtimeKept := idx.contentByName["ext.Тест.Модуль"]
	idx.contentMu.RUnlock()

	if fileKept {
		t.Fatalf("reload kept the file-backed cache entry for %q", names[0])
	}
	if !runtimeKept || runtimeEntry.content != "Процедура Расширение()" {
		t.Fatal("reload dropped a runtime-ingested content entry, which has no file to revalidate")
	}
}

// TestReload_FromLegacyFlatCache checks the path that has no generation to
// compare against: an index opened on a legacy flat cache carries no signature,
// so its first reload must build and attach one rather than short-circuit on an
// empty signature comparison.
func TestReload_FromLegacyFlatCache(t *testing.T) {
	dir := fixtureDump(t)
	cacheDir := t.TempDir()

	idx, err := NewIndex(dir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	waitReady(t, idx, 60*time.Second)
	if idx.gensig != "" {
		t.Fatalf("a flat-cache index reported generation %q, want none", idx.gensig)
	}

	const marker = "МаркерПослеПлоскогоКэша"
	mkBSLFile(t, dir, "CommonModules/ПослеПлоского/Ext/Module.bsl",
		"Процедура "+marker+"() Экспорт\nКонецПроцедуры\n")

	rep := mustReload(t, idx)
	if !rep.Changed || rep.SigBefore != "" || rep.SigAfter == "" {
		t.Fatalf("first reload of a flat-cache index: Changed=%v SigBefore=%q SigAfter=%q",
			rep.Changed, rep.SigBefore, rep.SigAfter)
	}
	if s, r, e := countAllModes(t, idx, marker); s == 0 || r == 0 || e == 0 {
		t.Fatalf("added module not found after reload: smart=%d regex=%d exact=%d", s, r, e)
	}
	// A second reload now has a signature to compare and must do nothing.
	if rep := mustReload(t, idx); rep.Changed {
		t.Fatal("the second reload of an unchanged dump still rebuilt")
	}
}
