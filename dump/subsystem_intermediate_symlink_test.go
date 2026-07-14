//go:build unix

package dump

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These regression tests close an INTERMEDIATE-directory-symlink TOCTOU: an escaping
// symlink can sit at ANY path component, not only the final one. Before the os.Root
// containment fix the reader guarded only the final component (lstat+IsRegular,
// O_NOFOLLOW, os.SameFile) and validated paths with a filepath.Join + os.Lstat +
// EvalSymlinks (safeJoin) whose ErrNotExist tolerance left a window: with an
// intermediate directory symlink pointing out of the dump and a concurrent writer
// toggling the out-of-dump target, the pre-open check passed while the momentarily
// absent final file was absent, then os.Lstat and the open both FOLLOWED the
// intermediate symlink and os.SameFile compared the already-escaped inode to itself,
// serving a subsystem whose Name came from OUTSIDE the dump. os.Root confines EVERY
// path component beneath the dump root at both walk sites, so an escaping symlink at
// any depth is refused with no timing dependence. The out-of-dump marker must NEVER
// appear in the parsed tree.

// MB-3 (Ext layout, concurrent TOCTOU): Subsystems/Evil####/Ext is a directory
// symlink pointing out of the dump; a concurrent writer toggles
// <outside>/Subsystem.xml. This is the clean two-phase window (safeJoin sees the
// final file absent, the open sees it present through the intermediate Ext symlink),
// so it reproduces the pre-fix leak deterministically and fast. After the fix
// os.Root refuses the intermediate Ext symlink, so no scheduling can leak.
func TestWalkExt_IntermediateDirSymlinkTOCTOU_NeverReadsOutside(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	outsideXML := filepath.Join(outside, "Subsystem.xml")
	const outsideMarker = "OUTSIDE_EXT_INTERMEDIATE_MARKER"
	validXML := secSubBody(outsideMarker)

	// A valid in-dump Ext-layout sibling so the layout is Ext and the walk runs.
	secWrite(t, filepath.Join(dir, "Subsystems", "Good", "Ext", "Subsystem.xml"), secSubBody("Good"))

	// Many Evil####/Ext -> outside directory symlinks (intermediate component escape).
	const nLinks = 64
	for i := 0; i < nLinks; i++ {
		evilExt := filepath.Join(dir, "Subsystems", fmt.Sprintf("Evil%04d", i), "Ext")
		if err := os.MkdirAll(filepath.Dir(evilExt), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, evilExt); err != nil {
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
			_ = os.WriteFile(outsideXML, []byte(validXML), 0o644)
			_ = os.Remove(outsideXML)
		}
	}()

	const iterations = 400
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
	_ = os.Remove(outsideXML)

	if leaked {
		t.Fatalf("TOCTOU: an out-of-dump file was read via an intermediate Ext directory symlink (marker %q appeared in the tree)", outsideMarker)
	}
}

// MB-3 (Hierarchical layout, deterministic containment): an escaping intermediate
// recursion-directory symlink (Subsystems/Escaper/Subsystems -> an out-of-dump dir
// holding an Evil.xml) must serve NO out-of-dump content and must be NAMED as
// refused, while the in-dump parent and a genuine in-dump child still parse. This is
// the deterministic guard for the hierarchical recursion-directory open (the
// readDir path), which os.Root confines exactly like the Ext file open above. It is
// fast and runs by default; the concurrent TOCTOU reproduction of the same class is
// the env-gated stress test below.
func TestWalkHierarchical_IntermediateDirSymlink_Contained(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	const outsideMarker = "OUTSIDE_HIER_INTERMEDIATE_MARKER"
	secWrite(t, filepath.Join(outside, "Evil.xml"), secSubBody(outsideMarker))

	// In-dump parent with a genuine in-dump child: the walk must still recurse.
	secWrite(t, filepath.Join(dir, "Subsystems", "Parent.xml"), secSubBody("Parent"))
	secWrite(t, filepath.Join(dir, "Subsystems", "Parent", "Subsystems", "Good.xml"), secSubBody("Good"))

	// A second parent whose recursion directory Subsystems/Escaper/Subsystems escapes
	// the dump (an intermediate directory symlink to a real out-of-dump dir).
	secWrite(t, filepath.Join(dir, "Subsystems", "Escaper.xml"), secSubBody("Escaper"))
	if err := os.MkdirAll(filepath.Join(dir, "Subsystems", "Escaper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "Subsystems", "Escaper", "Subsystems")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	subs, warnings, err := ParseAllSubsystemsCtx(context.Background(), dir)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	names := flattenNames(subs)
	if containsStr(names, outsideMarker) {
		t.Fatalf("out-of-dump content served via an intermediate recursion-directory symlink; names=%v", names)
	}
	if !containsStr(names, "Parent") || !containsStr(names, "Good") {
		t.Errorf("the in-dump parent and its in-dump child must still parse; names=%v", names)
	}
	if !containsStr(names, "Escaper") {
		t.Errorf("the escaper parent itself still parses (only its escaping subtree is refused); names=%v", names)
	}
	if !warningsContain(warnings, "Escaper") {
		t.Errorf("the refused escaping recursion directory must be NAMED; warnings=%v", warnings)
	}
}

// MB-3 (Hierarchical layout, concurrent TOCTOU stress): the reproduction of the
// intermediate recursion-directory symlink TOCTOU. walkHierarchical re-validates the
// recursion directory before reading it, so unlike the Ext case the pre-fix leak is
// a narrow multi-phase race (the out-of-dump directory must flip absent -> present
// across the safeJoin/readdir gap, and its Child.xml must flip within the per-file
// window); it needs many parents, per-parent writers, and concurrent parsers to
// reproduce, which is filesystem heavy. It is therefore env-gated (opt-in via
// MCP_TOCTOU_STRESS=1) so it does not slow the default suite. On the pre-fix code it
// leaks (an out-of-dump marker appears); after the os.Root fix the leak is
// structurally impossible, which the fast deterministic test above guards by default.
func TestWalkHierarchical_IntermediateDirSymlinkTOCTOU_NeverReadsOutside(t *testing.T) {
	if os.Getenv("MCP_TOCTOU_STRESS") == "" {
		t.Skip("set MCP_TOCTOU_STRESS=1 to run the slow walkHierarchical TOCTOU stress reproduction")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	const outsideMarker = "OUTSIDE_HIER_INTERMEDIATE_MARKER"
	childBody := secSubBody(outsideMarker)

	const nParents = 128
	outsideDirs := make([]string, nParents)
	for i := 0; i < nParents; i++ {
		p := fmt.Sprintf("P%04d", i)
		od := filepath.Join(outside, fmt.Sprintf("od%04d", i))
		outsideDirs[i] = od
		secWrite(t, filepath.Join(dir, "Subsystems", p+".xml"), secSubBody(p))
		if err := os.MkdirAll(filepath.Join(dir, "Subsystems", p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(od, filepath.Join(dir, "Subsystems", p, "Subsystems")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
	}

	stop := make(chan struct{})
	var stopOnce sync.Once
	stopf := func() { stopOnce.Do(func() { close(stop) }) }
	var wg sync.WaitGroup
	for i := 0; i < nParents; i++ {
		od := outsideDirs[i]
		childXML := filepath.Join(od, "Child.xml")
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = os.MkdirAll(od, 0o755)
				_ = os.WriteFile(childXML, []byte(childBody), 0o644)
				_ = os.Remove(childXML)
				_ = os.RemoveAll(od)
			}
		}()
	}

	const parsers = 32
	const maxParses = 12000
	deadline := time.After(60 * time.Second)
	var counter int64
	var leaked int32
	var pwg sync.WaitGroup
	for k := 0; k < parsers; k++ {
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			for {
				select {
				case <-stop:
					return
				case <-deadline:
					stopf()
					return
				default:
				}
				subs, _, err := ParseAllSubsystemsCtx(context.Background(), dir)
				if err != nil {
					continue
				}
				if containsStr(flattenNames(subs), outsideMarker) {
					atomic.StoreInt32(&leaked, 1)
					stopf()
					return
				}
				if atomic.AddInt64(&counter, 1) >= maxParses {
					stopf()
					return
				}
			}
		}()
	}
	pwg.Wait()
	stopf()
	wg.Wait()
	for _, od := range outsideDirs {
		_ = os.RemoveAll(od)
	}

	if atomic.LoadInt32(&leaked) == 1 {
		t.Fatalf("TOCTOU: an out-of-dump file was read via an intermediate recursion-directory symlink (marker %q appeared in the tree)", outsideMarker)
	}
}
