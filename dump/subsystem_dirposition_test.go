//go:build unix

package dump

import (
	"path/filepath"
	"testing"
	"time"
)

// These tests extend the writer-less-FIFO DoS coverage from the subsystem FILE
// positions (subsystem_nonregular_test.go) to the subsystem DIRECTORY positions. A
// directory open that is not type-checked first blocks forever on a writer-less FIFO
// planted at that position, and (unlike a bounded read) the blocked open() is not
// interruptible by ctx, so parseBounded's timeout IS the reproduction. plantFIFO,
// parseBounded and the assertion helpers are shared from subsystem_nonregular_test.go
// / subsystem_testhelper_test.go.

// enumBounded runs EnumerateAppliedObjects under a deadline; a non-regular applied
// folder that reached an unguarded open would block it forever (a DoS), so a timeout
// here reproduces that. EnumerateAppliedObjects has no ctx, so bounding is external.
func enumBounded(t *testing.T, dir string) []string {
	t.Helper()
	ch := make(chan []string, 1)
	go func() { ch <- EnumerateAppliedObjects(dir) }()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("EnumerateAppliedObjects did not return: a non-regular folder blocked enumeration (DoS)")
		return nil
	}
}

// DP-1 (top directory position): a writer-less FIFO planted exactly where the
// top-level Subsystems/ directory is expected must not block the parse. Layout
// detection opens Subsystems/ first, so this is the earliest and most severe hang.
// After the fix it is refused, the whole-tree drop is NAMED (never silent), the walk
// stays BOUNDED, and no error leaks the server-side path.
func TestParse_WriterlessFIFOAtSubsystemsDir_BoundedAndNamed(t *testing.T) {
	dir := t.TempDir()
	plantFIFO(t, filepath.Join(dir, "Subsystems"))

	subs, warnings := parseBounded(t, dir)
	if len(subs) != 0 {
		t.Errorf("a FIFO at the Subsystems position yields no subsystems; got %v", flattenNames(subs))
	}
	if !warningsContain(warnings, "каталог подсистем") {
		t.Errorf("the refused Subsystems catalog must be NAMED in a warning; warnings=%v", warnings)
	}
}

// DP-2 (nested recursion directory position): a valid Hierarchical parent whose
// child-recursion directory Subsystems/<Name>/Subsystems is a writer-less FIFO must
// not block the walk. walkHierarchical recurses into that directory unconditionally
// after parsing the parent, so an unguarded open there hangs the walk exactly like
// the top position. After the fix the parent still parses, the refused recursion
// directory is NAMED (by its parent), and the walk stays BOUNDED.
func TestWalkHierarchical_WriterlessFIFOAtRecursionDir_BoundedAndNamed(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Parent.xml"), secSubBody("Parent"))
	plantFIFO(t, filepath.Join(dir, "Subsystems", "Parent", "Subsystems"))

	subs, warnings := parseBounded(t, dir)
	names := flattenNames(subs)
	if !containsStr(names, "Parent") {
		t.Errorf("the valid parent must still parse; names=%v", names)
	}
	if !warningsContain(warnings, "Parent") {
		t.Errorf("the refused recursion directory must be NAMED (by its parent); warnings=%v", warnings)
	}
}

// DP-3 (Ext intermediate directory position): a writer-less FIFO named Ext at
// Subsystems/<N>/Ext. This position is reached only by an lstat THROUGH the FIFO
// (Subsystems/<N>/Ext/Subsystem.xml), which returns ENOTDIR without ever opening the
// FIFO, so it never blocked; this test documents that audit finding and guards it
// from regressing. A valid Ext sibling must still parse and the FIFO child is NAMED.
func TestWalkExt_WriterlessFIFOAtExtDir_BoundedAndNamed(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Good", "Ext", "Subsystem.xml"), secSubBody("Good"))
	plantFIFO(t, filepath.Join(dir, "Subsystems", "Blk", "Ext"))

	subs, warnings := parseBounded(t, dir)
	names := flattenNames(subs)
	if !containsStr(names, "Good") {
		t.Errorf("the valid Ext sibling must still parse; names=%v", names)
	}
	if containsStr(names, "Blk") {
		t.Errorf("the FIFO-Ext child must not parse; names=%v", names)
	}
	if !warningsContain(warnings, "Blk") {
		t.Errorf("the FIFO-Ext child must be NAMED; warnings=%v", warnings)
	}
}

// DP-4 (applied-kind folder position, universe enumerator): a writer-less FIFO named
// like an applied-kind folder (Documents). EnumerateAppliedObjects only descends
// directory entries (DirEntry.IsDir gate), so a FIFO entry is skipped before any
// open; this documents that audit finding, and the hardened directory open is the
// defense-in-depth backstop. A valid applied object must still enumerate, bounded.
func TestEnumerateAppliedObjects_WriterlessFIFOFolder_Bounded(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Catalogs", "Валюты.xml"), objBody("Валюты"))
	plantFIFO(t, filepath.Join(dir, "Documents"))

	got := enumBounded(t, dir)
	if !containsStr(got, "Справочник.Валюты") {
		t.Errorf("the valid applied object must still enumerate; got %v", got)
	}
}
