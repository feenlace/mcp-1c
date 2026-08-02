package extension

import (
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

// TestEventLogRefusesACallerWithoutTheRight is the guard for the defect above.
//
// It asserts ORDER, not merely presence: a check that stands after the dump call
// would leave the 500 exactly where it was.
func TestEventLogRefusesACallerWithoutTheRight(t *testing.T) {
	lines := eventLogFunctionLines(t)

	const (
		probe   = `ПравоДоступа("Администрирование", Метаданные)`
		refusal = `Возврат ОтветОшибка(403,`
		dump    = "ВыгрузитьЖурналРегистрации("
	)

	probeAt, refusalAt, dumpAt := -1, -1, -1
	for i, l := range lines {
		s := strings.TrimSpace(l)
		if probeAt < 0 && strings.Contains(s, probe) {
			probeAt = i
		}
		if probeAt >= 0 && refusalAt < 0 && strings.Contains(s, refusal) {
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
		t.Fatal("the rights check is not followed by a 403 refusal, so whatever it decides, the " +
			"caller is not told")
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
	t.Logf("check at +%d, refusal at +%d, dump at +%d; %d Попытка blocks, dump at depth %d",
		probeAt, refusalAt, dumpAt, opened, dumpDepth)
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
