package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ReloadDumpTool returns the MCP tool definition for reload_dump.
//
// The tool takes no arguments on purpose. Its only decision — rebuild or not — is
// made from the dump's own content signature, not from a caller's flag, so there
// is no way to ask it for a pointless 10-second rebuild of an unchanged dump.
func ReloadDumpTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  "reload_dump",
		Title: "Перечитать выгрузку конфигурации",
		// Not read-only: the call rebuilds the on-disk index cache. It is
		// idempotent (a second call on an unchanged dump does nothing) and it
		// never writes to the 1C database or to the dump itself.
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  true,
			DestructiveHint: boolPtr(false),
		},
		Description: "Перечитывает локальную выгрузку конфигурации (DumpConfigToFiles) " +
			"без перезапуска сервера. Вызывай после повторной выгрузки конфигурации. " +
			"Без этого search_code продолжает отвечать по тому состоянию выгрузки, которое " +
			"было на момент запуска сервера: новые модули не находятся, а удалённые и " +
			"изменённые в режиме smart остаются в результатах. " +
			"Индекс строится заново полностью, поэтому вызов занимает столько же времени, " +
			"сколько перезапуск сервера с перестроением индекса (порядка секунд на тысячу " +
			"модулей). Всё это время поиск продолжает отвечать по прежнему состоянию, а " +
			"новое подключается атомарно в конце. Если ни один файл выгрузки не изменился, " +
			"перестроение не выполняется и об этом сообщается явно. Аргументов нет.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {},
			"additionalProperties": false
		}`),
	}
}

// boolPtr returns a pointer to b, for the optional *bool annotation fields.
func boolPtr(b bool) *bool { return &b }

// NewReloadDumpHandler returns a ToolHandler that reloads the dump index in place.
func NewReloadDumpHandler(index *dump.Index) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rep, err := index.Reload()
		if err != nil {
			return nil, fmt.Errorf("перезагрузка выгрузки не выполнена: %w.%s",
				err, searchStateAfterFailedReload(index, err))
		}
		return textResult(FormatReloadReport(rep, index.Dir())), nil
	}
}

// searchStateAfterFailedReload describes what the caller can still do, decided
// from the INDEX'S STATE rather than from the fact that a reload failed.
//
// The reload itself is untouched-by-construction on every error path, so the
// tempting single sentence is "nothing changed, search keeps working". That
// sentence is false the first time a user calls the tool on a freshly started
// server: the initial build is still running, nothing has ever been loaded, and
// dump.Index.Search answers every query with "search index is building". A
// reassurance is worth nothing if it is printed even when it is untrue, and this
// one was printed on the first natural attempt.
//
// Ready() is the predicate the claim is actually about: dump.Index.Search gates
// on exactly that flag and errors while it is false. The closed index passes
// that gate yet serves nothing (its shards are shut), and ErrReloadClosed is how
// the dump package reports the closed flag, so it is excluded here.
//
// The serving branch carries the measured module count. A reassurance that
// names a number cannot quietly stand in for an index that holds nothing.
func searchStateAfterFailedReload(index *dump.Index, err error) string {
	switch {
	case errors.Is(err, dump.ErrReloadClosed):
		return " Индекс закрыт, поиск по выгрузке не отвечает."
	case !index.Ready():
		return " Индекс не тронут, но поиск по выгрузке пока не отвечает: первичная " +
			"загрузка индекса ещё не завершена или завершилась ошибкой."
	default:
		return fmt.Sprintf(" Индекс остался в прежнем состоянии, поиск продолжает работать "+
			"по той выгрузке, которая была загружена раньше (модулей в индексе: %d).",
			index.ModuleCount())
	}
}

// FormatReloadReport renders a reload result as the markdown text the user sees.
// It reports the measured numbers and nothing else: when no work was needed it
// says so instead of describing a rebuild that did not happen.
func FormatReloadReport(rep dump.ReloadReport, dumpDir string) string {
	var b strings.Builder
	seconds := rep.Elapsed.Seconds()

	if !rep.Changed {
		b.WriteString("## Выгрузка не изменилась\n\n")
		fmt.Fprintf(&b, "Ни один файл .bsl в каталоге %s не добавлен, не удалён и не "+
			"перезаписан с момента загрузки индекса. Сверка идёт по относительному пути, "+
			"времени изменения и размеру каждого файла.\n\n", dumpDir)
		b.WriteString("Индекс оставлен как есть, перестроение не выполнялось.\n\n")
		fmt.Fprintf(&b, "Модулей в индексе: %d\n", rep.ModulesAfter)
		fmt.Fprintf(&b, "Затрачено на проверку: %.1f с\n", seconds)
		return b.String()
	}

	b.WriteString("## Индекс выгрузки обновлён\n\n")
	fmt.Fprintf(&b, "Модулей было: %d\n", rep.ModulesBefore)
	fmt.Fprintf(&b, "Модулей стало: %d\n", rep.ModulesAfter)
	fmt.Fprintf(&b, "Затрачено: %.1f с\n\n", seconds)

	if rep.Rebuilt {
		b.WriteString("Индекс перестроен полностью и подключён вместо прежнего.\n\n")
	} else {
		b.WriteString("Готовый индекс для нового состояния выгрузки уже был в кэше, " +
			"поэтому перестроение не потребовалось: он просто подключён вместо прежнего.\n\n")
	}

	b.WriteString("Поиск search_code во всех трёх режимах (smart, regex, exact) " +
		"работает по текущему содержимому выгрузки.\n")
	return b.String()
}
