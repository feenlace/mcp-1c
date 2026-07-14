//go:build unix

package dump

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// These tests close the accepted-residual DoS at the top-level os.OpenRoot(dumpDir)
// entry points (ParseAllSubsystemsCtx and EnumerateAppliedObjects, plus the internal
// detectSubsystemLayout helper): when dumpDir ITSELF is a writer-less FIFO,
// os.OpenRoot blocks on an open() that ctx cannot interrupt. That node is set from
// operator / server config, never from dump content, so it is not a security vector,
// but it is closed for completeness. The per-position openDirInRoot guard (proven by
// subsystem_nonregular_test.go) already covers every position UNDER the root; these
// cover the root node itself. os.Stat does not open the node, so it is used to check
// the type BEFORE the blocking open. //go:build unix: mkfifo is unix only.

// parseRootNodeWithin runs ParseAllSubsystemsCtx(dir) in a goroutine and returns its
// result, failing the test if it does not return within d. Before the root-node type
// guard, os.OpenRoot on a writer-less FIFO at dumpDir blocks on a ctx-uninterruptible
// open(), so a timeout here IS the DoS reproduction.
func parseRootNodeWithin(t *testing.T, dir string, d time.Duration) ([]Subsystem, []string, error) {
	t.Helper()
	type result struct {
		subs     []Subsystem
		warnings []string
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dir)
		ch <- result{subs, warnings, err}
	}()
	select {
	case r := <-ch:
		return r.subs, r.warnings, r.err
	case <-time.After(d):
		t.Fatalf("ParseAllSubsystemsCtx did not return within %v: a non-directory dumpDir blocked os.OpenRoot (DoS)", d)
		return nil, nil, nil
	}
}

// enumRootNodeWithin runs EnumerateAppliedObjects(dir) in a goroutine and returns its
// result, failing the test if it does not return within d.
func enumRootNodeWithin(t *testing.T, dir string, d time.Duration) []string {
	t.Helper()
	ch := make(chan []string, 1)
	go func() { ch <- EnumerateAppliedObjects(dir) }()
	select {
	case r := <-ch:
		return r
	case <-time.After(d):
		t.Fatalf("EnumerateAppliedObjects did not return within %v: a non-directory dumpDir blocked os.OpenRoot (DoS)", d)
		return nil
	}
}

// RN-1: dumpDir ITSELF is a writer-less FIFO. Before the guard, os.OpenRoot(dumpDir)
// blocks; after it, ParseAllSubsystemsCtx must return promptly with the named,
// path-free refusal and no partial tree.
func TestParseAllSubsystemsCtx_DumpDirItselfFIFO_RefusedBounded(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "dumpdir-fifo")
	plantFIFO(t, fifo)

	subs, warnings, err := parseRootNodeWithin(t, fifo, 3*time.Second)
	if !errors.Is(err, errDumpDirNotDirectory) {
		t.Fatalf("err = %v, want errDumpDirNotDirectory for a FIFO dumpDir", err)
	}
	if len(subs) != 0 || len(warnings) != 0 {
		t.Errorf("want empty subs/warnings for a refused dumpDir; subs=%v warnings=%v", subs, warnings)
	}
}

// RN-2: EnumerateAppliedObjects on a FIFO dumpDir must return an empty universe
// promptly (its existing fail-safe for an unreadable dumpDir), never block.
func TestEnumerateAppliedObjects_DumpDirItselfFIFO_EmptyBounded(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "dumpdir-fifo")
	plantFIFO(t, fifo)

	if got := enumRootNodeWithin(t, fifo, 3*time.Second); len(got) != 0 {
		t.Errorf("EnumerateAppliedObjects(fifo) = %v, want empty universe", got)
	}
}

// RN-3: detectSubsystemLayout (test-only helper, guarded for completeness) must also
// stay bounded on a FIFO dumpDir and keep its (layoutExt, nil) default-layout contract.
func TestDetectSubsystemLayout_DumpDirItselfFIFO_Bounded(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "dumpdir-fifo")
	plantFIFO(t, fifo)

	type res struct {
		l subsystemLayout
		e error
	}
	ch := make(chan res, 1)
	go func() {
		l, e := detectSubsystemLayout(fifo)
		ch <- res{l, e}
	}()
	select {
	case r := <-ch:
		if r.e != nil {
			t.Errorf("detectSubsystemLayout(fifo) err = %v, want nil", r.e)
		}
		if r.l != layoutExt {
			t.Errorf("detectSubsystemLayout(fifo) = %v, want layoutExt (default)", r.l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("detectSubsystemLayout did not return within 3s on a FIFO dumpDir (DoS)")
	}
}

// RN-4 (regression): a MISSING dumpDir must keep the byte-for-byte pre-guard contract:
// ParseAllSubsystemsCtx -> (nil, nil, nil); EnumerateAppliedObjects -> nil. os.Stat
// returns an error for a missing path, so the guard reports false and the call falls
// through to the unchanged os.OpenRoot ErrNotExist branch.
func TestRootNodeGuard_MissingDumpDir_UnchangedContract(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), missing)
	if err != nil {
		t.Errorf("missing dumpDir: err = %v, want nil", err)
	}
	if subs != nil {
		t.Errorf("missing dumpDir: subs = %v, want nil", subs)
	}
	if warnings != nil {
		t.Errorf("missing dumpDir: warnings = %v, want nil", warnings)
	}
	if got := EnumerateAppliedObjects(missing); got != nil {
		t.Errorf("missing dumpDir: universe = %v, want nil", got)
	}
}

// RN-5 (regression): an EMPTY existing dir yields an empty tree and empty universe with
// no error (the guard falls through because the node is a directory).
func TestRootNodeGuard_EmptyDir_Unchanged(t *testing.T) {
	dir := t.TempDir() // exists, empty

	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err != nil || len(subs) != 0 || len(warnings) != 0 {
		t.Errorf("empty dir: got (subs=%v, warnings=%v, err=%v), want empty/nil", subs, warnings, err)
	}
	if got := EnumerateAppliedObjects(dir); len(got) != 0 {
		t.Errorf("empty dir: universe = %v, want empty", got)
	}
}

// RN-6 (regression): a VALID dump directory parses exactly as before the guard.
func TestRootNodeGuard_ValidDir_ParsesUnchanged(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи.xml"), secSubBody("Продажи"))

	subs, _, err := parseRootNodeWithin(t, dir, 3*time.Second)
	if err != nil {
		t.Fatalf("valid dir: err = %v, want nil", err)
	}
	if !reflect.DeepEqual(flattenNames(subs), []string{"Продажи"}) {
		t.Errorf("valid dir names = %v, want [Продажи]", flattenNames(subs))
	}
}

// RN-7 (regression, KEY): a symlink pointing AT a valid dump directory, used as
// dumpDir, must still be FOLLOWED and parsed. os.Stat follows the symlink and sees a
// directory, so the guard falls through and os.OpenRoot follows it too. Using os.Lstat
// in the guard would wrongly reject this legitimate operator symlink, so this proves
// os.Stat (not os.Lstat) is the correct primitive.
func TestRootNodeGuard_SymlinkToDumpDir_Followed(t *testing.T) {
	real := t.TempDir()
	secWrite(t, filepath.Join(real, "Subsystems", "Продажи.xml"), secSubBody("Продажи"))
	secWrite(t, filepath.Join(real, "Catalogs", "Контрагенты.xml"), objBody("Контрагенты"))

	link := filepath.Join(t.TempDir(), "dumplink")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	subs, warnings, err := parseRootNodeWithin(t, link, 3*time.Second)
	if err != nil {
		t.Fatalf("symlink-to-dir dumpDir: err = %v, want nil (symlink must be followed)", err)
	}
	if !containsStr(flattenNames(subs), "Продажи") {
		t.Errorf("symlink-to-dir dumpDir must be followed and parsed; names=%v", flattenNames(subs))
	}
	if len(warnings) != 0 {
		t.Errorf("symlink-to-dir dumpDir: unexpected warnings %v", warnings)
	}
	if got := enumRootNodeWithin(t, link, 3*time.Second); !containsStr(got, "Справочник.Контрагенты") {
		t.Errorf("symlink-to-dir dumpDir universe must be followed; got %v", got)
	}
}
