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
	{"РегламентныеЗадания", "Регламентные задания"},
	{"WebСервисы", "Веб-сервисы"},
	{"HTTPСервисы", "HTTP-сервисы"},
}

// metadataWarningsKey is the /metadata response key the 1C extension uses to report
// collections it could not read (МетаданныеGET). It is a diagnostics channel, not a
// metadata category: it is lifted out of the tree before rendering so it can never be
// mistaken for an unknown category and listed as a filter value.
const metadataWarningsKey = "warnings"

// metadataErrorsKey is the OTHER name the same HTTP service uses for a
// diagnostics array, and lifting it is grounded rather than defensive:
// ПроверкаЗапросаPOST in the extension answers with a key called "errors"
// carrying ОписаниеОшибки() text. A /metadata response that ever carried the
// same key was rendered as a metadata CATEGORY: measured, a 200 answering
//
//	{"Справочники":[…],"errors":["потеряно 109 заданий","потеряно 16 веб-сервисов"]}
//
// produced «- **errors** (2) — filter="errors"» in the summary and, under that
// filter, a section headed «## errors» listing the two losses as though they
// were configuration objects, with IsError = false throughout. A report that
// data was lost must never be presented as the data.
const metadataErrorsKey = "errors"

// metadataDiagnosticKeys is the set lifted out of the tree before anything is
// rendered. It is a set rather than two ifs because the two are the same kind of
// thing and the next one must be added in one place.
var metadataDiagnosticKeys = []string{metadataWarningsKey, metadataErrorsKey}

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
	return WithToolErrors(headingMetadata, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// The decode error is CHECKED, and that is the whole of this branch's
		// job. It used to be discarded, and the argument this tool takes is the
		// one that decides what the answer contains: {"filter":123} failed to
		// decode, left Filter at "", and produced the unfiltered summary, BYTE
		// IDENTICAL to a call that passed no filter at all. A caller could not
		// tell the answer to its question from the answer to a different one.
		//
		// The nil guard stays: no arguments at all is a legitimate call (the
		// filter is optional), while json.Unmarshal of a nil slice is an error.
		var input metadataInput
		if req.Params.Arguments != nil {
			if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
				return nil, InvalidParams(argumentDecodeError(err))
			}
		}

		var tree map[string][]string
		if err := client.Get(ctx, "/metadata", &tree); err != nil {
			return nil, fmt.Errorf("fetching metadata from 1C: %w", err)
		}

		// Lift the diagnostics channels out of the tree BEFORE anything else, so a
		// degraded (partial) metadata tree is reported as a degradation instead of
		// silently passing for a complete one, and never renders as a category.
		var warnings []string
		for _, key := range metadataDiagnosticKeys {
			warnings = append(warnings, tree[key]...)
			delete(tree, key)
		}

		filterNoise(tree)

		if input.Filter != "" {
			filtered := make(map[string][]string)
			if items, ok := tree[input.Filter]; ok {
				filtered[input.Filter] = items
			}
			// The filter is carried into the renderer because an empty result
			// has two different meanings and the renderer cannot tell them
			// apart from the map alone: a category that answered with nothing,
			// and a category the answer never contained. Both used to print a
			// bare header and nothing else, which reads as «this category is
			// empty» for a question 1С never answered.
			return textResult(formatMetadataTree(filtered, warnings, input.Filter)), nil
		}

		// Without filter — return only category names and counts.
		return textResult(formatMetadataSummary(tree, warnings)), nil
	})
}

// writeMetadataWarnings emits a short RU diagnostics line when the 1C extension
// reported collections it could not read (МетаданныеGET catches the throw, skips
// the collection and records its name). Without it a wrong collection literal is
// invisible: the tree simply comes back short. Mirrors
// analyze_subsystems.writeForestWarnings. Customer-facing RU: no em/en dash.
func writeMetadataWarnings(b *strings.Builder, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(b, "> Диагностика: дерево метаданных неполное, пропущено коллекций: %d. Причины: %s\n\n",
		len(warnings), diagnosticCauses(warnings))
}

// unknownKeyNotice introduces the keys this server does not recognise.
//
// Forward compatibility is why they are still shown: a later platform or a later
// extension can add a collection, and dropping it would hide real objects. What
// changed is the CLAIM made about them. They used to be rendered as ordinary
// sections, indistinguishable from «## Справочники», so anything the far side
// put in the response read as configuration content vouched for by this server.
//
// Customer-facing RU: no тире.
const unknownKeyNotice = "> Ключи ниже этот сервер не знает. Он не может подтвердить, " +
	"что в них перечислены объекты конфигурации, и категориями метаданных они не объявлены."

// emptyTreeNotice is what an answer with no categories at all says.
//
// It used to say nothing: the header was printed, the invitation to call with a
// filter was printed, and no category followed, which reads as «the
// configuration has no objects». Measured on three shapes that all produced it
// with IsError = false: a 200 carrying {}, a 200 carrying the JSON literal null,
// and a 200 whose only key was the diagnostics one. None of them is an empty
// configuration and none of them was distinguishable from one.
//
// Customer-facing RU: no тире.
const emptyTreeNotice = "В ответе 1С не оказалось ни одной категории метаданных. " +
	"Это не то же самое, что пустая конфигурация: так же выглядит ответ, из которого " +
	"дерево не попало в результат вовсе.\n\n" +
	"Проверьте:\n" +
	"1. Версию расширения MCP в базе: ответ метода /metadata мог измениться.\n" +
	"2. Права учётной записи из `--user`: коллекции, недоступные ей, расширение пропускает.\n"

// emptyCategoryNotice is what a filter that selected nothing says. The category
// name is quoted with %q so a name chosen by the caller cannot read as prose.
//
// Customer-facing RU: no тире.
const emptyCategoryNotice = "Категории %q в ответе 1С нет. Вызовите get_metadata_tree " +
	"без параметра filter, чтобы увидеть категории, которые в ответе есть.\n"

// formatMetadataTree formats the metadata tree as markdown text.
//
// filter is the category the caller asked for, or "" when it asked for none. It
// is needed because an empty tree is ambiguous by itself: see emptyTreeNotice
// and emptyCategoryNotice, which are the two answers it used to conflate into
// one bare header.
//
// warnings carries the collections 1C could not read; it is rendered right under
// the header so a partial tree cannot be read as a complete one.
func formatMetadataTree(tree map[string][]string, warnings []string, filter string) string {
	var b strings.Builder
	b.WriteString("# Метаданные конфигурации 1С\n\n")
	writeMetadataWarnings(&b, warnings)

	// Track which keys have been rendered.
	rendered := make(map[string]bool, len(metadataCategories))
	shown := 0

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
		shown++
	}

	// Collect the unknown keys (forward compatibility), and say what they are.
	var unknown []string
	for key := range tree {
		if !rendered[key] && len(tree[key]) > 0 {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)

	if len(unknown) > 0 {
		b.WriteString(unknownKeyNotice + "\n\n")
		for _, key := range unknown {
			// AN UNKNOWN KEY IS NOT OURS, and on the filtered path it is byte-identical
			// to the caller's own filter argument, so this heading is reflected exactly
			// as the search header was. The known categories a few lines above pass
			// cat.title, which is a compiled-in constant, and stay outside the span:
			// containment belongs at the call site that knows whose bytes these are,
			// not inside writeSection where the two would be indistinguishable.
			writeSection(&b, inlineCode(key), tree[key])
			shown++
		}
	}

	if shown == 0 {
		if filter != "" {
			fmt.Fprintf(&b, emptyCategoryNotice, filter)
		} else {
			b.WriteString(emptyTreeNotice)
		}
	}

	return b.String()
}

// formatMetadataSummary returns a compact summary: category names with object counts.
// warnings carries the collections 1C could not read (see writeMetadataWarnings).
func formatMetadataSummary(tree map[string][]string, warnings []string) string {
	var b strings.Builder
	b.WriteString("# Метаданные конфигурации 1С (сводка)\n\n")
	writeMetadataWarnings(&b, warnings)
	// Unknown keys are counted first, because whether the invitation to call
	// with a filter is worth printing depends on there being something to
	// filter, and an answer with nothing in it must say so instead.
	rendered := make(map[string]bool, len(metadataCategories))
	for _, cat := range metadataCategories {
		rendered[cat.key] = true
	}
	var unknown []string
	for key := range tree {
		if !rendered[key] && len(tree[key]) > 0 {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)

	known := 0
	for _, cat := range metadataCategories {
		if items, ok := tree[cat.key]; ok && len(items) > 0 {
			known++
		}
	}

	if known == 0 && len(unknown) == 0 {
		b.WriteString(emptyTreeNotice)
		return b.String()
	}

	b.WriteString("Для получения списка объектов вызови get_metadata_tree с параметром filter.\n\n")

	for _, cat := range metadataCategories {
		items, ok := tree[cat.key]
		if !ok || len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- **%s** (%d) — filter=%q\n", cat.title, len(items), cat.key)
	}

	if len(unknown) > 0 {
		b.WriteString("\n" + unknownKeyNotice + "\n\n")
		for _, key := range unknown {
			// A LIST ITEM IS A LINE CONSTRUCT TOO. The heading census that fixed
			// formatMetadataTree asked which format literals OPEN a heading and could
			// not see this one, but a break in the key puts the rest of it at COLUMN
			// ZERO, where `## ` is a heading again. The %q at the end of this same line
			// is escaped by strconv and is why this row read as contained.
			//
			// The replacer and not inlineCode: the `- **` that opens the row is the
			// delimiter, and the end of the line is the one bound it does not survive.
			// Wrapping the key in a code span here would also duplicate the escaped
			// copy the same row already prints as filter=.
			fmt.Fprintf(&b, "- **%s** (%d) — filter=%q\n",
				headingBreakReplacer.Replace(key), len(tree[key]), key)
		}
	}

	return b.String()
}

// writeSection writes a markdown section with the given title and items.
//
// THE ITEMS ARE THE 1C RESPONSE'S, at both call sites, so the break replacer runs
// HERE rather than at the call site: the title is the one argument whose owner
// differs between callers (a compiled-in cat.title, or an unknown key already
// wrapped by its caller), and the items never are.
//
// This one needs no unknown key and no filter. An ordinary object name carrying a
// break ends its list item and writes free markdown into an answer a model reads
// as this server's own words. The replacer is a no-op on every name that does not,
// so an ordinary tree renders byte for byte as it did.
func writeSection(b *strings.Builder, title string, items []string) {
	fmt.Fprintf(b, "## %s\n", title)
	for _, name := range items {
		b.WriteString("- ")
		b.WriteString(headingBreakReplacer.Replace(name))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}
