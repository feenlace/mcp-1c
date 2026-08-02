package tools

import (
	"context"
	"fmt"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The index-protection notice: the user-visible half of "the server never SILENTLY
// serves an index generation it could not protect".
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
		notice := indexProtectionNotice(index.Unprotected())
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
