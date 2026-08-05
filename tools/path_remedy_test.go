package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// A REMEDY THAT DOES NOTHING IS WORSE THAN NONE.
//
// Two of the standing notices diagnose the SAME condition: the path given in
// --dump is not the root of the dump, so the module keys are derived from the
// wrong anchor. Both then prescribed `reload_dump`.
//
// reload_dump cannot carry out that instruction. It takes no arguments at all
// (ReloadDumpTool's schema has no properties), and dump.Index.Reload re-reads the
// directory the index was CONSTRUCTED with, which is the wrong one. Even pointed
// at the same directory it short-circuits: it compares the dump's content
// signature against the one it is serving and returns without rebuilding when
// nothing on disk moved, and correcting a --dump flag moves nothing on disk. So
// the reader who follows the instruction sees «изменений не обнаружено», the keys
// are exactly as wrong as before, and the notice reappears verbatim on the next
// answer.
//
// The flag is read at startup. The remedy is a restart.

// pathNotices are the two notices that diagnose a wrong --dump.
func pathNotices() map[string]string {
	return map[string]string{
		"collapse": indexCollapseNotice(dump.CollapsedKeyState{
			Files: 2, Keys: 1, Sample: []string{"Справочник.Товары.МодульОбъекта"},
		}),
		"wrapped": indexWrappedNotice(dump.WrappedPathState{Files: 13575, Total: 13575}),
	}
}

// TestAWrongPathIsNotFixedByReloadDump pins what those two notices tell the user
// to do.
func TestAWrongPathIsNotFixedByReloadDump(t *testing.T) {
	// CONTROL FIRST, and it is the reason this is not simply "the string
	// reload_dump must never appear": the tool IS the right remedy elsewhere, and
	// a blanket ban would delete a working instruction. A search whose hits could
	// not be re-read is fixed by re-running the dump and calling reload_dump,
	// because there the path is right and the FILES moved.
	shortfall := searchShortfallNote(dump.SearchStats{Total: 40, Unreadable: 9, Unit: dump.SearchUnitModules}, 1, "модулей")
	if !strings.Contains(shortfall, "reload_dump") {
		t.Fatal("control failed: the shortfall note no longer prescribes reload_dump, so " +
			"the assertions below cannot tell a removed wrong remedy from a removed right one")
	}

	for name, notice := range pathNotices() {
		if notice == "" {
			t.Fatalf("%s: the notice under test is empty, so nothing below reads anything", name)
		}
		// THE IMPERATIVE is what is banned, not the word. Naming the tool in order
		// to say it does not apply is the opposite of prescribing it, and a reader
		// who was told to call it before will otherwise try it anyway and conclude
		// the diagnosis was wrong rather than the instruction.
		if strings.Contains(notice, "вызовите `reload_dump`") {
			t.Errorf("%s notice still prescribes reload_dump for a wrong --dump. The tool takes "+
				"no arguments and re-reads the directory the index was built with, so following "+
				"the instruction changes nothing:\n%s", name, notice)
		}
		if !strings.Contains(notice, "перезапустите сервер") {
			t.Errorf("%s notice does not name the thing that actually applies a new --dump:\n%s",
				name, notice)
		}
		if strings.Contains(notice, "reload_dump") && !strings.Contains(notice, "не поможет") {
			t.Errorf("%s notice mentions reload_dump without saying it does not apply here, "+
				"which reads as an instruction:\n%s", name, notice)
		}
	}
}

// TestReloadDumpCannotBeGivenAPath is the mechanical half of the claim above,
// asserted against the tool's own schema rather than against prose about it.
func TestReloadDumpCannotBeGivenAPath(t *testing.T) {
	var schema struct {
		Type                 string         `json:"type"`
		Properties           map[string]any `json:"properties"`
		AdditionalProperties *bool          `json:"additionalProperties"`
	}
	raw, ok := ReloadDumpTool().InputSchema.(json.RawMessage)
	if !ok {
		t.Fatalf("reload_dump input schema is %T, not raw JSON this test can read", ReloadDumpTool().InputSchema)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("reload_dump input schema does not parse: %v", err)
	}
	if len(schema.Properties) != 0 {
		t.Errorf("reload_dump accepts %d argument(s) %v; if one of them is a dump path the "+
			"notices may prescribe it again", len(schema.Properties), schema.Properties)
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Error("reload_dump does not refuse unknown arguments, so 'it takes no path' is " +
			"not something its schema establishes")
	}
}

// TestPathRemedyCarriesNoDash scans the rewritten sentences per codepoint.
func TestPathRemedyCarriesNoDash(t *testing.T) {
	dashes := []rune{'\u2014', '\u2013', '\u2012', '\u2015', '\u2212'}

	// POSITIVE CONTROL: the scan sees a dash when one is there.
	seen := false
	for _, r := range "путь \u2014 к корню" {
		for _, d := range dashes {
			if r == d {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("control failed: the per-codepoint scan did not see U+2014 in a string that carries it")
	}

	for name, notice := range pathNotices() {
		// The echoed sample lives in a fence, where a dash is the customer's own
		// directory name and not something this project wrote. The PROSE is what
		// the rule is about.
		prose := notice
		if before, _, ok := strings.Cut(notice, "```"); ok {
			prose = before
		}
		for i, r := range prose {
			for _, d := range dashes {
				if r == d {
					t.Errorf("%s notice prose carries %q (U+%04X) at byte %d:\n%s",
						name, string(r), r, i, prose)
				}
			}
		}
	}
}
