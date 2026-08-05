//go:build unix

package dump

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestAManifestThatWouldBlockCannotWedgeTheIndexBuild.
//
// WHY THIS IS NOT A CURIOSITY. The manifest read runs inside the sync.Once that
// idx.moduleKeyFor takes, and every file-loading goroutine in the pool waits on
// that Once. A bare os.Open on a FIFO with no writer blocks until one appears, so
// ONE named pipe called Configuration.xml in the dump directory stopped the whole
// index build forever: no error, no timeout, no partial index. Measured on the
// version this replaces, extensionNameOf had still not returned after three
// seconds.
//
// THE TEST ITSELF HAS A DEADLINE, which is not decoration: without one a
// regression would hang the package's own test run instead of failing it.
//
// Two things keep it shut, and both are needed. The pre-open lstat refuses a
// non-regular final component outright, so the FIFO is never opened at all;
// nonblockOpenFlag closes the window between that check and the open, where a
// regular file could be swapped for a pipe. The same pair is what
// subsystem_reader.go already uses, and the flag is that file's constant.
func TestAManifestThatWouldBlockCannotWedgeTheIndexBuild(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, extManifestClassic)
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}

	type result struct {
		layout extensionLayout
	}
	done := make(chan result, 1)
	go func() { done <- result{detectExtensionLayout(dir)} }()

	select {
	case got := <-done:
		if !got.layout.empty() {
			t.Errorf("a FIFO produced a layout: self=%q byDir=%v", got.layout.self, got.layout.byDir)
		}
		if len(got.layout.doubts) != 1 || got.layout.doubts[0].reason != doubtManifestNotRegular {
			t.Errorf("doubts = %v, want one non-regular-file doubt", got.layout.doubts)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("detectExtensionLayout has not returned after 15s on a FIFO named " +
			"Configuration.xml: the index build is wedged, and it is wedged inside the " +
			"sync.Once every loader goroutine waits on")
	}

	// POSITIVE CONTROL: a real manifest in the same place, read through the same
	// code, IS recognised. Without it a reader that refused everything would pass.
	if err := os.Remove(fifo); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fifo, []byte(classicExtensionManifest("Обычный", true)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := extensionNameOf(dir); !ok || got != "Обычный" {
		t.Fatalf("positive control failed: (%q, %v)", got, ok)
	}
}

// TestADirectoryNamedLikeTheManifestIsNotReadAsOne is the other non-regular shape,
// and the cheap one to get wrong: os.Open succeeds on a directory and io.ReadAll
// then fails with EISDIR, which the old reader turned into "not an extension"
// rather than into a doubt.
func TestADirectoryNamedLikeTheManifestIsNotReadAsOne(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, extManifestClassic), 0o755); err != nil {
		t.Fatal(err)
	}
	l := detectExtensionLayout(dir)
	if !l.empty() {
		t.Errorf("a directory named Configuration.xml produced a layout: self=%q", l.self)
	}
	if len(l.doubts) != 1 || l.doubts[0].reason != doubtManifestNotRegular {
		t.Errorf("doubts = %v, want one non-regular-file doubt", l.doubts)
	}
}
