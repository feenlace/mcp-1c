package epf

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestWebOriginConfiguration(t *testing.T) {
	for _, origin := range []string{"", "http://127.0.0.1:59342", "http://localhost:59342", "http://[::1]:59342", "https://reader.example", "https://reader.example:8443"} {
		if _, err := New(token, nil, origin); err != nil {
			t.Fatalf("valid origin %q refused: %v", origin, err)
		}
	}
	for _, origin := range []string{"null", "*", "https://*.example", "http://reader.example", "https://reader.example/", "https://reader.example?", "https://reader.example?q=1", "https://reader.example#x", "https://u:p@reader.example", "https://reader.example https://other.example", "https://READER.example", "https://reader.example:0", "https://reader.example:65536", "https://reader.example:", "https://reader.example:443", "https://:443", "file://reader.example", "https://[foo]", "https://127.1", "http://127.0.0.1:80"} {
		if _, err := New(token, nil, origin); err == nil {
			t.Fatalf("invalid origin %q accepted", origin)
		}
	}
}

func TestBrowserBoundaryAndPreflight(t *testing.T) {
	const origin = "http://127.0.0.1:59342"
	b, err := New(token, nil, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	invoke := func(method, path, auth string, origins []string, preflight, headers, host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:9000"+path, strings.NewReader(`{"protocol":1,"identity":"native-owner"}`))
		r.Header.Set("Authorization", "Bearer "+auth)
		if origins != nil {
			r.Header["Origin"] = origins
		}
		if preflight != "" {
			r.Header.Set("Access-Control-Request-Method", preflight)
		}
		if headers != "" {
			r.Header.Set("Access-Control-Request-Headers", headers)
		}
		if host != "" {
			r.Host = host
		}
		w := httptest.NewRecorder()
		b.Handler("127.0.0.1:9000").ServeHTTP(w, r)
		return w
	}
	for _, origins := range [][]string{{"http://evil.example"}, {"null"}, {""}, {origin, origin}, {origin + ", http://evil.example"}} {
		if w := invoke("POST", "/pair", token, origins, "", "", ""); w.Code != 403 {
			t.Fatal("foreign/ambiguous origin accepted", w.Code)
		}
	}
	if b.session != "" {
		t.Fatal("refused origin changed session")
	}
	for _, route := range []struct{ path, method string }{{"/pair", "POST"}, {"/poll", "POST"}, {"/session", "DELETE"}, {"/result/0123456789abcdef0123456789abcdef", "POST"}} {
		w := invoke("OPTIONS", route.path, "", []string{origin}, route.method, "authorization, CONTENT-TYPE", "")
		if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != origin || w.Header().Get("Access-Control-Allow-Methods") != route.method || w.Header().Get("Access-Control-Allow-Credentials") != "" || w.Header().Get("Vary") != "Origin" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("valid preflight refused or broad CORS permission", w.Code)
		}
	}
	if b.session != "" {
		t.Fatal("preflight changed session")
	}
	for _, tc := range []struct{ path, method, headers, host string }{{"/pair", "GET", "authorization", ""}, {"/unknown", "POST", "authorization", ""}, {"/result/", "POST", "authorization", ""}, {"/result/not-an-id", "POST", "authorization", ""}, {"/pair", "POST", "X-Evil", ""}, {"/pair", "POST", "authorization,", ""}, {"/pair?x=1", "POST", "authorization", ""}, {"/pair?", "POST", "authorization", ""}, {"/pair", "POST", "authorization", "evil.example"}} {
		if w := invoke("OPTIONS", tc.path, "", []string{origin}, tc.method, tc.headers, tc.host); w.Code != 403 {
			t.Fatal("invalid preflight accepted", tc.path, w.Code)
		}
	}
	if w := invoke("OPTIONS", "/pair", "", nil, "POST", "authorization", ""); w.Code != 403 {
		t.Fatal("no-origin preflight accepted")
	}
	if w := invoke("POST", "/pair", "wrong", []string{origin}, "", "", ""); w.Code != 401 || w.Header().Get("Access-Control-Allow-Origin") != origin {
		t.Fatal("CORS bypassed auth or hid auth failure")
	}
	if w := invoke("POST", "/pair", token, []string{origin}, "", "", ""); w.Code != 200 {
		t.Fatal("browser pair refused", w.Code)
	}
	// Unconfigured listeners reject even a present empty Origin; native omission stays valid.
	plain, _ := New(token, nil)
	r := httptest.NewRequest("POST", "http://127.0.0.1:9000/pair", nil)
	r.Header["Origin"] = []string{""}
	w := httptest.NewRecorder()
	plain.Handler(r.Host).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("default empty Origin accepted")
	}
	_ = pair(t, plain)
}

func TestBrowserLateResultKeepsOriginalOwnership(t *testing.T) {
	const origin = "https://reader.example"
	b, _ := New(token, nil, origin)
	defer b.Close()
	session := pair(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, done := enqueue(t, b, ctx)
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 200 {
		t.Fatal("poll failed")
	}
	request(b, "DELETE", "/session", session, "")
	newer := pair(t, b)
	result := func(auth string) int {
		r := httptest.NewRequest("POST", "http://127.0.0.1:9000/result/"+p.task.ID, strings.NewReader(`{"identity":"native-owner","status":200,"body":{}}`))
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+auth)
		w := httptest.NewRecorder()
		b.Handler(r.Host).ServeHTTP(w, r)
		return w.Code
	}
	if result(newer) != 409 || result(session) != 204 {
		t.Fatal("browser result lost original session binding")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientTransportNativeSourceBoundary(t *testing.T) {
	data, err := os.ReadFile("source/MCPConnector/Forms/Main/Ext/Form/Module.bsl")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	start := strings.Index(s, "Функция CallBridge(")
	end := strings.Index(s[start:], "КонецФункции") + start
	bridge := s[start:end]
	for _, want := range []string{"Новый HTTPСоединение(\"127.0.0.1\", Порт, , , Неопределено, 2)", "#Если ВебКлиент Тогда", "Ждать Соединение.ВызватьHTTPМетодАсинх", "#Иначе", "Соединение.ВызватьHTTPМетод(", "BridgeJSON(Данные)"} {
		if !strings.Contains(bridge, want) {
			t.Fatal("client HTTP boundary missing", want)
		}
	}
	if !strings.Contains(s, "&НаСервере\nФункция РазобратьJSON") {
		t.Fatal("JSON parsing must use the available server API")
	}
	start = strings.Index(s, "Функция ExecuteTaskAtServer(")
	end = strings.Index(s[start:], "КонецФункции") + start
	server := s[start:end]
	if strings.Contains(server, "CallBridge(") || !strings.Contains(server, "ОжидаемаяИдентичность <> Identity()") || !strings.Contains(server, "ТекущаяУниверсальнаяДата() < TaskDate") {
		t.Fatal("server execution lost identity/deadline boundary or does HTTP")
	}
	for _, want := range []string{"Если SessionToken = \"\" ИЛИ RequestInFlight Тогда Возврат", "Ответ.КодСостояния = 200 И Поколение = ConnectionGeneration", "CallBridge(ИсходныйПорт, \"POST\", \"/result/\" + Задача.id, ИсходныйТокен, Результат)", "ConnectionGeneration = ConnectionGeneration + 1"} {
		if !strings.Contains(s, want) {
			t.Fatal("async lifecycle boundary missing", want)
		}
	}
}

// The common form schedules a single poll one second after connect/completion.
// Native timing and Stop/reconnect remain separate runtime acceptance cases.
func TestCommonFormPollSchedulingKeepsOneShotAndSessionFence(t *testing.T) {
	raw, err := os.ReadFile("source/MCPConnector/Forms/Main/Ext/Form/Module.bsl")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	schedule := `ПодключитьОбработчикОжидания("Poll", 1, Истина)`
	if strings.Count(s, schedule) != 2 || strings.Contains(s, `ПодключитьОбработчикОжидания("Poll", 0.5, Истина)`) {
		t.Fatal("poll must be scheduled once after connect and completed cycle")
	}
	start := strings.Index(s, "Процедура Poll(")
	if start < 0 {
		t.Fatal("Poll missing")
	}
	end := strings.Index(s[start:], "КонецПроцедуры")
	if end < 0 {
		t.Fatal("Poll end missing")
	}
	poll := s[start : start+end]
	release := strings.LastIndex(poll, "RequestInFlight = Ложь")
	rearm := strings.LastIndex(poll, schedule)
	if release < 0 || rearm < release || !strings.Contains(poll[release:rearm], `SessionToken <> "" И Поколение = ConnectionGeneration`) {
		t.Fatal("post-cycle rearm lost session/inflight fence")
	}
}
