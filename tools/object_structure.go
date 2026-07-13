package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
					"description": "Тип объекта метаданных: Catalog, Document, Enum, InformationRegister, AccumulationRegister, AccountingRegister, CalculationRegister, ChartOfAccounts, ChartOfCharacteristicTypes, ChartOfCalculationTypes, ExchangePlan, BusinessProcess, Task, DataProcessor, Report, DefinedType, Subsystem. Для Enum дополнительно возвращается поле values со списком значений перечисления. Для DefinedType возвращается поле types с составом типов. Для Subsystem возвращаются поля content (состав подсистемы) и subsystems (дерево вложенных подсистем). Соответствие категориям из get_metadata_tree (мн. число рус. -> ед. число англ.): Справочники->Catalog, Документы->Document, Перечисления->Enum, Обработки->DataProcessor, Отчеты->Report, РегистрыСведений->InformationRegister, РегистрыНакопления->AccumulationRegister, РегистрыБухгалтерии->AccountingRegister, РегистрыРасчета->CalculationRegister, ПланыСчетов->ChartOfAccounts, ПланыВидовХарактеристик->ChartOfCharacteristicTypes, ПланыВидовРасчета->ChartOfCalculationTypes, ПланыОбмена->ExchangePlan, БизнесПроцессы->BusinessProcess, Задачи->Task, ОпределяемыеТипы->DefinedType, Подсистемы->Subsystem."
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

// NewObjectStructureHandler returns a ToolHandler that fetches object structure
// from the live 1C extension. It is exactly the live-only case of
// NewObjectStructureHandlerWithSource: NewObjectStructureHandler(client) ==
// NewObjectStructureHandlerWithSource(client, nil).
func NewObjectStructureHandler(client *onec.Client) mcp.ToolHandler {
	return NewObjectStructureHandlerWithSource(client, nil)
}

// NewObjectStructureHandlerWithSource returns a ToolHandler that can serve some
// object types from an offline source and the rest from the live 1C extension.
//
// When sub is non-nil it is consulted first with the requested (objectType,
// objectName): if it reports handled==true, its result is used verbatim (offline
// path, no HTTP; a source error is surfaced verbatim), which lets a single type
// (e.g. Subsystem) be served from an offline dump; if it reports handled==false,
// the handler falls through to the live request. When sub is nil, every type is
// fetched over HTTP with client.Get(ctx, "/object/<type>/<name>", ...),
// byte-for-byte identical to the legacy live path.
func NewObjectStructureHandlerWithSource(client *onec.Client, sub onec.SubsystemStructFunc) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input objectInput
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, fmt.Errorf("parsing input: %w", err)
		}
		if input.ObjectType == "" || input.ObjectName == "" {
			return nil, fmt.Errorf("object_type and object_name are required")
		}

		if sub != nil {
			obj, handled, err := sub(ctx, input.ObjectType, input.ObjectName)
			if handled {
				if err != nil {
					return nil, err
				}
				return textResult(formatObjectStructure(&obj)), nil
			}
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

	// Ambiguity signal (Subsystem short name matched more than one subsystem): the
	// server could not resolve a single object and returned the full-name
	// candidates instead. Surface them as a clear RU signal so the caller can
	// retry with a unique full name. Customer-facing RU: no em/en dash.
	if len(obj.Ambiguous) > 0 {
		names := append([]string(nil), obj.Ambiguous...)
		sort.Strings(names)
		fmt.Fprintf(&b, "# Неоднозначное имя подсистемы (%d)\n\n", len(names))
		b.WriteString("Короткому имени соответствует несколько подсистем. Уточните запрос, указав полное имя:\n")
		for _, n := range names {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		// A partial parse (a dropped subsystem) can coincide with an ambiguous short
		// name; render the diagnostics here too so the drop warning is not lost on
		// this early return. Without this, the ambiguity page silently swallows the
		// warning the non-ambiguous path below reports.
		writeObjectWarnings(&b, obj)
		return b.String()
	}

	fmt.Fprintf(&b, "# %s (%s)\n\n", obj.Name, obj.Synonym)
	writeObjectWarnings(&b, obj)

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

	if len(obj.Types) > 0 {
		// Deterministic order: the platform's .Типы() iteration order is not
		// guaranteed, so sort the composition in place for stable output (and a
		// stable JSON types slice for any downstream consumer).
		sort.Strings(obj.Types)
		b.WriteString("## Состав типа\n")
		for _, t := range obj.Types {
			fmt.Fprintf(&b, "- %s\n", t)
		}
		b.WriteByte('\n')
	}

	// Subsystem (object_type="Subsystem"): direct member composition. Sorted in
	// place for stable output, ASCII list markers only (no тире).
	if len(obj.Content) > 0 {
		sort.Strings(obj.Content)
		b.WriteString("## Состав\n")
		for _, c := range obj.Content {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteByte('\n')
	}

	// Subsystem child tree, rendered recursively with per-depth indentation.
	if len(obj.Subsystems) > 0 {
		b.WriteString("## Подсистемы\n")
		writeSubsystemTree(&b, obj.Subsystems, 0)
		b.WriteByte('\n')
	}

	return b.String()
}

// writeObjectWarnings emits a short RU diagnostics line when the 1C subsystem
// tree builder reported non-fatal warnings (a subsystem's Состав, full name or
// child recursion threw while being collected and was truncated), so a degraded
// or partial membership view is visible to the caller instead of being silently
// trusted as complete. Mirrors analyze_subsystems.writeForestWarnings.
// Customer-facing RU: no em/en dash.
func writeObjectWarnings(b *strings.Builder, obj *onec.ObjectStructure) {
	if len(obj.Warnings) == 0 {
		return
	}
	fmt.Fprintf(b, "> Диагностика: состав подсистемы неполный, предупреждений: %d. Причины: %s\n\n",
		len(obj.Warnings), strings.Join(obj.Warnings, "; "))
}

// writeSubsystemTree renders a subsystem tree as an indented ASCII list. Each
// node header is bold; its member composition and child subsystems are indented
// one level deeper. Nodes and their Состав are sorted for deterministic output
// (intentional UTF-8 byte-order sort, not linguistic collation: the output is
// machine-consumed and must be stable across runs and platforms). Each node's
// full metadata name is surfaced in brackets when present, because it is the
// only unique key when two nested subsystems share a short name under different
// roots. No em/en-dash is emitted (no-тире rule).
func writeSubsystemTree(b *strings.Builder, nodes []onec.SubsystemNode, depth int) {
	sorted := append([]onec.SubsystemNode(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	indent := strings.Repeat("  ", depth)
	for _, n := range sorted {
		line := fmt.Sprintf("%s- **%s**", indent, n.Name)
		if n.Synonym != "" {
			line += fmt.Sprintf(" (%s)", n.Synonym)
		}
		if n.FullName != "" {
			line += fmt.Sprintf(" [%s]", n.FullName)
		}
		fmt.Fprintf(b, "%s\n", line)

		content := append([]string(nil), n.Content...)
		sort.Strings(content)
		for _, c := range content {
			fmt.Fprintf(b, "%s  - %s\n", indent, c)
		}

		if len(n.Subsystems) > 0 {
			writeSubsystemTree(b, n.Subsystems, depth+1)
		}
	}
}
