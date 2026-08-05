package tools

// The collapsed-key notice, where the user actually reads it.
//
// THE PROPERTY: the server never SILENTLY serves an index that lost module
// content to a name collision. dump/collapsed_keys_test.go pins that the dump
// package counts the loss exactly; these tests pin the only half that reaches a
// human, the MCP tool response.
//
// It is the same argument that put the protection notice here, applied to a
// second standing condition, and it is deliberately routed through the SAME
// wrapper rather than a parallel one: two mechanisms that prepend notices would
// eventually disagree about ordering, about the error path, and about which
// return paths they cover.
//
// WHAT IS NOT PINNED HERE, stated rather than left to be discovered:
//
//   - The tools that do NOT read the index carry no notice by construction, for
//     the same reason the protection notice does not reach them: the wrapper is
//     applied only in NewSearchCodeHandler and NewReloadDumpHandler.
//   - The RECOVERY (a re-pointed dump, a reload, and the notice stops) is pinned
//     on the state in dump, not again here. This layer turns a state into a
//     sentence and does not know which way the state last moved.

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

// collapsingNoticeDump writes a dump in which two files derive ONE module name
// and two more do not, and returns its directory.
//
// The colliding pair is the customer's defect shape restricted to what SURVIVES
// the anchor scan in dump/index.go:bslPathToModuleName: a wrapper over a top-level
// directory that is not a known metadata kind, so no anchor exists and both files
// key on the wrapper. Using a shape the fix already handles would produce a dump
// with nothing to warn about.
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

// collapseIndex serves dumpDir out of a fresh cache and asserts, as a
// precondition, that the index really is in the state the test needs. A test of
// "the notice appears" that ran against a clean index would pass by never firing.
func collapseIndex(t *testing.T, dumpDir string, wantCollapse bool) *dump.Index {
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

	got := idx.CollapsedKeyCount()
	if wantCollapse && got == 0 {
		t.Fatalf("the fixture produced an index with no collapsed keys, so nothing below "+
			"tests the notice (ModuleCount = %d)", idx.ModuleCount())
	}
	if !wantCollapse && got != 0 {
		t.Fatalf("the clean fixture collapsed %d file(s): %v",
			got, idx.CollapsedKeys().Sample)
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

// TestCollapseNotice_ACollapsedIndexSaysSoInTheAnswer is the regression: an index
// that lost module content to a name collision must say so in the answer, and it
// must still be a real answer.
func TestCollapseNotice_ACollapsedIndexSaysSoInTheAnswer(t *testing.T) {
	idx := collapseIndex(t, collapsingNoticeDump(t), true)
	text := callSearchCollapse(t, idx)

	if !strings.Contains(text, collapseMarker) {
		t.Fatalf("the answer carries no notice that the index lost content:\n%s", text)
	}
	// AT THE FRONT, for the reason the protection notice is: this is a statement
	// about the whole answer, not a footnote about one number.
	if !strings.HasPrefix(text, "> ") {
		t.Errorf("the notice is not the first thing in the answer; it starts with %q",
			strings.SplitN(text, "\n", 2)[0])
	}
	// AND IT IS STILL AN ANSWER.
	if !strings.Contains(text, "## Результаты поиска") {
		t.Errorf("the notice displaced the search result:\n%s", text)
	}
	if searchRenderedMatches(text) == 0 {
		t.Errorf("the collapsed index answered with no matches at all:\n%s", text)
	}
	// AND IT CARRIES THE NUMBER. A warning that says "some content was lost"
	// without saying how much is a warning a reader cannot weigh.
	if !strings.Contains(text, "1") {
		t.Errorf("the notice does not carry the count of lost files:\n%s", text)
	}
	// AND IT NAMES WHAT COLLIDED, so the reader can go and look.
	sample := idx.CollapsedKeys().Sample
	if len(sample) == 0 {
		t.Fatalf("the index reported a collapse with no sample, so the assertion below is vacuous")
	}
	if !strings.Contains(text, sample[0]) {
		t.Errorf("the notice does not name the collided module %q:\n%s", sample[0], text)
	}
	// AND IT SAYS WHAT TO DO. A warning with no remedy is noise by the second time.
	for _, want := range []string{"--dump", "reload_dump"} {
		if !strings.Contains(text, want) {
			t.Errorf("the notice does not name %q as part of the remedy:\n%s", want, text)
		}
	}
}

// TestCollapseNotice_ACleanIndexIsSilent is the control that stops every other
// assertion here from passing on a build that always warns.
func TestCollapseNotice_ACleanIndexIsSilent(t *testing.T) {
	text := callSearchCollapse(t, collapseIndex(t, noticeDump(t), false))

	if strings.Contains(text, collapseMarker) {
		t.Errorf("a healthy index warned about a collapse it does not have:\n%s", text)
	}
	if !strings.HasPrefix(text, "## Результаты поиска") {
		t.Errorf("the healthy answer does not start with the result header:\n%s", text)
	}
}

// TestCollapseNotice_ReloadDumpCarriesItToo pins the second index-backed tool.
// It matters more than search_code and not less: re-pointing --dump and calling
// reload_dump is the remedy the notice offers, so the tool a user reaches for
// must be the one that keeps telling them whether it worked.
func TestCollapseNotice_ReloadDumpCarriesItToo(t *testing.T) {
	idx := collapseIndex(t, collapsingNoticeDump(t), true)
	res, err := NewReloadDumpHandler(idx)(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "reload_dump", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		if !strings.Contains(err.Error(), collapseMarker) {
			t.Fatalf("the failed reload carries no notice:\n%v", err)
		}
		return
	}
	text := resultText(t, res)
	if !strings.Contains(text, collapseMarker) {
		t.Errorf("the reload report carries no notice that the index lost content:\n%s", text)
	}
	if !strings.HasPrefix(text, "> ") {
		t.Errorf("the notice is not the first thing in the reload report:\n%s", text)
	}
}

// TestCollapseNotice_AFailingCallIsDecoratedToo pins the error path. A failing
// call on a collapsed index is exactly when the reason matters, and it is
// user-visible output only because WithToolErrors makes it so.
func TestCollapseNotice_AFailingCallIsDecoratedToo(t *testing.T) {
	idx := collapseIndex(t, collapsingNoticeDump(t), true)
	sentinel := errors.New("поиск не выполнен")
	h := withIndexProtectionNotice(idx, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, sentinel
	})

	_, err := h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "x"}})
	if err == nil {
		t.Fatal("the wrapper swallowed the handler's error")
	}
	if !strings.Contains(err.Error(), collapseMarker) {
		t.Errorf("the error text carries no collapse notice:\n%v", err)
	}
	if !strings.HasPrefix(err.Error(), "> ") {
		t.Errorf("the notice is not the first thing in the error text; it starts with %q",
			strings.SplitN(err.Error(), "\n", 2)[0])
	}
	if !strings.Contains(err.Error(), sentinel.Error()) {
		t.Errorf("the original error text was lost: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("the wrapper broke the error chain: errors.Is could not find the original in %v", err)
	}
}

// TestCollapseNotice_BothConditionsProduceBothSentences pins what happens when an
// index is unprotected AND collapsed. They are independent facts about the same
// index and a reader needs both; a wrapper that emitted only the first would hide
// the second exactly when things are worst.
func TestCollapseNotice_BothConditionsProduceBothSentences(t *testing.T) {
	both := indexNotices(dump.UnprotectedState{Reason: "claim refused"},
		dump.CollapsedKeyState{Files: 3, Keys: 2, Sample: []string{"А.Б.В", "Г.Д.Е"}})

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
	notice := indexCollapseNotice(dump.CollapsedKeyState{
		Files: 2, Keys: 1, Sample: []string{"Справочник.А.МодульОбъекта"},
	})
	if notice == "" {
		t.Fatal("the notice under test is empty, so the check below reads nothing")
	}
	for _, r := range []rune{'—', '–', '‒', '―', '−'} {
		if strings.ContainsRune(notice, r) {
			t.Errorf("the collapse notice contains %q (U+%04X), which customer-facing RU "+
				"text must not carry:\n%s", string(r), r, notice)
		}
	}
	// POSITIVE CONTROL: the check can fire.
	if !strings.ContainsRune("текст — с тире", '—') {
		t.Fatal("the control failed: the dash check cannot detect an em dash")
	}
}
