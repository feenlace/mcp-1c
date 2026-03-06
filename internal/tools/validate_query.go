package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/internal/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type validateQueryInput struct {
	Query string `json:"query"`
}

// ValidateQueryTool returns the MCP tool definition for validate_query.
func ValidateQueryTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "validate_query",
		Description: "Проверить синтаксис запроса на языке запросов 1С без выполнения. Возвращает список ошибок если запрос некорректен. " +
			"Используй перед execute_query чтобы убедиться в правильности запроса.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Текст запроса на языке запросов 1С для проверки"
				}
			},
			"required": ["query"]
		}`),
	}
}

// NewValidateQueryHandler returns a ToolHandler that validates a 1C query syntax.
func NewValidateQueryHandler(client *onec.Client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input validateQueryInput
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, fmt.Errorf("parsing input: %w", err)
		}
		if input.Query == "" {
			return nil, fmt.Errorf("query is required")
		}

		body := onec.ValidateQueryRequest{Query: input.Query}
		var result onec.ValidateQueryResult
		if err := client.Post(ctx, "/validate-query", body, &result); err != nil {
			return nil, fmt.Errorf("validating query in 1C: %w", err)
		}

		return textResult(formatValidateResult(&result)), nil
	}
}

func formatValidateResult(r *onec.ValidateQueryResult) string {
	var b strings.Builder

	if r.Valid {
		b.WriteString("## Результат проверки\n\n✅ Запрос корректен.\n")
		return b.String()
	}

	b.WriteString("## Результат проверки\n\n❌ Запрос содержит ошибки:\n\n")
	for _, e := range r.Errors {
		fmt.Fprintf(&b, "- %s\n", e)
	}

	return b.String()
}
