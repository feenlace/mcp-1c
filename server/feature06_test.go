package server

import (
	"context"
	"encoding/json"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/feenlace/mcp-1c/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeature06DeclaredSourceWithoutIndex(t *testing.T) {
	source, project := t.TempDir(), t.TempDir()
	for root, name := range map[string]string{source: "SourceA", project: "ProjectB"} {
		if err := os.WriteFile(filepath.Join(root, "Configuration.xml"), []byte(`<MetaDataObject><Configuration><Properties><Name>`+name+`</Name><Version>1.0.0</Version><Vendor>Vendor</Vendor></Properties><ChildObjects/></Configuration></MetaDataObject>`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client := onec.NewClient("http://must-not-call.invalid", "", "")
	session, _, cleanup := connectLoggedSession(t, NewWithOptions("1.2.3", client, nil, Options{SourceRoot: source, ProjectRoot: project, Passport: tools.PassportContext{Mode: tools.ModeOffline, Edition: "Community", ProductVersion: "1.2.3"}}), func() {})
	defer cleanup()
	for _, name := range []string{"metadata_summary", "environment_passport"} {
		args := json.RawMessage(`{}`)
		if name == "metadata_summary" {
			args = json.RawMessage(`{"kind":"configuration"}`)
		}
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, "SourceA") || strings.Contains(text, "ProjectB") || strings.Contains(text, source) || strings.Contains(text, project) {
			t.Fatalf("wrong root/provenance: %s", text)
		}
	}
	names := listToolNames(t, session)
	for _, name := range names {
		if name == "search_code" || name == "reload_dump" {
			t.Fatal("source-only options registered indexed tools")
		}
	}
	// Malformed A never silently reads valid B.
	_ = os.WriteFile(filepath.Join(source, "Configuration.xml"), []byte(`<broken>`), 0600)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "metadata_summary", Arguments: map[string]any{"kind": "configuration"}})
	if err != nil || !result.IsError || strings.Contains(result.Content[0].(*mcp.TextContent).Text, "ProjectB") {
		t.Fatalf("malformed A fell back: %+v %v", result, err)
	}
}
