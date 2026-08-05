package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// searchTotalRe pulls the match count out of the search_code answer, which is the
// only number a client actually sees.
//
// The header has two shapes and both are spelled out here rather than matched
// loosely. When every counted match could be rendered it is the plain
// «(модулей с совпадениями: N)». When some hits were dropped because their module
// could no longer be read, the header names the number as the index's and prints
// what the body actually holds next to it, so that the first line of the answer
// does not assert a count the body cannot support. That is exactly the state
// TestIntegration_ReloadDump_DropsADeletedModule puts the server in.
//
// The unit noun is captured, not skipped over. searchTotals below calls all three
// modes and smart counts MODULES while regex and exact count LINES; before the
// noun existed those three numbers were printed by one sentence, which is what a
// customer reading 2150 off this header ran into. Capturing it lets the helper
// assert, over the MCP wire, that each mode names its own quantity.
var searchTotalRe = regexp.MustCompile(`\((модулей|строк) с совпадениями(?: в индексе)?: (\d+)(?:, показано \d+)?\)`)

// searchUnitNouns is the noun each mode's header must use.
var searchUnitNouns = map[string]string{"smart": "модулей", "regex": "строк", "exact": "строк"}

// setupReloadDump wires a server over a dump the test owns and can rewrite, using
// the generation serve path (build + open read-only) that a real `serve` uses.
func setupReloadDump(t *testing.T) (*mcp.ClientSession, string, func()) {
	t.Helper()
	mock := httptest.NewServer(mock1CHandler())
	client := onec.NewClient(mock.URL, "", "")

	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSL(t, dumpDir, "CommonModules/ОбщегоНазначения/Ext/Module.bsl",
		"Процедура Пример() Экспорт\n\t// исходныйМаркер\nКонецПроцедуры\n")

	gensig, err := dump.GenSig(dumpDir)
	if err != nil {
		mock.Close()
		t.Fatalf("GenSig: %v", err)
	}
	if err := dump.BuildGeneration(dumpDir, cacheDir, gensig); err != nil {
		mock.Close()
		t.Fatalf("BuildGeneration: %v", err)
	}
	dumpIndex, err := dump.OpenGenerationReadOnly(dumpDir, cacheDir, gensig)
	if err != nil {
		mock.Close()
		t.Fatalf("OpenGenerationReadOnly: %v", err)
	}
	deadline := time.After(60 * time.Second)
	for !dumpIndex.Ready() {
		select {
		case <-deadline:
			dumpIndex.Close()
			mock.Close()
			t.Fatal("timed out waiting for the dump index to become ready")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	srv := New("test", client, dumpIndex)
	session, cleanup := connectSession(t, srv, func() { dumpIndex.Close(); mock.Close() })
	return session, dumpDir, cleanup
}

func callText(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) returned a tool error: %s", name, b.String())
	}
	return b.String()
}

// searchTotals runs one query through search_code in all three modes over the
// real MCP wire and returns the totals the client sees.
func searchTotals(t *testing.T, session *mcp.ClientSession, query string) (smart, regex, exact int) {
	t.Helper()
	out := [3]*int{&smart, &regex, &exact}
	for i, mode := range []string{"smart", "regex", "exact"} {
		text := callText(t, session, "search_code", map[string]any{"query": query, "mode": mode, "limit": 20})
		m := searchTotalRe.FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("search_code(%s) answer has no match count:\n%s", mode, text)
		}
		if want := searchUnitNouns[mode]; m[1] != want {
			t.Errorf("search_code(%s) header calls its number %q, want %q: over the wire the "+
				"three modes must not label two different quantities the same way:\n%s",
				mode, m[1], want, text)
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("search_code(%s) match count %q: %v", mode, m[2], err)
		}
		*out[i] = n
	}
	return smart, regex, exact
}

// TestIntegration_ReloadDump_MakesANewModuleSearchable is the end-to-end check
// over the MCP wire: a module added to the dump after the server started is
// invisible to all three search modes, and one reload_dump call makes it visible
// in all three.
func TestIntegration_ReloadDump_MakesANewModuleSearchable(t *testing.T) {
	session, dumpDir, cleanup := setupReloadDump(t)
	defer cleanup()

	const marker = "МаркерДобавленногоПослеСтарта"
	if s, r, e := searchTotals(t, session, marker); s != 0 || r != 0 || e != 0 {
		t.Fatalf("marker found before it was written: smart=%d regex=%d exact=%d", s, r, e)
	}

	mkBSL(t, dumpDir, "CommonModules/Свежий/Ext/Module.bsl",
		"Процедура "+marker+"() Экспорт\nКонецПроцедуры\n")

	if s, r, e := searchTotals(t, session, marker); s != 0 || r != 0 || e != 0 {
		t.Fatalf("the new module was visible WITHOUT reload_dump: smart=%d regex=%d exact=%d", s, r, e)
	}

	out := callText(t, session, "reload_dump", nil)
	if !strings.Contains(out, "обновлён") {
		t.Fatalf("reload_dump did not report an update:\n%s", out)
	}
	if !strings.Contains(out, "Модулей было: 1") || !strings.Contains(out, "Модулей стало: 2") {
		t.Fatalf("reload_dump did not report the before/after counts:\n%s", out)
	}

	smart, regex, exact := searchTotals(t, session, marker)
	if smart == 0 || regex == 0 || exact == 0 {
		t.Fatalf("the new module is still not searchable after reload_dump: smart=%d regex=%d exact=%d",
			smart, regex, exact)
	}
}

// TestIntegration_ReloadDump_UnchangedDumpReportsNoWork checks the honest answer
// path over the wire.
func TestIntegration_ReloadDump_UnchangedDumpReportsNoWork(t *testing.T) {
	session, _, cleanup := setupReloadDump(t)
	defer cleanup()

	out := callText(t, session, "reload_dump", nil)
	if !strings.Contains(out, "не изменилась") {
		t.Fatalf("an unchanged dump was not reported as unchanged:\n%s", out)
	}
	if strings.Contains(out, "перестроен полностью") {
		t.Fatalf("reload_dump claimed a rebuild it did not do:\n%s", out)
	}
}

// TestIntegration_ReloadDump_DropsADeletedModule covers the deletion case end to
// end: smart is the mode that keeps counting a deleted document until a reload.
func TestIntegration_ReloadDump_DropsADeletedModule(t *testing.T) {
	session, dumpDir, cleanup := setupReloadDump(t)
	defer cleanup()

	const marker = "МаркерУдаляемогоМодуля"
	mkBSL(t, dumpDir, "CommonModules/Временный/Ext/Module.bsl",
		"Процедура "+marker+"() Экспорт\nКонецПроцедуры\n")
	callText(t, session, "reload_dump", nil)
	if s, r, e := searchTotals(t, session, marker); s == 0 || r == 0 || e == 0 {
		t.Fatalf("marker not indexed after the first reload: smart=%d regex=%d exact=%d", s, r, e)
	}

	if err := os.RemoveAll(filepath.Join(dumpDir, "CommonModules", "Временный")); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := searchTotals(t, session, marker); s == 0 {
		t.Fatal("smart search stopped counting the deleted module without a reload; " +
			"this test can no longer prove reload_dump does it")
	}

	callText(t, session, "reload_dump", nil)
	if s, r, e := searchTotals(t, session, marker); s != 0 || r != 0 || e != 0 {
		t.Fatalf("deleted module still found after reload_dump: smart=%d regex=%d exact=%d", s, r, e)
	}
}

// TestIntegration_ReloadDump_AbsentWithoutADump checks the tool is not offered
// when there is no dump to reload.
func TestIntegration_ReloadDump_AbsentWithoutADump(t *testing.T) {
	session, cleanup := setupIntegrationNoDump(t)
	defer cleanup()

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range result.Tools {
		if tool.Name == "reload_dump" || tool.Name == "search_code" {
			t.Errorf("%q is offered without a --dump", tool.Name)
		}
	}
}
