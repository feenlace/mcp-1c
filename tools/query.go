package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultQueryLimit = 100
	maxQueryLimit     = 1000
)

// queryInput extends queryLimitInput with query parameters for execute_query.
type queryInput struct {
	queryLimitInput
	Parameters map[string]any `json:"parameters,omitempty"`
}

// QueryTool returns the MCP tool definition for execute_query.
func QueryTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  "execute_query",
		Title: "Выполнить запрос к данным",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Выполнить запрос на языке 1С (ВЫБРАТЬ/SELECT) и получить данные из базы: " +
			"список элементов справочника, документы за период, остатки, обороты, сведения из регистров. " +
			"Используй когда нужно найти, посчитать или вывести конкретные данные. Поддерживает параметры через &Имя. " +
			"Имена таблиц: Справочник.X, Документ.X, РегистрНакопления.X, РегистрСведений.X (единственное число, НЕ Справочники/Документы). " +
			"Перечисления НЕ являются таблицами: используй ЗНАЧЕНИЕ(Перечисление.Имя.Значение) в WHERE/CASE. " +
			"Виртуальные таблицы регистров: РегистрНакопления.X.Остатки(&Период), .Обороты(&НачалоПериода, &КонецПериода), " +
			"РегистрСведений.X.СрезПоследних(&Период). " +
			"Перед выполнением вызови validate_query для проверки синтаксиса. " +
			"Имена полей бери из get_object_structure.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Текст запроса на языке запросов 1С. Только ВЫБРАТЬ/SELECT. Параметры указывай через &ИмяПараметра."
				},
				"limit": {
					"type": "integer",
					"description": "Максимальное количество строк результата (по умолчанию 100, максимум 1000)"
				},
				"parameters": {
					"type": "object",
					"description": "Параметры запроса в виде пар ключ-значение. Ключ — имя параметра без амперсанда. Пример: {\"Контрагент\": \"ООО Ромашка\", \"ДатаНачала\": \"2026-01-01\"}"
				}
			},
			"required": ["query"]
		}`),
	}
}

// queryNotSelectMsg names both keywords, which is also what keeps the existing
// assertion in query_test.go green.
//
// Customer-facing RU: no тире.
const queryNotSelectMsg = "разрешены только запросы, начинающиеся с ВЫБРАТЬ или SELECT. " +
	"Этот инструмент только читает данные, изменяющий запрос 1С не выполнит"

// NewQueryHandler returns a ToolHandler that executes a read-only query in 1C.
func NewQueryHandler(client *onec.Client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input queryInput
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, fmt.Errorf("parsing input: %w", err)
		}
		if input.Query == "" {
			return nil, fmt.Errorf("query is required")
		}

		// Client-side read-only check (hint for LLM callers).
		// Server-side 1C extension enforces read-only execution.
		//
		// The trimmed text is what gets sent, because the extension applies the
		// same rule in the opposite order: it truncates to ten characters and
		// trims afterwards (ЗапросPOST in
		// extension/src/HTTPServices/MCPService/Ext/Module.bsl). ВЫБРАТЬ is
		// seven characters, so leading whitespace eats the keyword and the
		// extension answers 400 for a query that visibly starts with it.
		// Sending the trimmed text removes the divergence at the source.
		queryText := strings.TrimSpace(input.Query)
		prefix := queryText
		if len(prefix) > 30 {
			prefix = prefix[:30]
		}
		upper := strings.ToUpper(prefix)
		if !strings.HasPrefix(upper, "ВЫБРАТЬ") && !strings.HasPrefix(upper, "SELECT") {
			return nil, errors.New(queryNotSelectMsg)
		}

		input.Limit = clampLimit(input.Limit, defaultQueryLimit, maxQueryLimit)

		body := onec.QueryRequest{
			Query:      queryText,
			Limit:      input.Limit,
			Parameters: input.Parameters,
		}
		var result onec.QueryResult
		if err := client.Post(ctx, "/query", body, &result); err != nil {
			return nil, fmt.Errorf("executing query in 1C: %w", err)
		}

		return textResult(formatQueryResult(&result)), nil
	}
}

// formatQueryResult formats the query result as a markdown table.
func formatQueryResult(r *onec.QueryResult) string {
	var b strings.Builder
	b.Grow(len(r.Columns) * len(r.Rows) * 50)

	fmt.Fprintf(&b, "## Результат запроса (%d записей)\n\n", r.Total)

	if len(r.Columns) == 0 || len(r.Rows) == 0 {
		b.WriteString("Нет данных.\n")
		return b.String()
	}

	// Header (escape pipes in column names)
	b.WriteString("| ")
	for i, col := range r.Columns {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(strings.ReplaceAll(col, "|", `\|`))
	}
	b.WriteString(" |\n")

	// Separator
	b.WriteString("|")
	for range r.Columns {
		b.WriteString("---|")
	}
	b.WriteByte('\n')

	// Rows
	for _, row := range r.Rows {
		b.WriteString("| ")
		for i, cell := range row {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(formatCell(cell))
		}
		b.WriteString(" |\n")
	}

	if r.Truncated {
		b.WriteString("\n> Результат усечён. Показаны первые записи. Используйте параметр `limit` для увеличения.\n")
	}

	return b.String()
}

// formatCell converts a JSON-deserialized cell value to string without reflection.
// Pipe characters are escaped so they do not break markdown table formatting.
func formatCell(v any) string {
	var s string
	switch c := v.(type) {
	case string:
		s = c
	case float64:
		if c == float64(int64(c)) {
			s = strconv.FormatInt(int64(c), 10)
		} else {
			s = strconv.FormatFloat(c, 'f', -1, 64)
		}
	case bool:
		s = strconv.FormatBool(c)
	case nil:
		return ""
	default:
		s = fmt.Sprint(v)
	}
	return strings.ReplaceAll(s, "|", `\|`)
}
