//go:build unix

package dump

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// MB-2 (deterministic): a subsystem file that is itself a SYMLINK must be refused,
// even when it resolves to a valid in-dump XML file. The lstat+IsRegular guard
// rejects any symlinked final component at check time, which is the portable half of
// the TOCTOU close: a dangling symlink whose target is created mid-parse is the same
// symlinked final component and is refused identically, so it can never redirect the
// read out of the dump.
func TestWalk_SymlinkedSubsystemFile_Refused(t *testing.T) {
	dir := t.TempDir()
	// A real, valid, IN-dump target with a distinctive marker name. safeJoin's
	// containment check passes for this target (it is under the dump root), so only
	// the lstat+IsRegular guard stops the symlink from being followed.
	secWrite(t, filepath.Join(dir, "real", "Target.xml"), secSubBody("SYMLINK_TARGET_MARKER"))
	// A plain regular sibling so the layout is Hierarchical, the walk has work, and
	// Subsystems/ exists before the symlink below is planted into it.
	secWrite(t, filepath.Join(dir, "Subsystems", "Plain.xml"), secSubBody("Plain"))
	if err := os.Symlink(filepath.Join(dir, "real", "Target.xml"),
		filepath.Join(dir, "Subsystems", "Link.xml")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	names := flattenNames(subs)
	if containsStr(names, "SYMLINK_TARGET_MARKER") {
		t.Fatalf("a symlinked subsystem file was followed and parsed; names=%v", names)
	}
	if !containsStr(names, "Plain") {
		t.Errorf("the plain sibling must still parse; names=%v", names)
	}
	if !warningsContain(warnings, "Link") {
		t.Errorf("the refused symlinked subsystem must be NAMED; warnings=%v", warnings)
	}
}

// MB-2 (concurrent TOCTOU): dangling symlinks Subsystems/L####.xml -> <outside>/secret
// (target ABSENT at plant time) pass safeJoin's ErrNotExist-tolerant containment
// check. Before the fix, a concurrent create of <outside>/secret in the check->use
// window let os.Open follow the link and read the out-of-dump file into the returned
// tree. The lstat+IsRegular guard now refuses every symlinked final component before
// the open (and the post-open os.SameFile check closes any residual swap), so the
// outside marker must NEVER appear across many iterations.
func TestWalk_DanglingSymlinkTOCTOU_NeverReadsOutside(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	const outsideMarker = "OUTSIDE_TOCTOU_MARKER"
	validXML := secSubBody(outsideMarker)

	// A valid in-dump sibling so the layout is Hierarchical and the walk runs.
	secWrite(t, filepath.Join(dir, "Subsystems", "Plain.xml"), secSubBody("Plain"))

	const nLinks = 64
	for i := 0; i < nLinks; i++ {
		name := fmt.Sprintf("L%04d.xml", i)
		if err := os.Symlink(secret, filepath.Join(dir, "Subsystems", name)); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.WriteFile(secret, []byte(validXML), 0o644)
			_ = os.Remove(secret)
		}
	}()

	const iterations = 300
	leaked := false
	for i := 0; i < iterations && !leaked; i++ {
		subs, _, err := ParseAllSubsystemsCtx(context.Background(), dir)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("iteration %d err = %v, want nil", i, err)
		}
		if containsStr(flattenNames(subs), outsideMarker) {
			leaked = true
		}
	}
	close(stop)
	wg.Wait()
	_ = os.Remove(secret)

	if leaked {
		t.Fatalf("TOCTOU: an out-of-dump file was read via a dangling symlink (marker %q appeared in the tree)", outsideMarker)
	}
}
