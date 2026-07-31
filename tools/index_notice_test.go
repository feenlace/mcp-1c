package tools

// The index-protection notice, where the user actually reads it.
//
// THE PROPERTY: the server never SILENTLY serves an index generation it could not
// protect. dump/readonly_cache_test.go pins that the serve happens and that the
// dump package knows it is unprotected; these tests pin the only half that reaches
// a human — the MCP tool response.
//
// WHY THE LOG IS NOT ENOUGH, and why this file exists at all. The unprotected serve
// IS logged, at ERROR. In stdio mode that log goes into a file under the cache
// directory, because it is the only place it can go without corrupting the client's
// pipe. The whole shipped defect was a warning that was written and never read. A
// test that only asserted on slog would have passed on the version that nobody
// could see.
//
// WHAT IS NOT PINNED HERE, stated rather than left to be discovered:
//
//   - The read-only MOUNT case (EROFS), where the notice must NOT appear even
//     though the claim failed. No test can portably create such a mount, so what is
//     pinned is the layer boundary: an empty UnprotectedReason produces no notice
//     (TestIndexNotice_AProtectedIndexIsSilent covers the shape, on a healthy cache
//     rather than on a mount).
//   - The eight tools that do NOT read the index carry no notice by construction,
//     because the wrapper is applied only in NewSearchCodeHandler and
//     NewReloadDumpHandler. Nothing here fails if a ninth tool is later given the
//     index and not the wrapper.
//   - reload_dump's ERROR path is pinned through the wrapper directly, not through
//     a real failing Reload on a frozen cache: producing one needs a dump that
//     changes under a frozen cache, and that state has no service at all, so there
//     is no handler call left to decorate.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// noticeMarker is matched as a PHRASE and not through indexUnprotectedNotice, like
// every other marker in this package's tests, so these tests fail against a build
// whose notice says something else rather than tautologically agreeing with it.
const noticeMarker = "индекс выгрузки отдаётся без защиты"

// noticeTerm is carried by every module these tests write, so "the index still
// answers" is one query away.
const noticeTerm = "ПроцедураНезащищённогоИндекса"

// noticeDump writes a small dump and returns its directory.
func noticeDump(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for i := range 4 {
		mkBSL(t, dir, fmt.Sprintf("CommonModules/Модуль%02d/Ext/Module.bsl", i),
			fmt.Sprintf("Процедура Тест%02d()\n    Сообщить(\"%s %02d\");\nКонецПроцедуры\n", i, noticeTerm, i))
	}
	return dir
}

// noticeFreeze clears every write bit under root and restores them before the
// test's temp dirs are removed, then PROVES the freeze took. Without that control a
// chmod that silently failed would let every assertion below pass while exercising
// the ordinary protected path.
func noticeFreeze(t *testing.T, root string) {
	t.Helper()
	type saved struct {
		path string
		mode fs.FileMode
	}
	var modes []saved
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, iErr := d.Info()
		if iErr != nil {
			return iErr
		}
		modes = append(modes, saved{p, info.Mode().Perm()})
		return nil
	}); err != nil {
		t.Fatalf("walking %s before freezing it: %v", root, err)
	}
	t.Cleanup(func() {
		for _, s := range modes {
			_ = os.Chmod(s.path, s.mode)
		}
	})
	for i := len(modes) - 1; i >= 0; i-- {
		if err := os.Chmod(modes[i].path, modes[i].mode&^0o222); err != nil {
			t.Fatalf("clearing write bits on %s: %v", modes[i].path, err)
		}
	}
	if f, err := os.CreateTemp(root, ".control-"); err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		t.Fatalf("the control failed: %s is still writable after clearing its write bits", root)
	}
}

// noticeIndex returns an index serving dumpDir out of cacheDir. When freeze is set
// the cache is made unwritable first, so the open has to take the claim-less route.
func noticeIndex(t *testing.T, freeze bool) *dump.Index {
	t.Helper()
	dumpDir := noticeDump(t)
	cacheDir := t.TempDir()

	gen, err := dump.PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("warming the cache: %v", err)
	}
	gensig := gen.Gensig()
	gen.Release()

	if freeze {
		noticeFreeze(t, cacheDir)
	}
	idx, err := dump.OpenGenerationReadOnly(dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("opening the generation for serving: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()

	// The state under test, asserted as a precondition. A test of "the notice
	// appears" that ran against a protected index would pass by never firing.
	if freeze && idx.UnprotectedReason() == "" {
		t.Fatal("the frozen cache produced a protected index, so nothing below tests the notice")
	}
	if !freeze && idx.UnprotectedReason() != "" {
		t.Fatalf("the writable cache produced an unprotected index: %q", idx.UnprotectedReason())
	}
	return idx
}

// callSearch runs search_code through the real handler and returns its body.
func callSearch(t *testing.T, index *dump.Index) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{"query": noticeTerm, "limit": 5})
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

// TestIndexNotice_AnUnprotectedIndexSaysSoInTheAnswer is the regression: a search
// answered out of a generation nothing protects must carry the notice, and it must
// still be a real answer.
func TestIndexNotice_AnUnprotectedIndexSaysSoInTheAnswer(t *testing.T) {
	text := callSearch(t, noticeIndex(t, true))

	if !strings.Contains(text, noticeMarker) {
		t.Fatalf("the answer carries no notice that the index is unprotected:\n%s", text)
	}
	// AT THE FRONT. A notice under a page of code blocks is a notice an LLM client
	// summarises away; this one is a statement about the whole answer, so it goes
	// where the «> Диагностика:» notes go and not where the footnotes go.
	if !strings.HasPrefix(text, "> ") {
		t.Errorf("the notice is not the first thing in the answer; it starts with %q",
			strings.SplitN(text, "\n", 2)[0])
	}
	// AND IT IS STILL AN ANSWER. A notice that replaced the result would be a
	// refusal wearing a warning's clothes.
	if !strings.Contains(text, "## Результаты поиска") {
		t.Errorf("the notice displaced the search result:\n%s", text)
	}
	if searchRenderedMatches(text) == 0 {
		t.Errorf("the unprotected index answered with no matches at all:\n%s", text)
	}
	// AND IT SAYS WHAT TO DO. A warning with no remedy is noise by the second time.
	for _, want := range []string{"MCP_1C_CACHE_DIR", "--cache-dir"} {
		if !strings.Contains(text, want) {
			t.Errorf("the notice does not name %q as a remedy:\n%s", want, text)
		}
	}
	// AND WHAT THE RISK IS, not merely that something is wrong.
	if !strings.Contains(text, "может удалить") {
		t.Errorf("the notice does not say another process could remove the index:\n%s", text)
	}
}

// TestIndexNotice_AProtectedIndexIsSilent is the control that stops every other
// assertion here from passing on a build that always warns. A notice on a properly
// claimed index is the same defect as a refusal, just quieter, and it is the one
// that turns the line into noise nobody reads.
func TestIndexNotice_AProtectedIndexIsSilent(t *testing.T) {
	text := callSearch(t, noticeIndex(t, false))

	if strings.Contains(text, noticeMarker) {
		t.Errorf("a healthy index warned about its own protection:\n%s", text)
	}
	if !strings.HasPrefix(text, "## Результаты поиска") {
		t.Errorf("the healthy answer does not start with the result header:\n%s", text)
	}
}

// TestIndexNotice_ReloadDumpCarriesItToo pins the second index-backed tool. It
// matters more than search_code, not less: an unwritable cache is exactly what
// reload_dump cannot work around, so a user calling it is a user who needs to be
// told about the cache.
func TestIndexNotice_ReloadDumpCarriesItToo(t *testing.T) {
	idx := noticeIndex(t, true)
	res, err := NewReloadDumpHandler(idx)(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "reload_dump", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		// A reload on a frozen cache may fail; the notice must be on that too.
		if !strings.Contains(err.Error(), noticeMarker) {
			t.Fatalf("the failed reload carries no notice:\n%v", err)
		}
		return
	}
	text := resultText(t, res)
	if !strings.Contains(text, noticeMarker) {
		t.Errorf("the reload report carries no notice that the index is unprotected:\n%s", text)
	}
	if !strings.HasPrefix(text, "> ") {
		t.Errorf("the notice is not the first thing in the reload report:\n%s", text)
	}
}

// TestIndexNotice_AFailingCallIsDecoratedToo pins the error path of the wrapper.
// The SDK turns a handler error into a result with IsError and the error text as
// its content, so it is user-visible output like any other, and a failing call on a
// frozen cache is exactly when the reason matters.
//
// The handler is a stub rather than a real failing tool: the real failures that
// coincide with an unprotected index are the ones where nothing opened at all, and
// there is then no handler call left to decorate. The wrapper is what is under
// test, so the wrapper is what is called.
func TestIndexNotice_AFailingCallIsDecoratedToo(t *testing.T) {
	idx := noticeIndex(t, true)
	sentinel := errors.New("поиск не выполнен")
	h := withIndexProtectionNotice(idx, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, sentinel
	})

	_, err := h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "x"}})
	if err == nil {
		t.Fatal("the wrapper swallowed the handler's error")
	}
	if !strings.Contains(err.Error(), noticeMarker) {
		t.Errorf("the error text carries no notice:\n%v", err)
	}
	// THE CHAIN SURVIVES. A wrapper that reformatted the error into a string would
	// break every errors.Is in every caller above it, silently.
	if !errors.Is(err, sentinel) {
		t.Errorf("the wrapper broke the error chain: errors.Is could not find the original in %v", err)
	}

	// And on a protected index the same handler's error is untouched.
	clean := withIndexProtectionNotice(noticeIndex(t, false),
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, sentinel })
	_, cleanErr := clean(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "x"}})
	if cleanErr == nil || strings.Contains(cleanErr.Error(), noticeMarker) {
		t.Errorf("a healthy index decorated an error with a protection notice: %v", cleanErr)
	}
}

// TestIndexNotice_NoticeSurvivesAResultWithNoTextBlock pins the fallback in
// prependNotice. A result whose content the wrapper cannot splice into must still
// come back carrying the notice rather than losing it.
func TestIndexNotice_NoticeSurvivesAResultWithNoTextBlock(t *testing.T) {
	res := prependNotice(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: []byte{1}}},
	}, indexUnprotectedNotice)
	first, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("the notice was not added as the first content block; got %T", res.Content[0])
	}
	if !strings.Contains(first.Text, noticeMarker) {
		t.Errorf("the first block is not the notice: %q", first.Text)
	}
	if len(res.Content) != 2 {
		t.Errorf("the original content was dropped: %d blocks remain", len(res.Content))
	}

	// A nil result is the other shape a handler can hand back, and losing the notice
	// there would be losing it on exactly the calls that went wrong.
	if got := resultText(t, prependNotice(nil, indexUnprotectedNotice)); !strings.Contains(got, noticeMarker) {
		t.Errorf("a nil result lost the notice: %q", got)
	}
}

// TestIndexNotice_TheNoticeCarriesNoDash pins the house rule for customer-facing
// Russian, with the check applied to the shipped constant rather than to a copy.
func TestIndexNotice_TheNoticeCarriesNoDash(t *testing.T) {
	for _, r := range []rune{'—', '–', '‒', '―', '−'} {
		if strings.ContainsRune(indexUnprotectedNotice, r) {
			t.Errorf("the notice contains %q (U+%04X), which customer-facing RU text must not carry",
				string(r), r)
		}
	}
	// POSITIVE CONTROL: the check can fire. Without it a test comparing against an
	// empty set of characters would pass on any text at all.
	if !strings.ContainsRune("текст — с тире", '—') {
		t.Fatal("the control failed: the dash check cannot detect an em dash")
	}
}
