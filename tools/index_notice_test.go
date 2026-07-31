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
//   - The RECOVERY of a lost claim (the entry comes back and the notice stops) is
//     pinned in dump/lost_claim_test.go, on the state, and not again here: this
//     layer's whole job is to turn a state into a sentence, and it does not know
//     which way the state last moved.

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
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// noticeMarker is matched as a PHRASE and not through indexUnprotectedNotice, like
// every other marker in this package's tests, so these tests fail against a build
// whose notice says something else rather than tautologically agreeing with it.
//
// It is the sentence BOTH notices open with, deliberately: it is the fact a reader
// has to act on, and it is the same fact either way. What tells the two apart is
// noticeUnwritableMarker and noticeLostMarker below.
const noticeMarker = "индекс выгрузки отдаётся без защиты"

// noticeUnwritableMarker and noticeLostMarker are the halves that must NOT be
// interchangeable. The first describes a cache that refused the write; the second a
// cache that took it and a claim that has since stopped being refreshable. Telling a
// user in the second state to make their cache writable is an instruction about a
// state they are not in.
const (
	noticeUnwritableMarker = "Серверу не удалось записать заявку читателя"
	noticeLostMarker       = "больше не удаётся обновить"
)

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
// Russian, with the check applied to the shipped constants rather than to a copy.
func TestIndexNotice_TheNoticeCarriesNoDash(t *testing.T) {
	for name, notice := range map[string]string{
		"indexUnprotectedNotice": indexUnprotectedNotice,
		"indexClaimLostNotice":   indexClaimLostNotice,
	} {
		for _, r := range []rune{'—', '–', '‒', '―', '−'} {
			if strings.ContainsRune(notice, r) {
				t.Errorf("%s contains %q (U+%04X), which customer-facing RU text must not carry",
					name, string(r), r)
			}
		}
	}
	// POSITIVE CONTROL: the check can fire. Without it a test comparing against an
	// empty set of characters would pass on any text at all.
	if !strings.ContainsRune("текст — с тире", '—') {
		t.Fatal("the control failed: the dash check cannot detect an em dash")
	}
}

// TestIndexNotice_TheSentenceMatchesTheState pins the choice between the two
// notices, which is the whole of what this layer decides.
//
// A single notice for both states would be a general sentence standing in for a
// false specific: «Серверу не удалось записать заявку читателя» about a cache that
// took the write is not a rounding error, it is a wrong instruction at the moment
// the reader is deciding what to do.
func TestIndexNotice_TheSentenceMatchesTheState(t *testing.T) {
	cases := []struct {
		name  string
		state dump.UnprotectedState
		want  string
	}{
		{"protected", dump.UnprotectedState{}, ""},
		{"protected with a stray flag", dump.UnprotectedState{ClaimLost: true}, ""},
		{"the claim could not be written", dump.UnprotectedState{Reason: "claim refused"}, indexUnprotectedNotice},
		{"the claim was lost", dump.UnprotectedState{Reason: "claim lost", ClaimLost: true}, indexClaimLostNotice},
	}
	for _, c := range cases {
		if got := indexProtectionNotice(c.state); got != c.want {
			t.Errorf("%s: got notice %q, want %q", c.name, got, c.want)
		}
	}

	// AND THE TWO SENTENCES ARE ACTUALLY DIFFERENT, matched as phrases rather than
	// through the constants, so a build whose two notices had drifted into the same
	// text would fail here instead of agreeing with itself.
	if !strings.Contains(indexUnprotectedNotice, noticeUnwritableMarker) {
		t.Errorf("the unwritable-cache notice no longer says what failed: %q", indexUnprotectedNotice)
	}
	if strings.Contains(indexClaimLostNotice, noticeUnwritableMarker) {
		t.Errorf("the lost-claim notice tells the user their cache refused a write it accepted: %q",
			indexClaimLostNotice)
	}
	if !strings.Contains(indexClaimLostNotice, noticeLostMarker) {
		t.Errorf("the lost-claim notice does not say what actually happened: %q", indexClaimLostNotice)
	}
	// BOTH NAME A REMEDY. A warning with no remedy is noise by the second time, and
	// the remedies differ: a cache that already took the write is not fixed by being
	// made writable, so the lost-claim notice offers a restart instead.
	for _, want := range []string{"MCP_1C_CACHE_DIR", "--cache-dir"} {
		if !strings.Contains(indexClaimLostNotice, want) {
			t.Errorf("the lost-claim notice does not name %q as a remedy: %q", want, indexClaimLostNotice)
		}
	}
	if !strings.Contains(indexClaimLostNotice, "Перезапустите сервер") {
		t.Errorf("the lost-claim notice does not offer the restart that takes a fresh claim: %q",
			indexClaimLostNotice)
	}
	if strings.Contains(indexClaimLostNotice, "доступным для записи") {
		t.Errorf("the lost-claim notice tells the user to make a cache writable that already is: %q",
			indexClaimLostNotice)
	}
}

// TestIndexNotice_ALostClaimReachesTheRealHandler is the end-to-end one, and it is
// the only test on this side that runs the PRODUCTION heartbeat interval, so nothing
// between the registry entry and the sentence is stubbed.
//
// It costs one heartbeat of wall clock, and that is the point: the shortened beat
// that dump/lost_claim_test.go uses is a field only tests set, so a build in which
// the heartbeat no longer routes anything to the Index would still pass over there
// if the seam itself were what carried the report. Here nothing is shortened.
func TestIndexNotice_ALostClaimReachesTheRealHandler(t *testing.T) {
	dumpDir := noticeDump(t)
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

	// CONTROL: a healthy claim, and an answer that says nothing about protection.
	if st := idx.Unprotected(); st.Reason != "" {
		t.Fatalf("the open is already unprotected, so nothing below tests the transition: %+v", st)
	}
	if text := callSearch(t, idx); strings.Contains(text, noticeMarker) {
		t.Fatalf("the control failed: a healthy index already warns:\n%s", text)
	}

	entry := noticeReaderEntry(t, cacheDir)
	if err := os.Remove(entry); err != nil {
		t.Fatalf("removing the reader entry, which is what a peer's reaper does: %v", err)
	}

	// One production heartbeat plus slack. Nothing here shortens it.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && idx.UnprotectedReason() == "" {
		time.Sleep(250 * time.Millisecond)
	}
	if idx.UnprotectedReason() == "" {
		t.Fatal("the production heartbeat never reported the lost claim to the index")
	}

	text := callSearch(t, idx)
	if !strings.Contains(text, noticeMarker) {
		t.Fatalf("a search answered out of a generation nothing protects carries no notice:\n%s", text)
	}
	if !strings.HasPrefix(text, "> ") {
		t.Errorf("the notice is not the first thing in the answer; it starts with %q",
			strings.SplitN(text, "\n", 2)[0])
	}
	// THE RIGHT SENTENCE. This is the assertion the layer boundary exists for: the
	// cache here took the claim write, so the unwritable-cache text would be false.
	if !strings.Contains(text, noticeLostMarker) {
		t.Errorf("the answer carries the wrong notice for a claim that was lost:\n%s", text)
	}
	if strings.Contains(text, noticeUnwritableMarker) {
		t.Errorf("the answer tells the user their cache refused a write it accepted:\n%s", text)
	}
	// AND IT IS STILL AN ANSWER.
	if searchRenderedMatches(text) == 0 {
		t.Errorf("the unprotected index answered with no matches at all:\n%s", text)
	}
}

// noticeReaderEntry returns the single reader-registry entry under cacheDir. It
// walks rather than recomputing a generation signature, so it stays independent of
// how one is derived.
func noticeReaderEntry(t *testing.T, cacheDir string) string {
	t.Helper()
	var found []string
	if err := filepath.WalkDir(cacheDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Base(filepath.Dir(p)) == "readers" {
			found = append(found, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walking the cache: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one reader entry under %s, found %d: %v", cacheDir, len(found), found)
	}
	return found[0]
}
