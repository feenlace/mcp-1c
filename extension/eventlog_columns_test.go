package extension

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// THREE OF THE FOUR COLUMN NAMES THE HANDLER ASKED FOR DO NOT EXIST.
//
// ПолучитьКолонкуЖурнала answers "" for a column Колонки.Найти does not find, so
// a misspelling is indistinguishable from a record that has no value for that
// field. Nothing on the 1С side and nothing on the Go side can tell the two
// apart, so the three fields concerned came back empty on every record and no
// caller had any way to notice.
// ---------------------------------------------------------------------------

// eventLogColumns is the vocabulary ПолучитьКолонкуЖурнала may be asked for.
//
// WHERE THIS LIST COMES FROM, because a list of names typed from memory is the
// defect it is meant to catch. Two sources, both executed 2026-09-11 against real
// configuration dumps.
//
// FIRST, the columns 1С's own event log reads off the table
// ВыгрузитьЖурналРегистрации fills. In the standard library the common module
// ЖурналРегистрации fills СобытияЖурнала with that call, the form of the
// ЖурналРегистрации data processor loads the result with
// ЗначениеВДанныеФормы(СобытияЖурнала, Журнал), and every field it then binds is
// a DataPath of the form Журнал.<колонка>. That form carries 19 distinct such
// names, on a configuration whose declared compatibility mode is 8.3.24:
// ВспомогательныйIPПорт, Данные, Дата, ДатаНаСервере, ИмяПользователя,
// Комментарий, Компьютер, ОбластьДанных, ОсновнойIPПорт, ПредставлениеДанных,
// ПредставлениеМетаданных, ПредставлениеПриложения,
// ПредставлениеРазделенияДанныхСеанса, ПредставлениеСобытия, РабочийСервер,
// Сеанс, СтатусТранзакции, Транзакция, Уровень.
//
// THREE OF THE NINETEEN ARE LEFT OUT, because the common module makes them itself
// after the unload and they are therefore not names this handler could ask for:
// ДатаНаСервере is the unload's own Дата renamed out of the way, and ОбластьДанных
// and ПредставлениеРазделенияДанныхСеанса are added outright. Дата itself stays,
// because the unload does produce a column of that name; what the module adds
// under that name afterwards is a second one.
//
// SECOND, Событие, which that form binds nowhere and which the unload does have:
// the standard library names it in the Колонки argument of the same call, in five
// files of the same dump.
//
// The first source finds NO column named МетаданныеПредставление,
// ДанныеПредставление or ИдентификаторТранзакции, which is what this list exists
// to keep out. An anchored whole-identifier scan of the 53 917 .bsl and .xml files
// of that same dump finds the first two ZERO times anywhere in it, and the third
// 363 times, none of them in an event log: they belong to the DSS cryptography and
// the ЭДО subsystems, which is why the name looks plausible. The same scan over the
// same files finds ПредставлениеМетаданных 50 times and ПредставлениеДанных 1079
// times, so the two zeros are the scan working and not the scan being dead.
//
// THE LIST IS WHAT IS ESTABLISHED, NOT WHAT EXISTS. A column nothing here has had a
// reason to read is simply absent from it, so a new name has to be grounded the same
// way rather than typed in.
//
// ASKED OF THE PLATFORM ITSELF 2026-09-12, on one live 1С 8.3.27 base:
// ВыгрузитьЖурналРегистрации fills a table of 21 columns there, and all 17 above are
// among them. One base on one day, so this dates the list rather than closing it.
var eventLogColumns = []string{
	"ВспомогательныйIPПорт",
	"Данные",
	"Дата",
	"ИмяПользователя",
	"Комментарий",
	"Компьютер",
	"ОсновнойIPПорт",
	"ПредставлениеДанных",
	"ПредставлениеМетаданных",
	"ПредставлениеПриложения",
	"ПредставлениеСобытия",
	"РабочийСервер",
	"Сеанс",
	"Событие",
	"СтатусТранзакции",
	"Транзакция",
	"Уровень",
}

// bslCallArguments returns the top level arguments of the first call to name in
// src, or ok=false when there is no such call.
//
// It is a splitter and not a parser: it tracks nesting and string literals so a
// comma inside either is not a separator. An EMPTY argument comes back as an
// empty string, which is the whole point here, since the fifth argument is
// reached by writing two empty ones.
func bslCallArguments(src, name string) ([]string, bool) {
	i := strings.Index(src, name+"(")
	if i < 0 {
		return nil, false
	}
	rs := []rune(src[i+len(name)+1:])
	var args []string
	var cur strings.Builder
	depth, inString := 0, false
	for j := 0; j < len(rs); j++ {
		r := rs[j]
		if inString {
			cur.WriteRune(r)
			if r == '"' {
				if j+1 < len(rs) && rs[j+1] == '"' {
					cur.WriteRune('"')
					j++
					continue
				}
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
			cur.WriteRune(r)
		case '(':
			depth++
			cur.WriteRune(r)
		case ')':
			if depth == 0 {
				args = append(args, strings.TrimSpace(cur.String()))
				return args, true
			}
			depth--
			cur.WriteRune(r)
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(cur.String()))
				cur.Reset()
				continue
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	return nil, false
}

// TestEventLogAsksForColumnsTheTableHas is the guard for the second defect.
//
// ПолучитьКолонкуЖурнала returns "" when Колонки.Найти does not find the name, so
// a wrong name is silently empty and looks exactly like a record that has no
// value for that field. Nothing on the 1С side and nothing on the Go side can
// tell the two apart, which is why the check has to be on the NAME.
//
// The floor below is the count of fields the handler fills this way, and it moves
// with the handler: it was four, and event_presentation made it five.
func TestEventLogAsksForColumnsTheTableHas(t *testing.T) {
	body := strings.Join(eventLogFunctionLines(t), "\n")

	known := map[string]bool{}
	for _, c := range eventLogColumns {
		known[c] = true
	}

	// PREMISE: the vocabulary is populated. An empty list accepts every name.
	if len(eventLogColumns) < 10 {
		t.Fatalf("eventLogColumns holds %d names; the check is only as wide as this list",
			len(eventLogColumns))
	}
	// CONTROL: the membership test is a real lookup and not a map that agrees with
	// everything.
	if known["ЭтойКолонкиНет"] {
		t.Fatal("CONTROL: the vocabulary claims to hold a name that was never put in it")
	}

	asked := 0
	for _, line := range strings.Split(body, "\n") {
		args, ok := bslCallArguments(line, "ПолучитьКолонкуЖурнала")
		if !ok || len(args) != 3 {
			continue
		}
		name := strings.Trim(args[2], `"`)
		if name == args[2] {
			t.Errorf("ПолучитьКолонкуЖурнала is asked for %s, which is not a string literal; this "+
				"guard can only read a literal name", args[2])
			continue
		}
		asked++
		if !known[name] {
			t.Errorf("ЖурналРегистрацииPOST asks the journal table for a column named %q, and the "+
				"event log has no such column.\nПолучитьКолонкуЖурнала answers \"\" for a name "+
				"Колонки.Найти does not find, so this field is empty on every record and nothing "+
				"reports it.", name)
		}
	}

	// CONTROL: the walk found the calls. Every assertion above is satisfied by
	// finding none.
	if asked < 5 {
		t.Fatalf("CONTROL: only %d ПолучитьКолонкуЖурнала call(s) were read out of "+
			"ЖурналРегистрацииPOST; the handler fills five fields that way", asked)
	}
	t.Logf("checked %d column names against a vocabulary of %d", asked, len(eventLogColumns))
}

// TestBSLCallArgumentSplitterWorks is the positive control for the splitter both
// guards above rest on.
//
// Every assertion in them is of the form «the arguments are X». A splitter that
// returned nothing, or that returned the whole argument list as one string, would
// make some of those checks fail loudly and others pass by accident, so it is
// driven here against calls whose split is known.
func TestBSLCallArgumentSplitterWorks(t *testing.T) {
	cases := []struct {
		name string
		src  string
		call string
		want []string
	}{
		{"the two argument form this module used to ship",
			`    ВыгрузитьЖурналРегистрации(ТаблицаЖурнала, Отбор);`,
			"ВыгрузитьЖурналРегистрации", []string{"ТаблицаЖурнала", "Отбор"}},
		{"the five argument form with two empty positions",
			`    ВыгрузитьЖурналРегистрации(ТаблицаЖурнала, Отбор, , , Лимит);`,
			"ВыгрузитьЖурналРегистрации", []string{"ТаблицаЖурнала", "Отбор", "", "", "Лимит"}},
		{"a comma inside a nested call is not a separator",
			`    Ф(А, Мин(Б, В), Г);`, "Ф", []string{"А", "Мин(Б, В)", "Г"}},
		{"a comma inside a literal is not a separator",
			`    Ф(А, "б, в");`, "Ф", []string{"А", `"б, в"`}},
		{"an escaped quote does not end the literal",
			`    Ф("он сказал ""да"", и ушёл");`, "Ф", []string{`"он сказал ""да"", и ушёл"`}},
		{"a trailing empty argument is still an argument",
			`    Ф(А, );`, "Ф", []string{"А", ""}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := bslCallArguments(c.src, c.call)
			if !ok {
				t.Fatalf("the splitter found no call to %s in %q", c.call, c.src)
			}
			if len(got) != len(c.want) {
				t.Fatalf("split into %d argument(s) %q, want %d %q",
					len(got), got, len(c.want), c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("argument %d = %q, want %q", i+1, got[i], c.want[i])
				}
			}
		})
	}

	// AND IT MUST BE ABLE TO SAY NO. A splitter that reported a call for every
	// input would make the CONTROL lines in both guards above unable to fire.
	if _, ok := bslCallArguments(`    Возврат 1;`, "ВыгрузитьЖурналРегистрации"); ok {
		t.Error("the splitter reports a call in a line that has none")
	}
	if _, ok := bslCallArguments(`    ВыгрузитьЖурналРегистрации(А, Б`, "ВыгрузитьЖурналРегистрации"); ok {
		t.Error("the splitter accepts an argument list that is never closed")
	}
}
