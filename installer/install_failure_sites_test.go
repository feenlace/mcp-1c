package installer

import (
	"os"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/extension"
)

// ---------------------------------------------------------------------------
// Every apply-leg failure return, not one of them.
//
// The note was proven at a single call site. Deleting it from the other six,
// the default branch included, left the package green, which means six of the
// seven places the installer can give up were saying nothing about what they
// left behind and nothing noticed.
//
// So each site gets a run that reaches it, and the NUMBER of sites is derived
// from the source rather than typed here: an eighth call site added later
// either gets a scenario or this test fails and says which count moved. The
// derivation walks every non-test file of the package, so a site added in a
// sibling file counts too.
//
// Two of the seven can only be reached by making a file unwritable, because
// they are the strip-error returns. The fake chmods the extension's
// Configuration.xml read-only after loading it, which is what a real
// permissions problem would do at that moment.
// ---------------------------------------------------------------------------

// applyFailureSite is one apply-leg failure return, the run that reaches it, and
// the fragment of the resulting message that identifies it.
type applyFailureSite struct {
	name string
	mode string
	// flags passed to the fake alongside the mode.
	flags []string
	// wants is the fragment of the wrapped error unique to this return.
	wants string
	// designerText is the DESIGNER refusal that must survive into the message,
	// so a site is not identified by our own wrapper alone.
	designerText string
	// ops is the DESIGNER call sequence the run must produce.
	ops []string
	// noteExpected is false only where the note must NOT be attached.
	noteExpected bool
}

func applyFailureSites() []applyFailureSite {
	const (
		load  = "/LoadConfigFromFiles"
		apply = "/UpdateDBCfg"
	)
	return []applyFailureSite{
		{
			name: "inherited-override branch: the wide strip cannot write",
			mode: fakeModeInheritedAlways, flags: []string{fakeReadOnlyAfterLoadFile},
			wants: "updating database config: strip inherited properties",
			ops:   []string{load, apply}, noteExpected: true,
		},
		{
			name: "inherited-override branch: the reload fails",
			mode: fakeModeInheritedAlways, flags: []string{fakeFailLoadsAfterFile + "=1"},
			wants: "reloading extension config after strip", designerText: loadFailureLog,
			ops:          []string{load, apply, load},
			noteExpected: true,
		},
		{
			name:  "inherited-override branch: the retried apply fails",
			mode:  fakeModeInheritedAlways,
			wants: "updating database config", designerText: inheritedOverrideLog,
			ops:          []string{load, apply, load, apply},
			noteExpected: true,
		},
		{
			name: "run-mode branch: the narrow strip cannot write",
			mode: fakeModeRunModeAlways, flags: []string{fakeReadOnlyAfterLoadFile},
			wants: "updating database config: strip DefaultRunMode",
			ops:   []string{load, apply}, noteExpected: true,
		},
		{
			name: "run-mode branch: the reload fails",
			mode: fakeModeRunModeAlways, flags: []string{fakeFailLoadsAfterFile + "=1"},
			wants: "reloading extension config after DefaultRunMode strip", designerText: loadFailureLog,
			ops:          []string{load, apply, load},
			noteExpected: true,
		},
		{
			name:  "run-mode branch: the retried apply fails",
			mode:  fakeModeRunModeAlways,
			wants: "updating database config", designerText: runModeMismatchLog,
			ops:          []string{load, apply, load, apply},
			noteExpected: true,
		},
		{
			name:  "default branch: a refusal no retry predicate matches",
			mode:  fakeModeUnrelatedApplyFailure,
			wants: "updating database config", designerText: unrelatedApplyLog,
			ops:          []string{load, apply},
			noteExpected: true,
		},
	}
}

func TestEveryApplyLegFailureSaysWhatItLeftBehind(t *testing.T) {
	sites := applyFailureSites()

	// The count is read out of the source. A site added without a scenario
	// moves this number and the test says so instead of passing quietly.
	_, calls := packageLiteralsAndCalls(t, "notApplied")
	if calls != len(sites) {
		t.Fatalf("the package calls notApplied from %d places and this test drives %d of them. "+
			"Every apply-leg failure has to say what it left behind, so a new call site needs a "+
			"scenario here", calls, len(sites))
	}
	if calls < 2 {
		t.Fatalf("the call walk found %d notApplied call sites, which cannot be right and means the "+
			"derivation is not reading the source", calls)
	}

	if os.Geteuid() == 0 {
		t.Fatal("running as root: the two strip-error sites are reached by making a file unwritable, " +
			"and root ignores that, so those scenarios would pass without reaching their site")
	}

	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			dir := newFakeDesigner(t, site.mode, site.flags...)

			var err error
			out := captureStdout(t, func() {
				err = Install(extension.Source, `C:\base`, false, fakePlatformExe(t), "", "", shippedPlatform, false)
			})
			if err == nil {
				t.Fatalf("expected Install to fail\nstdout:\n%s", out)
			}

			// The run reached the site it claims to: the call sequence and the
			// wrapper text together identify the return.
			if got := callOps(fakeCalls(t, dir)); strings.Join(got, ",") != strings.Join(site.ops, ",") {
				t.Fatalf("call sequence\ngot:  %v\nwant: %v\nerror: %v\nstdout:\n%s",
					got, site.ops, err, out)
			}
			if !strings.Contains(err.Error(), site.wants) {
				t.Fatalf("this run did not reach the intended failure return: %q is not in\n%s",
					site.wants, err)
			}
			if site.designerText != "" && !strings.Contains(err.Error(), site.designerText) {
				t.Errorf("DESIGNER's own words were dropped: %q is not in\n%s", site.designerText, err)
			}

			// And it said what it left behind.
			found := noteSentencesIn(err.Error())
			if found[notAppliedHead] != site.noteExpected {
				t.Errorf("note present=%v, want %v. This is a failure AFTER the extension was loaded, "+
					"so the customer has to be told the load is stranded\n%s",
					found[notAppliedHead], site.noteExpected, err)
			}
			if site.noteExpected && !found[notAppliedAdvice] {
				t.Errorf("the note lost its closing advice:\n%s", err)
			}
		})
	}
}
