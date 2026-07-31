package main

// Reap-of-the-generation-this-process-just-built regression tests, driven
// through the REAL production serve entry point openServeIndexLocal.
//
// THE DEFECT. A cold serve open builds its generation and only THEN claims it.
// The build adopts by renaming the finished temp directory into the shared arena
// (g/<gensig>/), so from that instant the generation is READY, recorded as held
// by nobody, and therefore a legal target for every reaper in that arena — and it
// stays that way until this process's claim lands, several calls later, inside
// FinishServeOpen. A co-located process whose idea of "the current generation"
// differs from ours (it has not noticed the new dump yet, or it serves a
// different base out of the same cache) runs exactly that reaper. It deletes the
// generation this process built seconds ago, and the serve open then fails: the
// server answers a build error for every search until it is restarted.
//
// This is the SAME defect the reload path had (dump/generation_reap_test.go,
// TestReap_ReloadNeverPublishesAGenerationThatIsNotOnDisk) and it is closed the
// same way — the claim is written INSIDE the build's private temp directory and
// adopted together with it, so the generation enters the arena already held and
// there is no instant at which a reaper could take it.
//
// THE PROPERTY these tests pin: a cold serve open racing a reaper that fires the
// moment the generation appears must SERVE, every time. Refusing is safe but it
// is not the property — refusing SOMETIMES is the signature of a window that was
// narrowed rather than closed, so every round must get through.
//
// Both tests carry a POSITIVE CONTROL that the reaper actually fired INSIDE the
// window, i.e. that it saw the new generation READY in the arena and ran a real
// GC pass against it. Without that control a reaper that never got there would
// let both tests pass with nothing to suppress.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
)

// serveReapForeignGensig is a keepGensig no generation can ever have, so every
// generation in the arena is a GC candidate and only a reader claim can protect
// one. It models a co-located process that has not noticed the new dump.
const serveReapForeignGensig = "0000000000000000000000000000000000000000000000000000000000000000"

// serveReapSearchTerm appears in every module these tests write, so "the index
// still answers" is one query away.
const serveReapSearchTerm = "ТестоваяПроцедураПоиска"

// serveReapWriteModule writes one distinct BSL module under dumpDir. Distinct
// content means a distinct dump signature, which is what makes the next serve
// open build a NEW generation instead of reusing one.
func serveReapWriteModule(t *testing.T, dumpDir string, n int) {
	t.Helper()
	rel := filepath.Join("CommonModules", fmt.Sprintf("Модуль%04d", n), "Ext", "Module.bsl")
	full := filepath.Join(dumpDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("creating the module dir: %v", err)
	}
	body := fmt.Sprintf("Процедура %s%04d() Экспорт\n\tСообщить(\"привет %04d\");\nКонецПроцедуры\n",
		serveReapSearchTerm, n, n)
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the module: %v", err)
	}
}

// serveReapOpenAndWait runs the real serve open and waits for its background
// prepare+attach to finish. It returns the index and the build error the open
// recorded, if any.
func serveReapOpenAndWait(t *testing.T, dumpDir, cacheDir string) (*dump.Index, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	idx, err := openServeIndexLocal(ctx, dumpDir, cacheDir, false)
	if err != nil {
		return nil, fmt.Errorf("openServeIndexLocal returned an error: %w", err)
	}
	select {
	case <-idx.Done():
	case <-time.After(180 * time.Second):
		_ = idx.Close()
		t.Fatalf("the serve open of %s did not finish within 180s", dumpDir)
	}
	if !idx.Ready() {
		buildErr := idx.BuildError()
		_ = idx.Close()
		return nil, fmt.Errorf("the serve open did not become ready: %w", buildErr)
	}
	return idx, nil
}

// serveReapSearchWorks reports whether the index still answers a query every
// generation in these tests can satisfy. An index whose shards were reaped away
// answers with an empty alias or with zero hits.
func serveReapSearchWorks(idx *dump.Index) error {
	_, total, err := idx.Search(dump.SearchParams{Query: serveReapSearchTerm + "0000", Limit: 5})
	if err != nil {
		return err
	}
	if total == 0 {
		return fmt.Errorf("the index answers nothing")
	}
	return nil
}

// TestServeOpen_ReaperAtTheWindowMustNotTakeTheFreshGeneration is the
// single-process reproduction: a reaper goroutine watches for the generation the
// serve open is building and reaps the instant it becomes READY in the arena.
//
// It is the WEAKER of the two reproductions and is here for determinism, not for
// evidence: both halves share one address space, one page cache and one set of
// descriptors, so it cannot show the consequence a second OS process suffers
// (answering out of unlinked inodes). The cross-process test below is the
// evidence; this one is what fails on every run.
func TestServeOpen_ReaperAtTheWindowMustNotTakeTheFreshGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("hammers a reaper against a cold serve build for many rounds")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	for i := range 40 {
		serveReapWriteModule(t, dumpDir, i)
	}

	// The reaper must be shown to have FIRED INSIDE THE WINDOW. When the fix works
	// it correctly has nothing to take, so "the reaper removed something" is not
	// the control — "the reaper saw this generation READY in the arena and ran a
	// real GC pass against it" is.
	var gcPassesAtTheWindow atomic.Int64

	const rounds = 12
	served := 0
	for round := range rounds {
		serveReapWriteModule(t, dumpDir, 1000+round)

		target, err := dump.GenSig(dumpDir)
		if err != nil {
			t.Fatalf("round %d: GenSig: %v", round, err)
		}
		if dump.GenerationReady(dumpDir, cacheDir, target) {
			t.Fatalf("round %d: fixture: generation %s already exists, so this round would not build one",
				round, target)
		}

		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if dump.GenerationReady(dumpDir, cacheDir, target) {
					gcPassesAtTheWindow.Add(1)
					_, _ = dump.GCGenerations(dumpDir, cacheDir, serveReapForeignGensig)
				}
			}
		}()

		idx, openErr := serveReapOpenAndWait(t, dumpDir, cacheDir)
		close(stop)
		<-done

		if openErr != nil {
			t.Logf("round %d: the serve open refused: %v", round, openErr)
			continue
		}
		if !dump.GenerationReady(dumpDir, cacheDir, target) {
			_ = idx.Close()
			t.Fatalf("round %d: REAP OF A LIVE GENERATION: the serve open reported success for %s but its "+
				"READY marker is gone from disk", round, target)
		}
		if serr := serveReapSearchWorks(idx); serr != nil {
			_ = idx.Close()
			t.Fatalf("round %d: REAP OF A LIVE GENERATION: the serve open reported success for %s but the "+
				"index no longer answers: %v", round, target, serr)
		}
		served++
		_ = idx.Close()
	}

	if passes := gcPassesAtTheWindow.Load(); passes == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the competing reaper never saw a new generation READY in "+
			"the arena, so it never ran a GC pass against one and the %d successful opens prove nothing "+
			"about the race", served)
	} else {
		t.Logf("the competing reaper ran %d GC passes against the newly published generation", passes)
	}
	if served != rounds {
		t.Fatalf("only %d of %d cold serve opens got through under a concurrent reaper; the generation a "+
			"serve open builds must be published ALREADY claimed, so a reaper never has a window to take it in",
			served, rounds)
	}
}

// TestServeReapHelperProcess is the CHILD half of
// TestServeOpen_ForeignProcessReaperMustNotTakeTheFreshGeneration. It is a no-op
// unless MCP_1C_SERVEREAP_HELPER=1, so an ordinary `go test` run skips it.
//
// It is a REAPER, not a server: a second OS process that shares the cache arena
// and whose idea of the current generation (serveReapForeignGensig) matches
// nothing in it. It waits for the gensig named in the target file to become
// READY and runs a real GC pass the instant it does — the window the parent's
// serve open used to publish its generation into unclaimed.
func TestServeReapHelperProcess(t *testing.T) {
	if os.Getenv("MCP_1C_SERVEREAP_HELPER") != "1" {
		return
	}
	dumpDir := os.Getenv("MCP_1C_SERVEREAP_DUMP")
	cacheDir := os.Getenv("MCP_1C_SERVEREAP_CACHE")
	targetFile := os.Getenv("MCP_1C_SERVEREAP_TARGET")
	stopFile := os.Getenv("MCP_1C_SERVEREAP_STOP")

	var passes, removals int
	var removed []string
	for {
		if _, err := os.Stat(stopFile); err == nil {
			break
		}
		b, err := os.ReadFile(targetFile)
		if err != nil {
			continue
		}
		target := strings.TrimSpace(string(b))
		if target == "" {
			continue
		}
		if !dump.GenerationReady(dumpDir, cacheDir, target) {
			continue
		}
		passes++
		gone, gcErr := dump.GCGenerations(dumpDir, cacheDir, serveReapForeignGensig)
		if gcErr == nil && len(gone) > 0 {
			removals += len(gone)
			removed = append(removed, gone...)
		}
	}
	// The parent parses these two lines. They are the child's whole report.
	fmt.Printf("HELPER_PASSES %d\n", passes)
	fmt.Printf("HELPER_REMOVED %d %s\n", removals, strings.Join(removed, ","))
}

// TestServeOpen_ForeignProcessReaperMustNotTakeTheFreshGeneration is the same
// defect across TWO REAL OS PROCESSES, which is the only shape that shows what a
// co-located reaper actually does to a co-located server: separate address
// spaces, separate page caches, separate descriptor tables, one shared cache
// arena on disk.
//
// The child is the reaper and the parent runs the real serve open, so one process
// spawn buys many contested rounds. Which side is which does not change the
// defect: it is a generation one process published unclaimed and a DIFFERENT
// process removed.
func TestServeOpen_ForeignProcessReaperMustNotTakeTheFreshGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a second OS process")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	signalDir := t.TempDir()
	for i := range 40 {
		serveReapWriteModule(t, dumpDir, i)
	}

	targetFile := filepath.Join(signalDir, "target")
	stopFile := filepath.Join(signalDir, "stop")
	if err := os.WriteFile(targetFile, nil, 0o644); err != nil {
		t.Fatalf("creating the target file: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestServeReapHelperProcess$", "-test.timeout=600s")
	cmd.Env = append(os.Environ(),
		"MCP_1C_SERVEREAP_HELPER=1",
		"MCP_1C_SERVEREAP_DUMP="+dumpDir,
		"MCP_1C_SERVEREAP_CACHE="+cacheDir,
		"MCP_1C_SERVEREAP_TARGET="+targetFile,
		"MCP_1C_SERVEREAP_STOP="+stopFile,
	)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the reaper child: %v", err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()
	stopChild := func() {
		if err := os.WriteFile(stopFile, []byte("stop\n"), 0o644); err != nil {
			t.Logf("writing the stop file: %v", err)
		}
	}
	t.Cleanup(func() {
		stopChild()
		select {
		case <-childDone:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	// setTarget publishes the round's gensig to the child atomically, so the child
	// never reads a half-written name.
	setTarget := func(sig string) {
		tmp := targetFile + ".tmp"
		if err := os.WriteFile(tmp, []byte(sig+"\n"), 0o644); err != nil {
			t.Fatalf("writing the target file: %v", err)
		}
		if err := os.Rename(tmp, targetFile); err != nil {
			t.Fatalf("publishing the target file: %v", err)
		}
	}

	const rounds = 8
	served, refused := 0, 0
	var refusals []string
	for round := range rounds {
		serveReapWriteModule(t, dumpDir, 2000+round)

		target, err := dump.GenSig(dumpDir)
		if err != nil {
			t.Fatalf("round %d: GenSig: %v", round, err)
		}
		if dump.GenerationReady(dumpDir, cacheDir, target) {
			t.Fatalf("round %d: fixture: generation %s already exists, so this round would not build one",
				round, target)
		}
		setTarget(target)

		idx, openErr := serveReapOpenAndWait(t, dumpDir, cacheDir)
		setTarget("")

		if openErr != nil {
			refused++
			refusals = append(refusals, fmt.Sprintf("round %d: %v", round, openErr))
			t.Logf("round %d: the serve open refused: %v", round, openErr)
			continue
		}
		if !dump.GenerationReady(dumpDir, cacheDir, target) {
			_ = idx.Close()
			t.Fatalf("round %d: REAP OF A LIVE GENERATION ACROSS PROCESSES: the serve open reported success "+
				"for %s but its READY marker is gone from disk", round, target)
		}
		if serr := serveReapSearchWorks(idx); serr != nil {
			_ = idx.Close()
			t.Fatalf("round %d: REAP OF A LIVE GENERATION ACROSS PROCESSES: the serve open reported success "+
				"for %s but the index no longer answers: %v", round, target, serr)
		}
		served++
		_ = idx.Close()
	}

	stopChild()
	select {
	case err := <-childDone:
		if err != nil {
			t.Fatalf("the reaper child failed: %v\n--- child output ---\n%s", err, out.String())
		}
	case <-time.After(120 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("the reaper child did not exit\n--- child output ---\n%s", out.String())
	}

	childOut := out.String()
	passes := helperCount(t, childOut, "HELPER_PASSES ")
	if passes == 0 {
		t.Fatalf("POSITIVE CONTROL DID NOT FIRE: the reaper child never saw a target generation READY in the "+
			"arena, so it never ran a GC pass against one and the %d successful opens prove nothing about "+
			"the race\n--- child output ---\n%s", served, childOut)
	}
	t.Logf("the reaper child ran %d GC passes against the newly published generation, removing %d",
		passes, helperCount(t, childOut, "HELPER_REMOVED "))

	if served != rounds {
		t.Fatalf("only %d of %d cold serve opens got through while ANOTHER OS PROCESS reaped the arena "+
			"(%d refused: %s); the generation a serve open builds must be published ALREADY claimed, so a "+
			"reaper in another process never has a window to take it in\n--- child output ---\n%s",
			served, rounds, refused, strings.Join(refusals, " | "), childOut)
	}
}

// helperCount extracts the integer that follows prefix in the child's output.
// A missing line is a test failure and not a zero, because "the child reported
// nothing" and "the child reported none" are different facts and only the second
// one may be reasoned from.
func helperCount(t *testing.T, out, prefix string) int {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
		if !ok {
			continue
		}
		field, _, _ := strings.Cut(rest, " ")
		n, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("the child's %q line is not a number: %q", prefix, line)
		}
		return n
	}
	t.Fatalf("the child never printed a %q line, so its reaper cannot be shown to have run at all"+
		"\n--- child output ---\n%s", prefix, out)
	return 0
}
