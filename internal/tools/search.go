package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/internal/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 500
)

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// SearchCodeTool returns the MCP tool definition for search_code.
func SearchCodeTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "search_code",
		Description: "Поиск по коду всех модулей конфигурации 1С. Ищет подстроку в тексте модулей и возвращает совпадения с контекстом. " +
			"Используй когда нужно найти где используется процедура, переменная или ключевое слово.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Строка для поиска в коде модулей"
				},
				"limit": {
					"type": "integer",
					"description": "Максимальное количество результатов (по умолчанию 50)"
				}
			},
			"required": ["query"]
		}`),
	}
}

// NewSearchCodeHandler returns a ToolHandler that searches code across 1C modules.
func NewSearchCodeHandler(client *onec.Client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input searchInput
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, fmt.Errorf("parsing input: %w", err)
		}
		if input.Query == "" {
			return nil, fmt.Errorf("query is required")
		}
		if input.Limit <= 0 {
			input.Limit = defaultSearchLimit
		}
		if input.Limit > maxSearchLimit {
			input.Limit = maxSearchLimit
		}

		body := onec.SearchRequest{
			Query: input.Query,
			Limit: input.Limit,
		}
		var result onec.SearchResult
		if err := client.Post(ctx, "/search", body, &result); err != nil {
			return nil, fmt.Errorf("searching in 1C: %w", err)
		}

		return textResult(formatSearchResult(&result, input.Query)), nil
	}
}

func formatSearchResult(r *onec.SearchResult, query string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Результаты поиска \"%s\" (%d совпадений)\n\n", query, r.Total)

	if len(r.Matches) == 0 {
		b.WriteString("Ничего не найдено.\n")
		return b.String()
	}

	for _, m := range r.Matches {
		fmt.Fprintf(&b, "### %s (строка %d)\n", m.Module, m.Line)
		b.WriteString("```bsl\n")
		b.WriteString(m.Context)
		b.WriteString("\n```\n\n")
	}

	if r.Total > len(r.Matches) {
		fmt.Fprintf(&b, "> Показано %d из %d совпадений. Уточните поиск или увеличьте limit.\n", len(r.Matches), r.Total)
	}

	return b.String()
}
