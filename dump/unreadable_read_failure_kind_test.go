package dump

import (
	"errors"
	"os"
	"path/filepath"
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
