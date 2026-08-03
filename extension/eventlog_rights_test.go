package extension

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// READING THE JOURNAL NEEDS A RIGHT, AND A CALLER WITHOUT IT USED TO GET A 500
// CARRYING THE MODULE NAME AND A LINE NUMBER.
//
// ВыгрузитьЖурналРегистрации raises «Пользователю недостаточно прав для
// выполнения операции» for an account without Администрирование, the call sits
// outside every Попытка ON PURPOSE, and the platform therefore answered the
// caller with a page of its own:
//
//	HTTP 500
//	{MCP_HTTPService HTTPСервис.MCPService.Модуль(1250)}: Ошибка при вызове
//	метода контекста (ВыгрузитьЖурналРегистрации)
//
// measured on 1С 8.3.27 for infobase user Demo, 2026-08-03. That is the same
// class the module already closed for limit, whose comment states the goal: an
// input the handler cannot honour must not «рвать обработчик, отдавая
// вызывающему имя модуля и номер строки в теле ответа с кодом 500».
//
// THE REPAIR IS A CHECK AND NOT A CATCH, and the difference is the whole point.
// Wrapping ВыгрузитьЖурналРегистрации in Попытка would falsify
// TestEventLogRightsFailureRaisesInsideTheExtension (tools/remedy_truth_test.go)
// and the paragraph in tools/toolerror.go that rests on it, both of which say
// the call is outside every Попытка and that an exception there escapes the
// handler. ПравоДоступа asks a question; it opens no Попытка and moves nothing,
// so that property is untouched and is re-asserted below rather than assumed.
//
// THE PRE-EXISTING user-filter 403 DID NOT ALREADY COVER THIS. НайтиПоИмени
// succeeds on the caller's OWN name without the right (measured the same day:
// {"limit":3,"user":"Demo"} as Demo answered 500, not 403), so that branch fires
// only when the requested name differs from the caller's own, and a call with no
// filter at all never reached it.
// ---------------------------------------------------------------------------

const eventLogModulePath = "src/HTTPServices/MCPService/Ext/Module.bsl"

// eventLogFunctionLines returns the lines of ЖурналРегистрацииPOST as embedded
// in this binary, i.e. what an --install actually writes into a base.
func eventLogFunctionLines(t *testing.T) []string {
	t.Helper()
	src, err := Source.ReadFile(eventLogModulePath)
	if err != nil {
		t.Fatalf("reading the embedded extension module: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	start, end := -1, -1
	for i, l := range lines {
		s := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(s, "Функция ЖурналРегистрацииPOST"):
			start = i
		case start >= 0 && end < 0 && s == "КонецФункции":
			end = i
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("ЖурналРегистрацииPOST was not found in the embedded module; nothing below " +
			"measures anything")
	}
	return lines[start : end+1]
}

// rightsRefusalText is the first fragment of the diagnostic the rights refusal
// carries. THE REFUSAL IS FOUND BY THIS AND NOT BY «the first 403 after the
// check», which is what it used to be found by and what made the guard blind.
//
// ЖурналРегистрацииPOST answers 403 TWICE, and both stand before the dump: the
// rights gate here, and the user filter refusal further down. A walk that took
// the first `ОтветОшибка(403,` it met simply latched onto the other one once this
// refusal stopped being a 403, and every ordering assertion below stayed true of
// a refusal that is not the subject of this test. Measured: changing this site to
// 500 moved the guard's own log line from «refusal at +21» to «refusal at +117»
// and the whole repository stayed green.
const rightsRefusalText = "reading the event log requires the Администрирование right"

// answerStatusRE reads the status out of an ОтветОшибка call.
var answerStatusRE = regexp.MustCompile(`ОтветОшибка\((\d+),`)

// eventLogRefusalStatusInGo is the status tools/toolerror.go keys the rights
// remedy on (tools.eventLogRefusalStatus). It is written here because the two
// packages do not import one another; the value is held to the Go side by
// tools/eventlog_refusal_pin_test.go, which drives the renderer with the status
// it reads out of this same module.
const eventLogRefusalStatusInGo = 403

// TestEventLogRefusesACallerWithoutTheRight is the guard for the defect above.
//
// It asserts ORDER, POLARITY and STATUS, and each of the three exists because
// deleting the check is not the only way to break it:
//
//   - ORDER, because a check that stands after the dump call would leave the 500
//     exactly where it was;
//   - POLARITY, because inverting the condition turns the gate into its opposite
//     and leaves every position unchanged. Measured: with `Если НЕ ПравоДоступа`
//     changed to `Если ПравоДоступа`, rightless callers reach the dump again and
//     the guard's log line stayed byte identical;
//   - STATUS, because the remedy on the Go side is reached by status, so a
//     refusal that stops being a 403 disconnects the advice without removing it.
func TestEventLogRefusesACallerWithoutTheRight(t *testing.T) {
	lines := eventLogFunctionLines(t)

	const (
		probe = `ПравоДоступа("Администрирование", Метаданные)`
		dump  = "ВыгрузитьЖурналРегистрации("
	)

	probeAt, refusalAt, dumpAt := -1, -1, -1
	for i, l := range lines {
		s := strings.TrimSpace(l)
		if probeAt < 0 && strings.Contains(s, probe) {
			probeAt = i
		}
		if refusalAt < 0 && strings.Contains(s, rightsRefusalText) &&
			strings.Contains(s, "ОтветОшибка(") {
			refusalAt = i
		}
		if dumpAt < 0 && strings.Contains(s, dump) {
			dumpAt = i
		}
	}

	// CONTROL: the walk is reading the real function. Without this every
	// assertion below is satisfiable by an empty slice.
	if dumpAt < 0 {
		t.Fatal("CONTROL: ВыгрузитьЖурналРегистрации is not in the extracted body, so the walk " +
			"is not looking at ЖурналРегистрацииPOST")
	}

	if probeAt < 0 {
		t.Fatalf("ЖурналРегистрацииPOST does not ask %s; an account without that right reaches "+
			"ВыгрузитьЖурналРегистрации and the platform answers the caller with a 500 carrying "+
			"the module name and a line number", probe)
	}
	if refusalAt < 0 {
		t.Fatalf("no ОтветОшибка in ЖурналРегистрацииPOST carries %q, so the rights refusal is "+
			"gone or its diagnostic changed. tools/toolerror.go picks the remedy for this "+
			"endpoint by that same prefix, so the caller is either not told or told the other "+
			"refusal's story", rightsRefusalText)
	}

	// THE STATUS IS PART OF THE CONTRACT, not an incidental of the line. It is
	// read off the refusal that was located by its text, so a change here fails
	// instead of moving the walk onto the neighbouring 403.
	m := answerStatusRE.FindStringSubmatch(strings.TrimSpace(lines[refusalAt]))
	if m == nil {
		t.Fatalf("the rights refusal at +%d is not an ОтветОшибка(<код>, …) call, so its status "+
			"cannot be read: %s", refusalAt, strings.TrimSpace(lines[refusalAt]))
	}
	status, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("the rights refusal answers %q, which is not a status", m[1])
	}
	if status != eventLogRefusalStatusInGo {
		t.Errorf("the rights refusal answers %d, not %d. tools/toolerror.go reaches "+
			"remedyEventLogNoRight only for %d on this endpoint, so at %d the caller gets the "+
			"bare text with no advice at all", status, eventLogRefusalStatusInGo,
			eventLogRefusalStatusInGo, status)
	}

	// POLARITY. Everything else here is about WHERE the refusal stands; this is
	// about WHEN it is reached.
	if ok, why := refusalIsOnTheAbsentRightBranch(lines, probeAt, refusalAt); !ok {
		t.Errorf("the rights refusal is not on the branch taken when the right is ABSENT: %s. "+
			"A caller without Администрирование then walks past the gate to "+
			"ВыгрузитьЖурналРегистрации, which is the 500 this check exists to prevent", why)
	}

	if !(probeAt < refusalAt && refusalAt < dumpAt) {
		t.Errorf("the refusal does not stand between the check and the dump: check at +%d, "+
			"refusal at +%d, ВыгрузитьЖурналРегистрации at +%d (offsets inside the function). "+
			"A check that does not precede the call leaves the 500 where it was",
			probeAt, refusalAt, dumpAt)
	}

	// The refusal must be a REFUSAL: no rows and no total. An answer shaped like
	// a normal one is the failure mode this whole change exists to prevent, so
	// nothing that builds the success payload may run before the return.
	for i := 0; i < refusalAt && i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		for _, payload := range []string{`Результат.Вставить("events"`, `Результат.Вставить("total"`} {
			if strings.Contains(s, payload) {
				t.Errorf("%s runs before the rights refusal at +%d; a refusal that carries rows "+
					"or a count presents a log the account may not read as a normal answer",
					payload, refusalAt)
			}
		}
	}

	// A CHECK, NOT A CATCH. The probe must not be inside a Попытка either: a
	// swallowed probe would decide by accident.
	depth, probeDepth := 0, -1
	for i, l := range lines {
		s := strings.TrimSpace(l)
		switch {
		case s == "Попытка":
			depth++
		case strings.HasPrefix(s, "КонецПопытки"):
			depth--
		}
		if i == probeAt {
			probeDepth = depth
		}
	}
	if probeDepth != 0 {
		t.Errorf("the rights check sits inside %d Попытка block(s); it is a check precisely so "+
			"that it is not a catch", probeDepth)
	}

	// AND THE PROPERTY THE REPAIR MUST NOT COST. Re-asserted here so the two
	// halves cannot drift apart: the dump call stays outside every Попытка.
	depth, dumpDepth, opened := 0, -1, 0
	for i, l := range lines {
		s := strings.TrimSpace(l)
		switch {
		case s == "Попытка":
			depth++
			opened++
		case strings.HasPrefix(s, "КонецПопытки"):
			depth--
		}
		if i == dumpAt {
			dumpDepth = depth
		}
	}
	if opened == 0 {
		t.Fatal("CONTROL: the walk found no Попытка at all, so a depth of 0 proves nothing")
	}
	if dumpDepth != 0 {
		t.Errorf("ВыгрузитьЖурналРегистрации is now inside %d Попытка block(s); the rights check "+
			"was supposed to make the catch unnecessary, not to become one", dumpDepth)
	}
	// THE STATUS AND THE POLARITY ARE IN THE LOG LINE ON PURPOSE. The line used to
	// carry offsets only, and offsets are exactly what an inversion and a status
	// change leave alone: both mutations were measured to keep it byte identical.
	t.Logf("check at +%d (negated: %t), refusal at +%d answering %d, dump at +%d; "+
		"%d Попытка blocks, dump at depth %d",
		probeAt, conditionIsNegated(lines[probeAt]), refusalAt, status, dumpAt, opened, dumpDepth)
}

// conditionIsNegated reports whether an `Если … Тогда` line negates its
// condition. Returns false for a line that is not such a condition at all.
func conditionIsNegated(line string) bool {
	cond, ok := ifCondition(line)
	return ok && strings.HasPrefix(cond, "НЕ ")
}

// ifCondition returns the text between Если and Тогда, uppercased so keyword
// tests do not depend on how the module happens to write them.
func ifCondition(line string) (string, bool) {
	s := strings.ToUpper(strings.TrimSpace(line))
	if !strings.HasPrefix(s, "ЕСЛИ ") {
		return "", false
	}
	i := strings.LastIndex(s, "ТОГДА")
	if i < 0 {
		return "", false
	}
	return strings.TrimSpace(s[len("ЕСЛИ "):i]), true
}

// refusalIsOnTheAbsentRightBranch reports whether the statement at refusalAt is
// reached when the check at probeAt says the right is MISSING.
//
// TWO SHAPES ARE CORRECT and both are accepted, because pinning the source line
// verbatim would fail on a rewrite that kept the meaning:
//
//	Если НЕ ПравоДоступа(…) Тогда  <refusal>  КонецЕсли;
//	Если ПравоДоступа(…) Тогда  …  Иначе  <refusal>  КонецЕсли;
//
// A shape it cannot read is reported as a FAILURE and never as a pass. A guard
// that returns "fine" for input it did not understand is the thing this whole
// file exists to stop.
func refusalIsOnTheAbsentRightBranch(lines []string, probeAt, refusalAt int) (bool, string) {
	if probeAt < 0 || refusalAt < 0 || probeAt >= len(lines) {
		return false, "the check or the refusal was not located at all"
	}
	cond, ok := ifCondition(lines[probeAt])
	if !ok {
		return false, "the rights check is not the condition of an `Если … Тогда`, so which " +
			"branch the refusal is on cannot be decided"
	}
	negated := strings.HasPrefix(cond, "НЕ ")

	// Walk to the КонецЕсли that closes this Если, noting an Иначе at its own
	// level. Nested conditions are counted so an inner Иначе is not mistaken for
	// this one.
	depth, elseAt, endAt := 0, -1, -1
	for i := probeAt; i < len(lines); i++ {
		s := strings.ToUpper(strings.TrimSpace(lines[i]))
		switch {
		case strings.HasPrefix(s, "КОНЕЦЕСЛИ"):
			if depth == 1 {
				endAt = i
			}
			depth--
		case strings.HasPrefix(s, "ИНАЧЕЕСЛИ"):
			if depth == 1 {
				return false, "the check is written with an ИначеЕсли chain, which this guard " +
					"cannot read; state the refusal as a plain Если/Иначе or teach the guard " +
					"the new shape"
			}
		case strings.HasPrefix(s, "ИНАЧЕ"):
			if depth == 1 && elseAt < 0 {
				elseAt = i
			}
		case strings.HasPrefix(s, "ЕСЛИ "):
			depth++
		}
		if endAt >= 0 {
			break
		}
	}
	if endAt < 0 {
		return false, "the `Если` carrying the rights check is never closed by a КонецЕсли " +
			"inside the function"
	}

	thenEnd := endAt
	if elseAt >= 0 {
		thenEnd = elseAt
	}
	inThen := refusalAt > probeAt && refusalAt < thenEnd
	inElse := elseAt >= 0 && refusalAt > elseAt && refusalAt < endAt

	switch {
	case negated && inThen:
		return true, ""
	case !negated && inElse:
		return true, ""
	case negated:
		return false, "the condition is negated, so its Тогда branch is the one taken when the " +
			"right is missing, and the refusal does not stand there"
	case inThen:
		return false, "the condition is NOT negated, so its Тогда branch is taken when the " +
			"right is PRESENT, and the refusal stands there: the account that has the right " +
			"is refused and the account that lacks it is served"
	default:
		return false, "the condition is not negated and the refusal is not in an Иначе branch, " +
			"so nothing refuses a caller without the right"
	}
}

// TestUserFilterRefusalNamesNoCauseTheGateExcluded keeps the older 403 honest.
//
// Its text used to read «the account most likely lacks the Администрирование
// right». With the gate above in front of it, only an account that HAS the right
// can reach that line, so the sentence would name the one cause that has just
// been ruled out. A remedy is not improved by being reachable; it is improved by
// being true where it is reached.
func TestUserFilterRefusalNamesNoCauseTheGateExcluded(t *testing.T) {
	body := strings.Join(eventLogFunctionLines(t), "\n")

	// CONTROL: the branch is still there at all.
	if !strings.Contains(body, `ОтветОшибка(403, "user filter cannot be resolved`) {
		t.Fatal("CONTROL: the user-filter 403 is gone, so there is no text to be true or false")
	}
	if strings.Contains(body, "the account most likely lacks the") {
		t.Error("the user-filter 403 still blames the Администрирование right, which the check " +
			"at the top of the handler has already proved the caller HAS")
	}
}

// TestRightsPolarityCheckCanFail is the positive control for the polarity
// checker.
//
// refusalIsOnTheAbsentRightBranch is a predicate, and the guard above only ever
// asks it for agreement. A predicate that answered true for everything would
// make that guard green against the very mutation it was added for, which is the
// shape of the defect it repairs. So it is driven here against bodies whose
// answer is known, INCLUDING the inverted one, and the false cases must come
// back false.
func TestRightsPolarityCheckCanFail(t *testing.T) {
	const (
		probeLine    = `    Если НЕ ПравоДоступа("Администрирование", Метаданные) Тогда`
		invertedLine = `    Если ПравоДоступа("Администрирование", Метаданные) Тогда`
		refuseLine   = `        Возврат ОтветОшибка(403, "reading the event log requires the Администрирование right ");`
		endLine      = `    КонецЕсли;`
	)

	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"negated, refusal in Тогда", []string{probeLine, refuseLine, endLine}, true},
		{"not negated, refusal in Иначе",
			[]string{invertedLine, `        Продолжаем;`, `    Иначе`, refuseLine, endLine}, true},
		// THE MUTATION THAT SLIPPED THROUGH. Same lines, same order, same
		// offsets; only the НЕ is gone.
		{"INVERTED: not negated, refusal in Тогда",
			[]string{invertedLine, refuseLine, endLine}, false},
		{"negated but refusal in Иначе",
			[]string{probeLine, `        Продолжаем;`, `    Иначе`, refuseLine, endLine}, false},
		{"refusal after the КонецЕсли", []string{probeLine, `        Продолжаем;`, endLine,
			refuseLine}, false},
		{"no КонецЕсли at all", []string{probeLine, refuseLine}, false},
		{"the check is not a condition",
			[]string{`    Право = ПравоДоступа("Администрирование", Метаданные);`, refuseLine}, false},
		{"ИначеЕсли chain the guard cannot read",
			[]string{invertedLine, `        Продолжаем;`, `    ИначеЕсли Истина Тогда`, refuseLine,
				endLine}, false},
		// A nested Если must not be mistaken for this one's Иначе.
		{"nested Если with its own Иначе",
			[]string{probeLine, `        Если Истина Тогда`, `            Ничего;`, `        Иначе`,
				`            Ничего;`, `        КонецЕсли;`, refuseLine, endLine}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refusalAt := -1
			for i, l := range c.lines {
				if strings.Contains(l, rightsRefusalText) {
					refusalAt = i
					break
				}
			}
			if refusalAt < 0 {
				t.Fatal("CONTROL: the fixture carries no refusal, so the case measures nothing")
			}
			got, why := refusalIsOnTheAbsentRightBranch(c.lines, 0, refusalAt)
			if got != c.want {
				t.Errorf("refusalIsOnTheAbsentRightBranch = %t, want %t (%s)", got, c.want, why)
			}
			if !got && why == "" {
				t.Error("a false verdict came back with no reason, so a failure would print nothing")
			}
		})
	}

	// The negation reader must discriminate, or every case above agrees by
	// accident.
	if !conditionIsNegated(probeLine) {
		t.Error("conditionIsNegated does not see the НЕ in the shipped condition")
	}
	if conditionIsNegated(invertedLine) {
		t.Error("conditionIsNegated reports a plain condition as negated, so the inversion is " +
			"invisible to it")
	}
	if conditionIsNegated(refuseLine) {
		t.Error("conditionIsNegated reports a line that is not a condition as negated")
	}

	// And the status reader must be able to read a status other than 403, or a
	// status change would look like a missing call.
	m := answerStatusRE.FindStringSubmatch(`Возврат ОтветОшибка(500, "что угодно");`)
	if m == nil || m[1] != "500" {
		t.Errorf("answerStatusRE cannot read a 500 site: %v", m)
	}
}
