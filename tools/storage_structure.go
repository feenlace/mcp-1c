package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StorageStructureTool returns the MCP tool definition for get_storage_structure.
func StorageStructureTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  "get_storage_structure",
		Title: "Таблицы СУБД объекта",
		Description: "Получить таблицы, колонки и индексы СУБД (PostgreSQL, MS SQL и др.), в которых платформа 1С хранит объект метаданных: " +
			"какой таблице вида _Reference123 соответствует справочник, какой колонке вида _Fld456 соответствует реквизит, где лежат табличные части, итоги и регистрация изменений. " +
			"Ответ приходит от самой платформы (ПолучитьСтруктуруХраненияБазыДанных), поэтому имена точные для этой базы. " +
			"Используй, когда нужно связать объект 1С с таблицей базы данных: прямой SQL-запрос к базе, разбор плана запроса, анализ размера таблиц, сопоставление имён из журнала СУБД. " +
			"Для состава реквизитов и типов полей вызывай get_object_structure, этот инструмент показывает только хранение.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"object_type": {
					"type": "string",
					"description": "Тип объекта метаданных, как у get_object_structure: Catalog, Document, InformationRegister, AccumulationRegister, AccountingRegister, CalculationRegister, ChartOfAccounts, ChartOfCharacteristicTypes, ChartOfCalculationTypes, ExchangePlan, BusinessProcess, Task, Enum. У обработок, отчётов, определяемых типов и подсистем таблиц хранения нет."
				},
				"object_name": {
					"type": "string",
					"description": "Имя объекта метаданных, например Номенклатура"
				}
			},
			"required": ["object_type", "object_name"]
		}`),
	}
}

// NewStorageStructureHandler returns a ToolHandler that fetches the DBMS storage
// structure of one metadata object from the live 1C extension
// (GET /storage/<type>/<name>). There is no offline path: the table numbers are
// assigned by the platform per infobase and are not in a configuration dump.
func NewStorageStructureHandler(client *onec.Client) mcp.ToolHandler {
	return WithToolErrors(headingStorage, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input objectInput
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, InvalidParams(argumentDecodeError(err))
		}
		if input.ObjectType == "" || input.ObjectName == "" {
			return nil, fmt.Errorf("object_type and object_name are required")
		}

		endpoint := fmt.Sprintf("/storage/%s/%s", input.ObjectType, input.ObjectName)
		var st onec.StorageStructure
		if err := client.Get(ctx, endpoint, &st); err != nil {
			return nil, fmt.Errorf("fetching storage structure from 1C: %w", err)
		}

		return textResult(formatStorageStructure(&st)), nil
	})
}

// formatStorageStructure renders the storage structure as markdown. Every name
// on the page comes from 1C and is contained with inlineCode where it stands
// alone on a heading line (the same containment formatObjectStructure uses).
// Customer-facing RU: no em/en dash.
func formatStorageStructure(st *onec.StorageStructure) string {
	var b strings.Builder

	title := st.FullName
	if title == "" {
		title = st.Name
	}
	fmt.Fprintf(&b, "# Хранение %s в СУБД\n\n", inlineCode(title))

	if len(st.Tables) == 0 {
		b.WriteString("Платформа не вернула ни одной таблицы хранения для этого объекта: у него нет собственных таблиц в СУБД.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "Таблиц: %d. Имена колонок даны так, как их видит СУБД; в PostgreSQL они хранятся в нижнем регистре.\n\n", len(st.Tables))

	for _, t := range st.Tables {
		fmt.Fprintf(&b, "## %s (%s)\n", inlineCode(t.StorageName), t.Purpose)
		if t.TableName != "" {
			fmt.Fprintf(&b, "- Имя в языке запросов: %s\n", inlineCode(t.TableName))
		}
		if t.Metadata != "" {
			fmt.Fprintf(&b, "- Метаданные: %s\n", inlineCode(t.Metadata))
		}
		b.WriteByte('\n')

		if len(t.Fields) > 0 {
			b.WriteString("| Поле | Колонка СУБД | Метаданные |\n")
			b.WriteString("|---|---|---|\n")
			for _, f := range t.Fields {
				fmt.Fprintf(&b, "| %s | %s | %s |\n", cell(f.Name), cell(f.StorageName), cell(f.Metadata))
			}
			b.WriteByte('\n')
		}

		if len(t.Indexes) > 0 {
			b.WriteString("Индексы:\n")
			for _, ix := range t.Indexes {
				cols := make([]string, 0, len(ix.Fields))
				for _, f := range ix.Fields {
					cols = append(cols, f.StorageName)
				}
				if ix.Name != "" {
					fmt.Fprintf(&b, "- %s (%s): %s\n", inlineCode(ix.StorageName), ix.Name, strings.Join(cols, ", "))
				} else {
					fmt.Fprintf(&b, "- %s: %s\n", inlineCode(ix.StorageName), strings.Join(cols, ", "))
				}
			}
			b.WriteByte('\n')
		}
	}

	return b.String()
}

// cell makes one table cell out of a datum from 1C: a pipe or a line break
// inside it would otherwise redraw the table.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
