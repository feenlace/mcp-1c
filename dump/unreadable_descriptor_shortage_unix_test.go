//go:build unix

package dump

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// THE PROBE AND EVERYTHING THAT CALLS IT LIVE UNDER ONE CONSTRAINT, and that is
// what this file is for. The helpers below need RLIMIT_NOFILE, which is why they
// are behind `//go:build unix`; their two callers used to sit in an unconstrained
// file, so for GOOS=windows the helper was dropped and the callers were not, and
// `go vet` answered `undefined: exhaustDescriptors` against
// unreadable_read_failure_kind_test.go. Nothing local ever saw it: windows is a
// released target that nothing in this repository type-checked the test tree for,
// and there `go test ./...` was a hard failure while every developer machine and
// the ubuntu CI job stayed green. A helper and its callers now move together or not
// at all, and TestEveryTestFileCompilesForWindows type-checks the whole module for
// windows so the next split is a red test rather than a customer's build.
//
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

// TestADescriptorShortageDoesNotCondemnTheModule drives the decision through the
// real cache rather than through the predicate, on the path a search takes.
//
// IT USES THE PROCESS'S OWN DESCRIPTOR LIMIT, which is the only way to make a real
// EMFILE: the limit is a property of the process, so there is nothing narrower to
// lower. The window is one read long and the limit is restored in a defer.
//
// IT COVERS ONE OF THE TWO CALL SITES. Index.noteUnreadable is called from exactly
// two places, contentForScan and GetContent, each behind its own copy of the
// readFailureSaysSomethingAboutTheFile condition. This test drives the first;
// TestADescriptorShortageDoesNotCondemnTheModuleOnTheGetContentPath drives the
// second, because a guard written twice can be deleted once.
//
// WHAT IT WOULD CATCH THAT IT WOULD NOT CATCH IF THE FIX WERE REVERTED: a
// momentary «too many open files» removes the module from every mode until
// reload_dump, though the file was never even opened to find out.
func TestADescriptorShortageDoesNotCondemnTheModule(t *testing.T) {
	idx, _, _ := lockedModuleFixture(t)
	const key = "ОбщийМодуль.Хороший.Модуль"

	// CONTROL 1: the module is readable and is served, so anything below is the
	// shortage's doing.
	if _, ok := idx.contentForScan(key); !ok {
		t.Fatalf("control failed: %s is not served even with every descriptor available", key)
	}

	// AND THE CACHED COPY IS SENT BACK TO DISK. contentForScan serves the build-time
	// copy while its (mtime, size) stamp still matches, so without this the call
	// under the shortage never reaches a read and the test measures nothing. Moving
	// the mtime is done BEFORE the limit is lowered, because it is itself a syscall
	// on the path.
	idx.mu.RLock()
	path := idx.pathByName[key]
	idx.mu.RUnlock()
	if path == "" {
		t.Fatalf("control failed: %s has no path in the index", key)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if !exhaustDescriptors(t) {
		t.Skip("could not produce a descriptor shortage on this platform, so nothing " +
			"would be measured")
	}

	// CONTROL 2: the read really did fail while the limit was down. Without it a
	// platform that kept serving the file would make the assertion below pass on an
	// answer that never had a failure to classify.
	_, ok := idx.contentForScan(key)
	restoreDescriptors(t)
	if ok {
		t.Skip("the read succeeded despite the lowered descriptor limit, so no shortage " +
			"was produced and nothing is measured")
	}

	if idx.refusedAsUnreadable(key) {
		t.Errorf("a descriptor shortage put %s in the negative set. The file was never "+
			"opened, so nothing was learned about it, and every mode now refuses it "+
			"until the dump is reloaded.", key)
	}
	// AND THE MODULE IS SERVED AGAIN AS SOON AS THERE ARE DESCRIPTORS, which is the
	// customer-visible half: before the set existed the next call simply retried.
	if _, ok := idx.contentForScan(key); !ok {
		t.Errorf("%s is still refused after the descriptor shortage cleared", key)
	}
}

// TestADescriptorShortageDoesNotCondemnTheModuleOnTheGetContentPath is the twin of
// the test above on the OTHER call site, and it exists because the guard is
// written twice and was measured once.
//
// THE TWO SITES ARE NOT ONE SITE. noteUnreadable is reached from contentForScan,
// which the regex/exact scan runs over every candidate, and from GetContent, which
// is the exported content read: inside this module its only production caller is
// searchSmart, which builds the smart leg's body from it, and it is what importers
// of this module read module source through. Each carries its own
// `if readFailureSaysSomethingAboutTheFile(rerr)`. Deleting the one in GetContent
// left the whole dump package green: the predicate test above still passed because
// the predicate was untouched, and the scan test still passed because it never
// enters GetContent. The entry that deletion writes is not local to the call
// either, since the set is keyed by module id and shared, so a burst inside a
// GetContent condemns the id for the SCAN as well, until reload.
//
// THE FIXTURE IS THE SAME SHORTAGE, driven through GetContent instead. The cached
// copy has to be sent back to disk first, and here that takes an explicit read
// BEFORE the mtime is moved: GetContent caches what it reads, contentForScan does
// not, so control 1 is what puts the entry in contentByName and the Chtimes is
// what makes the entry stale enough to force the re-read the shortage then fails.
func TestADescriptorShortageDoesNotCondemnTheModuleOnTheGetContentPath(t *testing.T) {
	idx, _, _ := lockedModuleFixture(t)
	const key = "ОбщийМодуль.Хороший.Модуль"

	// CONTROL 1: the module is readable and is served by THIS path, so anything
	// below is the shortage's doing.
	if _, ok := idx.GetContent(key); !ok {
		t.Fatalf("control failed: %s is not served by GetContent even with every "+
			"descriptor available", key)
	}

	idx.mu.RLock()
	path := idx.pathByName[key]
	idx.mu.RUnlock()
	if path == "" {
		t.Fatalf("control failed: %s has no path in the index", key)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// CONTROL 2: moving the mtime really does defeat the cache, or the call under
	// the shortage would be served from memory and never reach a read at all. This
	// is the fixture trap the chmod-only version of this test fell into.
	idx.contentMu.RLock()
	entry, cached := idx.contentByName[key]
	idx.contentMu.RUnlock()
	if !cached {
		t.Fatalf("control failed: GetContent did not cache %s, so there is no cache "+
			"for the Chtimes to invalidate and this fixture measures something else", key)
	}
	if stamp, ok := statStamp(path); ok && stamp == entry.stamp {
		t.Fatalf("control failed: the cached stamp still matches the file after Chtimes, " +
			"so the next GetContent is served from memory and never opens anything")
	}

	if !exhaustDescriptors(t) {
		t.Skip("could not produce a descriptor shortage on this platform, so nothing " +
			"would be measured")
	}

	// CONTROL 3: the read really did fail while the limit was down.
	_, ok := idx.GetContent(key)
	restoreDescriptors(t)
	if ok {
		t.Skip("the read succeeded despite the lowered descriptor limit, so no shortage " +
			"was produced and nothing is measured")
	}

	if idx.refusedAsUnreadable(key) {
		t.Errorf("a descriptor shortage inside GetContent put %s in the negative set. The "+
			"file was never opened, so nothing was learned about it, and the set is shared "+
			"with the scan, so every mode now refuses it until the dump is reloaded.", key)
	}
	// AND THE MODULE IS SERVED AGAIN AS SOON AS THERE ARE DESCRIPTORS.
	if _, ok := idx.GetContent(key); !ok {
		t.Errorf("%s is still refused by GetContent after the descriptor shortage cleared", key)
	}
}
