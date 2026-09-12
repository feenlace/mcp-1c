package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bslVersionRe matches the literal the shipped module answers /mcp/version with.
var bslVersionRe = regexp.MustCompile(`Результат\.Вставить\("version", "([^"]+)"\);`)

// TestHandleVersion_AnswersWhatTheExtensionAnswers pins the stand-in to the
// extension it stands in for.
//
// This number said 0.3.0 while the build required a later one, so every start
// driven against the stand-in logged that the extension was older than the
// build requires, and an operator read a product state that was not true.
// Nothing compared the two: the extension version is held in step by
// TestExpectedExtensionVersion_MatchesBSL and
// TestExtensionVersion_MatchesConfigurationXML over in cmd/mcp-1c, and this
// copy sat outside both of them.
//
// The version is READ from the shipped module rather than written here again,
// so a bump moves this answer with it and there is no fifth number to forget.
func TestHandleVersion_AnswersWhatTheExtensionAnswers(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "extension", "src",
		"HTTPServices", "MCPService", "Ext", "Module.bsl"))
	if err != nil {
		t.Fatalf("read the shipped module: %v", err)
	}
	m := bslVersionRe.FindStringSubmatch(strings.TrimPrefix(string(raw), "\uFEFF"))
	if m == nil {
		t.Fatal("CONTROL: the shipped module carries no version literal, so this test compares nothing")
	}
	want := m[1]

	req := httptest.NewRequest(http.MethodGet, "/mcp/version", nil)
	rec := httptest.NewRecorder()
	handleVersion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["version"] != want {
		t.Errorf("the stand-in answers version %q while the extension it stands in for answers %q, "+
			"so a start driven against it reports the extension as older than the build requires",
			got["version"], want)
	}
}
