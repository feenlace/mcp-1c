package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/internal/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// objectStructureInput is the input for the get_object_structure tool.
type objectStructureInput struct {
	ObjectType string `json:"object_type"`
	ObjectName string `json:"object_name"`
}

// ObjectStructureTool returns the MCP tool definition for get_object_structure.
func ObjectStructureTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "get_object_structure",
		Description: "Получить структуру объекта метаданных 1С (документа, справочника, регистра): реквизиты, табличные части и типы. Используй когда нужно узнать какие поля есть у конкретного объекта.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"object_type": {
					"type": "string",
					"description": "Тип объекта метаданных: Document, Catalog, Register"
				},
				"object_name": {
					"type": "string",
					"description": "Имя объекта метаданных, например РеализацияТоваровУслуг"
				}
			},
			"required": ["object_type", "object_name"]
		}`),
	}
}

// NewObjectStructureHandler returns a ToolHandler that fetches object structure from 1C.
func NewObjectStructureHandler(client *onec.Client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input objectStructureInput
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, fmt.Errorf("parsing input: %w", err)
		}
		if input.ObjectType == "" || input.ObjectName == "" {
			return nil, fmt.Errorf("object_type and object_name are required")
		}

		endpoint := fmt.Sprintf("/object/%s/%s", input.ObjectType, input.ObjectName)
		var obj onec.ObjectStructure
		if err := client.Get(ctx, endpoint, &obj); err != nil {
			return nil, fmt.Errorf("fetching object structure from 1C: %w", err)
		}

		text := formatObjectStructure(&obj)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: text},
			},
		}, nil
	}
}

// formatObjectStructure formats the object structure as markdown text.
func formatObjectStructure(obj *onec.ObjectStructure) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s (%s)\n\n", obj.Name, obj.Synonym)

	b.WriteString("## Реквизиты\n")
	for _, attr := range obj.Attributes {
		fmt.Fprintf(&b, "- **%s** (%s) — %s\n", attr.Name, attr.Synonym, attr.Type)
	}
	b.WriteByte('\n')

	if len(obj.TabularParts) > 0 {
		b.WriteString("## Табличные части\n")
		for _, tp := range obj.TabularParts {
			fmt.Fprintf(&b, "\n### %s\n", tp.Name)
			for _, attr := range tp.Attributes {
				fmt.Fprintf(&b, "- **%s** (%s) — %s\n", attr.Name, attr.Synonym, attr.Type)
			}
		}
	}

	return b.String()
}
