package dump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// formXMLWith returns a minimal, well-formed 1C form XML whose InputField name
// carries marker, so the marker surfaces in the parsed FormInfo. A test that
// asserts on the marker therefore fails by the outside CONTENT coming back, not
// by an error string.
func formXMLWith(marker string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform" version="2.21">
  <ChildItems>
    <InputField name="` + marker + `" id="1">
      <DataPath>Объект.Реквизит1</DataPath>
    </InputField>
  </ChildItems>
</Form>`
}

// writeFormXML creates <base>/<rel> and writes a form XML carrying marker.
func writeFormXML(t *testing.T, base, rel, marker string) string {
	t.Helper()
	full := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(formXMLWith(marker)), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

// symlinkOrSkip creates newname -> oldname, skipping the test on platforms that
// do not support symlinks (matching the convention in symlink_containment_test.go).
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(newname), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
}

// markerReachable reports whether the get_form_structure dump chain, driven
// exactly as tools/form.go drives it (FindFormFiles then ParseFormXML on a
// returned path), yields a form whose parsed content contains marker.
//
// This mirrors the production chain rather than asserting on any error text, so
// the test fails by the outside file's CONTENT actually coming back.
func markerReachable(t *testing.T, dumpDir, objectType, objectName, marker string) (bool, int) {
	t.Helper()
	forms, err := FindFormFiles(dumpDir, objectType, objectName)
	if err != nil {
		return false, 0
	}
	for _, path := range forms {
		info, perr := ParseFormXML(path)
		if perr != nil {
			continue
		}
		for _, e := range info.Elements {
			if strings.Contains(e.Name, marker) {
				return true, len(forms)
			}
		}
	}
	return false, len(forms)
}

// TestFindFormFiles_SymlinkContainment proves that a malicious dump cannot use
// get_form_structure to read a form XML that lives outside the dump root.
//
// Lexical traversal (".." and both separators) is already refused before this
// test's fixtures matter; the vector here is a SYMLINK planted inside an
// otherwise well-formed dump, which the lexical guard cannot see. Three escape
// shapes are covered, one per path component the lookup walks:
//
//	(1) the object directory itself is a symlink out of the dump,
//	(2) the object's Forms/ directory is a symlink out of the dump,
//	(3) the leaf Form.xml is a symlink to an outside file.
//
// Each must fail CLOSED: the outside marker must never appear in a parsed form,
// and case (3) additionally must not answer as an existence oracle (a caller
// must not learn that an arbitrary outside path exists by seeing the form name
// listed). A legitimate real-directory lookup in the SAME dump root must keep
// working, which also proves the fixtures reach the guarded code at all.
func TestFindFormFiles_SymlinkContainment(t *testing.T) {
	const (
		outsideMarker = "OUTSIDE_FORM_MARKER_4b71e9"
		controlMarker = "INROOT_FORM_MARKER_2ad5c3"
	)

	dumpRoot := t.TempDir()
	outsideDir := t.TempDir() // sibling temp dir; its realpath is NOT under dumpRoot

	// The outside object an attacker wants to read: a complete Forms tree plus a
	// bare form file for the leaf-symlink case.
	writeFormXML(t, outsideDir, "Украдено/Forms/ФормаКража/Ext/Form.xml", outsideMarker)
	outsideBareForm := writeFormXML(t, outsideDir, "секрет.xml", outsideMarker)

	// Positive control: an ordinary, real-directory object inside the dump.
	writeFormXML(t, dumpRoot, "Documents/Контроль/Forms/ФормаДокумента/Ext/Form.xml", controlMarker)

	// (1) The object directory is a symlink pointing out of the dump.
	symlinkOrSkip(t,
		filepath.Join(outsideDir, "Украдено"),
		filepath.Join(dumpRoot, filepath.FromSlash("Documents/СсылкаОбъект")))

	// (2) The object's Forms/ directory is a symlink pointing out of the dump.
	symlinkOrSkip(t,
		filepath.Join(outsideDir, filepath.FromSlash("Украдено/Forms")),
		filepath.Join(dumpRoot, filepath.FromSlash("Documents/СсылкаФормы/Forms")))

	// (3) The leaf Form.xml is a symlink to an outside file, inside otherwise
	// real in-dump directories.
	symlinkOrSkip(t, outsideBareForm,
		filepath.Join(dumpRoot, filepath.FromSlash("Documents/СсылкаФайл/Forms/ФормаФайл/Ext/Form.xml")))

	// --- Positive control FIRST: proves the fixture layout reaches the code. ---
	ok, n := markerReachable(t, dumpRoot, "Document", "Контроль", controlMarker)
	if !ok {
		t.Fatalf("positive control did not resolve: a legitimate in-dump form must still be found and parsed (found %d forms)", n)
	}
	if n != 1 {
		t.Errorf("positive control: expected exactly 1 form, got %d", n)
	}

	// --- Every escape shape must fail closed on CONTENT. ---
	for _, tc := range []struct {
		name       string
		objectName string
	}{
		{"object-dir-symlink", "СсылкаОбъект"},
		{"forms-dir-symlink", "СсылкаФормы"},
		{"leaf-form-xml-symlink", "СсылкаФайл"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaked, _ := markerReachable(t, dumpRoot, "Document", tc.objectName, outsideMarker)
			if leaked {
				t.Errorf("SECURITY: get_form_structure returned a form from OUTSIDE the dump root (marker %q reached the caller via object %q)",
					outsideMarker, tc.objectName)
			}
		})
	}

	// --- The leaf case must not answer as an existence oracle either. ---
	// Even with the content refused, listing the form name would confirm to the
	// caller that the outside path exists.
	t.Run("no-existence-oracle", func(t *testing.T) {
		forms, err := FindFormFiles(dumpRoot, "Document", "СсылкаФайл")
		if err != nil {
			return // a refusal is a fine outcome; it discloses nothing
		}
		if _, listed := forms["ФормаФайл"]; listed {
			t.Errorf("SECURITY: escaping leaf Form.xml symlink was reported as an existing form, confirming an outside path to the caller: %v", forms)
		}
	})
}

// TestParseFormXML_RefusesSymlinkedFormFile covers ParseFormXML's own guard
// directly. FindFormFiles already refuses to LIST an escaping leaf symlink, so
// the containment test above never reaches this code path; this test drives it
// on purpose, because the guard exists for the window in which a vetted
// Form.xml is swapped for a symlink before it is read.
//
// The assertion is on CONTENT: the outside marker must not come back. It does
// not assert any error text, so the guard cannot pass by being reworded.
func TestParseFormXML_RefusesSymlinkedFormFile(t *testing.T) {
	const (
		outsideMarker = "OUTSIDE_SWAPPED_MARKER_c47a10"
		realMarker    = "REAL_FILE_MARKER_5b3e88"
	)

	dumpRoot := t.TempDir()
	outsideDir := t.TempDir()

	outsideForm := writeFormXML(t, outsideDir, "секрет.xml", outsideMarker)

	// Positive control: an ordinary regular form file still parses fully.
	realForm := writeFormXML(t, dumpRoot, "Documents/Док/Forms/Ф/Ext/Form.xml", realMarker)
	info, err := ParseFormXML(realForm)
	if err != nil {
		t.Fatalf("a plain regular form file must still parse: %v", err)
	}
	if !hasElement(info.Elements, realMarker, "InputField") {
		t.Fatalf("positive control lost its content: %+v", info.Elements)
	}

	// The swapped-in symlink must not yield the outside file's content.
	swapped := filepath.Join(dumpRoot, filepath.FromSlash("Documents/Док/Forms/Ф/Ext/Swapped.xml"))
	symlinkOrSkip(t, outsideForm, swapped)

	// Assert the fixture is genuinely the shape under test, so this test can
	// never pass vacuously on a broken fixture (a missing file would make
	// ParseFormXML fail for the wrong reason and look like a refusal).
	if st, err := os.Lstat(swapped); err != nil {
		t.Fatalf("fixture not created: %v", err)
	} else if st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("fixture is not a symlink, the guard under test would not be exercised")
	}
	if _, err := os.Stat(swapped); err != nil {
		t.Fatalf("fixture symlink does not resolve, so following it could not have leaked anyway: %v", err)
	}

	got, perr := ParseFormXML(swapped)
	if perr == nil {
		for _, e := range got.Elements {
			if strings.Contains(e.Name, outsideMarker) {
				t.Fatalf("SECURITY: ParseFormXML followed a symlink and returned the outside file's content (marker %q)", outsideMarker)
			}
		}
	}
}

// TestFindFormFiles_InRootRealFormsStillServed is the standalone positive
// control: containment must not break ordinary dumps. A plain object with two
// real forms keeps resolving to two readable, parseable Form.xml files.
func TestFindFormFiles_InRootRealFormsStillServed(t *testing.T) {
	const marker = "INROOT_ONLY_MARKER_8e2f01"

	dumpRoot := t.TempDir()
	writeFormXML(t, dumpRoot, "Documents/ТестДок/Forms/ФормаДокумента/Ext/Form.xml", marker)
	writeFormXML(t, dumpRoot, "Documents/ТестДок/Forms/ФормаСписка/Ext/Form.xml", marker)

	forms, err := FindFormFiles(dumpRoot, "Document", "ТестДок")
	if err != nil {
		t.Fatalf("legitimate lookup must not be refused: %v", err)
	}
	if len(forms) != 2 {
		t.Fatalf("expected 2 forms, got %d: %v", len(forms), forms)
	}
	for name, path := range forms {
		info, perr := ParseFormXML(path)
		if perr != nil {
			t.Fatalf("legitimate form %q must still parse: %v", name, perr)
		}
		if !hasElement(info.Elements, marker, "InputField") {
			t.Errorf("legitimate form %q lost its content: %+v", name, info.Elements)
		}
	}
}
