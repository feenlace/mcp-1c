package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// exit_path_delivery_test.go covers the two exits that were still writing to
// os.Stderr AFTER the pipe-mode redirect has replaced it.
//
// startup_refusal_delivery_test.go fixed the credential refusal and explains the
// mechanism: from the redirect onward os.Stderr is the log file, or os.DevNull
// when no log could be opened, so fmt.Fprintf(os.Stderr, ...) followed by
// os.Exit(1) leaves the person who launched the server with an exit code and
// nothing to read. reportStartupFailure exists to deliver on both channels.
//
// The two survivors were the dump-open failure and the serve failure. Their
// reachability is NOT the same and the difference is recorded here, because a
// test that pretends to exercise an unreachable branch is worse than no test.

// runChildWithStdin launches the real main in a child process, feeding it stdin,
// and returns the two descriptors separately. Separately is load-bearing: a
// merged capture cannot see a fix that moved the message onto fd 1, where the
// JSON-RPC stream lives.
//
// It reuses TestStartupRefusalHelperProcess as the child entry point. That
// helper ends in an explicit os.Exit(0) when main returns, which is what keeps
// go test's own "PASS" off fd 1 and keeps the stdout assertions falsifiable.
func runChildWithStdin(t *testing.T, stdin []byte, args ...string) refusalRun {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartupRefusalHelperProcess$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		refusalHelperEnv+"=1",
		"MCP_1C_REFUSAL_ARGS="+strings.Join(args, "\n"),
		"MCP_1C_CACHE_DIR=",
		"MCP_1C_NO_TTY=",
	)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("launching the child: %v", err)
	}
	return refusalRun{exit: code, stdout: out.Bytes(), stderr: errb.Bytes()}
}

// malformedFrame is a byte sequence the JSON-RPC transport cannot decode. It is
// the input that makes s.Run return an error, which is how the serve-failure
// exit is reached. No network and no 1C involved.
var malformedFrame = []byte("this is not json\n")

// TestServeFailureReachesTheUserInPipeMode is the reachable half of the defect.
//
// A single undecodable byte on stdin ends the session with an error, and the
// process then reports it with fmt.Fprintf(os.Stderr, ...) sixty lines after
// os.Stderr stopped being file descriptor 2. Gate B could not trigger this and
// marked it NOT RUN; the input above triggers it every time.
func TestServeFailureReachesTheUserInPipeMode(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")

	run := runChildWithStdin(t, malformedFrame,
		"--base", "http://127.0.0.1:19899/hs/mcp-1c", "--cache-dir", cacheDir)

	t.Logf("EXIT=%d  stdout=%d bytes  stderr=%d bytes", run.exit, len(run.stdout), len(run.stderr))

	if run.exit != 1 {
		t.Fatalf("premise broken: a malformed frame exited %d, not 1, so this case is not "+
			"measuring the serve-failure exit.\nstderr: %s", run.exit, run.stderr)
	}

	if len(run.stderr) == 0 {
		logged, _ := os.ReadFile(filepath.Join(cacheDir, "stderr.log"))
		t.Errorf("the serve failure put 0 bytes on fd 2; the user sees exit 1 and nothing else.\n"+
			"the message went to %s instead (%d bytes): %s",
			filepath.Join(cacheDir, "stderr.log"), len(logged), logged)
	}

	// fd 1 carries the JSON-RPC stream. A fix that moved the message there would
	// be worse than the defect it repaired.
	if len(run.stdout) != 0 {
		t.Errorf("the serve-failure path wrote %d bytes to fd 1, where the JSON-RPC stream "+
			"lives: %q", len(run.stdout), run.stdout)
	}
}

// TestServeFailurePositiveControl proves the redirect is what silences fd 2 in
// the case above. Same binary, same input, one flag: --verbose keeps
// effectiveTTY true so the redirect never runs. If this case were silent too,
// the cause would be something else and the case above would be measuring the
// wrong thing.
func TestServeFailurePositiveControl(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	run := runChildWithStdin(t, malformedFrame,
		"--base", "http://127.0.0.1:19899/hs/mcp-1c", "--cache-dir", cacheDir, "--verbose")

	t.Logf("EXIT=%d  stdout=%d bytes  stderr=%d bytes", run.exit, len(run.stdout), len(run.stderr))
	if run.exit != 1 {
		t.Fatalf("control broken: --verbose changed the exit code to %d", run.exit)
	}
	if len(run.stderr) == 0 {
		t.Fatalf("control broken: even with --verbose the serve failure reached fd 2 with 0 " +
			"bytes, so the redirect is not what the main case is measuring")
	}
}

// TestServeFailureNegativeControl proves exit 1 above comes from the malformed
// frame and not from the launch itself. Same arguments, clean EOF on stdin:
// s.Run returns nil and the process exits 0 with both descriptors empty.
func TestServeFailureNegativeControl(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	run := runChildWithStdin(t, nil,
		"--base", "http://127.0.0.1:19899/hs/mcp-1c", "--cache-dir", cacheDir)

	t.Logf("EXIT=%d  stdout=%d bytes  stderr=%d bytes", run.exit, len(run.stdout), len(run.stderr))
	if run.exit != 0 {
		t.Errorf("control broken: a clean EOF exited %d, so exit 1 above is not attributable "+
			"to the malformed frame.\nstderr: %s", run.exit, run.stderr)
	}
	if len(run.stderr) != 0 {
		t.Errorf("a clean shutdown wrote %d bytes to fd 2, so a non-empty fd 2 no longer means "+
			"a failure: %q", len(run.stderr), run.stderr)
	}
}

// TestNoDirectStderrWriteAfterTheRedirect is the guard that covers BOTH exits,
// including the one no input can reach.
//
// The dump-open failure at the top of the serving path cannot currently be
// triggered: openServeIndexLocal returns an error only by returning
// dump.NewIndex's, and NewIndex has exactly three return statements, all of them
// "return idx, nil". So that branch is dead today, which is precisely why a
// runtime test for it would be a test that cannot fail. It is still wrong to
// leave it writing to a descriptor that is not there, because the day NewIndex
// grows an error return the message disappears silently.
//
// This guard PARSES main.go rather than grepping it, and states the property
// directly: inside func main, no direct write to os.Stderr may appear after the
// statement that reassigns os.Stderr. Writes BEFORE the redirect are legitimate
// and plentiful (install mode, build-index mode) and are deliberately allowed.
//
// It is scoped to func main on purpose. reportStartupFailure itself writes to
// os.Stderr, and that write is the dual-channel delivery this whole design is
// built on, not a violation of it.
func TestNoDirectStderrWriteAfterTheRedirect(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	var mainFn *ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "main" && fn.Recv == nil {
			mainFn = fn
			break
		}
	}
	if mainFn == nil {
		t.Fatal("func main not found in main.go")
	}

	// Where os.Stderr stops being file descriptor 2.
	redirect := token.NoPos
	ast.Inspect(mainFn, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			if isOSStderr(lhs) && (redirect == token.NoPos || as.Pos() < redirect) {
				redirect = as.Pos()
			}
		}
		return true
	})
	if redirect == token.NoPos {
		t.Fatal("premise broken: no assignment to os.Stderr found in func main, so this guard " +
			"is not measuring the redirect it was written for")
	}
	t.Logf("os.Stderr is reassigned at %s", fset.Position(redirect))

	var offenders []string
	ast.Inspect(mainFn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() <= redirect {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "fmt" || !strings.HasPrefix(sel.Sel.Name, "Fprint") {
			return true
		}
		if len(call.Args) > 0 && isOSStderr(call.Args[0]) {
			offenders = append(offenders, fset.Position(call.Pos()).String())
		}
		return true
	})

	if len(offenders) > 0 {
		t.Errorf("func main writes directly to os.Stderr after the redirect at %s, so the "+
			"message lands in the log file (or in os.DevNull) instead of reaching the person "+
			"who launched the server. Route it through reportStartupFailure(realStderr, err).\n"+
			"offending call sites: %s",
			fset.Position(redirect), strings.Join(offenders, ", "))
	}
}

// isOSStderr reports whether an expression is the selector os.Stderr.
func isOSStderr(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Stderr" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "os"
}
