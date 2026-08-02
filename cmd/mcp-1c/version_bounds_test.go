package main

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// version_bounds_test.go bounds what the far side can spend on this side through
// the ONE value the /version probe accepts from it.
//
// The body arrives under the client's response cap, which defaults to 128 MiB
// (config.DefaultMaxResponseSizeMiB), and everything below happens on the probe
// goroutine, where an out-of-memory kills the whole process rather than failing
// one call.
//
// TWO SPENDS, measured separately because they have different remedies.
//
// The parser. versionComponents strings.Splits the value into components and
// collects an int per component. Measured on this repository before the bound:
// 1 MiB of "1." pairs allocated 28.1 MiB, 4 MiB allocated 129.9 MiB and 16 MiB
// allocated 503.2 MiB, i.e. 28x to 32x, which puts a body at the cap in the
// gigabytes. The baseline this branch started from had no parser at all, so the
// amplification arrived with the ordering fix.
//
// Those figures are reproducible rather than remembered: delete the
// maxVersionTextBytes clause from versionComponents and the first case below
// prints the ratio it measured on the way to failing. It reported 32.5x for the
// 4 MiB input when that was done.
//
// The log. The value is echoed as the "got" attribute. openAppendLog restarts a
// log file that has already reached logRollAtBytes, so a single oversized answer
// does not merely bloat the file, it makes the NEXT start throw the operator's
// history away.

// TestVersionComponents_RefusesAValueTooLongToBeAVersion is the parser half.
func TestVersionComponents_RefusesAValueTooLongToBeAVersion(t *testing.T) {
	const inputBytes = 4 << 20
	hostile := strings.Repeat("1.", inputBytes/2)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, ok := versionComponents(hostile)
	runtime.ReadMemStats(&after)
	spent := after.TotalAlloc - before.TotalAlloc
	runtime.KeepAlive(out)

	t.Logf("input %d bytes -> ok=%v components=%d allocated=%d bytes (%.1fx)",
		len(hostile), ok, len(out), spent, float64(spent)/float64(len(hostile)))

	if ok {
		t.Errorf("a %d byte value was accepted as a version number; nothing that long is one, "+
			"and parsing it is what the far side is paying this side to do", len(hostile))
	}
	if spent > inputBytes {
		t.Errorf("parsing a %d byte value allocated %d bytes; the answer arrives under a %d MiB "+
			"cap and this runs on a goroutine whose OOM takes the process with it",
			len(hostile), spent, 128)
	}

	// CONTROL: the bound must not swallow a version. Without this the assertions
	// above are satisfied by a parser that refuses everything.
	for _, good := range []string{"0.4.7", "8.3.27.2130", "0.5.0-beta", "1.0.0"} {
		if _, ok := versionComponents(good); !ok {
			t.Errorf("the bound refused %q, which is an ordinary version", good)
		}
	}
	// And the refusal is about LENGTH, not about the shape: a dotted number
	// exactly at the cap still parses, one byte more does not. Components are
	// single digits deliberately, so the refusal cannot come from Atoi overflowing
	// on a long one, which is what the first draft of this control measured.
	atCap := strings.Repeat("1.", maxVersionTextBytes/2)
	if len(atCap) != maxVersionTextBytes {
		t.Fatalf("control broken: the fixture is %d bytes, not %d", len(atCap), maxVersionTextBytes)
	}
	if _, ok := versionComponents(atCap); !ok {
		t.Errorf("a %d byte dotted number was refused, but the cap is %d",
			len(atCap), maxVersionTextBytes)
	}
	if _, ok := versionComponents(atCap + "1"); ok {
		t.Errorf("a %d byte value was accepted, but the cap is %d",
			len(atCap)+1, maxVersionTextBytes)
	}
}

// TestVersionProbe_LogsABoundedValue is the log half, driven through the probe
// so the assertion is about what an operator's file receives.
func TestVersionProbe_LogsABoundedValue(t *testing.T) {
	const answerBytes = 2 << 20
	hostile := strings.Repeat("9", answerBytes)

	got := captureProbeLog(t, http.StatusOK, "application/json", `{"version":"`+hostile+`"}`)
	t.Logf("answer %d bytes -> log %d bytes", answerBytes, len(got))

	if len(got) >= answerBytes {
		t.Errorf("a %d byte version answer produced a %d byte log line; logRollAtBytes is %d, "+
			"so an answer of this class makes the NEXT start truncate the file and take the "+
			"operator's history with it", answerBytes, len(got), logRollAtBytes)
	}
	if !hasError(got) {
		t.Errorf("the oversized answer was not reported as a fault at all:\n%.500s", got)
	}

	// CONTROL: a version that fits is still shown WHOLE. A bound that hid the
	// value in every case would pass the assertion above and lose the diagnostic.
	small := versionAnswer(t, "0.0.1")
	if !strings.Contains(small, "0.0.1") {
		t.Errorf("an ordinary version stopped being named in the log:\n%s", small)
	}
	if strings.Contains(small, "truncated") {
		t.Errorf("a version that fits was announced as truncated:\n%s", small)
	}
}

// TestVersionProbe_OversizedAnswerCannotRollTheLog is the same property one step
// further out: on the real binary, with a real log file, against a responder
// that sends an answer larger than the roll threshold.
//
// This is the consequence that matters. The file itself is the operator's record
// of every run, openAppendLog restarts it once it reaches logRollAtBytes, and a
// single answer must not be able to reach that on its own.
func TestVersionProbe_OversizedAnswerCannotRollTheLog(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a multi-megabyte HTTP answer")
	}
	body := `{"version":"` + strings.Repeat("9", logRollAtBytes+(1<<20)) + `"}`
	url, hits := slowVersionServer(t, 0, body)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	run := runChildWithStdin(t, nil, "--base", url, "--cache-dir", cacheDir)
	logged, _ := os.ReadFile(filepath.Join(cacheDir, "stderr.log"))

	t.Logf("answer %d bytes -> exit=%d log=%d bytes requests=%d roll threshold=%d",
		len(body), run.exit, len(logged), hits.Load(), logRollAtBytes)
	if hits.Load() != 1 {
		t.Fatalf("the probe made %d requests, want 1; the fixture did not exercise the path",
			hits.Load())
	}
	if len(logged) >= logRollAtBytes {
		t.Errorf("one answer wrote %d bytes into the log, at or past the %d byte threshold at "+
			"which the next start truncates it", len(logged), logRollAtBytes)
	}
	if len(logged) == 0 {
		t.Error("the oversized answer produced no log line at all, so this run cannot tell a " +
			"bound from a silence")
	}
}
