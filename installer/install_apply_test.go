package installer

import (
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/extension"
)

// ---------------------------------------------------------------------------
// The controlled-property mismatch surfaces on the APPLY leg, and until now the
// handler for it sat on the LOAD leg.
//
// Measured on a real 1С 8.3.27 base, four runs, reproduced byte for byte
// including the customer's text: /LoadConfigFromFiles SUCCEEDS, and the mismatch
// appears only on /UpdateDBCfg, which exits 101 with «Значение контролируемого
// свойства ОсновнойРежимЗапуска у объекта  не совпадает со значением в
// расширяемой конфигурации». The line the load-leg handler prints,
// «Retrying without DefaultRunMode property...», appeared in none of the four
// runs: on 8.3.14 and newer that handler is unreachable for this error class.
// The apply leg had one retry predicate, «переопределение свойств заимствованных
// объектов», which never matches this text, so the install stopped.
//
// What the customer was left with was worse than a failure that says so. A fresh
// install leaves the extension loaded but not applied. An UPDATE leaves the OLD
// version applied and serving while the new one sits in the main configuration,
// which is why the customer read «обновилось всё-таки» and went on running the
// old code.
//
// The retry has to strip NARROWLY. stripInheritedProperties takes twelve
// elements and is the reason the installer used to have to tell the customer to
// assign the role by hand; a run-mode mismatch must not cost anything beyond the
// run mode. The test below does not take that on trust: it runs the WIDE regexp
// over the shipped Configuration.xml, and every element the wide strip would
// have taken other than the run mode is required to survive the reload.
// ---------------------------------------------------------------------------

// shippedPlatform is a platform new enough that Install does no pre-patching at
// all, which is the platform the defect was measured on.
const shippedPlatform = "8.3.27"

// shippedConfiguration returns the extension's Configuration.xml exactly as it
// is embedded in the binary.
func shippedConfiguration(t *testing.T) string {
	t.Helper()
	raw, err := extension.Source.ReadFile("src/Configuration.xml")
	if err != nil {
		t.Fatalf("read embedded Configuration.xml: %v", err)
	}
	return string(raw)
}

func TestInstallRetriesTheApplyLegOnARunModeMismatch(t *testing.T) {
	dir := newFakeDesigner(t, fakeModeRunModeMismatch)
	exe := fakePlatformExe(t)

	var err error
	out := captureStdout(t, func() {
		err = Install(extension.Source, `C:\base`, false, exe, "", "", shippedPlatform, false)
	})
	if err != nil {
		t.Fatalf("Install must recover from a controlled-property mismatch on the apply leg, got: %v\n"+
			"stdout:\n%s", err, out)
	}

	// The reload is not optional. /UpdateDBCfg does not read the temp directory,
	// it applies what the last successful load put into the infobase, so the
	// sequence has to be load, apply, load, apply.
	wantOps := []string{"/LoadConfigFromFiles", "/UpdateDBCfg", "/LoadConfigFromFiles", "/UpdateDBCfg"}
	calls := fakeCalls(t, dir)
	gotOps := callOps(calls)
	if strings.Join(gotOps, ",") != strings.Join(wantOps, ",") {
		t.Fatalf("DESIGNER call sequence\ngot:  %v\nwant: %v\nstdout:\n%s", gotOps, wantOps, out)
	}

	// Both loads must address the same extension in the same directory,
	// otherwise the second one is loading something else. Everything is compared
	// except the /Out pair, which runDesigner deliberately points at a fresh
	// temporary log for every call.
	firstArgs := argsWithoutOut(calls[0].args)
	secondArgs := argsWithoutOut(calls[2].args)
	if len(firstArgs) == len(calls[0].args) {
		t.Fatalf("no /Out pair was dropped from %v, so this comparison is not the one described", calls[0].args)
	}
	if strings.Join(firstArgs, "\t") != strings.Join(secondArgs, "\t") {
		t.Errorf("the reload was given different arguments than the first load\nfirst:  %v\nsecond: %v",
			firstArgs, secondArgs)
	}
	if !strings.Contains(strings.Join(firstArgs, "\t"), "-Extension\t"+extensionName) {
		t.Errorf("the load does not name the extension: %v", firstArgs)
	}

	first := loadedConfiguration(t, dir, 1)
	second := loadedConfiguration(t, dir, 2)

	// Positive control for the reading below: the element IS visible to this
	// scanner when it is present, so its absence from the second load is a real
	// removal and not a scanner that matches nothing.
	if !strings.Contains(first, "<DefaultRunMode>") {
		t.Fatalf("the first load was not handed <DefaultRunMode>, so this test proves nothing about "+
			"its removal:\n%s", first)
	}
	if strings.Contains(second, "<DefaultRunMode>") {
		t.Errorf("the reload still carries <DefaultRunMode>, so the strip did not reach the file the "+
			"apply leg reads:\n%s", second)
	}

	// The narrow strip, established against the wide one. Every element the WIDE
	// regexp finds in the shipped file, other than the run mode, has to survive.
	// The list is read out of the production regexp and the shipped XML, so it
	// cannot go stale and cannot be padded by hand.
	shipped := shippedConfiguration(t)
	wide := inheritedPropertyRe.FindAllString(shipped, -1)
	if len(wide) < 4 {
		t.Fatalf("the wide strip matches %d elements of the shipped Configuration.xml. Below four "+
			"there is nothing left for the narrow strip to be narrower THAN, and this assertion "+
			"would pass on any implementation", len(wide))
	}
	narrow := defaultRunModeRe.FindAllString(shipped, -1)
	if len(narrow) != 1 {
		t.Fatalf("defaultRunModeRe matches %d elements of the shipped Configuration.xml, want exactly 1",
			len(narrow))
	}

	survivors := 0
	for _, element := range wide {
		if strings.Contains(element, "<DefaultRunMode>") {
			continue
		}
		survivors++
		if !strings.Contains(second, strings.TrimSpace(element)) {
			t.Errorf("the reload lost %q. A run-mode mismatch must cost the customer the run mode and "+
				"nothing else; this is what stripInheritedProperties would have taken",
				strings.TrimSpace(element))
		}
	}
	if survivors < 3 {
		t.Fatalf("only %d elements besides the run mode were checked for survival, so the difference "+
			"between the narrow and the wide strip is not being measured", survivors)
	}
	t.Logf("the wide strip matches %d elements of the shipped Configuration.xml; %d of them are "+
		"required here to survive the narrow one", len(wide), survivors)

	// Nothing structural was destroyed either.
	for _, keep := range []string{"<Name>" + extensionName + "</Name>", "<ChildObjects>", "<Role>MCP_ОсновнаяРоль</Role>"} {
		if !strings.Contains(second, keep) {
			t.Errorf("the reload lost %q", keep)
		}
	}

	// The installer must say what it did.
	if !strings.Contains(out, "Retrying without DefaultRunMode property") {
		t.Errorf("the retry ran but printed nothing about it:\n%s", out)
	}
}

// TestInstallLeavesTheInheritedOverrideRetryReachable pins the branch the new one
// is composed WITH rather than layered over. Both predicates have to keep firing,
// each on its own text.
func TestInstallLeavesTheInheritedOverrideRetryReachable(t *testing.T) {
	if !isInheritedOverrideError(errString("Ошибка: переопределение свойств заимствованных объектов не допускается")) {
		t.Error("the inherited-override predicate no longer recognises its own text")
	}
	if isInheritedOverrideError(errString(runModeMismatchLog)) {
		t.Error("the inherited-override predicate now swallows the run-mode text, so the branch added " +
			"for the run mode is unreachable behind it")
	}
	if !isRunModeMismatchError(errString(runModeMismatchLog)) {
		t.Error("the run-mode predicate does not recognise the message measured on the customer's base")
	}
	if isRunModeMismatchError(errString("Ошибка: переопределение свойств заимствованных объектов не допускается")) {
		t.Error("the run-mode predicate now swallows the inherited-override text, so the older branch " +
			"is unreachable behind it")
	}
	if isRunModeMismatchError(nil) || isInheritedOverrideError(nil) {
		t.Error("a nil error must match neither predicate")
	}
	if isRunModeMismatchError(errString("Неверный пароль базы данных")) ||
		isInheritedOverrideError(errString("Неверный пароль базы данных")) {
		t.Error("an unrelated error must match neither predicate")
	}
}

// argsWithoutOut drops the "/Out <path>" pair from a recorded argument list.
// runDesigner points every call at its own temporary log file, so that pair is
// the one thing two otherwise identical calls are expected to differ in.
func argsWithoutOut(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "/Out" {
			i++ // skip the path that follows
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// TestApplyLegTriesTheWiderStripFirst pins the ORDER of the two cases, which is
// load-bearing and was pinned by nothing.
//
// A DESIGNER log is not one message. /Out accumulates everything a run emitted,
// so one refusal can carry both texts at once, and then both predicates match
// and the order of the cases alone decides what happens. Trying the inherited
// override first strips widely, which also removes the run mode, and the install
// is recovered. Trying the run mode first removes only that property, the base
// still refuses the rest, and the install is lost. Swapping the two cases was a
// green edit.
func TestApplyLegTriesTheWiderStripFirst(t *testing.T) {
	bothTexts := errString(inheritedOverrideLog + "\n" + runModeMismatchLog)

	// The premise: this really is a log both predicates match. Without it the
	// test would be pinning an order that never comes into play.
	if !isInheritedOverrideError(bothTexts) || !isRunModeMismatchError(bothTexts) {
		t.Fatalf("this log is supposed to match BOTH predicates, and it does not: inherited=%v runmode=%v",
			isInheritedOverrideError(bothTexts), isRunModeMismatchError(bothTexts))
	}

	dir := newFakeDesigner(t, fakeModeBothPredicates)

	var err error
	out := captureStdout(t, func() {
		err = Install(extension.Source, `C:\base`, false, fakePlatformExe(t), "", "", shippedPlatform, false)
	})
	if err != nil {
		t.Fatalf("with both refusals in one log the wider strip clears the base and the install is "+
			"recovered. It was not: %v\nstdout:\n%s", err, out)
	}

	// It recovered through the wide strip, not the narrow one. The printed line
	// names which branch ran, and only one of them may have run.
	if !strings.Contains(out, "Retrying without inherited properties") {
		t.Errorf("the inherited-override branch did not run:\n%s", out)
	}
	if strings.Contains(out, "Retrying without DefaultRunMode property") {
		t.Errorf("the run-mode branch ran first. It strips only the run mode, the base still refuses "+
			"the remaining inherited properties, and the install is lost:\n%s", out)
	}

	wantOps := []string{"/LoadConfigFromFiles", "/UpdateDBCfg", "/LoadConfigFromFiles", "/UpdateDBCfg"}
	if got := callOps(fakeCalls(t, dir)); strings.Join(got, ",") != strings.Join(wantOps, ",") {
		t.Errorf("call sequence\ngot:  %v\nwant: %v", got, wantOps)
	}

	// The narrow strip alone would have left ScriptVariant in place, which is
	// what this base refuses. Proving it is gone proves the wide strip ran.
	second := loadedConfiguration(t, dir, 2)
	if strings.Contains(second, "<ScriptVariant>") {
		t.Errorf("the reload still carries <ScriptVariant>, so the narrow strip ran:\n%s", second)
	}
	if first := loadedConfiguration(t, dir, 1); !strings.Contains(first, "<ScriptVariant>") {
		t.Fatalf("the first load did not carry <ScriptVariant>, so its absence later proves nothing")
	}
}

// errString is the smallest possible error carrying exactly the given text.
type errString string

func (e errString) Error() string { return string(e) }
