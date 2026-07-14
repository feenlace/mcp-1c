//go:build unix

package dump

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// plantFIFO creates a writer-less named pipe at path (unix only). A reader that
// os.Open's it while no writer is present blocks forever, so the subsystem reader
// must refuse a non-regular final component BEFORE opening it.
func plantFIFO(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
}

// parseBounded runs ParseAllSubsystemsCtx in a goroutine and fails the test if it
// does not return within a short deadline. A writer-less FIFO that reached os.Open
// would block the walk forever (a DoS that ctx cancellation cannot interrupt), so a
// timeout here IS the reproduction. All t.Fatal calls stay on the test goroutine.
func parseBounded(t *testing.T, dir string) ([]Subsystem, []string) {
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
		if r.err != nil {
			t.Fatalf("ParseAllSubsystemsCtx err = %v, want nil", r.err)
		}
		return r.subs, r.warnings
	case <-time.After(10 * time.Second):
		t.Fatal("ParseAllSubsystemsCtx did not return: a non-regular subsystem file blocked the parse (DoS)")
		return nil, nil
	}
}

// MB-1 (Ext layout): a writer-less FIFO planted where <N>/Ext/Subsystem.xml is
// expected must not block the parse. It must be refused with a NAMED, path-free
// warning, a valid sibling must still parse, and the walk must stay BOUNDED.
func TestWalkExt_WriterlessFIFO_BoundedAndNamed(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Good", "Ext", "Subsystem.xml"), secSubBody("Good"))
	plantFIFO(t, filepath.Join(dir, "Subsystems", "Blk", "Ext", "Subsystem.xml"))

	subs, warnings := parseBounded(t, dir)
	names := flattenNames(subs)
	if !containsStr(names, "Good") {
		t.Errorf("valid sibling Good must still parse; names=%v", names)
	}
	if containsStr(names, "Blk") {
		t.Errorf("FIFO Blk must not be parsed; names=%v", names)
	}
	if !warningsContain(warnings, "Blk") {
		t.Errorf("the refused FIFO subsystem must be NAMED in a warning; warnings=%v", warnings)
	}
}

// MB-1 (Hierarchical layout): a writer-less FIFO planted at Subsystems/<Name>.xml
// must not block the parse; same bounded + named guarantees.
func TestWalkHierarchical_WriterlessFIFO_BoundedAndNamed(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "Good.xml"), secSubBody("Good"))
	plantFIFO(t, filepath.Join(dir, "Subsystems", "Blk.xml"))

	subs, warnings := parseBounded(t, dir)
	names := flattenNames(subs)
	if !containsStr(names, "Good") {
		t.Errorf("valid sibling Good must still parse; names=%v", names)
	}
	if containsStr(names, "Blk") {
		t.Errorf("FIFO Blk must not be parsed; names=%v", names)
	}
	if !warningsContain(warnings, "Blk") {
		t.Errorf("the refused FIFO subsystem must be NAMED in a warning; warnings=%v", warnings)
	}
}
