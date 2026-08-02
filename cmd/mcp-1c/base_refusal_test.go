package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// base_refusal_test.go pins WHERE the refusal lives.
//
// The credential split itself is a property of package onec and is tested there.
// What this file has to hold is the boundary decision: an address from which a
// credential cannot be separated is refused at the FLAG, not inside NewClient.
// NewClient must stay silent, because the paid edition constructs clients over
// internal schemes (proxy://<infobase>, poll://local) that no split can validate
// and no user typed, and a refusal there would take those call sites out.

// TestBaseRefusalRunsWhereTheBaseIsResolved is structural rather than behavioural
// on purpose. The refusal ends in os.Exit, so it cannot be called from a test;
// what CAN be asserted, and what actually matters, is that the call exists, that
// it is handed the resolved configuration value rather than the raw flag, and
// that it sits between the resolution and the construction of the client.
//
// It asserts positions relative to other nodes, never line numbers, so inserting
// code above it cannot break it.
func TestBaseRefusalRunsWhereTheBaseIsResolved(t *testing.T) {
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

	// selectorCall reports the positions of every call to pkg.Name inside main.
	selectorCall := func(pkg, name string) []*ast.CallExpr {
		var out []*ast.CallExpr
		ast.Inspect(mainFn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != name {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
				out = append(out, call)
			}
			return true
		})
		return out
	}

	// PREMISE, checked before any verdict: the two anchors this test measures
	// against are really there. Without this, a rename of NewClient would make the
	// ordering assertion pass by having nothing to compare with.
	newClient := selectorCall("onec", "NewClient")
	if len(newClient) != 1 {
		t.Fatalf("premise broken: main calls onec.NewClient %d times, want exactly 1", len(newClient))
	}
	var lastBaseAssign token.Pos
	ast.Inspect(mainFn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "BaseURL" {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "cfg" && assign.Pos() > lastBaseAssign {
				lastBaseAssign = assign.Pos()
			}
		}
		return true
	})
	if lastBaseAssign == token.NoPos {
		t.Fatal("premise broken: main never assigns cfg.BaseURL, so there is no resolution point to sit after")
	}

	guards := selectorCall("onec", "CheckURLCredentialResidue")
	if len(guards) != 1 {
		t.Fatalf("main calls onec.CheckURLCredentialResidue %d times, want exactly 1: the flag boundary "+
			"is the only place the refusal belongs", len(guards))
	}
	guard := guards[0]

	if len(guard.Args) != 1 {
		t.Fatalf("the guard takes %d arguments, want 1", len(guard.Args))
	}
	arg, ok := guard.Args[0].(*ast.SelectorExpr)
	if !ok || arg.Sel.Name != "BaseURL" {
		t.Fatalf("the guard is not handed cfg.BaseURL: %T", guard.Args[0])
	}
	if id, ok := arg.X.(*ast.Ident); !ok || id.Name != "cfg" {
		t.Fatalf("the guard is handed something other than cfg.BaseURL; checking the raw flag would " +
			"miss an address that came from the environment")
	}
	if guard.Pos() < lastBaseAssign {
		t.Error("the guard runs BEFORE cfg.BaseURL is resolved from the flags, so it checks a value " +
			"the client will not use")
	}
	if guard.Pos() > newClient[0].Pos() {
		t.Error("the guard runs AFTER onec.NewClient, so the client is built from the refused address")
	}
}

// TestBaseWithUnstrippableCredentialIsRefused is the behavioural half: the guard
// the boundary calls really refuses the shape it exists for, and says nothing
// about the value while doing it.
func TestBaseWithUnstrippableCredentialIsRefused(t *testing.T) {
	const bad = `http://Админ:Пароль123@1c.corp.local/hs/mcp-1c`
	err := onec.CheckURLCredentialResidue(bad)
	if err == nil {
		t.Fatalf("the guard accepted %q, whose credential net/url cannot separate", bad)
	}
	// The message goes to stderr and into whatever log a user pastes into a public
	// issue, so no part of the value may appear in it.
	for _, fragment := range []string{"Пароль123", "Админ", "1c.corp.local", bad} {
		if strings.Contains(err.Error(), fragment) {
			t.Errorf("the refusal names %q from the rejected value:\n%v", fragment, err)
		}
	}

	// The positive control: a clean address is not refused, so the guard is not
	// simply rejecting everything.
	for _, good := range []string{
		`http://1c.corp.local/hs/mcp-1c`,
		`http://admin:sup3rsecret@1c.corp.local/hs/mcp-1c`,
	} {
		if err := onec.CheckURLCredentialResidue(good); err != nil {
			t.Errorf("the guard refused the clean address %q: %v", good, err)
		}
	}
}

// TestNewClientNeverRefuses is what keeps the paid edition alive across a
// re-vendor. onec.NewClient is called there with proxy://<infobase> and
// poll://local, neither of which is an address a user typed, and it is also called
// with an empty base by anything that has not configured 1C yet.
//
// It also states the deliberate residual: an unstrippable address does not blow up
// in NewClient. It is refused at the flag, and a caller that bypasses the flag gets
// a client whose every call fails rather than a panic or a silent leak.
func TestNewClientNeverRefuses(t *testing.T) {
	for _, base := range []string{"proxy://SomeBase", "poll://local", ""} {
		c := NewClientNoPanic(t, base)
		if c.BaseURL != base {
			t.Errorf("NewClient(%q) stored BaseURL %q; an internal scheme must survive byte for byte",
				base, c.BaseURL)
		}
	}
	// An unstrippable address: no panic, and nothing of the value is kept.
	const bad = `http://admin:p@ss/w0rd@host/hs`
	c := NewClientNoPanic(t, bad)
	if strings.Contains(c.BaseURL, "w0rd") || strings.Contains(c.BaseURL, "admin") {
		t.Errorf("NewClient kept part of the refused address in BaseURL: %q", c.BaseURL)
	}
}

// NewClientNoPanic builds a client and turns a panic into a test failure, so
// "no panic" is asserted rather than assumed.
func NewClientNoPanic(t *testing.T, base string) *onec.Client {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("onec.NewClient(%q) panicked: %v", base, r)
		}
	}()
	return onec.NewClient(base, "", "")
}
