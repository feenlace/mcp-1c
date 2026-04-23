package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ObjectStructureTool returns the MCP tool definition for get_object_structure.
func ObjectStructureTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  "get_object_structure",
		Title: "Реквизиты и структура объекта",
		Description: "Получить реквизиты, табличные части, измерения, ресурсы, значения перечисления и типы полей объекта метаданных 1С. " +
			"Покажет из чего состоит справочник, документ, регистр, перечисление: какие поля, колонки, свойства, значения. " +
			"Используй когда спрашивают про реквизиты, состав или структуру конкретного объекта (например «какие реквизиты у справочника Валюты» или «какие значения у перечисления СтатусыЗаказов»). " +
			"Результат содержит точные имена реквизитов, табличных частей и значений перечислений для запросов и кода. " +
			"Вызывай перед написанием запросов или кода, работающего с объектом.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"object_type": {
					"type": "string",
					"description": "Тип объекта метаданных: Catalog, Document, Enum, InformationRegister, AccumulationRegister, AccountingRegister, CalculationRegister, ChartOfAccounts, ChartOfCharacteristicTypes, ChartOfCalculationTypes, ExchangePlan, BusinessProcess, Task, DataProcessor, Report. Для Enum дополнительно возвращается поле values со списком значений перечисления."
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
		var input objectInput
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

		return textResult(formatObjectStructure(&obj)), nil
	}
}

// formatObjectStructure formats the object structure as markdown text.
func formatObjectStructure(obj *onec.ObjectStructure) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s (%s)\n\n", obj.Name, obj.Synonym)

	attrSections := []struct {
		title string
		items []onec.Attribute
	}{
		{"Измерения", obj.Dimensions},
		{"Ресурсы", obj.Resources},
		{"Реквизиты", obj.Attributes},
	}
	for _, s := range attrSections {
		if len(s.items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n", s.title)
		for _, attr := range s.items {
			fmt.Fprintf(&b, "- **%s** (%s) — %s\n", attr.Name, attr.Synonym, attr.Type)
		}
		b.WriteByte('\n')
	}

	if len(obj.TabularParts) > 0 {
		b.WriteString("## Табличные части\n")
		for _, tp := range obj.TabularParts {
			fmt.Fprintf(&b, "\n### %s\n", tp.Name)
			for _, attr := range tp.Attributes {
				fmt.Fprintf(&b, "- **%s** (%s) — %s\n", attr.Name, attr.Synonym, attr.Type)
			}
		}
	}

	if len(obj.Values) > 0 {
		b.WriteString("## Значения\n")
		for _, v := range obj.Values {
			fmt.Fprintf(&b, "- **%s** (%s)", v.Name, v.Synonym)
			if v.Comment != "" {
				fmt.Fprintf(&b, " — %s", v.Comment)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}

	return b.String()
}
