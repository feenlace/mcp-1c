package dump

// A claim LOST after it was taken, and why it is the same defect as one that could
// never be taken.
//
// THE PROPERTY: the server never SILENTLY serves an index generation it could not
// protect. readonly_cache_test.go pins the half where the claim was refused at the
// door. This file pins the other door into the same room: the claim was written,
// accepted, and read back, and then the entry stopped being refreshable — a
// co-located reaper removed it, or the registry stopped taking the touch. Measured
// on the real binary at b74d027: the heartbeat logged the loss at ERROR and NOT ONE
// tool response carried a notice, so the server was serving a generation nothing
// protects and saying nothing about it. The log is not a delivery.
//
// AND THE OTHER HALF, which is harder and is most of this file: the notice must not
// fire when nothing is wrong. A claim is released ON PURPOSE at every shutdown and
// at every generation retirement, and both of those remove the same entry the
// heartbeat is watching. A notice on those is the same defect as a missing one, only
// quieter, and it is the one that teaches a reader to ignore the line that matters.
//
// WHAT IS NOT PINNED HERE, stated rather than left to be discovered:
//
//   - The PRODUCTION heartbeat interval. Every test here sets readerRegistration.beat
//     so a lose-and-report cycle costs milliseconds instead of ten seconds. The
//     unshortened path is measured on the real binary instead, and the value the
//     production path uses is pinned by TestLostClaim_TheProductionBeatIsTheDefault.
//   - The exact reason a touch fails. These tests produce the loss by removing the
//     entry, which is what a reaper does; an entry present but untouchable (a
//     registry that went read-only under a live server) reaches the same branch by
//     construction, and is not reproduced separately because no portable test can
//     make os.Chtimes fail on a file it owns without removing it.
//   - Which RU sentence a lost claim produces. That is the tool layer's decision and
//     is pinned where it is made, in tools/index_notice_test.go; the value crossing
//     the package boundary is UnprotectedState, and it is asserted on both sides.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// lostBeat is the shortened heartbeat these tests run at. It is long enough that a
// loaded machine still schedules the goroutine between ticks and short enough that a
// full detect-and-report cycle fits inside a poll loop of a few seconds.
const lostBeat = 100 * time.Millisecond

// lostClaimServe opens dumpDir's generation read-only through the SAME attach the
// serve path uses, holding a real claim whose heartbeat runs at lostBeat.
//
// The claim is built with claimReader followed by start(), which is exactly what
// registerReader does back to back; the only thing inserted between them is the beat.
// Passing it to openReadOnlyFrom as a held claim is what FinishServeOpen does with
// the claim a build came away with, so nothing about the attach is a test fiction.
//
// The returned closer is the ONLY way these tests close the index. (*Index).Close is
// not idempotent — the second call closes bleve shards that are already closed and
// panics on a closed channel — so a test that closes explicitly and a cleanup that
// closes again would take the process down. The once guard lives here rather than in
// each test so no test can forget it.
func lostClaimServe(t *testing.T, dumpDir, cacheDir, genDir string) (*Index, *readerRegistration, func() error) {
	t.Helper()
	reg, err := claimReader(genDir, genDir)
	if err != nil {
		t.Fatalf("taking the claim this test serves behind: %v", err)
	}
	reg.beat = lostBeat
	reg.start()
	idx, err := openReadOnlyFrom(dumpDir, cacheDir, genDir, reg)
	if err != nil {
		t.Fatalf("opening the generation for serving: %v", err)
	}
	var once sync.Once
	var closeErr error
	closeIdx := func() error {
		once.Do(func() { closeErr = idx.Close() })
		return closeErr
	}
	t.Cleanup(func() { _ = closeIdx() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("the open recorded a build error: %v", err)
	}
	// The precondition, asserted rather than assumed: a test of "the notice appears"
	// that started from an already-unprotected index would pass by never firing.
	if st := idx.Unprotected(); st.Reason != "" || st.ClaimLost {
		t.Fatalf("the open is already unprotected, so nothing below tests the transition: %+v", st)
	}
	return idx, reg, closeIdx
}

// lostClaimEntry returns the single reader-registry entry under genDir, failing
// unless there is exactly one. Finding it by listing rather than by remembering what
// was written keeps the test honest about what a peer's reaper would see.
func lostClaimEntry(t *testing.T, genDir string) string {
	t.Helper()
	readersDir := filepath.Join(genDir, readersDirName)
	entries, err := os.ReadDir(readersDir)
	if err != nil {
		t.Fatalf("reading the reader registry %s: %v", readersDir, err)
	}
	var names []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), readerProbePrefix) {
			names = append(names, e.Name())
		}
	}
	if len(names) != 1 {
		t.Fatalf("expected exactly one reader entry under %s, found %d: %v", readersDir, len(names), names)
	}
	return filepath.Join(readersDir, names[0])
}

// lostClaimAwait polls until the index's protection state satisfies want, or fails
// after a deadline generous enough for a loaded machine.
func lostClaimAwait(t *testing.T, idx *Index, what string, want func(UnprotectedState) bool) UnprotectedState {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var st UnprotectedState
	for time.Now().Before(deadline) {
		st = idx.Unprotected()
		if want(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the index never reached the state %q; it is still %+v", what, st)
	return st
}

// TestLostClaim_ALostClaimReachesTheToolResponse is the regression. The claim is
// taken correctly, the index serves protected and silently, and then the registry
// entry goes away underneath it.
func TestLostClaim_ALostClaimReachesTheToolResponse(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	idx, _, _ := lostClaimServe(t, dumpDir, cacheDir, genDir)
	entry := lostClaimEntry(t, genDir)

	// CONTROL: the index answers, and answers silently, BEFORE the entry is removed.
	// Without it, an index that warned from the start would pass everything below.
	hits, _, err := idx.Search(SearchParams{Query: serveFrozenTerm, Limit: 20})
	if err != nil {
		t.Fatalf("searching the healthy serve: %v", err)
	}
	if len(hits) != serveFrozenModules {
		t.Fatalf("the control failed: a healthy serve returned %d hits, want %d", len(hits), serveFrozenModules)
	}

	if err := os.Remove(entry); err != nil {
		t.Fatalf("removing the reader entry, which is what a peer's reaper does: %v", err)
	}

	st := lostClaimAwait(t, idx, "unprotected after the claim was lost",
		func(s UnprotectedState) bool { return s.Reason != "" })

	// IT IS THE LOST-CLAIM STATE AND NOT THE UNWRITABLE-CACHE ONE. The two produce
	// different sentences, and a state that arrived under the wrong flag would put
	// «Серверу не удалось записать заявку читателя» in front of a user whose cache
	// took the write.
	if !st.ClaimLost {
		t.Errorf("a claim lost after it was taken is reported as one that could never be written: %+v", st)
	}
	// THE REASON NAMES THE ENTRY, so an operator reading the log can find it.
	if !strings.Contains(st.Reason, entry) {
		t.Errorf("the reason does not name the entry it is about: %q", st.Reason)
	}
	// AND IT DOES NOT NAME A CAUSE IT NEVER MEASURED. The touch failed; who removed
	// the entry, or whether it was removed at all, was not observed.
	for _, forbidden := range []string{"deleted by", "removed by", "a reaper removed"} {
		if strings.Contains(st.Reason, forbidden) {
			t.Errorf("the reason asserts a cause the code did not measure (%q): %q", forbidden, st.Reason)
		}
	}
	// AND IT IS STILL AN ANSWER, not a refusal wearing a warning's clothes.
	if hits, _, err := idx.Search(SearchParams{Query: serveFrozenTerm, Limit: 20}); err != nil {
		t.Errorf("the index stopped answering once its claim was lost: %v", err)
	} else if len(hits) != serveFrozenModules {
		t.Errorf("the unprotected index returned %d hits, want %d", len(hits), serveFrozenModules)
	}
}

// TestLostClaim_AClaimThatComesBackClearsTheNotice is the "stop when it stops" half.
// The state this reports is not an event: a reaper takes a generation by renaming it
// out of the arena and rolls that rename back when it finds a holder, and the entry
// is back at its path when it does. A notice that outlived the state would tell a
// user with a healthy cache that their index is unprotected, for ever, with nothing
// they can do to make it stop.
func TestLostClaim_AClaimThatComesBackClearsTheNotice(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	idx, _, _ := lostClaimServe(t, dumpDir, cacheDir, genDir)
	entry := lostClaimEntry(t, genDir)

	body, err := os.ReadFile(entry)
	if err != nil {
		t.Fatalf("reading the entry before removing it: %v", err)
	}
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	lostClaimAwait(t, idx, "unprotected", func(s UnprotectedState) bool { return s.ClaimLost })

	// The rename rolled back: the entry is at its path again, so the touch works and
	// the process is a recorded holder once more.
	if err := os.WriteFile(entry, body, 0o644); err != nil {
		t.Fatalf("putting the entry back: %v", err)
	}
	st := lostClaimAwait(t, idx, "protected again",
		func(s UnprotectedState) bool { return s.Reason == "" })
	if st.ClaimLost {
		t.Errorf("the state cleared its reason but kept the flag: %+v", st)
	}
}

// TestLostClaim_AnOrderlyCloseIsNotALoss is the first of the two false-fire guards.
// Close removes exactly the entry the heartbeat is watching, so an implementation
// that removed it before stopping the beat would raise the alarm on every clean
// shutdown — and a warning printed at every shutdown is a warning nobody reads.
func TestLostClaim_AnOrderlyCloseIsNotALoss(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	rec := captureLogs(t)
	idx, reg, closeIdx := lostClaimServe(t, dumpDir, cacheDir, genDir)

	// Let the heartbeat actually run, so a Close that raced it has something to race.
	time.Sleep(6 * lostBeat)
	if reg.lost.Load() {
		t.Fatal("the claim was reported lost while nothing had touched it")
	}
	// CONTROL: after several beats the serve is still protected and still answering,
	// so what Close is about to race is a live heartbeat and not a dead one.
	if st := idx.Unprotected(); st.Reason != "" {
		t.Fatalf("the serve went unprotected before Close was called: %+v", st)
	}
	if hits, _, sErr := idx.Search(SearchParams{Query: serveFrozenTerm, Limit: 20}); sErr != nil {
		t.Fatalf("searching before the close: %v", sErr)
	} else if len(hits) != serveFrozenModules {
		t.Fatalf("the control failed: %d hits before the close, want %d", len(hits), serveFrozenModules)
	}
	if err := closeIdx(); err != nil {
		t.Fatalf("closing the index: %v", err)
	}
	// Give a heartbeat that outlived Close every chance to fire.
	time.Sleep(6 * lostBeat)

	if reg.lost.Load() {
		t.Error("an orderly Close was seen as a lost claim; every clean shutdown would now warn")
	}
	if got := strings.Join(rec.atLevel(slog.LevelError), "\n"); strings.Contains(got, "no longer holds a reader claim") {
		t.Errorf("an orderly Close logged the lost-claim alarm:\n%s", got)
	}

	// POSITIVE CONTROL, in the same run and on the same recorder: the alarm CAN fire.
	// Without it, an implementation that never reported anything at all would pass
	// every assertion above.
	cache2 := t.TempDir()
	gensig2 := serveFrozenPrepared(t, dumpDir, cache2)
	gen2 := serveClaimGenDir(t, dumpDir, cache2, gensig2)
	idx2, _, _ := lostClaimServe(t, dumpDir, cache2, gen2)
	if err := os.Remove(lostClaimEntry(t, gen2)); err != nil {
		t.Fatal(err)
	}
	lostClaimAwait(t, idx2, "unprotected", func(s UnprotectedState) bool { return s.ClaimLost })
	if got := strings.Join(rec.atLevel(slog.LevelError), "\n"); !strings.Contains(got, "no longer holds a reader claim") {
		t.Fatalf("the control failed: the lost-claim alarm never fires at ERROR, so its absence above "+
			"proves nothing. ERROR records were:\n%s", got)
	}
}

// TestLostClaim_AReleaseInProgressIsNeverALoss is the deterministic half of the
// orderly-close guarantee.
//
// The other half is an ordering — Close stops the beat, waits for the goroutine, and
// only then removes the entry — and it is real but untestable: no portable test can
// force a tick into a window a few microseconds wide, which was measured, by
// reversing those three lines and watching every timing test still pass. So the
// property is also carried by state, and the state is what is pinned here.
func TestLostClaim_AReleaseInProgressIsNeverALoss(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	// CONTROL: with no release in progress, a removed entry IS a loss. Without it,
	// a touch that reported nothing ever would pass every assertion below.
	control, err := claimReader(genDir, genDir)
	if err != nil {
		t.Fatalf("taking the control claim: %v", err)
	}
	if err := os.Remove(control.path); err != nil {
		t.Fatal(err)
	}
	control.touch(time.Now())
	if !control.lost.Load() {
		t.Fatal("the control failed: a removed entry outside a release was not reported lost, so the " +
			"silence below proves nothing")
	}

	// THE PROPERTY: the same removal during a release says nothing.
	quiet, err := claimReader(genDir, genDir)
	if err != nil {
		t.Fatalf("taking the second claim: %v", err)
	}
	quiet.releasing.Store(true)
	if err := os.Remove(quiet.path); err != nil {
		t.Fatal(err)
	}
	quiet.touch(time.Now())
	if quiet.lost.Load() {
		t.Error("a claim released on purpose was reported lost; every clean shutdown and every " +
			"generation retirement would now put a notice on the next answer")
	}

	// AND THROUGH THE REAL Close, which is what has to announce the release: a touch
	// after a completed Close still says nothing. This fails if Close stopped
	// announcing OR if touch stopped listening, so the two halves are pinned together.
	full, err := claimReader(genDir, genDir)
	if err != nil {
		t.Fatalf("taking the third claim: %v", err)
	}
	full.beat = lostBeat
	full.start()
	full.Close()
	if !full.releasing.Load() {
		t.Error("Close did not announce the release, so a beat that outlived it would read our own " +
			"removal as a reaper's")
	}
	full.touch(time.Now())
	if full.lost.Load() {
		t.Error("a touch after a completed Close reported the claim lost")
	}
}

// TestLostClaim_ARetiredRegistrationCannotSpeakForTheIndex is the second false-fire
// guard, and the one a reload depends on. swapGeneration publishes the new claim
// under mu and the caller closes the old one afterwards, so a beat of the retired
// heartbeat can still land in between. It must describe nothing: the generation it
// is about is no longer being served.
func TestLostClaim_ARetiredRegistrationCannotSpeakForTheIndex(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	idx, retired, _ := lostClaimServe(t, dumpDir, cacheDir, genDir)

	// A second claim on the same generation replaces the first, the way a reload's
	// swap replaces one generation's claim with the next one's.
	current, err := registerReader(genDir)
	if err != nil {
		t.Fatalf("taking the replacement claim: %v", err)
	}
	t.Cleanup(current.Close)
	idx.adoptClaim(current)

	retired.lost.Store(true)
	retired.reportProtection()
	if st := idx.Unprotected(); st.Reason != "" {
		t.Errorf("a retired registration wrote a notice about a generation nobody is serving: %+v", st)
	}

	// POSITIVE CONTROL: the CURRENT registration reporting the same thing does land.
	// Without it an implementation that dropped every report would pass the assertion
	// above while delivering nothing at all.
	current.lost.Store(true)
	current.reportProtection()
	if st := idx.Unprotected(); st.Reason == "" || !st.ClaimLost {
		t.Fatalf("the control failed: the registration the index is serving behind cannot write the "+
			"notice either, so the assertion above proves nothing: %+v", st)
	}
}

// TestLostClaim_AGenerationRetirementIsSilent is the same guard end to end, through
// a real Reload rather than through adoptClaim. Retiring a generation removes the
// old claim's entry on purpose, and the heartbeat watching it is running at lostBeat
// while that happens.
func TestLostClaim_AGenerationRetirementIsSilent(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	rec := captureLogs(t)
	idx, old, _ := lostClaimServe(t, dumpDir, cacheDir, genDir)

	// The dump moves, so Reload reaches the swap instead of returning "unchanged".
	mkBSLFile(t, dumpDir, "CommonModules/МодульПослеСмены/Ext/Module.bsl",
		fmt.Sprintf("Процедура Новая() Экспорт\n\tСообщить(\"%s 9999\");\nКонецПроцедуры\n", serveFrozenTerm))

	rep, err := idx.Reload()
	if err != nil {
		t.Fatalf("the reload failed: %v", err)
	}
	if !rep.Changed {
		t.Fatal("the reload reported no change, so it never retired a generation and this test proves nothing")
	}
	// Long enough for a retired heartbeat that outlived the swap to have fired.
	time.Sleep(6 * lostBeat)

	if st := idx.Unprotected(); st.Reason != "" {
		t.Errorf("retiring a generation on purpose produced a notice on the one that replaced it: %+v", st)
	}
	if old.lost.Load() {
		t.Error("the retired claim was reported lost although it was released deliberately")
	}
	if got := strings.Join(rec.atLevel(slog.LevelError), "\n"); strings.Contains(got, "no longer holds a reader claim") {
		t.Errorf("an ordinary generation retirement logged the lost-claim alarm:\n%s", got)
	}
	if hits, _, sErr := idx.Search(SearchParams{Query: serveFrozenTerm, Limit: 20}); sErr != nil {
		t.Fatalf("searching after the reload: %v", sErr)
	} else if len(hits) != serveFrozenModules+1 {
		t.Errorf("the reloaded index served %d modules, want %d", len(hits), serveFrozenModules+1)
	}
}

// lostClaimCurrent returns the registration the index is serving behind, read under
// the mutex that publishes it, so a test never races a swap for it.
func lostClaimCurrent(idx *Index) *readerRegistration {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.readerReg
}

// TestLostClaim_AClaimTakenByAReloadIsWatchedToo is the other half of the
// retirement: the generation a reload installs must be watched exactly as the one
// the open installed was. swapGeneration is a SECOND place a registration is put on
// an Index, and a fix applied only to the first one leaves every server that has
// ever reloaded back in the silent state, permanently and invisibly.
//
// It drives touch directly rather than waiting for a tick, because the registration a
// reload builds beats at the production interval; that the ticker calls touch at all
// is pinned by TestLostClaim_ALostClaimReachesTheToolResponse, which uses a real one.
func TestLostClaim_AClaimTakenByAReloadIsWatchedToo(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	idx, old, _ := lostClaimServe(t, dumpDir, cacheDir, genDir)

	mkBSLFile(t, dumpDir, "CommonModules/МодульДляПерезагрузки/Ext/Module.bsl",
		fmt.Sprintf("Процедура Новая() Экспорт\n\tСообщить(\"%s 8888\");\nКонецПроцедуры\n", serveFrozenTerm))
	rep, err := idx.Reload()
	if err != nil {
		t.Fatalf("the reload failed: %v", err)
	}
	if !rep.Changed {
		t.Fatal("the reload reported no change, so no new claim was installed and this test proves nothing")
	}

	current := lostClaimCurrent(idx)
	if current == nil || current == old {
		t.Fatalf("the reload did not install a new registration (current==old: %v)", current == old)
	}
	// CONTROL: the reloaded index is protected and silent before anything is removed.
	if st := idx.Unprotected(); st.Reason != "" {
		t.Fatalf("the reloaded index is already unprotected: %+v", st)
	}

	if err := os.Remove(current.path); err != nil {
		t.Fatalf("removing the new generation's reader entry: %v", err)
	}
	current.touch(time.Now())

	st := idx.Unprotected()
	if st.Reason == "" || !st.ClaimLost {
		t.Errorf("a claim lost on the generation a RELOAD installed says nothing; the notice mechanism "+
			"is wired to the open only, so every server that has reloaded is silent again: %+v", st)
	}
}

// TestLostClaim_AClaimlessRegistrationKeepsItsOwnReason pins the precedence between
// the two unprotected states and the fact that they cannot be confused. A claimless
// registration never heartbeats, so it can never acquire the lost flag; if a future
// edit made one, the permanent condition is what the user is told about, because it
// is the one with a remedy.
func TestLostClaim_AClaimlessRegistrationKeepsItsOwnReason(t *testing.T) {
	reg := claimlessRegistration("the cache refused the write")
	st := reg.protectionState()
	if st.Reason != "the cache refused the write" || st.ClaimLost {
		t.Errorf("a claim that could never be written is reported as one that was lost: %+v", st)
	}
	reg.lost.Store(true)
	if st := reg.protectionState(); st.ClaimLost {
		t.Errorf("a claimless registration reported the lost-claim state: %+v", st)
	}

	// The read-only-filesystem shape stays silent, lost flag or not: there is no
	// reaper on a filesystem nothing can write, so there is nothing to report.
	proven := claimlessRegistration("")
	if st := proven.protectionState(); st.Reason != "" || st.ClaimLost {
		t.Errorf("the provably-safe claimless serve produced a notice: %+v", st)
	}

	// And a registration that holds a real claim says nothing until it loses it.
	held := &readerRegistration{path: "/nonexistent/entry"}
	if st := held.protectionState(); st.Reason != "" || st.ClaimLost {
		t.Errorf("a held claim reported itself unprotected: %+v", st)
	}
	held.lost.Store(true)
	if st := held.protectionState(); st.Reason == "" || !st.ClaimLost {
		t.Errorf("a lost claim reported itself protected: %+v", st)
	}
	// A nil registration is the shape every accessor in this file has to survive.
	var none *readerRegistration
	if st := none.protectionState(); st.Reason != "" || st.ClaimLost {
		t.Errorf("a nil registration produced a state: %+v", st)
	}
	none.reportProtection()
	none.adoptedBy(nil)
}

// TestLostClaim_AnOpenThatFailedDescribesNothing is the third false-fire guard, and
// the one that only exists on the ASYNC serve path. FinishServeOpen populates a
// placeholder the caller is already holding, so an attach that fails leaves that
// pointer alive and reachable; if the claim state stayed on it, every "index build
// failed" answer would come with a notice about an index nobody is serving.
//
// Reaching the attach's own failure branch needs both halves: a claim that cannot be
// written (so there is a reason to leave behind) and shards that cannot be opened
// (so the attach fails after adopting it). The generation is corrupted first and
// frozen second, because a frozen one cannot be corrupted.
func TestLostClaim_AnOpenThatFailedDescribesNothing(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	shardDirs := cacheShardDirs(genDir)
	if len(shardDirs) == 0 {
		t.Fatal("the generation has no shards to corrupt, so this test cannot reach the branch it is about")
	}
	// index_meta.json is what bleve.Open reads first to learn the index type, so a
	// shard whose meta is unparseable fails the open while the directory is still
	// listed by cacheShardDirs — which is the state this branch is about.
	if err := os.WriteFile(filepath.Join(shardDirs[0], "index_meta.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupting a shard: %v", err)
	}
	freezeTree(t, cacheDir)
	serveFrozenClaimRefused(t, genDir)

	idx := NewServePlaceholder(dumpDir)
	t.Cleanup(func() { _ = idx.Close() })
	idx.FinishServeOpen(cacheDir, &ServeGeneration{gensig: gensig}, nil)
	<-idx.Done()

	if idx.Ready() {
		t.Fatal("the corrupt generation opened, so nothing below tests the failure branch")
	}
	if idx.BuildError() == nil {
		t.Fatal("the failed open recorded no build error, so the failure branch was not reached")
	}
	if st := idx.Unprotected(); st.Reason != "" || st.ClaimLost {
		t.Errorf("an open that failed left its protection state behind, so every answer of an index "+
			"that never opened carries a notice about one in use: %+v", st)
	}
}

// TestLostClaim_TheProductionBeatIsTheDefault is the guard on the test seam every
// other test in this file uses. A registration nobody shortened must beat at
// readerHeartbeatInterval, or the seam would have quietly become the production
// timing and readerStaleAfter would no longer be three beats of it.
func TestLostClaim_TheProductionBeatIsTheDefault(t *testing.T) {
	var reg readerRegistration
	if got := reg.beatInterval(); got != readerHeartbeatInterval {
		t.Errorf("an unshortened registration beats every %v, want %v", got, readerHeartbeatInterval)
	}
	// A negative value is a zero value with a typo, not an instruction to beat
	// backwards; a ticker built from one panics.
	reg.beat = -time.Second
	if got := reg.beatInterval(); got != readerHeartbeatInterval {
		t.Errorf("a non-positive beat selected %v instead of the production interval", got)
	}
	reg.beat = lostBeat
	if got := reg.beatInterval(); got != lostBeat {
		t.Errorf("the seam does not take: beat=%v produced %v", lostBeat, got)
	}
	// And the staleness window is still three beats of the production interval, which
	// is what stops a live-but-slow reader being false-reaped.
	if readerStaleAfter != 3*readerHeartbeatInterval {
		t.Errorf("readerStaleAfter is %v, no longer three heartbeat intervals of %v",
			readerStaleAfter, readerHeartbeatInterval)
	}
}

// TestLostClaim_ATouchThatWorksSaysNothing pins the healthy branch of touch itself:
// the entry is there, the touch lands, and nothing is reported at all. Without it a
// touch that reported on every beat would pass every test above, because they all
// assert on a state that a permanently-warning build also reaches.
func TestLostClaim_ATouchThatWorksSaysNothing(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	idx, reg, _ := lostClaimServe(t, dumpDir, cacheDir, genDir)
	entry := lostClaimEntry(t, genDir)

	before, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}
	// Drive the beat directly, so this is a test of touch and not of a ticker.
	for range 5 {
		reg.touch(time.Now().Add(time.Second))
		if reg.lost.Load() {
			t.Fatal("a touch that succeeded set the lost flag")
		}
		if st := idx.Unprotected(); st.Reason != "" {
			t.Fatalf("a touch that succeeded produced a notice: %+v", st)
		}
	}
	after, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}
	// THE CONTROL FOR THE CONTROL: the touch really did move the mtime, so "nothing
	// was reported" is a statement about a working heartbeat and not about a no-op.
	if !after.ModTime().After(before.ModTime()) {
		t.Errorf("the touch did not refresh the entry (%v -> %v), so the silence above is the silence "+
			"of a heartbeat that does nothing", before.ModTime(), after.ModTime())
	}
}
