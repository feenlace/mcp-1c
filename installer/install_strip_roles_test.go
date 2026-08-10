package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/extension"
)

// ---------------------------------------------------------------------------
// --strip-default-roles, and the reason it is a flag rather than the default.
//
// MEASURED on two synthetic file bases and again on a real typical
// configuration. The reading that holds on both: an ordinary least-privileged
// account holding roles of the configuration answers 200 WITH the element and
// 403 WITHOUT it, while a user holding MCP_ОсновнаяРоль answers 200 either way
// and a user with no roles at all answers 403 either way. Causation was
// confirmed bidirectionally on both, five flips on the real base.
//
// The administrator row does NOT agree between them and is not used here: on the
// real configuration an administrator kept the service in every arm. The full
// tables and the reason the synthetic bases misled are in
// extension/default_roles_test.go.
//
// So the declaration is the mechanism that grants the service to the restricted
// account a careful customer points the connector at. Removing it by default
// would fix the one customer whose Standard Subsystems configuration refuses the
// extra entry and take the service away from every other customer's service
// account on their next update.
//
// Under the flag the strip has to be NARROW for the same reason the run-mode
// strip is: it is paid for one property. The wide-versus-narrow assertion below
// is the same shape as the run-mode one, run against the production regexps.
// ---------------------------------------------------------------------------

// loadedUnderStripFlag runs a successful install with or without the flag and
// returns what the single load was handed, plus stdout.
func loadedUnderStripFlag(t *testing.T, stripRoles bool) (loaded, out string) {
	t.Helper()
	dir := newFakeDesigner(t, fakeModeOK)

	var err error
	out = captureStdout(t, func() {
		err = Install(extension.Source, `C:\base`, false, fakePlatformExe(t), "", "", shippedPlatform, stripRoles)
	})
	if err != nil {
		t.Fatalf("Install failed with stripRoles=%v: %v\nstdout:\n%s", stripRoles, err, out)
	}
	return loadedConfiguration(t, dir, 1), out
}

func TestDefaultRolesAreStrippedOnlyUnderTheFlag(t *testing.T) {
	const declaration = "<DefaultRoles>"
	const item = "Role.MCP_ОсновнаяРоль"

	// The premise: the shipped file declares it. If it did not, both halves
	// below would agree for the wrong reason.
	if shipped := shippedConfiguration(t); !strings.Contains(shipped, declaration) {
		t.Fatalf("the shipped Configuration.xml does not declare %s, so this test cannot tell the "+
			"flag's effect from the file's contents", declaration)
	}

	withoutFlag, _ := loadedUnderStripFlag(t, false)
	withFlag, _ := loadedUnderStripFlag(t, true)

	// Default: the declaration reaches the base. This is what keeps every user
	// who holds a role of the configuration working.
	if !strings.Contains(withoutFlag, declaration) {
		t.Errorf("without the flag the declaration was removed anyway. Measured on every base: that "+
			"answers 403 to an ordinary least-privileged account holding roles of the "+
			"configuration\n%s", withoutFlag)
	}
	if !strings.Contains(withoutFlag, item) {
		t.Errorf("without the flag the declaration no longer names %s:\n%s", item, withoutFlag)
	}

	// Under the flag: gone.
	if strings.Contains(withFlag, declaration) {
		t.Errorf("--strip-default-roles did not remove the declaration:\n%s", withFlag)
	}
	if strings.Contains(withFlag, item) {
		t.Errorf("--strip-default-roles left the default-role item behind:\n%s", withFlag)
	}

	// The role itself is NOT removed: it is what an administrator assigns, and
	// under the flag it is the only thing that grants the service.
	for _, keep := range []string{
		"<Role>MCP_ОсновнаяРоль</Role>",
		"<ChildObjects>",
		"<DefaultRunMode>",
		"<Name>" + extensionName + "</Name>",
	} {
		if !strings.Contains(withFlag, keep) {
			t.Errorf("--strip-default-roles also removed %q, which it must not:\n%s", keep, withFlag)
		}
	}

	// Narrow against wide, the same way the run-mode strip is measured. Every
	// element the WIDE regexp finds in the shipped file, other than the default
	// roles, must survive the flag. The list comes from the production regexp
	// and the shipped XML, not from a list typed here.
	shipped := shippedConfiguration(t)
	wide := inheritedPropertyRe.FindAllString(shipped, -1)
	if len(wide) < 5 {
		t.Fatalf("the wide strip matches %d elements of the shipped Configuration.xml; below five "+
			"there is nothing for the narrow strip to be narrower THAN", len(wide))
	}
	narrow := defaultRolesRe.FindAllString(shipped, -1)
	if len(narrow) != 1 {
		t.Fatalf("defaultRolesRe matches %d elements of the shipped Configuration.xml, want exactly 1",
			len(narrow))
	}

	survivors := 0
	for _, element := range wide {
		if strings.Contains(element, declaration) {
			continue
		}
		survivors++
		if !strings.Contains(withFlag, strings.TrimSpace(element)) {
			t.Errorf("the flag also took %q. It is paid for one property, exactly like the run-mode "+
				"strip", strings.TrimSpace(element))
		}
	}
	if survivors < 4 {
		t.Fatalf("only %d elements besides the declaration were checked for survival, so narrow is not "+
			"being measured against wide", survivors)
	}
	t.Logf("the wide strip matches %d elements; %d of them must survive --strip-default-roles",
		len(wide), survivors)
}

// TestDeclaresOurRoleAsDefaultReadsWhatItClaims pins the observation the note is
// selected by, including the two cases its doc comment promises and no install
// path produces.
//
// A declaration that is empty, or that names somebody else's role, grants OUR
// role to nobody. Treating either as "declared" would print the note that says
// users holding roles of the configuration are already served, which is the
// exact falsehood the observing selector exists to prevent. Nothing else in the
// suite reaches these two shapes, because no code path builds them: measured,
// weakening the check to "any DefaultRoles element is enough" left every other
// test in this package green.
func TestDeclaresOurRoleAsDefaultReadsWhatItClaims(t *testing.T) {
	const head = "<Properties>\n\t\t\t<Name>MCP_HTTPService</Name>\n"
	const tail = "\t\t\t<Vendor/>\n</Properties>"

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "declared, as shipped",
			body: "\t\t\t<DefaultRoles>\n\t\t\t\t<xr:Item xsi:type=\"xr:MDObjectRef\">Role.MCP_ОсновнаяРоль</xr:Item>\n\t\t\t</DefaultRoles>\n",
			want: true,
		},
		{
			name: "empty self-closing declaration grants nobody anything",
			body: "\t\t\t<DefaultRoles/>\n",
			want: false,
		},
		{
			name: "declaration naming somebody else's role",
			body: "\t\t\t<DefaultRoles>\n\t\t\t\t<xr:Item xsi:type=\"xr:MDObjectRef\">Role.ПолныеПрава</xr:Item>\n\t\t\t</DefaultRoles>\n",
			want: false,
		},
		{
			name: "no declaration at all",
			body: "",
			want: false,
		},
	}

	// The set must contain both answers, or a function returning a constant
	// would satisfy it.
	trues := 0
	for _, tc := range cases {
		if tc.want {
			trues++
		}
	}
	if trues == 0 || trues == len(cases) {
		t.Fatalf("%d of %d cases expect true; a set that expects one answer cannot detect a "+
			"constant", trues, len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Configuration.xml")
			if err := os.WriteFile(path, []byte(head+tc.body+tail), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := declaresOurRoleAsDefault(path); got != tc.want {
				t.Errorf("declaresOurRoleAsDefault = %v, want %v, for:\n%s", got, tc.want, tc.body)
			}
		})
	}

	// A file that cannot be read counts as NOT declared, so the note falls back
	// to telling the customer to assign the role by hand.
	if declaresOurRoleAsDefault(filepath.Join(t.TempDir(), "absent.xml")) {
		t.Error("an unreadable configuration was reported as declaring the role")
	}
}

// TestDefaultRolesRegexpHandlesBothSpellings pins the regexp against the empty
// self-closing form 1С writes when the property was set and then cleared. The
// populated form is the one in the shipped file; the empty one is a declaration
// too, and it grants nothing, so leaving it behind would be the defect the flag
// exists to avoid.
func TestDefaultRolesRegexpHandlesBothSpellings(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{
			name: "populated, as shipped",
			input: "\t\t\t<ScriptVariant>Russian</ScriptVariant>\n" +
				"\t\t\t<DefaultRoles>\n" +
				"\t\t\t\t<xr:Item xsi:type=\"xr:MDObjectRef\">Role.MCP_ОсновнаяРоль</xr:Item>\n" +
				"\t\t\t</DefaultRoles>\n" +
				"\t\t\t<Vendor/>\n",
		},
		{
			name:  "empty self-closing",
			input: "\t\t\t<ScriptVariant>Russian</ScriptVariant>\n\t\t\t<DefaultRoles/>\n\t\t\t<Vendor/>\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Control: the element really is in this input, so a regexp that
			// matched nothing would not be mistaken for a clean removal.
			if !strings.Contains(tc.input, "DefaultRoles") {
				t.Fatal("the control input does not contain the element it is named for")
			}
			got := defaultRolesRe.ReplaceAllString(tc.input, "")
			if strings.Contains(got, "DefaultRoles") {
				t.Errorf("the element survived the strip:\n%q", got)
			}
			for _, keep := range []string{"<ScriptVariant>Russian</ScriptVariant>", "<Vendor/>"} {
				if !strings.Contains(got, keep) {
					t.Errorf("the strip took %q with it:\n%q", keep, got)
				}
			}
		})
	}
}
