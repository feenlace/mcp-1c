package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"os"
)

// version_probe_exit_test.go is about the CONTRACT the probe publishes, not
// about what it computes: extension_version_check_test.go already covers every
// verdict.
//
// THE CLAIM WAS THAT SILENCE MEANS SOMETHING. checkExtensionVersion reports
// every fault at ERROR and the two healthy outcomes at INFO, and main installs
// the pipe-mode handler at LevelError, so a quiet log was said to mean exactly
// one thing: the probe ran and was satisfied.
//
// IT DID NOT. The probe was started with a bare `go checkExtensionVersion(...)`
// and nothing sequenced that goroutine against the end of the process, so a
// session that ended before the round trip finished produced a log that is byte
// for byte what a healthy run produces. That is the very defect the probe was
// changed to fix, one level up: an empty log meant either "verified" or "never
// ran" and the reader could not tell which.
//
// A SYNCHRONOUS PROBE IS NOT THE FIX. Running it before serving starts would put
// a network round trip, up to its three second deadline, in front of the MCP
// initialize handshake, and keeping that handshake immediate is why the dump
// index builds in the background at all (issue #30). The probe stays
// asynchronous and is instead SEQUENCED: serving cannot end without it reaching
// a verdict, and being cut short by the end of the session is itself a verdict
// with its own wording.
//
// Every listener binds 127.0.0.1 in the 19800-19999 range that
// extension_version_check_test.go declares; loopbackListenerInRange is shared.

// probeVerdictMarkers are the openings of every line checkExtensionVersion can
// emit. Matching on the set rather than on one message keeps the assertion about
// "a verdict was reached" instead of about which verdict.
var probeVerdictMarkers = []string{
	"Extension version verified",
	"Extension version NOT verified",
	"Extension is OLDER than this build requires",
}

func hasVerdict(log string) bool {
	for _, m := range probeVerdictMarkers {
		if strings.Contains(log, m) {
			return true
		}
	}
	return false
}

// slowVersionServer answers /version after delay, and counts the requests that
// actually arrived. The count is the oracle for "the probe ran": a log line can
// go missing for reasons that have nothing to do with the probe, a request on
// the wire cannot.
func slowVersionServer(t *testing.T, delay time.Duration, body string) (url string, hits *atomic.Int64) {
	t.Helper()
	hits = &atomic.Int64{}
	ln := loopbackListenerInRange(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL, hits
}

// TestVersionProbe_VerdictSurvivesASessionThatEndsFirst is the defect.
//
// stdin is at EOF the moment the child starts, which is an ordinary way for an
// MCP session to end, and the answer to /version is deliberately slower than
// that. Before the fix the process was gone before the round trip completed and
// the log carried nothing at all.
func TestVersionProbe_VerdictSurvivesASessionThatEndsFirst(t *testing.T) {
	url, hits := slowVersionServer(t, 400*time.Millisecond, `{"version":"`+expectedExtensionVersion+`"}`)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	start := time.Now()
	run := runChildWithStdin(t, nil, "--base", url, "--cache-dir", cacheDir, "--verbose")
	elapsed := time.Since(start)

	t.Logf("exit=%d elapsed=%s stderr=%d bytes requests=%d",
		run.exit, elapsed.Round(time.Millisecond), len(run.stderr), hits.Load())
	if run.exit != 0 {
		t.Fatalf("premise broken: the child exited %d\nstderr: %s", run.exit, run.stderr)
	}
	assertStdoutIsProtocolOnly(t, run.stdout)

	if hits.Load() == 0 {
		t.Fatal("the probe never reached the server, so this run says nothing about what it " +
			"reports; the fixture is broken, not the code")
	}
	if !hasVerdict(string(run.stderr)) {
		t.Errorf("the session ended before /version answered and the log says NOTHING, so it is "+
			"byte for byte what a verified extension produces; the reader cannot tell "+
			"'checked and fine' from 'never checked'.\nstderr:\n%s", run.stderr)
	}
}

// TestVersionProbe_VerdictControlOnAPromptAnswer is the control for the case
// above. Without it the assertion could be satisfied by a build where the probe
// never speaks at all, and the failure would be blamed on sequencing.
func TestVersionProbe_VerdictControlOnAPromptAnswer(t *testing.T) {
	url, hits := slowVersionServer(t, 0, `{"version":"0.0.1"}`)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	run := runChildWithStdin(t, nil, "--base", url, "--cache-dir", cacheDir, "--verbose")

	t.Logf("exit=%d stderr=%d bytes requests=%d", run.exit, len(run.stderr), hits.Load())
	if run.exit != 0 {
		t.Fatalf("control broken: the child exited %d\nstderr: %s", run.exit, run.stderr)
	}
	if hits.Load() != 1 {
		t.Fatalf("control broken: %d requests reached /version, want exactly 1", hits.Load())
	}
	if !hasVerdict(string(run.stderr)) {
		t.Fatalf("control broken: an instant answer produced no verdict either, so the matcher "+
			"in this file finds nothing anywhere\nstderr:\n%s", run.stderr)
	}
	if !strings.Contains(string(run.stderr), "Extension is OLDER") {
		t.Errorf("0.0.1 is below %s and was not reported as older:\n%s",
			expectedExtensionVersion, run.stderr)
	}
}

// TestVersionProbe_SilenceInPipeModeMeansVerified states the contract in the
// mode it is claimed for. Pipe mode is how every MCP client launches the server,
// the handler there sits at LevelError, and the INFO confirmation is filtered
// out, so the log really is empty on a healthy pairing.
//
// What makes that emptiness mean something is the request count: the probe
// provably ran AND stayed quiet. Asserting only on the empty log would be the
// same non-evidence the defect is made of.
func TestVersionProbe_SilenceInPipeModeMeansVerified(t *testing.T) {
	url, hits := slowVersionServer(t, 250*time.Millisecond, `{"version":"`+expectedExtensionVersion+`"}`)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	run := runChildWithStdin(t, nil, "--base", url, "--cache-dir", cacheDir)

	logged, _ := os.ReadFile(filepath.Join(cacheDir, "stderr.log"))
	t.Logf("exit=%d log=%d bytes requests=%d", run.exit, len(logged), hits.Load())
	if run.exit != 0 {
		t.Fatalf("premise broken: the child exited %d\nstderr: %s", run.exit, run.stderr)
	}
	if hits.Load() != 1 {
		t.Fatalf("the probe made %d requests, want exactly 1; without it the empty log below "+
			"proves nothing", hits.Load())
	}
	if strings.Contains(string(logged), "level=ERROR") {
		t.Errorf("a matching extension was reported as a fault in pipe mode:\n%s", logged)
	}

	// CONTROL: the same session length against an extension BELOW the floor must
	// break the silence. Otherwise the emptiness above is a property of pipe mode
	// and not of the verdict.
	urlOld, hitsOld := slowVersionServer(t, 250*time.Millisecond, `{"version":"0.0.1"}`)
	cacheOld := filepath.Join(t.TempDir(), "cache")
	runOld := runChildWithStdin(t, nil, "--base", urlOld, "--cache-dir", cacheOld)
	loggedOld, _ := os.ReadFile(filepath.Join(cacheOld, "stderr.log"))
	t.Logf("control: exit=%d log=%d bytes requests=%d",
		runOld.exit, len(loggedOld), hitsOld.Load())
	if !strings.Contains(string(loggedOld), "level=ERROR") {
		t.Errorf("an extension below the floor produced no ERROR line in pipe mode, so silence "+
			"there does not discriminate:\n%s", loggedOld)
	}
}

// TestVersionProbe_CutShortSaysSoInsteadOfBlamingThePublication pins the wording
// of the one new outcome.
//
// Reusing the "did not answer" line would swap one false statement for another:
// that line names a wrong publication path, refused credentials, a web server in
// the way or a stopped 1C, and none of those is what happened when the reader's
// own session ended first.
func TestVersionProbe_CutShortSaysSoInsteadOfBlamingThePublication(t *testing.T) {
	url, _ := slowVersionServer(t, 2*time.Second, `{"version":"`+expectedExtensionVersion+`"}`)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	run := runChildWithStdin(t, nil, "--base", url, "--cache-dir", cacheDir, "--verbose")
	got := string(run.stderr)

	if !strings.Contains(got, "did not finish") {
		t.Errorf("a probe cut short by the end of the session does not say so:\n%s", got)
	}
	if strings.Contains(got, "the publication path is wrong") {
		t.Errorf("a probe cut short by the end of the session blames the publication, which is "+
			"not what happened:\n%s", got)
	}

	// CONTROL: the publication advice is still given where it is true, on a base
	// nothing is listening at.
	dead := runChildWithStdin(t, nil,
		"--base", "http://127.0.0.1:19899/hs/mcp-1c", "--cache-dir",
		filepath.Join(t.TempDir(), "cache"), "--verbose")
	if !strings.Contains(string(dead.stderr), "the publication path is wrong") {
		t.Errorf("an unreachable base lost the advice that belongs to it:\n%s", dead.stderr)
	}
}
