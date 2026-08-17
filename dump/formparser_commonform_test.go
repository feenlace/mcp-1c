package dump

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A common form is not an object form and its path is not the object-form path.
//
//	object form   Catalogs/<Объект>/Forms/<Форма>/Ext/Form.xml   six segments
//	common form   CommonForms/<Имя>/Ext/Form.xml                 four segments
//
// There is no "Forms" segment and no directory named after the form: the form IS
// the metadata object. Any rule written for the six-segment shape misses every
// common form, and misses it SILENTLY, because a missing Forms directory is not
// an error here. Measured: the reference dump holds 386 CommonForms directories,
// 22 of which carry 42 dynamic lists.

// commonFormRoot builds a dump root holding one common form.
func commonFormRoot(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	extDir := filepath.Join(dir, "CommonForms", name, "Ext")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "Form.xml"), []byte("<Form/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestFindFormFiles_CommonFormIsFoundUnderBothSpellings covers the lookup itself.
// The tool input carries whichever spelling the caller used, and both name the
// same metadata kind, so both have to resolve.
func TestFindFormFiles_CommonFormIsFoundUnderBothSpellings(t *testing.T) {
	const name = "ФайлыВТоме"

	for _, objectType := range []string{"CommonForm", "ОбщаяФорма"} {
		t.Run(objectType, func(t *testing.T) {
			dir := commonFormRoot(t, name)

			forms, err := FindFormFiles(dir, objectType, name)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(forms) != 1 {
				t.Fatalf("expected exactly 1 form for a common form, got %d: %v", len(forms), forms)
			}

			// The form is keyed by its own name, because a common form has no
			// separate form name: the metadata object IS the form.
			got, ok := forms[name]
			if !ok {
				t.Fatalf("expected the map to be keyed %q, got keys %v", name, forms)
			}

			// THE WHOLE VALUE IS COMPARED, and that is the point of this
			// assertion rather than a stricter habit. The only consumer of this
			// value hands it straight to ParseFormXML, whose contract says it
			// performs no containment of its own, so a value that is not the
			// dump-joined path is a different file. A relative value would pass
			// every weaker check written here.
			want := filepath.Join(dir, "CommonForms", name, "Ext", "Form.xml")
			if got != want {
				t.Errorf("path value:\n got %q\nwant %q", got, want)
			}

			// This next assertion is kept for what it documents, NOT for what it
			// proves, and saying so is the point: "no Forms segment" is true of
			// the correct value AND of a relative one, so on its own it cannot
			// tell them apart. The comparison above is what discriminates.
			if strings.Contains(got, string(os.PathSeparator)+"Forms"+string(os.PathSeparator)) {
				t.Errorf("a common form path has no Forms segment, got %q", got)
			}
		})
	}
}

// TestFindFormFiles_CommonFormSymlinkIsNotListed keeps the containment property
// the object-form branch already has: root.Lstat does not follow the final
// component, so a symlinked Form.xml is neither read NOR listed, and the return
// value cannot be used as an existence oracle for a file outside the dump.
//
// The corpus cannot supply this case: 0 of the 5665 Form.xml files are symlinks,
// so "we walked the dump and found none" measures blindness. The link is built
// here, and its target carries a marker.
func TestFindFormFiles_CommonFormSymlinkIsNotListed(t *testing.T) {
	const name = "ФайлыВТоме"
	dir := t.TempDir()

	const marker = "МАРКЕР_ЦЕЛИ_ССЫЛКИ"
	target := filepath.Join(dir, "secret.xml")
	if err := os.WriteFile(target, []byte("<Form>"+marker+"</Form>"), 0o644); err != nil {
		t.Fatal(err)
	}

	extDir := filepath.Join(dir, "CommonForms", name, "Ext")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(extDir, "Form.xml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this filesystem: %v", err)
	}

	forms, err := FindFormFiles(dir, "CommonForm", name)
	// No error, exactly as for an object form whose Form.xml is a symlink: the
	// entry is dropped, not diagnosed.
	if err != nil {
		t.Fatalf("a symlinked Form.xml is dropped, not diagnosed: %v", err)
	}
	if len(forms) != 0 {
		t.Fatalf("a symlinked Form.xml must not be listed, got %v", forms)
	}

	// POSITIVE CONTROL: replacing the link with a real file makes the same call
	// return one entry, so the emptiness above measures the symlink rule and not
	// a lookup that cannot find this layout at all.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("<Form/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	forms, err = FindFormFiles(dir, "CommonForm", name)
	if err != nil || len(forms) != 1 {
		t.Fatalf("control failed: a real Form.xml at the same path must be listed, got %v (err %v)",
			forms, err)
	}
}

// TestFindFormFiles_CommonFormAbsentIsNotAnError keeps the established contract:
// an object with no forms in the dump is not a failure, it is an empty answer,
// and the caller turns that into its own message naming the object.
func TestFindFormFiles_CommonFormAbsentIsNotAnError(t *testing.T) {
	dir := commonFormRoot(t, "ЕстьТакая")

	forms, err := FindFormFiles(dir, "CommonForm", "НетТакой")
	if err != nil {
		t.Fatalf("an absent common form is not an error: %v", err)
	}
	if len(forms) != 0 {
		t.Fatalf("expected no forms for an absent common form, got %v", forms)
	}

	// POSITIVE CONTROL: the root really does serve common forms, so the empty
	// answer above is about this name and not about the layout.
	forms, err = FindFormFiles(dir, "CommonForm", "ЕстьТакая")
	if err != nil || len(forms) != 1 {
		t.Fatalf("control failed: the root serves no common form at all, got %v (err %v)", forms, err)
	}
}

// TestFindFormFiles_CommonFormPathTraversalIsRefused extends the lexical guard to
// the new branch. It matters more here than for object forms, not less: the
// common-form path is built by joining the name straight into it, so without the
// guard a name of "../Catalogs/Валюты/Forms/ФормаСписка" would read a DIFFERENT
// form that is still inside the dump, which containment cannot see because
// nothing escaped.
func TestFindFormFiles_CommonFormPathTraversalIsRefused(t *testing.T) {
	dir := commonFormRoot(t, "ФайлыВТоме")

	cases := []string{
		"..",
		"../../etc",
		"../Catalogs/Валюты/Forms/ФормаСписка",
		"foo/bar",
		"foo\\bar",
		"..\\secret",
	}
	for _, objectName := range cases {
		t.Run(objectName, func(t *testing.T) {
			forms, err := FindFormFiles(dir, "CommonForm", objectName)
			if err == nil {
				t.Fatalf("expected a refusal for %q, got %v", objectName, forms)
			}
			// CLASSIFIED, not merely non-nil. "Some error came back" was green
			// before this branch existed at all, because an unrecognised type is
			// also an error, so a bare err != nil check could not tell a working
			// guard from a missing feature.
			if !errors.Is(err, ErrFormObjectNameRejected) {
				t.Errorf("refusal for %q is not the name guard: %v", objectName, err)
			}
			if forms != nil {
				t.Errorf("a refused lookup must return no map, got %v", forms)
			}
		})
	}
}

// TestFindFormFiles_NameGuardRunsBeforeAnyFilesystemAccess is the ordering half
// of the guard. The refusal has to be lexical and has to happen first: a name
// like "../Catalogs/Валюты/Forms/ФормаСписка" stays INSIDE the dump after the
// path is cleaned, so containment never sees it and only the guard can stop it.
//
// The proof is a dump directory that does not exist. If anything touched the
// filesystem before the guard, the answer would be about the missing directory;
// the guard answering instead is what shows it ran first.
func TestFindFormFiles_NameGuardRunsBeforeAnyFilesystemAccess(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "нет-такого-каталога")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("control failed: %q must not exist for this test to mean anything", missing)
	}

	for _, objectName := range []string{"..", "../Catalogs/Валюты/Forms/ФормаСписка", ""} {
		_, err := FindFormFiles(missing, "CommonForm", objectName)
		if !errors.Is(err, ErrFormObjectNameRejected) {
			t.Errorf("name %q against a non-existent dump answered %v, want the lexical "+
				"refusal, which would prove nothing reached the filesystem first", objectName, err)
		}
	}

	// POSITIVE CONTROL: a well-formed name against the same non-existent dump
	// does reach the filesystem, and answers about the dump rather than the name.
	forms, err := FindFormFiles(missing, "CommonForm", "ФайлыВТоме")
	if errors.Is(err, ErrFormObjectNameRejected) {
		t.Error("control failed: a valid name was rejected by the name guard, so the " +
			"assertions above cannot be measuring the guard's position")
	}
	t.Logf("valid name against a missing dump answered forms=%v err=%v", forms, err)
}

// TestFindFormFiles_EmptyObjectNameIsRefused closes the one input the lexical
// guard did not cover. An empty name is not a traversal, but it is not a form
// either: joined into the path it collapses a segment, and the lookup then
// addresses the parent directory instead of a form inside it. Refusing is the
// only reading that cannot answer about something nobody asked for.
func TestFindFormFiles_EmptyObjectNameIsRefused(t *testing.T) {
	dir := commonFormRoot(t, "ФайлыВТоме")

	for _, objectType := range []string{"CommonForm", "Document"} {
		t.Run(objectType, func(t *testing.T) {
			forms, err := FindFormFiles(dir, objectType, "")
			if err == nil {
				t.Fatalf("an empty object name must be refused, got %v", forms)
			}
			if !errors.Is(err, ErrFormObjectNameRejected) {
				t.Errorf("refusal is not the name guard: %v", err)
			}
			if forms != nil {
				t.Errorf("a refused lookup must return no map, got %v", forms)
			}
		})
	}
}

// TestMetadataTypesDoesNotGainACommonFormEntry is the guard on the road NOT
// taken, and it is here because that road looks like the cheap one.
//
// Adding {"CommonForm", "CommonForms", "ОбщаяФорма"} to metadataTypes would make
// the type known and would look like a fix. It would not be one: FindFormFiles
// would then reach filepath.Join(dirName, objectName, "Forms"), find no such
// directory, and return the "no forms directory, not an error" answer. The loud
// unknown-type refusal would become a silent empty result, which is the worse of
// the two failures. The branch stays explicit instead.
//
// The guard prints the LIST, not the length. A length is satisfied by a swap.
func TestMetadataTypesDoesNotGainACommonFormEntry(t *testing.T) {
	want := []string{
		"Catalog", "Document", "DataProcessor", "Report",
		"InformationRegister", "AccumulationRegister", "AccountingRegister",
		"CalculationRegister", "ChartOfAccounts", "ChartOfCharacteristicTypes",
		"ChartOfCalculationTypes", "ExchangePlan", "BusinessProcess", "Task",
		"Enum", "Constant",
	}

	got := make([]string, 0, len(metadataTypes))
	for _, mt := range metadataTypes {
		got = append(got, mt.SingularEng)
	}
	if len(got) != len(want) {
		t.Fatalf("metadataTypes holds %d kinds, want %d\n got %v\nwant %v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("metadataTypes[%d] = %q, want %q\n got %v\nwant %v", i, got[i], want[i], got, want)
		}
	}

	if dir, ok := objectTypeToDumpDir["CommonForm"]; ok {
		t.Errorf("objectTypeToDumpDir gained a CommonForm entry (-> %q). The common form is "+
			"served by its own branch in FindFormFiles precisely because the object-form "+
			"path shape does not fit it", dir)
	}

	// The directory name the branch uses is tied to the table that already knows
	// this kind, so the two cannot drift into disagreeing about its spelling.
	if ru := dumpDirNames[commonFormsDumpDir]; ru != "ОбщаяФорма" {
		t.Errorf("dumpDirNames[%q] = %q, want %q", commonFormsDumpDir, ru, "ОбщаяФорма")
	}
}

// TestFindFormFiles_CommonFormUsesTheSameRootAsObjectForms pins the containment
// mechanism rather than its outcome: the value has to come from the same os.Root
// walk the object-form branch uses, so a dump that smuggles an outside file in
// as a directory symlink is refused the same way for both shapes.
func TestFindFormFiles_CommonFormUsesTheSameRootAsObjectForms(t *testing.T) {
	outside := t.TempDir()
	const marker = "МАРКЕР_СНАРУЖИ"
	outsideExt := filepath.Join(outside, "ФайлыВТоме", "Ext")
	if err := os.MkdirAll(outsideExt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideExt, "Form.xml"),
		[]byte("<Form>"+marker+"</Form>"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "CommonForms")); err != nil {
		t.Skipf("symlinks unavailable on this filesystem: %v", err)
	}

	forms, err := FindFormFiles(dir, "CommonForm", "ФайлыВТоме")
	if len(forms) != 0 {
		t.Errorf("a CommonForms directory that escapes the dump must yield no forms, got %v", forms)
	}
	// Either answer is containment; what must not happen is a path to the file
	// outside the dump coming back as if it were part of it.
	for _, p := range forms {
		if strings.Contains(p, outside) {
			t.Errorf("a path outside the dump was returned: %q", p)
		}
	}
	if err != nil && !errors.Is(err, ErrFormsDirUnreadable) {
		t.Errorf("a containment refusal must be the named path-free one, got %v", err)
	}
	t.Logf("escaping CommonForms answered with forms=%v err=%v", forms, err)
}

// TestParseFormXML_RefusalsCarryNoAbsolutePath closes the channel the package
// already closed once for the forms DIRECTORY and had left open for the form
// FILE.
//
// ErrFormsDirUnreadable exists, in this file's own words, because the OS error
// it replaces "carries the absolute path it failed on, which must never reach
// the caller". The three read failures inside ParseFormXML were still wrapping
// that same OS error, so an unreadable Form.xml reported the dump root, and with
// it the operator's account name, into an answer a model reads and into
// server.log under --debug.
//
// The path is not a detail of the message here: it is the only part of it that
// says anything the caller cannot already see, and it is the part they must not.
func TestParseFormXML_RefusalsCarryNoAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	ext := filepath.Join(dir, "CommonForms", "ФайлыВТоме", "Ext")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}

	unreadable := filepath.Join(ext, "Form.xml")
	if err := os.WriteFile(unreadable, []byte("<Form/>"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(unreadable); err == nil {
		t.Skip("this filesystem or user ignores mode 000, so there is no unreadable file to test")
	}

	cases := map[string]string{
		"unreadable file":        unreadable,
		"file that is not there": filepath.Join(ext, "НетТакого.xml"),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			form, err := ParseFormXML(path)
			if err == nil {
				t.Fatalf("expected a refusal, got a form: %+v", form)
			}
			if strings.Contains(err.Error(), dir) {
				t.Errorf("the refusal carries the absolute path it failed on: %v", err)
			}
			if !errors.Is(err, ErrFormXMLUnreadable) {
				t.Errorf("the refusal is not the named path-free one: %v", err)
			}
		})
	}

	// POSITIVE CONTROL over the detector. It has to be able to see the path,
	// or every assertion above is measuring a scan that never fires.
	planted := "reading form XML: open " + filepath.Join(dir, "CommonForms", "Ф", "Ext", "Form.xml") +
		": permission denied"
	if !strings.Contains(planted, dir) {
		t.Fatal("control failed: the detector did not see the dump root in a message built " +
			"around it, so the checks above prove nothing")
	}
}
