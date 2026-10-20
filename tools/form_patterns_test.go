package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFormPatternsReference(t *testing.T) {
	all, err := FormPatternsText("all")
	if err != nil || len(all) > 16000 || !strings.Contains(all, "версия "+FormPatternsVersion) {
		t.Fatalf("reference: %v %q", err, all)
	}
	for _, selection := range formPatternOrder {
		text, err := FormPatternsText(selection)
		if err != nil || !strings.Contains(all, formPatternText[selection]) || !strings.Contains(text, formPatternText[selection]) {
			t.Fatalf("selection %s: %v", selection, err)
		}
	}
	if _, err := FormPatternsText("invented"); err == nil {
		t.Fatal("unsupported pattern accepted")
	}
	result, err := NewFormPatternsHandler()(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "form_patterns", Arguments: json.RawMessage(`{"selection":"list"}`)}})
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "## Список") {
		t.Fatalf("handler: %+v %v", result, err)
	}
	_, err = NewFormPatternsHandler()(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(`{"selection":"list","action":"anything"}`)}})
	if err == nil {
		t.Fatal("unknown argument accepted")
	}
}
