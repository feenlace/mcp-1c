package installer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestBuildConnectionArgs_FileMode verifies file-mode produces only --db-path.
func TestBuildConnectionArgs_FileMode(t *testing.T) {
	opts := IbcmdOptions{DBPath: "/tmp/infobase", ServerMode: false}
	args, err := buildConnectionArgs(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) != 1 || args[0] != "--db-path=/tmp/infobase" {
		t.Errorf("expected --db-path arg, got %v", args)
	}
}

// TestBuildConnectionArgs_ServerMode verifies cluster-server parsing.
func TestBuildConnectionArgs_ServerMode(t *testing.T) {
	opts := IbcmdOptions{
		DBPath:     `cluster:1541\db1_staging`,
		ServerMode: true,
		DBUser:     "dt_user",
		DBPassword: "p@ss!w0rd",
	}
	args, err := buildConnectionArgs(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// CRITICAL: --db-server must NOT contain trailing ";"
	// (this is the #1 gotcha we're codifying)
	for _, a := range args {
		if strings.HasPrefix(a, "--db-server=") && strings.HasSuffix(a, ";") {
			t.Errorf("CRITICAL: --db-server has trailing semicolon (gotcha #1): %s", a)
		}
	}

	// Verify expected flags are present
	want := map[string]bool{
		"--dbms=PostgreSQL":          true,
		"--db-server=cluster:1541":   true,
		"--db-name=db1_staging":      true,
		"--db-user=dt_user":          true,
		`--db-pwd=p@ss!w0rd`:         true,
	}
	got := map[string]bool{}
	for _, a := range args {
		got[a] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("expected arg %q not in %v", k, args)
		}
	}
}

// TestBuildConnectionArgs_UserWithoutPasswordRejected verifies we prevent
// the STDIN-hang gotcha (#2).
func TestBuildConnectionArgs_UserWithoutPasswordRejected(t *testing.T) {
	opts := IbcmdOptions{
		DBPath:     `cluster\db`,
		ServerMode: true,
		DBUser:     "admin",
		// DBPassword intentionally empty
	}
	_, err := buildConnectionArgs(opts)
	if err == nil {
		t.Fatal("expected error for DBUser without DBPassword (would hang on STDIN), got nil")
	}
	if !strings.Contains(err.Error(), "DBPassword empty") {
		t.Errorf("error should mention DBPassword: %v", err)
	}
}

// TestBuildConnectionArgs_ServerModeWithoutBackslashRejected verifies invalid DBPath.
func TestBuildConnectionArgs_ServerModeWithoutBackslashRejected(t *testing.T) {
	opts := IbcmdOptions{
		DBPath:     "no-backslash-here",
		ServerMode: true,
	}
	_, err := buildConnectionArgs(opts)
	if err == nil {
		t.Fatal("expected error for DBPath without backslash in ServerMode")
	}
}

// TestFindIbcmd_NextToPlatform verifies path resolution.
func TestFindIbcmd_NextToPlatform(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		t.Skip("test uses Windows-style path; run on Windows or refactor")
	}
	tmp := t.TempDir()
	platform := filepath.Join(tmp, "1cv8.exe")
	ibcmd := filepath.Join(tmp, "ibcmd.exe")
	for _, p := range []string{platform, ibcmd} {
		if err := os.WriteFile(p, []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	found, err := findIbcmd(platform)
	if err != nil {
		t.Fatalf("findIbcmd: %v", err)
	}
	if found != ibcmd {
		t.Errorf("expected %s, got %s", ibcmd, found)
	}
}

// TestRunIbcmd_TimeoutKills verifies hard timeout enforcement.
// Uses a slow shell stub instead of real ibcmd.
func TestRunIbcmd_TimeoutKills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub uses /bin/sleep")
	}
	start := time.Now()
	err := runIbcmd(context.Background(), "/bin/sleep", []string{"10"}, 500*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention timeout: %v", err)
	}
	// Sanity: we killed it within ~1s, not waited the full 10s.
	if elapsed > 2*time.Second {
		t.Errorf("timeout enforcement too slow: %v elapsed (expected <2s)", elapsed)
	}
}

// TestClassifyIbcmdError_TimeoutMessage verifies user-friendly hint for lock contention.
func TestClassifyIbcmdError_TimeoutMessage(t *testing.T) {
	original := errFromString("ibcmd timed out after 60s")
	got := classifyIbcmdError("config import files", original, 60*time.Second)
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	msg := got.Error()
	for _, want := range []string{"lock contention", "--ibcmd-timeout", "--installer=designer"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message should mention %q: %s", want, msg)
		}
	}
}

// errFromString is a test helper: wraps string in error.
func errFromString(s string) error {
	return &simpleErr{s}
}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }
