package dump

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
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
		// THE FOUR THE TWO-ERRNO DENYLIST RECORDED AS FACTS ABOUT THE FILE. Each is a
		// state of the process, the kernel or the transport under the mount, and each
		// clears without anything about the file changing, so a per-key verdict outlives
		// the condition that produced it.
		{"ENOMEM, the kernel could not allocate", wrap(syscall.ENOMEM), false},
		{"EINTR, the read was interrupted", wrap(syscall.EINTR), false},
		{"ETIMEDOUT, the mount under the file did not answer", wrap(syscall.ETIMEDOUT), false},
		{"EAGAIN, the read would have blocked", wrap(syscall.EAGAIN), false},
		{"ESTALE, the handle went stale and a remount clears it", wrap(syscall.ESTALE), false},
		{"EACCES, the file's permissions", syscall.EACCES, true},
		{"EACCES wrapped by os.ReadFile", wrap(syscall.EACCES), true},
		{"EPERM", wrap(syscall.EPERM), true},
		{"EIO, the storage under the file", wrap(syscall.EIO), true},
		{"ENOENT, the file is not there", wrap(syscall.ENOENT), true},
		{"ENOTDIR, a component of the path is not a directory", wrap(syscall.ENOTDIR), true},
		// UNKNOWN STILL RECORDS, and that is the denylist's default kept on purpose:
		// see readFailureSaysSomethingAboutTheFile for why this stayed a denylist
		// rather than becoming an allowlist.
		{"an error this package cannot classify", errors.New("что-то пошло не так"), true},
	}
	for _, c := range cases {
		if got := readFailureSaysSomethingAboutTheFile(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestTheTransientClassIsTheStandardLibrarysAndNotACopyOfIt pins the DELEGATION
// rather than the list, which is the whole reason the predicate stops enumerating.
//
// syscall.Errno.Temporary() is maintained per platform by the standard library and
// already names the class this set must never record: EINTR, EMFILE, EAGAIN,
// EWOULDBLOCK, ETIMEDOUT, and ENFILE on unix. A predicate that copied those names
// into this package would answer a question the toolchain has already answered, and
// would answer it out of date the first time upstream adds one.
//
// THE PROPERTY IS ONE-DIRECTIONAL. Temporary() being true must imply the set does
// not record it. The converse is not asserted and must not be: ENFILE on windows
// and ENOMEM everywhere are excluded here while Temporary() says nothing about
// them, which is exactly why the predicate has arms of its own as well.
func TestTheTransientClassIsTheStandardLibrarysAndNotACopyOfIt(t *testing.T) {
	probes := []syscall.Errno{
		syscall.EINTR, syscall.EMFILE, syscall.ENFILE, syscall.EAGAIN,
		syscall.ETIMEDOUT, syscall.ENOMEM, syscall.ESTALE,
		syscall.EACCES, syscall.EPERM, syscall.EIO, syscall.ENOENT,
	}
	// CONTROL: at least one probe IS Temporary() on this platform, or the implication
	// below is vacuously true and measures nothing.
	temporaries := 0
	for _, e := range probes {
		if e.Temporary() {
			temporaries++
		}
	}
	if temporaries == 0 {
		t.Fatalf("control failed: none of the %d probes is Temporary() on %s, so the "+
			"implication below is vacuous", len(probes), runtime.GOOS)
	}
	for _, e := range probes {
		if e.Temporary() && readFailureSaysSomethingAboutTheFile(&os.PathError{Op: "open", Err: e}) {
			t.Errorf("%v is Temporary() and the negative set records it as a fact about the "+
				"file, so a condition the standard library calls transient becomes a verdict "+
				"that lasts until reload_dump", e)
		}
	}
}

// TestTheNamedArmAnswersWithoutAskingTemporary pins the entries the predicate
// keeps for itself, on the arm that decides them.
//
// WHY THE ARM AND NOT THE PREDICATE. readFailureSaysSomethingAboutTheFile asks
// Temporary() first, and unix Temporary() already returns true for ENFILE. So on
// every machine this project's tests run on, the ENFILE entry in
// namedTransientErrnos changes NO result of the full predicate: it was measured,
// and deleting it left the whole dump suite green. It is not dead code though -
// windows Temporary() is EINTR || EMFILE || Timeout, so on Windows that entry is
// the only thing keeping a machine-wide descriptor shortage out of a per-key
// verdict that lasts until reload_dump. isNamedTransient is the same code on both
// platforms, so calling it directly tests the Windows decision from here.
//
// This does not test Windows. No test in this project runs on Windows; the one
// test job in .github/workflows/release.yml is ubuntu-latest and windows appears
// only in the build matrix. It tests the arm Windows relies on.
func TestTheNamedArmAnswersWithoutAskingTemporary(t *testing.T) {
	wrap := func(e error) error { return &os.PathError{Op: "open", Path: "Module.bsl", Err: e} }

	// CONTROL: this is the state that makes the test necessary. On a platform whose
	// Temporary() already covers ENFILE, the full predicate cannot see the entry, so
	// a test written against the predicate would pass with the entry deleted.
	if syscall.ENFILE.Temporary() {
		if readFailureSaysSomethingAboutTheFile(wrap(syscall.ENFILE)) {
			t.Fatalf("control failed: ENFILE is Temporary() on %s and the predicate still "+
				"records it, so neither arm is behaving as this test assumes", runtime.GOOS)
		}
	}

	for _, e := range []syscall.Errno{syscall.ENFILE, syscall.ENOMEM, syscall.ESTALE} {
		if !isNamedTransient(wrap(e)) {
			t.Errorf("%v is not matched by the named arm. On Windows that arm is the only "+
				"thing standing between this errno and a per-key verdict that lasts until "+
				"reload_dump, and no run on this platform would notice through the full "+
				"predicate.", e)
		}
	}

	// NEGATIVE CONTROL: the arm is not simply answering true. Without this a list
	// emptied to nothing, or a loop that returned true unconditionally, would pass
	// every assertion above.
	for _, e := range []syscall.Errno{syscall.EACCES, syscall.EIO, syscall.ENOENT} {
		if isNamedTransient(wrap(e)) {
			t.Errorf("%v is matched by the named arm, so the arm no longer distinguishes a "+
				"fact about the file from a state of the machine", e)
		}
	}
	if isNamedTransient(errors.New("что-то пошло не так")) {
		t.Errorf("an error carrying no errno is matched by the named arm")
	}
}
