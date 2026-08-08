package dump

// Part-level tests for the serve open's reader claim.
//
// cmd/mcp-1c/serve_claim_test.go pins the DEFECT end to end, through the real
// serve entry point and against a reaper in a second OS process. These pin the
// individual mechanisms that path is built out of, because several of them
// mutually mask: attachReadOnlyShards still takes a post-adopt claim when the
// caller hands it none, so a producer that stops claiming early is caught by the
// racing end-to-end test but not by any assertion on the final state. Each test
// here is written so that reverting exactly one part turns exactly this test red.
//
// THE ORACLE FOR "THE WINDOW IS CLOSED" IS A RACING REAPER, and it has to be.
// The property is that at NO INSTANT is the generation READY in the arena and
// held by nobody, and no assertion on the state AFTER a producer returns can see
// that: a claim taken one call too late is present by then either way. So the
// window tests below run a reaper that watches for the generation to appear and
// reaps the moment it does, over many rounds, and require EVERY round to get
// through. Refusing sometimes is the signature of a window narrowed rather than
// closed. Each of them carries TWO positive controls, because a racing reaper can
// fail to prove anything in two different ways: it can never contend at all, and
// it can contend against an arena that never held the generation.
//
// A REAPER THAT WAS NEVER SCHEDULED IS NOT A REAPER, and starting one is not the
// same thing as running one. These tests used to `go` their reapers and call the
// producer immediately, and on a SHORT producer that meant the reapers never
// executed a single loop iteration, the four ungated racers included. Instrumented
// on the flat adopt: 0 iterations over 10 rounds at GOMAXPROCS=1, where the same
// instrumentation at GOMAXPROCS=10 counted thousands. The zero is the stable part
// and the only one quoted here, because the non-zero side varies run to run.
// OpenForServe's migration reaches the same adopt and failed the same way at the
// same rate, which is how the two of them differ from the rest.
// The mechanism is in the runtime's own constants.
// A flat-cache adopt returns about 2.6ms after it is called, while the scheduler
// preempts a running goroutine only after forcePreemptNS (10ms, runtime/proc.go)
// or when a syscall leaves a P in _Psyscall across a sysmon tick, which a run of
// short filesystem syscalls never does. The producer held the only P start to
// finish, the reapers were still sitting in the run queue when stop() closed their
// channel, and every one of them exited at the loop top having done nothing. The
// long producers here (cold build, reindex) outlast that 10ms slice and were never
// affected, which is why only two of these five tests went red.
//
// So the race these tests are named for did not happen at all, and the ONLY thing
// that noticed was the gated counter reading zero. That is the counter doing its
// job. The fix is therefore to make the reapers actually run, and to assert that
// they did — never to make the counter quieter. It has three parts, and the third
// is the one that makes the other two worth having:
//
//   - reaperAtTheWindow returns only once every reaper has completed a pass, so
//     they are established as running rather than merely created;
//   - it gives them Ps of their own (ensureReaperProcs), so that whether they run
//     during the window does not depend on the producer choosing to yield;
//   - every round reports how many ungated passes began WHILE the producer was in
//     flight, and a test whose rounds total zero FAILS. Zero used to be the silent
//     normal case at one P, asserted by nothing.
//
// NOT PINNED BY THE TEST YOU WOULD EXPECT, stated rather than left to be
// discovered:
//
//   - the claim's PLACEMENT inside adoptFlatShards is not pinned by
//     TestServeClaim_FlatAdoptPublishesAnAlreadyClaimedGeneration, the test named
//     for that branch. Moving the claim from before the adopt to after it left
//     that test green in three runs out of three: the path returns within a
//     couple of syscalls of the rename, so a reaper never gets in between. The
//     same mutation reddens
//     TestServeClaim_OpenForServeMigratePublishesAnAlreadyClaimedGeneration in
//     three runs out of three, because that path keeps the reaper contending
//     through the much longer shard open. The placement IS pinned, just not
//     where a reader would look first. Details in each test's own comment.
//
// NOT PINNED AT ALL. Two guards have no test that reverting them alone turns
// red, because they are unreachable from any producer as the code stands:
//
//   - heldGeneration's and openClaimedGeneration's refusal of a nil claim. Every
//     producer returns either a live claim or an error, so nothing today hands
//     either of them a nil. They are pinned only by the DIRECT part-level tests
//     below (TestServeClaim_HeldGenerationRefusesAnUnclaimedGeneration and
//     TestServeClaim_OpenClaimedGenerationRefusesAnUnclaimedGeneration), which
//     call them with nil themselves. That is a real test of the guard, but it is
//     not a test that any production path can reach it: if a future edit lets a
//     producer return (generation, nil, nil), those two are what stops it being
//     served, and no end-to-end test would have noticed the guard was gone first.
//   - attachReadOnlyShards' fallback to registerReader when held is nil cannot be
//     removed to prove it is load-bearing, because removing it turns the
//     already-published paths (OpenGenerationReadOnly and PrepareServeGeneration's
//     ready fast path) into unconditional refusals and reddens most of the suite
//     rather than one test. It is load-bearing for those paths by construction,
//     not by a pinning test.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// serveClaimForeignGensig is a keepGensig no generation can ever have, so a
// reaper using it treats every generation in the arena as a candidate and only a
// reader claim can protect one.
const serveClaimForeignGensig = "0000000000000000000000000000000000000000000000000000000000000000"

// serveClaimWriteModules writes n distinct BSL modules under dumpDir.
func serveClaimWriteModules(t *testing.T, dumpDir string, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		mkBSLFile(t, dumpDir,
			fmt.Sprintf("CommonModules/Модуль%04d/Ext/Module.bsl", i),
			fmt.Sprintf("Процедура %s%04d() Экспорт\n\tСообщить(\"привет %04d\");\nКонецПроцедуры\n",
				reapSearchTerm, i, i))
	}
}

// serveClaimGenDir resolves <cache>/g/<gensig>.
func serveClaimGenDir(t *testing.T, dumpDir, cacheDir, gensig string) string {
	t.Helper()
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	return generationDir(cpath, gensig)
}

// reaperRacers is how many ungated reapers race the producer.
const reaperRacers = 4

// reaperProcsNeeded is how many Ps the window reapers need in order not to depend
// on the producer yielding: the four ungated racers, the gated counter, and the
// producer itself.
//
// THE START BARRIER ALONE IS NOT ENOUGH, measured rather than reasoned. With the
// barrier and GOMAXPROCS=1 the reapers do get established and the five serve-claim
// tests pass on their own, but in a whole-package run
// TestServeClaim_OpenForServeMigrate... still reported zero ungated passes during
// the open. The reason is arithmetic: a flat-cache adopt is about 2.6ms of work
// and forcePreemptNS is 10ms (runtime/proc.go), so a producer that starts early
// enough in its slice finishes the entire window inside it and never hands the P
// back. Whether it does is a coin flip, which is the flake this is here to remove.
//
// Ps ARE NOT CPUs. This is runtime.GOMAXPROCS, the count of logical schedulers, so
// a single-CPU CI runner still runs every one of these goroutines — the OS
// time-slices them across threads instead of the Go scheduler refusing to preempt
// inside 10ms. It is deliberately not a t.Skip on low GOMAXPROCS: skipping would
// drop the race on exactly the configuration CI is most likely to hand us. Nothing
// about the product moves either, because this package sizes its shards and
// workers off runtime.NumCPU, which GOMAXPROCS does not change.
const reaperProcsNeeded = reaperRacers + 2

// ensureReaperProcs raises GOMAXPROCS for the rest of the test when it is below
// what a racing reaper needs, and restores it afterwards. It never lowers it.
//
// Side effect worth naming: runtime.GOMAXPROCS(n) with n > 0 sets
// sched.customGOMAXPROCS (runtime/debug.go), which switches off Go 1.25's
// automatic container-aware updating for the remainder of the process, and
// restoring the old value does not switch it back on. In a test binary that costs
// nothing; it would matter in the product, which is why the product does not do it.
func ensureReaperProcs(t *testing.T) {
	t.Helper()
	prev := runtime.GOMAXPROCS(0)
	if prev >= reaperProcsNeeded {
		return
	}
	runtime.GOMAXPROCS(reaperProcsNeeded)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
}

// reaperRound is what one round's reapers actually did. Both numbers are positive
// controls and they rule out different vacuities, which is why neither replaces
// the other.
type reaperRound struct {
	// gated is the number of GC passes that provably BEGAN while target was READY
	// in the arena. Zero means the arena never held the generation the round is
	// about — a broken fixture, a wrong gensig, a wrong cache path — so "the
	// producer survived" is a statement about nothing.
	gated int64
	// racedDuring is the number of ungated GC passes that began WHILE the producer
	// was running. Zero means no reaper contended at all, so the round did not run
	// the race it is named for. THE HARNESS USED TO HAVE NO ASSERTION FOR THIS AT
	// ALL: on the two short producers here — the flat adopt and OpenForServe's
	// migration — it was zero on all 10 rounds at GOMAXPROCS=1, and the only symptom
	// was the gated counter also reading zero. The long producers (cold build,
	// reindex) outlive a 10ms scheduling slice and did keep their reapers fed, so
	// this is the assertion that tells the two cases apart instead of leaving the
	// difference to be inferred from a count nobody checked.
	racedDuring int64
}

// windowReaper is a running set of reapers plus the record of what they did.
type windowReaper struct {
	dumpDir, cacheDir, target string

	racerPasses atomic.Int64
	gatedPasses atomic.Int64

	racersAtStart int64
	stopCh        chan struct{}
	wg            sync.WaitGroup
	once          sync.Once
	round         reaperRound
}

// reaperAtTheWindow starts reapers that reap everything not matching
// serveClaimForeignGensig, continuously, until stop() ends them. It returns only
// once every one of them has completed a full pass, so "the reaper is live" is
// established by construction rather than assumed: the goroutines used to be left
// merely CREATED, and a created goroutine that is never scheduled reaps nothing.
//
// TWO KINDS OF REAPER RUN, because one measurement cannot do both jobs.
//
// The UNGATED ones do the racing. Gating a pass on "target is READY" was measured
// to make a reaper lose every time on the narrow windows: the gate is itself a
// syscall, so the pass could only START after the generation was already
// published, and a claim taken one call after the publish always got there first.
// Ungated, a scan can already be in flight when the rename lands. Several run at
// once for the same reason — at any instant they sit at different points of the
// pass, so one is always close to the part that matters. What an ungated pass was
// contending for is not knowable from outside it, so they are counted only as
// evidence that a reaper was on the machine while the producer ran.
//
// The GATED one does the counting, and only the counting. It looks for target in
// the arena and runs a GC pass against it when it finds it, so every pass it
// reports provably began while the generation was published and reapable. It
// reports opportunity, not victory.
func reaperAtTheWindow(t *testing.T, dumpDir, cacheDir, target string) *windowReaper {
	t.Helper()
	ensureReaperProcs(t)

	r := &windowReaper{dumpDir: dumpDir, cacheDir: cacheDir, target: target, stopCh: make(chan struct{})}
	live := make(chan struct{}, reaperRacers+1)

	for range reaperRacers {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			for first := true; ; first = false {
				select {
				case <-r.stopCh:
					return
				default:
				}
				r.racerPasses.Add(1)
				_, _ = GCGenerations(dumpDir, cacheDir, serveClaimForeignGensig)
				if first {
					live <- struct{}{}
				}
			}
		}()
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for first := true; ; first = false {
			select {
			case <-r.stopCh:
				return
			default:
			}
			if GenerationReady(dumpDir, cacheDir, target) {
				r.gatedPasses.Add(1)
				_, _ = GCGenerations(dumpDir, cacheDir, serveClaimForeignGensig)
			}
			if first {
				live <- struct{}{}
			}
		}
	}()

	for range reaperRacers + 1 {
		<-live
	}
	r.racersAtStart = r.racerPasses.Load()
	return r
}

// stop ends the reapers and reports what they did. It is safe to call twice, so a
// round that bails out on a timeout can stop them on the way past.
func (r *windowReaper) stop() reaperRound {
	r.once.Do(func() {
		// Read BEFORE anything is torn down: the racers are still running, so this
		// counts only the ungated passes that began while the producer had the
		// machine.
		r.round.racedDuring = r.racerPasses.Load() - r.racersAtStart

		// THE GATED OBSERVATION IS TAKEN HERE, BY THE TEST GOROUTINE, and not left
		// to whether a poller happened to be on a P during the window. The generation
		// a producer published is still READY at this instant — measured 10 rounds out
		// of 10 at GOMAXPROCS 1, 2, 4 and 10 — so this is the same fact the gated
		// goroutine was trying to sample, established instead of guessed at.
		// It stays falsifiable on exactly what it is about: an arena that never
		// received the generation reports not-READY here too, and the count stays
		// zero. What it can no longer do is read zero because the scheduler was busy.
		if GenerationReady(r.dumpDir, r.cacheDir, r.target) {
			r.gatedPasses.Add(1)
			_, _ = GCGenerations(r.dumpDir, r.cacheDir, serveClaimForeignGensig)
		}

		close(r.stopCh)
		r.wg.Wait()
		r.round.gated = r.gatedPasses.Load()
	})
	return r.round
}

// TestServeClaim_ColdBuildPublishesAnAlreadyClaimedGeneration pins the build
// branch of PrepareServeGeneration. A cold build adopts its generation by
// renaming the finished temp directory into the shared arena; unless the claim
// went into that temp directory first, the generation is READY and held by nobody
// from that rename until the claim lands, and a reaper firing there takes it.
func TestServeClaim_ColdBuildPublishesAnAlreadyClaimedGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("hammers a reaper against a cold build for many rounds")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	serveClaimWriteModules(t, dumpDir, 0, 30)

	var totalPasses, totalRaced int64
	const rounds = 12
	prepared := 0
	for round := range rounds {
		serveClaimWriteModules(t, dumpDir, 1000+round, 1)
		target, err := GenSig(dumpDir)
		if err != nil {
			t.Fatalf("round %d: GenSig: %v", round, err)
		}
		if GenerationReady(dumpDir, cacheDir, target) {
			t.Fatalf("round %d: fixture: generation %s already exists, so this round builds nothing", round, target)
		}

		reaper := reaperAtTheWindow(t, dumpDir, cacheDir, target)
		gen, prepErr := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
		did := reaper.stop()
		totalPasses += did.gated
		totalRaced += did.racedDuring

		if prepErr != nil {
			t.Logf("round %d: prepare refused: %v", round, prepErr)
			continue
		}
		if gen.Gensig() != target {
			gen.Release()
			t.Fatalf("round %d: prepared generation %s, want %s", round, gen.Gensig(), target)
		}
		if !GenerationReady(dumpDir, cacheDir, target) {
			gen.Release()
			t.Fatalf("round %d: prepare reported success for %s but its READY marker is gone from disk",
				round, target)
		}
		prepared++
		gen.Release()
	}

	if totalRaced == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: no reaper ran a single GC pass while a build was in flight, "+
			"so nothing ever contended for the window and the %d successful prepares prove nothing", prepared)
	}
	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw a generation READY in the arena, so "+
			"the %d successful prepares prove nothing about the race", prepared)
	}
	t.Logf("the reaper ran %d GC passes against newly published generations, and %d ungated passes while a "+
		"build was in flight", totalPasses, totalRaced)
	if prepared != rounds {
		t.Fatalf("only %d of %d cold builds survived a reaper at the window; the build must write its claim "+
			"into the private temp dir and adopt the two together", prepared, rounds)
	}
}

// TestServeClaim_FlatAdoptPublishesAnAlreadyClaimedGeneration pins the
// flat-cache adopt branch, which has the same shape as the build (temp dir, READY
// written last, atomic rename) and therefore the same window. The fixture is
// rebuilt per round because adopting a flat cache CONSUMES it — the shards are
// moved, not copied — so one arena cannot be adopted twice.
//
// WHAT THIS TEST DOES AND DOES NOT PIN, measured rather than assumed. It pins
// that PrepareServeGeneration asks the migration for a claim: with the branch
// changed to noClaim the prepare refuses and this goes red every run. It does NOT
// pin the claim's PLACEMENT inside adoptFlatShards. Moving that claim from before
// the adopt to after it was run against this test three times and it stayed green
// all three: this path returns within a couple of syscalls of the rename, so a
// reaper never gets in between. The placement is pinned by
// TestServeClaim_OpenForServeMigratePublishesAnAlreadyClaimedGeneration, which
// reaches the same adopt and then keeps the reaper contending through the much
// longer shard open, where it does lose (3 of 3 runs).
func TestServeClaim_FlatAdoptPublishesAnAlreadyClaimedGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("hammers a reaper against a flat-cache adopt for many rounds")
	}
	var totalPasses, totalRaced int64
	const rounds = 10
	adopted := 0
	for round := range rounds {
		dumpDir := t.TempDir()
		cacheDir := t.TempDir()
		serveClaimWriteModules(t, dumpDir, 0, 12)

		// A LEGACY flat cache: shard_* directly under the per-dump cache dir, with
		// no generation for this signature yet. This is what the adopt branch takes.
		if err := BuildCache(dumpDir, cacheDir, false); err != nil {
			t.Fatalf("round %d: BuildCache (flat): %v", round, err)
		}
		cpath, err := cachePath(dumpDir, cacheDir)
		if err != nil {
			t.Fatalf("round %d: cachePath: %v", round, err)
		}
		if len(cacheShardDirs(cpath)) == 0 {
			t.Fatalf("round %d: fixture: no flat shards after BuildCache, so nothing would be adopted", round)
		}
		target, err := GenSig(dumpDir)
		if err != nil {
			t.Fatalf("round %d: GenSig: %v", round, err)
		}

		reaper := reaperAtTheWindow(t, dumpDir, cacheDir, target)
		gen, prepErr := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
		did := reaper.stop()
		totalPasses += did.gated
		totalRaced += did.racedDuring

		if prepErr != nil {
			t.Logf("round %d: prepare refused: %v", round, prepErr)
			continue
		}
		if !GenerationReady(dumpDir, cacheDir, gen.Gensig()) {
			gen.Release()
			t.Fatalf("round %d: prepare reported success for %s but its READY marker is gone from disk",
				round, gen.Gensig())
		}
		adopted++
		gen.Release()
	}

	if totalRaced == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: no reaper ran a single GC pass while an adopt was in flight, "+
			"so nothing ever contended for the window and the %d successful adopts prove nothing", adopted)
	}
	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw a generation READY in the arena, so "+
			"the %d successful adopts prove nothing about the race", adopted)
	}
	t.Logf("the reaper ran %d GC passes against newly adopted generations, and %d ungated passes while an "+
		"adopt was in flight", totalPasses, totalRaced)
	if adopted != rounds {
		t.Fatalf("only %d of %d flat-cache adopts survived a reaper at the window; the adopt must write its "+
			"claim into the private temp dir and adopt the two together", adopted, rounds)
	}
}

// TestServeClaim_ReindexPublishesAnAlreadyClaimedGeneration pins the reindex
// branch. A reindex drops the current generation and rebuilds it, so it publishes
// into the arena exactly as a cold build does and needs the same early claim.
func TestServeClaim_ReindexPublishesAnAlreadyClaimedGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("hammers a reaper against a reindex rebuild for many rounds")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	serveClaimWriteModules(t, dumpDir, 0, 20)

	target, err := GenSig(dumpDir)
	if err != nil {
		t.Fatalf("GenSig: %v", err)
	}

	var totalPasses, totalRaced int64
	const rounds = 10
	rebuilt := 0
	for round := range rounds {
		reaper := reaperAtTheWindow(t, dumpDir, cacheDir, target)
		gen, prepErr := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, true)
		did := reaper.stop()
		totalPasses += did.gated
		totalRaced += did.racedDuring

		if prepErr != nil {
			t.Logf("round %d: reindex prepare refused: %v", round, prepErr)
			continue
		}
		if gen.Gensig() != target {
			gen.Release()
			t.Fatalf("round %d: reindex prepared %s, want the unchanged signature %s", round, gen.Gensig(), target)
		}
		if !GenerationReady(dumpDir, cacheDir, target) {
			gen.Release()
			t.Fatalf("round %d: reindex reported success for %s but its READY marker is gone from disk",
				round, target)
		}
		rebuilt++
		// Released BEFORE the next round so the next reindex's drop is not skipped
		// by this round's own live claim.
		gen.Release()
	}

	if totalRaced == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: no reaper ran a single GC pass while a reindex was in "+
			"flight, so nothing ever contended for the window and the %d successful reindexes prove nothing",
			rebuilt)
	}
	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw the generation READY in the arena, so "+
			"the %d successful reindexes prove nothing about the race", rebuilt)
	}
	t.Logf("the reaper ran %d GC passes against reindexed generations, and %d ungated passes while a "+
		"reindex was in flight", totalPasses, totalRaced)
	if rebuilt != rounds {
		t.Fatalf("only %d of %d reindex rebuilds survived a reaper at the window; the reindex must build "+
			"WITH a claim", rebuilt, rounds)
	}
}

// TestServeClaim_OpenForServeMigratePublishesAnAlreadyClaimedGeneration pins the
// OpenForServe read path, which reaches the same adopt through
// migrateFlatToGeneration and used to claim only afterwards, in
// OpenGenerationReadOnly.
func TestServeClaim_OpenForServeMigratePublishesAnAlreadyClaimedGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("hammers a reaper against OpenForServe's migration for many rounds")
	}
	var totalPasses, totalRaced int64
	const rounds = 10
	served := 0
	for round := range rounds {
		dumpDir := t.TempDir()
		cacheDir := t.TempDir()
		serveClaimWriteModules(t, dumpDir, 0, 12)

		if err := BuildCache(dumpDir, cacheDir, false); err != nil {
			t.Fatalf("round %d: BuildCache (flat): %v", round, err)
		}
		target, err := GenSig(dumpDir)
		if err != nil {
			t.Fatalf("round %d: GenSig: %v", round, err)
		}
		if GenerationReady(dumpDir, cacheDir, target) {
			t.Fatalf("round %d: fixture: a generation already exists, so OpenForServe would not migrate", round)
		}

		reaper := reaperAtTheWindow(t, dumpDir, cacheDir, target)
		idx, openErr := OpenForServe(dumpDir, cacheDir)
		did := reaper.stop()
		totalPasses += did.gated
		totalRaced += did.racedDuring

		if openErr != nil {
			t.Logf("round %d: OpenForServe refused: %v", round, openErr)
			continue
		}
		select {
		case <-idx.Done():
		case <-time.After(60 * time.Second):
			_ = idx.Close()
			t.Fatalf("round %d: the open did not finish within 60s", round)
		}
		if !idx.Ready() {
			buildErr := idx.BuildError()
			_ = idx.Close()
			t.Logf("round %d: OpenForServe did not become ready: %v", round, buildErr)
			continue
		}
		if !GenerationReady(dumpDir, cacheDir, target) {
			_ = idx.Close()
			t.Fatalf("round %d: OpenForServe reported success for %s but its READY marker is gone from disk",
				round, target)
		}
		served++
		_ = idx.Close()
	}

	if totalRaced == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: no reaper ran a single GC pass while an open was in flight, "+
			"so nothing ever contended for the window and the %d successful opens prove nothing", served)
	}
	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw a generation READY in the arena, so "+
			"the %d successful opens prove nothing about the race", served)
	}
	t.Logf("the reaper ran %d GC passes against generations OpenForServe migrated, and %d ungated passes "+
		"while an open was in flight", totalPasses, totalRaced)
	if served != rounds {
		t.Fatalf("only %d of %d OpenForServe migrations survived a reaper at the window; the migration must "+
			"hand its claim straight to the open", served, rounds)
	}
}

// TestServeClaim_LegacyReindexPublishesAnAlreadyClaimedGeneration pins the THIRD
// place the same window existed: NewIndex(reindex=true) -> reindexGeneration,
// which built its generation and only then attached and claimed it. It is a
// different entry point from PrepareServeGeneration's reindex branch and reaches
// the arena by the same rename, so it needs the same early claim.
func TestServeClaim_LegacyReindexPublishesAnAlreadyClaimedGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("hammers a reaper against a legacy reindex for many rounds")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	serveClaimWriteModules(t, dumpDir, 0, 15)

	target, err := GenSig(dumpDir)
	if err != nil {
		t.Fatalf("GenSig: %v", err)
	}

	var totalPasses, totalRaced int64
	const rounds = 10
	opened := 0
	for round := range rounds {
		reaper := reaperAtTheWindow(t, dumpDir, cacheDir, target)
		idx, openErr := NewIndex(dumpDir, cacheDir, true)
		if openErr == nil {
			select {
			case <-idx.Done():
			case <-time.After(60 * time.Second):
				reaper.stop()
				_ = idx.Close()
				t.Fatalf("round %d: the reindex open did not finish within 60s", round)
			}
		}
		did := reaper.stop()
		totalPasses += did.gated
		totalRaced += did.racedDuring

		if openErr != nil {
			t.Logf("round %d: NewIndex(reindex) refused: %v", round, openErr)
			continue
		}
		if !idx.Ready() {
			buildErr := idx.BuildError()
			_ = idx.Close()
			t.Logf("round %d: the reindex open did not become ready: %v", round, buildErr)
			continue
		}
		if !GenerationReady(dumpDir, cacheDir, target) {
			_ = idx.Close()
			t.Fatalf("round %d: the reindex reported success for %s but its READY marker is gone from disk",
				round, target)
		}
		opened++
		// Released before the next round so the next reindex's drop is not skipped
		// by this round's own live claim.
		_ = idx.Close()
	}

	if totalRaced == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: no reaper ran a single GC pass while a legacy reindex was "+
			"in flight, so nothing ever contended for the window and the %d successful reindexes prove "+
			"nothing", opened)
	}
	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw the generation READY in the arena, so "+
			"the %d successful reindexes prove nothing about the race", opened)
	}
	t.Logf("the reaper ran %d GC passes against legacy-reindexed generations, and %d ungated passes while a "+
		"reindex was in flight", totalPasses, totalRaced)
	if opened != rounds {
		t.Fatalf("only %d of %d legacy reindexes survived a reaper at the window; reindexGeneration must "+
			"build WITH a claim and hand it to the attach", opened, rounds)
	}
}

// TestServeClaim_FinishServeOpenKeepsTheClaimItWasHanded pins the transfer of
// ownership into the Index on the SUCCESS path: FinishServeOpen must attach the
// claim it was handed rather than release it, and Close must then release it. A
// release on the success path would deregister the claim on the generation the
// index is at that moment serving, which is the original defect with an extra
// step.
func TestServeClaim_FinishServeOpenKeepsTheClaimItWasHanded(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	serveClaimWriteModules(t, dumpDir, 0, 6)

	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("PrepareServeGeneration: %v", err)
	}
	gensig := gen.Gensig()
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	ph := NewServePlaceholder(dumpDir)
	ph.FinishServeOpen(cacheDir, gen, nil)
	if !ph.Ready() {
		t.Fatalf("the open did not become ready: %v", ph.BuildError())
	}

	live, err := generationHasLiveReader(genDir)
	if err != nil {
		t.Fatalf("generationHasLiveReader: %v", err)
	}
	if !live {
		t.Fatal("after a successful FinishServeOpen the generation being served has NO live reader; a " +
			"reaper is free to delete it out from under this index")
	}

	// CONTROL — the same probe must report false once the index is closed, so the
	// assertion above cannot pass for a probe that answers true unconditionally.
	if err := ph.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	live, err = generationHasLiveReader(genDir)
	if err != nil {
		t.Fatalf("generationHasLiveReader after Close: %v", err)
	}
	if live {
		t.Fatal("CONTROL FAILED: the generation still reports a live reader after Close, so the assertion " +
			"above proves nothing")
	}
}

// TestServeClaim_FinishServeOpenReleasesTheClaimItCannotUse pins the other half
// of the ownership transfer: on a path that never reaches the attach,
// FinishServeOpen must RELEASE the claim it was handed. Leaking it pins the
// generation for the life of the process, so nothing ever reclaims it — the
// serve failed and the disk stays occupied by a generation nobody serves.
func TestServeClaim_FinishServeOpenReleasesTheClaimItCannotUse(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	serveClaimWriteModules(t, dumpDir, 0, 6)

	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("PrepareServeGeneration: %v", err)
	}
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gen.Gensig())

	// Make the finish fail AFTER the claim exists and BEFORE the attach, which is
	// the only place a handed-in claim can be stranded: take the READY marker away,
	// exactly as a reaper mid-removal would.
	if err := os.Remove(filepath.Join(genDir, readySentinelName)); err != nil {
		t.Fatalf("removing the READY sentinel: %v", err)
	}

	ph := NewServePlaceholder(dumpDir)
	ph.FinishServeOpen(cacheDir, gen, nil)
	if ph.Ready() {
		_ = ph.Close()
		t.Fatal("the open became ready although the generation has no READY marker")
	}
	if be := ph.BuildError(); be == nil {
		t.Fatal("a refused finish must record a BuildError")
	}
	t.Cleanup(func() { _ = ph.Close() })

	entries, err := os.ReadDir(filepath.Join(genDir, readersDirName))
	if err != nil {
		t.Fatalf("reading the reader registry: %v", err)
	}
	var left []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), readerProbePrefix) {
			left = append(left, e.Name())
		}
	}
	if len(left) != 0 {
		t.Fatalf("a finish that refused to serve left its reader claim behind (%v); the generation stays "+
			"pinned for the life of the process and is never reclaimed", left)
	}
}

// TestServeClaim_HeldGenerationRefusesAnUnclaimedGeneration pins the guard
// directly, because no producer can reach it (see the file header). A prepared
// generation with no claim is one that entered the arena unheld; serving it is
// the silent degradation the whole registry exists to prevent.
func TestServeClaim_HeldGenerationRefusesAnUnclaimedGeneration(t *testing.T) {
	gen, err := heldGeneration("deadbeefdeadbeef", nil)
	if err == nil {
		t.Fatalf("heldGeneration accepted a generation with no reader claim and returned %v", gen)
	}
	if gen != nil {
		t.Fatalf("heldGeneration returned both an error and a usable handle: %v", gen)
	}
	if !strings.Contains(err.Error(), "deadbeefdeadbeef") {
		t.Fatalf("the refusal must name the generation it refused; got %v", err)
	}

	// CONTROL — the same call with a real claim must SUCCEED, so the assertion
	// above is caused by the missing claim and not by the function refusing
	// everything.
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	serveClaimWriteModules(t, dumpDir, 0, 3)
	gensig, err := GenSig(dumpDir)
	if err != nil {
		t.Fatalf("GenSig: %v", err)
	}
	if err := BuildGeneration(dumpDir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	reg, err := registerReader(serveClaimGenDir(t, dumpDir, cacheDir, gensig))
	if err != nil {
		t.Fatalf("registerReader: %v", err)
	}
	t.Cleanup(reg.Close)
	if _, err := heldGeneration(gensig, reg); err != nil {
		t.Fatalf("CONTROL FAILED: heldGeneration refused a generation WITH a live claim: %v", err)
	}
}

// TestServeClaim_OpenClaimedGenerationRefusesAnUnclaimedGeneration is the same
// guard on the OpenForServe side, and pinned the same way and for the same
// reason.
func TestServeClaim_OpenClaimedGenerationRefusesAnUnclaimedGeneration(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	serveClaimWriteModules(t, dumpDir, 0, 3)
	gensig, err := GenSig(dumpDir)
	if err != nil {
		t.Fatalf("GenSig: %v", err)
	}
	if err := BuildGeneration(dumpDir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}

	idx, err := openClaimedGeneration(dumpDir, cacheDir, gensig, nil)
	if err == nil {
		_ = idx.Close()
		t.Fatal("openClaimedGeneration served a generation with no reader claim")
	}
	if !strings.Contains(err.Error(), gensig) {
		t.Fatalf("the refusal must name the generation it refused; got %v", err)
	}

	// CONTROL — with a real claim the same call must open.
	reg, err := registerReader(serveClaimGenDir(t, dumpDir, cacheDir, gensig))
	if err != nil {
		t.Fatalf("registerReader: %v", err)
	}
	idx, err = openClaimedGeneration(dumpDir, cacheDir, gensig, reg)
	if err != nil {
		t.Fatalf("CONTROL FAILED: openClaimedGeneration refused a generation WITH a live claim: %v", err)
	}
	_ = idx.Close()
}
