package tools

import (
	"fmt"

	"github.com/feenlace/mcp-1c/internal/jsonshape"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// textResult wraps a text string into an MCP tool result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}
}

// objectInput is the common input for tools that operate on a specific metadata object.
type objectInput struct {
	ObjectType string `json:"object_type"`
	ObjectName string `json:"object_name"`
}

// queryLimitInput is the common input for tools that accept a query string with an optional limit.
type queryLimitInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// searchCodeInput is the input for the search_code tool.
type searchCodeInput struct {
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
	Category string `json:"category"`
	Module   string `json:"module"`
	Mode     string `json:"mode"`
}

// clampLimit normalises a user-supplied limit to [defaultVal, maxVal].
func clampLimit(value, defaultVal, maxVal int) int {
	if value <= 0 {
		return defaultVal
	}
	if value > maxVal {
		return maxVal
	}
	return value
}

// argumentDecodeError describes a failed decode of a tool's arguments in the
// vocabulary of JSON.
//
// WHAT IT REPLACES. Every constructor used to answer fmt.Errorf("parsing input:
// %w", err), and encoding/json's own text names Go: measured, search_code given
// {"query":123} produced «parsing input: json: cannot unmarshal number into Go
// struct field searchCodeInput.query of type string». searchCodeInput is an
// unexported type in this package. Naming it tells the caller nothing it can act
// on, and it is a detail of an implementation the caller cannot see; what it can
// act on is that the argument called query has to be a string.
//
// The JSON field path comes from the decoder itself (json.UnmarshalTypeError's
// Field), so it is the name the CALLER used rather than the name this package
// gave the struct field, and the two are not always the same.
//
// Customer-facing RU: no тире.
func argumentDecodeError(err error) error {
	field, want, got, ok := jsonshape.TypeMismatch(err)
	switch {
	case ok && field != "":
		return fmt.Errorf("аргумент %q должен быть: %s, а получено: %s", field, want, got)
	case ok:
		return fmt.Errorf("аргументы должны быть объектом JSON, а получено: %s", got)
	default:
		// A syntax error, a truncated body or an empty one. None of these names
		// a Go type, and each of them is the whole diagnostic, so the decoder's
		// own words are kept.
		return fmt.Errorf("аргументы разобрать не удалось: %v", err)
	}
}
