package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExtensionFormFixture(t *testing.T, root, extension, object, element string) {
	t.Helper()
	dir := filepath.Join(root, "Расширения", extension)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := "<MetaDataObject><Configuration><Properties>" +
		"<ObjectBelonging>Adopted</ObjectBelonging><Name>" + extension + "</Name>" +
		"<ConfigurationExtensionPurpose>Customization</ConfigurationExtensionPurpose>" +
		"</Properties></Configuration></MetaDataObject>"
	if err := os.WriteFile(filepath.Join(dir, "Configuration.xml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	writeDumpForm(t, dir, "DataProcessors", object, "Форма", formXMLWithTitle("Extension form", element))
}

func TestNewFormStructureHandler_ExtensionFormComposition(t *testing.T) {
	srv := formHTTPServer(t, "Форма", "Live form")
	root := t.TempDir()
	writeExtensionFormFixture(t, root, "Addon", "CriticalStock", "ExtensionOnlyElement")
	for _, formName := range []string{"", "Форма"} {
		result, err := callFormHandler(t, srv.URL, root, "DataProcessor", "CriticalStock", formName)
		if err != nil {
			t.Fatal(err)
		}
		text := resultText(t, result)
		if !strings.Contains(text, "ExtensionOnlyElement") {
			t.Fatalf("composition missing: %s", text)
		}
		if strings.Contains(text, dumpNoteMarker) || strings.Contains(text, root) {
			t.Fatalf("healthy extension composition degraded or leaked path: %s", text)
		}
	}
}

func TestNewFormStructureHandler_ExtensionAmbiguityNoComposition(t *testing.T) {
	srv := formHTTPServer(t, "Форма", "Live form")
	root := t.TempDir()
	writeExtensionFormFixture(t, root, "First", "Shared", "FirstElement")
	writeExtensionFormFixture(t, root, "Second", "Shared", "SecondElement")
	result, err := callFormHandler(t, srv.URL, root, "DataProcessor", "Shared", "")
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "extension_unresolved") || !strings.Contains(text, "Live form") {
		t.Fatalf("ambiguity not explained alongside live heading: %s", text)
	}
	for _, unwanted := range []string{"FirstElement", "SecondElement", root, "Причина not_found"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("ambiguous form response contains %q: %s", unwanted, text)
		}
	}
}

func TestNewFormStructureHandler_ExistingMainNamedFormRemainsAuthoritative(t *testing.T) {
	srv := formHTTPServer(t, "Main", "Live form")
	root := t.TempDir()
	writeDumpForm(t, root, "DataProcessors", "Shared", "Main", formXMLWithTitle("Main form", "MainElement"))
	writeExtensionFormFixture(t, root, "Addon", "Shared", "ExtensionElement")
	result, err := callFormHandler(t, srv.URL, root, "DataProcessor", "Shared", "Форма")
	text := failureText(t, result, err)
	if !strings.Contains(text, "Main") || strings.Contains(text, "ExtensionElement") {
		t.Fatalf("named form crossed main precedence: %s", text)
	}
}
