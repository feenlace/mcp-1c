package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
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
