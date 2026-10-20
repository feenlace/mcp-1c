package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/feenlace/mcp-1c/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFeature07ProjectRootAndRecovery(t *testing.T) {
	source, project := t.TempDir(), t.TempDir()
	xsd := []byte(`<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"/>`)
	if err := os.WriteFile(filepath.Join(project, "own.xsd"), xsd, 0600); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 32, 24))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "own.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	session, _, cleanup := connectLoggedSession(t, NewWithOptions("1.2.3", onec.NewClient("http://must-not-call.invalid", "", ""), nil, Options{SourceRoot: source, ProjectRoot: project, Passport: tools.PassportContext{Mode: tools.ModeOffline}}), func() {})
	defer cleanup()
	ctx := context.Background()
	cases := []struct {
		name string
		args any
	}{
		{"form_patterns", map[string]any{"selection": "document"}},
		{"register_schema", tools.SchemaRegistrationRequest{XSDPath: "own.xsd", Version: "own-1", ManifestPath: "schemas.json"}},
		{"image_grid", tools.ImageGridRequest{InputPath: "own.png", OutputPath: "grid.png", Spacing: 16}},
	}
	for _, c := range cases {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: c.name, Arguments: c.args})
		if err != nil || result.IsError {
			t.Fatalf("%s: %+v %v", c.name, result, err)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(project, "schemas.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registered map[string]any
	if err := json.Unmarshal(manifest, &registered); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"schemas.json", "grid.png"} {
		if _, err := os.Stat(filepath.Join(source, name)); !os.IsNotExist(err) {
			t.Fatalf("wrote SourceRoot A: %s", name)
		}
	}
	// A collision is visible; retry with a new output succeeds without overwriting the first.
	r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "image_grid", Arguments: cases[2].args})
	if err != nil || !r.IsError {
		t.Fatalf("collision: %+v %v", r, err)
	}
	r, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "image_grid", Arguments: tools.ImageGridRequest{InputPath: "own.png", OutputPath: "retry.png", Spacing: 16}})
	if err != nil || r.IsError {
		t.Fatalf("recovery: %+v %v", r, err)
	}
}
