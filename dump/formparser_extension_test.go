package dump

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func seedExtensionForm(t *testing.T, root, name, form, body string) string {
	t.Helper()
	path := filepath.Join(root, "DataProcessors", name, "Forms", form, "Ext", "Form.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindFormFiles_ExtensionRoots(t *testing.T) {
	for _, prefix := range []string{filepath.Join("Расширения", "IMZ_IT"), "IMZ_IT", filepath.Join("Exports", "different-directory-name")} {
		t.Run(prefix, func(t *testing.T) {
			root := t.TempDir()
			ext := filepath.Join(root, prefix)
			mkExtensionDump(t, ext, "Configuration.xml", "IMZ_IT")
			want := seedExtensionForm(t, ext, "CriticalStock", "Форма", "<Form/>")
			got, err := FindFormFiles(root, "DataProcessor", "CriticalStock")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got["Форма"] != want {
				t.Fatalf("got %v, want Форма=%s", got, want)
			}
		})
	}
}

func TestFindFormFiles_ExistingMainFormsPreserved(t *testing.T) {
	root := t.TempDir()
	main := seedExtensionForm(t, root, "Shared", "Main", "<Form/>")
	ext := filepath.Join(root, "Расширения", "IMZ_IT")
	mkExtensionDump(t, ext, "Configuration.xml", "IMZ_IT")
	seedExtensionForm(t, ext, "Shared", "Extension", "<Form/>")
	got, err := FindFormFiles(root, "DataProcessor", "Shared")
	if err != nil || len(got) != 1 || got["Main"] != main {
		t.Fatalf("main forms changed: %v %v", got, err)
	}
}

func TestFindFormFiles_DirectExtensionRootAndLazyXML(t *testing.T) {
	root := t.TempDir()
	mkExtensionDump(t, root, "Configuration.xml", "IMZ_IT")
	path := seedExtensionForm(t, root, "CriticalStock", "Форма", "<broken xml")
	got, err := FindFormFiles(root, "DataProcessor", "CriticalStock")
	if err != nil || got["Форма"] != path {
		t.Fatalf("finder parsed XML or missed direct root: %v %v", got, err)
	}
}

func TestFindFormFiles_ExtensionSymlinkDoesNotEscapeDump(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mkExtensionDump(t, outside, "Configuration.xml", "IMZ_IT")
	seedExtensionForm(t, outside, "CriticalStock", "Форма", "<Form/>")
	if err := os.MkdirAll(filepath.Join(root, "Расширения"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "Расширения", "IMZ_IT")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, _ := FindFormFiles(root, "DataProcessor", "CriticalStock")
	if len(got) != 0 {
		t.Fatalf("outside form admitted: %v", got)
	}
}

func TestFindFormFiles_MultipleExtensionObjectsRefused(t *testing.T) {
	root := t.TempDir()
	for _, extName := range []string{"First", "Second"} {
		ext := filepath.Join(root, "Расширения", extName)
		mkExtensionDump(t, ext, "Configuration.xml", extName)
		seedExtensionForm(t, ext, "Shared", extName, "<Form/>")
	}
	got, err := FindFormFiles(root, "DataProcessor", "Shared")
	if len(got) != 0 || err == nil {
		t.Fatalf("ambiguous source selected: %v %v", got, err)
	}
	assertFormSentinel(t, err, "ErrFormExtensionUnresolved")
}

func TestFindFormFiles_DuplicateManifestNamesNotFilteredToWinner(t *testing.T) {
	root := t.TempDir()
	for _, prefix := range []string{"First/Dir", "Second/Dir"} {
		ext := filepath.Join(root, filepath.FromSlash(prefix))
		mkExtensionDump(t, ext, "Configuration.xml", "SameManifestName")
		seedExtensionForm(t, ext, "Shared", "Форма", "<Form/>")
	}
	got, err := FindFormFiles(root, "DataProcessor", "Shared")
	if len(got) != 0 || err == nil {
		t.Fatalf("duplicate roots lost ambiguity: %v %v", got, err)
	}
	assertFormSentinel(t, err, "ErrFormExtensionUnresolved")
}

func TestFindFormFiles_IncompleteExtensionEnumerationRefused(t *testing.T) {
	root := t.TempDir()
	ext := filepath.Join(root, "AExtension")
	mkExtensionDump(t, ext, "Configuration.xml", "Unique")
	seedExtensionForm(t, ext, "Shared", "Форма", "<Form/>")
	for i := 0; i < maxExtensionScan; i++ {
		child := filepath.Join(root, fmt.Sprintf("Z%03d", i))
		if err := os.MkdirAll(child, 0755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FindFormFiles(root, "DataProcessor", "Shared")
	if len(got) != 0 || err == nil {
		t.Fatalf("truncated scan claimed unique source: %v %v", got, err)
	}
	assertFormSentinel(t, err, "ErrFormExtensionUnresolved")
}

func TestFindFormFiles_ExtensionCommonForm(t *testing.T) {
	root := t.TempDir()
	ext := filepath.Join(root, "Расширения", "Addon")
	mkExtensionDump(t, ext, "Configuration.xml", "Addon")
	path := filepath.Join(ext, "CommonForms", "Settings", "Ext", "Form.xml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("<Form/>"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := FindFormFiles(root, "CommonForm", "Settings")
	if err != nil || got["Settings"] != path {
		t.Fatalf("extension common form missing: %v %v", got, err)
	}
}
