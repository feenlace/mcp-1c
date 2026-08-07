//go:build unix

package dump

import (
	"os"
	"syscall"
	"testing"
)

// exhaustDescriptors lowers this process's open-file limit below what it is
// already using, so the next open fails with EMFILE. It reports whether the limit
// could be lowered at all; restoreDescriptors puts it back.
//
// LOWERING THE LIMIT DOES NOT CLOSE THE DESCRIPTORS ALREADY OPEN, which is what
// makes this usable inside a test with a built index: everything that is open
// stays open and only NEW opens are refused. The window is therefore exactly the
// reads the caller makes between the two calls.
//
// THE FILE CARRIES //go:build unix AND NOT A GOOS SUFFIX IN ITS NAME. A test file
// named for a platform is excluded by the toolchain without a word, and a guard
// that is silently not compiled is worse than no guard; the name here is inert and
// the constraint is explicit.
// LOWERING THE LIMIT IS NOT ENOUGH BY ITSELF, which is a measurement and not a
// guess. The kernel refuses a descriptor whose NUMBER reaches the limit, and it
// hands out the lowest free number, so with the limit at 8 an open still succeeds
// while any of 0..7 is free. MEASURED on darwin: with RLIMIT_NOFILE lowered from
// 122880 to 8, os.Open("/dev/null") returned a nil error. The free slots below the
// limit therefore have to be taken up before the shortage is real, which is what
// the loop does.
var (
	savedRlimit syscall.Rlimit
	heldFDs     []*os.File
)

func exhaustDescriptors(t *testing.T) bool {
	t.Helper()
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &savedRlimit); err != nil {
		t.Logf("Getrlimit: %v", err)
		return false
	}
	lowered := savedRlimit
	lowered.Cur = 8 // far below what a Go test process already holds
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lowered); err != nil {
		t.Logf("Setrlimit down: %v", err)
		return false
	}
	// Bounded by the limit itself: there can be no more free slots than that.
	for i := 0; i < int(lowered.Cur)+1; i++ {
		f, err := os.Open(os.DevNull)
		if err != nil {
			return true // the next open is the one the test wants to fail
		}
		heldFDs = append(heldFDs, f)
	}
	restoreDescriptors(t)
	t.Log("every open still succeeded at the lowered limit, so no shortage was produced")
	return false
}

func restoreDescriptors(t *testing.T) {
	t.Helper()
	for _, f := range heldFDs {
		_ = f.Close()
	}
	heldFDs = nil
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &savedRlimit); err != nil {
		t.Fatalf("Setrlimit up: %v (the process is left with a lowered descriptor limit)", err)
	}
}
