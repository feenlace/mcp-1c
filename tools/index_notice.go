package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The standing index notices: the user-visible half of "the server never SILENTLY
// serves an index it could not protect, or one that has lost module content".
//
// There are two of them now, and they go through ONE wrapper on purpose. The
// wrapper is still called withIndexProtectionNotice, which records the condition
// that came first rather than the set it carries today; a second wrapper would
// eventually disagree with this one about ordering, about the error path, and
// about which return paths it covers, and the whole reason this is a wrapper and
// not a formatter is that a wrapper has no return path to miss.
//
// The first condition, protection, is documented immediately below. The second,
// a collapsed keyspace, is documented at indexCollapseNotice.
//
// WHY IT IS HERE AND NOT ONLY IN THE LOG. The dump package already reports an
// unprotected serve at slog.Error (claimOrServeUnprotected). That line goes into a
// file under the cache directory, which in stdio mode is the only place it CAN go,
// and which is not where anybody running a search is looking. The whole defect in
// the shipped release was not that a read-only cache serves; it is that it served
// with nothing the user could see. A log nobody reads and no message at all are the
// same thing.
//
// WHY EVERY RESPONSE AND NOT ONCE. The state is not an event, it is a condition
// that holds for as long as the generation is attached, and it can begin or end
// mid-process when Reload swaps the generation. A one-shot announcement is missable
// by exactly the mechanism this exists to defeat: an MCP client that drops early
// context, or a user who reads the fifth answer and not the first. The house
// pattern is the same for every other standing condition in this package (the
// «> Диагностика:» notes and the search shortfall note both re-appear on every
// answer they apply to).
//
// WHY IT IS NOT NOISE. It is attached to the two tools that answer FROM the index
// and to nothing else, so a server whose cache is fine never emits it at all, and
// the tools that talk to live 1C are never decorated with a fact about a cache they
// do not use. Silence is the normal case and is what gives the line its weight: a
// notice on a properly claimed index would be the same defect as a refusal, just
// quieter.

// indexUnprotectedNotice is that line. It states what is true and nothing more: the
// index is being served, it cannot be protected, a peer may remove it while it is
// in use, and here are the two ways to fix that.
//
// It deliberately does NOT assert that the answer below it is complete or that the
// server is working normally. It is attached to failed calls as well as successful
// ones, and a reassurance printed next to an error is worth less than nothing.
//
// Customer-facing RU: no тире.
const indexUnprotectedNotice = "> ВНИМАНИЕ: индекс выгрузки отдаётся без защиты. Серверу не удалось " +
	"записать заявку читателя в каталоге кэша, поэтому другой процесс может удалить этот индекс " +
	"прямо во время работы с ним. Выделите этому серверу отдельный каталог кэша (переменная " +
	"`MCP_1C_CACHE_DIR` или флаг `--cache-dir`) либо сделайте текущий каталог кэша доступным для записи.\n"

// indexClaimLostNotice is the same warning about the other way into the same state:
// the claim WAS written, and this server can no longer refresh it.
//
// IT IS A SEPARATE SENTENCE BECAUSE THE FIRST ONE WOULD BE FALSE HERE. «Серверу не
// удалось записать заявку читателя» describes a cache that refused the write; this
// server's cache accepted it, and the entry then stopped being refreshable. The
// remedy differs with it: making the cache writable fixes nothing when it already
// was, so what is offered is a restart, which takes a fresh claim, and the separate
// cache directory that stops a co-located process reaching this one's generations.
//
// It says what was OBSERVED and stops there. The touch failed and this server is no
// longer counted among the holders; who removed the entry, and whether it was
// removed at all rather than merely become untouchable, is not something the server
// measured, and a notice that named a cause it did not measure would be a guess in
// the user's face.
//
// The first sentence is deliberately identical to the one above, because the fact it
// states is identical and it is the sentence a reader has to act on.
//
// Customer-facing RU: no тире.
const indexClaimLostNotice = "> ВНИМАНИЕ: индекс выгрузки отдаётся без защиты. Заявку читателя, " +
	"которую сервер записал в каталоге кэша, больше не удаётся обновить, поэтому сервер уже не " +
	"числится среди тех, кто использует этот индекс, и другой процесс может удалить индекс прямо " +
	"во время работы с ним. Перезапустите сервер либо выделите ему отдельный каталог кэша " +
	"(переменная `MCP_1C_CACHE_DIR` или флаг `--cache-dir`).\n"

// indexProtectionNotice picks the line that is TRUE of the state, or "" when there
// is nothing to say.
//
// The state arrives as ONE value read in ONE atomic load (dump.Index.Unprotected),
// so the flag and the reason always describe the same moment. Asking the index twice
// instead would let a reload land between the two questions and produce the sentence
// for one generation with the flag from another.
func indexProtectionNotice(st dump.UnprotectedState) string {
	if st.Reason == "" {
		return ""
	}
	if st.ClaimLost {
		return indexClaimLostNotice
	}
	return indexUnprotectedNotice
}

// indexCollapseNotice is the second standing condition: the index derived one
// module name for more than one dump file, so the later file overwrote the
// earlier one in every map the index reads through and that content is not served
// at all. ModuleCount still counts both, which is why the server cannot be left
// to answer with the number alone.
//
// WHY IT IS BUILT AND NOT A CONSTANT, unlike the two above. Those two say
// everything they have to say in a fixed sentence; this one is worth reading only
// with its numbers in it. A reader deciding whether to re-point a path needs to
// know whether a couple of files are missing or most of the dump: measured on the
// customer-shaped corpus, a root pointed one level too high lost 10839 modules out
// of 13575. A notice that said "some content" would push that reader back to the
// log this whole mechanism exists to replace.
//
// WHAT IT DOES NOT SAY. It does not name a cause. The server observed a
// collision; it did not observe why there was one. The path check below is
// offered as something to VERIFY, in the imperative, and not as a diagnosis,
// because the one cause ever measured (a --dump pointed one level too high) is
// now corrected automatically for every metadata kind this build knows, and what
// survives is by definition the cases nobody has characterised.
//
// The sample is bounded by the dump package; the counts are not. A truncated list
// beside an exact count is a reader who can act; a rounded count is a reader who
// cannot.
//
// Customer-facing RU: no тире.
func indexCollapseNotice(st dump.CollapsedKeyState) string {
	if st.Files <= 0 {
		return ""
	}
	notice := fmt.Sprintf("> ВНИМАНИЕ: индекс выгрузки потерял часть содержимого. "+
		"Файлов, чьё имя модуля уже было занято другим файлом: %d. Совпавших имён: %d.",
		st.Files, st.Keys)
	if len(st.Sample) > 0 {
		notice += " Например: " + strings.Join(st.Sample, ", ") + "."
	}
	notice += " Такие файлы сервер считает в общем числе модулей, но выдать их содержимое " +
		"не может. Проверьте, что путь в `--dump` указывает на сам корень выгрузки, то есть " +
		"на каталог, внутри которого лежат `Catalogs`, `Documents` и `Ext`, а не на каталог " +
		"выше него. После исправления пути вызовите `reload_dump`.\n"
	return notice
}

// indexNotices assembles every standing condition that is TRUE of the index into
// the block that goes in front of an answer, or "" when there is nothing to say.
//
// Both states arrive as arguments, each already read in ONE atomic load by the
// caller, so a reload landing between them cannot produce a sentence about one
// generation next to a number from another.
//
// ORDER IS FIXED AND IS PROTECTION FIRST. That one says the answer below can be
// removed from under the reader while they are reading it, which changes what
// they should do next; the collapse says part of the dump is missing, which
// changes what they should believe about the answer. Both matter, and a stable
// order is what lets a reader who sees these lines every call learn to skim them.
func indexNotices(prot dump.UnprotectedState, collapse dump.CollapsedKeyState) string {
	return indexProtectionNotice(prot) + indexCollapseNotice(collapse)
}

// withIndexProtectionNotice wraps an index-backed tool handler so that every
// response it produces carries indexUnprotectedNotice while the attached generation
// is unprotected, and none of them carries it otherwise.
//
// IT WRAPS THE HANDLER RATHER THAN THE FORMATTERS, and that is the point. A notice
// added inside a formatter is a notice some other return path forgets: this package
// already has a case where a warning had to be duplicated onto an early return so
// the page could not swallow it (writeObjectWarnings). A wrapper has no return path
// to miss, including ones added later.
//
// ERRORS ARE DECORATED TOO, AND THE NOTICE GOES FIRST ON THEM AS WELL. A handler
// error is turned into user-visible content by WithToolErrors, which converts it into
// a CallToolResult with IsError; the SDK does NOT do this for a raw ToolHandler
// (mcp/tool.go treats a returned error as a protocol error, and Server.callTool in
// mcp/server.go returns it untouched). Because WithToolErrors runs INSIDE this
// wrapper, an operational failure reaches prependNotice as a result and the notice
// lands first by the same path a success takes, so the two halves of one statement
// about one answer cannot drift. A marked protocol error still arrives here as an
// error and is wrapped below, which keeps its code: the wire encoder resolves the
// code with errors.As, so a %w wrap does not lose it.
//
// Remove that wrapper and this notice goes back into a JSON-RPC error message the
// model never reads, at the end of a message a client is free to truncate. A failing
// call on a frozen cache is exactly when the reason matters most. Wrapping keeps the
// original error in the chain, so errors.Is and errors.As on it keep working.
//
// The state is read AFTER the handler runs, so a reload_dump call that swaps the
// generation is described by the state it left behind rather than the one it found.
func withIndexProtectionNotice(index *dump.Index, h mcp.ToolHandler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := h(ctx, req)
		notice := indexNotices(index.Unprotected(), index.CollapsedKeys())
		if notice == "" {
			return res, err
		}
		if err != nil {
			// The notice already ends in a newline, so one more separates it from the
			// error text exactly as prependNotice separates it from a body.
			return res, fmt.Errorf("%s\n%w", notice, err)
		}
		return prependNotice(res, notice), nil
	}
}

// prependNotice puts notice at the very front of a tool result.
//
// It SPLICES INTO THE FIRST TEXT BLOCK rather than adding a second content block,
// because a client that renders only the first block would drop a separate one, and
// a notice that some clients do not show is the invisible-warning defect again in a
// new place. A result with no text block to splice into gets the notice as its own
// block, which is the best available and still better than nothing.
//
// The front, and not the end under the body: this is a statement about the whole
// answer, like the «> Диагностика:» notes, not a footnote about one number, like
// the search shortfall note.
func prependNotice(res *mcp.CallToolResult, notice string) *mcp.CallToolResult {
	if res == nil {
		return textResult(notice)
	}
	for i, c := range res.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			continue
		}
		// A fresh value rather than a mutation: the caller's block may be shared with
		// something that has already been handed out.
		res.Content[i] = &mcp.TextContent{Text: notice + "\n" + tc.Text}
		return res
	}
	res.Content = append([]mcp.Content{&mcp.TextContent{Text: notice}}, res.Content...)
	return res
}
