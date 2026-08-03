package tools

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// ---------------------------------------------------------------------------
// ONE STATUS, TWO CAUSES, AND THE ADVICE FITTED ONLY ONE OF THEM.
//
// remedyQueryRejected sends the caller to check table names, syntax and query
// parameters. That is right for the 400 ЗапросPOST raises out of its Попытка,
// where Новый Запрос, УстановитьПараметр and Выполнить all live, because the
// query really is what 1С refused.
//
// It is wrong for the OTHER 400s the same handler now answers. Those fire before
// there is a query to talk about: the body did not parse, the body was not an
// object, there was no query key, limit was not a number. Rewriting the query
// cannot affect any of them, and the last paragraph of that remedy then sends the
// caller on to rights, locks and database state, which are further still from the
// cause.
//
// AND THE STATUS IS NEW. Those bodies used to escape as HTTP 500 text/plain with
// a module name and a line number, which never reached this renderer as an
// extension envelope at all. The commit that turned them into 400 {"error":…}
// routed them straight into advice written for something else.
//
// WHY THE FAR SIDE'S TEXT IS ALLOWED TO PICK THE ADVICE. It picks between two
// pieces of OUR text and nothing else: no branch here shows more of the answer,
// spends more, or trusts it further. A responder that forges "query is required"
// swaps one static checklist for another. What it must not do is go stale, so the
// set is pinned to the shipped module by the walk at the bottom of this file
// rather than to this comment.
// ---------------------------------------------------------------------------

// queryBodyFaultAnswers are the four the extension can answer with. The first
// carries ОписаниеОшибки() after the colon, so it is matched as a prefix.
var queryBodyFaultAnswers = []string{
	"request body is not valid JSON: Ошибка преобразования данных JSON",
	"request body must be a JSON object",
	"query is required",
	"limit must be a number",
}

// TestQueryBodyFaultGetsAdviceThatFitsIt is the defect.
func TestQueryBodyFaultGetsAdviceThatFitsIt(t *testing.T) {
	// COUNTS, because every assertion below lives inside the loop and a loop over
	// nothing satisfies all of them. Measured: with queryBodyFaultAnswers emptied
	// this test reported --- PASS with no subtest at all. Its two siblings in this
	// file and in cmd/mcp-1c/docs_base_url_test.go both state a floor; this one
	// did not.
	if len(queryBodyFaultAnswers) < 4 {
		t.Fatalf("only %d body fault answers are driven; the table has shrunk, and a walk over "+
			"nothing satisfies every check below", len(queryBodyFaultAnswers))
	}
	// AND THE FLOOR IS TIED TO THE SHIPPED CLASSIFIER, not only to a number. Every
	// prefix isQueryBodyFault redirects on must have an answer driven here,
	// otherwise a prefix added to queryBodyFaultPrefixes would ship with its
	// rendering unexercised while the count above stayed satisfied.
	for _, prefix := range queryBodyFaultPrefixes {
		covered := false
		for _, detail := range queryBodyFaultAnswers {
			if strings.HasPrefix(detail, prefix) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("isQueryBodyFault redirects %q, and no answer driven here starts with it, "+
				"so what a caller receives for that prefix is not measured anywhere", prefix)
		}
	}

	for _, detail := range queryBodyFaultAnswers {
		t.Run(detail, func(t *testing.T) {
			text := renderFailure(headingQuery, &onec.StatusError{
				StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: detail,
			})

			if strings.Contains(text, "Имена таблиц и полей") {
				t.Errorf("the answer sends the caller to check table names for a failure that "+
					"happened before the query was looked at:\n%s", text)
			}
			if strings.Contains(text, "права доступа, блокировках или состоянии базы") {
				t.Errorf("the answer ends by blaming rights, locks or database state for a body "+
					"that did not arrive:\n%s", text)
			}
			// It has to say something TRUE instead, not merely say less: the
			// cause is that what reached the extension is not what was sent.
			if !strings.Contains(text, "тело") {
				t.Errorf("the answer never mentions the request body, which is the whole of the "+
					"cause:\n%s", text)
			}
			if !strings.Contains(text, "перенаправл") {
				t.Errorf("the answer does not name the one mechanism that drops a POST body on "+
					"the way to a publication:\n%s", text)
			}
			// The extension's own words still reach the caller: they are how the
			// caller tells these four apart.
			if !strings.Contains(text, detail) {
				t.Errorf("the extension's diagnostic was dropped from the answer:\n%s", text)
			}
		})
	}
}

// TestGenuineQueryRejectionKeepsItsAdvice is the control.
//
// Without it every assertion above is satisfied by deleting remedyQueryRejected
// outright, which would take the advice away from the cause it was written for.
func TestGenuineQueryRejectionKeepsItsAdvice(t *testing.T) {
	for _, detail := range []string{
		"Ошибка в запросе: Таблица не найдена <<?>>Справочник.Нетути",
		"Несоответствие типов (Параметр номер 1)",
		"Only SELECT queries allowed",
	} {
		text := renderFailure(headingQuery, &onec.StatusError{
			StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: detail,
		})
		if !strings.Contains(text, "Имена таблиц и полей") {
			t.Errorf("a genuine query rejection (%q) lost the advice written for it:\n%s",
				detail, text)
		}
		if !strings.Contains(text, "Параметры запроса") {
			t.Errorf("a genuine query rejection (%q) lost the parameter cause:\n%s", detail, text)
		}
	}

	// And the body-fault advice must not leak onto it.
	text := renderFailure(headingQuery, &onec.StatusError{
		StatusCode: 400, BodyKind: onec.BodyKindExtension,
		Detail: "Ошибка в запросе: Таблица не найдена",
	})
	if strings.Contains(text, "перенаправл") {
		t.Errorf("body-fault advice leaked onto a genuine query rejection:\n%s", text)
	}
}

// TestQueryBodyFaultSetMatchesTheShippedModule is what keeps the split from
// going stale.
//
// A classifier keyed on the far side's text is only as good as the day it was
// written. This walks ЗапросPOST and requires every literal 400 it can answer
// with to be accounted for: either as a body fault this code redirects, or as one
// of the answers deliberately left with the query advice. A new literal fails the
// test rather than quietly picking the wrong remedy.
func TestQueryBodyFaultSetMatchesTheShippedModule(t *testing.T) {
	src, err := os.ReadFile(bslModule)
	if err != nil {
		t.Fatalf("reading the shipped extension module: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	start, end := -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(strings.TrimSpace(l), "Функция ЗапросPOST"):
			start = i
		case start >= 0 && end < 0 && strings.TrimSpace(l) == "КонецФункции":
			end = i
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("ЗапросPOST was not found in the shipped module; the split between the two " +
			"remedies cannot be checked and must not be trusted")
	}
	body := strings.Join(lines[start:end+1], "\n")

	// aboutTheQueryItself are the 400s that are NOT body faults and must keep
	// remedyQueryRejected. Listed explicitly so a new literal lands in neither
	// bucket and fails below.
	aboutTheQueryItself := map[string]bool{"Only SELECT queries allowed": true}

	// builtFromAnExpression are the 400 answers whose text is NOT a literal.
	// ОписаниеОшибки() is 1С's own diagnostic out of the Попытка around Новый
	// Запрос, УстановитьПараметр and Выполнить, so remedyQueryRejected is right
	// for it and it is not a body fault: by the time it can be raised the body has
	// parsed and the query has been read out of it.
	//
	// THEY ARE PINNED BECAUSE THEY USED TO BE INVISIBLE. The walk was a regexp
	// requiring a quote right after the comma, so it saw only same expression
	// literals and this call was not one of them. Measured: adding a second
	// expression built 400 to ЗапросPOST left this test passing and still
	// reporting «classified 5 literal 400 answers», because five literals were
	// still there and the floor only counted those.
	builtFromAnExpression := map[string]bool{"ОписаниеОшибки()": true}

	args := bslAnswerArguments(body, 400)
	literals, expressions := 0, 0
	firstLiteral := regexp.MustCompile(`^"([^"]*)"`)
	for _, a := range args {
		m := firstLiteral.FindStringSubmatch(a)
		if m == nil {
			expressions++
			if !builtFromAnExpression[a] {
				t.Errorf("ЗапросPOST can answer 400 with the expression %s, which this repository "+
					"has not classified. Its text is decided at run time, so isQueryBodyFault "+
					"cannot be read to find out which remedy it gets: say here which one is "+
					"right for it.", a)
			}
			continue
		}
		literals++
		text := m[1]
		switch {
		case isQueryBodyFault(text):
		case aboutTheQueryItself[text]:
		default:
			t.Errorf("ЗапросPOST can answer 400 %q, and this repository classifies it as neither "+
				"a body fault nor a statement about the query. It will be given "+
				"remedyQueryRejected by default, which may be the wrong advice.", text)
		}
	}
	// Both floors, because the two shapes fail independently: a broken literal
	// reader leaves the expression count intact and the other way round.
	if literals < 5 {
		t.Fatalf("the walk found %d literal 400 answers in ЗапросPOST; it used to find five, so "+
			"the extractor is broken and every check above passes on nothing", literals)
	}
	if expressions < 1 {
		t.Fatalf("the walk found %d expression built 400 answers in ЗапросPOST; it used to find "+
			"one, ОтветОшибка(400, ОписаниеОшибки()), so the extractor no longer sees the shape "+
			"it was widened for", expressions)
	}

	// CONTROL: the classifier is not simply true of everything. The extension's
	// own diagnostics, which come from ОписаниеОшибки() and are the reason
	// remedyQueryRejected exists, must not be swallowed by it.
	for _, notABodyFault := range []string{
		"Ошибка в запросе: Таблица не найдена",
		"Несоответствие типов (Параметр номер 1)",
		"Only SELECT queries allowed",
		"",
	} {
		if isQueryBodyFault(notABodyFault) {
			t.Errorf("isQueryBodyFault(%q) = true, so the classifier accepts anything", notABodyFault)
		}
	}
	t.Logf("classified %d 400 answers in ЗапросPOST: %d literal, %d built from an expression",
		len(args), literals, expressions)
}

// bslAnswerArguments returns the first argument of every ОтветОшибка(status, …)
// call in body, whatever shape that argument has.
//
// IT BALANCES PARENTHESES rather than reading to the first ")", because the one
// argument this walk was blind to is ОписаниеОшибки(), whose own ")" comes
// first. Quotes are tracked so a ")" inside a diagnostic does not close the
// call; BSL escapes a quote by doubling it, which toggles the flag off and
// straight back on and therefore cannot leave the scan inside a literal.
func bslAnswerArguments(body string, status int) []string {
	open := regexp.MustCompile(`ОтветОшибка\(\s*` + strconv.Itoa(status) + `\s*,\s*`)
	var out []string
	for pos := 0; pos < len(body); {
		loc := open.FindStringIndex(body[pos:])
		if loc == nil {
			break
		}
		start := pos + loc[1]
		depth, inString, i := 1, false, start
		for ; i < len(body); i++ {
			c := body[i]
			if inString {
				if c == '"' {
					inString = false
				}
				continue
			}
			switch c {
			case '"':
				inString = true
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if i >= len(body) {
			break
		}
		// Multi line concatenations are joined so the argument reads as one
		// expression; the literal reader below only looks at its first fragment.
		out = append(out, strings.Join(strings.Fields(body[start:i]), " "))
		pos = i
	}
	return out
}
