package extension

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// LIMIT BOUNDED THE ANSWER AND NOT THE READ, SO ASKING FOR LESS COST MORE.
//
// ВыгрузитьЖурналРегистрации takes five positional arguments:
//
//	ВыгрузитьЖурналРегистрации(<Приемник>, <Отбор>, <Колонки>, <ИмяВходногоФайла>,
//	                           <МаксимальноеКоличество>)
//
// ЖурналРегистрацииPOST used to pass TWO. Without the fifth the platform reads
// every record matching the filter into the ТаблицаЗначений and the handler then
// walks the first Лимит rows of it. Measured on a base holding more than 255 000
// records: no filter 71.5 s, limit:10 87.5 s. Asking for ten records cost SIXTEEN
// SECONDS MORE than asking for all of them, which is the shape of a bound applied
// after the work rather than to it.
// ---------------------------------------------------------------------------

// TestEventLogUnloadIsBoundedAtTheRead is the guard for the first defect.
//
// It asserts the ARITY and the POSITION, not merely that the limit appears
// somewhere in the line. Passing Лимит as the third argument would put it where
// Колонки goes and the platform would read it as a column list; passing it
// fourth would name it as an input file. Both are five-character edits away from
// correct and neither would be visible in the answer.
func TestEventLogUnloadIsBoundedAtTheRead(t *testing.T) {
	body := strings.Join(eventLogFunctionLines(t), "\n")

	args, ok := bslCallArguments(body, "ВыгрузитьЖурналРегистрации")
	if !ok {
		t.Fatal("CONTROL: ЖурналРегистрацииPOST does not call ВыгрузитьЖурналРегистрации, so " +
			"nothing below measures the read at all")
	}

	if len(args) != 5 {
		t.Fatalf("ВыгрузитьЖурналРегистрации is called with %d argument(s): %q.\n"+
			"The fifth, МаксимальноеКоличество, is what bounds the READ. Without it the platform "+
			"materialises every record matching the filter and the limit is applied afterwards, "+
			"so asking for ten records costs more than asking for all of them.",
			len(args), args)
	}

	// The limit has to be the LIMIT, and it has to be in the fifth position.
	if got := args[4]; got != "Лимит" {
		t.Errorf("the fifth argument is %q, not the limit the handler computed. "+
			"МаксимальноеКоличество is the only argument that bounds the read", got)
	}

	// Arguments three and four are reached by leaving them empty. If either
	// carries anything the limit is no longer in the fifth position even when the
	// count is five.
	for i, name := range []string{"Колонки", "ИмяВходногоФайла"} {
		if got := args[i+2]; got != "" {
			t.Errorf("argument %d (%s) is %q and was expected to be left empty; the handler asks "+
				"for no column subset and reads no file", i+3, name, got)
		}
	}

	// And the first two must still be what they were, or the call is bounded and
	// wrong.
	if args[0] != "ТаблицаЖурнала" || args[1] != "Отбор" {
		t.Errorf("the receiver and the filter are %q and %q", args[0], args[1])
	}

	t.Logf("ВыгрузитьЖурналРегистрации(%s)", strings.Join(args, ", "))
}

// TestEventLogTotalCountsTheAnswer pins what «total» counts.
//
// IT USED TO COUNT SOMETHING ELSE, and the change is deliberate. While the read
// was unbounded, ТаблицаЖурнала.Количество() was every record matching the filter
// inside the handler's window, and the answer carried at most Лимит of them. Once
// the read is bounded that number cannot exceed Лимит any more: the count and the
// number of records shown became the same quantity.
//
// A number that silently starts counting something else is worse than a number
// that is gone, so total is now taken FROM the array that ships. It says how many
// records this answer carries, it says it by construction rather than by
// coincidence, and there is no longer a second quantity for a reader to confuse
// it with.
//
// THERE IS NO CHEAP UNBOUNDED COUNT TO KEEP. Counting every matching record is
// what reading every matching record is for; on the base measured above that is
// the 71.5 s this change exists to remove. A second call to get the old number
// back would reinstate the defect in a field of its own.
func TestEventLogTotalCountsTheAnswer(t *testing.T) {
	body := strings.Join(eventLogFunctionLines(t), "\n")

	const insert = `Результат.Вставить("total"`
	i := strings.Index(body, insert)
	if i < 0 {
		t.Fatal("CONTROL: the answer carries no total at all, so there is nothing to pin")
	}
	line := body[i:]
	if j := strings.Index(line, "\n"); j >= 0 {
		line = line[:j]
	}

	if !strings.Contains(line, "МассивСобытий.Количество()") {
		t.Errorf("total is filled from %s.\nIt has to come from the array that ships, so that the "+
			"number and the records cannot disagree. Taken from the journal table it would count "+
			"rows the answer may not carry, and with the read now bounded it would silently mean "+
			"a third thing again.", strings.TrimSpace(line))
	}

	// AND THE OLD SOURCE MUST BE GONE. Leaving ВсегоЗаписей in place would keep a
	// second count next to the first with nothing to say which one ships.
	if strings.Contains(body, "ВсегоЗаписей") {
		t.Error("ЖурналРегистрацииPOST still computes ВсегоЗаписей. With the read bounded that " +
			"variable counts the same rows the array does, so it is a second name for one " +
			"quantity and the next reader has to work out which one the answer uses")
	}
}
