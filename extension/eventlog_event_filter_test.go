package extension

import (
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// AN EVENT NAME THE BASE DOES NOT KNOW MUST NOT BE DROPPED.
//
// ЖурналРегистрацииPOST already states the rule for every other filter: «отбор,
// который применить нельзя, ОТКЛОНЯЕТСЯ, а не отбрасывается». A dropped filter
// gives back the log selected by whatever is left, with a total counted over it,
// and that answer is indistinguishable from the answer to the question that was
// actually asked. On this endpoint the reader is investigating an incident, so a
// wrong answer that looks right is worse than a refusal.
//
// The failure this guards is concrete: the 1С event log window shows a phrase in
// Russian («Сеанс. Начало»), the filter key takes a technical identifier
// (_$Session$_.Start), and a reader who copies what the window shows types a name
// no base has. Everything below exists so that such a name stops the call.
//
// WHERE THE VOCABULARY COMES FROM. ПолучитьЗначенияОтбораЖурналаРегистрации
// answers a Структура whose Событие member is a Соответствие: the key is the
// event identifier, the value is its representation. Read 2026-09-11 out of a
// real configuration dump, in the ЖурналРегистрации data processor, and cited by
// the routine rather than by a line so that the next edit to that dump cannot
// silently invalidate it:
//
//   - form ЖурналРегистрации, ПриСозданииНаСервере, statement
//     ЗначенияОтбора = ПолучитьЗначенияОтбораЖурналаРегистрации("Событие").Событие;
//   - the same form, Функция ПредставлениеСобытия, statement
//     ПредставлениеСобытия = ЗначенияОтбора[Событие];
//     which indexes that answer BY the identifier and gets the phrase back;
//   - form РедакторСоставаСвойства, Процедура УстановитьПараметрыРедактора,
//     which walks the same answer as Для Каждого ЭлементСоответствия and reads
//     ЭлементСоответствия.Ключ and ЭлементСоответствия.Значение off it.
//
// It is the same list the event log window offers, so a name outside it is a name
// that window never proposed.
// ---------------------------------------------------------------------------

const (
	// eventFilterKeyInsert is how the filter reaches the platform.
	eventFilterKeyInsert = `Отбор.Вставить("Событие"`
	// eventVocabularyCall is where the set of legal names is read from.
	eventVocabularyCall = "ПолучитьЗначенияОтбораЖурналаРегистрации("
	// unknownEventRefusalText is the first fragment of the diagnostic an
	// unrecognised name is refused with.
	unknownEventRefusalText = "unknown event: "
	// eventPresentationColumn is the column the 1С event log window itself binds,
	// via the DataPath Журнал.ПредставлениеСобытия in the form that loads the
	// unloaded table. It is therefore the phrase a reader sees in that window.
	eventPresentationColumn = "ПредставлениеСобытия"
	// eventPresentationField is the name that phrase travels under.
	eventPresentationField = "event_presentation"
)

// eventLogOffsets returns the offset of the first line inside
// ЖурналРегистрацииPOST containing each marker, or -1.
func eventLogOffsets(t *testing.T, markers ...string) map[string]int {
	t.Helper()
	lines := eventLogFunctionLines(t)
	out := map[string]int{}
	for _, m := range markers {
		out[m] = -1
	}
	for i, l := range lines {
		s := strings.TrimSpace(l)
		for _, m := range markers {
			if out[m] < 0 && strings.Contains(s, m) {
				out[m] = i
			}
		}
	}
	return out
}

// TestEventLogFiltersByEvent pins that the event filter reaches the platform at
// all, and reaches it as the key the platform reads.
//
// The key is Событие. Inserting it under any other name leaves a Структура member
// ВыгрузитьЖурналРегистрации ignores, and the call then returns the log the
// caller did not ask for with no sign that anything was ignored.
func TestEventLogFiltersByEvent(t *testing.T) {
	const dump = "ВыгрузитьЖурналРегистрации("
	at := eventLogOffsets(t, `Параметры.Свойство("event")`, eventFilterKeyInsert, dump)

	// CONTROL: the walk is reading the real function. Every assertion below is
	// satisfied by an empty slice without it.
	if at[dump] < 0 {
		t.Fatal("CONTROL: ВыгрузитьЖурналРегистрации is not in the extracted body, so the walk " +
			"is not looking at ЖурналРегистрацииPOST")
	}

	if at[`Параметры.Свойство("event")`] < 0 {
		t.Fatal("ЖурналРегистрацииPOST never looks at event in the request body, so a caller " +
			"asking for one event gets every event")
	}
	if at[eventFilterKeyInsert] < 0 {
		t.Fatalf("no %s in ЖурналРегистрацииPOST. The platform reads the event filter under the "+
			"key Событие; under any other name the member is ignored and the unfiltered log "+
			"comes back looking like an answer", eventFilterKeyInsert)
	}
	if !(at[`Параметры.Свойство("event")`] < at[eventFilterKeyInsert] &&
		at[eventFilterKeyInsert] < at[dump]) {
		t.Errorf("the filter is not built between reading the parameter and the unload: "+
			"parameter at +%d, insert at +%d, ВыгрузитьЖурналРегистрации at +%d (offsets "+
			"inside the function). A filter inserted after the unload filters nothing",
			at[`Параметры.Свойство("event")`], at[eventFilterKeyInsert], at[dump])
	}
}

// TestEventLogRefusesAnEventTheBaseDoesNotKnow is the guard for the failure this
// whole filter is written around.
//
// It asserts four things, and each of them alone can be satisfied while the
// defect is present:
//
//   - the handler READS a vocabulary from the base. Without it there is nothing
//     to compare a name against and every name is accepted;
//   - it REFUSES a name outside that vocabulary, and does so with a diagnostic
//     of its own rather than the neighbouring refusals';
//   - the refusal stands BEFORE ВыгрузитьЖурналРегистрации. A check after the
//     unload has already paid for the wrong read;
//   - the refusal is a REFUSAL: nothing that builds the success payload may run
//     before it, so no partial answer can be mistaken for a filtered one.
func TestEventLogRefusesAnEventTheBaseDoesNotKnow(t *testing.T) {
	lines := eventLogFunctionLines(t)
	const dump = "ВыгрузитьЖурналРегистрации("

	vocabAt, refusalAt, dumpAt := -1, -1, -1
	for i, l := range lines {
		s := strings.TrimSpace(l)
		if vocabAt < 0 && strings.Contains(s, eventVocabularyCall) {
			vocabAt = i
		}
		if refusalAt < 0 && strings.Contains(s, unknownEventRefusalText) &&
			strings.Contains(s, "ОтветОшибка(") {
			refusalAt = i
		}
		if dumpAt < 0 && strings.Contains(s, dump) {
			dumpAt = i
		}
	}

	// CONTROL: the walk is reading the real function.
	if dumpAt < 0 {
		t.Fatal("CONTROL: ВыгрузитьЖурналРегистрации is not in the extracted body, so the walk " +
			"is not looking at ЖурналРегистрацииPOST")
	}

	if vocabAt < 0 {
		t.Fatalf("ЖурналРегистрацииPOST never calls %s, so it holds no list of the names this "+
			"base knows and cannot tell a misspelling from a name with no records. The filter "+
			"then either matches nothing silently or is dropped silently, and the caller cannot "+
			"tell which", eventVocabularyCall)
	}
	if refusalAt < 0 {
		t.Fatalf("no ОтветОшибка in ЖурналРегистрацииPOST carries %q. An event name the base "+
			"does not know is then answered with a log, and a log selected by everything except "+
			"the filter that was asked for reads exactly like the right answer",
			unknownEventRefusalText)
	}

	// The status is part of the contract. 400 says the caller chose the value and
	// can choose another; this is the one refusal on this endpoint that really is
	// about a value, so it must not borrow the status of the two that are not.
	m := answerStatusRE.FindStringSubmatch(strings.TrimSpace(lines[refusalAt]))
	if m == nil {
		t.Fatalf("the unknown event refusal at +%d is not an ОтветОшибка(<код>, …) call: %s",
			refusalAt, strings.TrimSpace(lines[refusalAt]))
	}
	status, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("the unknown event refusal answers %q, which is not a status", m[1])
	}
	if status != 400 {
		t.Errorf("the unknown event refusal answers %d, not 400. The caller chose this value and "+
			"can choose another, which is what 400 says and what %d does not", status, status)
	}

	if !(vocabAt < refusalAt && refusalAt < dumpAt) {
		t.Errorf("the refusal does not stand between the vocabulary read and the unload: "+
			"vocabulary at +%d, refusal at +%d, ВыгрузитьЖурналРегистрации at +%d (offsets "+
			"inside the function). A check after the unload has already read the wrong rows",
			vocabAt, refusalAt, dumpAt)
	}

	// It must be a refusal and not a partial answer.
	for i := 0; i < refusalAt && i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		for _, payload := range []string{`Результат.Вставить("events"`, `Результат.Вставить("total"`} {
			if strings.Contains(s, payload) {
				t.Errorf("%s runs at +%d, before the unknown event refusal at +%d, so the refusal "+
					"can carry part of an answer", payload, i, refusalAt)
			}
		}
	}
}

// TestEventLogAnswerCarriesTheReadableEventName pins the second half of the
// same problem.
//
// The filter takes a technical identifier and the answer used to carry only that
// identifier, so a reader handed _$Data$_.Update could not connect it to the
// phrase their own event log window shows, and had no way to arrive at a name
// the filter would accept. ПредставлениеСобытия is a column of the very table
// ВыгрузитьЖурналРегистрации fills: the form of the ЖурналРегистрации data
// processor binds it as the DataPath Журнал.ПредставлениеСобытия, which is the
// phrase that window prints.
func TestEventLogAnswerCarriesTheReadableEventName(t *testing.T) {
	body := strings.Join(eventLogFunctionLines(t), "\n")

	// CONTROL: the body is the real one.
	if !strings.Contains(body, "ВыгрузитьЖурналРегистрации(") {
		t.Fatal("CONTROL: the extracted body does not call ВыгрузитьЖурналРегистрации")
	}

	want := `Событие.Вставить("` + eventPresentationField + `",`
	if !strings.Contains(body, want) {
		t.Fatalf("the answer carries no %s field. The reader gets the technical identifier and "+
			"nothing that connects it to the phrase the 1С event log window shows, which is the "+
			"only name most readers have", eventPresentationField)
	}

	// And it must be filled from the column, not from anything else. A hand made
	// value would be a second source for a phrase the platform already has.
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, want) {
			continue
		}
		args, ok := bslCallArguments(line, "ПолучитьКолонкуЖурнала")
		if !ok || len(args) != 3 {
			t.Fatalf("%s is not filled by ПолучитьКолонкуЖурнала(Строка, ТаблицаЖурнала, "+
				"\"%s\"): %s", eventPresentationField, eventPresentationColumn,
				strings.TrimSpace(line))
		}
		if got := strings.Trim(args[2], `"`); got != eventPresentationColumn {
			t.Errorf("%s is filled from the column %q, and the phrase the event log window shows "+
				"is %q", eventPresentationField, got, eventPresentationColumn)
		}
		return
	}
	t.Fatalf("no line carries %s", want)
}
