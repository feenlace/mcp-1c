package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dump_path_report_test.go covers a --dump path that cannot be a dump at all.
//
// THE POLICY, and it is a deliberate choice rather than an oversight left in
// place. A bad --dump stays NON-FATAL and the server still starts, because:
//
//   - the tools that need the dump already tell the truth. search_code answers
//     isError=true naming the unreadable path, so the model is never told
//     "nothing found" for code that exists, which is the failure that would
//     actually corrupt an answer;
//   - nothing else depends on it. server.New registers search_code and
//     reload_dump only when the index is present; every other tool talks to 1C;
//   - openServeIndexLocal is asynchronous on purpose, so the MCP initialize
//     handshake returns immediately whatever the dump's size (issue #30);
//   - and an unreadable path is also what a not-yet-mounted network share looks
//     like, so exiting would convert a degraded server into a restart loop.
//
// What was wrong is not the degradation, it is that the operator could be left
// with a silent success. The ONLY report came from the background goroutine, and
// nothing sequences that goroutine before the process ends: measured over
// repeated runs the line is sometimes missing entirely, and on the serve-failure
// exit os.Exit skips the deferred Close that would otherwise wait for it.
//
// So the path is checked SYNCHRONOUSLY at startup, before serving begins, and
// reported through slog, which already routes itself per launch mode: to fd 2 on
// a terminal, to stderr.log under a pipe, to server.log under --debug. The
// process still starts, and exit stays 0.

// TestDumpPathFault is the predicate on its own, so a wrong verdict here can be
// told apart from a wrong report in the case below.
func TestDumpPathFault(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("preparing the file: %v", err)
	}

	if err := dumpPathFault(filepath.Join(dir, "no-such-dir")); err == nil {
		t.Error("a path that does not exist was accepted as a dump directory")
	}
	if err := dumpPathFault(file); err == nil {
		t.Error("a regular file was accepted as a dump directory")
	}
	// The control: a real directory must produce NO fault, otherwise the two
	// assertions above would pass for a predicate that rejects everything.
	if err := dumpPathFault(dir); err != nil {
		t.Errorf("a real directory was rejected: %v", err)
	}
}

// dumpReportMarker is the stable part of the startup line. It is deliberately
// not the background goroutine's wording: the point of the assertion is that the
// SYNCHRONOUS report is there, and matching the async line would let a run pass
// on the very message whose unreliability caused this work.
const dumpReportMarker = "--dump"

// TestBadDumpIsReportedOnEveryStart is the defect.
//
// The measured behaviour was exit 0 with nothing on either descriptor, and a
// single line from a background goroutine that repeated runs showed going
// missing. A report an operator gets only most of the time is not a report.
func TestBadDumpIsReportedOnEveryStart(t *testing.T) {
	const runs = 8
	missing := 0

	for i := 0; i < runs; i++ {
		cacheDir := filepath.Join(t.TempDir(), "cache")
		bad := filepath.Join(t.TempDir(), "no-such-dump")

		run := runChildWithStdin(t, nil,
			"--base", "http://127.0.0.1:19899/hs/mcp-1c",
			"--dump", bad, "--cache-dir", cacheDir)

		if run.exit != 0 {
			t.Fatalf("premise broken: a bad --dump exited %d; the policy under test is that it "+
				"stays non-fatal.\nstderr: %s", run.exit, run.stderr)
		}
		// fd 1 carries JSON-RPC and must stay free of prose whatever is reported.
		assertStdoutIsProtocolOnly(t, run.stdout)

		logged, _ := os.ReadFile(filepath.Join(cacheDir, "stderr.log"))
		if !strings.Contains(string(logged), dumpReportMarker) {
			missing++
		}
	}

	if missing > 0 {
		t.Errorf("the unusable --dump went unreported in %d of %d starts; an operator who reads "+
			"the log cannot rely on seeing it, and exit is 0 either way", missing, runs)
	}
}

// TestGoodDumpIsNotReported is the control that keeps the case above honest. If
// a usable dump produced the same line, the assertion would be satisfied by a
// report that fires unconditionally and would prove nothing about a bad path.
func TestGoodDumpIsNotReported(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	good := t.TempDir()

	run := runChildWithStdin(t, nil,
		"--base", "http://127.0.0.1:19899/hs/mcp-1c",
		"--dump", good, "--cache-dir", cacheDir)

	if run.exit != 0 {
		t.Fatalf("control broken: a usable --dump exited %d.\nstderr: %s", run.exit, run.stderr)
	}
	logged, _ := os.ReadFile(filepath.Join(cacheDir, "stderr.log"))
	if strings.Contains(string(logged), dumpReportMarker) {
		t.Errorf("a usable --dump produced the unusable-path report, so the report does not "+
			"discriminate:\n%s", logged)
	}
}

// TestBadDumpReachesTheTerminalToo pins the other launch mode. On a terminal the
// person who typed the flag is watching fd 2, and that is where slog writes when
// the redirect does not run.
func TestBadDumpReachesTheTerminalToo(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	bad := filepath.Join(t.TempDir(), "no-such-dump")

	run := runChildWithStdin(t, nil,
		"--base", "http://127.0.0.1:19899/hs/mcp-1c",
		"--dump", bad, "--cache-dir", cacheDir, "--verbose")

	t.Logf("EXIT=%d  stdout=%d bytes  stderr=%d bytes", run.exit, len(run.stdout), len(run.stderr))
	if run.exit != 0 {
		t.Fatalf("premise broken: --verbose changed the exit code to %d", run.exit)
	}
	if !strings.Contains(string(run.stderr), dumpReportMarker) {
		t.Errorf("on a terminal launch the unusable --dump was not reported on fd 2, where the "+
			"person who typed it is looking:\n%s", run.stderr)
	}
	assertStdoutIsProtocolOnly(t, run.stdout)
}
