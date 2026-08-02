package tools

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// ---------------------------------------------------------------------------
// A remedy must be true for EVERY cause that can produce the class it is shown
// for, not only for the cause whoever wrote it had in mind.
//
// remedyForeignBody claimed the request never reached the extension. That is one
// of its two causes. The other, which the extension's own source proves, is an
// exception raised inside a handler that had already been running: the platform
// then answers with a page of its own, the body is foreign by the same test, and
// the advice sent the operator to re-check a publication that had just worked.
//
// remedyUnreachable asked about --user and --password on the two classes where
// no HTTP exchange happened at all, so credentials cannot be the cause and a 401
// (which they would cause) is rendered somewhere else entirely.
// ---------------------------------------------------------------------------

const bslModule = "../extension/src/HTTPServices/MCPService/Ext/Module.bsl"

// TestEventLogRightsFailureRaisesInsideTheExtension is the source measurement
// the foreign-body remedy now rests on.
//
// It is a test rather than a comment because the claim is about a file this
// repository ships and can therefore go stale: move
// ВыгрузитьЖурналРегистрации inside a Попытка and the remedy's second cause
// stops existing for this endpoint, and someone should be told.
func TestEventLogRightsFailureRaisesInsideTheExtension(t *testing.T) {
	src, err := os.ReadFile(bslModule)
	if err != nil {
		t.Fatalf("reading the shipped extension module: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	start, end := -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(strings.TrimSpace(l), "Функция ЖурналРегистрацииPOST"):
			start = i
		case start >= 0 && end < 0 && strings.TrimSpace(l) == "КонецФункции":
			end = i
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("ЖурналРегистрацииPOST was not found in the shipped module; the remedy's " +
			"grounding cannot be checked and must not be trusted")
	}

	depth, dumpDepth, dumpSeen := 0, -1, false
	opened := 0
	for _, l := range lines[start : end+1] {
		s := strings.TrimSpace(l)
		switch {
		case s == "Попытка":
			depth++
			opened++
		case strings.HasPrefix(s, "КонецПопытки"):
			depth--
		case strings.Contains(s, "ВыгрузитьЖурналРегистрации("):
			dumpSeen, dumpDepth = true, depth
		}
	}
	if !dumpSeen {
		t.Fatal("ВыгрузитьЖурналРегистрации is not called in ЖурналРегистрацииPOST; the claim " +
			"about where it raises is about code that is not there")
	}
	if opened == 0 {
		t.Fatal("the walk found no Попытка at all in the function, so a nesting depth of 0 " +
			"proves nothing about the call being outside one")
	}
	if dumpDepth != 0 {
		t.Errorf("ВыгрузитьЖурналРегистрации is inside %d Попытка block(s); the remedy says an "+
			"exception there escapes the handler, and that is no longer true", dumpDepth)
	}
	t.Logf("ЖурналРегистрацииPOST opens %d Попытка blocks and calls ВыгрузитьЖурналРегистрации "+
		"at nesting depth %d", opened, dumpDepth)

	// The envelope claim: only ОтветОшибка builds {"error":…}, so an escaped
	// exception cannot arrive as an extension envelope and MUST land on the
	// foreign branch.
	if !strings.Contains(string(src), `Новый Структура("error", ТекстОшибки)`) {
		t.Error("ОтветОшибка no longer builds the {\"error\":…} envelope; the classification " +
			"the remedy assumes has moved")
	}
}

// TestRemedyUnreachableDoesNotBlameCredentials pins the swept sibling.
func TestRemedyUnreachableDoesNotBlameCredentials(t *testing.T) {
	// The remedy is reached from exactly these two classes and no other. Both
	// are driven, so the assertion is about what a caller sees rather than about
	// a constant nobody proved was used.
	classes := map[string]error{
		"transport": &onec.TransportError{Base: "http://server", Endpoint: "/query",
			Err: errors.New("connect: connection refused")},
		"request": &onec.RequestError{Base: "http://server", Endpoint: "/query",
			Err: errors.New("invalid control character in URL")},
	}
	for name, err := range classes {
		t.Run(name, func(t *testing.T) {
			text := renderFailure(headingQuery, err)
			if !strings.Contains(text, "Учётные данные тут ни при чём") {
				t.Errorf("the answer does not rule credentials out on a class they cannot "+
					"cause:\n%s", text)
			}
			if regexp.MustCompile(`Проверьте:[\s\S]*--user`).MatchString(text) {
				t.Errorf("the checklist still sends the operator to --user:\n%s", text)
			}
		})
	}

	// CONTROL: on the class credentials CAN cause, a 401 with a foreign body,
	// the advice about credentials is still given. Without this the assertions
	// above are satisfied by advice that never mentions credentials anywhere,
	// which would be a different wrong answer.
	text := renderFailure(headingQuery, &onec.StatusError{
		StatusCode: 401, BodyKind: onec.BodyKindForeign, ContentType: "text/html", BodyBytes: 40,
	})
	if !strings.Contains(text, "Учётные данные") {
		t.Errorf("a 401 no longer mentions credentials at all:\n%s", text)
	}
}

// TestRemedyForeignBodyCoversBothItsCauses states the property in the answer the
// model reads, not in the constant.
func TestRemedyForeignBodyCoversBothItsCauses(t *testing.T) {
	text := renderFailure(headingEventLog, &onec.StatusError{
		StatusCode: 500, BodyKind: onec.BodyKindForeign, ContentType: "text/html", BodyBytes: 512,
	})

	if strings.Contains(text, "до того, как управление дошло до расширения") {
		t.Errorf("the answer still asserts the request never reached the extension, which is "+
			"false for an exception raised inside a handler:\n%s", text)
	}
	if !strings.Contains(text, "прервался исключением") {
		t.Errorf("the second cause is not named:\n%s", text)
	}
	if !strings.Contains(text, "get_configuration_info") {
		t.Errorf("the answer offers no way to tell the two causes apart:\n%s", text)
	}
	if !strings.Contains(text, "Права учётной записи на саму операцию") {
		t.Errorf("the remedy for the second cause is missing:\n%s", text)
	}

	// CONTROL: the first cause is still addressed. A rewrite that swapped one
	// half-truth for the other would be no better than what it replaced.
	for _, want := range []string{"default.vrd", "401 и 403"} {
		if !strings.Contains(text, want) {
			t.Errorf("the original cause lost its advice (%q missing):\n%s", want, text)
		}
	}
}

// TestRemedyQueryRejectedNamesTheParameterCause covers the third sibling.
//
// ЗапросPOST wraps Новый Запрос, УстановитьПараметр and Выполнить in ONE Попытка,
// so a parameter the extension cannot convert produces the same 400 as a syntax
// error. validate_query does not substitute parameters, so it answers valid:true
// for that query, and the old text then sent the caller to «права доступа,
// блокировки или состояние базы» for a cause it had supplied itself.
func TestRemedyQueryRejectedNamesTheParameterCause(t *testing.T) {
	text := renderFailure(headingQuery, &onec.StatusError{
		StatusCode: 400, BodyKind: onec.BodyKindExtension,
		Detail: "Ошибка преобразования данных XML",
	})
	if !strings.Contains(text, "Параметры запроса") {
		t.Errorf("the parameter cause is not named:\n%s", text)
	}
	if !strings.Contains(text, "и параметров в запросе нет") {
		t.Errorf("the conclusion about validate_query is still stated unconditionally:\n%s", text)
	}

	// CONTROL: the claim is only made where it belongs. A non-query heading on
	// the same class must not carry query advice at all.
	other := renderFailure(headingMetadata, &onec.StatusError{
		StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: "что угодно",
	})
	if strings.Contains(other, "Параметры запроса") {
		t.Errorf("query advice leaked onto another tool's failure:\n%s", other)
	}
}
