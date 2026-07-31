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
// closed. Each of them carries a positive control that the reaper actually fired
// inside the window, so a reaper that never got there cannot pass them with
// nothing to suppress.
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

// reaperAtTheWindow runs reapers that reap everything not matching
// serveClaimForeignGensig, continuously, for as long as they are left running.
// stop() ends them and reports how many GC passes BEGAN while target was READY
// in the arena, which is the positive control: zero means no pass ever contested
// the published generation and the round proves nothing.
//
// TWO KINDS OF REAPER RUN, because one measurement cannot do both jobs.
//
// The UNGATED ones do the racing. Gating a pass on "target is READY" was measured
// to make a reaper lose every time on the narrow windows: the gate is itself a
// syscall, so the pass could only START after the generation was already
// published, and a claim taken one call after the publish always got there first.
// Ungated, a scan can already be in flight when the rename lands. Several run at
// once for the same reason — at any instant they sit at different points of the
// pass, so one is always close to the part that matters. They are not counted:
// what an ungated pass was contending for is not knowable from outside it.
//
// The GATED one does the counting, and only the counting. It looks for target in
// the arena and runs a GC pass against it when it finds it, so every pass it
// reports provably began while the generation was published and reapable. That is
// the positive control, and it stays reliable precisely because it is the poor
// racer: it reports opportunity, not victory.
func reaperAtTheWindow(dumpDir, cacheDir, target string) (stop func() int64) {
	const racers = 4
	var passes atomic.Int64
	stopCh := make(chan struct{})
	var wg sync.WaitGroup

	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
				}
				_, _ = GCGenerations(dumpDir, cacheDir, serveClaimForeignGensig)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			if GenerationReady(dumpDir, cacheDir, target) {
				passes.Add(1)
				_, _ = GCGenerations(dumpDir, cacheDir, serveClaimForeignGensig)
			}
		}
	}()

	return func() int64 {
		close(stopCh)
		wg.Wait()
		return passes.Load()
	}
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

	var totalPasses int64
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

		stop := reaperAtTheWindow(dumpDir, cacheDir, target)
		gen, prepErr := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
		totalPasses += stop()

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

	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw a generation READY in the arena, so "+
			"the %d successful prepares prove nothing about the race", prepared)
	}
	t.Logf("the reaper ran %d GC passes against newly published generations", totalPasses)
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
	var totalPasses int64
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

		stop := reaperAtTheWindow(dumpDir, cacheDir, target)
		gen, prepErr := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
		totalPasses += stop()

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

	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw a generation READY in the arena, so "+
			"the %d successful adopts prove nothing about the race", adopted)
	}
	t.Logf("the reaper ran %d GC passes against newly adopted generations", totalPasses)
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

	var totalPasses int64
	const rounds = 10
	rebuilt := 0
	for round := range rounds {
		stop := reaperAtTheWindow(dumpDir, cacheDir, target)
		gen, prepErr := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, true)
		totalPasses += stop()

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

	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw the generation READY in the arena, so "+
			"the %d successful reindexes prove nothing about the race", rebuilt)
	}
	t.Logf("the reaper ran %d GC passes against reindexed generations", totalPasses)
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
	var totalPasses int64
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

		stop := reaperAtTheWindow(dumpDir, cacheDir, target)
		idx, openErr := OpenForServe(dumpDir, cacheDir)
		totalPasses += stop()

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

	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw a generation READY in the arena, so "+
			"the %d successful opens prove nothing about the race", served)
	}
	t.Logf("the reaper ran %d GC passes against generations OpenForServe migrated", totalPasses)
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

	var totalPasses int64
	const rounds = 10
	opened := 0
	for round := range rounds {
		stop := reaperAtTheWindow(dumpDir, cacheDir, target)
		idx, openErr := NewIndex(dumpDir, cacheDir, true)
		if openErr == nil {
			select {
			case <-idx.Done():
			case <-time.After(60 * time.Second):
				totalPasses += stop()
				_ = idx.Close()
				t.Fatalf("round %d: the reindex open did not finish within 60s", round)
			}
		}
		totalPasses += stop()

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

	if totalPasses == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper never saw the generation READY in the arena, so "+
			"the %d successful reindexes prove nothing about the race", opened)
	}
	t.Logf("the reaper ran %d GC passes against legacy-reindexed generations", totalPasses)
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
