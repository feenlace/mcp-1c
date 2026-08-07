package dump

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// WHAT THE NEGATIVE SET IS ALLOWED TO CONCLUDE FROM A FAILED READ.
//
// The set records «this key names a file that is there and cannot be opened», and
// it was written from ANY failure of readModuleContent. Two of those failures are
// not about the file at all: EMFILE is «this process has no descriptor left» and
// ENFILE is «this machine has none left». The path in such an error is whichever
// open happened to be the one that ran out, and the scan opens candidates in
// parallel chunks, so one burst can condemn a whole chunk of the index for the
// rest of the generation over a condition that cleared immediately.

// TestOnlyAFailureAboutTheFileReachesTheNegativeSet pins the decision itself.
//
// THE SHAPE OF THE ERROR IS MEASURED AND NOT ASSUMED, which is what the first
// subtest is for: it takes a real refusal from the operating system and shows that
// errors.Is sees the errno through the *os.PathError os.ReadFile returns. Without
// it the table below would be a test of constants this package made up.
func TestOnlyAFailureAboutTheFileReachesTheNegativeSet(t *testing.T) {
	t.Run("the errno really is visible through what os.ReadFile returns", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "Module.bsl")
		if err := os.WriteFile(p, []byte("Процедура П()\nКонецПроцедуры\n"), 0o000); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := os.ReadFile(p)
		if err == nil {
			t.Skip("this process can read a mode 000 file (running as root), so no real " +
				"refusal can be produced here")
		}
		var pe *os.PathError
		if !errors.As(err, &pe) {
			t.Fatalf("control failed: os.ReadFile returned %T, not *os.PathError, so the "+
				"table below describes an error shape this package does not receive", err)
		}
		if !errors.Is(err, syscall.EACCES) {
			t.Fatalf("control failed: a permission refusal does not match syscall.EACCES "+
				"through errors.Is (%v), so errors.Is cannot be used to classify these at all", err)
		}
		if !readFailureSaysSomethingAboutTheFile(err) {
			t.Errorf("a permission refusal is a fact about the file and the set refuses to "+
				"record it, so a locked module re-pays readRetryDelay on every call")
		}
	})

	// The two shapes os.ReadFile produces: the bare errno and the *os.PathError it
	// is wrapped in. Both are asserted, because the call sites see the second and a
	// classifier that only handled the first would be silently inert.
	wrap := func(e error) error { return &os.PathError{Op: "open", Path: "Module.bsl", Err: e} }
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"EMFILE, this process is out of descriptors", syscall.EMFILE, false},
		{"EMFILE wrapped by os.ReadFile", wrap(syscall.EMFILE), false},
		{"ENFILE, the machine is out of descriptors", syscall.ENFILE, false},
		{"ENFILE wrapped by os.ReadFile", wrap(syscall.ENFILE), false},
		{"EACCES, the file's permissions", syscall.EACCES, true},
		{"EACCES wrapped by os.ReadFile", wrap(syscall.EACCES), true},
		{"EPERM", wrap(syscall.EPERM), true},
		{"EIO, the storage under the file", wrap(syscall.EIO), true},
		{"an error this package cannot classify", errors.New("что-то пошло не так"), true},
	}
	for _, c := range cases {
		if got := readFailureSaysSomethingAboutTheFile(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
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
// serves read_module, the smart leg's body and every other content read in the
// product. Each carries its own `if readFailureSaysSomethingAboutTheFile(rerr)`.
// Deleting the one in GetContent left the whole dump package green: the predicate
// test above still passed because the predicate was untouched, and the scan test
// still passed because it never enters GetContent. The entry that deletion writes
// is not local to the call either — the set is keyed by module id and shared, so a
// burst inside a read_module condemns the id for the SCAN as well, until reload.
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
		t.Fatalf("control failed: the cached stamp still matches the file after Chtimes, "+
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
