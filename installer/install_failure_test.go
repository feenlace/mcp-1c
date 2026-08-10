package installer

import (
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/extension"
)

// ---------------------------------------------------------------------------
// What a failed apply leaves behind has to be said, because the customer cannot
// see it and guesses wrong in both directions.
//
// "Installation error: updating database config: ..." is true and incomplete.
// The extension IS loaded; only the apply failed. On a fresh install that means
// the extension is present and dead. On an update it means the OLD version is
// still applied and still serving requests while the new one sits in the main
// configuration. The customer who reported this read the error, concluded
// «обновилось всё-таки», and kept running the old code.
//
// The note belongs to the apply leg only. When the LOAD fails, nothing was
// loaded, and telling that customer the new version is loaded but not applied
// would be a second false statement in place of the first. The two halves are
// asserted with the SAME expression below, once expecting true and once
// expecting false, so neither is a check that cannot fail.
// ---------------------------------------------------------------------------

func TestInstallSaysWhatAFailedApplyLeavesBehind(t *testing.T) {
	// The note is read out of production, never copied, so the test cannot
	// agree with a stale version of the text.
	note := notAppliedNote(false)
	if strings.Count(note, "\n") < 3 {
		t.Fatalf("the note is %d lines. It has to state the fact and then what to do, and a "+
			"one-line note cannot do both", strings.Count(note, "\n")+1)
	}
	// The register is fixed by the rest of the installer's Russian output: no
	// dash and no тире anywhere in customer-facing text.
	for _, dash := range []string{"—", "–", "‒", "―"} {
		if strings.Contains(note, dash) {
			t.Errorf("the note contains %q", dash)
		}
	}

	t.Run("apply exhausted: the note is attached to the error", func(t *testing.T) {
		dir := newFakeDesigner(t, fakeModeRunModeAlways)
		exe := fakePlatformExe(t)

		var err error
		out := captureStdout(t, func() {
			err = Install(extension.Source, `C:\base`, false, exe, "", "", shippedPlatform)
		})
		if err == nil {
			t.Fatalf("Install reported success while /UpdateDBCfg refused every time\nstdout:\n%s", out)
		}

		// The retry really was exhausted first, so this is the end of the ladder
		// and not a shortcut past it.
		wantOps := []string{"/LoadConfigFromFiles", "/UpdateDBCfg", "/LoadConfigFromFiles", "/UpdateDBCfg"}
		gotOps := callOps(fakeCalls(t, dir))
		if strings.Join(gotOps, ",") != strings.Join(wantOps, ",") {
			t.Fatalf("DESIGNER call sequence\ngot:  %v\nwant: %v", gotOps, wantOps)
		}

		if !strings.Contains(err.Error(), note) {
			t.Errorf("the apply leg gave up and said nothing about what it left behind.\ngot:\n%s", err)
		}

		// What the customer actually reads, in full.
		t.Logf("Installation error: %v", err)

		// Nothing is swallowed: DESIGNER's own words are still there for whoever
		// has to diagnose the base.
		for _, keep := range []string{"updating database config", "ОсновнойРежимЗапуска"} {
			if !strings.Contains(err.Error(), keep) {
				t.Errorf("the note replaced the diagnosis instead of adding to it: %q is gone from\n%s",
					keep, err)
			}
		}
	})

	t.Run("load failed: the note is absent, because nothing was loaded", func(t *testing.T) {
		dir := newFakeDesigner(t, fakeModeLoadFails)
		exe := fakePlatformExe(t)

		var err error
		out := captureStdout(t, func() {
			err = Install(extension.Source, `C:\base`, false, exe, "", "", shippedPlatform)
		})
		if err == nil {
			t.Fatalf("Install reported success while /LoadConfigFromFiles refused\nstdout:\n%s", out)
		}

		// The apply leg was never reached, so there is nothing loaded to strand.
		gotOps := callOps(fakeCalls(t, dir))
		if strings.Join(gotOps, ",") != "/LoadConfigFromFiles" {
			t.Fatalf("expected the run to stop at the load, got %v", gotOps)
		}

		if strings.Contains(err.Error(), note) {
			t.Errorf("the load failed, so «загружено в конфигурацию, но не применено» is false, "+
				"and the installer stated it anyway.\ngot:\n%s", err)
		}
		if !strings.Contains(err.Error(), "loading extension config") {
			t.Errorf("the load failure lost its own diagnosis:\n%s", err)
		}
	})
}
