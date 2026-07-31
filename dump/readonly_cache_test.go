package dump

// A cache this process may READ but not WRITE.
//
// THE DEFECT. Making the reader claim mandatory turned every generation in an
// unwritable cache into a permanent refusal. Measured on the real binary against a
// warm cache that had just answered "220 совпадений": chmod -R a-w, and the next
// start returns "search: index build failed: claiming the existing generation ...:
// permission denied", and so does the one after it, and every one after that.
// v1.12.0 on the same cache answers the query. The refusal is right whenever
// something could delete the generation; it is wrong when nothing can, and the
// process was not asking which.
//
// THE PROPERTY these tests pin: a claim that cannot be WRITTEN is not by itself a
// reason to refuse, and it is not by itself a reason to serve either. Serving is
// permitted only against a PROOF that no process can remove the generation
// (arenaUnreapable), and an arena someone else could still write is refused exactly
// as before. The pair matters more than either half: a fix that only made the
// refusal go away would have restored the original defect — serving a generation
// nothing protects — under a new name.
//
// EVERY FROZEN-ARENA TEST CARRIES A POSITIVE CONTROL that the claim really could
// not be written (serveFrozenClaimRefused). Without it a chmod that silently failed,
// or a test process that happened to be able to write anyway, would let the whole
// file pass with nothing to suppress.
//
// WHAT IS NOT PINNED HERE, stated rather than left to be discovered:
//
//   - The EROFS branch is pinned only through classifyArenaProbe, not through a
//     real read-only mount, because no unit test can portably create one. The
//     inputs the table feeds it are MEASURED, not invented: on a macOS UDRO image
//     the probe fails with EROFS while os.Stat reports mode 0755. The end-to-end
//     half was run by hand against a mounted UDRO image of a warm cache — v1.12.0
//     served, a74dc1a refused, this code serves and logs the read-only-mount proof
//     — and that run is not reproduced by any test in this repo.
//   - The branch that refuses an arena writable BY ITS OWNER but not by us is
//     pinned only through classifyArenaProbe for the same kind of reason: creating
//     a directory owned by another uid needs privileges a test does not have. The
//     end-to-end refusal IS pinned, but via the arena-writable-by-us case
//     (TestReadOnlyCache_AnArenaAPeerCouldReapIsStillRefused), which reaches the
//     same refusal through a different branch of the same function.
//   - Nothing here pins the Windows behaviour. There directory rights are ACLs and
//     Mode().Perm() does not describe them, so the mode branch never fires and an
//     unclaimable generation keeps being refused. That is fail-closed and no worse
//     than a74dc1a, but it is untested on this machine.
//   - Close's unreapable guard IS pinned, but only against a state no production
//     path can reach: a claim-less registration whose started flag is set. start()
//     refuses to set it, so the test sets it directly. Without that half the guard
//     survived every mutation — with started false, Close falls into the
//     never-started branch and removes an empty path, which is indistinguishable
//     from the guard's own early return. It is a real test of the guard and not a
//     test that anything today can reach it.

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// serveFrozenTerm appears in every module these tests write AS A WHOLE WORD, so
// "the index still answers" is one query away. Whole-word matters: the analyzer
// indexes tokens, so a term that only ever occurs glued to a numeric suffix is
// never a hit and every assertion below would read 0 no matter what served.
const serveFrozenTerm = "ТестоваяПроцедураЗамороженногоКэша"

// serveFrozenModules is how many modules serveFrozenDump writes, and therefore how
// many hits a healthy serve returns.
const serveFrozenModules = 5

// serveFrozenDump writes a small dump and returns its directory.
func serveFrozenDump(t *testing.T) string {
	t.Helper()
	dumpDir := t.TempDir()
	for i := range serveFrozenModules {
		mkBSLFile(t, dumpDir,
			fmt.Sprintf("CommonModules/Модуль%04d/Ext/Module.bsl", i),
			fmt.Sprintf("Процедура Проверка%04d() Экспорт\n\tСообщить(\"%s %04d\");\nКонецПроцедуры\n",
				i, serveFrozenTerm, i))
	}
	return dumpDir
}

// freezeTree clears every write bit under root and restores them before the test's
// temp dirs are removed. The restore is registered AFTER root was created, so LIFO
// cleanup runs it BEFORE t.TempDir's os.RemoveAll — which would otherwise fail on
// a directory it may not unlink from.
func freezeTree(t *testing.T, root string) {
	t.Helper()
	type saved struct {
		path string
		mode fs.FileMode
	}
	var modes []saved
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, iErr := d.Info()
		if iErr != nil {
			return iErr
		}
		modes = append(modes, saved{p, info.Mode().Perm()})
		return nil
	}); err != nil {
		t.Fatalf("walking %s before freezing it: %v", root, err)
	}
	t.Cleanup(func() {
		for _, s := range modes {
			_ = os.Chmod(s.path, s.mode)
		}
	})
	for i := len(modes) - 1; i >= 0; i-- {
		if err := os.Chmod(modes[i].path, modes[i].mode&^0o222); err != nil {
			t.Fatalf("clearing write bits on %s: %v", modes[i].path, err)
		}
	}
}

// serveFrozenClaimRefused is THE POSITIVE CONTROL for every frozen-arena test: it
// fails the test unless a reader claim on genDir genuinely cannot be written. A
// chmod that did not take, or a test process that can write anyway (root), would
// otherwise let a test pass while exercising the ordinary claim path and proving
// nothing about the frozen one.
func serveFrozenClaimRefused(t *testing.T, genDir string) {
	t.Helper()
	if reg, err := registerReader(genDir); err == nil {
		reg.Close()
		t.Fatalf("the control failed: a reader claim on %s was written even though the arena was "+
			"supposed to be frozen, so this test would prove nothing", genDir)
	}
}

// serveFrozenPrepared builds a generation for dumpDir under cacheDir and RELEASES
// the claim, leaving a READY generation with an empty registry — the state a cache
// is in after the process that built it exited.
func serveFrozenPrepared(t *testing.T, dumpDir, cacheDir string) string {
	t.Helper()
	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("warming the cache: %v", err)
	}
	gensig := gen.Gensig()
	gen.Release()
	return gensig
}

// serveFrozenSearch opens gen for serving and returns the number of hits for the
// term every module carries. It goes through the same FinishServeOpen the real
// serve entry point uses, so a claim-less registration is carried through the
// attach exactly as in production.
func serveFrozenSearch(t *testing.T, dumpDir, cacheDir string, gen *ServeGeneration) int {
	t.Helper()
	idx := NewServePlaceholder(dumpDir)
	t.Cleanup(func() { _ = idx.Close() })
	idx.FinishServeOpen(cacheDir, gen, nil)
	select {
	case <-idx.Done():
	case <-time.After(60 * time.Second):
		t.Fatal("the serve open did not finish within 60s")
	}
	if err := idx.BuildError(); err != nil {
		t.Fatalf("the serve open recorded a build error: %v", err)
	}
	hits, _, err := idx.Search(SearchParams{Query: serveFrozenTerm, Limit: 20})
	if err != nil {
		t.Fatalf("searching the served generation: %v", err)
	}
	return len(hits)
}

// TestReadOnlyCache_FrozenArenaIsServedWithoutAClaim is the regression itself: a
// cache whose write bits are all gone must still serve, because nothing can remove
// what is in it.
func TestReadOnlyCache_FrozenArenaIsServedWithoutAClaim(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)

	freezeTree(t, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)
	serveFrozenClaimRefused(t, genDir)

	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("a frozen cache must still be servable, but the open refused: %v", err)
	}
	// It must have taken the PROVEN route, not an ordinary claim. Without this the
	// test would also pass if the freeze had silently not applied.
	if reason := gen.claim().unreapableReason(); reason == "" {
		t.Fatal("the generation was served with an ordinary reader claim, so the frozen-arena route " +
			"was never exercised")
	} else if !strings.Contains(reason, "no write permission") {
		t.Errorf("the proof does not name the mode that produced it: %q", reason)
	}
	if n := serveFrozenSearch(t, dumpDir, cacheDir, gen); n != serveFrozenModules {
		t.Errorf("the frozen cache served %d modules, want %d", n, serveFrozenModules)
	}
}

// TestReadOnlyCache_ReindexOnAFrozenArenaStillServes covers the path the brief for
// this fix did not name. --reindex skips the ready fast path entirely: it goes to
// forceRebuildGeneration, whose drop cannot happen on a frozen arena, whose build
// then no-ops on the still-READY generation, and whose claim lands in
// claimBuiltGeneration. MEASURED on the real binary before the fix: v1.12.0 served,
// a74dc1a returned "force-rebuilding dump generation ...: permission denied".
func TestReadOnlyCache_ReindexOnAFrozenArenaStillServes(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)

	freezeTree(t, cacheDir)
	serveFrozenClaimRefused(t, serveClaimGenDir(t, dumpDir, cacheDir, gensig))

	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, true)
	if err != nil {
		t.Fatalf("--reindex against a frozen cache must still serve the existing generation, "+
			"but it refused: %v", err)
	}
	if gen.claim().unreapableReason() == "" {
		t.Fatal("the reindex path took an ordinary claim, so the frozen-arena route was not exercised")
	}
	if n := serveFrozenSearch(t, dumpDir, cacheDir, gen); n != serveFrozenModules {
		t.Errorf("the frozen cache served %d modules under --reindex, want %d", n, serveFrozenModules)
	}
}

// TestReadOnlyCache_OpenForServeIsCoveredToo pins the OTHER entry into the claim,
// the one that arrives at attachReadOnlyShards with no claim at all.
func TestReadOnlyCache_OpenForServeIsCoveredToo(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)

	freezeTree(t, cacheDir)
	serveFrozenClaimRefused(t, serveClaimGenDir(t, dumpDir, cacheDir, gensig))

	idx, err := OpenGenerationReadOnly(dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("OpenGenerationReadOnly must serve a frozen arena, but it refused: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	if reason := idx.readerReg.unreapableReason(); reason == "" {
		t.Fatal("the read-only open took an ordinary claim, so the frozen-arena route was not exercised")
	}
	<-idx.Done()
	hits, _, err := idx.Search(SearchParams{Query: serveFrozenTerm, Limit: 20})
	if err != nil {
		t.Fatalf("searching the frozen generation: %v", err)
	}
	if len(hits) != serveFrozenModules {
		t.Errorf("the frozen cache served %d modules, want %d", len(hits), serveFrozenModules)
	}
}

// TestReadOnlyCache_AnArenaAPeerCouldReapIsStillRefused is the other half, and the
// one that stops the fix from becoming the defect it replaced. The generation
// directory is frozen so no claim can be written, but the ARENA around it is not,
// so a reaper can still rename the generation out of it and delete it. There is
// nothing to prove and the only sound answer is to refuse.
func TestReadOnlyCache_AnArenaAPeerCouldReapIsStillRefused(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)

	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)
	freezeTree(t, genDir) // the generation only; g/ above it stays writable
	serveFrozenClaimRefused(t, genDir)

	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err == nil {
		gen.Release()
		t.Fatal("an unclaimable generation in a WRITABLE arena was served; a reaper can remove it, " +
			"so serving it is the defect this whole change set exists to prevent")
	}
	if !strings.Contains(err.Error(), "is writable by this process") {
		t.Errorf("the refusal does not say why no proof was possible: %v", err)
	}
	// And the reaper the refusal is about really can act, which is what makes the
	// refusal correct rather than merely cautious.
	if _, gcErr := GCGenerations(dumpDir, cacheDir, serveClaimForeignGensig); gcErr != nil {
		t.Fatalf("the control failed: GC could not even run against this arena: %v", gcErr)
	}
}

// TestReadOnlyCache_ProbeClassification pins each branch of the proof, including
// the two no test on this machine can reach through the filesystem. See the file
// header for what that does and does not establish.
func TestReadOnlyCache_ProbeClassification(t *testing.T) {
	const arena = "/some/cache/g"
	perr := func(e error) error { return &fs.PathError{Op: "open", Path: arena + "/.reapprobe-1", Err: e} }

	cases := []struct {
		name      string
		probeErr  error
		perm      fs.FileMode
		wantProof string // substring; "" means the classification must be an error
		wantErr   string // substring, checked when wantProof is ""
	}{{
		// MEASURED on a macOS UDRO image: EROFS, and a mode with the owner write
		// bit SET. This is why the mount branch cannot be folded into the mode one.
		name:     "a read-only mount is a proof even though the mode looks writable",
		probeErr: perr(syscall.EROFS), perm: 0o755,
		wantProof: "read-only mount",
	}, {
		name:     "no write bit for anyone is a proof",
		probeErr: perr(syscall.EACCES), perm: 0o555,
		wantProof: "no write permission for its owner, its group or others (mode 0555)",
	}, {
		name:     "unwritable for us but writable for its owner is NOT a proof",
		probeErr: perr(syscall.EACCES), perm: 0o755,
		wantErr: "grants write to its owner or its group",
	}, {
		name:     "unwritable for us but writable for its group is NOT a proof",
		probeErr: perr(syscall.EACCES), perm: 0o775,
		wantErr: "grants write to its owner or its group",
	}, {
		// A full disk refuses the probe too. Treating any refusal as a proof would
		// serve an ENOSPC arena unprotected, and an ENOSPC arena is fully reapable.
		name:     "a failure that is not about permission is never a proof",
		probeErr: perr(syscall.ENOSPC), perm: 0o555,
		wantErr: "probing whether the generations arena",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proof, err := classifyArenaProbe(arena, tc.probeErr, tc.perm)
			if tc.wantProof == "" {
				if err == nil {
					t.Fatalf("this must not be a proof, but it returned one: %q", proof)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("this must be a proof, but it returned an error: %v", err)
			}
			if !strings.Contains(proof, tc.wantProof) {
				t.Errorf("proof %q does not contain %q", proof, tc.wantProof)
			}
		})
	}
}

// TestReadOnlyCache_ArenaProbeAnswersTheRealFilesystem pins the probe half, which
// the classifier table cannot: that a writable arena is reported as writable and a
// frozen one is not, through real syscalls rather than synthesised errors.
func TestReadOnlyCache_ArenaProbeAnswersTheRealFilesystem(t *testing.T) {
	arena := t.TempDir()

	if _, err := arenaUnreapable(arena); err == nil {
		t.Fatal("a writable arena must never yield a proof that nothing can reap it")
	}
	// The probe must leave nothing behind: a file loitering in g/ is something every
	// reaper and every generation scan has to step over.
	entries, err := os.ReadDir(arena)
	if err != nil {
		t.Fatalf("reading the arena after the probe: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the write probe left %d entries behind in the arena: %v", len(entries), entries)
	}

	freezeTree(t, arena)
	proof, err := arenaUnreapable(arena)
	if err != nil {
		t.Fatalf("a frozen arena must yield a proof, got: %v", err)
	}
	if !strings.Contains(proof, arena) {
		t.Errorf("the proof does not name the arena it is about: %q", proof)
	}
}

// TestReadOnlyCache_ClaimlessRegistrationRecordsNothing pins what the claim-less
// registration IS, which is the part that must never quietly become a claim-shaped
// thing that protects nothing on a writable arena. It records nothing, no peer sees
// it as a holder, it never heartbeats, and closing it is a no-op that returns.
func TestReadOnlyCache_ClaimlessRegistrationRecordsNothing(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	reg := unreapableClaim("because this test says so")
	if reg.unreapableReason() != "because this test says so" {
		t.Errorf("the proof was not carried: %q", reg.unreapableReason())
	}
	if reg.path != "" || reg.tmpPath != "" {
		t.Errorf("a claim-less registration names files it never wrote: path=%q tmpPath=%q",
			reg.path, reg.tmpPath)
	}

	reg.start()
	if reg.started.Load() {
		t.Error("a claim-less registration started a heartbeat; there is no entry for it to touch, " +
			"so it would raise the lost-claim alarm every interval about a claim never taken")
	}

	// HONEST ABOUT THE GUARANTEE: it is not a hold, and no peer is told it is. A
	// registry that reported this as a live reader would be claiming a protection
	// that does not exist.
	live, err := generationHasLiveReader(genDir)
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if live {
		t.Error("the registry reports a live reader although nothing was written")
	}

	done := make(chan struct{})
	go func() { reg.Close(); reg.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close on a claim-less registration blocked; there is no heartbeat to wait for")
	}

	// AND THE SAME WITH started SET, which is the only state in which Close's
	// unreapable guard is load-bearing. It is NOT reachable from any production path
	// today — start() refuses to set it for a claim-less registration, which is what
	// the assertion above pins — so this constructs it directly, the way
	// serve_claim_test.go's nil-claim tests construct theirs. Without the guard,
	// Close falls into the heartbeat teardown and waits for a goroutine that was
	// never started, for ever.
	stuck := unreapableClaim("proof")
	stuck.started.Store(true)
	closed := make(chan struct{})
	go func() { stuck.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited on a heartbeat that was never started")
	}
}

// TestReadOnlyCache_RefusalNamesARemedy pins the text an operator actually gets on
// the path they will actually hit. Before this fix the fast path's failure returned
// through PrepareServeGeneration with only a generic wrapper: the sentence naming a
// remedy lived on heldGeneration and attachReadOnlyShards, neither of which this
// failure reaches.
func TestReadOnlyCache_RefusalNamesARemedy(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)

	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)
	freezeTree(t, genDir)
	serveFrozenClaimRefused(t, genDir)

	rec := captureLogs(t)
	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err == nil {
		gen.Release()
		t.Fatal("this configuration must be refused")
	}
	errors := strings.Join(rec.atLevel(slog.LevelError), "\n")
	for _, want := range []string{"MCP_1C_CACHE_DIR", "--cache-dir", "cannot claim"} {
		if !strings.Contains(errors, want) {
			t.Errorf("the ERROR-level log of the refusal does not contain %q; got:\n%s", want, errors)
		}
	}
}

// TestReadOnlyCache_ServingUnclaimedIsAudible pins the other message. It is logged
// at ERROR and not at WARN on purpose: cmd/mcp-1c/main.go pins the default handler
// to LevelError in stdio mode, and as a Warn this line produced ZERO output across
// the read-only-cache and read-only-mount runs it describes.
func TestReadOnlyCache_ServingUnclaimedIsAudible(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)

	freezeTree(t, cacheDir)
	serveFrozenClaimRefused(t, serveClaimGenDir(t, dumpDir, cacheDir, gensig))

	rec := captureLogs(t)
	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("the frozen cache must be served: %v", err)
	}
	gen.Release()

	// At ERROR, and ONLY the ERROR list is consulted: asserting on every level would
	// pass just as happily with the Warn this used to be, which is the exact defect.
	errs := strings.Join(rec.atLevel(slog.LevelError), "\n")
	if !strings.Contains(errs, "without a reader claim") {
		t.Errorf("the claim-less serve was not reported at ERROR, so it is written nowhere in stdio "+
			"mode; ERROR records were:\n%s", errs)
	}
}
