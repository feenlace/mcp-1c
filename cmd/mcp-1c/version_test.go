package main

import (
	"os"
	"path/filepath"
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
	const want = "0.4.1"

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
