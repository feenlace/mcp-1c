package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReloadDumpTool_Shape(t *testing.T) {
	tool := ReloadDumpTool()
	if tool.Name != "reload_dump" {
		t.Fatalf("tool name = %q, want reload_dump", tool.Name)
	}
	if tool.Annotations == nil {
		t.Fatal("tool has no annotations")
	}
	// A reload rewrites the on-disk index cache, so claiming read-only would be a
	// lie to any client that gates writes on the hint.
	if tool.Annotations.ReadOnlyHint {
		t.Error("reload_dump must not be marked read-only: it rebuilds the index cache")
	}
	if !tool.Annotations.IdempotentHint {
		t.Error("reload_dump is idempotent: a second call on an unchanged dump does nothing")
	}
	if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
		t.Error("reload_dump must be marked non-destructive: it never writes to the dump or to 1C")
	}

	// The schema must be valid JSON and take no arguments.
	raw, ok := tool.InputSchema.(json.RawMessage)
	if !ok {
		t.Fatalf("InputSchema is %T, want json.RawMessage", tool.InputSchema)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("InputSchema has no properties object: %v", schema)
	}
	if len(props) != 0 {
		t.Errorf("reload_dump takes no arguments, but the schema declares %v", props)
	}

	// The description has to tell the model WHEN to call it, otherwise the tool
	// exists but never fires. "DumpConfigToFiles" is the trigger phrase.
	if !strings.Contains(tool.Description, "DumpConfigToFiles") {
		t.Error("the description does not name DumpConfigToFiles, so nothing links the tool to re-dumping")
	}
	if !strings.Contains(tool.Description, "search_code") {
		t.Error("the description does not say which tool goes stale without a reload")
	}
}

func TestFormatReloadReport_NoChangeDoesNotClaimWork(t *testing.T) {
	out := FormatReloadReport(dump.ReloadReport{
		Changed:       false,
		Rebuilt:       false,
		ModulesBefore: 13575,
		ModulesAfter:  13575,
		Elapsed:       870 * time.Millisecond,
	}, "/dumps/base")

	if !strings.Contains(out, "не изменилась") {
		t.Errorf("an unchanged dump must be reported as unchanged:\n%s", out)
	}
	for _, forbidden := range []string{"перестроен полностью", "обновлён"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the no-change report claims work it did not do (%q):\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, "13575") {
		t.Errorf("the no-change report omits the module count:\n%s", out)
	}
	if !strings.Contains(out, "0.9 с") {
		t.Errorf("the no-change report omits the elapsed time:\n%s", out)
	}
	if !strings.Contains(out, "/dumps/base") {
		t.Errorf("the no-change report does not say which directory was checked:\n%s", out)
	}
}

func TestFormatReloadReport_ChangedStatesBeforeAfterAndTime(t *testing.T) {
	out := FormatReloadReport(dump.ReloadReport{
		Changed:       true,
		Rebuilt:       true,
		ModulesBefore: 13575,
		ModulesAfter:  13576,
		Elapsed:       13*time.Second + 400*time.Millisecond,
	}, "/dumps/base")

	for _, want := range []string{"13575", "13576", "13.4 с", "перестроен полностью", "smart", "regex", "exact"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report omits %q:\n%s", want, out)
		}
	}
}

func TestFormatReloadReport_ReusedGenerationSaysSo(t *testing.T) {
	out := FormatReloadReport(dump.ReloadReport{
		Changed:       true,
		Rebuilt:       false,
		ModulesBefore: 10,
		ModulesAfter:  11,
		Elapsed:       120 * time.Millisecond,
	}, "/dumps/base")

	if strings.Contains(out, "перестроен полностью") {
		t.Errorf("no rebuild happened, but the report says it did:\n%s", out)
	}
	if !strings.Contains(out, "уже был в кэше") {
		t.Errorf("the report does not explain why it was fast:\n%s", out)
	}
}

// reloadStillServesMarker is the reassurance the error path used to append to
// EVERY failure: that the index is untouched and search keeps answering from the
// dump loaded earlier. Matched as a phrase rather than through the constant, the
// way the note markers in form_test.go are.
const reloadStillServesMarker = "поиск продолжает работать"

// TestNewReloadDumpHandler_ColdStartDoesNotPromiseSearchStillWorks is the
// honesty case. On a server that has just started, the index is still building
// and nothing has EVER been loaded: dump.Index.Search returns "search index is
// building" for every query. Telling that caller their search keeps answering
// "from the dump loaded earlier" is false on the first natural attempt, and it
// is the first attempt a user makes.
func TestNewReloadDumpHandler_ColdStartDoesNotPromiseSearchStillWorks(t *testing.T) {
	// Deliberately NOT closed: Index.Close waits on Done(), which only
	// FinishServeOpen ever closes, and calling that would end the very state
	// under test. The placeholder holds no shards and starts no goroutine, so
	// there is nothing to release.
	index := dump.NewServePlaceholder(t.TempDir())

	// Premise, asserted rather than assumed: this index serves nothing yet.
	if index.Ready() {
		t.Fatal("premise broken: a fresh placeholder reports itself ready")
	}
	if _, _, err := index.Search(dump.SearchParams{Query: "что-нибудь"}); err == nil {
		t.Fatal("premise broken: search answers on an index that never loaded a dump")
	}

	res, err := callReloadHandler(t, index)
	// The failure is a tool result with IsError now. What it must and must not
	// claim is unchanged; only where the caller reads it moved.
	text := failureText(t, res, err)
	if strings.Contains(text, reloadStillServesMarker) {
		t.Errorf("the failure promises that search still works, but nothing was ever loaded:\n%s", text)
	}
	// The caller still has to learn what state they are in, so the absence of
	// the false claim is not enough on its own.
	if !strings.Contains(text, "не отвечает") {
		t.Errorf("the failure never says search is unavailable, so the caller learns nothing "+
			"about whether search works:\n%s", text)
	}
}

// TestNewReloadDumpHandler_WorkingIndexKeepsTheReassurance is the other half:
// when an index IS serving, a failed reload really does leave it serving, and
// dropping the reassurance would be its own falsehood. The failure used here is
// the emptiness guard, a real refusal on a real index rather than an injected
// one.
func TestNewReloadDumpHandler_WorkingIndexKeepsTheReassurance(t *testing.T) {
	dumpDir := t.TempDir()
	mkBSL(t, dumpDir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\n\t// маркерВыгрузки\nКонецПроцедуры\n")

	index, err := dump.NewIndex(dumpDir, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { index.Close() })
	waitReady(t, index, 60*time.Second)

	// Premise: this index answers searches before the failed reload.
	//
	// SMART mode, and not exact or regex, because the failure injected below has
	// to remove the .bsl file: exact and regex re-read the file from disk on
	// every query (see the freeze analysis at the top of dump/reload.go), so
	// after the removal they would report zero whatever the reload did, and the
	// assertion could not distinguish a serving index from a broken one. Smart
	// answers from the generation the index still holds, which is exactly the
	// state the reassurance is about.
	if _, total, err := index.Search(dump.SearchParams{Query: "маркерВыгрузки", Mode: dump.SearchModeSmart}); err != nil || total == 0 {
		t.Fatalf("premise broken: baseline search total=%d err=%v", total, err)
	}

	// Empty the dump: the reload builds a valid but empty generation and the
	// emptiness guard refuses to swap it in, leaving the old index serving.
	if err := os.RemoveAll(filepath.Join(dumpDir, "Catalogs")); err != nil {
		t.Fatal(err)
	}

	res, err := callReloadHandler(t, index)
	text := failureText(t, res, err)
	if !strings.Contains(text, reloadStillServesMarker) {
		t.Errorf("the index is still serving, so the failure must say so:\n%s", text)
	}
	// And the claim has to be true: prove the index really did keep serving.
	if _, total, searchErr := index.Search(dump.SearchParams{Query: "маркерВыгрузки", Mode: dump.SearchModeSmart}); searchErr != nil || total == 0 {
		t.Errorf("the error claims search still works, but it does not: total=%d err=%v", total, searchErr)
	}
}

// callReloadHandler drives the reload_dump handler the way the MCP server does.
func callReloadHandler(t *testing.T, index *dump.Index) (*mcp.CallToolResult, error) {
	t.Helper()
	handler := NewReloadDumpHandler(index)
	return handler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "reload_dump", Arguments: json.RawMessage(`{}`)},
	})
}

// TestFormatReloadReport_NoDashesInRussianText guards the house style: the user
// facing text must not contain a dash (em, en, horizontal bar, minus). Hyphens
// inside words are fine. MCP tool DESCRIPTIONS are exempt and are not checked
// here; this covers only what the user reads back from a call.
func TestFormatReloadReport_NoDashesInRussianText(t *testing.T) {
	reports := []dump.ReloadReport{
		{Changed: false, ModulesAfter: 1, Elapsed: time.Second},
		{Changed: true, Rebuilt: true, ModulesBefore: 1, ModulesAfter: 2, Elapsed: time.Second},
		{Changed: true, Rebuilt: false, ModulesBefore: 1, ModulesAfter: 2, Elapsed: time.Second},
	}
	dashes := []rune{'\u2014', '\u2013', '\u2015', '\u2212'}
	for _, rep := range reports {
		out := FormatReloadReport(rep, "/dumps/base")
		for _, d := range dashes {
			if strings.ContainsRune(out, d) {
				t.Errorf("report contains %U:\n%s", d, out)
			}
		}
	}
	// Positive control: the check must actually be able to fire.
	if !strings.ContainsRune("текст \u2014 продолжение", '\u2014') {
		t.Fatal("the dash check cannot detect a dash it was handed directly")
	}
}
