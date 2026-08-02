package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// A request this server cannot honour is refused, never answered.
//
// Two ways to fail to honour one, and both were live:
//
//   - an argument that does not DECODE. get_metadata_tree discarded the decode
//     error, so {"filter":123} produced the unfiltered summary, byte identical
//     to a call carrying no filter. Measured before the fix, with a valid
//     filter as the control that the answer does change when the filter is
//     honoured.
//   - an argument whose VALUE is outside a constraint the tool's own schema
//     declares. get_event_log declares an enum for level and checked nothing,
//     and ЖурналРегистрацииPOST in the extension drops an unmapped level
//     silently, so the whole log came back as though it had been filtered.
//
// The rule is one rule for both, and these tests are what keeps it one rule
// rather than ten tools' taste.
// ---------------------------------------------------------------------------

// enumDriver drives one tool with one value outside one declared enum.
//
// It exists because the enums cannot be checked by reading source alone: the
// question is not "is there an if statement" but "does a value outside the set
// come back refused", and only running the handler answers that.
type enumDriver struct {
	tool     string
	property string
	// outside is a value the enum does not contain. It is spelled here rather
	// than derived, because "any string not in the list" is exactly the input a
	// caller supplies by mistake and the assertion must use a real one.
	outside string
	// inside is a member of the enum. It is the CONTROL: without it a test that
	// refused everything would pass, and refusing everything is a worse defect
	// than the one under repair.
	inside string
	call   func(t *testing.T, args string) (text string, isErr bool, err error)
}

func enumDrivers() []enumDriver {
	return []enumDriver{
		{
			tool: "get_event_log", property: "level",
			outside: "Критическая", inside: "Ошибка",
			call: func(t *testing.T, args string) (string, bool, error) {
				t.Helper()
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"events":[{"date":"2026-01-01","level":"Информация",` +
						`"event":"X","user":"U"}],"total":1}`))
				}))
				defer srv.Close()
				return callTool(t, NewEventLogHandler(onec.NewClient(srv.URL, "", "")), args)
			},
		},
		{
			tool: "search_code", property: "mode",
			outside: "fuzzy", inside: "exact",
			call: func(t *testing.T, args string) (string, bool, error) {
				t.Helper()
				return callTool(t, NewSearchCodeHandler(oneModuleIndex(t)), args)
			},
		},
	}
}

// TestDeclaredEnumsAreEnforced reads the enums out of the SHIPPED schemas and
// requires a driver for each one.
//
// Reading the schemas is the load-bearing half. A hand-written list of the two
// enums that exist today would pass unchanged on the day someone adds a third
// enum to a new tool and forgets to check it, which is precisely the defect
// being repaired here rather than a hypothetical one. Measured: at 6d96384
// (v1.12.1) tools/eventlog.go already declared the level enum, and
//
//	/usr/bin/git show 6d96384:tools/eventlog.go |
//		/usr/bin/grep -c 'eventLogLevels\|slices.Contains'
//
// returns 0 there against 5 at this commit, so the enum was declared and
// enforced by nothing. The count at this commit is the positive control: a
// command that returns 0 on both sides has measured nothing.
func TestDeclaredEnumsAreEnforced(t *testing.T) {
	declared := declaredEnums(t)
	if len(declared) == 0 {
		t.Fatal("no enum was found in any tool schema; the walk is looking in the wrong place, " +
			"and a walk that finds nothing agrees with any driver table at all")
	}

	drivers := map[string]enumDriver{}
	for _, d := range enumDrivers() {
		drivers[d.tool+"."+d.property] = d
	}

	for key, values := range declared {
		d, ok := drivers[key]
		if !ok {
			t.Errorf("%s declares an enum %v and no driver refuses a value outside it; "+
				"a raw-registered schema enforces nothing by itself, so an unchecked enum "+
				"is a promise this server does not keep", key, values)
			continue
		}
		if len(values) == 0 {
			t.Errorf("%s declares an empty enum", key)
			continue
		}
		if slicesContains(values, d.outside) {
			t.Errorf("%s: the driver's %q is IN the declared enum %v, so the test cannot fail",
				key, d.outside, values)
			continue
		}
		if !slicesContains(values, d.inside) {
			t.Errorf("%s: the driver's control %q is NOT in the declared enum %v, so a handler "+
				"that refuses everything would still pass", key, d.inside, values)
			continue
		}

		t.Run(key, func(t *testing.T) {
			outArgs := mustArgs(t, d.property, d.outside)
			_, isErr, err := d.call(t, outArgs)
			if !isErr && err == nil {
				t.Errorf("%s with %s=%q was ANSWERED, not refused; the value is outside the "+
					"enum this tool's own schema declares", d.tool, d.property, d.outside)
			}

			// The control. A refusal is only evidence once the same driver is
			// seen accepting a member of the same enum.
			inArgs := mustArgs(t, d.property, d.inside)
			_, ctlIsErr, ctlErr := d.call(t, inArgs)
			if ctlIsErr || ctlErr != nil {
				t.Errorf("%s with %s=%q (a declared member) was refused too: isError=%v err=%v; "+
					"a check that refuses every value proves nothing about the one it was written for",
					d.tool, d.property, d.inside, ctlIsErr, ctlErr)
			}
		})
	}

	for key := range drivers {
		if _, ok := declared[key]; !ok {
			t.Errorf("driver for %s has no declared enum in any shipped schema; "+
				"it is testing a constraint the model is never told about", key)
		}
	}
}

// TestMetadataFilterThatDoesNotDecodeIsRefused is the behavioural half of the
// same rule, driven end to end rather than read off the AST.
//
// The measurement before the fix: {"filter":123} returned isError=false and text
// BYTE IDENTICAL to {}. Byte identity is the assertion, not a substring, because
// the defect is precisely that the two answers cannot be told apart.
func TestMetadataFilterThatDoesNotDecodeIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Справочники":["Контрагенты"],"Документы":["Реализация"]}`))
	}))
	defer srv.Close()
	h := NewMetadataHandler(onec.NewClient(srv.URL, "", ""))

	unfiltered, unfilteredIsErr, unfilteredErr := callTool(t, h, `{}`)
	if unfilteredIsErr || unfilteredErr != nil {
		t.Fatalf("a call with no arguments must still succeed: isError=%v err=%v", unfilteredIsErr, unfilteredErr)
	}

	bad, badIsErr, badErr := callTool(t, h, `{"filter":123}`)
	if !badIsErr && badErr == nil {
		t.Errorf("filter=123 was ANSWERED (isError=false, no protocol error) with %q", bad)
	}
	if bad == unfiltered {
		t.Errorf("filter=123 produced text byte identical to the unfiltered call, so a caller " +
			"cannot tell the answer to its question from the answer to a different one")
	}

	// CONTROL 1: a filter that DOES decode changes the answer. Without it, a
	// handler that refused every filter would satisfy the assertion above.
	good, goodIsErr, goodErr := callTool(t, h, `{"filter":"Справочники"}`)
	if goodIsErr || goodErr != nil {
		t.Fatalf("a valid filter must be honoured, not refused: isError=%v err=%v text=%q", goodIsErr, goodErr, good)
	}
	if good == unfiltered {
		t.Fatal("a valid filter did not change the answer, so this test could not have detected " +
			"an ignored filter in the first place")
	}

	// CONTROL 2: absent arguments are not an unhonoured request. The nil guard
	// in the handler is what keeps them apart, and deleting it would turn every
	// no-argument call into a refusal.
	res, err := h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("a call with no arguments at all must succeed: err=%v result=%+v", err, res)
	}
}

// TestNoToolDiscardsItsArgumentDecodeError walks the package and requires every
// decode of req.Params.Arguments to be error-checked.
//
// The AST walk answers the question a grep cannot: json.Unmarshal appears in
// this package for other reasons, and the shape that matters is "the returned
// error is bound to something", not "the word Unmarshal is on the line". The
// discarded call this repairs was spelled with a //nolint:errcheck comment, so a
// linter would not have found it either.
func TestNoToolDiscardsItsArgumentDecodeError(t *testing.T) {
	checked, discarded := argumentDecodeSites(t, ".")

	if len(checked)+len(discarded) == 0 {
		t.Fatal("the walk found no decode of req.Params.Arguments at all; it is looking at the " +
			"wrong tree, and an empty walk reports agreement with anything")
	}
	if len(discarded) > 0 {
		sort.Strings(discarded)
		t.Errorf("these decodes of req.Params.Arguments drop their error, so an argument that "+
			"does not decode is answered as if it had: %v", discarded)
	}
	t.Logf("argument decode sites: %d checked, %d discarded", len(checked), len(discarded))

	// The walk must be able to say "discarded". Without this, a walk that
	// classified everything as checked would pass and prove nothing.
	t.Run("the walk can report a discarded site", func(t *testing.T) {
		dir := t.TempDir()
		src := `package tools

import "encoding/json"

func NewPlantedHandler() {
	var input struct{}
	json.Unmarshal(req.Params.Arguments, &input)
}
`
		if err := os.WriteFile(filepath.Join(dir, "planted.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		ck, dc := argumentDecodeSites(t, dir)
		if len(dc) != 1 || len(ck) != 0 {
			t.Fatalf("a planted discarded decode was not reported as discarded: checked=%v discarded=%v", ck, dc)
		}
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// callTool runs a handler the way the SDK does and reports the three facts the
// contract is about: the text, IsError, and the protocol error.
func callTool(t *testing.T, h mcp.ToolHandler, args string) (string, bool, error) {
	t.Helper()
	res, err := h(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(args)},
	})
	if err != nil {
		return "", false, err
	}
	if res == nil {
		return "", false, nil
	}
	var text string
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	return text, res.IsError, nil
}

// declaredEnums returns "<tool>.<property>" -> enum values, read from the tool
// definitions this server actually registers.
func declaredEnums(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, tool := range shippedToolDefs() {
		raw, ok := tool.InputSchema.(json.RawMessage)
		if !ok {
			// bsl_syntax_help builds its schema from a Go type through the
			// generic mcp.AddTool, which resolves and validates it. Nothing
			// here is about that path.
			continue
		}
		var schema struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("tool %s: input schema does not parse: %v", tool.Name, err)
		}
		for prop, spec := range schema.Properties {
			if len(spec.Enum) > 0 {
				out[tool.Name+"."+prop] = spec.Enum
			}
		}
	}
	return out
}

// shippedToolDefs is every tool definition this package exports. It is spelled
// out rather than discovered so that a new tool has to be added here by hand;
// the tool registry contract test is what pins the list against server.New.
func shippedToolDefs() []*mcp.Tool {
	return []*mcp.Tool{
		MetadataTool(),
		ObjectStructureTool(),
		QueryTool(),
		SearchCodeTool(),
		ReloadDumpTool(),
		FormStructureTool(),
		ValidateQueryTool(),
		EventLogTool(),
		ConfigurationInfoTool(),
		AnalyzeSubsystemsTool(),
		BSLHelpTool(),
	}
}

// argumentDecodeSites walks dir and splits every json.Unmarshal whose first
// argument is req.Params.Arguments into checked and discarded.
//
// "Checked" means the call is the init of an if statement or its error result
// is assigned to a name. A bare ExpressionStatement is the discarded shape and
// is the only one this test refuses.
func argumentDecodeSites(t *testing.T, dir string) (checked, discarded []string) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isArgumentDecode(call) {
					return true
				}
				site := fmt.Sprintf("%s:%d", filepath.Base(name), fset.Position(call.Pos()).Line)
				if decodeErrorIsBound(file, call) {
					checked = append(checked, site)
				} else {
					discarded = append(discarded, site)
				}
				return true
			})
		}
	}
	return checked, discarded
}

// isArgumentDecode reports whether call is json.Unmarshal(req.Params.Arguments, …).
func isArgumentDecode(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Unmarshal" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "json" {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}
	return exprText(call.Args[0]) == "req.Params.Arguments"
}

// decodeErrorIsBound reports whether the call's error result goes anywhere: the
// init of an if, or the right-hand side of an assignment. A call standing alone
// as a statement drops it.
func decodeErrorIsBound(file *ast.File, call *ast.CallExpr) bool {
	bound := true
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		if stmt.X == ast.Expr(call) {
			bound = false
			return false
		}
		return true
	})
	return bound
}

func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	}
	return ""
}

func mustArgs(t *testing.T, property, value string) string {
	t.Helper()
	m := map[string]string{property: value}
	if property != "query" {
		// search_code requires query; supplying it keeps the mode check the
		// only thing under test.
		m["query"] = "Процедура"
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func slicesContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// oneModuleIndex builds a tiny real index so search_code's handler runs for
// real rather than against a stub.
func oneModuleIndex(t *testing.T) *dump.Index {
	t.Helper()
	dumpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dumpDir, "M.bsl"),
		[]byte("Процедура Пример() КонецПроцедуры"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := dump.NewIndex(dumpDir, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	// An index that is still building answers every search with a failure, so
	// the control would be refused for a reason that has nothing to do with the
	// mode it is there to accept.
	waitReady(t, idx, 30*time.Second)
	return idx
}
