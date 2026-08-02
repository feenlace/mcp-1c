package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tool_registry_contract_test.go IS THE OTHER HALF OF THE GUARD: the AST census in
// tools/tool_error_contract_test.go proves what the SOURCE contains, and this file
// proves what a RUNNING SERVER actually offers.
//
// WHY THE REGISTRY IS ENUMERATED AND NOT GREPPED. A grep over server.go finds
// AddTool calls; it does not find a tool the SDK refused to register, a tool
// registered twice, or a tool whose registration is conditional on state the grep
// cannot evaluate. The enumeration here is a real in-memory session asking a real
// server what it has: server.New, mcp.NewInMemoryTransports, ListTools. The list
// is the server's own answer, so the only way to satisfy it is to actually
// register the tool.
//
// TWO SERVERS, NOT ONE. server.New registers search_code and reload_dump only
// when a dump index is present. Counting once would let an accidentally nil index
// satisfy a count of 11 by never being noticed; the second server pins the other
// arm at 9, and the difference between them is asserted against the table rather
// than assumed.
//
// EVERY COUNT IS EXACT. Not >= 11. Tool #12 arriving unclassified is the event
// this file exists to catch, and a floor is satisfied by it.

// toolExpect is one row of the expectation table.
type toolExpect struct {
	// needsDump marks a tool server.New registers only when a dump index exists.
	needsDump bool
	// protocolOnly marks the one tool registered through the generic mcp.AddTool,
	// whose declared schema IS enforced before the handler runs. Every other tool
	// is raw-registered, where the published schema is decoration and the
	// handler's own check is the entire gate.
	protocolOnly bool
}

// wantTools IS THE EXPECTATION TABLE, enumeration (C) for the registry.
//
// It is compared against the live listing in BOTH directions: a listed tool with
// no row here and a row here with no listed tool are both failures. Keeping the
// table as the single place the split is written down is what makes the 9-without
// -a-dump count derived rather than a second literal that can drift from this one.
var wantTools = map[string]toolExpect{
	"get_metadata_tree":      {},
	"get_object_structure":   {},
	"execute_query":          {},
	"get_form_structure":     {},
	"validate_query":         {},
	"get_event_log":          {},
	"get_configuration_info": {},
	"analyze_subsystems":     {},
	"bsl_syntax_help":        {protocolOnly: true},
	"search_code":            {needsDump: true},
	"reload_dump":            {needsDump: true},
}

const (
	// wantToolsWithDump is a literal on purpose. Comparing the listing only
	// against len(wantTools) would let an emptied table agree with an empty
	// server, which is the vacuous pass this guard is written to be unable to
	// produce.
	wantToolsWithDump = 11

	// wantToolsWithoutDump is the same fact measured the other way: server.go
	// registers the search_code / reload_dump pair together or not at all.
	wantToolsWithoutDump = 9
)

// registryContract is what a live session reported.
type registryContract struct {
	withDump    []string
	withoutDump []string
}

// verifyRegistry is R1 through R3. As in the tools half, the verdict is a
// function over data rather than a body full of t.Error, so it can be aimed at an
// empty listing and shown to report something.
func verifyRegistry(c registryContract) contractViolations {
	var f contractViolations

	// The table itself is pinned first. Everything below compares the server to
	// the table, so a table that lost entries would otherwise quietly lower the
	// bar for the server.
	if len(wantTools) != wantToolsWithDump {
		f.addf("R0", "the expectation table has %d entries, want exactly %d",
			len(wantTools), wantToolsWithDump)
	}
	var wantWithout int
	for _, e := range wantTools {
		if !e.needsDump {
			wantWithout++
		}
	}
	if wantWithout != wantToolsWithoutDump {
		f.addf("R0", "the expectation table says %d tools survive without a dump, want exactly %d",
			wantWithout, wantToolsWithoutDump)
	}

	// R1 — the live listing with a dump index. EXACT.
	if len(c.withDump) != wantToolsWithDump {
		f.addf("R1", "a server built with a dump index lists %d tools, want exactly %d: %v",
			len(c.withDump), wantToolsWithDump, sortedNames(c.withDump))
	}

	// R2 — set equality with the table, BOTH directions.
	listed := map[string]bool{}
	for _, n := range c.withDump {
		if listed[n] {
			f.addf("R2", "tool %q is listed twice", n)
		}
		listed[n] = true
	}
	for n := range wantTools {
		if !listed[n] {
			f.addf("R2", "tool %q is in the expectation table and the server does not offer it", n)
		}
	}
	for n := range listed {
		if _, ok := wantTools[n]; !ok {
			f.addf("R2", "the server offers tool %q and no expectation row mentions it; "+
				"a tool was registered without being classified", n)
		}
	}

	// R3 — the other arm. Its expected content is DERIVED from the table, so the
	// two counts cannot drift apart by editing one of them.
	if len(c.withoutDump) != wantToolsWithoutDump {
		f.addf("R3", "a server built without a dump index lists %d tools, want exactly %d: %v",
			len(c.withoutDump), wantToolsWithoutDump, sortedNames(c.withoutDump))
	}
	listedNoDump := map[string]bool{}
	for _, n := range c.withoutDump {
		listedNoDump[n] = true
	}
	for n, e := range wantTools {
		switch {
		case e.needsDump && listedNoDump[n]:
			f.addf("R3", "tool %q needs a dump index and is offered without one", n)
		case !e.needsDump && !listedNoDump[n]:
			f.addf("R3", "tool %q does not need a dump index and is missing without one", n)
		}
	}
	return f
}

// contractViolations mirrors contractFindings in the tools package: a tagged list
// so a failure names the assertion that produced it.
type contractViolations []string

func (f *contractViolations) addf(code, format string, a ...any) {
	*f = append(*f, code+": "+fmt.Sprintf(format, a...))
}

func (f contractViolations) has(code string) bool {
	for _, s := range f {
		if strings.HasPrefix(s, code+":") {
			return true
		}
	}
	return false
}

func sortedNames(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// listToolNames asks a live server what it offers.
//
// DefaultPageSize is 1000, so a listing of this size arrives in one page; the
// NextCursor check below is here so that stops being an assumption the day it
// stops being true.
func listToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if res.NextCursor != "" {
		t.Fatalf("the tool listing is paginated (cursor %q), so this enumeration is partial", res.NextCursor)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestToolRegistryContract is the registry guard.
func TestToolRegistryContract(t *testing.T) {
	mock := failingQueryServer(t, http.StatusBadRequest, `{"error":"Поле не найдено \"Номенклатура\""}`)
	defer mock.Close()
	client := onec.NewClient(mock.URL, "", "")

	// Deliberately NOT closed. Index.Close blocks on Done(), and nothing on this
	// path ever closes that channel: NewServePlaceholder only builds the struct,
	// so it holds no shards, starts no goroutine, and reaches none of the sites
	// that close it. Closing it here would hang the test binary instead of failing
	// it. The same fixture note is on tools/toolwiring_test.go:placeholderIndex,
	// where it is phrased as "only FinishServeOpen ever closes"; that is narrower
	// than the tree, which has several closing sites, and the property being
	// relied on is that a placeholder reaches none of them.
	index := dump.NewServePlaceholder(t.TempDir())

	withDump, _, cleanupA := connectLoggedSession(t, New("guard", client, index), func() {})
	defer cleanupA()
	withoutDump, logB, cleanupB := connectLoggedSession(t, New("guard", client, nil), func() {})
	defer cleanupB()

	c := registryContract{
		withDump:    listToolNames(t, withDump),
		withoutDump: listToolNames(t, withoutDump),
	}

	findings := verifyRegistry(c)
	for _, v := range findings {
		t.Error(v)
	}
	if len(findings) > 0 {
		t.Fatalf("the tool registry contract has %d violations, listed above", len(findings))
	}

	// R4 — the protocol channel is still there. Without it, a blanket conversion
	// of every error into a tool result would satisfy R5 and nothing would say so.
	// bsl_syntax_help is the single protocolOnly row, and the table is walked
	// rather than the name being retyped, so removing the row breaks this too.
	protocolOnly := 0
	for name, e := range wantTools {
		if !e.protocolOnly {
			continue
		}
		protocolOnly++
		_, err, frame := call(t, withoutDump, logB, name, map[string]any{})
		if err == nil {
			t.Errorf("R4: %s accepted a call with no arguments; the generic registration path validates "+
				"the declared schema, so this is the protocol channel disappearing: %s", name, frame.raw)
			continue
		}
		var we *jsonrpc.Error
		if !errors.As(err, &we) {
			t.Errorf("R4: %s returned %T, which carries no JSON-RPC code: %v", name, err, err)
			continue
		}
		if we.Code != jsonrpc.CodeInvalidParams {
			t.Errorf("R4: %s answered code %d, want %d: %s", name, we.Code, jsonrpc.CodeInvalidParams, frame.raw)
		}
		if !frame.has("error") || frame.has("result") {
			t.Errorf("R4: %s frame shape disagrees with the client error: %s", name, frame.raw)
		}
	}
	if protocolOnly != 1 {
		t.Errorf("R4: the table has %d protocolOnly rows, want exactly 1; the contrast that makes "+
			"R5 meaningful is gone", protocolOnly)
	}

	// R5 — a raw-registered tool's own required check is operational ON THE WIRE.
	// execute_query with no arguments is the same mistake bsl_syntax_help refuses
	// at -32602, and the answer must be a readable result instead.
	_, err, frame := call(t, withoutDump, logB, "execute_query", map[string]any{})
	if err != nil {
		t.Fatalf("R5: execute_query with no arguments returned an error, so the caller cannot read "+
			"what it did wrong: %v", err)
	}
	if frame.has("error") {
		t.Errorf("R5: the frame carries an error member: %s", frame.raw)
	}
	if !frame.has("result") {
		t.Fatalf("R5: the frame carries no result member: %s", frame.raw)
	}
	assertFrameIsError(t, frame)
	if text := frameText(t, frame); !strings.Contains(text, "## Запрос не выполнен") {
		t.Errorf("R5: the failure is not rendered under the execute_query heading:\n%s", text)
	}

	t.Logf("registry: %d tools with a dump, %d without; expectation table has %d entries",
		len(c.withDump), len(c.withoutDump), len(wantTools))
}

// TestToolRegistryContract_AnEmptyListingIsAFailure IS THE PROOF THAT THE
// REGISTRY HALF CANNOT ENUMERATE ZERO AND CALL IT AGREEMENT.
//
// The failure mode is not hypothetical: a helper that returns no names on an
// error path, a session that never connected, a listing read from the wrong
// server. Each of them yields an empty slice, and a verdict built out of set
// containment alone is green on an empty set. It runs the SAME verifyRegistry the
// real test runs.
func TestToolRegistryContract_AnEmptyListingIsAFailure(t *testing.T) {
	findings := verifyRegistry(registryContract{})
	if !findings.has("R1") {
		t.Errorf("an empty listing produced no R1 finding: %v", findings)
	}
	if !findings.has("R2") {
		t.Errorf("an empty listing produced no R2 finding: %v", findings)
	}
	if !findings.has("R3") {
		t.Errorf("an empty listing produced no R3 finding: %v", findings)
	}
	t.Logf("an empty listing is rejected by %d findings", len(findings))

	// The other direction: a listing that is the right SIZE but holds a tool
	// nobody classified. A count-only guard passes this.
	var swapped []string
	for name := range wantTools {
		if name == "execute_query" {
			swapped = append(swapped, "tool_twelve")
			continue
		}
		swapped = append(swapped, name)
	}
	var without []string
	for _, n := range swapped {
		if e, ok := wantTools[n]; !ok || !e.needsDump {
			without = append(without, n)
		}
	}
	findings = verifyRegistry(registryContract{withDump: swapped, withoutDump: without})
	if !findings.has("R2") {
		t.Errorf("an unclassified tool of the right count produced no R2 finding: %v", findings)
	}
	t.Logf("an unclassified tool at the right count is rejected by %v", findings)
}
