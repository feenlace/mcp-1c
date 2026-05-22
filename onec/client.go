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
	BaseURL    string
	User       string
	Password   string
	HTTPClient *http.Client

	// maxResponseSize — максимальный размер ответа 1С в байтах.
	// Ответ крупнее этого значения отбрасывается с понятной ошибкой.
	maxResponseSize int64
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
// When user is non-empty, basic auth is added to every request.
// Без опций используются значения по умолчанию: лимит ответа
// DefaultMaxResponseSizeMiB и таймаут DefaultRequestTimeout.
func NewClient(baseURL, user, password string, opts ...Option) *Client {
	c := &Client{
		BaseURL:  baseURL,
		User:     user,
		Password: password,
		HTTPClient: &http.Client{
			Timeout: DefaultRequestTimeout,
		},
		maxResponseSize: int64(DefaultMaxResponseSizeMiB) * mib,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get performs a GET request to a 1C endpoint and decodes the JSON response.
func (c *Client) Get(ctx context.Context, endpoint string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+endpoint, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	return c.do(req, result)
}

// Post performs a POST request to a 1C endpoint with a JSON body and decodes the JSON response.
func (c *Client) Post(ctx context.Context, endpoint string, body any, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, result)
}

// do executes the request, checks the status, and decodes the JSON response.
func (c *Client) do(req *http.Request, result any) error {
	if c.User != "" {
		req.SetBasicAuth(c.User, c.Password)
	}
	req.Close = true // close connection after each request (avoids 1C session limit)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request to 1C: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("1C returned status %d: %s", resp.StatusCode, string(body))
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
