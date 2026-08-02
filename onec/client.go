package onec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Размер ответа измеряется в мебибайтах (MiB, 1 MiB = 1<<20 байт) везде:
// и в значении опции WithMaxResponseSize, и в тексте ошибки. Внутри клиент
// хранит уже пересчитанный лимит в байтах (поле maxResponseSize).

// DefaultMaxResponseSizeMiB — лимит размера ответа 1С по умолчанию, в мебибайтах.
// 128 MiB покрывает крупные базы с расширениями (например, ответ /extensions
// с большим base64 .cfe), оставаясь безопасным потолком против OOM.
const DefaultMaxResponseSizeMiB = 128

// DefaultRequestTimeout — таймаут HTTP-запроса к 1С по умолчанию.
// Подобран с запасом на передачу крупных ответов /extensions (сотни мегабайт):
// при коротком таймауте такая передача обрывалась бы вместо успешного чтения.
const DefaultRequestTimeout = 5 * time.Minute

const mib = 1 << 20

// Client is an HTTP client for communicating with 1C:Enterprise.
type Client struct {
	// BaseURL никогда не содержит учётных данных: NewClient снимает их с адреса
	// при создании (см. urlcred.go). Это поле читают и печатают, поэтому пароль
	// в нём означал бы пароль в тексте ошибки, в логе и в отчёте об ошибке.
	BaseURL    string
	User       string
	Password   string
	HTTPClient *http.Client

	// maxResponseSize — максимальный размер ответа 1С в байтах.
	// Ответ крупнее этого значения отбрасывается с понятной ошибкой.
	maxResponseSize int64

	// displayBase — «схема://хост» для показа человеку и модели. Собран только
	// из Scheme и Host, поэтому не может нести ни учётных данных, ни строки
	// запроса, ни пути. Вычисляется здесь, потому что здесь единственное место,
	// где адрес ещё разобран; читают его типизированные ошибки onec.
	displayBase string

	// sendAuth решает, отправлять ли Basic. Именно он, а не «User != \"\"»:
	// адрес http://:pw@host имеет ПУСТОЙ логин и сегодня аутентифицируется,
	// потому что net/http сам поднимает req.URL.User в заголовок
	// (/usr/local/go/src/net/http/client.go). Проверка по User молча лишила бы
	// такую базу аутентификации.
	sendAuth bool

	// baseErr — отказ, вынесенный при разборе адреса. Он проваливает КАЖДЫЙ
	// вызов и не содержит ни одного байта значения. Отказ живёт здесь, а не в
	// виде паники или os.Exit, потому что NewClient вызывают и с внутренними
	// схемами (proxy://, poll://), которые никто не вводил руками.
	baseErr error
}

// Option настраивает Client при создании через NewClient.
type Option func(*Client)

// WithMaxResponseSize задаёт лимит размера ответа 1С в мебибайтах (MiB).
// Значения <= 0 игнорируются — используется DefaultMaxResponseSizeMiB.
func WithMaxResponseSize(mibLimit int) Option {
	return func(c *Client) {
		if mibLimit > 0 {
			c.maxResponseSize = int64(mibLimit) * mib
		}
	}
}

// WithRequestTimeout задаёт таймаут HTTP-запроса к 1С.
// Значения <= 0 игнорируются — используется DefaultRequestTimeout.
func WithRequestTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.HTTPClient.Timeout = timeout
		}
	}
}

// MaxResponseSize возвращает действующий лимит размера ответа 1С в байтах.
// Возвращается уже пересчитанное значение в байтах (не MiB), чтобы вызывающий
// код мог напрямую использовать его в io.LimitReader и аналогичных API.
// Используется кодом, который выполняет «сырые» HTTP-запросы (минуя Get/Post)
// и должен соблюдать тот же лимит, что и встроенный декодер do().
func (c *Client) MaxResponseSize() int64 {
	return c.maxResponseSize
}

// NewClient creates a client for 1C HTTP service.
//
// Учётные данные берутся из ДВУХ источников, и порядок между ними тот же, что и
// раньше: флаги --user и --password побеждают, потому что SetBasicAuth ставит
// заголовок до отправки, и подъём userinfo средствами net/http в этом случае
// никогда не срабатывал.
//
// Адрес разбирается ЗДЕСЬ, на входе, а не в местах печати. Разбор в месте
// печати не помог бы: пароль остался бы в поле BaseURL, и следующий читатель
// этого поля потёк бы снова. Адрес, из которого учётные данные нельзя отделить
// однозначно, НЕ отвергается броском: NewClient обязан пережить внутренние
// схемы платных редакций, поэтому отказ запоминается в baseErr и проваливает
// каждый вызов. Отказ ПОЛЬЗОВАТЕЛЮ выносит граница флага в cmd/mcp-1c.
//
// Без опций используются значения по умолчанию: лимит ответа
// DefaultMaxResponseSizeMiB и таймаут DefaultRequestTimeout.
func NewClient(baseURL, user, password string, opts ...Option) *Client {
	res, err := SplitURLCredentials(baseURL)
	c := &Client{
		BaseURL:     res.Base,
		displayBase: res.Display,
		baseErr:     err,
		HTTPClient: &http.Client{
			Timeout: DefaultRequestTimeout,
		},
		maxResponseSize: int64(DefaultMaxResponseSizeMiB) * mib,
	}
	switch {
	case user != "":
		c.User, c.Password, c.sendAuth = user, password, true
	case res.HadUserinfo:
		c.User, c.Password, c.sendAuth = res.User, res.Password, true
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get performs a GET request to a 1C endpoint and decodes the JSON response.
func (c *Client) Get(ctx context.Context, endpoint string, result any) error {
	if c.baseErr != nil {
		return c.baseErr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+endpoint, nil)
	if err != nil {
		return &RequestError{Base: c.displayBase, Endpoint: endpoint, Err: ScrubbedURLError(err)}
	}
	return c.do(req, endpoint, result)
}

// Post performs a POST request to a 1C endpoint with a JSON body and decodes the JSON response.
func (c *Client) Post(ctx context.Context, endpoint string, body any, result any) error {
	if c.baseErr != nil {
		return c.baseErr
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return &RequestError{Base: c.displayBase, Endpoint: endpoint, Err: ScrubbedURLError(err)}
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, endpoint, result)
}

// do executes the request, checks the status, and decodes the JSON response.
//
// endpoint приходит сюда параметром, а не вынимается из req: адрес запроса это
// уже BaseURL+endpoint, и обратно имя метода из него не достать, не зная, какая
// часть пути принадлежит публикации базы.
func (c *Client) do(req *http.Request, endpoint string, result any) error {
	// sendAuth покрывает случай пустого логина; «c.User != \"\"» сохраняет прежнее
	// поведение для вызывающего, который присвоил экспортируемые поля уже после
	// создания клиента. Ни одного из двух условий по отдельности недостаточно.
	if c.sendAuth || c.User != "" {
		req.SetBasicAuth(c.User, c.Password)
	}
	req.Close = true // close connection after each request (avoids 1C session limit)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		// http.Client строит СВОЙ *url.Error из адреса запроса и маскирует в нём
		// только пароль, логин печатается целиком. После разделения на границе
		// в адресе учётных данных уже нет, но вызывающий мог присвоить BaseURL
		// напрямую, поэтому очистка остаётся.
		return &TransportError{Base: c.displayBase, Endpoint: endpoint, Err: ScrubbedURLError(err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.statusError(resp, endpoint)
	}

	// Лимит размера ответа защищает от OOM на неожиданно больших ответах.
	// Читаем не более limit+1 байт. Если декодер успешно разобрал JSON, всё
	// значение уместилось в бюджет — ответ корректен, и хвост (например,
	// завершающий перевод строки от HTTP-сервиса 1С) роли не играет.
	// Если декодер упал, причину определяем по числу прочитанных байт:
	// прочитали больше лимита => поток обрезан потолком, выдаём понятную
	// ошибку вместо невнятного «unexpected EOF» (Issue #19).
	counter := &countingReader{r: io.LimitReader(resp.Body, c.maxResponseSize+1)}
	decodeErr := json.NewDecoder(counter).Decode(result)

	// Дочитываем остаток (в пределах бюджета limit+1) ради переиспользования
	// соединения. На решение об успехе/ошибке это не влияет, ошибку чтения
	// игнорируем — это лишь очистка.
	_, _ = io.Copy(io.Discard, counter)

	if decodeErr == nil {
		return nil
	}
	if counter.read > c.maxResponseSize {
		return c.errResponseTooLarge()
	}
	return fmt.Errorf("decoding 1C response: %w", decodeErr)
}

// statusError строит *StatusError по ответу, отличному от 200.
//
// Читается на один байт больше потолка: это и есть признак того, что тело
// длиннее, и он честнее, чем сравнение с потолком после чтения ровно потолка.
// Хвост недобитой руны отбрасывается ЗДЕСЬ, до разбора конверта, потому что
// иначе он доехал бы и до json.Unmarshal, и до RawBody.
//
// Класс тела решается разбором, а не заголовком Content-Type: заголовок ставит
// та сторона, а конверт расширения либо разбирается, либо нет.
func (c *Client) statusError(resp *http.Response, endpoint string) *StatusError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes+1))
	truncated := len(raw) > maxErrorBodyBytes
	if truncated {
		raw = raw[:maxErrorBodyBytes]
	}
	raw = trimPartialRune(raw)

	e := &StatusError{
		StatusCode:  resp.StatusCode,
		Endpoint:    endpoint,
		Base:        c.displayBase,
		BodyKind:    BodyKindForeign,
		RawBody:     string(raw),
		ContentType: resp.Header.Get("Content-Type"),
		BodyBytes:   len(raw),
		Truncated:   truncated,
	}
	if detail, ok := extensionEnvelopeDetail(raw); ok {
		e.BodyKind, e.Detail = BodyKindExtension, detail
	}
	return e
}

// errResponseTooLarge returns a Russian, user-facing error explaining that the
// 1C response exceeded the configured size limit and how to raise the limit.
func (c *Client) errResponseTooLarge() error {
	limitMiB := c.maxResponseSize / mib
	return fmt.Errorf(
		"ответ 1С превысил лимит размера (%d MiB). "+
			"Увеличьте лимит флагом --max-response-size <МиБ> "+
			"или переменной окружения MCP_1C_MAX_RESPONSE_SIZE",
		limitMiB,
	)
}

// countingReader wraps a reader and counts the total number of bytes read.
type countingReader struct {
	r    io.Reader
	read int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.read += int64(n)
	return n, err
}
