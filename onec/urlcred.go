package onec

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// urlcred.go отделяет учётные данные от базового адреса 1С там, где адрес
// входит в процесс, чтобы ни одно поле, которое кто-то может прочитать или
// напечатать, никогда не хранило пароль.
//
// ПОЧЕМУ НА ГРАНИЦЕ, А НЕ В МЕСТЕ ПЕЧАТИ. Флаг --base принимает адрес, а
// --user и --password принимают учётные данные, но раздельность никогда не
// проверялась: cmd/mcp-1c/main.go кладёт значение флага в cfg.BaseURL как есть
// и передаёт его в onec.NewClient как BaseURL. Дальше строка склеивается с
// именем метода в onec/client.go, в Get и в Post, и попадает в
// *url.Error, чей Error() печатает %q сырого адреса
// (/usr/local/go/src/net/url/url.go:40). Отредактировать одно место печати
// недостаточно: пароль остался бы в структуре, и следующий читатель
// Client.BaseURL потёк бы снова.
//
// ЭТО НЕ МЁРТВЫЙ ПАРАМЕТР. Измерено на локальном слушателе: если в адресе
// есть userinfo и нет --user, net/http сам превращает userinfo в реальный
// заголовок Basic (/usr/local/go/src/net/http/client.go:251), то есть
// администраторы, записавшие учётные данные в адрес, действительно
// аутентифицировались ими. Поэтому учётные данные ПЕРЕНОСЯТСЯ в поля клиента,
// а не выбрасываются.
//
// ПОЛИТИКА: РАЗБОР ПО RFC 3986 СРЕДСТВАМИ net/url, ОТКАЗ ПРИ ЛЮБОЙ
// НЕОДНОЗНАЧНОСТИ. Ручной побайтовой хирургии здесь нет намеренно. Разбор
// «по символам» уже дал утечку: для адреса http://admin:p@ss/w0rd@host/hs
// RFC 3986 говорит, что authority кончается на первом «/», а userinfo это всё
// до ПОСЛЕДНЕГО «@» внутри authority, то есть логин admin, пароль p, хост ss,
// а хвост w0rd@host остаётся в пути. Разбор верный, замысел администратора
// другой, и выразить его без процентного кодирования нельзя. Поэтому здесь не
// угадывают замысел, а отказывают и объясняют, что делать.

// ErrURLCredentialUnstrippable возвращается для адреса, из которого учётные
// данные нельзя отделить однозначно. Сообщение не содержит ни одного байта
// самого адреса.
var ErrURLCredentialUnstrippable = errors.New(
	"адрес 1С содержит логин и пароль в форме, которую невозможно разобрать однозначно, поэтому он отклонён. " +
		"Уберите логин и пароль из адреса и задайте их флагами --user и --password. " +
		"Если они обязаны остаться в адресе, закодируйте в них служебные символы: @ как %40, / как %2F, ? как %3F, # как %23")

// ErrBaseURLHasQueryOrFragment возвращается для адреса с «?» или «#».
// Такой адрес нерабочий: onec/client.go в Get и в Post склеивает BaseURL с именем
// метода, а имя метода всегда начинается с «/», поэтому оно попадёт внутрь
// строки запроса. Всё, что стоит после «?», при этом осталось бы в адресе,
// который видит модель.
var ErrBaseURLHasQueryOrFragment = errors.New(
	"адрес 1С содержит ? или #, поэтому он отклонён. К адресу дописывается имя метода HTTP-сервиса, " +
		"поэтому всё, что стоит после ? или #, ломает запрос и остаётся в адресе, который видит модель. " +
		"Укажите корневой адрес HTTP-сервиса, например http://сервер/база/hs/mcp-1c")

// ErrBaseURLUnparsable возвращается для адреса, который net/url не разбирает и
// в котором нет «@», то есть речь не про учётные данные. Оборачивать исходную
// ошибку нельзя: *url.Error печатает %q сырого адреса.
var ErrBaseURLUnparsable = errors.New(
	"адрес 1С не удалось разобрать, поэтому он отклонён. " +
		"Проверьте, что адрес записан целиком и без пробелов, например http://сервер/база/hs/mcp-1c")

// BaseURLSplit — результат разбора базового адреса.
//
// При отказе возвращается НУЛЕВОЙ BaseURLSplit: в нём нет ни одного байта
// исходного адреса, поэтому его безопасно положить в любое поле и напечатать.
type BaseURLSplit struct {
	// Base — адрес без учётных данных. Для адреса без учётных данных это
	// исходная строка байт в байт.
	Base string
	// User и Password — учётные данные, снятые с адреса, уже раскодированные
	// (процентные последовательности разрешены) ровно так, как их разрешает
	// net/url, потому что именно в таком виде net/http положил бы их в
	// заголовок Authorization.
	User     string
	Password string
	// HadUserinfo сообщает, что в адресе были учётные данные и они сняты.
	// Именно это, а не «User != \"\"», решает, отправлять ли Basic: адрес
	// http://:pw@host сегодня даёт заголовок Basic OnB3 с пустым логином.
	HadUserinfo bool
	// Display — «схема://хост» для показа человеку и модели. Собирается
	// ТОЛЬКО из Scheme и Host разобранного адреса, поэтому не может содержать
	// ни userinfo (net/url кладёт её в отдельное поле User), ни строку
	// запроса, ни путь. Для отклонённого адреса пустая строка.
	Display string
}

// SplitURLCredentials разбирает базовый адрес 1С по RFC 3986 средствами
// net/url и отделяет от него учётные данные.
//
// Что принимается:
//   - адрес без «@», «?» и «#» — возвращается байт в байт, без учётных данных;
//     это то, что оставляет нетронутыми внутренние адреса вида proxy://Имя,
//     poll://local и пустую строку;
//   - адрес с ОДНИМ userinfo в authority и без «@» в пути — учётные данные
//     снимаются, остальное собирается обратно через net/url;
//     сюда попадает http://admin:p@ssw0rd@host/hs, потому что net/url
//     разрешает «@» внутри userinfo (url.go:1282-1292) и берёт последнюю «@».
//
// Что отклоняется:
//   - адрес, который net/url не разбирает (сырая кириллица в userinfo,
//     битая процентная последовательность, «/» «?» «#» в пароле);
//   - адрес без authority (opaque): «http:user:pass@host», «user:pass@host:8080»;
//   - адрес с «?» или «#» (см. ErrBaseURLHasQueryOrFragment);
//   - адрес, в пути которого осталась незакодированная «@»;
//   - адрес, в котором после снятия userinfo «@» всё ещё осталась.
//
// Функция никогда не возвращает частично очищенный адрес: либо Base не
// содержит учётных данных, либо возвращается ошибка и нулевой результат.
func SplitURLCredentials(raw string) (BaseURLSplit, error) {
	// Шаг 1. Строка без «@» не может нести userinfo: «@» это единственный
	// разделитель userinfo в RFC 3986, а «%40» разделителем не является. «?» и
	// «#» исключены здесь только для того, чтобы их увидел шаг 4. Возврат
	// исходной строки байт в байт — это то, что сохраняет работоспособность
	// уже настроенных адресов и внутренних схем.
	if !strings.ContainsAny(raw, "@?#") {
		return BaseURLSplit{Base: raw, Display: displayBaseOf(parseQuiet(raw))}, nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		// *url.Error печатает %q сырого адреса, а «invalid port \":pa\" after
		// host» печатает и кусок пароля. Исходную ошибку не возвращаем.
		if strings.ContainsRune(raw, '@') {
			return BaseURLSplit{}, ErrURLCredentialUnstrippable
		}
		return BaseURLSplit{}, ErrBaseURLUnparsable
	}

	// Шаг 2. Opaque значит, что после схемы не было «//», то есть authority
	// нет вовсе и снимать userinfo неоткуда.
	if parsed.Opaque != "" {
		return BaseURLSplit{}, ErrURLCredentialUnstrippable
	}

	// Шаг 3. Защита от того, чего net/url выдать не должен.
	if strings.ContainsRune(parsed.Host, '@') {
		return BaseURLSplit{}, ErrURLCredentialUnstrippable
	}

	// Шаг 4. «@» в пути. По RFC 3986 это законный символ пути, но в базовом
	// адресе 1С «@» имеет ровно одно осмысленное назначение: разделитель
	// userinfo. Именно «@» за пределами authority превращает
	// http://admin:p@ss/w0rd@host/hs и http://user:1234/passZ@host/hs в
	// нечто иное, чем имел в виду администратор. EscapedPath() отдаёт путь в
	// исходном виде, поэтому «%40» сюда не попадает.
	if strings.ContainsRune(parsed.EscapedPath(), '@') {
		return BaseURLSplit{}, ErrURLCredentialUnstrippable
	}

	// Шаг 5. «?» или «#». Проверяем сырую строку, а не поля: литеральный «?»
	// в адресе всегда разделитель, потому что validUserinfo его не пропускает
	// (/usr/local/go/src/net/url/url.go:1267-1300), а закодированный «%3F»
	// разделителем не является и сюда не попадёт.
	//
	// Какое из двух сообщений печатать, решает наличие userinfo, а не догадка
	// о содержимом строки запроса: если net/url нашёл userinfo, администратор
	// точно записал в адрес учётные данные, и это срочнее.
	if isDialedScheme(parsed.Scheme) && strings.ContainsAny(raw, "?#") {
		if parsed.User != nil {
			return BaseURLSplit{}, ErrURLCredentialUnstrippable
		}
		return BaseURLSplit{}, ErrBaseURLHasQueryOrFragment
	}

	if parsed.User == nil {
		// «@» была, но не в authority, не в пути и не в строке запроса.
		// Учётных данных нет, адрес отдаём байт в байт.
		return BaseURLSplit{Base: raw, Display: displayBaseOf(parsed)}, nil
	}

	user := parsed.User.Username()
	password, _ := parsed.User.Password()

	stripped := *parsed
	stripped.User = nil
	base := stripped.String()

	// Шаг 6. Обязательство доказать результат, а не предположить его.
	// Если после снятия userinfo «@» осталась, значит разбор был не тем, что
	// мы думали, и адрес отклоняется вместо того, чтобы уехать наружу.
	if strings.ContainsRune(base, '@') {
		return BaseURLSplit{}, ErrURLCredentialUnstrippable
	}

	return BaseURLSplit{
		Base:        base,
		User:        user,
		Password:    password,
		HadUserinfo: true,
		Display:     displayBaseOf(&stripped),
	}, nil
}

// CheckURLCredentialResidue — та же проверка ради одной только ошибки.
//
// У неё НЕТ собственных ветвлений намеренно. Прежняя версия начиналась с
// «if _, _, _, had := SplitURLCredentials(raw); had { return nil }», и этот
// возврат делал проверку структурно недостижимой ровно для той формы, ради
// которой она существовала. Здесь короткого замыкания нет и добавить его
// некуда: вердикт один и тот же объект.
func CheckURLCredentialResidue(raw string) error {
	_, err := SplitURLCredentials(raw)
	return err
}

// DisplayBase возвращает «схема://хост» для адреса, который прошёл проверку,
// и пустую строку для отклонённого. Отдельная функция нужна тем вызывающим,
// у кого нет BaseURLSplit под рукой.
func DisplayBase(raw string) string {
	res, err := SplitURLCredentials(raw)
	if err != nil {
		return ""
	}
	return res.Display
}

// displayBaseOf собирает строку показа ТОЛЬКО из Scheme и Host.
//
// Ни одна другая часть адреса в неё не попадает, поэтому она не может нести
// ни userinfo (net/url держит её в поле User, а не в Host), ни строку
// запроса, ни фрагмент, ни путь. Это структурное свойство, а не проверка.
func displayBaseOf(parsed *url.URL) string {
	if parsed == nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme == "" {
		return parsed.Host
	}
	return parsed.Scheme + "://" + parsed.Host
}

// parseQuiet разбирает адрес и возвращает nil вместо ошибки. Используется
// только для строки показа: адрес, который net/url не разбирает, показывать
// нечем, и это не повод отказывать в адресе без «@».
func parseQuiet(raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return parsed
}

// isDialedScheme сообщает, что по этому адресу onec.Client действительно
// пойдёт в сеть как по HTTP. Внутренние схемы (proxy://, poll://) перехватывает
// свой транспорт, и правило про «?» и «#» к ним не относится.
func isDialedScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "":
		return true
	}
	return false
}

// ScrubbedURLError убирает учётные данные из адреса, который несёт *url.Error.
//
// Нужна даже после разделения на границе: http.Client.Do строит свой
// *url.Error из URL запроса и маскирует только ПАРОЛЬ
// (/usr/local/go/src/net/http/client.go:1047), логин печатается целиком.
// А *url.Error от url.Parse не маскирует вообще ничего: он печатает %q сырого
// адреса (/usr/local/go/src/net/url/url.go:40).
//
// Если адрес очистить нельзя, из ошибки убирается адрес целиком: операция и
// причина остаются, адреса нет. Лучше потерять диагностику, чем напечатать
// пароль.
func ScrubbedURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	res, splitErr := SplitURLCredentials(ue.URL)
	if splitErr != nil {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	if !res.HadUserinfo {
		return err
	}
	return &url.Error{Op: ue.Op, URL: res.Base, Err: ue.Err}
}
