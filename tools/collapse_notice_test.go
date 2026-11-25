package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// collapseMarker is matched as a PHRASE and not through the notice builder, like
// every other marker in this package's tests, so these tests fail against a build
// whose notice says something else rather than tautologically agreeing with it.
const collapseMarker = "индекс выгрузки потерял часть содержимого"

// collapseTerm is carried by every module these tests write, so "the index still
// answers" is one query away.
const collapseTerm = "ПроцедураСхлопнутогоИндекса"

// collapsingNoticeDump creates two paths with the same unqualified ID
// candidate and two otherwise unrelated modules.
func collapsingNoticeDump(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := func(n string) string {
		return "Процедура " + n + "()\n    Сообщить(\"" + collapseTerm + "\");\nКонецПроцедуры\n"
	}
	mkBSL(t, dir, "обёртка/Прочее/Первый/Ext/ObjectModule.bsl", body("Первый"))
	mkBSL(t, dir, "обёртка/Прочее/Второй/Ext/ObjectModule.bsl", body("Второй"))
	mkBSL(t, dir, "CommonModules/Целый/Ext/Module.bsl", body("Целый"))
	mkBSL(t, dir, "CommonModules/Другой/Ext/Module.bsl", body("Другой"))
	return dir
}

// collapseIndex serves dumpDir through a fresh immutable generation.
func collapseIndex(t *testing.T, dumpDir string) *dump.Index {
	t.Helper()
	cacheDir := t.TempDir()

	gen, err := dump.PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("warming the cache: %v", err)
	}
	gensig := gen.Gensig()
	gen.Release()

	idx, err := dump.OpenGenerationReadOnly(dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("opening the generation for serving: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()

	if err := idx.BuildError(); err != nil {
		t.Fatal(err)
	}
	if got := idx.CollapsedKeyCount(); got != 0 {
		t.Fatalf("index collapsed %d file(s): %v", got, idx.CollapsedKeys().Sample)
	}
	return idx
}

// callSearchCollapse runs search_code through the real handler and returns its body.
func callSearchCollapse(t *testing.T, index *dump.Index) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{"query": collapseTerm, "limit": 5})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewSearchCodeHandler(index)(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "search_code", Arguments: args},
	})
	if err != nil {
		t.Fatalf("search_code returned an error: %v", err)
	}
	return resultText(t, res)
}

func TestCollapseNotice_QualifiedModulesAreServedWithoutLoss(t *testing.T) {
	idx := collapseIndex(t, collapsingNoticeDump(t))
	text := callSearchCollapse(t, idx)
	if strings.Contains(text, collapseMarker) || searchRenderedMatches(text) != 4 {
		t.Errorf("search should serve all four files without a loss notice:\n%s", text)
	}
}

// TestCollapseNotice_ACleanIndexIsSilent is the control that stops every other
// assertion here from passing on a build that always warns.
func TestCollapseNotice_ACleanIndexIsSilent(t *testing.T) {
	// THE FIXTURE MUST CARRY THE TERM. This used to run against noticeDump, whose
	// modules carry a different term, so the search matched nothing and «the healthy
	// index still answers» was asserted against «Ничего не найдено». A control that
	// never exercises the thing it controls is not a control.
	clean := t.TempDir()
	body := func(n string) string {
		return "Процедура " + n + "()\n    Сообщить(\"" + collapseTerm + "\");\nКонецПроцедуры\n"
	}
	mkBSL(t, clean, "CommonModules/Целый/Ext/Module.bsl", body("Целый"))
	mkBSL(t, clean, "CommonModules/Другой/Ext/Module.bsl", body("Другой"))

	text := callSearchCollapse(t, collapseIndex(t, clean))

	if strings.Contains(text, collapseMarker) {
		t.Errorf("a healthy index warned about a collapse it does not have:\n%s", text)
	}
	if !strings.HasPrefix(text, "## Результаты поиска") {
		t.Errorf("the healthy answer does not start with the result header:\n%s", text)
	}
	if searchRenderedMatches(text) == 0 {
		t.Fatalf("the clean fixture answered with no matches, so the silence above is about "+
			"an empty answer rather than about a healthy index:\n%s", text)
	}
}

func TestCollapseNotice_ReloadKeepsQualifiedModules(t *testing.T) {
	idx := collapseIndex(t, collapsingNoticeDump(t))
	res, err := NewReloadDumpHandler(idx)(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "reload_dump", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, res); strings.Contains(text, collapseMarker) {
		t.Errorf("reload reported lost files:\n%s", text)
	}
	if text := callSearchCollapse(t, idx); searchRenderedMatches(text) != 4 {
		t.Errorf("search after reload should serve all four files:\n%s", text)
	}
}

func TestCollapseNotice_QualifiedIndexPreservesHandlerError(t *testing.T) {
	idx := collapseIndex(t, collapsingNoticeDump(t))
	sentinel := errors.New("поиск не выполнен")
	h := withIndexProtectionNotice(idx, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, sentinel
	})
	_, err := h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "x"}})
	if !errors.Is(err, sentinel) || strings.Contains(err.Error(), collapseMarker) {
		t.Errorf("got %v, want original error without a loss notice", err)
	}
}

// TestCollapseNotice_BothConditionsProduceBothSentences pins what happens when an
// index is unprotected AND collapsed. They are independent facts about the same
// index and a reader needs both; a wrapper that emitted only the first would hide
// the second exactly when things are worst.
func TestCollapseNotice_BothConditionsProduceBothSentences(t *testing.T) {
	both := indexNotices(dump.UnprotectedState{Reason: "claim refused"},
		dump.CollapsedKeyState{Files: 3, Keys: 2, Sample: []string{"А.Б.В", "Г.Д.Е"}},
		dump.WrappedPathState{}, dump.ExtensionLayoutSummary{})

	if !strings.Contains(both, noticeMarker) {
		t.Errorf("the protection sentence is missing:\n%s", both)
	}
	if !strings.Contains(both, collapseMarker) {
		t.Errorf("the collapse sentence is missing:\n%s", both)
	}
	// ORDER: protection first, because it is the one that says the answer below may
	// vanish under the reader's feet. The order is pinned so it cannot drift into
	// whatever the last edit happened to leave.
	if strings.Index(both, noticeMarker) > strings.Index(both, collapseMarker) {
		t.Errorf("the collapse sentence came before the protection one:\n%s", both)
	}
	// AND THEY ARE SEPARATE LINES, not one run-on paragraph.
	if !strings.Contains(both, "\n> ") {
		t.Errorf("the two notices are not on separate quoted lines:\n%s", both)
	}
}

// TestCollapseNotice_TheSentenceMatchesTheState pins the whole of what this layer
// decides: a state in, a sentence or silence out.
func TestCollapseNotice_TheSentenceMatchesTheState(t *testing.T) {
	if got := indexCollapseNotice(dump.CollapsedKeyState{}); got != "" {
		t.Errorf("a clean state produced a notice: %q", got)
	}
	// A state with names but no lost files cannot happen, and if it ever does the
	// honest answer is silence rather than a sentence with a zero in it.
	if got := indexCollapseNotice(dump.CollapsedKeyState{Keys: 2}); got != "" {
		t.Errorf("a state losing no files produced a notice: %q", got)
	}

	got := indexCollapseNotice(dump.CollapsedKeyState{
		Files: 7, Keys: 3, Sample: []string{"Справочник.А.МодульОбъекта", "Документ.Б.МодульМенеджера"},
	})
	if got == "" {
		t.Fatal("a state that lost seven files produced no notice")
	}
	for _, want := range []string{collapseMarker, "7", "3",
		"Справочник.А.МодульОбъекта", "Документ.Б.МодульМенеджера", "--dump", "reload_dump"} {
		if !strings.Contains(got, want) {
			t.Errorf("the notice does not carry %q:\n%s", want, got)
		}
	}
	// It must end in a newline, like the two constants, so prependNotice separates
	// it from the body exactly as it separates them.
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("the notice does not end in a newline: %q", got)
	}
	// And it must not claim a cause it did not measure. The server observed a
	// collision; it did not observe WHY, and naming the mis-pointed root as the
	// cause rather than as a thing to check would be a guess in the reader's face.
	for _, forbidden := range []string{"потому что", "причина в том"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the notice asserts a cause it did not measure (%q):\n%s", forbidden, got)
		}
	}
}

// TestCollapseNotice_CarriesNoDash pins the house rule for customer-facing
// Russian, applied to the shipped text rather than to a copy.
func TestCollapseNotice_CarriesNoDash(t *testing.T) {
	// A HOSTILE SAMPLE, NOT A POLITE ONE. The sample names are module keys built
	// from the DIRECTORY NAMES in the customer's dump, and a real customer tree
	// holds «Доработки — копия». Joined into the sentence, as they were, such a name
	// put a тире straight into customer-facing RU, and a guard fed benign literals
	// stayed green through exactly that.
	hostile := []string{
		"Справочник.Доработки — копия.МодульОбъекта",
		"Документ.А–Б.МодульМенеджера",
		"Обработка.`код`.МодульОбъекта",
		"Отчет.> цитата.МодульОбъекта",
		"Справочник.А\nВНИМАНИЕ: всё в порядке.МодульОбъекта",
	}
	notice := indexCollapseNotice(dump.CollapsedKeyState{Files: 2, Keys: 5, Sample: hostile})
	if notice == "" {
		t.Fatal("the notice under test is empty, so the check below reads nothing")
	}

	// POSITIVE CONTROL FIRST, on the production input: the samples really do carry
	// what is being looked for, so a clean prose scan below is the notice being
	// clean and not the scan being blind.
	if !strings.ContainsAny(strings.Join(hostile, ""), "—–‒―−") {
		t.Fatal("control failed: the hostile samples carry no dash character at all")
	}

	// THE PROSE is what the rule is about. The echoed names live inside a fenced
	// block, where a dash is data rather than something this project wrote; that
	// distinction is the whole reason the block exists.
	prose, fence, ok := strings.Cut(notice, "```")
	if !ok {
		t.Fatalf("the notice has no fenced block, so the echoed names are in the prose:\n%s", notice)
	}
	for _, r := range []rune{'—', '–', '‒', '―', '−'} {
		if strings.ContainsRune(prose, r) {
			t.Errorf("the collapse notice PROSE contains %q (U+%04X), which customer-facing RU "+
				"text must not carry:\n%s", string(r), r, prose)
		}
	}
	// The blockquote is not broken out of: every prose line is still quoted or blank.
	for _, line := range strings.Split(strings.TrimSpace(prose), "\n") {
		if line == "" || strings.HasPrefix(line, "> ") || line == "Совпавшие имена:" {
			continue
		}
		t.Errorf("a line escaped the notice structure: %q", line)
	}
	// The name carrying a line break is dropped rather than shown, because it cannot
	// be shown on one line.
	if strings.Contains(fence, "ВНИМАНИЕ: всё в порядке") {
		t.Errorf("a sample carrying a line break was echoed anyway:\n%s", fence)
	}
	// And the fence really does contain a run of backticks that a fixed ``` could
	// not have closed over.
	if !strings.Contains(fence, "`код`") {
		t.Errorf("the backticked sample is missing from the fenced block:\n%s", fence)
	}
}
