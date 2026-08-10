package installer

import (
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/extension"
)

// ---------------------------------------------------------------------------
// The note exists to stop the installer saying something false. Two runs made
// it say something false itself.
//
// B1. The base whose compat mode forbids extensions. installer.go already
// documents this one on compatModeNotFoundRe: LoadConfigFromFiles CLAIMS
// success and the extension is silently rejected, so UpdateDBCfg reports it
// missing. NOTHING is loaded. The classified message tells the customer the
// base forbids extensions; the note then told the same customer the extension
// is loaded into the configuration and that a previous version may still be
// serving. Two of its three conditional claims are false and the message
// contradicts itself four lines apart.
//
// B2. The run where Install deletes an installed extension and loads again.
// stdout prints «Removed old extension: MCP_HTTPService», and the note then
// promised that «продолжает работать прежняя его версия». We had just deleted
// it. Whether the deleted extension truly stops serving needs a real base; the
// self contradiction on the page does not.
//
// Both are checked with the SAME reading used in the opposite direction, so
// neither "absent" nor "present" is a verdict that cannot fail.
// ---------------------------------------------------------------------------

// noteSentencesIn reports which of the note's sentences appear in the text.
func noteSentencesIn(text string) map[string]bool {
	found := map[string]bool{}
	for _, s := range []string{
		notAppliedHead,
		notAppliedPreviousKeepsWorking,
		notAppliedFirstInstall,
		notAppliedPreviousDeleted,
		notAppliedAdvice,
	} {
		if strings.Contains(text, s) {
			found[s] = true
		}
	}
	return found
}

func TestNoteIsAbsentWhenTheBaseRejectsExtensionsOutright(t *testing.T) {
	// Positive control on the same reading: a run where the extension really is
	// loaded and unapplied must show the head sentence. Without it, "the note is
	// absent" is what a broken reader reports about every input.
	t.Run("control: a genuinely stranded load does carry the note", func(t *testing.T) {
		newFakeDesigner(t, fakeModeRunModeAlways)
		var err error
		captureStdout(t, func() {
			err = Install(extension.Source, `C:\base`, false, fakePlatformExe(t), "", "", shippedPlatform, false)
		})
		if err == nil {
			t.Fatal("expected the apply to fail")
		}
		if !noteSentencesIn(err.Error())[notAppliedHead] {
			t.Fatalf("the reading cannot find the head sentence where it belongs, so its verdict "+
				"elsewhere means nothing:\n%s", err)
		}
	})

	t.Run("compat mode forbids extensions: nothing was loaded", func(t *testing.T) {
		dir := newFakeDesigner(t, fakeModeCompatNotFound)
		var err error
		out := captureStdout(t, func() {
			err = Install(extension.Source, `C:\base`, false, fakePlatformExe(t), "", "", shippedPlatform, false)
		})
		if err == nil {
			t.Fatalf("expected the apply to fail\nstdout:\n%s", out)
		}

		// The run really is the documented one: the load reported success and
		// the apply reported the extension missing.
		wantOps := []string{"/LoadConfigFromFiles", "/UpdateDBCfg"}
		if gotOps := callOps(fakeCalls(t, dir)); strings.Join(gotOps, ",") != strings.Join(wantOps, ",") {
			t.Fatalf("call sequence\ngot:  %v\nwant: %v", gotOps, wantOps)
		}

		// The message that IS true must survive.
		if !strings.Contains(err.Error(), "режим совместимости конфигурации запрещает расширения") {
			t.Errorf("the classified compat-mode diagnosis is gone:\n%s", err)
		}

		// Not one sentence of the note may appear: the extension is not loaded,
		// there is no stranded new version and no previous version to speak of.
		for _, sentence := range []string{notAppliedHead, notAppliedPreviousKeepsWorking, notAppliedFirstInstall} {
			if strings.Contains(err.Error(), sentence) {
				t.Errorf("the base rejects extensions outright and nothing was loaded, but the note "+
					"claims otherwise. This contradicts the message it is appended to.\nclaim: %q\nfull:\n%s",
					sentence, err)
			}
		}
	})
}

func TestNoteDoesNotPromiseAVersionInstallJustDeleted(t *testing.T) {
	dir := newFakeDesigner(t, fakeModeExistsThenRunModeAlways)

	var err error
	out := captureStdout(t, func() {
		err = Install(extension.Source, `C:\base`, false, fakePlatformExe(t), "", "", shippedPlatform, false)
	})
	if err == nil {
		t.Fatalf("expected the apply to fail\nstdout:\n%s", out)
	}

	// The delete really fired, and we really told the customer so.
	wantOps := []string{
		"/LoadConfigFromFiles", "/ManageCfgExtensions", "/LoadConfigFromFiles",
		"/UpdateDBCfg", "/LoadConfigFromFiles", "/UpdateDBCfg",
	}
	if gotOps := callOps(fakeCalls(t, dir)); strings.Join(gotOps, ",") != strings.Join(wantOps, ",") {
		t.Fatalf("call sequence\ngot:  %v\nwant: %v\nstdout:\n%s", gotOps, wantOps, out)
	}
	if !strings.Contains(out, "Removed old extension: "+extensionName) {
		t.Fatalf("this run is supposed to be the one that deletes the installed extension, and stdout "+
			"does not say it did:\n%s", out)
	}

	found := noteSentencesIn(err.Error())

	// Still true: the new version was loaded and not applied.
	if !found[notAppliedHead] {
		t.Errorf("the head sentence is missing from a run that did strand a load:\n%s", err)
	}
	// The contradiction.
	if found[notAppliedPreviousKeepsWorking] {
		t.Errorf("stdout says «Removed old extension: %s» and the note then promises that the previous "+
			"version keeps working. Both cannot be true.\nfull:\n%s", extensionName, err)
	}
	// And what is true in its place.
	if !found[notAppliedPreviousDeleted] {
		t.Errorf("the run deleted the installed extension, and the note does not say so:\n%s", err)
	}
	// The "first install" conditional is equally wrong here: an extension was
	// installed, we removed it.
	if found[notAppliedFirstInstall] {
		t.Errorf("the note calls this a first install on a run that deleted an installed extension:\n%s", err)
	}
}
