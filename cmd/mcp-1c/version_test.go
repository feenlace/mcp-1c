package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bslModulePath returns the path to the MCPService BSL module relative to this
// test's directory (cmd/mcp-1c).
func bslModulePath() string {
	return filepath.Join("..", "..", "extension", "src", "HTTPServices", "MCPService", "Ext", "Module.bsl")
}

// readBSLModule reads the MCPService module and strips a leading UTF-8 BOM so
// that content checks are independent of the byte-order mark.
func readBSLModule(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(bslModulePath())
	if err != nil {
		t.Fatalf("read BSL module: %v", err)
	}
	return strings.TrimPrefix(string(raw), "\uFEFF")
}

// TestExpectedExtensionVersion_MatchesBSL keeps the Go constant and the BSL
// module in lockstep on the extension version.
func TestExpectedExtensionVersion_MatchesBSL(t *testing.T) {
	const want = "0.4.7"

	if expectedExtensionVersion != want {
		t.Errorf("expectedExtensionVersion = %q, want %q", expectedExtensionVersion, want)
	}

	module := readBSLModule(t)

	if !strings.Contains(module, "// Версия расширения: "+want) {
		t.Errorf("Module.bsl: missing version comment %q", "// Версия расширения: "+want)
	}

	if !strings.Contains(module, `Результат.Вставить("version", "`+want+`");`) {
		t.Errorf("Module.bsl: missing version literal for %q", want)
	}
}

// configurationXMLPath returns the path to the extension's Configuration.xml
// relative to this test's directory (cmd/mcp-1c).
func configurationXMLPath() string {
	return filepath.Join("..", "..", "extension", "src", "Configuration.xml")
}

// versionElementRe matches the extension's <Version> property in Configuration.xml,
// in both the filled form and the self-closing empty form 1C writes when the field
// was never set.
var versionElementRe = regexp.MustCompile(`<Version\s*/>|<Version>([^<]*)</Version>`)

// TestExtensionVersion_MatchesConfigurationXML pins the FIFTH copy of the extension
// version: the <Version> property of the extension itself. It is the only copy 1C
// shows in "Конфигурация / Расширения", so leaving it empty (as it was until 0.4.6)
// means the installed extension reports no version at all in the designer while
// /version reports one. The other three copies are pinned by the test above.
func TestExtensionVersion_MatchesConfigurationXML(t *testing.T) {
	raw, err := os.ReadFile(configurationXMLPath())
	if err != nil {
		t.Fatalf("read Configuration.xml: %v", err)
	}

	matches := versionElementRe.FindAllStringSubmatch(string(raw), -1)
	if len(matches) != 1 {
		t.Fatalf("Configuration.xml: found %d <Version> elements, want exactly 1", len(matches))
	}

	got := matches[0][1]
	if got == "" {
		t.Fatalf("Configuration.xml: <Version> is empty; it must carry the extension version %q",
			expectedExtensionVersion)
	}
	if got != expectedExtensionVersion {
		t.Errorf("Configuration.xml <Version> = %q, want %q (same as expectedExtensionVersion)",
			got, expectedExtensionVersion)
	}
}

// TestQueryParamsConvertIsoDates verifies that query parameters are routed
// through the conversion helper so ISO date strings become Дата values.
func TestQueryParamsConvertIsoDates(t *testing.T) {
	module := readBSLModule(t)

	if !strings.Contains(module, "Функция ПреобразоватьЗначениеПараметра") {
		t.Error("Module.bsl: missing helper Функция ПреобразоватьЗначениеПараметра")
	}

	const wantBinding = "Запрос1С.УстановитьПараметр(КлючИЗначение.Ключ, ПреобразоватьЗначениеПараметра(КлючИЗначение.Значение));"
	if !strings.Contains(module, wantBinding) {
		t.Errorf("Module.bsl: missing converted binding %q", wantBinding)
	}

	const oldBinding = "Запрос1С.УстановитьПараметр(КлючИЗначение.Ключ, КлючИЗначение.Значение);"
	if strings.Contains(module, oldBinding) {
		t.Errorf("Module.bsl: still contains raw binding %q", oldBinding)
	}
}
