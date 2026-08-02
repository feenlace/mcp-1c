package onec

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Отказ шлюза несёт ПРИЧИНУ, и причина есть весь смысл ответа.
//
// Конверт отказа приходит не от расширения 1С, а от шлюза, который стоит перед
// публикацией (издание Advanced ходит через него). Тело у него того же рода, что
// у конверта расширения: объект JSON, у которого ключ error несёт диагностику,
// написанную НАШЕЙ стороной, а не веб-сервером и не прокси. Разница только в
// том, что значением может быть не строка, а вложенный объект {"code","message"}.
//
// До этих проверок extensionEnvelopeDetail требовала, чтобы значение ключа error
// было ИМЕННО строкой, поэтому вложенная форма проваливалась в BodyKindForeign, и
// вместо причины отказа читатель получал «response body is not an MCP extension
// envelope». Причина терялась целиком: ни кода, ни текста.
//
// Проверки идут через настоящий Client и настоящий сервер, а не через
// extensionEnvelopeDetail напрямую: терялась причина на проводе, и закреплять
// надо именно провод.

// proxyDenialReasons — коды отказа, каждый из которых обязан дойти до читателя.
// Список закреплён поимённо, чтобы регрессия не могла пройти молча: пропажа
// любого из них роняет НАЗВАННЫЙ подтест.
var proxyDenialReasons = []struct {
	reason string
	status int
}{
	{"env_forbidden", http.StatusForbidden},
	{"prod_sudo_required", http.StatusForbidden},
	{"seat_limit_exceeded", http.StatusPaymentRequired},
	{"rate_limited", http.StatusTooManyRequests},
	{"upstream_unavailable", http.StatusBadGateway},
}

// TestStatusError_ProxyDenialReasonSurvives_ObjectForm закрепляет вложенную форму
// конверта: {"error":{"code":"…","message":"…"}}. Код отказа обязан доехать и до
// Detail, и до текста ошибки, потому что Error() это отдельный канал к модели
// (tools/form.go formServiceCallFailedNote кладёт его текст в ответ).
func TestStatusError_ProxyDenialReasonSurvives_ObjectForm(t *testing.T) {
	for _, c := range proxyDenialReasons {
		t.Run(c.reason, func(t *testing.T) {
			body := fmt.Sprintf(`{"error":{"code":%q,"message":"доступ не предоставлен"}}`, c.reason)
			srv := statusServer(t, c.status, "application/json", body)

			cl := NewClient(srv.URL, "", "")
			se := statusErrorFrom(t, callGet(t, cl, "/metadata"))

			if se.BodyKind != BodyKindExtension {
				t.Fatalf("BodyKind = %q, want %q: конверт отказа шлюза это не чужое тело",
					se.BodyKind, BodyKindExtension)
			}
			if !strings.Contains(se.Detail, c.reason) {
				t.Errorf("Detail = %q, want it to carry the reason %q", se.Detail, c.reason)
			}
			if !strings.Contains(se.Detail, "доступ не предоставлен") {
				t.Errorf("Detail = %q, want it to carry the message too", se.Detail)
			}
			got := se.Error()
			if !strings.Contains(got, c.reason) {
				t.Errorf("Error() = %q, want it to carry the reason %q", got, c.reason)
			}
			if !strings.Contains(got, fmt.Sprintf("%d", c.status)) {
				t.Errorf("Error() = %q, want it to carry the %d status", got, c.status)
			}
			// Обобщённая формулировка чужого тела означает, что причина потеряна.
			if strings.Contains(got, "MCP extension envelope") {
				t.Errorf("Error() = %q: отказ описан как чужое тело, причина потеряна", got)
			}
		})
	}
}

// TestStatusError_ProxyDenialReasonSurvives_CodeOnly закрепляет ту же форму без
// message: шлюз кладёт только код (так выглядят отказы без человеческого текста).
func TestStatusError_ProxyDenialReasonSurvives_CodeOnly(t *testing.T) {
	for _, c := range proxyDenialReasons {
		t.Run(c.reason, func(t *testing.T) {
			body := fmt.Sprintf(`{"error":{"code":%q}}`, c.reason)
			srv := statusServer(t, c.status, "application/json", body)

			cl := NewClient(srv.URL, "", "")
			se := statusErrorFrom(t, callGet(t, cl, "/query"))

			if se.BodyKind != BodyKindExtension {
				t.Fatalf("BodyKind = %q, want %q", se.BodyKind, BodyKindExtension)
			}
			if se.Detail != c.reason {
				t.Errorf("Detail = %q, want exactly the reason %q", se.Detail, c.reason)
			}
			if got := se.Error(); !strings.Contains(got, c.reason) {
				t.Errorf("Error() = %q, want it to carry the reason %q", got, c.reason)
			}
		})
	}
}

// TestStatusError_ProxyDenialReasonSurvives_StringForm закрепляет строковую форму
// {"error":"env_forbidden"}. Она разбиралась и раньше; проверка стоит здесь, чтобы
// расширение предиката не увело поведение строковой ветви.
func TestStatusError_ProxyDenialReasonSurvives_StringForm(t *testing.T) {
	for _, c := range proxyDenialReasons {
		t.Run(c.reason, func(t *testing.T) {
			body := fmt.Sprintf(`{"error":%q}`, c.reason)
			srv := statusServer(t, c.status, "application/json", body)

			cl := NewClient(srv.URL, "", "")
			se := statusErrorFrom(t, callGet(t, cl, "/metadata"))

			if se.BodyKind != BodyKindExtension {
				t.Fatalf("BodyKind = %q, want %q", se.BodyKind, BodyKindExtension)
			}
			if se.Detail != c.reason {
				t.Errorf("Detail = %q, want exactly %q", se.Detail, c.reason)
			}
			if got := se.Error(); !strings.Contains(got, c.reason) {
				t.Errorf("Error() = %q, want it to carry the reason %q", got, c.reason)
			}
		})
	}
}

// TestStatusError_DenialMessageOnlyIsAnEnvelope: объект без code, но с непустым
// message. Причина здесь и есть текст, и терять его не за что: строковая форма
// {"error":"текст"} принимает ровно такой же произвольный текст с той стороны.
func TestStatusError_DenialMessageOnlyIsAnEnvelope(t *testing.T) {
	srv := statusServer(t, http.StatusForbidden, "application/json",
		`{"error":{"message":"среда закрыта для этого пользователя"}}`)

	cl := NewClient(srv.URL, "", "")
	se := statusErrorFrom(t, callGet(t, cl, "/metadata"))

	if se.BodyKind != BodyKindExtension {
		t.Fatalf("BodyKind = %q, want %q", se.BodyKind, BodyKindExtension)
	}
	if se.Detail != "среда закрыта для этого пользователя" {
		t.Errorf("Detail = %q, want the message verbatim", se.Detail)
	}
}

// TestStatusError_ForeignBodiesStayForeign — вторая сторона того же предиката.
//
// Расширение предиката не должно превратить его в «принимать что угодно».
// Страница веб-сервера, ответ балансировщика и объект JSON без диагностики
// обязаны остаться чужими, а их байты не должны появиться в тексте ошибки.
//
// marker — строка из тела, которой в ответе быть НЕ ДОЛЖНО. Пустой marker
// означает, что в теле нечего искать (пустое тело, {}).
func TestStatusError_ForeignBodiesStayForeign(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		marker      string
	}{
		{"iis_html_page", "text/html",
			`<html><head><title>401.5 Authorization</title></head><body>C:\inetpub\wwwroot ПулПриложений1С</body></html>`,
			`C:\inetpub`},
		{"lb_502_html", "text/html",
			`<html><body><h1>502 Bad Gateway</h1><hr>nginx/1.24.0</body></html>`, "nginx/1.24.0"},
		{"plain_text_500", "text/plain", `internal error at backend-07`, "backend-07"},
		{"empty_body", "application/json", ``, ""},
		{"json_array", "application/json", `["env_forbidden"]`, "env_forbidden"},
		{"empty_object", "application/json", `{}`, ""},
		{"error_absent", "application/json", `{"status":"env_forbidden"}`, "env_forbidden"},
		{"error_empty_string", "application/json", `{"error":""}`, ""},
		{"error_blank_string", "application/json", `{"error":"   "}`, ""},
		{"error_null", "application/json", `{"error":null}`, ""},
		{"error_number", "application/json", `{"error":403}`, ""},
		{"error_array", "application/json", `{"error":["env_forbidden"]}`, "env_forbidden"},
		{"error_empty_object", "application/json", `{"error":{}}`, ""},
		{"error_object_blank_code", "application/json", `{"error":{"code":"   "}}`, ""},
		{"error_object_code_not_string", "application/json", `{"error":{"code":403}}`, ""},
		{"error_object_no_diagnostic", "application/json",
			`{"error":{"retry_after":30,"trace":"backend-07"}}`, "backend-07"},
		{"not_json_at_all", "application/json", `env_forbidden`, "env_forbidden"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := statusServer(t, http.StatusBadGateway, c.contentType, c.body)

			cl := NewClient(srv.URL, "", "")
			se := statusErrorFrom(t, callGet(t, cl, "/metadata"))

			if se.BodyKind != BodyKindForeign {
				t.Fatalf("BodyKind = %q, want %q: тело без диагностики нашей стороны остаётся чужим",
					se.BodyKind, BodyKindForeign)
			}
			if se.Detail != "" {
				t.Errorf("Detail = %q, want empty for a foreign body", se.Detail)
			}
			got := se.Error()
			if !strings.Contains(got, "MCP extension envelope") {
				t.Errorf("Error() = %q, want the foreign-body wording", got)
			}
			if c.marker != "" && strings.Contains(got, c.marker) {
				t.Errorf("Error() = %q: байты чужого тела (%q) попали наружу", got, c.marker)
			}
		})
	}
}

// TestProxyDenialProbeFindsTheReasonWhenPresent — положительный контроль к
// отрицательным проверкам выше. Без него «причина не найдена» могло бы значить,
// что искать её нечем: тот же поиск по той же строке обязан НАХОДИТЬ причину,
// когда она действительно на месте.
func TestProxyDenialProbeFindsTheReasonWhenPresent(t *testing.T) {
	srv := statusServer(t, http.StatusForbidden, "application/json",
		`{"error":{"code":"env_forbidden"}}`)

	cl := NewClient(srv.URL, "", "")
	se := statusErrorFrom(t, callGet(t, cl, "/metadata"))

	if !strings.Contains(se.Error(), "env_forbidden") {
		t.Fatalf("положительный контроль не сработал: Error() = %q не несёт причину, "+
			"значит отрицательные проверки выше ничего не доказывают", se.Error())
	}
	if strings.Contains(se.Error(), "MCP extension envelope") {
		t.Fatalf("положительный контроль: Error() = %q описан как чужое тело", se.Error())
	}
}
