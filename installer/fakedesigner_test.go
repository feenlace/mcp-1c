package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A fake DESIGNER, so that Install can be driven end to end.
//
// Install shells out to the platform binary, so the only way to exercise the
// retry ladder is to give it a binary to shell out to. This file makes the test
// binary itself that binary: TestMain runs before the testing flags are parsed,
// sees the environment variable, and serves the call instead of running tests.
// No shell script and no toolchain are involved, so the harness works wherever
// the package's own tests already run.
//
// The fake is a MODEL, not a tape of scripted answers. It keeps an "infobase"
// file, /LoadConfigFromFiles copies the extension's Configuration.xml into it,
// and /UpdateDBCfg reads THAT file and nothing else. This mirrors the fact the
// retry under test depends on and which was measured on a real 1С 8.3.27 base:
// /UpdateDBCfg does not look at the temp directory, it applies whatever the last
// successful load put into the infobase. A fix that strips the property but does
// not load the stripped XML again therefore still fails here, exactly as it does
// on the customer's base.
// ---------------------------------------------------------------------------

// fakeDesignerDirEnv names the directory the fake DESIGNER reads its mode from
// and writes its record of the run into. Install passes the platform path
// through to exec.Command without touching the environment, so the child
// inherits this variable and recognises itself.
const fakeDesignerDirEnv = "MCP_1C_FAKE_DESIGNER_DIR"

// The modes the fake DESIGNER understands, written into <dir>/mode.
const (
	// fakeModeOK accepts everything.
	fakeModeOK = "ok"
	// fakeModeRunModeMismatch refuses /UpdateDBCfg while the loaded
	// configuration still carries <DefaultRunMode>, and accepts it once the
	// property is gone. This is the customer's base.
	fakeModeRunModeMismatch = "runmode"
	// fakeModeRunModeAlways refuses /UpdateDBCfg unconditionally with the same
	// text, so the retry runs and still cannot save the install.
	fakeModeRunModeAlways = "runmode-always"
	// fakeModeLoadFails refuses /LoadConfigFromFiles, so nothing ever reaches
	// the apply leg.
	fakeModeLoadFails = "loadfail"
	// fakeModeInheritedOverride refuses /UpdateDBCfg while the loaded
	// configuration still carries the properties an old compat mode rejects,
	// and accepts it once they are gone. This drives the OTHER branch of the
	// apply-leg switch, so both branches are exercised end to end and not only
	// through their predicates.
	fakeModeInheritedOverride = "inherited-override"
	// fakeModeInheritedAlways refuses /UpdateDBCfg unconditionally with the
	// inherited-override text, so that branch's retry runs and still fails.
	fakeModeInheritedAlways = "inherited-always"
	// fakeModeCompatNotFound is the base whose compat mode forbids extensions
	// outright. /LoadConfigFromFiles CLAIMS success and the extension is
	// silently rejected, so /UpdateDBCfg reports it missing. Nothing is loaded.
	fakeModeCompatNotFound = "notfound"
	// fakeModeUnrelatedApplyFailure refuses /UpdateDBCfg with a failure that
	// matches no retry predicate, which is the ordinary way to reach the
	// default branch of the apply-leg switch.
	fakeModeUnrelatedApplyFailure = "unrelated"
	// fakeModeExistsThenRunModeAlways makes the FIRST load report that the
	// extension already exists, so Install deletes the old one and reloads, and
	// then refuses every apply. This is the run in which the note used to
	// promise a previous version four lines after we printed that we deleted it.
	fakeModeExistsThenRunModeAlways = "exists-runmode-always"
	// fakeModeBothPredicates emits a log carrying BOTH retry texts at once,
	// which a real /Out can do because it accumulates every message of a run.
	// Only the wide strip clears the refusal, so the order of the two cases in
	// the apply-leg switch decides whether the install is recovered.
	fakeModeBothPredicates = "both-predicates"
)

// Optional flags, written as files in the fake's directory alongside the mode.
const (
	// fakeReadOnlyAfterLoadFile makes the fake chmod the extension's
	// Configuration.xml read-only once it has been loaded, so the next strip in
	// Install fails on write. That is the only way to reach the strip-error
	// return of either retry branch.
	fakeReadOnlyAfterLoadFile = "readonly-after-load"
	// fakeFailLoadsAfterFile holds a number N: loads after the N-th one fail.
	// It reaches the reload-error return of either retry branch.
	fakeFailLoadsAfterFile = "fail-loads-after"
)

// alreadyExistsLog is the load refusal that makes Install delete the installed
// extension and load again.
const alreadyExistsLog = "Ошибка: Уже существует расширение конфигурации MCP_HTTPService"

// compatNotFoundLog is what a base whose compat mode forbids extensions reports
// from /UpdateDBCfg. classifyDesignerError recognises this text.
const compatNotFoundLog = "Не найдено: расширение конфигурации с указанным именем не найдено: " + extensionName

// unrelatedApplyLog is an apply failure no retry predicate matches.
const unrelatedApplyLog = "Ошибка: Неверный пароль базы данных"

// inheritedOverrideLog is the refusal old configurations give to an extension
// that overrides inherited properties of the base configuration.
const inheritedOverrideLog = "Ошибка: переопределение свойств заимствованных объектов не поддерживается"

// runModeMismatchLog is the DESIGNER message measured on a real 1С 8.3.27 base,
// reproduced verbatim including the double space the platform leaves where the
// object name would go.
const runModeMismatchLog = "MCP_HTTPService: Значение контролируемого свойства ОсновнойРежимЗапуска " +
	"у объекта  не совпадает со значением в расширяемой конфигурации"

// loadFailureLog is a plausible unrelated load failure, used to show that the
// apply-leg advice does NOT appear when nothing was loaded.
const loadFailureLog = "Ошибка загрузки конфигурации из файлов"

// Names of the files the fake keeps inside its directory.
const (
	fakeModeFile     = "mode"
	fakeCallsFile    = "calls.log"
	fakeInfobaseFile = "infobase.xml"
)

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeDesignerDirEnv); dir != "" {
		os.Exit(serveFakeDesigner(dir, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// designerCall is one invocation of the fake, as recorded in calls.log.
type designerCall struct {
	op   string   // "/LoadConfigFromFiles", "/UpdateDBCfg", "/ManageCfgExtensions" or "other"
	args []string // the full argument list DESIGNER was called with
}

// serveFakeDesigner answers a single DESIGNER invocation and returns the exit
// code the platform would return.
func serveFakeDesigner(dir string, args []string) int {
	op, extDir, outPath := classifyDesignerCall(args)

	recordFakeCall(dir, op, args)

	mode := readFakeFile(dir, fakeModeFile)
	if mode == "" {
		mode = fakeModeOK
	}
	infobase := filepath.Join(dir, fakeInfobaseFile)

	switch op {
	case "/LoadConfigFromFiles":
		if mode == fakeModeLoadFails {
			writeFakeLog(outPath, loadFailureLog)
			return 1
		}
		attempt := countFakeCalls(dir, "/LoadConfigFromFiles")
		// The first load of this mode reports the extension as already
		// installed, which sends Install through the delete and reload.
		if mode == fakeModeExistsThenRunModeAlways && attempt == 1 {
			writeFakeLog(outPath, alreadyExistsLog)
			return 1
		}
		if limit := readFakeFile(dir, fakeFailLoadsAfterFile); limit != "" {
			if n, convErr := strconv.Atoi(limit); convErr == nil && attempt > n {
				writeFakeLog(outPath, loadFailureLog)
				return 1
			}
		}
		cfg, err := os.ReadFile(filepath.Join(extDir, "Configuration.xml"))
		if err != nil {
			writeFakeLog(outPath, "Не найдено: "+err.Error())
			return 1
		}
		// Snapshot what this load was handed, then let it become the state the
		// apply leg will read.
		snapshots, _ := filepath.Glob(filepath.Join(dir, "loaded-*.xml"))
		if err := os.WriteFile(filepath.Join(dir,
			fmt.Sprintf("loaded-%d.xml", len(snapshots)+1)), cfg, 0o644); err != nil {
			writeFakeLog(outPath, err.Error())
			return 1
		}
		if err := os.WriteFile(infobase, cfg, 0o644); err != nil {
			writeFakeLog(outPath, err.Error())
			return 1
		}
		// Make the next strip fail on write, which is the only route to the
		// strip-error return of either retry branch.
		if readFakeFileExists(dir, fakeReadOnlyAfterLoadFile) {
			os.Chmod(filepath.Join(extDir, "Configuration.xml"), 0o444) //nolint:errcheck // best effort
		}
		writeFakeLog(outPath, "Загрузка конфигурации из файлов завершена")
		return 0

	case "/UpdateDBCfg":
		loaded, err := os.ReadFile(infobase)
		if err != nil {
			// Nothing was ever loaded: the platform reports the extension as
			// missing, which is the shape classifyDesignerError already knows.
			writeFakeLog(outPath, "Не найдено: расширение конфигурации с указанным именем не найдено: "+extensionName)
			return 1
		}
		switch mode {
		case fakeModeRunModeAlways, fakeModeExistsThenRunModeAlways:
			writeFakeLog(outPath, runModeMismatchLog)
			return 101
		case fakeModeInheritedAlways:
			writeFakeLog(outPath, inheritedOverrideLog)
			return 101
		case fakeModeCompatNotFound:
			writeFakeLog(outPath, compatNotFoundLog)
			return 1
		case fakeModeUnrelatedApplyFailure:
			writeFakeLog(outPath, unrelatedApplyLog)
			return 1
		case fakeModeRunModeMismatch:
			if strings.Contains(string(loaded), "<DefaultRunMode>") {
				writeFakeLog(outPath, runModeMismatchLog)
				return 101
			}
		case fakeModeInheritedOverride:
			// ScriptVariant is one of the twelve stripInheritedProperties takes
			// and one defaultRunModeRe does not, so only the wide strip clears
			// this refusal.
			if strings.Contains(string(loaded), "<ScriptVariant>") {
				writeFakeLog(outPath, inheritedOverrideLog)
				return 101
			}
		case fakeModeBothPredicates:
			// One /Out carrying both refusals. Only the wide strip clears it,
			// so recovering the install depends on which case is tried first.
			if strings.Contains(string(loaded), "<ScriptVariant>") {
				writeFakeLog(outPath, inheritedOverrideLog+"\n"+runModeMismatchLog)
				return 101
			}
		}
		writeFakeLog(outPath, "Обновление конфигурации базы данных завершено")
		return 0
	}

	writeFakeLog(outPath, "OK")
	return 0
}

// classifyDesignerCall reads the operation, the source directory of a load and
// the /Out log path back out of the argument list Install built.
func classifyDesignerCall(args []string) (op, extDir, outPath string) {
	op = "other"
	for i, a := range args {
		switch a {
		case "/LoadConfigFromFiles":
			op = a
			if i+1 < len(args) {
				extDir = args[i+1]
			}
		case "/UpdateDBCfg", "/ManageCfgExtensions":
			op = a
		case "/Out":
			if i+1 < len(args) {
				outPath = args[i+1]
			}
		}
	}
	return op, extDir, outPath
}

func recordFakeCall(dir, op string, args []string) {
	f, err := os.OpenFile(filepath.Join(dir, fakeCallsFile),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\n", op, strings.Join(args, "\t"))
}

// countFakeCalls returns how many invocations of the given operation have been
// recorded so far, including the one being served.
func countFakeCalls(dir, op string) int {
	raw, err := os.ReadFile(filepath.Join(dir, fakeCallsFile))
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, op+"\t") {
			n++
		}
	}
	return n
}

func readFakeFileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func readFakeFile(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeFakeLog writes the DESIGNER log the way the platform does: to the path
// given after /Out. Install reads the process exit code together with this file.
func writeFakeLog(outPath, text string) {
	if outPath == "" {
		return
	}
	os.WriteFile(outPath, []byte(text+"\n"), 0o644) //nolint:errcheck // best effort, mirrors DESIGNER
}

// newFakeDesigner prepares a directory for the fake, points the environment at
// it and returns the directory. The platform path callers should pass to Install
// is the test binary itself. Extra flag files are created empty unless the name
// carries a value after '=', as in "fail-loads-after=1".
func newFakeDesigner(t *testing.T, mode string, flags ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fakeModeFile), []byte(mode), 0o644); err != nil {
		t.Fatalf("write fake mode: %v", err)
	}
	for _, flag := range flags {
		name, value, _ := strings.Cut(flag, "=")
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
			t.Fatalf("write fake flag %q: %v", name, err)
		}
	}
	t.Setenv(fakeDesignerDirEnv, dir)
	return dir
}

// fakePlatformExe returns the path Install should be given as the platform
// binary: this very test binary, which TestMain turns into the fake DESIGNER.
func fakePlatformExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary to use as the fake platform: %v", err)
	}
	return exe
}

// fakeCalls reads back the invocations the fake recorded, in order.
func fakeCalls(t *testing.T, dir string) []designerCall {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, fakeCallsFile))
	if err != nil {
		return nil
	}
	var calls []designerCall
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		calls = append(calls, designerCall{op: fields[0], args: fields[1:]})
	}
	return calls
}

// callOps reduces the recorded calls to the sequence of operations.
func callOps(calls []designerCall) []string {
	ops := make([]string, 0, len(calls))
	for _, c := range calls {
		ops = append(ops, c.op)
	}
	return ops
}

// captureStdout runs fn with os.Stdout replaced by a pipe and returns
// everything fn printed. Install reports progress and its notes with fmt.Println,
// so this is the only place those lines can be read back.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
		done <- sb.String()
	}()

	func() {
		defer func() {
			os.Stdout = saved
			w.Close()
		}()
		fn()
	}()

	out := <-done
	r.Close()
	return out
}

// loadedConfiguration returns the Configuration.xml the n-th (1-based)
// /LoadConfigFromFiles call was handed.
func loadedConfiguration(t *testing.T, dir string, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("loaded-%d.xml", n)))
	if err != nil {
		t.Fatalf("load #%d left no snapshot: %v", n, err)
	}
	return string(data)
}
