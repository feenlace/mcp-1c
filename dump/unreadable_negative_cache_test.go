package dump

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A FILE THAT CANNOT BE OPENED COST 50 ms EVERY TIME IT WAS ASKED FOR.
//
// readModuleContent retries once after readRetryDelay, because a module file on
// Windows is routinely locked for a moment by antivirus, cloud-sync or the OS
// search indexer, and the retry is what turns that moment into a served file. What
// was missing is the other end: nothing recorded that the retry had already been
// spent, so the pause was re-paid on every call, for every caller, for as long as
// the file stayed locked.
//
// THE THREE ARMS ARE NOT ALIKE, and only one of them is expensive. Measured on
// this tree with a 3-module fixture, per call:
//
//	readable, served from cache   2.30 µs
//	DELETED file                 44.33 µs
//	PRESENT but unopenable       50.66 ms
//
// A deleted file never reaches the retry: GetContent resolves the path through
// EvalSymlinks for the dump-root containment check, that resolution fails for a
// file that is not there, and the call is refused before any read is attempted. So
// the negative cache below records ONLY the arm that pays the pause. Recording the
// missing-file arm too would restrict when a restored file is served again, in
// exchange for saving 44 µs.
//
// THE SET IS PER GENERATION, which is the owner's decision and not an accident: a
// file that becomes readable again is not served until the dump is reloaded, and
// reload_dump is the remedy the product already offers for a dump that moved.

// lockedModuleFixture writes three modules and returns the index plus the paths of
// the one to delete and the one to make unopenable.
func lockedModuleFixture(t *testing.T) (idx *Index, delPath, lockPath string) {
	t.Helper()
	root := t.TempDir()
	mk := func(name string) string {
		d := filepath.Join(root, "CommonModules", name, "Ext")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(d, "Module.bsl")
		if err := os.WriteFile(p, []byte("Процедура П()\n\tСообщить(\"игла\");\nКонецПроцедуры\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk("Хороший")
	delPath = mk("Удалённый")
	lockPath = mk("Закрытый")

	var err error
	idx, err = NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	return idx, delPath, lockPath
}

const (
	keyReadable   = "ОбщийМодуль.Хороший.Модуль"
	keyDeleted    = "ОбщийМодуль.Удалённый.Модуль"
	keyUnopenable = "ОбщийМодуль.Закрытый.Модуль"
)

// lockFile makes path unopenable, or skips the test when the platform refuses to
// cooperate. It never reports success it did not achieve.
func lockFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skipf("cannot remove read permission: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	f, err := os.Open(path)
	if err == nil {
		f.Close()
		t.Skip("the file is still openable after chmod 000 (running as root?), so the " +
			"expensive arm cannot be constructed here")
	}
}

// TestAnUnopenableModuleIsRefusedOnceNotOnEveryCall.
func TestAnUnopenableModuleIsRefusedOnceNotOnEveryCall(t *testing.T) {
	idx, _, lockPath := lockedModuleFixture(t)
	lockFile(t, lockPath)

	// THE CONTROL, and it runs FIRST because everything below is only meaningful
	// if the first call really did pay the retry. If it did not, this fixture is
	// not the expensive arm and a fast second call would prove nothing.
	start := time.Now()
	if _, ok := idx.GetContent(keyUnopenable); ok {
		t.Fatal("an unopenable module was served")
	}
	first := time.Since(start)
	if first < readRetryDelay {
		t.Fatalf("the first call took %v, less than the %v retry pause: this fixture is not "+
			"the present-but-unopenable arm, so the measurement below is about something else",
			first, readRetryDelay)
	}

	// Twenty further calls must together cost less than ONE retry pause. Without
	// the negative set they would cost twenty of them.
	const repeats = 20
	start = time.Now()
	for i := 0; i < repeats; i++ {
		if _, ok := idx.GetContent(keyUnopenable); ok {
			t.Fatalf("call %d served an unopenable module", i)
		}
	}
	rest := time.Since(start)
	if rest >= readRetryDelay {
		t.Errorf("%d further calls took %v, which is at least the %v of a SINGLE retry pause; "+
			"the refusal is being re-derived from disk every time (unmodified behaviour would "+
			"cost about %v here)", repeats, rest, readRetryDelay, repeats*readRetryDelay)
	}
}

// TestAReloadForgetsTheUnreadableSetEvenWhenNothingOnDiskChanged is the one that
// decides where the set may be cleared.
//
// Clearing it only where a generation is SWAPPED is not enough, and this is not a
// theoretical gap. Making a file readable again changes neither its modification
// time nor its size, GenSig hashes exactly those two per .bsl, so the signature is
// unchanged and Reload returns at its "nothing on disk moved" check WITHOUT
// building or swapping anything. A set cleared only in swapGeneration would
// therefore survive the very command the product tells the user to run, and the
// file would stay refused for the life of the process.
func TestAReloadForgetsTheUnreadableSetEvenWhenNothingOnDiskChanged(t *testing.T) {
	idx, _, lockPath := lockedModuleFixture(t)

	// ATTACH A GENERATION FIRST. A freshly built index carries no generation
	// signature (ReloadReport.SigBefore documents the empty case), so its FIRST
	// reload always rebuilds and always swaps — which is the path this test is
	// explicitly not about. One reload up front puts a signature on the index so
	// the reload that matters can take the unchanged-signature early return.
	if _, err := idx.Reload(); err != nil {
		t.Fatalf("priming Reload: %v", err)
	}

	lockFile(t, lockPath)

	if _, ok := idx.GetContent(keyUnopenable); ok {
		t.Fatal("an unopenable module was served")
	}

	// The file recovers. Nothing else about it moves.
	sigBefore, err := GenSig(idx.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o644); err != nil {
		t.Fatal(err)
	}
	sigAfter, err := GenSig(idx.dir)
	if err != nil {
		t.Fatal(err)
	}
	if sigAfter != sigBefore {
		t.Fatalf("premise moved: restoring the permission changed the dump signature "+
			"(%s -> %s), so this test no longer exercises the unchanged-signature path",
			sigBefore, sigAfter)
	}

	// Still refused, because the set is per generation and no reload has happened.
	if _, ok := idx.GetContent(keyUnopenable); ok {
		t.Error("the module was served before any reload; the set is supposed to hold " +
			"until the dump is reloaded")
	}

	rep, err := idx.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// THE PREMISE, and the whole point of the test: this reload did NOT swap a
	// generation. If it had, clearing the set in swapGeneration alone would be
	// enough and nothing here would be testing the early return.
	if rep.Changed || rep.Rebuilt {
		t.Fatalf("premise moved: the reload built or swapped a generation "+
			"(Changed=%v Rebuilt=%v), so it did not take the unchanged-signature early return",
			rep.Changed, rep.Rebuilt)
	}
	if rep.SigBefore == "" || rep.SigBefore != rep.SigAfter {
		t.Fatalf("premise moved: SigBefore=%q SigAfter=%q, want a non-empty pair that matches",
			rep.SigBefore, rep.SigAfter)
	}

	if _, ok := idx.GetContent(keyUnopenable); !ok {
		t.Error("after reload_dump the module is STILL refused: the remedy the product " +
			"offers does not reach the negative set, so a recovered file is masked forever")
	}
}

// TestTheNegativeSetNeverRefusesAReadableModule is the control that the set
// discriminates at all. A set that refused everything would satisfy every
// assertion above.
func TestTheNegativeSetNeverRefusesAReadableModule(t *testing.T) {
	idx, _, lockPath := lockedModuleFixture(t)
	lockFile(t, lockPath)

	for i := 0; i < 5; i++ {
		if _, ok := idx.GetContent(keyUnopenable); ok {
			t.Fatal("an unopenable module was served")
		}
		content, ok := idx.GetContent(keyReadable)
		if !ok {
			t.Fatalf("call %d: the READABLE module beside it was refused", i)
		}
		if content == "" {
			t.Fatalf("call %d: the readable module came back empty", i)
		}
	}
}

// TestADeletedModuleIsServedAgainAsSoonAsItReturns pins the scoping decision.
//
// The missing-file arm is refused by the containment check for about 44 µs and
// never reaches the retry, so it is deliberately NOT recorded. Recording it would
// mean a file restored by the user is not served until a reload, which is a
// restriction bought with no saving.
func TestADeletedModuleIsServedAgainAsSoonAsItReturns(t *testing.T) {
	idx, delPath, _ := lockedModuleFixture(t)

	body, err := os.ReadFile(delPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(delPath); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.GetContent(keyDeleted); ok {
		t.Fatal("a deleted module was served")
	}
	if err := os.WriteFile(delPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.GetContent(keyDeleted); !ok {
		t.Error("a module that came back is refused without a reload; the missing-file arm " +
			"is not supposed to be recorded in the negative set")
	}
}

// TestTheScanPathAlsoStopsRePayingTheRetry. Regex and exact search stream every
// candidate through contentForScan, so an unopenable module costs the retry once
// per candidate per scan there — the same defect on the other read path, and worse
// because a scan visits every module.
func TestTheScanPathAlsoStopsRePayingTheRetry(t *testing.T) {
	idx, _, lockPath := lockedModuleFixture(t)
	lockFile(t, lockPath)

	// First scan pays the retry for the locked module.
	start := time.Now()
	if _, _, err := idx.SearchWithStats(SearchParams{
		Query: "игла", Mode: SearchModeExact, Limit: 50,
	}); err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)
	if first < readRetryDelay {
		t.Fatalf("the first scan took %v, less than the %v retry pause: the locked module "+
			"was not reached by the scan, so the measurement below is about something else",
			first, readRetryDelay)
	}

	const repeats = 10
	start = time.Now()
	for i := 0; i < repeats; i++ {
		if _, _, err := idx.SearchWithStats(SearchParams{
			Query: "игла", Mode: SearchModeExact, Limit: 50,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rest := time.Since(start)
	if rest >= readRetryDelay {
		t.Errorf("%d further scans took %v, at least the %v of a SINGLE retry pause; the scan "+
			"path is re-paying it per scan", repeats, rest, readRetryDelay)
	}
}
