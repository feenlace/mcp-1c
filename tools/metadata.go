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

// metadataCategory maps a JSON key from 1C response to a human-readable title.
type metadataCategory struct {
	key   string // JSON key from 1C (e.g. "Справочники")
	title string // Display name (e.g. "Справочники")
}

// metadataCategories defines all known 1C metadata categories in display order.
var metadataCategories = []metadataCategory{
	{"Справочники", "Справочники"},
	{"Документы", "Документы"},
	{"Перечисления", "Перечисления"},
	{"Обработки", "Обработки"},
	{"Отчеты", "Отчёты"},
	{"РегистрыСведений", "Регистры сведений"},
	{"РегистрыНакопления", "Регистры накопления"},
	{"РегистрыБухгалтерии", "Регистры бухгалтерии"},
	{"РегистрыРасчета", "Регистры расчёта"},
	{"ПланыСчетов", "Планы счетов"},
	{"ПланыВидовХарактеристик", "Планы видов характеристик"},
	{"ПланыВидовРасчета", "Планы видов расчёта"},
	{"ПланыОбмена", "Планы обмена"},
	{"БизнесПроцессы", "Бизнес-процессы"},
	{"Задачи", "Задачи"},
	{"ОпределяемыеТипы", "Определяемые типы"},
	{"ЖурналыДокументов", "Журналы документов"},
	{"Константы", "Константы"},
	{"ОбщиеМодули", "Общие модули"},
	{"ОбщиеФормы", "Общие формы"},
	{"ОбщиеКоманды", "Общие команды"},
	{"ОбщиеМакеты", "Общие макеты"},
	{"Роли", "Роли"},
	{"Подсистемы", "Подсистемы"},
	{"РегулярныеЗадания", "Регулярные задания"},
	{"ВебСервисы", "Веб-сервисы"},
	{"HTTPСервисы", "HTTP-сервисы"},
}

// MetadataTool returns the MCP tool definition for get_metadata_tree.
func MetadataTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "get_metadata_tree",
		Title:       "Дерево метаданных конфигурации",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Список всех объектов конфигурации 1С по категориям: справочники, документы, регистры, перечисления, обработки и т.д. " +
			"Без фильтра: сводка (категории и количество), с filter: полный перечень объектов категории. " +
			"Используй когда нужно узнать какие объекты есть в базе. " +
			"Вызывай первым при работе с незнакомой конфигурацией. " +
			"Имена объектов из результата используются в get_object_structure и в запросах.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"filter": {
					"type": "string",
					"description": "Категория метаданных для фильтрации: Справочники, Документы, Перечисления, Обработки, Отчеты, РегистрыСведений, РегистрыНакопления, ОбщиеМодули и др. Если не указан - возвращаются все категории (только названия категорий и количество)."
				}
			}
		}`),
	}
}

// metadataInput holds optional filter for metadata handler.
type metadataInput struct {
	Filter string `json:"filter"`
}

// noiseSuffixes lists suffixes of auto-generated objects to filter out.
var noiseSuffixes = []string{
	"ПрисоединенныеФайлы",
	"ПрисоединённыеФайлы",
}

// isNoise returns true if the object name is auto-generated noise.
func isNoise(name string) bool {
	for _, suffix := range noiseSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// filterNoise removes auto-generated objects from the metadata tree.
func filterNoise(tree map[string][]string) {
	for key, items := range tree {
		filtered := items[:0]
		for _, name := range items {
			if !isNoise(name) {
				filtered = append(filtered, name)
			}
		}
		tree[key] = filtered
	}
}

// NewMetadataHandler returns a ToolHandler that fetches the metadata tree from 1C.
func NewMetadataHandler(client *onec.Client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input metadataInput
		if req.Params.Arguments != nil {
			json.Unmarshal(req.Params.Arguments, &input) //nolint:errcheck
		}

		var tree map[string][]string
		if err := client.Get(ctx, "/metadata", &tree); err != nil {
			return nil, fmt.Errorf("fetching metadata from 1C: %w", err)
		}

		filterNoise(tree)

		if input.Filter != "" {
			filtered := make(map[string][]string)
			if items, ok := tree[input.Filter]; ok {
				filtered[input.Filter] = items
			}
			tree = filtered
			return textResult(formatMetadataTree(tree)), nil
		}

		// Without filter — return only category names and counts.
		return textResult(formatMetadataSummary(tree)), nil
	}
}

// formatMetadataTree formats the metadata tree as markdown text.
// Known categories are rendered first in a stable order, then any unknown
// categories are appended at the end for forward compatibility.
func formatMetadataTree(tree map[string][]string) string {
	var b strings.Builder
	b.WriteString("# Метаданные конфигурации 1С\n\n")

	// Track which keys have been rendered.
	rendered := make(map[string]bool, len(metadataCategories))

	// Render known categories in defined order.
	for _, cat := range metadataCategories {
		items, ok := tree[cat.key]
		if !ok {
			continue
		}
		rendered[cat.key] = true
		if len(items) == 0 {
			continue
		}
		writeSection(&b, cat.title, items)
	}

	// Collect and render unknown categories (forward compatibility).
	var unknown []string
	for key := range tree {
		if !rendered[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)

	for _, key := range unknown {
		items := tree[key]
		if len(items) == 0 {
			continue
		}
		writeSection(&b, key, items)
	}

	return b.String()
}

// formatMetadataSummary returns a compact summary: category names with object counts.
func formatMetadataSummary(tree map[string][]string) string {
	var b strings.Builder
	b.WriteString("# Метаданные конфигурации 1С (сводка)\n\n")
	b.WriteString("Для получения списка объектов вызови get_metadata_tree с параметром filter.\n\n")

	for _, cat := range metadataCategories {
		items, ok := tree[cat.key]
		if !ok || len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- **%s** (%d) — filter=%q\n", cat.title, len(items), cat.key)
	}

	// Unknown categories.
	var unknown []string
	rendered := make(map[string]bool, len(metadataCategories))
	for _, cat := range metadataCategories {
		rendered[cat.key] = true
	}
	for key := range tree {
		if !rendered[key] && len(tree[key]) > 0 {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		fmt.Fprintf(&b, "- **%s** (%d) — filter=%q\n", key, len(tree[key]), key)
	}

	return b.String()
}

// writeSection writes a markdown section with the given title and items.
func writeSection(b *strings.Builder, title string, items []string) {
	fmt.Fprintf(b, "## %s\n", title)
	for _, name := range items {
		b.WriteString("- ")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}
