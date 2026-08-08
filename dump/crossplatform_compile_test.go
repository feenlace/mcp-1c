package dump

import (
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// EVERY FILE IN THIS MODULE, TEST FILES INCLUDED, MUST COMPILE FOR WINDOWS.
//
// WHAT WENT WRONG AND WHY NOTHING SAW IT. dump.exhaustDescriptors needs
// RLIMIT_NOFILE and lives in a file carrying `//go:build unix`; its callers sat in
// a file carrying no constraint at all. On unix both are compiled and the package
// is green; for GOOS=windows the helper is dropped and the callers are not, so
// `go vet ./dump/` answered `undefined: exhaustDescriptors` while every local run
// stayed green. The release workflow builds Windows with `go build ./cmd/mcp-1c/`
// (.github/workflows/release.yml, job `build`), which compiles no test file of any
// package, and runs `go test ./...` in the `test` job on ubuntu only. Nothing in
// this repository type-checked the test tree for windows at all.
//
// THE CHECK IS `go vet` AND NOT `go build`. `go build` does not look at _test.go
// files, which is precisely the half that broke; vet type-checks the test files of
// every package it is given. `go test -run=^$` would work too and costs a link of
// every test binary for a platform this machine cannot run.
//
// WINDOWS AND NOT «EVERY GOOS». The released targets are linux, darwin and windows
// (Makefile `release`, and the goos matrix of .github/workflows/release.yml). The
// first two satisfy the `unix` constraint, so a developer machine and the ubuntu CI
// job already type-check what they select; windows is the only released target that
// selects the other side of every `//go:build unix` in this module, and it is the
// one README.md's OS table marks as serving all three modes while sending macOS to
// a Windows VM.
// IT DOES NOT HONOUR -short, AND THAT IS THE POINT RATHER THAN AN OVERSIGHT. It
// used to open with `if testing.Short() { t.Skip(...) }`, on the reasoning that a
// cross-GOOS type-check is expensive. REPRODUCED with the exact defect this file
// exists for planted into this package, a `//go:build unix` helper beside an
// unconstrained caller: `go test -short` reported SKIP and exited 0, while the same
// tree without the flag reported FAIL and exited 1. So the flag turned the one guard
// on a released target into a no-op, and it did so silently.
//
// NOTHING ELSE COVERS THAT GAP. windows is type-checked by no job in this
// repository: `.github/workflows/release.yml` runs `go test ./... -v -race` in its
// `test` job on `runs-on: ubuntu-latest`, and `windows-latest` appears in NONE of
// the 8 revisions of `.github/` in this repository's history, measured with
// `ubuntu-latest` as the control that the same sweep does find a runner when one is
// there. A guard that can skip itself cannot defend a platform nothing else runs.
//
// THE COST IS THREE `go vet` RUNS, two of them over the throwaway module built
// below and the third over this one. That is the price of the only check standing
// between this module and a released target, and it is paid on every run rather
// than on the runs somebody remembered.
func TestEveryTestFileCompilesForWindows(t *testing.T) {
	// POSITIVE CONTROL, RUN FIRST AND OVER A MODULE THAT IS NOT THIS ONE. A vet
	// invocation that came back clean for any reason at all — a toolchain that
	// cannot cross-target, an env var that turns the run into a no-op, a wrapper
	// that swallows the exit status — would certify this module by silence. So a
	// throwaway module carrying THE EXACT DEFECT SHAPE is checked first: a helper
	// behind `//go:build unix`, a caller with no constraint. If that one is not
	// refused, nothing below is measured.
	ctlDir := t.TempDir()
	writeFile(t, filepath.Join(ctlDir, "go.mod"), "module example.com/crossoscontrol\n\ngo 1.25\n")
	writeFile(t, filepath.Join(ctlDir, "helper_unix_test.go"),
		"//go:build unix\n\npackage crossoscontrol\n\nfunc onlyCompiledOnUnix() bool { return true }\n")
	writeFile(t, filepath.Join(ctlDir, "caller_test.go"),
		"package crossoscontrol\n\nimport \"testing\"\n\n"+
			"func TestCaller(t *testing.T) {\n\tif !onlyCompiledOnUnix() {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")

	if out, err := vetFor(t, "windows", ctlDir); err == nil {
		t.Fatalf("control failed: `GOOS=windows go vet` PASSED a module whose test file calls a "+
			"helper compiled only on unix, so a pass on this module measures nothing.\n%s", out)
	}
	// AND THE CONTROL FIXTURE IS OTHERWISE SOUND: the same files are accepted for a
	// GOOS that does satisfy the constraint. Without this the refusal above could be
	// a broken fixture (a syntax error, an unusable temp module) rather than the
	// constraint doing its work.
	if out, err := vetFor(t, "darwin", ctlDir); err != nil {
		t.Fatalf("control failed: the fixture is refused for darwin too, so its refusal for "+
			"windows says nothing about build constraints.\n%s", out)
	}

	root := moduleRoot(t)
	out, err := vetFor(t, "windows", root)
	if err != nil {
		t.Errorf("`GOOS=windows go vet ./...` refuses this module, so `go test ./...` is a hard "+
			"failure on a released target of this product.\n%s", out)
	}
}

// vetFor type-checks dir's whole module for one GOOS and returns the combined
// output beside the exit status.
//
// THE TOOLCHAIN IS TAKEN FROM GOROOT AND NOT FROM PATH. build.Default.GOROOT is
// the root of the toolchain that compiled this test, so the vet runs are done by
// the same go this package is built with rather than by whatever shim happens to
// be first on PATH. PATH is the fallback for a toolchain installed without its
// bin/go in place.
//
// GOWORK IS TURNED OFF so an ambient go.work outside the checkout cannot pull a
// different set of modules into either run. GOFLAGS is cleared for the same
// reason: an inherited -mod or -tags would make the two runs answer about
// different builds.
func vetFor(t *testing.T, goos, dir string) (string, error) {
	t.Helper()
	cmd := exec.Command(goToolPath(t), "vet", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOWORK=off", "GOFLAGS=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// goToolPath is the go binary the vet runs are driven with.
//
// A MISSING TOOLCHAIN IS A FAILURE AND NOT A SKIP, which is the second fail-open
// this file carried. It ended `t.Skipf("no go toolchain to drive")`, so a run that
// could not find a go binary reported PASS and exited 0 over a module carrying the
// defect. REPRODUCED by compiling this package's test binary with the planted
// `//go:build unix` helper and running it under GOROOT and PATH pointing at
// directories that do not exist: SKIP, exit 0.
//
// A SKIP HERE IS AN UNANSWERED QUESTION ABOUT A RELEASED TARGET, and there is no
// second guard behind it: no job in this repository type-checks windows. The
// premise is also not a fragile one to insist on, because something compiled this
// test: build.Default.GOROOT is the root of that toolchain, and PATH is the
// fallback for a toolchain installed without bin/go in place. If neither answers,
// the environment cannot run this check and saying so out loud is the only honest
// result.
func goToolPath(t *testing.T) string {
	t.Helper()
	if root := build.Default.GOROOT; root != "" {
		p := filepath.Join(root, "bin", "go")
		if runtime.GOOS == "windows" {
			p += ".exe"
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	p, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go toolchain to drive the cross-GOOS type-check (%v). GOROOT is %q and "+
			"`go` is not on PATH either, so this guard cannot answer whether the module "+
			"compiles for windows. Nothing else in this repository asks that question, so a "+
			"skip here would report a clean run over an unmeasured released target.",
			err, build.Default.GOROOT)
	}
	return p
}

// moduleRoot walks up from the test's working directory to the directory holding
// go.mod. It is the module this test is a part of by construction rather than by a
// relative path spelled out here, which cannot drift if the package moves.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	if strings.TrimSpace(body) == "" {
		t.Fatalf("refusing to write an empty fixture at %s", path)
	}
}
