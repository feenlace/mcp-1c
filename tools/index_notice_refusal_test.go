package tools

// THE PROPERTY: when an index-backed tool REFUSES, the first line of what the
// model reads is the refusal heading.
//
// It is not a style preference. The server hands the model one paragraph per
// session saying «Ответ, первая строка которого говорит, что запрошенное не
// выполнено, не получено или не прочитано, это отказ инструмента, а не пустой
// результат» (internal/instructions). Everything downstream of that sentence
// rests on the first line being the classifier. The two tools wrapped in
// withIndexProtectionNotice used to break it: the wrapper put its «> ВНИМАНИЕ»
// line in front of the heading whenever the cache was degraded, so a model that
// followed our own instruction read line one, failed to recognise a refusal, and
// reported an empty result for a call that had refused. The condition is rare and
// the failure is silent, which is the worst pair.
//
// WHY THIS IS A CENSUS AND NOT TWO NAMED TESTS. The defect is a property of a
// COMPOSITION, not of two functions: any constructor that wraps WithToolErrors in
// withIndexProtectionNotice has it. A hand-written list of the two tools that were
// wrapped when the defect was found is green on the day a third is added, which is
// precisely the day the property needs a guard. So the list is read out of the
// source and a wrapped tool this file cannot drive is a FAILURE rather than a
// silent omission.
//
// WHAT IT DOES NOT COVER, stated rather than left to be found: the wrapper's
// raw-error branch, where the handler was not wrapped in WithToolErrors at all.
// That branch produces a JSON-RPC error with no heading anywhere in it, so there
// is no first line to hold and the notice stays in front there;
// TestIndexNotice_AFailingCallIsDecoratedToo pins it.

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// noticedTool is one constructor whose handler is wrapped in
// withIndexProtectionNotice, with the heading the wrapped WithToolErrors renders
// its refusals under.
type noticedTool struct {
	ctor    string // the enclosing constructor, which is what a driver is keyed by
	heading string // the resolved heading value, never a constant name
	where   string // file:line, so a failure names the call and not a symbol
}

// noticedTools reads the wrapped-tool list FROM THE CALL SITES.
//
// It takes dir as a parameter for the same reason instrRefusalHeadings does: so
// the walk can be aimed somewhere with no Go source and shown to report nothing
// rather than agreement.
//
// A call site it cannot read is returned as unresolved rather than skipped. An
// unread site is an unchecked first line, and a census that reports agreement over
// the sites it happened to understand is the shape that stays green through the
// change it exists to catch.
func noticedTools(dir string) ([]noticedTool, []string, error) {
	consts, err := instrStringConsts(dir)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, nil, err
	}

	var found []noticedTool
	var unresolved []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fd, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					fn, ok := call.Fun.(*ast.Ident)
					if !ok || fn.Name != "withIndexProtectionNotice" {
						return true
					}
					where := fset.Position(call.Pos()).String()
					if len(call.Args) != 2 {
						unresolved = append(unresolved, fmt.Sprintf(
							"%s: the wrapper takes %d arguments here, and this census reads the two-argument form",
							where, len(call.Args)))
						return true
					}
					// The wrapped handler must be a WithToolErrors call, because that
					// is what gives a refusal a heading at all. A wrapped handler
					// without one has no first line to protect and is a defect in its
					// own right.
					inner, ok := call.Args[1].(*ast.CallExpr)
					if !ok {
						unresolved = append(unresolved, fmt.Sprintf(
							"%s: %s wraps a %T rather than a call, so this census cannot tell whether its refusals carry a heading",
							where, fd.Name.Name, call.Args[1]))
						return true
					}
					innerFn, ok := inner.Fun.(*ast.Ident)
					if !ok || innerFn.Name != "WithToolErrors" {
						unresolved = append(unresolved, fmt.Sprintf(
							"%s: %s wraps something other than WithToolErrors, so its refusals have no heading to keep first",
							where, fd.Name.Name))
						return true
					}
					if len(inner.Args) == 0 {
						unresolved = append(unresolved, fmt.Sprintf(
							"%s: %s passes no heading to WithToolErrors", where, fd.Name.Name))
						return true
					}
					heading, ok := noticedHeadingValue(inner.Args[0], consts)
					if !ok {
						unresolved = append(unresolved, fmt.Sprintf(
							"%s: %s passes a heading this census cannot resolve to a string", where, fd.Name.Name))
						return true
					}
					found = append(found, noticedTool{ctor: fd.Name.Name, heading: heading, where: where})
					return true
				})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ctor < found[j].ctor })
	return found, unresolved, nil
}

// noticedHeadingValue resolves a heading argument written either as a string
// literal or as a string constant of this package.
func noticedHeadingValue(arg ast.Expr, consts map[string]string) (string, bool) {
	switch a := arg.(type) {
	case *ast.BasicLit:
		if a.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(a.Value)
		if err != nil {
			return "", false
		}
		return v, true
	case *ast.Ident:
		v, ok := consts[a.Name]
		return v, ok
	default:
		return "", false
	}
}

// refuseNoticedTool builds one wrapped tool's real handler and calls it in a way
// that MUST come back as a refusal. Keyed by constructor name, so the census above
// decides which of these run and an unknown constructor has nowhere to hide.
//
// The real constructor, never a reconstruction of the composition: a guard that
// rebuilt withIndexProtectionNotice(WithToolErrors(...)) itself would keep passing
// after a constructor stopped using that shape, which is the one change it is here
// to notice.
var refuseNoticedTool = map[string]func(t *testing.T, index *dump.Index, dumpDir string) (*mcp.CallToolResult, error){
	// search_code refuses on a mode outside its enum, before the index is read, so
	// the refusal does not depend on what the dump holds.
	"NewSearchCodeHandler": func(t *testing.T, index *dump.Index, _ string) (*mcp.CallToolResult, error) {
		t.Helper()
		args, err := json.Marshal(map[string]any{"query": noticeTerm, "mode": "НетТакогоРежима"})
		if err != nil {
			t.Fatalf("building the search arguments: %v", err)
		}
		return NewSearchCodeHandler(index)(context.Background(), &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{Name: "search_code", Arguments: args},
		})
	},
	// reload_dump takes no arguments, so there is no bad value to send it and the
	// only way to make it fail is to take the dump away. dump.Index.Reload walks the
	// dump directory for a signature before it touches the cache, so a directory
	// that is gone fails there; on a frozen cache it would fail at the cache step
	// instead, and both are the operational refusal this drives for.
	"NewReloadDumpHandler": func(t *testing.T, index *dump.Index, dumpDir string) (*mcp.CallToolResult, error) {
		t.Helper()
		if err := os.RemoveAll(dumpDir); err != nil {
			t.Fatalf("removing the dump directory to force a failed reload: %v", err)
		}
		if _, err := os.Stat(dumpDir); !os.IsNotExist(err) {
			t.Fatalf("control failed: %s still exists after RemoveAll (stat error %v), so the reload has no reason to fail", dumpDir, err)
		}
		return NewReloadDumpHandler(index)(context.Background(), &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{Name: "reload_dump", Arguments: json.RawMessage(`{}`)},
		})
	},
}

// TestNoticedToolRefusalOpensWithItsHeading is the guard on the property.
func TestNoticedToolRefusalOpensWithItsHeading(t *testing.T) {
	sites, unresolved, err := noticedTools(".")
	if err != nil {
		t.Fatalf("parsing the package source: %v", err)
	}
	for _, u := range unresolved {
		t.Errorf("the wrapped-tool census cannot resolve a call site, so its first line is unchecked: %s", u)
	}

	// PREMISE: the walk found the wrapped tools. Zero would agree with everything.
	if len(sites) < 2 {
		t.Fatalf("the census found %d tools wrapped in withIndexProtectionNotice; the package had two "+
			"when this guard was written, so the walk is measuring the wrong thing", len(sites))
	}

	// Control: aimed at a directory with no Go source, the same walk reports nothing
	// rather than the two it found above.
	if empty, _, err := noticedTools(t.TempDir()); err != nil {
		t.Fatalf("control failed: the census errored on an empty directory: %v", err)
	} else if len(empty) != 0 {
		t.Fatalf("control failed: the census found %d wrapped tools in an empty directory", len(empty))
	}

	driven := map[string]bool{}
	for _, s := range sites {
		refuse, ok := refuseNoticedTool[s.ctor]
		if !ok {
			t.Errorf("%s: %s wraps its handler in the index notice, but this guard has no way to make it "+
				"refuse, so the property is unchecked for it. Add a driver to refuseNoticedTool.", s.where, s.ctor)
			continue
		}
		driven[s.ctor] = true

		t.Run(s.ctor+"/degraded_index", func(t *testing.T) {
			index, dumpDir := noticeIndexAndDump(t, true)
			res, err := refuse(t, index, dumpDir)
			if err != nil {
				t.Fatalf("the call came back as a protocol error, so the model never reads a refusal text "+
					"and there is no first line to hold: %v", err)
			}
			if res == nil {
				t.Fatal("neither a result nor an error came back")
			}
			if !res.IsError {
				t.Fatalf("the driver no longer produces a refusal, so nothing below tests the ordering:\n%s",
					resultText(t, res))
			}
			text := resultText(t, res)

			// THE NOTICE IS STILL THERE. Moving it below the heading must not become
			// dropping it: an unprotected index that says nothing is the defect the
			// wrapper was written for.
			if !strings.Contains(text, noticeMarker) {
				t.Errorf("the refusal carries no index-protection notice, so the fix for the ordering lost "+
					"the warning entirely:\n%s", text)
			}

			// THE PROPERTY.
			first, _, _ := strings.Cut(text, "\n")
			if want := "## " + s.heading; first != want {
				t.Errorf("the first line of the refusal is %q, want %q. A model told that a refusal is "+
					"recognised by its first line will read this one as an empty result.", first, want)
			}
		})

		// NEGATIVE CONTROL. On a healthy index no notice is emitted at all, so the
		// assertion above cannot be passing because the notice happens to be absent
		// everywhere: this case shows the difference between the two indexes is
		// exactly the notice, and the heading leads either way.
		t.Run(s.ctor+"/healthy_index_control", func(t *testing.T) {
			index, dumpDir := noticeIndexAndDump(t, false)
			res, err := refuse(t, index, dumpDir)
			if err != nil {
				t.Fatalf("control failed: the call came back as a protocol error: %v", err)
			}
			if res == nil || !res.IsError {
				t.Fatal("control failed: the driver did not produce a refusal on a healthy index")
			}
			text := resultText(t, res)
			if strings.Contains(text, noticeMarker) {
				t.Errorf("control failed: a healthy index emitted the protection notice, so the degraded "+
					"case proves nothing about ordering:\n%s", text)
			}
			if want := "## " + s.heading; !strings.HasPrefix(text, want) {
				t.Errorf("control failed: an undecorated refusal does not open with %q:\n%s", want, text)
			}
		})
	}

	// A driver for a constructor the census no longer finds is a guard aimed at
	// nothing, and it would keep this test green while covering less than it claims.
	for ctor := range refuseNoticedTool {
		if !driven[ctor] {
			t.Errorf("refuseNoticedTool has a driver for %q, which the census does not find wrapped in "+
				"withIndexProtectionNotice; the driver is stale", ctor)
		}
	}
}

// TestNoticedBodyKeepsTheNoticeOnEveryShape pins the splice itself, at the unit,
// including the shapes the two live tools do not produce.
func TestNoticedBodyKeepsTheNoticeOnEveryShape(t *testing.T) {
	const notice = "> ВНИМАНИЕ: заметка.\n"

	t.Run("success keeps the notice first", func(t *testing.T) {
		got := noticedBody("Найдено: 3\n", notice, false)
		if !strings.HasPrefix(got, notice) {
			t.Errorf("a successful answer no longer opens with the notice:\n%s", got)
		}
		if !strings.Contains(got, "Найдено: 3") {
			t.Errorf("the body was lost:\n%s", got)
		}
	})

	t.Run("refusal keeps the heading first and the notice under it", func(t *testing.T) {
		got := noticedBody("## Поиск не выполнен\n\n❌ причина\n", notice, true)
		first, _, _ := strings.Cut(got, "\n")
		if first != "## Поиск не выполнен" {
			t.Errorf("the first line is %q, want the heading", first)
		}
		if !strings.Contains(got, notice) {
			t.Errorf("the notice was lost:\n%s", got)
		}
		if strings.Index(got, notice) > strings.Index(got, "❌ причина") {
			t.Errorf("the notice sank below the body instead of sitting under the heading:\n%s", got)
		}
	})

	// A one-line refusal has nowhere to splice into. It must still lead with the
	// heading and still carry the notice; dropping it here would be losing the
	// warning on the shape nobody looks at.
	t.Run("single line refusal", func(t *testing.T) {
		got := noticedBody("## Поиск не выполнен", notice, true)
		first, _, _ := strings.Cut(got, "\n")
		if first != "## Поиск не выполнен" {
			t.Errorf("the first line is %q, want the heading", first)
		}
		if !strings.Contains(got, notice) {
			t.Errorf("a one-line refusal lost the notice:\n%s", got)
		}
	})
}
