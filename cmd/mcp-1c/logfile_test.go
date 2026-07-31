package main

// The log file, and the two ways it stopped being evidence.
//
// DEFECT A — THE REFUSAL WAS INVISIBLE EXACTLY WHERE IT FIRED HARDEST. openLogFile
// created the log inside cacheDir and nowhere else, and main's handler for a
// failure to create it is to point slog at os.DevNull. So the unwritable cache
// directory that makes a serve open refuse also destroyed the only channel that
// could report the refusal. MEASURED on the real binary: with the cache root
// read-only, stderr.log stayed 0 bytes across a warm-up and three runs; with only
// readers/ unwritable and the cache root writable, the SAME refusal produced 662
// bytes and one ERROR line. The level was never the defect. The destination was.
//
// DEFECT B — THE OPEN TRUNCATED. It ended in os.Create, so in a shared cache every
// process that started wiped what the ones before it had written: after 36
// processes, one record survived. That is the multi-process arena this whole change
// set exists to protect, and it was unauditable.
//
// WHAT THESE TESTS PIN, and what they deliberately do not:
//
//   - They pin openLogFile, not main. main's own wiring (which handler gets the
//     returned file, and the DevNull last resort behind it) has no test here; it is
//     covered only by the real-binary runs recorded in the change description.
//   - Nothing pins the case where all three candidate directories fail. Making
//     os.TempDir() unwritable is not something a test may do to the machine it runs
//     on, so the DevNull path below openLogFile stays untested — as it was before.

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installTestLogger points the default slog handler at w, at the SAME LevelError
// cmd/mcp-1c pins in stdio mode, and returns a function that restores it. The level
// matters: a note emitted below Error is a note an operator would never have.
func installTestLogger(t *testing.T, w *os.File) func() {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelError})))
	return func() { slog.SetDefault(prev) }
}

// logTestHome points os.UserCacheDir() inside the test's own tree, so the fallback
// candidate is one the test controls and no run ever writes into the real user
// cache. XDG_CACHE_HOME is set too because os.UserCacheDir consults it on Linux and
// HOME on macOS.
func logTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

// freezeTree clears the write bits of dir AND of everything already inside it, and
// restores them before the test's temp dirs are removed (cleanups run LIFO, and
// this one is registered after t.TempDir's).
//
// IT HAS TO BE RECURSIVE, and that is a measurement rather than caution. Freezing
// only the directory left an already-created stderr.log writable, and appending to
// an EXISTING file needs no write permission on the directory holding it — so the
// open succeeded, the fallback never ran, and the test failed against correct code.
// The scenario being modelled is chmod -R a-w, which takes the file too.
func freezeTree(t *testing.T, dir string) {
	t.Helper()
	type saved struct {
		path string
		mode os.FileMode
	}
	var modes []saved
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
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
		t.Fatalf("walking %s before freezing it: %v", dir, err)
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
	// POSITIVE CONTROL: the freeze took. Without it a chmod that did nothing would
	// let every assertion below pass while exercising the ordinary path.
	if f, err := os.CreateTemp(dir, ".control-"); err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		t.Fatalf("the control failed: %s is still writable after clearing its write bits", dir)
	}
}

func TestLogFile_UnwritableCacheDirDoesNotSilenceTheLog(t *testing.T) {
	logTestHome(t)
	cacheDir := t.TempDir()

	// CONTROL FIRST: a writable cache dir is used as-is and reports no substitution.
	// Without this half, a function that ALWAYS fell back would pass the real
	// assertion and would have quietly moved every ordinary run's log.
	ok, err := openLogFile("mcp-1c", cacheDir, "stderr.log")
	if err != nil {
		t.Fatalf("control: a writable cache dir must be usable: %v", err)
	}
	_ = ok.file.Close()
	if ok.path != filepath.Join(cacheDir, "stderr.log") {
		t.Errorf("control: a writable cache dir was not used; log went to %s", ok.path)
	}
	if ok.cause != nil {
		t.Errorf("control: a writable cache dir reported a substitution: %v", ok.cause)
	}

	freezeTree(t, cacheDir)

	got, err := openLogFile("mcp-1c", cacheDir, "stderr.log")
	if err != nil {
		t.Fatalf("an unwritable cache dir must not leave the run with no log at all: %v", err)
	}
	defer got.file.Close()
	if strings.HasPrefix(got.path, cacheDir) {
		t.Fatalf("the log was placed in the unwritable cache dir: %s", got.path)
	}
	if got.cause == nil {
		t.Error("the substitution was not reported, so nothing can say where this run's diagnostics went")
	}
	if got.requested != cacheDir {
		t.Errorf("the reported request was %q, want %q", got.requested, cacheDir)
	}
	// It must be a log that actually TAKES writes; a path is not a channel.
	if _, err := got.file.WriteString("proof\n"); err != nil {
		t.Fatalf("the fallback log is not writable: %v", err)
	}
	body, err := os.ReadFile(got.path)
	if err != nil || !strings.Contains(string(body), "proof") {
		t.Fatalf("the fallback log did not keep what was written to it (%v): %q", err, body)
	}
}

func TestLogFile_FallsBackPastAnUnwritablePlatformCacheDirToo(t *testing.T) {
	home := logTestHome(t)
	cacheDir := t.TempDir()
	freezeTree(t, cacheDir)
	freezeTree(t, home) // so os.UserCacheDir()'s tree cannot be created either

	got, err := openLogFile("mcp-1c", cacheDir, "stderr.log")
	if err != nil {
		t.Fatalf("with both the cache dir and the platform cache dir unusable there is still the "+
			"temp dir, but the open failed: %v", err)
	}
	defer got.file.Close()
	if !strings.HasPrefix(got.path, os.TempDir()) {
		t.Errorf("the last-resort log did not land in the temp dir: %s", got.path)
	}
	if got.cause == nil {
		t.Error("the substitution was not reported")
	}
}

func TestLogFile_AppendsInsteadOfTruncating(t *testing.T) {
	logTestHome(t)
	cacheDir := t.TempDir()

	for _, line := range []string{"first process\n", "second process\n", "third process\n"} {
		target, err := openLogFile("mcp-1c", cacheDir, "stderr.log")
		if err != nil {
			t.Fatalf("opening the log: %v", err)
		}
		if _, err := target.file.WriteString(line); err != nil {
			t.Fatalf("writing to the log: %v", err)
		}
		_ = target.file.Close()
	}

	body, err := os.ReadFile(filepath.Join(cacheDir, "stderr.log"))
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	for _, want := range []string{"first process", "second process", "third process"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("%q did not survive the later opens; the log holds:\n%s", want, body)
		}
	}
}

func TestLogFile_RollsOnceItIsPastTheCap(t *testing.T) {
	logTestHome(t)
	cacheDir := t.TempDir()
	path := filepath.Join(cacheDir, "stderr.log")

	// CONTROL: just under the cap, history is kept. This is what makes the roll
	// assertion below a test of the CAP rather than of truncation returning.
	if err := os.WriteFile(path, make([]byte, logRollAtBytes-1), 0o644); err != nil {
		t.Fatal(err)
	}
	under, err := openLogFile("mcp-1c", cacheDir, "stderr.log")
	if err != nil {
		t.Fatal(err)
	}
	_ = under.file.Close()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != logRollAtBytes-1 {
		t.Fatalf("control: a log under the cap was not kept intact; it is %d bytes", st.Size())
	}

	if err := os.WriteFile(path, make([]byte, logRollAtBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	over, err := openLogFile("mcp-1c", cacheDir, "stderr.log")
	if err != nil {
		t.Fatal(err)
	}
	_ = over.file.Close()
	st, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 0 {
		t.Errorf("a log at the cap was not rolled; it is still %d bytes", st.Size())
	}
}

func TestLogFile_FallbackIsReportedIntoTheFileThatWasOpened(t *testing.T) {
	logTestHome(t)
	cacheDir := t.TempDir()
	freezeTree(t, cacheDir)

	target, err := openLogFile("mcp-1c", cacheDir, "stderr.log")
	if err != nil {
		t.Fatal(err)
	}
	defer target.file.Close()

	prev := installTestLogger(t, target.file)
	reportLogFallback(target)
	prev()

	body, err := os.ReadFile(target.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"level=ERROR", cacheDir, target.path, "MCP_1C_CACHE_DIR"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the fallback note does not contain %q; the log holds:\n%s", want, body)
		}
	}

	// And it says nothing at all when there was nothing to say.
	quiet, err := openLogFile("mcp-1c", t.TempDir(), "stderr.log")
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.file.Close()
	restore := installTestLogger(t, quiet.file)
	reportLogFallback(quiet)
	restore()
	if body, err := os.ReadFile(quiet.path); err != nil || len(body) != 0 {
		t.Errorf("an ordinary run wrote a fallback note (%v): %q", err, body)
	}
}
