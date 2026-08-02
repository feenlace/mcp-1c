package tools

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A handler that reads a field straight off the parsed request body trusts the
// caller to have sent it. When the caller did not, 1С raises «Поле объекта не
// обнаружено» and the platform answers 500 text/plain with the module name and
// an internal line number in the body. That is three failures at once: the
// caller is told nothing actionable, the {"error":…} envelope every other
// rejection uses is not produced, and internal structure leaks to whoever can
// reach the endpoint.
//
// Measured on the authorised base at extension 0.4.7, BEFORE this guard:
//
//	POST /query          {}       -> 500 text/plain «Поле объекта не обнаружено (query)»
//	POST /validate-query {}       -> 500 text/plain «Поле объекта не обнаружено (query)»
//	POST /query          {…limit:"abc"} -> 500 text/plain «Операции сравнения…»
//	POST /eventlog       {}       -> 200 (the sibling that was already repaired)
//
// The rules below are the shape ЖурналРегистрацииPOST already satisfies, turned
// into something that fails for the handlers that do not:
//
//  1. every field read off the parsed body has a Свойство check for that field,
//  2. every such field compared as a number has a ТипЗнч check,
//  3. the РазобратьJSON call sits inside a Попытка, so a body that is empty or
//     is not JSON is rejected rather than escaping as a 500.
//
// This is a source walk, not a grep: the fields, the guards and the Попытка
// nesting are read per function, so a guard belonging to a different handler
// cannot satisfy this one.
// ---------------------------------------------------------------------------

type bslFunc struct {
	name  string
	lines []string
}

// bslFunctions splits the module into its top level functions.
func bslFunctions(src string) []bslFunc {
	var out []bslFunc
	var cur *bslFunc
	def := regexp.MustCompile(`^(?:Функция|Процедура)\s+([^\s(]+)`)
	for _, l := range strings.Split(src, "\n") {
		if m := def.FindStringSubmatch(l); m != nil {
			out = append(out, bslFunc{name: m[1]})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.TrimSpace(l) == "КонецФункции" || strings.TrimSpace(l) == "КонецПроцедуры" {
			cur = nil
			continue
		}
		cur.lines = append(cur.lines, l)
	}
	return out
}

// stripComments removes BSL // comments so that prose mentioning a field is not
// mistaken for code reading it.
func stripComments(l string) string {
	if i := strings.Index(l, "//"); i >= 0 {
		return l[:i]
	}
	return l
}

var (
	parseAssign = regexp.MustCompile(`([^\s=]+)\s*=\s*РазобратьJSON\(`)
	// A property read is <var>.<name> NOT followed by "(" — that excludes the
	// method calls Свойство(), Вставить(), Количество() without naming them.
	propRead = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_]*)\s*(.?)`)
)

func TestBodyReadingHandlersRejectAMissingFieldInsteadOfRaising(t *testing.T) {
	src, err := os.ReadFile(bslModule)
	if err != nil {
		t.Fatalf("reading the shipped extension module: %v", err)
	}
	funcs := bslFunctions(string(src))
	if len(funcs) == 0 {
		t.Fatal("no functions parsed out of the module; every assertion below would be vacuous")
	}

	checked, reads := 0, 0
	for _, f := range funcs {
		body := strings.Join(f.lines, "\n")
		m := parseAssign.FindStringSubmatch(body)
		if m == nil {
			continue // not a body reading handler
		}
		v := m[1]
		checked++

		// (3) the parse itself must be guarded.
		depth, parseDepth := 0, -1
		for _, raw := range f.lines {
			l := strings.TrimSpace(stripComments(raw))
			switch {
			case l == "Попытка":
				depth++
			case strings.HasPrefix(l, "КонецПопытки"):
				depth--
			}
			if strings.Contains(l, "РазобратьJSON(") && parseDepth < 0 {
				parseDepth = depth
			}
		}
		if parseDepth == 0 {
			t.Errorf("%s: РазобратьJSON is called outside any Попытка, so an empty or malformed "+
				"body escapes as HTTP 500 text/plain instead of a 400 {\"error\":…}", f.name)
		}

		// (1) and (2), per field.
		seen := map[string]bool{}
		for _, raw := range f.lines {
			l := stripComments(raw)
			idx := strings.Index(l, v+".")
			for idx >= 0 {
				rest := l[idx+len(v):]
				fm := propRead.FindStringSubmatch(rest)
				if fm != nil && fm[2] != "(" {
					seen[fm[1]] = true
				}
				next := strings.Index(l[idx+1:], v+".")
				if next < 0 {
					break
				}
				idx = idx + 1 + next
			}
		}
		for field := range seen {
			reads++
			if !strings.Contains(body, v+`.Свойство("`+field+`")`) {
				t.Errorf("%s: reads %s.%s with no %s.Свойство(%q) guard; a request whose JSON "+
					"object omits %q raises «Поле объекта не обнаружено» and the platform answers "+
					"500 text/plain with an internal line number",
					f.name, v, field, v, field, field)
			}
			numeric := strings.Contains(body, v+"."+field+" >") ||
				strings.Contains(body, v+"."+field+" <") ||
				strings.Contains(body, "Мин("+v+"."+field)
			if numeric && !strings.Contains(body, "ТипЗнч("+v+"."+field+")") {
				t.Errorf("%s: compares %s.%s as a number with no ТипЗнч(%s.%s) check; a non "+
					"numeric value raises «Операции сравнения…» and leaks the same 500",
					f.name, v, field, v, field)
			}
		}
	}

	// CONTROLS: the walk must have reached real handlers and real field reads,
	// otherwise passing means only that nothing was inspected.
	if checked < 3 {
		t.Fatalf("CONTROL: only %d body reading handlers found; the module defines three "+
			"(ЗапросPOST, ПроверкаЗапросаPOST, ЖурналРегистрацииPOST)", checked)
	}
	if reads == 0 {
		t.Fatal("CONTROL: no field read off a parsed body was detected at all, so the guard " +
			"above inspected nothing")
	}
	t.Logf("inspected %d body reading handlers and %d field reads", checked, reads)
}
