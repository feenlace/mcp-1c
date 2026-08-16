package dump

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE SENTINELS WERE INTERCHANGEABLE AND THE SUITE WAS GREEN.
//
// Every refusal in this file is classified from outside the package with
// errors.Is, and that is the whole reason the sentinels are exported: matching
// the message is the coupling they exist to remove, and the message is also the
// thing that must not travel, because one of these failures used to be built
// around the absolute path it happened on.
//
// A classification is only worth anything if the producer really attaches the
// value the classifier looks for. Before this file, ErrFormXMLTooLarge,
// ErrFormXMLNotRegular and ErrFormUnknownObjectType could each be swapped for
// another sentinel at the site that produces it and nothing anywhere went red:
// the tests that reached those sites asserted that SOMETHING came back, and the
// tests that named the sentinels fed them to the classifier by hand, so the two
// halves never met.
//
// So the identity is asserted AT THE PRODUCER, and it is asserted as an identity
// rather than as a match: the wanted sentinel has to be found and every other
// sentinel in the set has to be absent. «errors.Is(err, X)» alone is satisfied by
// an error carrying X and four others.

// formSentinelSet is the closed set an ErrForm refusal is classified into. It is
// written out once here and shared, so a test cannot check identity against a
// set that has quietly lost a member.
//
// It is kept in step with the declarations by
// TestFormSentinelsAreEnumeratedWhereTheyAreClaimed, which reads the exported
// sentinels out of formparser.go; a sentinel added there and forgotten here
// would leave this set checking identity against six values out of seven.
func formSentinelSet() []struct {
	name string
	err  error
} {
	return []struct {
		name string
		err  error
	}{
		{"ErrFormsDirUnreadable", ErrFormsDirUnreadable},
		{"ErrFormXMLNotRegular", ErrFormXMLNotRegular},
		{"ErrFormObjectNameRejected", ErrFormObjectNameRejected},
		{"ErrFormUnknownObjectType", ErrFormUnknownObjectType},
		{"ErrFormXMLUnreadable", ErrFormXMLUnreadable},
		{"ErrFormXMLTooLarge", ErrFormXMLTooLarge},
	}
}

// assertFormSentinel requires err to carry EXACTLY the named sentinel out of the
// closed set: the wanted one present, every other one absent.
func assertFormSentinel(t *testing.T, err error, wantName string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a refusal carrying %s, got nil", wantName)
	}
	set := formSentinelSet()

	found := false
	var alsoMatched []string
	for _, s := range set {
		hit := errors.Is(err, s.err)
		switch {
		case s.name == wantName && hit:
			found = true
		case s.name == wantName && !hit:
			t.Errorf("the refusal does not carry %s, so a caller classifying with errors.Is "+
				"cannot tell this failure from any other: %v", wantName, err)
		case hit:
			alsoMatched = append(alsoMatched, s.name)
		}
	}
	if len(alsoMatched) > 0 {
		t.Errorf("the refusal carries %s AND %v, so the classification is not an identity: %v",
			wantName, alsoMatched, err)
	}
	// POSITIVE CONTROL over the set itself: the wanted name has to BE in it, or
	// the loop above compared against nothing and reported nothing.
	if !found {
		names := make([]string, 0, len(set))
		for _, s := range set {
			names = append(names, s.name)
		}
		if !strings.Contains(strings.Join(names, " "), wantName) {
			t.Fatalf("control failed: %q is not in the sentinel set %v, so this assertion "+
				"checked an identity against a value nobody holds", wantName, names)
		}
	}
}

// TestFormRefusalsCarryTheirOwnSentinel drives every producer in this file and
// requires each to attach its own sentinel and no other.
//
// Every case reaches the real producer. None of them constructs an error and
// hands it to a classifier, because that is precisely the shape that left the
// three swappable sentinels green.
func TestFormRefusalsCarryTheirOwnSentinel(t *testing.T) {
	cases := []struct {
		name     string
		want     string
		produce  func(t *testing.T) error
		skipRoot bool
	}{
		{
			name: "an object kind this package serves no forms for",
			want: "ErrFormUnknownObjectType",
			produce: func(t *testing.T) error {
				_, err := FindFormFiles(t.TempDir(), "ЧтоТоНеТо", "Валюты")
				return err
			},
		},
		{
			name: "an empty object name, refused before the filesystem is touched",
			want: "ErrFormObjectNameRejected",
			produce: func(t *testing.T) error {
				_, err := FindFormFiles(t.TempDir(), "Catalog", "")
				return err
			},
		},
		{
			name: "an object name carrying a path separator",
			want: "ErrFormObjectNameRejected",
			produce: func(t *testing.T) error {
				_, err := FindFormFiles(t.TempDir(), "Catalog", "../../etc")
				return err
			},
		},
		{
			name:     "a Forms directory that cannot be read",
			want:     "ErrFormsDirUnreadable",
			skipRoot: true,
			produce: func(t *testing.T) error {
				dumpDir := t.TempDir()
				forms := filepath.Join(dumpDir, "Catalogs", "Валюты", "Forms")
				if err := os.MkdirAll(forms, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(forms, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(forms, 0o755) })
				if _, err := os.ReadDir(forms); err == nil {
					t.Skip("this filesystem or user ignores mode 000, so the directory is readable")
				}
				_, err := FindFormFiles(dumpDir, "Catalog", "Валюты")
				return err
			},
		},
		{
			name: "a form path that is a directory rather than a file",
			want: "ErrFormXMLNotRegular",
			produce: func(t *testing.T) error {
				dir := filepath.Join(t.TempDir(), "Form.xml")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				_, err := ParseFormXML(dir)
				return err
			},
		},
		{
			name: "a form file that is not there",
			want: "ErrFormXMLUnreadable",
			produce: func(t *testing.T) error {
				_, err := ParseFormXML(filepath.Join(t.TempDir(), "НетТакого.xml"))
				return err
			},
		},
		{
			name:     "a form file that cannot be opened",
			want:     "ErrFormXMLUnreadable",
			skipRoot: true,
			produce: func(t *testing.T) error {
				path := filepath.Join(t.TempDir(), "Form.xml")
				if err := os.WriteFile(path, []byte("<Form/>"), 0o000); err != nil {
					t.Fatal(err)
				}
				if _, err := os.ReadFile(path); err == nil {
					t.Skip("this filesystem or user ignores mode 000, so the file is readable")
				}
				_, err := ParseFormXML(path)
				return err
			},
		},
		{
			name: "a form file one byte over the read limit",
			want: "ErrFormXMLTooLarge",
			produce: func(t *testing.T) error {
				original := maxFormFileBytes
				t.Cleanup(func() { maxFormFileBytes = original })
				maxFormFileBytes = 512
				path := filepath.Join(t.TempDir(), "Form.xml")
				body := "<Form>" + strings.Repeat("x", int(maxFormFileBytes)) + "</Form>"
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				st, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if st.Size() <= maxFormFileBytes {
					t.Fatalf("control failed: the file is %d bytes against a limit of %d, so it "+
						"is not over the limit at all", st.Size(), maxFormFileBytes)
				}
				_, err = ParseFormXML(path)
				return err
			},
		},
	}

	// Every sentinel in the closed set has to be produced by at least one case,
	// or a sentinel nobody can reach would keep its identity unchecked forever.
	covered := map[string]bool{}
	for _, tc := range cases {
		covered[tc.want] = true
	}
	for _, s := range formSentinelSet() {
		if !covered[s.name] {
			t.Errorf("no case in this table reaches the producer of %s, so nothing checks that "+
				"the site attaching it attaches THAT one", s.name)
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipRoot && os.Geteuid() == 0 {
				t.Skip("running as root, so a mode 000 path is still reachable")
			}
			assertFormSentinel(t, tc.produce(t), tc.want)
		})
	}
}
