package dump

// A cache this process may READ but not WRITE.
//
// THE PROPERTY these tests pin, in one sentence: the server never SILENTLY serves
// an index generation it could not protect. Both halves are load-bearing and each
// one alone is a defect.
//
//   - It SERVES. Making the reader claim mandatory turned every generation in an
//     unwritable cache into a permanent refusal. Measured on the real binary against
//     a warm cache that had just answered: chmod -R a-w, and the next start returns
//     "search: index build failed: claiming the existing generation ...: permission
//     denied", and so does every one after it. v1.12.0 on the same cache answers.
//   - It SAYS SO, where the user is looking. The claim-less serve is logged at ERROR
//     and, crucially, carried into the MCP tool response through
//     (*Index).UnprotectedReason. A log line under the cache directory is not a
//     report; the shipped release "warned" that way and nobody ever saw it.
//
// AND IT NO LONGER CONSULTS THE MODE BITS. The previous attempt allowed a silent
// claim-less serve against a "proof" built from os.Stat().Mode().Perm(): a
// permission-refused probe plus a mode with no write bit for anyone. That proof is
// FALSE, and TestReadOnlyCache_AnACLBearing0555ArenaIsNotSilentlyTrusted is the
// measurement that kills it. The only proof still accepted is EROFS, which is the
// kernel asserting a property of the whole filesystem rather than of our
// credentials.
//
// EVERY FROZEN-ARENA TEST CARRIES A POSITIVE CONTROL that the claim really could
// not be written (serveFrozenClaimRefused). Without it a chmod that silently failed,
// or a test process that happened to be able to write anyway, would let the whole
// file pass with nothing to suppress.
//
// WHAT IS NOT PINNED HERE, stated rather than left to be discovered:
//
//   - The EROFS branch is pinned only through probeProvesReadOnlyFilesystem, on
//     synthesised errors, because no unit test can portably create a read-only
//     mount. The inputs are MEASURED, not invented: on a macOS UDRO image the probe
//     fails with EROFS while os.Stat reports mode 0755. NOTHING HERE PINS THE
//     END-TO-END EROFS PATH — that an index on a read-only mount is served AND
//     carries no notice is pinned only at the unit boundary (the claimless
//     registration with an empty reason), never through a real mount.
//   - Nothing here pins the Windows behaviour. There directory rights are ACLs,
//     which is now irrelevant to the decision — the mode is not consulted anywhere —
//     but the probe's error classification on Windows is untested on this machine.
//   - The ACL test is darwin-only and skips elsewhere. It shells out to /bin/chmod
//     because Go's os package cannot set an ACL.
//   - Close's claimless guard IS pinned, but only against a state no production path
//     can reach: a claimless registration whose started flag is set. start() refuses
//     to set it, so the test sets it directly. Without that half the guard survived
//     every mutation — with started false, Close falls into the never-started branch
//     and removes an empty path, which is indistinguishable from the guard's own
//     early return. It is a real test of the guard and not a test that anything
//     today can reach it.

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// attach exactly as in production. idx is returned so the caller can also read the
// state the attach published.
func serveFrozenSearch(t *testing.T, dumpDir, cacheDir string, gen *ServeGeneration) (int, *Index) {
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
	return len(hits), idx
}

// TestReadOnlyCache_FrozenArenaIsServedAndReported is the regression itself, and
// both of its assertions are the point: a cache whose write bits are all gone must
// still serve, AND the process must say it is serving unprotected.
func TestReadOnlyCache_FrozenArenaIsServedAndReported(t *testing.T) {
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
	// It must have taken the claim-less route, not an ordinary claim. Without this
	// the test would also pass if the freeze had silently not applied.
	reason := gen.claim().unprotectedReason()
	if reason == "" {
		t.Fatal("the generation reports itself protected; either it took an ordinary reader claim, " +
			"so the frozen-arena route was never exercised, or it is serving unprotected in silence")
	}
	if !strings.Contains(reason, gensig) {
		t.Errorf("the reason does not name the generation it is about: %q", reason)
	}

	hits, idx := serveFrozenSearch(t, dumpDir, cacheDir, gen)
	if hits != serveFrozenModules {
		t.Errorf("the frozen cache served %d modules, want %d", hits, serveFrozenModules)
	}
	// THE STATE MUST SURVIVE INTO THE INDEX, because that is the only place the tool
	// layer can read it. A reason carried on the registration and dropped at the
	// attach is a warning that never reaches a user.
	if idx.UnprotectedReason() == "" {
		t.Error("the index serving the frozen generation reports itself protected, so no tool " +
			"response will carry a notice and the serve is silent after all")
	}
}

// TestReadOnlyCache_AnACLBearing0555ArenaIsNotSilentlyTrusted is the measurement
// that killed the mode-bits proof, kept as a test.
//
// The previous attempt served an arena claim-less and IN SILENCE whenever a refused
// probe met a mode with no write bit for anyone. os.Stat().Mode().Perm() cannot see
// ACLs, so an arena reporting 0555 that a group may still write was declared
// unreapable. It is not: the real reap — the same os.Rename out of the arena that
// claimGenerationForRemoval performs — succeeds on such a directory and leaves the
// mode still reading 0555. ~/Library/Caches on macOS carries an ACL already, so
// this is routine rather than exotic.
//
// The no-ACL control is the plain frozen arena in the test above: same mode, no
// ACL, and it must reach the same verdict. If the two ever diverged, the mode or
// the ACL would be deciding something again.
func TestReadOnlyCache_AnACLBearing0555ArenaIsNotSilentlyTrusted(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("chmod +a and ls -le are macOS ACL tools; this vector is not portable")
	}
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)

	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)
	arena := filepath.Dir(genDir)
	freezeTree(t, cacheDir)

	// The ACL grants write to a group this test process is NOT in, which is what
	// makes the arena unwritable for us and writable for someone else — exactly the
	// shape the mode-bits proof waved through.
	if out, err := exec.Command("/bin/chmod", "+a",
		"group:wheel allow write,delete,add_file,add_subdirectory,delete_child", arena).CombinedOutput(); err != nil {
		t.Skipf("cannot set an ACL on %s (%v): %s", arena, err, out)
	}

	// VERIFY THE FIXTURE IS WHAT IT CLAIMS TO BE, on both counts, or the test proves
	// nothing about ACLs at all.
	out, err := exec.Command("/bin/ls", "-lde", arena).CombinedOutput()
	if err != nil {
		t.Fatalf("ls -le on %s: %v", arena, err)
	}
	if !strings.Contains(string(out), "group:wheel allow") {
		t.Fatalf("the control failed: no ACL is present on %s after chmod +a:\n%s", arena, out)
	}
	st, err := os.Stat(arena)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o555 {
		t.Fatalf("the control failed: the arena reports mode %04o, not 0555, so it is not the "+
			"fixture this test is about", perm)
	}
	serveFrozenClaimRefused(t, genDir)

	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("an ACL-bearing read-only cache must still serve, but the open refused: %v", err)
	}
	if gen.claim().unprotectedReason() == "" {
		t.Fatalf("an arena reporting mode 0555 WITH an ACL granting write to group wheel was served "+
			"as if protected. A wheel process can rename the generation out of %s and delete it, "+
			"and Mode().Perm() cannot see that ACL, so this serve is silent and unprotected", arena)
	}
	hits, idx := serveFrozenSearch(t, dumpDir, cacheDir, gen)
	if hits != serveFrozenModules {
		t.Errorf("the ACL-bearing cache served %d modules, want %d", hits, serveFrozenModules)
	}
	if idx.UnprotectedReason() == "" {
		t.Error("the index reports itself protected, so no tool response will carry a notice")
	}
}

// TestReadOnlyCache_ReindexOnAFrozenArenaStillServes covers the path the brief for
// the previous fix did not name. --reindex skips the ready fast path entirely: it
// goes to forceRebuildGeneration, whose drop cannot happen on a frozen arena, whose
// build then no-ops on the still-READY generation, and whose claim lands in
// claimBuiltGeneration. MEASURED on the real binary before that fix: v1.12.0
// served, a74dc1a returned "force-rebuilding dump generation ...: permission
// denied".
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
	if gen.claim().unprotectedReason() == "" {
		t.Fatal("the reindex path reports the generation protected, so either it took an ordinary " +
			"claim and the frozen-arena route was not exercised, or it is serving silently")
	}
	if hits, _ := serveFrozenSearch(t, dumpDir, cacheDir, gen); hits != serveFrozenModules {
		t.Errorf("the frozen cache served %d modules under --reindex, want %d", hits, serveFrozenModules)
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
	if idx.UnprotectedReason() == "" {
		t.Fatal("the read-only open reports the generation protected, so either it took an ordinary " +
			"claim and the frozen-arena route was not exercised, or it is serving silently")
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

// TestReadOnlyCache_AnArenaAPeerCouldReapIsServedAndReported is the case the
// previous attempt refused. The generation directory is frozen so no claim can be
// written, but the ARENA around it is not, so a reaper can rename the generation
// out of it and delete it. The owner's decision is that breaking a working setup to
// guard against that is worse than the risk, PROVIDED the risk is not hidden: this
// serves, and it reports.
//
// The reaper the notice is about really can act, which is what stops this from
// being a test of a warning about nothing.
func TestReadOnlyCache_AnArenaAPeerCouldReapIsServedAndReported(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)

	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)
	freezeTree(t, genDir) // the generation only; g/ above it stays writable
	serveFrozenClaimRefused(t, genDir)

	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("an unclaimable generation in a writable arena must be served, not refused: %v", err)
	}
	if gen.claim().unprotectedReason() == "" {
		t.Fatal("a generation a peer's reaper can remove was served as if protected")
	}
	if hits, idx := serveFrozenSearch(t, dumpDir, cacheDir, gen); hits != serveFrozenModules {
		t.Errorf("the served generation returned %d modules, want %d", hits, serveFrozenModules)
	} else if idx.UnprotectedReason() == "" {
		t.Error("the index reports itself protected, so no tool response will carry a notice")
	}

	// CONTROL: the reaper really can run against this arena.
	if _, gcErr := GCGenerations(dumpDir, cacheDir, serveClaimForeignGensig); gcErr != nil {
		t.Fatalf("the control failed: GC could not even run against this arena: %v", gcErr)
	}
}

// TestReadOnlyCache_AHealthyCacheIsSilent is requirement-shaped rather than
// defect-shaped: a warning on a properly claimed index is the same defect as a
// refusal, just quieter. An ordinary writable cache must produce a live claim, an
// empty reason, and NO ERROR-level log line.
func TestReadOnlyCache_AHealthyCacheIsSilent(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()

	rec := captureLogs(t)
	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("an ordinary writable cache must serve: %v", err)
	}
	if reason := gen.claim().unprotectedReason(); reason != "" {
		t.Errorf("a writable cache reported itself unprotected: %q", reason)
	}
	if gen.claim().claimless {
		t.Error("a writable cache produced a claimless registration, so nothing records the serve")
	}
	hits, idx := serveFrozenSearch(t, dumpDir, cacheDir, gen)
	if hits != serveFrozenModules {
		t.Errorf("the healthy cache served %d modules, want %d", hits, serveFrozenModules)
	}
	if reason := idx.UnprotectedReason(); reason != "" {
		t.Errorf("the index on a healthy cache would put a notice on every tool response: %q", reason)
	}
	for _, msg := range rec.atLevel(slog.LevelError) {
		if strings.Contains(msg, "without a reader claim") {
			t.Errorf("a healthy serve logged the unprotected line at ERROR: %q", msg)
		}
	}
}

// TestReadOnlyCache_AReapedGenerationIsNotServedAsAnEmptyIndex is the defect the
// removal of the refusal opened, closed.
//
// Serving on a failed claim means the reaped generation is no longer stopped by
// the claim on its way past, and nothing below the claim could tell "removed" from
// "empty": cacheShardDirs swallows its ReadDir error, openCachedShards accepts an
// empty list, and LoadManifest reports a missing manifest as no manifest, so
// loadNamesReadOnly walks the dump instead. MEASURED before this guard: ready=true,
// buildErr=nil, 0 shards, 5 names, and every search answering "cannot perform
// operation on empty alias". A component that reports success while holding nothing
// is the failure mode this whole change set exists to prevent.
//
// It calls openReadOnlyFrom rather than OpenGenerationReadOnly on purpose: the
// state under test is the one AFTER the READY gate has passed, which is precisely
// the window a real reaper wins.
func TestReadOnlyCache_AReapedGenerationIsNotServedAsAnEmptyIndex(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	// POSITIVE CONTROL: the same call on the same generation, still present, opens
	// and answers. Without it a guard that refused everything would pass below.
	ctrl, err := openReadOnlyFrom(dumpDir, cacheDir, genDir, nil)
	if err != nil {
		t.Fatalf("the control failed: an intact generation must open: %v", err)
	}
	<-ctrl.Done()
	if hits, _, sErr := ctrl.Search(SearchParams{Query: serveFrozenTerm, Limit: 20}); sErr != nil || len(hits) != serveFrozenModules {
		t.Fatalf("the control failed: an intact generation answered %d hits (err %v), want %d",
			len(hits), sErr, serveFrozenModules)
	}
	_ = ctrl.Close()

	// The reaper won the race: claimGenerationForRemoval renamed the generation out
	// of the arena and deleted it, taking the READY sentinel with it.
	if err := os.RemoveAll(genDir); err != nil {
		t.Fatal(err)
	}

	idx, err := openReadOnlyFrom(dumpDir, cacheDir, genDir, nil)
	if err == nil {
		t.Cleanup(func() { _ = idx.Close() })
		<-idx.Done()
		hits, total, sErr := idx.Search(SearchParams{Query: serveFrozenTerm, Limit: 20})
		t.Fatalf("a generation that no longer exists was opened for serving: ready=%v buildErr=%v "+
			"shards=%d names=%d, and a search over it returns hits=%d total=%d err=%v",
			idx.Ready(), idx.BuildError(), len(idx.shards), len(idx.names), len(hits), total, sErr)
	}
	if !strings.Contains(err.Error(), gensig) {
		t.Errorf("the refusal does not name the generation it is about: %v", err)
	}
}

// TestReadOnlyCache_AnEmptyDumpIsStillServed is the other half of the guard above,
// and it is not hypothetical: MEASURED, a dump directory with no .bsl at all builds
// a perfectly good generation with ZERO shard directories and its READY sentinel in
// place (entries: READY, readers). A guard that refused on "no shards" alone would
// therefore refuse every server started against an empty dump — a spurious refusal,
// which is the exact defect class this whole change is undoing.
//
// This test exists because a mutation that dropped the READY half of the condition
// SURVIVED the rest of the suite: nothing else opens an empty-dump generation
// through the read-only attach.
func TestReadOnlyCache_AnEmptyDumpIsStillServed(t *testing.T) {
	dumpDir := t.TempDir() // deliberately empty
	cacheDir := t.TempDir()

	gen, err := PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("an empty dump must still prepare a generation: %v", err)
	}
	gensig := gen.Gensig()
	gen.Release()

	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)
	// The fixture is what the guard has to tell apart from a reaped generation:
	// no shards, sentinel present. Asserted, not assumed.
	if n := len(cacheShardDirs(genDir)); n != 0 {
		t.Fatalf("the fixture is wrong: an empty dump produced %d shard dirs, so this test does not "+
			"exercise the zero-shard branch", n)
	}
	if !generationReadyDir(genDir) {
		t.Fatal("the fixture is wrong: the empty-dump generation carries no READY sentinel")
	}

	idx, err := OpenGenerationReadOnly(dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("an empty dump must be served, not refused: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()
	if !idx.Ready() {
		t.Fatalf("the empty-dump index never became ready: %v", idx.BuildError())
	}
}

// TestReadOnlyCache_ARefusedAttachReleasesTheClaimItWasGiven pins the ownership
// contract on the guard's own failure path. attachReadOnlyShards owns the claim it
// is handed on EVERY path; a refusal that returned without releasing would pin the
// generation against every reaper for the life of the process, and would leave the
// index reporting an unprotected serve that is not happening.
//
// It also needed writing because a mutation deleting exactly that release SURVIVED:
// the reaped-generation test above cannot reach it, since a generation that is gone
// is one no claim could have been taken on in the first place.
func TestReadOnlyCache_ARefusedAttachReleasesTheClaimItWasGiven(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	// A REAL claim, held, exactly as a producing path would hand one down.
	held, err := registerReader(genDir)
	if err != nil {
		t.Fatalf("taking the claim this test hands in: %v", err)
	}
	if live, lErr := generationHasLiveReader(genDir); lErr != nil || !live {
		t.Fatalf("the control failed: the claim is not visible in the registry (live=%v err=%v)", live, lErr)
	}

	// Now strip the generation down to the state the guard refuses: no shards, no
	// sentinel, but the directory and its registry still there.
	for _, d := range cacheShardDirs(genDir) {
		if rErr := os.RemoveAll(d); rErr != nil {
			t.Fatal(rErr)
		}
	}
	if rErr := os.Remove(filepath.Join(genDir, readySentinelName)); rErr != nil {
		t.Fatal(rErr)
	}

	// openReadOnlyFrom is the caller that hands a held claim down, and it returns
	// the attach's error without leaving an Index behind. A bare NewServePlaceholder
	// would be the wrong instrument here: its Close blocks on a done channel that
	// only FinishServeOpen closes.
	idx, aErr := openReadOnlyFrom(dumpDir, cacheDir, genDir, held)
	if aErr == nil {
		_ = idx.Close()
		t.Fatal("a generation with no shards and no READY sentinel was attached")
	}
	if live, lErr := generationHasLiveReader(genDir); lErr != nil {
		t.Fatalf("reading the registry after the refusal: %v", lErr)
	} else if live {
		t.Error("the refused attach kept the reader claim it was handed; the generation stays pinned " +
			"against every reaper for the life of the process")
	}
}

// thawTree gives every write bit back under root, so a test can move a cache from
// unwritable to writable in the middle of a run.
func thawTree(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, iErr := d.Info()
		if iErr != nil {
			return iErr
		}
		return os.Chmod(p, info.Mode().Perm()|0o200)
	}); err != nil {
		t.Fatalf("restoring write bits under %s: %v", root, err)
	}
}

// TestReadOnlyCache_AReloadRepublishesTheState pins the OTHER place the reason is
// installed. attachReadOnlyShards sets it at the open; swapGeneration must reset it
// when a reload replaces the generation, or the notice goes on describing a
// generation nobody is serving any more.
//
// It runs the recovery direction — unprotected, then the cache becomes writable and
// a reload lands a properly claimed generation — because that is the direction a
// stale value is VISIBLE in: a notice that never clears would tell a user with a
// perfectly healthy cache that their index is unprotected, for the life of the
// process, with no way to make it stop.
func TestReadOnlyCache_AReloadRepublishesTheState(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)

	freezeTree(t, cacheDir)
	serveFrozenClaimRefused(t, serveClaimGenDir(t, dumpDir, cacheDir, gensig))

	idx, err := OpenGenerationReadOnly(dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("the frozen cache must serve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()
	if idx.UnprotectedReason() == "" {
		t.Fatal("the precondition failed: the open is not in the unprotected state this test is about")
	}

	// The operator does what the notice told them to: the cache becomes writable.
	// The dump also moves, because an unchanged dump returns from Reload before it
	// ever reaches the swap.
	thawTree(t, cacheDir)
	mkBSLFile(t, dumpDir, "CommonModules/МодульПослеПерезагрузки/Ext/Module.bsl",
		fmt.Sprintf("Процедура Новая() Экспорт\n\tСообщить(\"%s 9999\");\nКонецПроцедуры\n", serveFrozenTerm))

	rep, err := idx.Reload()
	if err != nil {
		t.Fatalf("the reload onto a now-writable cache failed: %v", err)
	}
	if !rep.Changed {
		t.Fatal("the reload reported no change, so it never reached the generation swap and this " +
			"test proves nothing")
	}
	if reason := idx.UnprotectedReason(); reason != "" {
		t.Errorf("the index kept warning after a reload took a real reader claim; the notice now "+
			"describes a retired generation and cannot be cleared: %q", reason)
	}
	if hits, _, sErr := idx.Search(SearchParams{Query: serveFrozenTerm, Limit: 20}); sErr != nil {
		t.Fatalf("searching after the reload: %v", sErr)
	} else if len(hits) != serveFrozenModules+1 {
		t.Errorf("the reloaded index served %d modules, want %d", len(hits), serveFrozenModules+1)
	}
}

// TestReadOnlyCache_OnlyEROFSProvesAnything pins what the proof is now, and every
// case it must reject. The mode is not an input at all any more: the function does
// not take one.
func TestReadOnlyCache_OnlyEROFSProvesAnything(t *testing.T) {
	perr := func(e error) error { return &fs.PathError{Op: "open", Path: "/c/g/.reapprobe-1", Err: e} }

	cases := []struct {
		name  string
		err   error
		proof bool
	}{{
		// MEASURED on a macOS UDRO image: EROFS, and a mode with the owner write bit
		// SET. The kernel is asserting a property of the filesystem, not of our
		// credentials, so nobody can reap here.
		name: "a read-only mount is the one proof", err: perr(syscall.EROFS), proof: true,
	}, {
		// The case that must NEVER be a proof again. A permission refusal says "not
		// you"; the owner, an ACL holder or root may still write and still reap. The
		// previous attempt turned exactly this, plus a 0555 mode, into a silent serve.
		name: "permission denied is not a proof", err: perr(syscall.EACCES), proof: false,
	}, {
		// A full disk refuses the probe too, and an ENOSPC arena is fully reapable.
		name: "a full disk is not a proof", err: perr(syscall.ENOSPC), proof: false,
	}, {
		name: "a successful probe is not a proof", err: nil, proof: false,
	}, {
		name: "a missing directory is not a proof", err: perr(syscall.ENOENT), proof: false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeProvesReadOnlyFilesystem(tc.err); got != tc.proof {
				t.Errorf("probeProvesReadOnlyFilesystem(%v) = %v, want %v", tc.err, got, tc.proof)
			}
		})
	}
}

// TestReadOnlyCache_ArenaProbeAnswersTheRealFilesystem pins the probe half, which
// the classifier cannot: that a writable arena is reported writable and a frozen
// one is not, through real syscalls rather than synthesised errors.
func TestReadOnlyCache_ArenaProbeAnswersTheRealFilesystem(t *testing.T) {
	arena := t.TempDir()

	if err := arenaWriteProbe(arena); err != nil {
		t.Fatalf("a writable arena must accept the probe, got: %v", err)
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
	err = arenaWriteProbe(arena)
	if err == nil {
		t.Fatal("a frozen arena must refuse the probe")
	}
	// And that refusal is NOT a proof, which is the whole change: a frozen directory
	// is unwritable for us and says nothing about anyone else.
	if probeProvesReadOnlyFilesystem(err) {
		t.Errorf("a chmod-frozen directory was taken as a read-only filesystem: %v", err)
	}
}

// TestReadOnlyCache_ClaimlessRegistrationRecordsNothing pins what the claim-less
// registration IS, which is the part that must never quietly become a claim-shaped
// thing that protects nothing. It records nothing, no peer sees it as a holder, it
// never heartbeats, and closing it is a no-op that returns.
func TestReadOnlyCache_ClaimlessRegistrationRecordsNothing(t *testing.T) {
	dumpDir := serveFrozenDump(t)
	cacheDir := t.TempDir()
	gensig := serveFrozenPrepared(t, dumpDir, cacheDir)
	genDir := serveClaimGenDir(t, dumpDir, cacheDir, gensig)

	reg := claimlessRegistration("because this test says so")
	if reg.unprotectedReason() != "because this test says so" {
		t.Errorf("the reason was not carried: %q", reg.unprotectedReason())
	}
	if !reg.claimless {
		t.Error("a claimless registration does not report itself claimless, so start() and Close() " +
			"will treat it as one holding a real entry")
	}
	if reg.path != "" || reg.tmpPath != "" {
		t.Errorf("a claim-less registration names files it never wrote: path=%q tmpPath=%q",
			reg.path, reg.tmpPath)
	}
	// The read-only-filesystem shape: claimless, and deliberately WITHOUT a reason,
	// because there is nothing to tell. An empty reason here is a positive statement,
	// not a missing one.
	proven := claimlessRegistration("")
	if !proven.claimless || proven.unprotectedReason() != "" {
		t.Errorf("the proven-safe shape is wrong: claimless=%v reason=%q",
			proven.claimless, proven.unprotectedReason())
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
	// claimless guard is load-bearing. It is NOT reachable from any production path
	// today — start() refuses to set it for a claim-less registration, which is what
	// the assertion above pins — so this constructs it directly, the way
	// serve_claim_test.go's nil-claim tests construct theirs. Without the guard,
	// Close falls into the heartbeat teardown and waits for a goroutine that was
	// never started, for ever.
	stuck := claimlessRegistration("reason")
	stuck.started.Store(true)
	closed := make(chan struct{})
	go func() { stuck.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited on a heartbeat that was never started")
	}
}

// TestReadOnlyCache_ServingUnclaimedIsAudible pins the log half. It is logged at
// ERROR and not at WARN on purpose: cmd/mcp-1c/main.go pins the default handler to
// LevelError in stdio mode, and as a Warn this line produced ZERO output across the
// read-only-cache runs it describes.
//
// The log is not the delivery this fix is about — that is the tool response, pinned
// in tools/index_notice_test.go — but it is the record an operator reads afterwards
// and it must not regress to invisible.
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
	// AND IT MUST NAME THE REMEDY, on the path the operator actually reaches. The
	// previous shape of this failure returned through PrepareServeGeneration with
	// only a generic wrapper, and the sentence naming a remedy lived on functions
	// that failure never reached.
	for _, want := range []string{"MCP_1C_CACHE_DIR", "--cache-dir"} {
		if !strings.Contains(errs, want) {
			t.Errorf("the ERROR log of an unprotected serve does not name %q; got:\n%s", want, errs)
		}
	}
	// AND IT MUST NOT PROMISE SERVICE IT CANNOT DELIVER. The text this replaced said
	// "The server is serving normally; a read-only cache cannot pick up a changed
	// dump until it is writable again". Measured: once the dump actually changes a
	// frozen cache gives NO service at all — the initial build cannot create its
	// generation temp dir and reload_dump cannot recover.
	if strings.Contains(errs, "serving normally") {
		t.Errorf("the ERROR log still claims the server keeps serving normally after a dump change, "+
			"which is measurably false:\n%s", errs)
	}
}
