package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// startup_refusal_delivery_test.go is the DELIVERY half of the base refusal.
//
// base_refusal_test.go pins where the refusal lives and what it says, and states
// in its own words that the refusal "ends in os.Exit, so it cannot be called from
// a test". That is true of an in-process call and it is the reason the delivery
// went unmeasured: the guard was asserted to EXIST while the channel it writes to
// had already been replaced sixty lines earlier. A structural test cannot see an
// fd redirect, so this file runs the real main in a real child process and reads
// the two file descriptors an MCP client actually reads.
//
// Every case here launches the binary the way an MCP client launches it: stdin is
// not a terminal, so effectiveTTY is false and the stderr redirect is in force.
// That is the whole point; a case that ran on a terminal would measure the one
// configuration where the defect does not exist.

const refusalHelperEnv = "MCP_1C_REFUSAL_HELPER"

// TestStartupRefusalHelperProcess is not a test. It is the child: it re-enters
// main with the arguments the parent chose, so the code under measurement is the
// real startup path including the real redirect, not a re-implementation of it.
func TestStartupRefusalHelperProcess(t *testing.T) {
	if os.Getenv(refusalHelperEnv) != "1" {
		t.Skip("child process entry point; run by TestStartupRefusalReachesTheUserInPipeMode")
	}
	os.Args = append([]string{"mcp-1c"}, strings.Split(os.Getenv("MCP_1C_REFUSAL_ARGS"), "\n")...)
	main()
	// main returned instead of exiting, so exit here rather than letting the
	// testing framework print its own PASS line. That line would land on fd 1
	// and make the stdout-discipline assertions unfalsifiable.
	os.Exit(0)
}

// refusalRun is what one child launch produced.
type refusalRun struct {
	exit   int
	stdout []byte
	stderr []byte
}

// runRefusalChild launches the helper with args and returns both descriptors
// separately. Separately is load-bearing: the defect is invisible if the two are
// merged, and merging them is also what would hide a regression that put the
// message on stdout, where the JSON-RPC stream lives.
func runRefusalChild(t *testing.T, args ...string) refusalRun {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartupRefusalHelperProcess$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		refusalHelperEnv+"=1",
		"MCP_1C_REFUSAL_ARGS="+strings.Join(args, "\n"),
		// The child must not inherit an ambient cache dir or a no-TTY override
		// from the environment the parent happens to run in.
		"MCP_1C_CACHE_DIR=",
		"MCP_1C_NO_TTY=",
	)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	// Stdin left nil: the child gets /dev/null, which is not a terminal, which is
	// exactly the pipe-mode condition. Verified by the negative control below,
	// which reaches s.Run and returns on EOF.
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("launching the child: %v", err)
	}
	return refusalRun{exit: code, stdout: out.Bytes(), stderr: errb.Bytes()}
}

// theRefusedBase carries a credential net/url cannot separate. Loopback only, and
// it is never dialled: the refusal happens before any client is built.
const theRefusedBase = `http://Админ:Пароль123@127.0.0.1/hs/mcp-1c`

func TestStartupRefusalReachesTheUserInPipeMode(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")

	run := runRefusalChild(t, "--base", theRefusedBase, "--cache-dir", cacheDir)

	t.Logf("EXIT=%d  stdout=%d bytes  stderr=%d bytes", run.exit, len(run.stdout), len(run.stderr))

	if run.exit != 1 {
		t.Fatalf("premise broken: a refused base exited %d, not 1, so this case is not measuring the refusal", run.exit)
	}

	// THE DEFECT: a user launching the server the way an MCP client launches it
	// gets exit 1 and nothing to read.
	if len(run.stderr) == 0 {
		logged, _ := os.ReadFile(filepath.Join(cacheDir, "stderr.log"))
		t.Errorf("the refusal put 0 bytes on fd 2; the user sees an exit code and nothing else.\n"+
			"the message is in %s instead (%d bytes): %s",
			filepath.Join(cacheDir, "stderr.log"), len(logged), logged)
	}

	// STDOUT DISCIPLINE. fd 1 carries the JSON-RPC stream and must never receive
	// a byte that is not a protocol frame, so a fix that moved the message there
	// would be worse than the defect it repaired.
	if len(run.stdout) != 0 {
		t.Errorf("the startup path wrote %d bytes to fd 1, where the JSON-RPC stream lives: %q",
			len(run.stdout), run.stdout)
	}

	// The message must still be readable, and must still name none of the value.
	if len(run.stderr) > 0 {
		got := string(run.stderr)
		if !strings.Contains(got, "логин и пароль") {
			t.Errorf("what reached fd 2 is not the refusal:\n%s", got)
		}
		for _, secret := range []string{"Пароль123", "Админ", "127.0.0.1"} {
			if strings.Contains(got, secret) {
				t.Errorf("the refusal on fd 2 names %q from the rejected value:\n%s", secret, got)
			}
		}
	}
}

// TestStartupRefusalPositiveControl is the control that proves the redirect is
// the cause. Same binary, same input, one flag: --verbose sets effectiveTTY and
// the redirect does not run. If this case is silent too, the cause is something
// other than the redirect and the case above is measuring the wrong thing.
func TestStartupRefusalPositiveControl(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	run := runRefusalChild(t, "--base", theRefusedBase, "--cache-dir", cacheDir, "--verbose")
	t.Logf("EXIT=%d  stdout=%d bytes  stderr=%d bytes", run.exit, len(run.stdout), len(run.stderr))
	if run.exit != 1 {
		t.Fatalf("control broken: --verbose changed the exit code to %d", run.exit)
	}
	if len(run.stderr) == 0 {
		t.Fatalf("control broken: even with --verbose the refusal reached fd 2 with 0 bytes, " +
			"so the redirect is not what the main case is measuring")
	}
	if len(run.stdout) != 0 {
		t.Errorf("--verbose put %d bytes on fd 1: %q", len(run.stdout), run.stdout)
	}
}

// TestStartupRefusalNegativeControl proves exit 1 in the main case comes from the
// refusal and not from an empty stdin. A clean base on a port nothing listens on
// reaches s.Run, which returns on EOF, and the process exits 0.
func TestStartupRefusalNegativeControl(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	run := runRefusalChild(t, "--base", "http://127.0.0.1:1/hs/mcp-1c", "--cache-dir", cacheDir)
	t.Logf("EXIT=%d  stdout=%d bytes  stderr=%d bytes", run.exit, len(run.stdout), len(run.stderr))
	if run.exit != 0 {
		t.Errorf("control broken: a clean base exited %d, so exit 1 above is not attributable to the refusal.\nstderr:\n%s",
			run.exit, run.stderr)
	}
	if len(run.stderr) != 0 {
		t.Errorf("a clean startup wrote %d bytes to fd 2, so a non-empty fd 2 no longer means a refusal: %q",
			len(run.stderr), run.stderr)
	}
	if len(run.stdout) != 0 {
		t.Errorf("a clean startup wrote %d bytes to fd 1: %q", len(run.stdout), run.stdout)
	}
}

// TestStartupRefusalSurvivesAnUnusableLogDirectory is the configuration where the
// log is not a fallback at all. When no log file can be opened, main redirects
// os.Stderr to os.DevNull, so a refusal written only to os.Stderr is destroyed
// rather than merely misfiled: exit 1 recoverable from no channel whatsoever.
// Container and systemd deployments with a scrubbed HOME and a read-only rootfs
// are the natural home of this configuration.
func TestStartupRefusalSurvivesAnUnusableLogDirectory(t *testing.T) {
	ro := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(ro, 0o500); err != nil {
		t.Fatalf("preparing the read-only directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })

	cmd := exec.Command(os.Args[0], "-test.run=^TestStartupRefusalHelperProcess$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		refusalHelperEnv+"=1",
		"MCP_1C_REFUSAL_ARGS="+strings.Join([]string{"--base", theRefusedBase, "--cache-dir", filepath.Join(ro, "cache")}, "\n"),
		"MCP_1C_CACHE_DIR=",
		"MCP_1C_NO_TTY=",
		"HOME="+filepath.Join(ro, "nohome"),
		"TMPDIR="+filepath.Join(ro, "notmp"),
		"XDG_CACHE_HOME="+filepath.Join(ro, "nocache"),
	)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("launching the child: %v", err)
	}
	t.Logf("EXIT=%d  stdout=%d bytes  stderr=%d bytes", code, out.Len(), errb.Len())

	// PREMISE: the run really did fail to place a log where it asked to. Without
	// this the case could pass while silently exercising the ordinary path.
	if _, statErr := os.Stat(filepath.Join(ro, "cache", "stderr.log")); statErr == nil {
		t.Fatalf("premise broken: the child created %s, so this is not the unusable-log configuration",
			filepath.Join(ro, "cache", "stderr.log"))
	}
	if code != 1 {
		t.Fatalf("premise broken: the child exited %d, not 1", code)
	}
	if errb.Len() == 0 {
		t.Errorf("with no log available the refusal reached no channel at all: exit 1, fd 1 and fd 2 both empty")
	}
	if out.Len() != 0 {
		t.Errorf("the refusal went to fd 1, where the JSON-RPC stream lives: %q", out.Bytes())
	}
}
