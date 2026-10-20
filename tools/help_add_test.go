package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHelpAddSourceOnlyAndUnavailableCheck(t *testing.T) {
	for _, mode := range []string{"patch", "patch_and_check"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			data, err := os.ReadFile(filepath.Join("..", "dump", "testdata", "help-native", "HelpProbeReport.xml"))
			if err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(filepath.Join(root, "HelpProbeReport.xml"), data, 0600)
			source, err := dump.InspectExternalSource(context.Background(), root, "HelpProbeReport.xml")
			if err != nil {
				t.Fatal(err)
			}
			input := HelpAddRequest{HelpPatchRequest: dump.HelpPatchRequest{ManifestPath: source.ManifestPath, ExpectedSourceSHA256: source.SourceTreeSHA256, Language: "ru", Content: "Справка"}, Mode: mode, ArtifactPath: "owned.erf"}
			args, _ := json.Marshal(input)
			result, err := NewHelpAddHandler(root, nil)(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "help_add", Arguments: args}})
			if err != nil || result == nil {
				t.Fatalf("handler %+v %v", result, err)
			}
			text := result.Content[0].(*mcp.TextContent).Text
			if !strings.Contains(text, `"status": "patched"`) || !strings.Contains(text, `"ui_open_status": "NOT_RUN"`) {
				t.Fatal(text)
			}
			if mode == "patch_and_check" && (!result.IsError || !strings.Contains(text, "configure_explicit_platform_file_fixture_output_root")) {
				t.Fatal("missing configuration marked verified")
			}
		})
	}
}
