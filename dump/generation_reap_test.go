package dump

// Reap-of-a-live-generation regression tests.
//
// THE DEFECT. Two processes sharing one cache arena could delete the index
// generation the other was actively serving. The reader registry
// (g/<gensig>/readers/) is the ONLY thing that tells a reaper "somebody is
// serving this, do not remove it", and it failed three independent ways, each of
// them silently:
//
//  1. A process that could NOT register (readers/ unwritable) logged a
//     slog.Warn and served anyway. cmd/mcp-1c/main.go pins the default handler
//     to slog.LevelError, so that warning reached nobody, and the generation
//     then looked unheld to every reaper in the arena.
//  2. A reaper could not tell "no reader ever registered" (safe to reap) from
//     "the registry is unusable, so a peer that wanted to register could not"
//     (NOT safe to reap). generationHasLiveReader returned a bare bool, so both
//     read as "unheld".
//  3. Reload registered its reader only AFTER the new generation was already
//     READY in the arena, i.e. already reapable. A reaper firing inside that
//     window removed the generation, and registerReader's os.MkdirAll then
//     RESURRECTED the directory as an empty shell, so the reload attached ZERO
//     shards (openCachedShards does not treat an empty shard list as an error)
//     and still reported success.
//
// THE PROPERTY these tests pin is the one that must never regress: a process
// that cannot establish that its generation is protected must refuse LOUDLY,
// never serve quietly. Each test asserts the refusal directly AND carries the
// invariant behind it — whenever a process reaches a serving state, a concurrent
// reaper must not be able to remove what it serves — so a future fix that
// protects the generation some other way still passes the second half.
//
// Every "must not be removed" assertion is paired with a POSITIVE CONTROL that
// shows the reaper removing a different generation in the SAME call. Without it
// a reaper that never fires at all would pass every one of them.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// reapForeignGensig is a keepGensig no generation can ever have: it models a
// co-located process whose idea of "the current generation" differs from ours
// (it has not noticed the new dump yet, or it serves a different base out of the
// same arena). Every generation in the arena is therefore a GC candidate, and
// only the reader registry can protect one.
const reapForeignGensig = "0000000000000000000000000000000000000000000000000000000000000000"

// reapSearchTerm is present in every module these tests write, so "the index
// still answers" is one query away.
const reapSearchTerm = "ТестоваяПроцедураПоиска"

// reapWriteModules writes n distinct BSL modules under dumpDir, all sharing
// reapSearchTerm. The suffix makes each module's content, and therefore the
// dump signature, distinct.
func reapWriteModules(t *testing.T, dumpDir string, n int) {
	t.Helper()
	for i := range n {
		mkBSLFile(t, dumpDir,
			fmt.Sprintf("CommonModules/Модуль%03d/Ext/Module.bsl", i),
			fmt.Sprintf("Процедура %s%03d() Экспорт\n\tСообщить(\"привет %03d\");\nКонецПроцедуры\n",
				reapSearchTerm, i, i))
	}
}

// reapAddModule appends one more distinct module, which changes the dump
// signature and so makes the next build produce a NEW generation.
func reapAddModule(t *testing.T, dumpDir string, n int) {
	t.Helper()
	mkBSLFile(t, dumpDir,
		fmt.Sprintf("CommonModules/Добавленный%03d/Ext/Module.bsl", n),
		fmt.Sprintf("Процедура %s9%03d() Экспорт\n\tСообщить(\"добавлено %03d\");\nКонецПроцедуры\n",
			reapSearchTerm, n, n))
}

// reapBuildGen builds (or reuses) the READY generation for dumpDir's current
// content and returns its gensig.
func reapBuildGen(t *testing.T, dumpDir, cacheDir string) string {
	t.Helper()
	gensig, err := GenSig(dumpDir)
	if err != nil {
		t.Fatalf("GenSig: %v", err)
	}
	if err := BuildGeneration(dumpDir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	if !GenerationReady(dumpDir, cacheDir, gensig) {
		t.Fatalf("fixture: generation %s is not READY after a successful build", gensig)
	}
	return gensig
}

// reapGenDir resolves <cache>/g/<gensig>, the directory the reader registry
// lives under.
func reapGenDir(t *testing.T, dumpDir, cacheDir, gensig string) string {
	t.Helper()
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	return generationDir(cpath, gensig)
}

// reapCrippleRegistry creates the generation's reader registry and takes mode
// bits away from it, restoring them at the end of the test so t.TempDir's own
// cleanup can still remove the tree.
//
// mode 0o555 is readable but not writable: a reaper sees an EMPTY registry (and
// used to read that as "unheld") while no peer can record that it is serving the
// generation. mode 0o333 is the mirror image — a claim can be written but
// nothing can list it back, so the claim is invisible to every reaper.
func reapCrippleRegistry(t *testing.T, dumpDir, cacheDir, gensig string, mode os.FileMode) string {
	t.Helper()
	genDir := reapGenDir(t, dumpDir, cacheDir, gensig)
	readersDir := filepath.Join(genDir, readersDirName)
	if err := os.MkdirAll(readersDir, 0o755); err != nil {
		t.Fatalf("creating the reader registry to cripple: %v", err)
	}
	if err := os.Chmod(readersDir, mode); err != nil {
		t.Fatalf("chmod of the reader registry: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readersDir, 0o755) })
	return genDir
}

// reapGCOnce runs the real old-generation GC with a keepGensig that protects
// nothing in this arena and reports the gensigs it removed. A GC error is not
// fatal: a reaper that refuses to act on a registry it cannot trust is the
// CORRECT outcome, so every caller asserts on what was removed rather than on
// what was returned.
func reapGCOnce(t *testing.T, dumpDir, cacheDir string) []string {
	t.Helper()
	removed, err := GCGenerations(dumpDir, cacheDir, reapForeignGensig)
	if err != nil {
		t.Logf("GC returned an error (acceptable as long as the right things were removed): %v", err)
	}
	return removed
}

// reapHasLiveReader answers generationHasLiveReader for a registry the caller
// expects to be readable. It fails the test on the "cannot tell" answer instead
// of folding it into "unheld" — which is exactly the conflation the returned
// error exists to prevent, so a test must never re-introduce it.
func reapHasLiveReader(t *testing.T, genDir string) bool {
	t.Helper()
	live, err := generationHasLiveReader(genDir)
	if err != nil {
		t.Fatalf("generationHasLiveReader(%s): %v", genDir, err)
	}
	return live
}

// reapOpenAndWait opens a generation read-only and waits for its asynchronous
// name load to finish.
func reapOpenAndWait(t *testing.T, dumpDir, cacheDir, gensig string) (*Index, error) {
	t.Helper()
	idx, err := OpenGenerationReadOnly(dumpDir, cacheDir, gensig)
	if err != nil {
		return nil, err
	}
	select {
	case <-idx.Done():
	case <-time.After(60 * time.Second):
		_ = idx.Close()
		t.Fatalf("the read-only open of %s did not finish within 60s", gensig)
	}
	if !idx.Ready() {
		buildErr := idx.BuildError()
		_ = idx.Close()
		return nil, fmt.Errorf("the open did not become ready: %w", buildErr)
	}
	return idx, nil
}

// reapSearchWorks reports whether the index still answers a query every
// generation in these tests can satisfy. An index whose shards were reaped away
// answers with ErrorAliasEmpty or with zero hits.
func reapSearchWorks(t *testing.T, idx *Index) error {
	t.Helper()
	_, total, err := idx.Search(SearchParams{Query: reapSearchTerm + "000", Limit: 5})
	if err != nil {
		return err
	}
	if total == 0 {
		return fmt.Errorf("the index answers nothing")
	}
	return nil
}

// TestReap_UnwritableRegistryMustNotServe is defect (1), in one process and with
// no timing involved: the reader registry cannot be WRITTEN to, so this process
// cannot record that it holds the generation. It must refuse to serve. If it
// served anyway, a reaper that knows nothing about it would delete the
// generation out from under it — which the second half of this test shows a
// reaper doing, to a generation with a healthy registry, in the same GC pass.
func TestReap_UnwritableRegistryMustNotServe(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a directory unwritable")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 3)

	victimSig := reapBuildGen(t, dumpDir, cacheDir)
	// A second, fully healthy generation. It is the positive control: the same
	// reaper call that must leave the victim alone MUST remove this one.
	reapAddModule(t, dumpDir, 1)
	controlSig := reapBuildGen(t, dumpDir, cacheDir)

	reapCrippleRegistry(t, dumpDir, cacheDir, victimSig, 0o555)

	idx, openErr := OpenGenerationReadOnly(dumpDir, cacheDir, victimSig)
	if openErr == nil {
		t.Cleanup(func() { _ = idx.Close() })
	}

	removed := reapGCOnce(t, dumpDir, cacheDir)
	if !slices.Contains(removed, controlSig) {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper removed %v, which does not include the healthy "+
			"generation %s; a reaper that removes nothing would pass the assertion below for the wrong reason",
			removed, controlSig)
	}

	if openErr == nil {
		if slices.Contains(removed, victimSig) {
			t.Fatalf("REAP OF A LIVE GENERATION: this process serves %s and the reaper removed it (removed=%v)",
				victimSig, removed)
		}
		t.Fatalf("the open served generation %s although this process could not record a reader claim on it; "+
			"it must refuse loudly instead", victimSig)
	}
	if !strings.Contains(openErr.Error(), victimSig) {
		t.Fatalf("the refusal must name the generation it refused; got %v", openErr)
	}
}

// TestReap_UnreadableRegistryMustNotServe is the other half of an unusable
// registry: the claim CAN be written but nothing can list it back (mode 0o333).
// A claim no reaper can see is not a claim, so writing one successfully is not
// enough to start serving on.
func TestReap_UnreadableRegistryMustNotServe(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a directory unreadable")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 3)

	victimSig := reapBuildGen(t, dumpDir, cacheDir)
	reapCrippleRegistry(t, dumpDir, cacheDir, victimSig, 0o333)

	idx, openErr := OpenGenerationReadOnly(dumpDir, cacheDir, victimSig)
	if openErr == nil {
		_ = idx.Close()
		t.Fatalf("the open served generation %s after writing a reader claim into a registry nothing can "+
			"list; every reaper in the arena sees that registry as empty, so the claim protects nothing",
			victimSig)
	}
}

// TestReap_GCMustNotReapAnUntrustworthyRegistry pins the REAPER half of the same
// defect, with no serving process involved at all. An empty registry only means
// "nobody is serving this" if a process that WANTED to register could have. When
// readers/ is unwritable that inference is invalid, and a reaper that draws it
// anyway is what deleted a generation a co-located process was serving.
func TestReap_GCMustNotReapAnUntrustworthyRegistry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a directory unwritable")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 3)

	victimSig := reapBuildGen(t, dumpDir, cacheDir)
	reapAddModule(t, dumpDir, 1)
	controlSig := reapBuildGen(t, dumpDir, cacheDir)

	reapCrippleRegistry(t, dumpDir, cacheDir, victimSig, 0o555)

	removed := reapGCOnce(t, dumpDir, cacheDir)
	if !slices.Contains(removed, controlSig) {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper removed %v, which does not include the healthy "+
			"generation %s", removed, controlSig)
	}
	if slices.Contains(removed, victimSig) {
		t.Fatalf("the reaper removed generation %s after reading an EMPTY registry it could not have been "+
			"written to; an empty registry only means \"unheld\" when a peer could have recorded a claim in it "+
			"(removed=%v)", victimSig, removed)
	}
	if !GenerationReady(dumpDir, cacheDir, victimSig) {
		t.Fatalf("generation %s lost its READY marker although the reaper did not report removing it", victimSig)
	}
}

// TestReapServeHelperProcess is the CHILD half of
// TestReap_ForeignReaperMustNotReapWhatAnotherProcessServes. It is a no-op
// unless MCP_1C_REAPTEST_HELPER=1, so an ordinary `go test` run skips it. It
// opens the shared dump+cache through the REAL serve path and reports on stdout
// whether it reached a serving state.
func TestReapServeHelperProcess(t *testing.T) {
	if os.Getenv("MCP_1C_REAPTEST_HELPER") != "1" {
		return
	}
	dumpDir := os.Getenv("MCP_1C_REAPTEST_DUMP")
	cacheDir := os.Getenv("MCP_1C_REAPTEST_CACHE")
	readyFile := os.Getenv("MCP_1C_REAPTEST_READY")
	goFile := os.Getenv("MCP_1C_REAPTEST_GO")

	gensig, err := GenSig(dumpDir)
	if err != nil {
		fmt.Printf("HELPER_ERR gensig: %v\n", err)
		return
	}
	idx, err := OpenForServe(dumpDir, cacheDir)
	if err != nil {
		fmt.Printf("HELPER_REFUSED open: %v\n", err)
		return
	}
	defer func() { _ = idx.Close() }()

	select {
	case <-idx.Done():
	case <-time.After(90 * time.Second):
		fmt.Println("HELPER_ERR index not ready within 90s")
		return
	}
	if !idx.Ready() {
		fmt.Printf("HELPER_REFUSED build: %v\n", idx.BuildError())
		return
	}
	fmt.Printf("HELPER_SERVING gensig=%s modules=%d\n", gensig, idx.ModuleCount())
	if err := os.WriteFile(readyFile, []byte("serving\n"), 0o644); err != nil {
		fmt.Printf("HELPER_ERR ready file: %v\n", err)
		return
	}
	// Hold the generation open while the PARENT process runs its reaper.
	for range 1200 {
		if _, err := os.Stat(goFile); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stillReady := GenerationReady(dumpDir, cacheDir, gensig)
	hits := 0
	if _, total, serr := idx.Search(SearchParams{Query: reapSearchTerm + "000", Limit: 5}); serr == nil {
		hits = total
	}
	fmt.Printf("HELPER_POSTGC ready=%v hits=%d\n", stillReady, hits)
}

// TestReap_ForeignReaperMustNotReapWhatAnotherProcessServes is the same defect
// across TWO REAL OS PROCESSES, which is how it was first reproduced. One
// process serves a generation it could not register a reader for, and the OTHER
// process's reaper deletes that generation while it is being served — the child
// then keeps answering out of unlinked inodes, which is why it looks healthy
// from inside. An in-process reproduction cannot show any of that: both halves
// would share one address space, one page cache and one set of descriptors.
func TestReap_ForeignReaperMustNotReapWhatAnotherProcessServes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a directory unwritable")
	}
	if testing.Short() {
		t.Skip("spawns a second OS process")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	signalDir := t.TempDir()
	reapWriteModules(t, dumpDir, 3)

	gensig := reapBuildGen(t, dumpDir, cacheDir)
	reapCrippleRegistry(t, dumpDir, cacheDir, gensig, 0o555)

	readyFile := filepath.Join(signalDir, "serving")
	goFile := filepath.Join(signalDir, "gc-done")

	cmd := exec.Command(os.Args[0], "-test.run=^TestReapServeHelperProcess$", "-test.timeout=180s")
	cmd.Env = append(os.Environ(),
		"MCP_1C_REAPTEST_HELPER=1",
		"MCP_1C_REAPTEST_DUMP="+dumpDir,
		"MCP_1C_REAPTEST_CACHE="+cacheDir,
		"MCP_1C_REAPTEST_READY="+readyFile,
		"MCP_1C_REAPTEST_GO="+goFile,
	)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the serve child: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Watch for BOTH outcomes at once: a child that refuses exits BEFORE the ready
	// file appears. The exit is remembered rather than waited on twice, because
	// waiting twice is how this shape of harness reports a phantom hang for a
	// child that has already exited cleanly.
	serving, exited := false, false
	var childErr error
	deadline := time.After(150 * time.Second)
waitLoop:
	for {
		if _, err := os.Stat(readyFile); err == nil {
			serving = true
			break waitLoop
		}
		select {
		case childErr = <-done:
			exited = true
			break waitLoop
		case <-deadline:
			_ = cmd.Process.Kill()
			t.Fatalf("timed out waiting for the serve child\n--- child output ---\n%s", out.String())
		case <-time.After(50 * time.Millisecond):
		}
	}

	// The reaper in THIS process knows nothing about the child.
	removed, gcErr := GCGenerations(dumpDir, cacheDir, reapForeignGensig)
	if err := os.WriteFile(goFile, []byte("go\n"), 0o644); err != nil {
		t.Fatalf("writing the go file: %v", err)
	}
	if !exited {
		select {
		case childErr = <-done:
		case <-time.After(120 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("the serve child did not exit\n--- child output ---\n%s", out.String())
		}
	}
	if childErr != nil {
		t.Fatalf("the serve child failed: %v\n--- child output ---\n%s", childErr, out.String())
	}

	childOut := out.String()
	if serving {
		if gcErr != nil {
			t.Logf("GC returned an error (acceptable as long as nothing was removed): %v", gcErr)
		}
		if slices.Contains(removed, gensig) {
			t.Fatalf("REAP OF A LIVE GENERATION ACROSS PROCESSES: the child serves %s and this process removed "+
				"it (removed=%v)\n--- child output ---\n%s", gensig, removed, childOut)
		}
		if !strings.Contains(childOut, "HELPER_POSTGC ready=true") {
			t.Fatalf("the generation the child serves did not survive this process's reaper"+
				"\n--- child output ---\n%s", childOut)
		}
		t.Fatalf("the child served generation %s without being able to record a reader claim on it; "+
			"it must refuse loudly instead\n--- child output ---\n%s", gensig, childOut)
	}
	if !strings.Contains(childOut, "HELPER_REFUSED") {
		t.Fatalf("the child neither served nor refused\n--- child output ---\n%s", childOut)
	}
}

// TestReap_ReloadRefusesAGenerationItCannotClaim pins the reload half of the
// registration guard deterministically, with no racing reaper: the replacement
// generation is already built and perfectly healthy, but its reader registry
// cannot be written to. Swapping it in would leave this process serving a
// generation nothing records it as holding.
func TestReap_ReloadRefusesAGenerationItCannotClaim(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a directory unwritable")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 5)

	gensig := reapBuildGen(t, dumpDir, cacheDir)
	idx, err := reapOpenAndWait(t, dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("opening the first generation: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	before := idx.ModuleCount()

	// CONTROL — a reload onto a healthy pre-built generation succeeds, so the
	// refusal below is caused by the registry and not by the fixture shape.
	reapAddModule(t, dumpDir, 1)
	controlSig := reapBuildGen(t, dumpDir, cacheDir)
	rep, err := idx.Reload()
	if err != nil {
		t.Fatalf("control: a reload onto a healthy pre-built generation must succeed: %v", err)
	}
	if !rep.Changed || rep.SigAfter != controlSig || rep.ModulesAfter != before+1 {
		t.Fatalf("control: reload reported changed=%v sig=%s modules=%d; want a swap to %s with %d modules",
			rep.Changed, rep.SigAfter, rep.ModulesAfter, controlSig, before+1)
	}

	// THE DEFECT — build the next generation, then take away the ability to claim it.
	reapAddModule(t, dumpDir, 2)
	targetSig := reapBuildGen(t, dumpDir, cacheDir)
	reapCrippleRegistry(t, dumpDir, cacheDir, targetSig, 0o555)

	rep, err = idx.Reload()
	if err == nil {
		t.Fatalf("reload swapped in generation %s (changed=%v modules=%d) without being able to record a "+
			"reader claim on it; it must refuse and keep the previous generation serving",
			targetSig, rep.Changed, rep.ModulesAfter)
	}
	// A refused reload must leave the PREVIOUS generation serving, untouched.
	if serr := reapSearchWorks(t, idx); serr != nil {
		t.Fatalf("after a refused reload the previous index stopped serving: %v", serr)
	}
	if got := idx.ModuleCount(); got != before+1 {
		t.Fatalf("after a refused reload the index has %d modules; want the previous generation's %d",
			got, before+1)
	}
	if !GenerationReady(dumpDir, cacheDir, controlSig) {
		t.Fatalf("a refused reload retired generation %s, which is still being served", controlSig)
	}
}

// TestReap_ReloadRefusesAGenerationWithNoShards pins the consequence that was
// actually observed, deterministically: a generation whose manifest lists
// modules but whose directory holds no shard to open. openCachedShards returns
// no error for an empty shard list, so without an explicit guard the swap
// installs an index that reports success and then answers nothing.
func TestReap_ReloadRefusesAGenerationWithNoShards(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 5)

	gensig := reapBuildGen(t, dumpDir, cacheDir)
	idx, err := reapOpenAndWait(t, dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("opening the first generation: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	before := idx.ModuleCount()

	// Build the replacement, then strip its shards while leaving READY and the
	// manifest in place — the exact residue a reaper leaves mid-removal.
	reapAddModule(t, dumpDir, 1)
	targetSig := reapBuildGen(t, dumpDir, cacheDir)
	targetDir := reapGenDir(t, dumpDir, cacheDir, targetSig)
	shardDirs := cacheShardDirs(targetDir)
	if len(shardDirs) == 0 {
		t.Fatalf("fixture: the target generation %s had no shard_* dirs to strip", targetSig)
	}
	for _, sd := range shardDirs {
		if err := os.RemoveAll(sd); err != nil {
			t.Fatalf("stripping shard %s: %v", sd, err)
		}
	}
	if !GenerationReady(dumpDir, cacheDir, targetSig) {
		t.Fatalf("fixture: the target generation must still look READY after its shards are stripped")
	}

	rep, reloadErr := idx.Reload()
	if reloadErr == nil {
		t.Fatalf("reload published generation %s (changed=%v modules=%d) although it contains no shards to "+
			"open; an index with an empty shard set answers nothing", targetSig, rep.Changed, rep.ModulesAfter)
	}
	if serr := reapSearchWorks(t, idx); serr != nil {
		t.Fatalf("after a refused reload the previous index stopped serving: %v", serr)
	}
	if got := idx.ModuleCount(); got != before {
		t.Fatalf("after a refused reload the index has %d modules; want the previous generation's %d", got, before)
	}
}

// TestReap_ReloadNeverPublishesAGenerationThatIsNotOnDisk is defect (3): a
// reload builds a new generation, and a co-located reaper that still thinks the
// OLD generation is current fires inside the window between "the new generation
// became READY in the arena" and "this process registered a reader on it".
//
// The invariant: a Reload that reports SUCCESS must have published a generation
// that is still on disk and whose shards are attached. A Reload that cannot do
// that must return an error and leave the previous index serving. What must
// never happen is the third outcome — success reported, READY gone from disk,
// and the index answering out of an empty shard set.
//
// The run must also make PROGRESS, and every round must. A guard that answered
// "refused" under a hammering reaper would satisfy the safety half and be
// useless, and refusing SOMETIMES is the signature of a window that was narrowed
// rather than closed: the claim has to be written into the build's private temp
// directory and adopted with it, so the reaper never sees the new generation as
// reapable and never has anything to take.
func TestReap_ReloadNeverPublishesAGenerationThatIsNotOnDisk(t *testing.T) {
	if testing.Short() {
		t.Skip("hammers a reaper against a reload for many rounds")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	reapWriteModules(t, dumpDir, 200)

	gensig := reapBuildGen(t, dumpDir, cacheDir)
	idx, err := reapOpenAndWait(t, dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("opening the first generation: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	// THE POSITIVE CONTROL for this test is not "the reaper removed something".
	// When the fix works the reaper correctly has nothing to take: the new
	// generation is already claimed and every older one has been GC'd by the
	// previous round's reload. What must be shown instead is that the reaper
	// actually FIRED INSIDE THE WINDOW — that it saw the new generation READY in
	// the arena and ran a real GC pass against it before the reload returned.
	// A reaper that never got there would let this test pass with nothing to
	// suppress, which is the failure mode this counter exists to rule out.
	var gcPassesAtTheWindow atomic.Int64

	const rounds = 20
	swapped := 0
	for round := range rounds {
		reapAddModule(t, dumpDir, round)

		sigAfter, err := GenSig(dumpDir)
		if err != nil {
			t.Fatalf("round %d: GenSig: %v", round, err)
		}
		keep := gensig // a co-located process that has not noticed the new dump

		// The competing reaper watches for the new generation to become READY and
		// reaps everything that is not ITS current generation the instant it
		// appears — precisely the window the reader claim used to be published after.
		stop := make(chan struct{})
		reaped := make(chan []string, 64)
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if GenerationReady(dumpDir, cacheDir, sigAfter) {
					gcPassesAtTheWindow.Add(1)
					if removed, gcErr := GCGenerations(dumpDir, cacheDir, keep); gcErr == nil && len(removed) > 0 {
						select {
						case reaped <- removed:
						default:
						}
					}
				}
			}
		}()

		rep, reloadErr := idx.Reload()
		close(stop)

		var allReaped []string
		for drained := false; !drained; {
			select {
			case r := <-reaped:
				allReaped = append(allReaped, r...)
			default:
				drained = true
			}
		}

		if reloadErr != nil {
			// Refused. The PREVIOUS generation must still be serving.
			if serr := reapSearchWorks(t, idx); serr != nil {
				t.Fatalf("round %d: reload refused (%v) but the previous index stopped serving: %v",
					round, reloadErr, serr)
			}
			t.Logf("round %d: reload refused: %v (reaped=%v)", round, reloadErr, allReaped)
			continue
		}
		if !rep.Changed {
			t.Fatalf("round %d: reload reported no change after a module was added", round)
		}
		if !GenerationReady(dumpDir, cacheDir, rep.SigAfter) {
			t.Fatalf("round %d: REAP OF A LIVE GENERATION: reload reported success for %s but its READY marker "+
				"is gone from disk (reaped=%v)", round, rep.SigAfter, allReaped)
		}
		if serr := reapSearchWorks(t, idx); serr != nil {
			t.Fatalf("round %d: REAP OF A LIVE GENERATION: reload reported success for %s but the index no "+
				"longer answers: %v (reaped=%v)", round, rep.SigAfter, serr, allReaped)
		}
		gensig = rep.SigAfter
		swapped++
	}
	if passes := gcPassesAtTheWindow.Load(); passes == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the competing reaper never saw the new generation READY in "+
			"the arena, so it never ran a GC pass against it and the %d successful reloads below prove "+
			"nothing about the race", swapped)
	} else {
		t.Logf("the competing reaper ran %d GC passes against the newly published generation", passes)
	}
	if swapped != rounds {
		t.Fatalf("only %d of %d reloads got through under a concurrent reaper; the new generation must be "+
			"published ALREADY claimed, so a reaper never has a window to take it in", swapped, rounds)
	}
}
