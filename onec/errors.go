package onec

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Тела ответа, которые может нести не 200.
//
// Различение существует потому, что echo сырого тела в ответ модели небезопасно:
// на IIS страница такого класса несёт физические пути и учётную запись, под
// которой крутится пул, а .github/ISSUE_TEMPLATE/bug_report.md просит вставить
// логи в публичный issue. Конверт расширения безопасен, потому что расширение
// строит его из ОписаниеОшибки(), и это единственный текст, который
// действительно является диагностикой 1С.
const (
	// BodyKindExtension — тело разобралось как конверт расширения {"error":"…"}.
	BodyKindExtension = "extension"
	// BodyKindForeign — тело конвертом расширения не является. Источник по нему
	// не определить: это может быть страница веб-сервера, прокси или самой
	// платформы, поэтому наружу оно не отдаётся.
	BodyKindForeign = "foreign"
)

// maxErrorBodyBytes — потолок чтения тела у ответа, отличного от 200.
//
// Прежние 4096 не имели записанного обоснования нигде в репозитории и резали по
// смещению в байтах, поэтому кириллический текст ошибки обрывался на середине
// руны. Потолок поднят и живёт здесь, рядом с trimPartialRune и StatusError,
// потому что решает он ровно одно: хватит ли прочитанного, чтобы конверт
// расширения разобрался. Наружу тело не отдаётся вовсе (BodyKindForeign), а
// диагностика расширения печатается через Detail.
//
// Это не стоит соединений: do ставит req.Close = true, то есть соединение
// закрывается после каждого запроса в любом случае. И это 1/2048 бюджета,
// который та же функция принимает на успешном ответе (DefaultMaxResponseSizeMiB).
const maxErrorBodyBytes = 65536

// StatusError — ответ 1С с кодом, отличным от 200.
//
// Класс тела назван полем, а не угадывается по тексту в месте показа: место
// показа не знает ни Content-Type, ни того, разобрался ли конверт, а решение
// «показывать текст или не показывать» без этого принять нельзя.
type StatusError struct {
	StatusCode int
	// Endpoint — метод HTTP-сервиса, например "/query". Приходит от Get и Post,
	// потому что ниже по стеку его уже никто не знает.
	Endpoint string
	// Base — «схема://хост» клиента (displayBase). Ни учётных данных, ни строки
	// запроса, ни пути в нём нет по построению (см. displayBaseOf в urlcred.go).
	Base string
	// BodyKind — BodyKindExtension или BodyKindForeign.
	BodyKind string
	// Detail — значение ключа error из конверта расширения. Заполняется ТОЛЬКО
	// при BodyKind == BodyKindExtension.
	Detail string
	// RawBody — прочитанное тело, уже обрезанное по границе руны. Хранится
	// целиком, потому что выбрасывать диагностику нельзя, но наружу отдаётся
	// только через Detail.
	RawBody     string
	ContentType string
	// BodyBytes — сколько байт тела прочитано (после обрезки по границе руны).
	// Это не длина тела на той стороне: при Truncated == true оно длиннее.
	BodyBytes int
	// Truncated — тело оказалось длиннее потолка чтения maxErrorBodyBytes.
	Truncated bool
}

func (e *StatusError) Error() string {
	var b strings.Builder
	// Первые слова оставлены прежними намеренно: по ним ищут в журналах, и
	// server/errshape_test.go закрепляет именно этот текст на проводе.
	fmt.Fprintf(&b, "1C returned status %d", e.StatusCode)
	if e.Endpoint != "" {
		fmt.Fprintf(&b, " for %s", e.Endpoint)
	}
	if e.Base != "" {
		fmt.Fprintf(&b, " on %s", e.Base)
	}
	if e.BodyKind == BodyKindExtension {
		fmt.Fprintf(&b, ": %s", e.Detail)
		return b.String()
	}
	// Тело чужое, поэтому описывается классом, а не показывается.
	fmt.Fprintf(&b, ": response body is not an MCP extension envelope (content-type %q, %d bytes read",
		e.ContentType, e.BodyBytes)
	if e.Truncated {
		b.WriteString(", truncated")
	}
	b.WriteString(")")
	return b.String()
}

// TransportError — ответ от 1С не получен вовсе: соединение не установилось,
// оборвалось или истёк таймаут.
type TransportError struct {
	Base     string
	Endpoint string
	Err      error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("executing request to 1C for %s on %s: %v", e.Endpoint, e.Base, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// RequestError — запрос не удалось даже собрать: адрес, полученный из --base и
// имени метода, не является корректным URL.
type RequestError struct {
	Base     string
	Endpoint string
	Err      error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("creating request for %s on %s: %v", e.Endpoint, e.Base, e.Err)
}

func (e *RequestError) Unwrap() error { return e.Err }

// extensionEnvelopeDetail возвращает значение ключа error из конверта расширения.
//
// Требуется всё сразу: тело разбирается как объект JSON, ключ error есть, и его
// значение — НЕПУСТАЯ строка. Иначе тело считается чужим. Слабее нельзя: {} и
// {"error":""} разбираются, но диагностики не несут, а объявить их конвертом
// значит показать модели пустой блок вместо объяснения, почему она его не
// получила.
func extensionEnvelopeDetail(body []byte) (string, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return "", false
	}
	raw, ok := obj["error"]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	if s == "" {
		return "", false
	}
	return s, true
}

// trimPartialRune отбрасывает хвост, который остался от руны, разрезанной
// потолком чтения.
//
// Потолок режет по СМЕЩЕНИЮ В БАЙТАХ, а кириллица занимает два байта на руну,
// поэтому разрез попадает в середину руны в половине случаев. Недобитый хвост
// доезжает до encoding/json и выходит наружу как U+FFFD.
//
// Отбрасывается не более utf8.UTFMax-1 байт, и только те, которые сами по себе
// руной не являются: корректная запись U+FFFD декодируется как RuneError
// размером 3, а испорченный байт — как RuneError размером 1, и различие именно
// в размере.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size != 1 {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}
