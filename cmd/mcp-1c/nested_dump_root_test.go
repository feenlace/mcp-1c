package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// nested_dump_root_test.go covers the delivery half of the nested-root check. The
// detection itself is measured in dump/dumproot_test.go; what is asserted here is
// that the answer REACHES the person who typed the flag, and that it does so at a
// level this binary actually prints.

// customerShapedTree builds the tree the customer had: a parent holding a proper
// dump root and a second root beside it.
func customerShapedTree(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	for _, d := range []string{"main/Catalogs", "main/Documents", "main/Ext", "ext/Catalogs", "ext/CommonModules"} {
		if err := os.MkdirAll(filepath.Join(parent, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(parent, "main", "ConfigDumpInfo.xml"), []byte("<x/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return parent
}

// captureAtErrorLevel installs a default slog handler that keeps NOTHING below
// Error, mirroring what main installs, and returns the buffer it writes to.
func captureAtErrorLevel(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var out bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelError})))
	return &out
}

// TestNestedDumpRootNoticeIsDeliveredAtErrorLevel is the level pin.
//
// The handler below discards everything under Error. A message published at Warn
// would therefore produce an EMPTY buffer here while still passing any test that
// merely called the formatter, which is precisely how a warning ships that
// "exists, has a passing test, and never appears in a normal run".
func TestNestedDumpRootNoticeIsDeliveredAtErrorLevel(t *testing.T) {
	out := captureAtErrorLevel(t)
	parent := customerShapedTree(t)

	reportNestedDumpRoots(parent)

	got := out.String()
	if got == "" {
		t.Fatal("nothing was published through a handler that keeps only Error and above. " +
			"The message is being sent below Error, so it cannot appear in a normal run of " +
			"this binary, whose default handler is installed at LevelError.")
	}
	if !strings.Contains(got, "level=ERROR") {
		t.Errorf("published record is not at ERROR:\n%s", got)
	}
	for _, want := range []string{"main", "ext", "--dump"} {
		if !strings.Contains(got, want) {
			t.Errorf("the message does not mention %q, so the operator is not told what to "+
				"change:\n%s", want, got)
		}
	}

	// Positive control for the buffer itself: the handler really does drop a Warn,
	// so the non-empty buffer above is evidence about the level and not about the
	// handler being permissive.
	out.Reset()
	slog.Warn("control")
	if out.Len() != 0 {
		t.Fatalf("positive control failed: the handler published a Warn (%q), so passing the "+
			"assertion above proves nothing about the level", out.String())
	}
}

// TestNestedDumpRootNoticeIsSilentOnAProperRoot pins the other half: the shipped
// behaviour is silence, and the check must not become a new line every operator
// with a correct path has to learn to ignore.
func TestNestedDumpRootNoticeIsSilentOnAProperRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dump")
	for _, d := range []string{"Catalogs", "Documents", "Ext"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out := captureAtErrorLevel(t)
	reportNestedDumpRoots(root)
	if out.Len() != 0 {
		t.Errorf("a correctly pointed --dump produced output:\n%s", out.String())
	}

	// And a path with nothing dump-like anywhere is equally silent: the check
	// reports what it found, never that it looked.
	plain := t.TempDir()
	if err := os.MkdirAll(filepath.Join(plain, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	reportNestedDumpRoots(plain)
	if out.Len() != 0 {
		t.Errorf("an ordinary directory produced output:\n%s", out.String())
	}

	// Positive control: the same capture DOES carry the message for the shape it is
	// meant for, so the two silences above are the check answering "nothing" rather
	// than the capture being broken.
	reportNestedDumpRoots(customerShapedTree(t))
	if out.Len() == 0 {
		t.Fatal("positive control failed: the customer's shape published nothing either, " +
			"so the silences above say nothing about the check")
	}
}

// TestNestedDumpRootMessageWording pins the two things the sentence must do and
// the one it must not.
func TestNestedDumpRootMessageWording(t *testing.T) {
	one := nestedDumpRootMessage(dump.DumpRootInspection{NestedRoots: []string{"main"}})
	many := nestedDumpRootMessage(dump.DumpRootInspection{NestedRoots: []string{"ext", "main"}})
	if one == "" || many == "" {
		t.Fatal("the message is empty for an inspection that found roots")
	}
	if !strings.Contains(many, "ext, main") {
		t.Errorf("the multi-root message does not list the roots it found: %q", many)
	}
	// It must not tell the operator the server will handle it.
	for _, forbidden := range []string{"автоматически", "сам перейдёт", "будет использован"} {
		if strings.Contains(one, forbidden) || strings.Contains(many, forbidden) {
			t.Errorf("the message promises an action the server does not take (%q)", forbidden)
		}
	}
	// Customer-facing RU carries no тире. The flag name is not prose and keeps its
	// hyphens, so only the dash characters are checked.
	for _, s := range []string{one, many} {
		for _, dash := range []string{"—", "–", "―"} {
			if strings.Contains(s, dash) {
				t.Errorf("customer-facing RU contains %q: %s", dash, s)
			}
		}
	}
	// A root, and a path with nothing under it, both say nothing.
	if got := nestedDumpRootMessage(dump.DumpRootInspection{IsRoot: true, NestedRoots: []string{"main"}}); got != "" {
		t.Errorf("a path that IS a root produced %q", got)
	}
	if got := nestedDumpRootMessage(dump.DumpRootInspection{}); got != "" {
		t.Errorf("an inspection that found nothing produced %q", got)
	}
	// Truncation is carried into the sentence rather than dropped: "no root below
	// this" and "no root among the first few" are different answers.
	cut := nestedDumpRootMessage(dump.DumpRootInspection{NestedRoots: []string{"main"}, Truncated: true})
	if cut == one {
		t.Error("a truncated scan produces the same sentence as a complete one, so the " +
			"operator cannot tell a full answer from a partial one")
	}
}

// TestMainReportsNestedDumpRootsBeforeServing is structural, and it is the test
// that stops all of the above from being dead code.
//
// Every behavioural test here calls reportNestedDumpRoots directly. That proves
// the function works and proves nothing about whether the binary ever runs it, so
// what is asserted here is that main calls it, that it is handed the resolved
// --dump value, and that the call sits BEFORE the index is opened. Positions are
// compared against other nodes, never against line numbers.
func TestMainReportsNestedDumpRootsBeforeServing(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}
	var mainFn *ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "main" {
			mainFn = fn
		}
	}
	if mainFn == nil {
		t.Fatal("premise broken: main.go declares no func main")
	}

	callsTo := func(name string) []*ast.CallExpr {
		var out []*ast.CallExpr
		ast.Inspect(mainFn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if fn.Name == name {
					out = append(out, call)
				}
			case *ast.SelectorExpr:
				if fn.Sel.Name == name {
					out = append(out, call)
				}
			}
			return true
		})
		return out
	}

	report := callsTo("reportNestedDumpRoots")
	if len(report) != 1 {
		t.Fatalf("main calls reportNestedDumpRoots %d times, want exactly 1; every "+
			"behavioural test in this file would still pass with the call removed", len(report))
	}
	// PREMISE: the anchor it is ordered against really exists, or the ordering
	// assertion below would hold vacuously.
	open := callsTo("openServeIndexLocal")
	if len(open) != 1 {
		t.Fatalf("premise broken: main calls openServeIndexLocal %d times, want 1", len(open))
	}
	if report[0].Pos() > open[0].Pos() {
		t.Error("reportNestedDumpRoots runs AFTER the index is opened, so the operator is " +
			"told about the path only once the wrong one has been used")
	}

	// It must receive the resolved flag value, not something else that happens to
	// be a string.
	if len(report[0].Args) != 1 {
		t.Fatalf("reportNestedDumpRoots is called with %d arguments, want 1", len(report[0].Args))
	}
	star, ok := report[0].Args[0].(*ast.StarExpr)
	if !ok {
		t.Fatalf("reportNestedDumpRoots is not handed a dereferenced flag: %T", report[0].Args[0])
	}
	if id, ok := star.X.(*ast.Ident); !ok || id.Name != "dumpDir" {
		t.Errorf("reportNestedDumpRoots is handed %v, want *dumpDir", star.X)
	}
}
