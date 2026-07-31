package main

// The read-only cache, through the REAL serve entry point.
//
// dump/readonly_cache_test.go pins the mechanism; this pins the outcome an operator
// sees, through openServeIndexLocal — the same function main calls — so that a fix
// living only in the dump package's own idea of an open cannot pass while the
// server still refuses.
//
// THE DEFECT, measured on the real binary before this: a warm cache that had just
// answered "220 совпадений" was chmod -R a-w'ed, and every start after that
// returned "search: index build failed: claiming the existing generation ...:
// permission denied". Not once. Permanently. v1.12.0 on the same cache answered the
// query.
//
// NOT PINNED HERE: the read-only MOUNT half of the same defect. Creating one needs
// hdiutil (or a loop device) and root-ish machine state that a test must not take,
// so the mount case was run by hand and is recorded in the change description only.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// roCacheFreeze clears every write bit under root and restores them before the
// test's temp dirs are removed. It models chmod -R a-w, which is what the field
// report and the gate both used.
func roCacheFreeze(t *testing.T, root string) {
	t.Helper()
	type saved struct {
		path string
		mode os.FileMode
	}
	var modes []saved
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
		t.Fatalf("walking %s before freezing it: %v", root, err)
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
	// POSITIVE CONTROL: the freeze took, so what follows is a test of the frozen
	// path and not of the ordinary one.
	if f, err := os.CreateTemp(root, ".control-"); err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		t.Fatalf("the control failed: %s is still writable after clearing its write bits", root)
	}
}

// TestReadOnlyCache_ServeOpenKeepsServingAFrozenCache is the regression, end to
// end, and it runs the open TWICE because the defect was not that the first start
// failed — it was that every start failed, for ever.
func TestReadOnlyCache_ServeOpenKeepsServingAFrozenCache(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	for i := range 8 {
		serveReapWriteModule(t, dumpDir, i)
	}

	// Warm the cache the way a first run does, then let it go, so the generation is
	// READY with an empty registry — the state a cache is in between runs.
	warm, err := serveReapOpenAndWait(t, dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("warming the cache: %v", err)
	}
	if err := serveReapSearchWorks(warm); err != nil {
		t.Fatalf("control: the warm cache must answer before it is frozen: %v", err)
	}
	_ = warm.Close()

	roCacheFreeze(t, cacheDir)

	for round := 1; round <= 3; round++ {
		idx, err := serveReapOpenAndWait(t, dumpDir, cacheDir)
		if err != nil {
			t.Fatalf("round %d: a frozen cache must still serve, but the open failed: %v", round, err)
		}
		if err := serveReapSearchWorks(idx); err != nil {
			t.Errorf("round %d: the frozen cache opened but answers nothing: %v", round, err)
		}
		_ = idx.Close()
	}
}

// TestReadOnlyCache_ServeOpenStillRefusesWhatAPeerCouldReap is the other half. The
// generation cannot be claimed and the arena around it CAN be written, so a reaper
// can still take it. Serving there is the defect the reader registry exists to
// prevent, and no amount of "but it used to work" makes it safe.
func TestReadOnlyCache_ServeOpenStillRefusesWhatAPeerCouldReap(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	for i := range 8 {
		serveReapWriteModule(t, dumpDir, i)
	}

	warm, err := serveReapOpenAndWait(t, dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("warming the cache: %v", err)
	}
	_ = warm.Close()

	genDir := roCacheOnlyGeneration(t, cacheDir)
	roCacheFreeze(t, genDir) // the generation only; the arena above it stays writable

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	idx, err := openServeIndexLocal(ctx, dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("the placeholder open must not fail synchronously: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	select {
	case <-idx.Done():
	case <-time.After(180 * time.Second):
		t.Fatal("the serve open did not finish within 180s")
	}
	if idx.Ready() {
		t.Fatal("an unclaimable generation in a WRITABLE arena was served; a co-located reaper can " +
			"remove it while it is being served")
	}
	buildErr := idx.BuildError()
	if buildErr == nil {
		t.Fatal("the open neither became ready nor recorded why")
	}
	if !strings.Contains(buildErr.Error(), "writable by this process") {
		t.Errorf("the recorded error does not say why serving unclaimed could not be justified: %v", buildErr)
	}
}

// roCacheOnlyGeneration returns the single generation directory under cacheDir,
// failing the test if the arena does not hold exactly one. Finding it by walking
// rather than by recomputing the signature keeps this test independent of how a
// gensig is derived.
func roCacheOnlyGeneration(t *testing.T, cacheDir string) string {
	t.Helper()
	var found []string
	if err := filepath.WalkDir(cacheDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && filepath.Base(filepath.Dir(p)) == "g" {
			found = append(found, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walking the cache: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one generation under %s, found %d: %v", cacheDir, len(found), found)
	}
	return found[0]
}
