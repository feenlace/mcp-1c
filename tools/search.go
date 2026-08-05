package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 500
)

// SearchCodeTool returns the MCP tool definition for search_code.
func SearchCodeTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "search_code",
		Title:       "Поиск по коду модулей",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Полнотекстовый поиск по коду всех модулей конфигурации 1С. " +
			"Поддерживает три режима: smart (полнотекстовый с ранжированием BM25, " +
			"по умолчанию), regex (регулярные выражения), exact (точная подстрока). " +
			"Фильтрация по типу метаданных (category) и типу модуля (module). " +
			"BSL-синонимы: поиск по английским именам находит русские и наоборот " +
			"(StrFind -> СтрНайти, Procedure -> Процедура). " +
			"Работает по локальной выгрузке конфигурации (DumpConfigToFiles). " +
			"Режим smart (по умолчанию) для поиска по смыслу, regex для точных паттернов. " +
			"Фильтруй по category и module для сужения результатов.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Поисковый запрос. В режиме smart — слова для полнотекстового поиска. В режиме regex — регулярное выражение (Go regexp). В режиме exact — точная подстрока (регистронезависимо)."
				},
				"limit": {
					"type": "integer",
					"description": "Максимальное количество результатов (по умолчанию 50, максимум 500)"
				},
				"category": {
					"type": "string",
					"description": "Фильтр по типу метаданных: Документ, Справочник, ОбщийМодуль, Обработка, Отчет, РегистрСведений, РегистрНакопления и т.д. Значение чувствительно к регистру (например, 'Документ', не 'документ')."
				},
				"module": {
					"type": "string",
					"description": "Фильтр по типу модуля: МодульОбъекта, МодульМенеджера, МодульФормы, МодульНабораЗаписей, МодульКоманды, Модуль. Значение чувствительно к регистру (например, 'МодульОбъекта', не 'модульобъекта')."
				},
				"mode": {
					"type": "string",
					"enum": ["smart", "regex", "exact"],
					"description": "Режим поиска. smart — полнотекстовый с BM25-ранжированием и поддержкой BSL-синонимов (по умолчанию). regex — регулярное выражение. exact — точная подстрока."
				}
			},
			"required": ["query"]
		}`),
	}
}

// emptyIndexMessage is what search_code answers when the index holds no modules at
// all. It is a named constant because it is the FIRST thing a new user sees when they
// make the commonest first mistake, and because all three search modes must give the
// same answer to the same question.
//
// WHAT IT HAS TO DO. An empty index is not an error and not a failed search: it is a
// configuration that has not been dumped, or a --dump pointing somewhere else. Both
// are things the user can check, so both are named. What it must NOT do is read as a
// malfunction, which is exactly how «bleve search: cannot perform operation on empty
// alias» read, and that was the shipped behaviour of the smart mode.
//
// It says «пуст» about the INDEX and not about the configuration, because an empty
// index is all the server observed. A configuration that exists and was never dumped
// produces the same empty index as a path that points at nothing.
//
// Customer-facing RU: no тире.
const emptyIndexMessage = "Индекс пуст: в каталоге, указанном в --dump, не найдено ни одного " +
	"файла .bsl.\n\nПроверьте два момента:\n" +
	"1. Указан ли в --dump путь к каталогу выгрузки конфигурации.\n" +
	"2. Выполнена ли сама выгрузка: Конфигуратор, Конфигурация -> Выгрузить конфигурацию в файлы."

// NewSearchCodeHandler returns a ToolHandler that searches BSL code in a local dump.
//
// The handler is wrapped so every answer it gives carries the index-protection
// notice while the served generation is unprotected. search_code IS the index, so
// this is the surface where that state has to be visible; see index_notice.go.
func NewSearchCodeHandler(index *dump.Index) mcp.ToolHandler {
	return withIndexProtectionNotice(index, WithToolErrors(headingSearch, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input searchCodeInput
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, InvalidParams(argumentDecodeError(err))
		}
		if input.Query == "" {
			return nil, fmt.Errorf("query is required")
		}

		limit := clampLimit(input.Limit, defaultSearchLimit, maxSearchLimit)
		var mode dump.SearchMode
		switch input.Mode {
		case "regex":
			mode = dump.SearchModeRegex
		case "exact":
			mode = dump.SearchModeExact
		case "smart", "":
			mode = dump.SearchModeSmart
		default:
			return nil, fmt.Errorf("unknown mode: %q (allowed: smart, regex, exact)", input.Mode)
		}

		matches, stats, err := index.SearchWithStats(dump.SearchParams{
			Query:    input.Query,
			Category: input.Category,
			Module:   input.Module,
			Mode:     mode,
			Limit:    limit,
		})
		if err != nil {
			return nil, fmt.Errorf("search: %w", err)
		}

		if stats.Total == 0 && index.ModuleCount() == 0 {
			return textResult(emptyIndexMessage), nil
		}

		return textResult(FormatSearchResultWithStats(matches, stats, input.Query, mode, nil)), nil
	}))
}

// MatchDisplay holds the display name and optional prefix for a search match.
type MatchDisplay struct {
	Prefix      string // e.g. "[Расш] " for extension modules
	DisplayName string // module name shown to the user
}

// MatchDisplayFunc transforms a module name into a display name with an
// optional prefix. When nil is passed to FormatSearchResult, the module
// name is used as-is with no prefix (default community behavior).
type MatchDisplayFunc func(moduleName string) MatchDisplay

// FormatSearchResult formats search matches into markdown text.
//
// displayFn is optional. When nil, each match's Module field is used as the
// display name with no prefix (community behavior). Callers that need to
// decorate module names (e.g. marking extension modules) can pass a custom
// MatchDisplayFunc.
//
// It carries no shortfall information, so it renders every answer as one whose
// count the body can support. Callers that obtain their count from
// dump.Index.SearchWithStats should call FormatSearchResultWithStats instead;
// this signature is kept for callers outside this module.
func FormatSearchResult(matches []dump.Match, total int, query string, mode dump.SearchMode, displayFn MatchDisplayFunc) string {
	return FormatSearchResultWithStats(matches,
		dump.SearchStats{Total: total, Unit: dump.SearchUnitFor(mode)}, query, mode, displayFn)
}

// countNoun is the RU genitive-plural noun for what a search counted, used
// wherever the number is printed.
//
// THE HEADER USED ONE WORD FOR THREE DIFFERENT QUANTITIES. «(N совпадений)» meant
// modules in smart and lines in regex/exact, and the reader had no way to tell.
// Measured on a 13575-file dump, one query «Процедура», limit 500: smart 11788,
// regex 203718, exact 204795, all rendered by that one sentence. A customer read
// 2150 off it and went looking for 2150 lines.
//
// The unit comes from the SearchStats the engine stamped. A stats value built by
// hand and never filled in resolves through dump.SearchUnitFor, the single mapping
// the engine itself uses, rather than through a default here: a default here is the
// assumption that produced the shared label.
//
// Customer-facing RU: no тире.
func countNoun(unit dump.SearchUnit, mode dump.SearchMode) string {
	if unit == "" {
		unit = dump.SearchUnitFor(mode)
	}
	if unit == dump.SearchUnitLines {
		return "строк"
	}
	return "модулей"
}

// smartOneLinePerModuleNote is the route to the results smart cannot give.
//
// Smart is a BM25 ranking over DOCUMENTS: it selects modules and this formatter
// shows one line from each. That is why a customer searching «Процедура» saw line
// numbers that never went past twenty. They were first occurrences, and the
// definition he wanted, on line 1198 of a module whose first hit is on line 199,
// was not in the answer and nothing said it existed.
//
// The note is printed only when at least one shown module HAS more matching lines
// than the one shown, so it appears exactly when it is actionable and an answer
// that is already complete stays quiet.
//
// Customer-facing RU: no тире.
const smartOneLinePerModuleNote = "> Режим smart ранжирует модули и показывает по одной строке " +
	"из каждого, поэтому число в заголовке считает модули, а не строки. Чтобы получить все " +
	"строки, повторите запрос в режиме exact или regex, при необходимости сузив его фильтрами " +
	"category и module.\n"

// FormatSearchResultWithStats formats search matches into markdown text and
// keeps the answer consistent with its own count.
//
// The count and the matches come from different halves of the search: the count
// from the index, the matches from a render path that re-reads each hit's module
// and drops the ones whose file has changed or vanished (dump.Index.GetContent).
// The drop is right, but silently it produces «(386 совпадений)» over
// «Ничего не найдено». So when a hit was dropped, the header stops presenting the
// number as a plain result count and names it as the index's, next to what the
// body actually holds, and the footer says how many are missing and why.
//
// The two causes of a short answer are reported separately because their
// remedies are opposite: a limit that truncated is fixed by a bigger limit, a
// module that cannot be re-read is fixed by re-running the dump and calling
// reload_dump. Neither remedy does anything for the other cause.
func FormatSearchResultWithStats(matches []dump.Match, stats dump.SearchStats, query string, mode dump.SearchMode, displayFn MatchDisplayFunc) string {
	var b strings.Builder
	noun := countNoun(stats.Unit, mode)

	if stats.Unreadable > 0 {
		fmt.Fprintf(&b, "## Результаты поиска \"%s\" (%s с совпадениями в индексе: %d, показано %d)\n\n",
			query, noun, stats.Total, len(matches))
	} else {
		fmt.Fprintf(&b, "## Результаты поиска \"%s\" (%s с совпадениями: %d)\n\n", query, noun, stats.Total)
	}

	if len(matches) == 0 {
		if stats.Unreadable == 0 {
			b.WriteString("Ничего не найдено.\n")
			return b.String()
		}
		// «Ничего не найдено» answers about the QUERY: it says the code does not
		// contain what was asked for. Here the code did contain it and the files
		// are gone, which is a different fact with a different remedy, so the
		// sentence must not be reused for it.
		b.WriteString("Ни одного совпадения показать не удалось.\n\n")
		b.WriteString(searchShortfallNote(stats, 0, noun))
		return b.String()
	}

	// Does any shown module hold matching lines this answer is not showing? Asked
	// over the matches rather than assumed from the mode, so the note below appears
	// only where there is something further to fetch.
	moreLinesHidden := false
	for _, m := range matches {
		if m.LinesMatched > 1 {
			moreLinesHidden = true
			break
		}
	}

	for _, m := range matches {
		prefix := ""
		displayName := m.Module
		if displayFn != nil {
			d := displayFn(m.Module)
			prefix = d.Prefix
			displayName = d.DisplayName
		}

		// Line 0 means the module matched but no line of its current content
		// could be identified as the hit. Print neither a line number nor a code
		// block: quoting source that does not contain the query would present it
		// as the match.
		lineLabel := fmt.Sprintf("строка %d", m.Line)
		if m.Line == 0 {
			lineLabel = "строка не определена"
		}
		// One line is shown; say how many there are when there are more. A module
		// with a single matching line has nothing further to offer, and printing
		// «1» for it would put a number on every row of every ordinary answer.
		if m.LinesMatched > 1 {
			lineLabel += fmt.Sprintf(", строк с совпадениями в модуле: %d", m.LinesMatched)
		}

		if mode == dump.SearchModeSmart && m.Score > 0 {
			fmt.Fprintf(&b, "### %s%s (%s, score: %.3f)\n", prefix, displayName, lineLabel, m.Score)
		} else {
			fmt.Fprintf(&b, "### %s%s (%s)\n", prefix, displayName, lineLabel)
		}

		if m.Line == 0 {
			b.WriteString("Модуль найден полнотекстовым поиском, точная строка в текущем содержимом файла не определена.\n\n")
			continue
		}

		b.WriteString("```bsl\n")
		b.WriteString(m.Context)
		b.WriteString("\n```\n\n")
	}

	if stats.Total > len(matches) {
		b.WriteString(searchShortfallNote(stats, len(matches), noun))
	}
	if moreLinesHidden {
		b.WriteString(smartOneLinePerModuleNote)
	}

	return b.String()
}

// searchShortfallNote explains why the body carries fewer matches than the index
// counted, per cause and with the remedy that belongs to it.
//
// shown is the number of matches rendered. stats.Unreadable of the hits the limit
// selected were dropped as unreadable; whatever the index counts beyond those two
// never left the index at all and is the ordinary limit truncation.
// noun is what stats.Total counted, in the genitive plural, and it is the SAME
// word the header used: a reader who is told «модулей» above and «совпадений»
// below has been given two labels for one number and learnt nothing from either.
func searchShortfallNote(stats dump.SearchStats, shown int, noun string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "> Показано %d из %d %s.", shown, stats.Total, noun)

	if stats.Unreadable == 0 {
		// Nothing was dropped, so the only cause is the caller's own limit and the
		// wording stays exactly what it has always been.
		b.WriteString(" Уточните поиск или увеличьте limit.\n")
		return b.String()
	}

	fmt.Fprintf(&b, " Ещё %d отобрано, но не показано: эти модули не удалось перечитать, "+
		"файлы изменились или удалены уже после того, как построен индекс. Число в заголовке взято "+
		"из индекса и их всё ещё учитывает. Выполните выгрузку конфигурации заново "+
		"и вызовите reload_dump.", stats.Unreadable)

	if stats.Total > shown+stats.Unreadable {
		b.WriteString(" Остальные совпадения в limit не поместились: уточните поиск или увеличьте limit.")
	}

	b.WriteString("\n")
	return b.String()
}
