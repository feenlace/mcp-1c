package dump

// Part-level tests for the reader-claim primitives.
//
// generation_reap_test.go pins the DEFECTS end to end, through the real serve,
// GC and reload paths. These pin the individual mechanisms those paths are built
// out of, because several of them mutually mask: reverting one alone leaves
// another catching the same end-to-end symptom, so an end-to-end suite cannot
// tell you which part is load-bearing. Each test here is written so that
// reverting exactly one part turns exactly this test red.
//
// NOT PINNED, stated rather than left to be discovered. One part has no
// deterministic test: the post-claim READY re-read at the end of claimReader (its
// "lost its READY marker while this process was claiming it" branch). Reverting
// it alone keeps the whole suite green. It is reachable only when a reaper's
// claim-by-rename lands between this process's claim readback and that re-read —
// a two-syscall interleaving with no seam to drive it through — and on the build
// path it is unreachable by construction, because the temp directory it re-reads
// is private. The rest of the barrier chain IS pinned: the pre-check by
// TestClaim_ReadyMarkerIsRequired, the readback by
// TestReap_UnreadableRegistryMustNotServe, and the reaper's half by
// TestReap_GCMustNotReapAnUntrustworthyRegistry.

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// levelRecorder is a slog.Handler that remembers the level every record was
// emitted at, so a test can assert on the SEVERITY of an operational message and
// not only on whether the code took a branch.
type levelRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *levelRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *levelRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *levelRecorder) WithGroup(string) slog.Handler      { return h }

// atLevel returns the messages recorded at exactly lvl.
func (h *levelRecorder) atLevel(lvl slog.Level) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level == lvl {
			out = append(out, r.Message)
		}
	}
	return out
}

// captureLogs installs a recording handler as the default logger for the test.
func captureLogs(t *testing.T) *levelRecorder {
	t.Helper()
	rec := &levelRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

// TestClaim_RegistryDirIsNeverCreatedRecursively pins the single-level create.
//
// It has to be a direct test of ensureReaderRegistryDir rather than of a claim,
// because in a claim this part is MASKED: claimReader also refuses a workDir with
// no READY marker, and a generation whose directory is gone has no READY either,
// so reverting os.Mkdir to os.MkdirAll leaves the end-to-end refusal intact while
// silently restoring the resurrection. The resurrection is the actual harm — it
// is what turned a reaped generation into a live index answering nothing — so it
// gets its own assertion, on the filesystem, not on an error string.
func TestClaim_RegistryDirIsNeverCreatedRecursively(t *testing.T) {
	arena := t.TempDir()
	genDir := filepath.Join(arena, "g", "deadbeefdeadbeef")

	// POSITIVE CONTROL: with the generation directory present, the create works.
	// Without this, a function that always failed would pass the assertion below.
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readersDir, err := ensureReaderRegistryDir(genDir)
	if err != nil {
		t.Fatalf("control: creating the registry inside an existing generation must succeed: %v", err)
	}
	if _, err := os.Stat(readersDir); err != nil {
		t.Fatalf("control: the registry dir was not created: %v", err)
	}
	// Idempotent: a second call on an existing registry is not an error.
	if _, err := ensureReaderRegistryDir(genDir); err != nil {
		t.Fatalf("control: creating an already-existing registry must be a no-op: %v", err)
	}

	// THE PROPERTY: the generation is reaped, taking the whole subtree with it.
	if err := os.RemoveAll(filepath.Join(arena, "g")); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureReaderRegistryDir(genDir); err == nil {
		t.Fatal("creating the reader registry SUCCEEDED under a generation that no longer exists; " +
			"a recursive create resurrects the generation directory as an empty shell, and the open " +
			"that follows attaches zero shards and reports success")
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want a not-exist error for a removed generation, got %v", err)
	}
	if _, err := os.Stat(genDir); !os.IsNotExist(err) {
		t.Fatalf("the reader registry create RESURRECTED the removed generation directory %s", genDir)
	}
}

// TestClaim_ReadyMarkerIsRequired pins the other half of the pair above: a
// generation directory that exists but carries no READY marker must not be
// claimable. That is the shape of an orphaned READY-less generation, and of a
// generation a reaper has stripped, and neither is something to serve.
func TestClaim_ReadyMarkerIsRequired(t *testing.T) {
	arena := t.TempDir()
	genDir := filepath.Join(arena, "g", "cafebabecafebabe")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// The assertion is on WHICH guard answered, not merely that one did. claimReader
	// carries two READY checks — a pre-check before the claim is written and a
	// barrier re-read after it becomes visible — and they mask each other for any
	// test that only asks "was there an error": drop the pre-check and the barrier
	// catches this same fixture. They answer different questions, and they say so,
	// so pinning the pre-check means pinning its answer.
	if _, err := registerReader(genDir); err == nil {
		t.Fatal("a generation with no READY marker was claimed; it has either never been published or " +
			"has already been claimed for removal, and neither may be served")
	} else if !strings.Contains(err.Error(), "cannot be claimed") {
		t.Fatalf("a generation that never had a %s marker must be refused BEFORE a claim is written, by the "+
			"pre-check that says so; got the post-claim barrier's answer instead: %v", readySentinelName, err)
	}

	// POSITIVE CONTROL: the same directory, once READY, IS claimable — so the
	// refusal above is caused by the marker and not by the fixture shape.
	if err := writeReadySentinel(genDir, "cafebabecafebabe"); err != nil {
		t.Fatal(err)
	}
	reg, err := registerReader(genDir)
	if err != nil {
		t.Fatalf("control: a READY generation must be claimable: %v", err)
	}
	reg.Close()
}

// TestClaim_UnstartedRegistrationClosesWithoutBlocking pins a landmine rather
// than a defect: a claim taken inside a build's temp dir has no heartbeat running
// until the adopt, so Close must neither wait on a goroutine that was never
// started nor remove the post-adopt path, which does not exist. Both would be
// silent — a deadlocked Close hangs the process, and removing the wrong path
// leaves the claim behind in a temp dir.
func TestClaim_UnstartedRegistrationClosesWithoutBlocking(t *testing.T) {
	arena := t.TempDir()
	tmpDir := filepath.Join(arena, ".building-abc-123")
	finalDir := filepath.Join(arena, "abc")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeReadySentinel(tmpDir, "abc"); err != nil {
		t.Fatal(err)
	}

	reg, err := claimReader(tmpDir, finalDir)
	if err != nil {
		t.Fatalf("claiming inside a build temp dir: %v", err)
	}
	if _, err := os.Stat(reg.tmpPath); err != nil {
		t.Fatalf("the claim was not written into the temp dir: %v", err)
	}

	closed := make(chan struct{})
	go func() {
		reg.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close BLOCKED on a registration whose heartbeat was never started; it waited for a " +
			"goroutine that does not exist, which hangs every caller that abandons an unadopted claim")
	}
	if _, err := os.Stat(reg.tmpPath); !os.IsNotExist(err) {
		t.Fatalf("Close left the unadopted claim behind at %s; it removed the post-adopt path instead",
			reg.tmpPath)
	}
	reg.Close() // idempotent
}

// TestClaim_LostClaimRaisesAnAudibleAlarm pins the LAST line of defence: a
// process whose registry entry disappears while it is serving. The claim and
// reaper guards are meant to make that unreachable, but if it happens anyway the
// process is serving a generation nothing records it as holding, and the only
// thing standing between that and a silent reap-under-a-live-reader is this
// alarm.
//
// It must be at slog.Error specifically. cmd/mcp-1c/main.go pins the default
// handler to LevelError, so a Warn here goes exactly where the original defect's
// "could not register reader" warning went: nowhere. Asserting the LEVEL, not
// just that something was logged, is the whole point.
//
// It costs one heartbeat interval of wall time, which is why it is skipped in
// -short. There is no seam to shrink the interval through, and adding one to
// production code for a diagnostic would be a worse trade than the wait.
func TestClaim_LostClaimRaisesAnAudibleAlarm(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a heartbeat interval")
	}
	arena := t.TempDir()
	genDir := filepath.Join(arena, "g", "feedfacefeedface")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeReadySentinel(genDir, "feedfacefeedface"); err != nil {
		t.Fatal(err)
	}

	rec := captureLogs(t)
	reg, err := registerReader(genDir)
	if err != nil {
		t.Fatalf("registerReader: %v", err)
	}
	t.Cleanup(reg.Close)

	// POSITIVE CONTROL: while the entry is there, a heartbeat must NOT complain.
	// Without this, a heartbeat that shouted on every tick would pass the
	// assertion below without carrying the property.
	time.Sleep(readerHeartbeatInterval + 2*time.Second)
	if msgs := rec.atLevel(slog.LevelError); len(msgs) != 0 {
		t.Fatalf("the heartbeat raised the lost-claim alarm while the claim was intact: %v", msgs)
	}

	// A reaper took the entry out from under this process.
	if err := os.Remove(reg.path); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(readerHeartbeatInterval + 5*time.Second)
	for time.Now().Before(deadline) {
		if len(rec.atLevel(slog.LevelError)) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the heartbeat found its registry entry gone and said nothing at Error; this process is "+
		"serving a generation nothing records it as holding, and the shipped binary logs nothing below "+
		"Error (levels seen: Warn=%v Info=%v)",
		rec.atLevel(slog.LevelWarn), rec.atLevel(slog.LevelInfo))
}

// TestGC_CrossUserGenerationStaysAtDebug pins the log SEVERITY of the one GC
// failure that is ordinary rather than alarming: a generation on a shared arena
// that this user has no permission to remove. It is the only failure a healthy
// multi-user deployment produces on every single pass, and reporting it at Error
// would bury the genuine "a generation's holders cannot be established" alarm
// under it.
//
// This is the test for a re-derivation, not for the ported defect. The
// classification used to be os.IsPermission, which works only on a bare
// *PathError; the removal path now returns errors wrapped with fmt.Errorf("%w"),
// through which os.IsPermission answers false. Measured, not assumed:
// os.IsPermission predates error wrapping and unwraps only *PathError,
// *LinkError and *SyscallError.
func TestGC_CrossUserGenerationStaysAtDebug(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a directory unwritable")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 2)
	gensig := reapBuildGen(t, dumpDir, cacheDir)

	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	// An arena this process may read but not write is what a generation owned by
	// another unix user looks like from here: it cannot be renamed out or removed.
	gensDir := generationsDir(cpath)
	if err := os.Chmod(gensDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gensDir, 0o755) })

	rec := captureLogs(t)
	removed, gcErr := GCGenerations(dumpDir, cacheDir, reapForeignGensig)
	if len(removed) != 0 {
		t.Fatalf("a generation this process cannot touch was reported as removed: %v", removed)
	}
	if gcErr != nil {
		t.Fatalf("GCGenerations: %v", gcErr)
	}
	if !GenerationReady(dumpDir, cacheDir, gensig) {
		t.Fatalf("fixture: generation %s must still be READY", gensig)
	}

	// POSITIVE CONTROL: the pass really did try and fail on this generation. If it
	// logged nothing at all, the severity assertion below would pass vacuously.
	debugs := rec.atLevel(slog.LevelDebug)
	errorsAt := rec.atLevel(slog.LevelError)
	if len(debugs)+len(errorsAt) == 0 {
		t.Fatal("POSITIVE CONTROL DID NOT FIRE: the GC pass logged nothing, so it never reached the " +
			"generation it cannot remove and this test proves nothing")
	}
	if len(errorsAt) != 0 {
		t.Fatalf("a cross-user generation on a shared arena was reported at Error (%v); it is the ordinary "+
			"steady state of a shared cache and belongs at Debug, or it buries the real alarm", errorsAt)
	}
}

// TestReap_OrphanWithUnusableRegistryIsNotRemoved pins the ReapStaleBuildDirs
// caller of generationHasLiveReader. It reaps READY-less orphans, so it is the
// one reaper that acts on directories that are ALREADY damaged — which is
// exactly where "the registry cannot be read" is most likely, and where reading
// that as "unheld" removes a tree a co-located process may still hold open.
//
// THE REGISTRY IS BROKEN BY REPLACING IT WITH A FILE, NOT BY chmod. An
// unreadable DIRECTORY also breaks buildDirStale's tree walk, and a failed walk
// makes it answer "not stale", so the orphan survives for a reason that has
// nothing to do with the registry — this test passed against a reverted guard
// until that was measured. A plain file makes os.ReadDir fail with ENOTDIR while
// the walk succeeds, so the registry check is the only thing standing between
// this orphan and the reaper.
func TestReap_OrphanWithUnusableRegistryIsNotRemoved(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 2)
	gensig := reapBuildGen(t, dumpDir, cacheDir)

	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	gensDir := generationsDir(cpath)
	victim := generationDir(cpath, gensig)

	// A second orphan with a perfectly readable registry is the positive control:
	// the same pass MUST remove it.
	control := filepath.Join(gensDir, "0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(control, "shard_000"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Turn the victim into an orphan (READY stripped) whose registry cannot be
	// listed, then age both trees past the staleness cutoff.
	if err := os.Remove(readySentinelPath(victim)); err != nil {
		t.Fatal(err)
	}
	victimRegistry := filepath.Join(victim, readersDirName)
	if err := os.WriteFile(victimRegistry, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * buildDirStaleAfter)
	ageDirTree(t, victim, stale)
	ageDirTree(t, control, stale)

	// The fixture must actually be unreadable-as-a-registry, and NOT in the
	// "nobody ever registered" way, which is a legitimate reason to reap.
	if _, err := generationHasLiveReader(victim); err == nil {
		t.Fatal("fixture: the victim's registry is still answerable, so this test cannot show a reaper " +
			"refusing to act on one it cannot read")
	}
	if !buildDirStale(victim, time.Now().Add(-buildDirStaleAfter)) {
		t.Fatal("fixture: the victim's tree does not read as stale, so the staleness gate would keep it " +
			"regardless of the registry and this test would prove nothing")
	}

	removed, err := ReapStaleBuildDirs(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("ReapStaleBuildDirs: %v", err)
	}
	if !slices.Contains(removed, "0123456789abcdef") {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper removed %v, which does not include the stale "+
			"orphan with a readable registry", removed)
	}
	if slices.Contains(removed, gensig) {
		t.Fatalf("the reaper removed orphan %s after failing to read its registry; whether anything still "+
			"holds it is UNKNOWN, and unknown is not \"unheld\" (removed=%v)", gensig, removed)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("orphan %s was removed despite an unreadable registry: %v", gensig, err)
	}
}

// TestReload_RefusesABuildThatCameBackWithoutAClaim pins the reload's own
// belt-and-braces check. Production cannot reach it — buildGeneration returns a
// registration or an error — but the build step is injectable, and a future
// change to it that returns success without a claim must not be able to put this
// process back into the state the whole fix exists to prevent: serving a
// generation nothing records it as holding.
func TestReload_RefusesABuildThatCameBackWithoutAClaim(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 3)

	gensig := reapBuildGen(t, dumpDir, cacheDir)
	idx, err := reapOpenAndWait(t, dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("opening the first generation: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	before := idx.ModuleCount()

	reapAddModule(t, dumpDir, 1)
	orig := reloadBuildGeneration
	var called bool
	reloadBuildGeneration = func(dir, cache, sig string) (*readerRegistration, error) {
		called = true
		// Really build it, then throw the claim away: the generation is genuinely
		// READY in the arena, and genuinely unclaimed by this process.
		reg, buildErr := orig(dir, cache, sig)
		reg.Close()
		return nil, buildErr
	}
	t.Cleanup(func() { reloadBuildGeneration = orig })

	rep, reloadErr := idx.Reload()
	if !called {
		t.Fatal("the injected build never ran, so this test proves nothing")
	}
	if reloadErr == nil {
		t.Fatalf("reload swapped in a generation (changed=%v modules=%d) that the build returned no reader "+
			"claim for; the generation is in the arena, reapable, and nothing records that it is served",
			rep.Changed, rep.ModulesAfter)
	}
	if serr := reapSearchWorks(t, idx); serr != nil {
		t.Fatalf("after a refused reload the previous index stopped serving: %v", serr)
	}
	if got := idx.ModuleCount(); got != before {
		t.Fatalf("after a refused reload the index has %d modules; want the previous generation's %d",
			got, before)
	}
}

// TestForceDrop_ColdCacheIsNotAnError pins the one case the claim-by-rename
// removal made newly reachable: --reindex against a cache with nothing in it
// yet. removeUnheldGeneration cannot answer for a directory that does not exist
// (its write probe has nowhere to go), so a cold force-rebuild has to be
// recognised as "nothing to drop" before the removal is attempted, or every cold
// --reindex logs a spurious refusal.
func TestForceDrop_ColdCacheIsNotAnError(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 2)

	gensig, err := GenSig(dumpDir)
	if err != nil {
		t.Fatal(err)
	}
	rec := captureLogs(t)
	if err := ForceRebuildGeneration(dumpDir, cacheDir, gensig); err != nil {
		t.Fatalf("a cold force-rebuild must succeed: %v", err)
	}
	if !GenerationReady(dumpDir, cacheDir, gensig) {
		t.Fatalf("a cold force-rebuild did not leave generation %s READY", gensig)
	}
	for _, msg := range rec.atLevel(slog.LevelError) {
		if strings.Contains(msg, "refusing to drop") {
			t.Fatalf("a cold --reindex reported a refusal to drop a generation that was never built: %q", msg)
		}
	}
}
