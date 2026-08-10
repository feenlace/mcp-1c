package installer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/extension"
)

// ---------------------------------------------------------------------------
// The role instruction is true of every install, so it has to print on every
// install.
//
// It used to print only where stripInheritedProperties ran: the pre-patch for
// platforms below 8.3.14, and the two retries for old compat modes. Every
// customer on 8.3.14 or newer whose base accepted the extension first time was
// told nothing at all, and that is now the ordinary case. With the default-role
// declaration gone from Configuration.xml the platform hands MCP_ОсновнаяРоль to
// nobody, so a least-privilege user is denied until an administrator assigns it.
// Not saying so leaves the customer with a working install and 403s.
//
// "Every install" is checked as a COUNT, not as presence, because the obvious
// way to get it onto every path is to add a call and leave the old ones, and a
// presence check is green either way. The old platform path is the one that
// already printed the note, so it is the case that reddens on a leftover.
// ---------------------------------------------------------------------------

// countRoleNote returns how many complete copies of the given note the output
// contains. Every line is counted separately and the counts must agree, so a
// half-printed note is not reported as a whole one.
func countRoleNote(t *testing.T, out string, lines []string) int {
	t.Helper()
	if len(lines) < 3 {
		t.Fatalf("the note is %d lines; it is meant to say what the role does, who reaches the "+
			"service already, and who must be given the role by hand", len(lines))
	}
	counts := make([]int, 0, len(lines))
	for _, line := range lines {
		counts = append(counts, strings.Count(out, line))
	}
	for i, c := range counts {
		if c != counts[0] {
			t.Fatalf("the note printed in pieces: line 1 appears %d times, line %d appears %d times\n%s",
				counts[0], i+1, c, out)
		}
	}
	return counts[0]
}

func TestInstallPrintsTheRoleNoteOnEverySuccessfulInstall(t *testing.T) {
	cases := []struct {
		name            string
		mode            string
		platformVersion string
		stripRoles      bool
		wantErr         bool
		wantNotes       int
	}{
		{
			// The ordinary case, and the one that printed nothing before: a
			// modern platform whose base takes the extension first time.
			name: "modern platform, nothing to strip", mode: fakeModeOK,
			platformVersion: "8.3.27", wantNotes: 1,
		},
		{
			// Recovered through the run-mode retry: still a successful install,
			// so still exactly one note.
			name: "run-mode mismatch, recovered on the apply leg", mode: fakeModeRunModeMismatch,
			platformVersion: "8.3.27", wantNotes: 1,
		},
		{
			// Recovered through the OTHER apply-leg branch. This one used to
			// print the note itself and return early; it now falls through to
			// the single exit like everything else, and must still print once.
			name: "inherited-property override, recovered on the apply leg", mode: fakeModeInheritedOverride,
			platformVersion: "8.3.27", wantNotes: 1,
		},
		{
			// Nothing was applied. Telling the customer the role is installed
			// would be false.
			name: "apply exhausted", mode: fakeModeRunModeAlways,
			platformVersion: "8.3.27", wantErr: true, wantNotes: 0,
		},
		{
			name: "load refused", mode: fakeModeLoadFails,
			platformVersion: "8.3.27", wantErr: true, wantNotes: 0,
		},
		{
			// Under the flag the note is the OTHER one, and it must print
			// exactly once just the same.
			name: "strip-default-roles", mode: fakeModeOK,
			platformVersion: "8.3.27", stripRoles: true, wantNotes: 1,
		},
		{
			// NO FLAG, and the declaration is still gone: the pre-patch for
			// platforms below 8.3.14 strips inherited properties, and
			// <DefaultRoles> is one of the twelve.
			name: "old platform strips the declaration without the flag", mode: fakeModeOK,
			platformVersion: "8.3.13", wantNotes: 1,
		},
		{
			// NO FLAG: the load-leg retry for old compat modes strips it.
			name: "load-leg inherited-override strip, no flag", mode: fakeModeLoadInheritedOverride,
			platformVersion: "8.3.27", wantNotes: 1,
		},
		{
			// NO FLAG: the apply-leg retry this branch added strips it.
			name: "apply-leg inherited-override strip, no flag", mode: fakeModeInheritedOverride,
			platformVersion: "8.3.27", wantNotes: 1,
		},
		{
			name: "strip-default-roles on a failed install", mode: fakeModeRunModeAlways,
			platformVersion: "8.3.27", stripRoles: true, wantErr: true, wantNotes: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newFakeDesigner(t, tc.mode)
			exe := fakePlatformExe(t)

			var err error
			out := captureStdout(t, func() {
				err = Install(extension.Source, `C:\base`, false, exe, "", "", tc.platformVersion, tc.stripRoles)
			})
			if tc.wantErr && err == nil {
				t.Fatalf("expected Install to fail in mode %q\nstdout:\n%s", tc.mode, out)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Install failed: %v\nstdout:\n%s", err, out)
			}

			// WHICH note is right is decided by the XML that was actually
			// loaded, never by the flag. Three code paths strip the declaration
			// with the flag unset, so a flag-driven selector tells the customer
			// that users with roles are served while the declaration that serves
			// them is absent from the applied configuration.
			printed, silent := roleNoteLines, roleNoteStrippedLines
			printedName, silentName := "default", "stripped"
			if !tc.wantErr {
				loaded := lastLoadedConfiguration(t, dir)
				if !strings.Contains(loaded, "<DefaultRoles>") {
					printed, silent = roleNoteStrippedLines, roleNoteLines
					printedName, silentName = "stripped", "default"
				}
				t.Logf("last loaded XML declares default roles: %v (flag was %v)",
					strings.Contains(loaded, "<DefaultRoles>"), tc.stripRoles)
			} else if tc.stripRoles {
				printed, silent = roleNoteStrippedLines, roleNoteLines
				printedName, silentName = "stripped", "default"
			}
			if got := countRoleNote(t, out, printed); got != tc.wantNotes {
				t.Errorf("the %s role note printed %d times, want %d\nstdout:\n%s",
					printedName, got, tc.wantNotes, out)
			}
			if got := countRoleNote(t, out, silent); got != 0 {
				t.Errorf("the %s role note printed %d times on a run that must not use it. The two "+
					"notes make opposite promises about who reaches the service\nstdout:\n%s",
					silentName, got, out)
			}
		})
	}
}

// TestRoleNoteSelectorIsTheXMLNotTheFlag is the guard for the regression the
// flag-driven selector was.
//
// The declaration's survival does NOT depend on --strip-default-roles. Three
// other paths remove it with the flag unset: the pre-patch for platforms 8.3.10
// to 8.3.13, the load-leg retry for old compat modes, and the apply-leg retry
// for the same. Selecting the note by the flag told those customers that a user
// holding roles of the configuration is served without further action, while the
// declaration that serves them was absent from the applied XML. That is false by
// our own measurement.
//
// The reading below is DERIVED: for every successful install, the note the
// customer got is compared against the file the run actually loaded. Nothing
// here says which note belongs to which scenario, so switching the selector back
// to the flag reddens on the three flag-unset paths without any case needing to
// be rewritten.
func TestRoleNoteSelectorIsTheXMLNotTheFlag(t *testing.T) {
	scenarios := []struct {
		name       string
		mode       string
		platform   string
		stripRoles bool
		// declared is what the run is EXPECTED to end up loading. It is stated
		// so the scenario set cannot silently collapse to one side.
		declared bool
	}{
		{"nothing strips it", fakeModeOK, "8.3.27", false, true},
		{"the flag strips it", fakeModeOK, "8.3.27", true, false},
		{"the pre-patch strips it, flag unset", fakeModeOK, "8.3.13", false, false},
		{"the load-leg retry strips it, flag unset", fakeModeLoadInheritedOverride, "8.3.27", false, false},
		{"the apply-leg retry strips it, flag unset", fakeModeInheritedOverride, "8.3.27", false, false},
		{"the narrow run-mode strip leaves it alone", fakeModeRunModeMismatch, "8.3.27", false, true},
	}

	// The set has to contain the case the flag-driven selector gets wrong, or
	// this test passes on the implementation it exists to reject.
	disagreements := 0
	for _, sc := range scenarios {
		if sc.stripRoles != !sc.declared {
			disagreements++
		}
	}
	if disagreements < 3 {
		t.Fatalf("only %d scenarios have the flag disagreeing with the XML; those are the only ones "+
			"that can tell an observing selector from a flag-driven one", disagreements)
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			dir := newFakeDesigner(t, sc.mode)

			var err error
			out := captureStdout(t, func() {
				err = Install(extension.Source, `C:\base`, false, fakePlatformExe(t), "", "", sc.platform, sc.stripRoles)
			})
			if err != nil {
				t.Fatalf("Install failed: %v\nstdout:\n%s", err, out)
			}

			loaded := lastLoadedConfiguration(t, dir)
			declared := strings.Contains(loaded, "Role."+roleName)
			if declared != sc.declared {
				t.Fatalf("this scenario was supposed to end up with declared=%v and ended up with %v, "+
					"so it is not exercising what it claims", sc.declared, declared)
			}

			want, wantName := roleNoteLines, "default"
			other, otherName := roleNoteStrippedLines, "stripped"
			if !declared {
				want, wantName = roleNoteStrippedLines, "stripped"
				other, otherName = roleNoteLines, "default"
			}

			if got := countRoleNote(t, out, want); got != 1 {
				t.Errorf("the loaded configuration declares our role: %v, so the %s note belongs here "+
					"and it printed %d times\nstdout:\n%s", declared, wantName, got, out)
			}
			if got := countRoleNote(t, out, other); got != 0 {
				t.Errorf("the %s note printed %d times while the loaded configuration declares our "+
					"role: %v. The flag was %v, and the flag is not what decides this\nstdout:\n%s",
					otherName, got, declared, sc.stripRoles, out)
			}
		})
	}
}

// TestRoleNoteHasOneCaller pins the shape that makes double printing impossible:
// the lines are written once, in roleNoteLines, and printed from one call site.
// A second call site is how "print it everywhere" turns into "print it twice on
// the paths that had it already".
//
// The reading is an AST walk, not a text search, because one of the three lines
// contains quotes: in the source it is spelled with \" and it never appears in
// the file as the string it evaluates to. A text search reported that line as
// absent while it was in fact printed from three places.
//
// The walk covers EVERY non-test file of the package, not the literal
// "installer.go". Parsing one named file left a second print from any sibling
// file invisible to a guard whose name promises to count callers.
func TestRoleNoteHasOneCaller(t *testing.T) {
	literals, calls := packageLiteralsAndCalls(t, "printRoleNote")

	// Positive controls for both readings, on the same walk: a literal that is
	// certainly in the file, and a call that is certainly made.
	if !slices.Contains(literals, "Updating database...") {
		t.Fatalf("the literal walk did not find a string the package certainly contains, so its "+
			"verdict on the note lines means nothing (found %d literals)", len(literals))
	}
	if _, stripCalls := packageLiteralsAndCalls(t, "stripInheritedProperties"); stripCalls < 2 {
		t.Fatalf("the call walk found %d calls to stripInheritedProperties, which the package makes "+
			"from more than one place, so its verdict on printRoleNote means nothing", stripCalls)
	}

	if calls != 1 {
		t.Errorf("printRoleNote is called from %d places in the package, want exactly 1. Install has "+
			"a single successful exit, and keeping the call there is what makes a second note "+
			"structurally impossible", calls)
	}

	// The lines of BOTH notes must be written once each, inside their slice, or
	// a stray fmt.Println of the same text would slip past the call count above.
	for _, set := range []struct {
		name  string
		lines []string
	}{
		{"roleNoteLines", roleNoteLines},
		{"roleNoteStrippedLines", roleNoteStrippedLines},
	} {
		if len(set.lines) < 3 {
			t.Errorf("%s has %d lines; an emptied set makes the loop below check nothing",
				set.name, len(set.lines))
		}
		for i, line := range set.lines {
			got := 0
			for _, lit := range literals {
				if lit == line {
					got++
				}
			}
			if got != 1 {
				t.Errorf("line %d of %s is written %d times in the package, want 1: %q",
					i+1, set.name, got, line)
			}
		}
	}
}

// packageGoFiles lists the package's non-test Go sources. The guards below used
// to parse the literal "installer.go", which made a second print from any other
// file in the package invisible to a test whose name promises to count callers.
func packageGoFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	var files, skipped []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			skipped = append(skipped, name)
			continue
		}
		files = append(files, name)
	}
	// Controls: the walk found the production file, and the exclusion of test
	// files was exercised against test files that really are on disk. Without
	// the second, a filter that dropped everything would look the same.
	if !slices.Contains(files, "installer.go") {
		t.Fatalf("the package walk did not find installer.go, it found %v", files)
	}
	if len(skipped) == 0 {
		t.Fatal("the walk skipped no _test.go file, so its exclusion rule never ran and a production " +
			"file named like a test would be dropped unnoticed")
	}
	return files
}

// packageLiteralsAndCalls parses every non-test Go file of the package and
// returns each string literal it contains, already unquoted, together with the
// number of calls made to the named function across all of them.
func packageLiteralsAndCalls(t *testing.T, funcName string) (literals []string, calls int) {
	t.Helper()
	for _, path := range packageGoFiles(t) {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind == token.STRING {
					if s, uErr := strconv.Unquote(node.Value); uErr == nil {
						literals = append(literals, s)
					}
				}
			case *ast.CallExpr:
				if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == funcName {
					calls++
				}
			}
			return true
		})
	}
	return literals, calls
}
