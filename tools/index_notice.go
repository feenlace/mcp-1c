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
// customer-shaped corpus wrapped in "Documents/dumps/", a root pointed TWO levels
// too high lost 10839 modules out of 13575. A notice that said "some content" would push that reader back to the
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
	notice += " Такие файлы сервер считает в общем числе модулей, но выдать их содержимое " +
		"не может. Проверьте, что путь в `--dump` указывает на сам корень выгрузки, то есть " +
		"на каталог, внутри которого лежат `Catalogs`, `Documents` и `Ext`, а не на каталог " +
		"выше него. Путь читается при запуске, поэтому исправленный путь применяется только " +
		"перезапуском: укажите новый `--dump` и перезапустите сервер. " + reloadDumpIsNotThePathRemedy + "\n"
	if sample := echoableSample(st.Sample); len(sample) > 0 {
		notice += "\nСовпавшие имена:\n" + fenced(strings.Join(sample, "\n")) + "\n"
	}
	return notice
}

// echoableSample is the collided module names, in a form that can be shown.
//
// THE NAMES ARE NOT OURS. A module key is built from the directory names in the
// dump, and a directory name is whatever the customer called it: a real customer
// tree holds «Доработки — копия». Joined into the blockquote above, as they were,
// such a name put a тире into customer-facing RU, and a name holding a backtick or
// a newline left the structure it was placed in altogether.
//
// SO THEY MOVE OUT OF THE PROSE AND INTO A FENCE, one per line, with the fence
// length computed from the payload by fenceLen so no run of backticks can close
// it. Inside a fence a dash is data rather than prose, which is the distinction the
// no-dash rule is about and the one a guard fed polite literals could never make.
//
// A name carrying a control character or a line break is DROPPED rather than
// repaired: it cannot be shown on one line, and the counts above it are exact
// whether or not the sample is complete.
func echoableSample(names []string) []string {
	var out []string
	for _, n := range names {
		if strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// indexWrappedNotice is the third standing condition: the index derived its module
// names from paths carrying directory levels ABOVE the dump root.
//
// WHY IT IS NOT THE COLLAPSE NOTICE. The two are different measurements and either
// can be zero while the other is not. A --dump two levels above a SINGLE extension
// collides with nothing at all, so the collapse counter stays silent, while every
// module in the dump is filed as though it belonged to the configuration and the
// extension namespace has simply disappeared. That case had no channel: the startup
// check cannot see it either, because one ReadDir cannot tell it from a hand-made
// tree with one kind directory in it. This number can, because it is measured after
// the keys are derived rather than guessed from the shape of a directory.
//
// The proportion is in it because it is what tells a reader which case they have:
// a handful of odd files, or the whole dump.
//
// IT SAYS WHAT WAS COUNTED AND STOPS. The sentence used to go on «пространство имён
// расширения при этом теряется: модули расширения попадают туда же, куда модули
// конфигурации», and that clause is a CAUSE the counter never measured. It is false
// on an ordinary tree: a --dump holding a base-configuration dump beside a
// recognised extension, «ext» and «main» as siblings, counts the two files under
// main as wrapped and serves ext.FeenlaceMCPService.ОбщийМодуль.Расш1.Модуль IN THE
// SAME ANSWER. «2 из 4» was right and the reason printed next to it was not, which
// is worse than saying nothing: a reader who checks the keys finds the namespace
// there and concludes the whole notice is noise.
//
// The counter measures ONE thing, that anchorIndex moved, and there are several
// trees that makes true. The namespace survives a wrap under the legacy
// «Расширения/<Имя>/» layout, because extensionDirName is itself an anchor marker
// (see dump/index.go:dumpRootMarker) and the path rule applies below the skip. It is
// lost when a manifest sits deeper than the detection looks. Nothing in the number
// says which tree this is, so the notice does not guess.
//
// What IS true of every one of them is the mechanism, and that is what replaces the
// clause: detectExtensionLayout reads the manifest of the --dump directory itself
// and of its immediate children, and of nothing below that, so a manifest deeper
// than one level is never opened at all.
//
// Customer-facing RU: no тире.
func indexWrappedNotice(st dump.WrappedPathState) string {
	if st.Files <= 0 {
		return ""
	}
	return fmt.Sprintf("> ВНИМАНИЕ: имена модулей выведены не от корня выгрузки. "+
		"Файлов, у которых над корнем выгрузки оказались лишние каталоги: %d из %d. "+
		"Имена таких модулей сервер вывел от найденного ниже корня выгрузки, а не от "+
		"каталога, указанного в `--dump`. Чего это стоило, счётчик не измеряет. "+
		"Манифест расширения сервер читает только в самом каталоге `--dump` и в его "+
		"подкаталогах первого уровня, поэтому манифест, лежащий глубже, не прочитан "+
		"вовсе. Укажите в `--dump` сам корень выгрузки и перезапустите сервер. %s\n",
		st.Files, st.Total, reloadDumpIsNotThePathRemedy)
}

// reloadDumpIsNotThePathRemedy is the sentence that stops a reader following the
// instruction these two notices used to give.
//
// BOTH OF THEM PRESCRIBED `reload_dump`, AND IT CANNOT CARRY THAT OUT. The tool
// takes no arguments at all, so there is no way to hand it a corrected path; it
// re-reads the directory the index was CONSTRUCTED with, which is the wrong one.
// Pointed at the same directory it does even less: dump.Index.Reload compares the
// dump's content signature against the one it is serving and returns without
// rebuilding when nothing on disk moved, and correcting a --dump flag moves
// nothing on disk. The reader who followed the instruction got «изменений не
// обнаружено», keys exactly as wrong as before, and this same notice again on the
// next answer.
//
// So the remedy named is the restart, and the tool is named too, because a reader
// who has been told to call it before will otherwise try it and conclude the
// diagnosis was wrong rather than the instruction.
//
// `reload_dump` stays the right remedy where the PATH is right and the FILES
// moved, which is the search shortfall note, and it keeps saying so there.
//
// Customer-facing RU: no тире.
const reloadDumpIsNotThePathRemedy = "Вызов `reload_dump` здесь не поможет: он " +
	"перечитывает тот же самый каталог и на имена модулей не влияет."

// indexLayoutDoubtNotice is the fourth: directories whose extension-ness the
// server could not decide.
//
// A DOUBT IS NEVER A GUESS AND IS NEVER SILENCE. Detection has three answers, and
// the third leaves the keys exactly as they were before extensions were recognised
// at all. That is the safe direction, and it is only safe because it is said out
// loud: a namespace that quietly failed to appear looks identical to a dump that
// never had one.
//
// COUNTS, NEVER NAMES, for the reason echoableSample gives at length.
//
// Customer-facing RU: no тире.
func indexLayoutDoubtNotice(st dump.ExtensionLayoutSummary) string {
	if st.Undecided() == 0 && !st.ScanTruncated {
		return ""
	}
	notice := "> ВНИМАНИЕ: часть каталогов выгрузки сервер не смог отнести к расширениям. " +
		"Их модули проиндексированы без имени расширения, то есть так же, как до появления " +
		"этой возможности."
	for _, part := range []struct {
		n    int
		text string
	}{
		{st.NotRegular, "Configuration.xml оказался не обычным файлом, каталогов: %d."},
		{st.Unreadable, "Не удалось прочитать Configuration.xml, каталогов: %d."},
		{st.ReadTruncated, "Манифест не поместился в окно чтения, каталогов: %d."},
		{st.NameRejected, "Объявленное имя расширения нельзя использовать как часть ключа, каталогов: %d."},
		{st.Malformed, "В Configuration.xml не закрыт комментарий, блок CDATA или инструкция обработки, каталогов: %d."},
		{st.Unscannable, "В Configuration.xml есть объявление DOCTYPE или другое объявление разметки, границы которого сервер не определяет, каталогов: %d."},
	} {
		if part.n > 0 {
			notice += " " + fmt.Sprintf(part.text, part.n)
		}
	}
	if st.ScanTruncated {
		notice += " Просмотрены не все подкаталоги, поэтому расширения могли остаться незамеченными."
	}
	return notice + "\n"
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
func indexNotices(prot dump.UnprotectedState, collapse dump.CollapsedKeyState,
	wrapped dump.WrappedPathState, layout dump.ExtensionLayoutSummary) string {
	return indexProtectionNotice(prot) + indexCollapseNotice(collapse) +
		indexWrappedNotice(wrapped) + indexLayoutDoubtNotice(layout)
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
		notice := indexNotices(index.Unprotected(), index.CollapsedKeys(),
			index.WrappedPaths(), index.ExtensionLayout())
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
