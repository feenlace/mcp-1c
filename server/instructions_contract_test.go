package server

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/internal/instructions"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// instructions_contract_test.go asks a RUNNING SERVER the two questions the
// instruction text cannot answer about itself: did the client actually receive
// it, and is what it says about the tool registry still true.
//
// WHY A LIVE SESSION AND NOT AN ASSERTION ON server.New's ARGUMENT. Reading the
// argument would prove the value was passed. It would not prove it survives:
// mcp.NewServer copies ServerOptions by value and drops the caller's pointer, the
// field lives on ServerOptions rather than on Server, and the only read of it is
// InitializeResult.Instructions. Every one of those is a place the value could
// stop travelling, and only a client that received the initialize result can say
// it did not.
//
// SAME SHAPE AS tool_registry_contract_test.go, deliberately: server.New,
// mcp.NewInMemoryTransports, a real handshake, and the server's own answer as the
// data under test.

// instructionsTools is the set of tool names the instruction TEXT names.
//
// It is written down here, and that is the point: it is the bridge between prose
// and registry, and it is checked in BOTH directions below, so it cannot drift
// from either side without a failure. Direction one, every name here is a
// substring of the text, is also the anti-vacuity anchor for the whole guard
// suite: an emptied constant fails it immediately.
var instructionsTools = []string{
	"execute_query",
	"search_code",
	"get_event_log",
	"get_metadata_tree",
	"bsl_syntax_help",
	"reload_dump",
}

// capacityParams are the property names that would let a caller bound the size of
// a tool's answer. The instruction text tells the model that exactly three tools
// have one, so the set of tools that have one is DERIVED from the live schemas
// below rather than written down a second time.
//
// Wider than "limit" on purpose: the text's claim is about being able to bound the
// answer at all, so a cap that shipped under another name would falsify it just as
// squarely and must not be able to slip past a check spelled for one word.
var capacityParams = []string{"limit", "max", "max_results", "top", "count", "offset", "page_size", "size"}

// instructionsSession stands up a server with a dump index, which is the arm that
// offers all eleven tools; the text names two of them.
func instructionsSession(t *testing.T) (*mcp.ClientSession, *frameLog) {
	t.Helper()
	mock := failingQueryServer(t, http.StatusBadRequest, `{"error":"нет"}`)
	t.Cleanup(mock.Close)
	client := onec.NewClient(mock.URL, "", "")

	// Deliberately not closed, for the reason tool_registry_contract_test.go
	// gives at its own placeholder: Index.Close blocks on a Done() that a
	// placeholder never closes.
	index := dump.NewServePlaceholder(t.TempDir())

	session, log, cleanup := connectLoggedSession(t, New("guard", client, index), func() {})
	t.Cleanup(cleanup)
	return session, log
}

// listToolsForInstructions returns the live tool listing, whole, because the
// schema assertions below need more than the names.
func listToolsForInstructions(t *testing.T, session *mcp.ClientSession) []*mcp.Tool {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if res.NextCursor != "" {
		t.Fatalf("the tool listing is paginated (cursor %q), so this enumeration is partial", res.NextCursor)
	}
	if len(res.Tools) == 0 {
		t.Fatal("premise failed: the live listing is empty, and every comparison below would be vacuous")
	}
	return res.Tools
}

// TestInstructionsReachTheClient is the reachability assertion.
//
// Byte identity, not "non-empty" and not "contains something": the failure this
// guards against is the value being dropped or replaced somewhere between the
// constant and the wire, and a substring check passes on a truncated copy.
func TestInstructionsReachTheClient(t *testing.T) {
	// PREMISE: the constant is not empty. Without this, a server that sends no
	// instructions at all satisfies "" == "" and the test reports agreement.
	if instructions.Text == "" {
		t.Fatal("premise failed: instructions.Text is empty, so byte identity below is satisfied " +
			"by a server that sends nothing")
	}

	session, log := instructionsSession(t)

	got := session.InitializeResult()
	if got == nil {
		t.Fatal("the client session has no InitializeResult")
	}
	if got.Instructions != instructions.Text {
		t.Errorf("the instructions the client received are not the constant.\n"+
			"got  %d bytes: %q\nwant %d bytes: %q",
			len(got.Instructions), truncateForMessage(got.Instructions),
			len(instructions.Text), truncateForMessage(instructions.Text))
	}

	// AND IT CROSSED THE TRANSPORT. The value above is decoded from the initialize
	// frame, so this is belt and braces, but it is the half that would still fail
	// if some future in-process shortcut handed the client a copy without sending
	// one. The probe is the first paragraph rather than the whole text because the
	// text contains double quotes (filter="..."), which are escaped inside the JSON
	// frame and so cannot be matched raw.
	probe := strings.SplitN(instructions.Text, "\n", 2)[0]
	if strings.ContainsAny(probe, `"\`) {
		t.Fatalf("control failed: the wire probe %q contains a character JSON escapes, so a miss "+
			"below would be an artefact of escaping rather than a missing frame", probe)
	}
	found := false
	for _, frame := range log.reads() {
		if strings.Contains(frame, probe) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no frame the server sent carries the instruction text; the client got it some other way")
	}
}

// TestInstructionsNameOnlyLiveTools is the two-directional name check.
func TestInstructionsNameOnlyLiveTools(t *testing.T) {
	if len(instructionsTools) == 0 {
		t.Fatal("premise failed: the name bridge is empty, so both directions below are vacuous")
	}

	session, _ := instructionsSession(t)
	listed := listToolsForInstructions(t, session)

	named := make(map[string]bool, len(instructionsTools))
	for _, n := range instructionsTools {
		named[n] = true
	}

	// DIRECTION 1: everything the bridge claims the text names, the text really
	// does name, and the server really does offer.
	live := make(map[string]bool, len(listed))
	for _, tool := range listed {
		live[tool.Name] = true
	}
	for _, n := range instructionsTools {
		if !strings.Contains(instructions.Text, n) {
			t.Errorf("the text does not name %q, so the bridge no longer describes it", n)
		}
		if !live[n] {
			t.Errorf("the text names %q but no such tool is offered; the text is telling the model "+
				"about a tool that is not there", n)
		}
	}

	// DIRECTION 2: no tool the server offers is mentioned by the text unless the
	// bridge says so. This is what fires when a tool is renamed into a name the
	// text happens to contain, or when someone adds a tool to the text and forgets
	// the bridge.
	for _, tool := range listed {
		mentioned := strings.Contains(instructions.Text, tool.Name)
		if mentioned != named[tool.Name] {
			if mentioned {
				t.Errorf("the text mentions the tool %q, which the bridge does not list", tool.Name)
			} else {
				t.Errorf("the bridge lists %q as named by the text, but the text does not contain it", tool.Name)
			}
		}
	}
}

// TestInstructionsLimitSentenceMatchesTheSchemas derives the limit claim from the
// live schemas.
//
// NOTHING HERE NAMES THE THREE TOOLS. The expected set is whatever the listing
// says has a capacity parameter, and the sentence is located by its opening rather
// than reproduced, so adding a limit to a fourth tool turns this red without
// anyone having remembered that the text exists.
func TestInstructionsLimitSentenceMatchesTheSchemas(t *testing.T) {
	session, _ := instructionsSession(t)
	listed := listToolsForInstructions(t, session)

	const anchor = "Параметр limit есть только у "
	start := strings.Index(instructions.Text, anchor)
	if start < 0 {
		t.Fatalf("the sentence beginning %q is gone from the text; if the claim was rewritten, "+
			"this guard has to be re-aimed at the new wording rather than deleted", anchor)
	}
	rest := instructions.Text[start:]
	end := strings.Index(rest, ".")
	if end < 0 {
		t.Fatalf("the limit sentence has no terminator: %q", rest)
	}
	sentence := rest[:end]

	var capped, named []string
	for _, tool := range listed {
		props := schemaProperties(t, tool)
		hasCap := false
		for _, p := range capacityParams {
			if _, ok := props[p]; ok {
				hasCap = true
				break
			}
		}
		if hasCap {
			capped = append(capped, tool.Name)
		}
		if strings.Contains(sentence, tool.Name) {
			named = append(named, tool.Name)
		}
	}
	sort.Strings(capped)
	sort.Strings(named)

	// PREMISE: the schema read works at all. Zero capped tools would make the
	// comparison agree with a sentence that names nobody, and it is exactly what a
	// broken schema decode produces.
	if len(capped) == 0 {
		t.Fatal("premise failed: no listed tool declares any capacity parameter, so the schema read " +
			"is measuring nothing")
	}

	if strings.Join(capped, ",") != strings.Join(named, ",") {
		t.Errorf("the limit sentence and the schemas disagree.\n"+
			"tools whose schema declares a capacity parameter: %v\n"+
			"tools the sentence names:                         %v\n"+
			"sentence: %q", capped, named, sentence)
	}
}

// TestInstructionsEventLogPeriodParametersExist pins the remedy paragraph 5 hands
// the model.
//
// WHY THE REMEDY IS THERE AT ALL. The sentence it replaced told the model to
// compare the number of shown records against «Всего» and conclude from the two
// whether anything had been dropped. That inference is invalid:
// ЖурналРегистрацииPOST in the bundled extension inserts a default ДатаНачала
// BEFORE ВыгрузитьЖурналРегистрации and counts ВсегоЗаписей after it, so «Всего»
// counts a window rather than the log. Asked «were there errors last month», a
// model applying the check the server told it to apply sees 3 of 3 and reports
// three errors about a period it never read. The comparison is gone; naming the
// two parameters is what is left, and it is only a remedy while they exist.
//
// NOTHING HERE TYPES THE PARAMETER NAMES. They are lifted out of the sentence,
// because the failure this guards against is the text naming a property the
// schema does not declare, and a test that spelled them itself would agree with
// the schema while the text disagreed with both.
func TestInstructionsEventLogPeriodParametersExist(t *testing.T) {
	const anchor = "задавай его параметрами "
	start := strings.Index(instructions.Text, anchor)
	if start < 0 {
		t.Fatalf("the clause beginning %q is gone from the text; if the remedy was rewritten, re-aim "+
			"this guard at the new wording rather than deleting it", anchor)
	}

	// The paragraph the clause lives in, so the tool it belongs to is read rather
	// than assumed.
	paraStart := strings.LastIndex(instructions.Text[:start], "\n\n")
	if paraStart < 0 {
		paraStart = 0
	}
	paraEnd := strings.Index(instructions.Text[start:], "\n\n")
	if paraEnd < 0 {
		paraEnd = len(instructions.Text) - start
	}
	paragraph := instructions.Text[paraStart : start+paraEnd]

	clause := instructions.Text[start+len(anchor):]
	if end := strings.Index(clause, "."); end >= 0 {
		clause = clause[:end]
	}
	params := regexp.MustCompile(`[a-z][a-z0-9_]*`).FindAllString(clause, -1)

	// PREMISE: the clause really named parameters. An empty list agrees with any
	// schema at all.
	if len(params) == 0 {
		t.Fatalf("no parameter name was lifted out of %q", clause)
	}

	session, _ := instructionsSession(t)
	listed := listToolsForInstructions(t, session)

	var owner *mcp.Tool
	for _, tool := range listed {
		if strings.Contains(paragraph, tool.Name) {
			if owner != nil {
				t.Fatalf("the paragraph names both %q and %q, so which schema the parameters belong to "+
					"is ambiguous", owner.Name, tool.Name)
			}
			owner = tool
		}
	}
	if owner == nil {
		t.Fatalf("the paragraph carrying %q names no live tool, so there is no schema to check the "+
			"parameters against:\n%s", anchor, paragraph)
	}

	props := schemaProperties(t, owner)
	for _, p := range params {
		if _, ok := props[p]; !ok {
			t.Errorf("the text tells the model to pass %q to %s, and that schema declares no such "+
				"property; nothing rejects a call before the handler, so the argument would be dropped "+
				"in silence", p, owner.Name)
		}
		// Control: the lookup is a real key read, so a hit above is the schema and
		// not the map answering yes to everything.
		if _, ok := props[p+"x"]; ok {
			t.Fatalf("control failed: %s's schema claims to declare %q", owner.Name, p+"x")
		}
	}
}

// TestInstructionsDumpParagraphMatchesTheRegistry pins the last paragraph: the
// pair it tells the model to look for is exactly the pair that appears with a dump
// index and disappears without one.
func TestInstructionsDumpParagraphMatchesTheRegistry(t *testing.T) {
	const anchor = "Если в списке инструментов нет "
	start := strings.Index(instructions.Text, anchor)
	if start < 0 {
		t.Fatalf("the sentence beginning %q is gone from the text", anchor)
	}
	rest := instructions.Text[start:]
	end := strings.Index(rest, ".")
	if end < 0 {
		t.Fatalf("the dump sentence has no terminator: %q", rest)
	}
	sentence := rest[:end]

	withDump, _ := instructionsSession(t)

	mock := failingQueryServer(t, http.StatusBadRequest, `{"error":"нет"}`)
	t.Cleanup(mock.Close)
	noDumpSession, _, cleanup := connectLoggedSession(t, New("guard", onec.NewClient(mock.URL, "", ""), nil), func() {})
	t.Cleanup(cleanup)

	withNames := map[string]bool{}
	for _, tool := range listToolsForInstructions(t, withDump) {
		withNames[tool.Name] = true
	}
	withoutNames := map[string]bool{}
	for _, tool := range listToolsForInstructions(t, noDumpSession) {
		withoutNames[tool.Name] = true
	}

	// The set that appears only with a dump index, derived from the two listings.
	var dumpOnly []string
	for name := range withNames {
		if !withoutNames[name] {
			dumpOnly = append(dumpOnly, name)
		}
	}
	sort.Strings(dumpOnly)
	if len(dumpOnly) == 0 {
		t.Fatal("premise failed: the two listings are identical, so the sentence's premise " +
			"(a pair that appears only with a dump) is not what the server does")
	}

	var namedInSentence []string
	for name := range withNames {
		if strings.Contains(sentence, name) {
			namedInSentence = append(namedInSentence, name)
		}
	}
	sort.Strings(namedInSentence)

	if strings.Join(dumpOnly, ",") != strings.Join(namedInSentence, ",") {
		t.Errorf("the dump paragraph names %v, but the tools that appear only with a dump index are %v\n"+
			"sentence: %q", namedInSentence, dumpOnly, sentence)
	}
	if !strings.Contains(sentence, "--dump") {
		t.Errorf("the dump paragraph no longer names the flag it blames: %q", sentence)
	}
}

// schemaProperties reads a listed tool's declared properties.
//
// The listing arrives from the CLIENT side, where mcp.Tool.InputSchema holds the
// default JSON unmarshalling of whatever the server declared (a map[string]any),
// not the json.RawMessage the server wrote. Round-tripping through JSON is what
// makes this read work for both shapes rather than for the one the server happens
// to use today.
func schemaProperties(t *testing.T, tool *mcp.Tool) map[string]json.RawMessage {
	t.Helper()
	if tool.InputSchema == nil {
		t.Fatalf("tool %q has no input schema", tool.Name)
	}
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshalling the schema of %q: %v", tool.Name, err)
	}
	var shape struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("decoding the schema of %q: %v", tool.Name, err)
	}
	return shape.Properties
}

func truncateForMessage(s string) string {
	const limit = 240
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// ---------------------------------------------------------------------------
// The wiring.
// ---------------------------------------------------------------------------

// newServerSite is one mcp.NewServer call found in non-test source.
type newServerSite struct {
	where           string
	hasInstructions bool
}

// instructionsWiring walks a tree of Go source and answers two questions with one
// parse: which non-test files read instructions.Text, and does every production
// construction of an MCP server pass it.
//
// It takes root as a parameter so the controls below can aim it at a fixture and
// show it reporting what is there rather than agreeing with everything.
func instructionsWiring(root string, includeTests bool) (refs []string, servers []newServerSite, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if !includeTests && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parsing %s: %w", path, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if ident, ok := node.X.(*ast.Ident); ok && ident.Name == "instructions" && node.Sel.Name == "Text" {
					refs = append(refs, fset.Position(node.Pos()).String())
				}
			case *ast.CallExpr:
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "NewServer" {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "mcp" {
					return true
				}
				site := newServerSite{where: fset.Position(node.Pos()).String()}
				if len(node.Args) >= 2 {
					site.hasInstructions = compositeCarriesInstructions(node.Args[1])
				}
				servers = append(servers, site)
			}
			return true
		})
		return nil
	})
	sort.Strings(refs)
	sort.Slice(servers, func(i, j int) bool { return servers[i].where < servers[j].where })
	return refs, servers, err
}

// compositeCarriesInstructions reports whether an mcp.NewServer options argument
// is a composite literal with an Instructions key.
func compositeCarriesInstructions(arg ast.Expr) bool {
	if unary, ok := arg.(*ast.UnaryExpr); ok {
		arg = unary.X
	}
	lit, ok := arg.(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Instructions" {
			return true
		}
	}
	return false
}

// TestInstructionsAreWiredExactlyOnce is two failures in one walk.
//
// ONE READER. Conditioning any behaviour on the string makes two clients running
// the same binary get different answers, invisibly, because MCP instructions are
// advisory and a client MAY drop them: the SDK's own doc comment on
// InitializeResult.Instructions calls the field a hint that MAY be added to the
// system prompt, so a handler branching on it branches on the client.
//
// ONE CONSTRUCTOR, AND IT PASSES THE FIELD. A second entry point calling
// mcp.NewServer without ServerOptions re-enters the pre-commit wire shape on one
// path while the other stays correct and hides it.
func TestInstructionsAreWiredExactlyOnce(t *testing.T) {
	const root = ".."

	refs, servers, err := instructionsWiring(root, false)
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}

	// PREMISE plus control: the walk reads Go at all. The same walk including tests
	// must find strictly more references, or it is finding nothing and saying so.
	withTests, _, err := instructionsWiring(root, true)
	if err != nil {
		t.Fatalf("control walk: %v", err)
	}
	if len(withTests) <= len(refs) {
		t.Fatalf("control failed: the walk found %d references to instructions.Text in non-test source "+
			"and %d including tests; the guards themselves read the constant, so this walk is not "+
			"reading Go", len(refs), len(withTests))
	}

	if len(refs) != 1 {
		t.Errorf("instructions.Text has %d non-test references, want exactly one (the wiring in "+
			"server.New): %v\nA handler that reads the text conditions the server's behaviour on a "+
			"value the client is free to ignore.", len(refs), refs)
	}

	if len(servers) == 0 {
		t.Fatal("premise failed: the walk found no mcp.NewServer call in non-test source, so the " +
			"assertion below is vacuous")
	}
	for _, s := range servers {
		if !s.hasInstructions {
			t.Errorf("%s constructs an MCP server without an Instructions field; that server answers "+
				"initialize with no instructions at all", s.where)
		}
	}

	// Control: the checker really can report a bare constructor. Planted rather than
	// asserted, because "every site passes the field" is exactly what a walk that
	// found nothing also reports.
	fixture := t.TempDir()
	planted := filepath.Join(fixture, "planted.go")
	if err := os.WriteFile(planted, []byte("package planted\n\nfunc F() { mcp.NewServer(impl, nil) }\n"), 0o600); err != nil {
		t.Fatalf("planting the control: %v", err)
	}
	_, plantedServers, err := instructionsWiring(fixture, false)
	if err != nil {
		t.Fatalf("control walk over the fixture: %v", err)
	}
	if len(plantedServers) != 1 || plantedServers[0].hasInstructions {
		t.Fatalf("control failed: the walk reported %v for a planted mcp.NewServer(impl, nil)", plantedServers)
	}
}
