package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func MetadataSummaryTool() *mcp.Tool {
	return &mcp.Tool{Name: "metadata_summary", Title: "Краткая сводка XML метаданных", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, Description: "Прочитать краткую сводку объявленной XML-выгрузки без индекса кода: конфигурация, один объект или подсистема. Возвращает относительный источник и SHA-256; не утверждает соответствие текущей базе. Не читает модули и не строит граф влияния.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"kind":{"type":"string","enum":["configuration","object","subsystem"]},"object_type":{"type":"string"},"name":{"type":"string"}},"required":["kind"]}`)}
}
func NewMetadataSummaryHandler(root string) mcp.ToolHandler {
	return WithToolErrors("Сводка XML метаданных не получена", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var sel dump.MetadataSelection
		decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&sel); err != nil {
			return nil, InvalidParams(argumentDecodeError(err))
		}
		if sel.Kind == "" {
			return nil, fmt.Errorf("kind must be configuration, object or subsystem")
		}
		summary, err := dump.ReadMetadataSummary(ctx, root, sel)
		if err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(summary, "", "  ")
		return textResult("# Сводка XML метаданных\n\nИсточник: XML-выгрузка, без обращения к базе.\n\n```json\n" + string(data) + "\n```"), nil
	})
}
