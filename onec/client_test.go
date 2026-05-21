package onec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	c := NewClient("http://localhost:8080/1c-mcp", "", "")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.BaseURL != "http://localhost:8080/1c-mcp" {
		t.Fatalf("expected base URL, got %s", c.BaseURL)
	}
	if c.User != "" {
		t.Fatalf("expected empty user, got %s", c.User)
	}
}

// TestNewClientDefaults verifies the default response-size limit and timeout
// when no options are passed.
func TestNewClientDefaults(t *testing.T) {
	c := NewClient("http://localhost:8080/1c-mcp", "", "")
	if c.maxResponseSize != int64(DefaultMaxResponseSizeMiB)*mib {
		t.Errorf("expected default limit %d bytes, got %d", int64(DefaultMaxResponseSizeMiB)*mib, c.maxResponseSize)
	}
	if c.HTTPClient.Timeout != DefaultRequestTimeout {
		t.Errorf("expected default timeout %s, got %s", DefaultRequestTimeout, c.HTTPClient.Timeout)
	}
}

// TestClientOptions verifies that the functional options apply, and that
// non-positive values are ignored in favour of the defaults.
func TestClientOptions(t *testing.T) {
	c := NewClient("http://localhost:8080/1c-mcp", "", "",
		WithMaxResponseSize(64),
		WithRequestTimeout(90*time.Second),
	)
	if c.maxResponseSize != 64*mib {
		t.Errorf("expected 64 MiB limit, got %d bytes", c.maxResponseSize)
	}
	if c.HTTPClient.Timeout != 90*time.Second {
		t.Errorf("expected 90s timeout, got %s", c.HTTPClient.Timeout)
	}

	// Non-positive values must be ignored, keeping the defaults.
	c2 := NewClient("http://localhost:8080/1c-mcp", "", "",
		WithMaxResponseSize(0),
		WithRequestTimeout(0),
	)
	if c2.maxResponseSize != int64(DefaultMaxResponseSizeMiB)*mib {
		t.Errorf("expected default limit kept on zero option, got %d", c2.maxResponseSize)
	}
	if c2.HTTPClient.Timeout != DefaultRequestTimeout {
		t.Errorf("expected default timeout kept on zero option, got %s", c2.HTTPClient.Timeout)
	}
}

func TestClientGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"key":"value"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	var result map[string]string
	err := client.Get(context.Background(), "/test", &result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["key"] != "value" {
		t.Fatalf("expected value, got %s", result["key"])
	}
}

func TestClientBasicAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			t.Error("expected basic auth header")
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		if user != "admin" || pass != "secret" {
			t.Errorf("unexpected credentials: %s:%s", user, pass)
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "secret")
	var result map[string]any
	err := client.Get(context.Background(), "/auth", &result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientNoAuthWhenUserEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("expected no Authorization header when user is empty")
			http.Error(w, "unexpected auth header", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	var result map[string]any
	err := client.Get(context.Background(), "/noauth", &result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientGetError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "")
	var result map[string]string
	err := client.Get(context.Background(), "/test", &result)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

// extensionsBody builds an /extensions-shaped JSON payload whose total size is
// at least targetBytes. The bulk is a single base64-like string field, which
// mirrors a real 1C response carrying a large base64-encoded .cfe extension.
func extensionsBody(targetBytes int) []byte {
	var b strings.Builder
	b.Grow(targetBytes + 64)
	b.WriteString(`{"extensions":[{"name":"BigExt","data":"`)
	for b.Len() < targetBytes {
		b.WriteByte('A')
	}
	b.WriteString(`"}]}`)
	return []byte(b.String())
}

// exactExtensionsBody builds a valid /extensions-shaped JSON payload whose
// length is exactly jsonBytes. The "data" string field is padded with 'A's so
// that the full JSON value occupies precisely jsonBytes bytes.
func exactExtensionsBody(t *testing.T, jsonBytes int) []byte {
	t.Helper()
	const prefix = `{"extensions":[{"name":"BigExt","data":"`
	const suffix = `"}]}`
	pad := jsonBytes - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("jsonBytes %d too small for envelope", jsonBytes)
	}
	var b strings.Builder
	b.Grow(jsonBytes)
	b.WriteString(prefix)
	for i := 0; i < pad; i++ {
		b.WriteByte('A')
	}
	b.WriteString(suffix)
	if b.Len() != jsonBytes {
		t.Fatalf("built body of %d bytes, want %d", b.Len(), jsonBytes)
	}
	return []byte(b.String())
}

// newExtensionsServer serves the given body at /extensions.
func newExtensionsServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
}

// TestClientResponseExceedsLimit verifies that a response larger than the
// configured limit produces a clear, actionable error instead of a bare
// "unexpected EOF" (Issue #19, defect A).
func TestClientResponseExceedsLimit(t *testing.T) {
	// 4 MiB body, 2 MiB limit => over limit.
	body := extensionsBody(4 << 20)
	server := newExtensionsServer(t, body)
	defer server.Close()

	client := NewClient(server.URL, "", "", WithMaxResponseSize(2))
	var result map[string]any
	err := client.Get(context.Background(), "/extensions", &result)
	if err == nil {
		t.Fatal("expected an error for an over-limit response")
	}
	msg := err.Error()
	if strings.Contains(msg, "unexpected EOF") {
		t.Fatalf("error must not be a bare decode error, got: %v", err)
	}
	// The message must name the limit and tell the user how to raise it.
	for _, want := range []string{"2", "MiB", "--max-response-size", "MCP_1C_MAX_RESPONSE_SIZE"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message must mention %q, got: %v", want, err)
		}
	}
}

// TestClientResponseUnderLimit verifies that a response below the configured
// limit still decodes normally.
func TestClientResponseUnderLimit(t *testing.T) {
	// 1 MiB body, 8 MiB limit => under limit, must decode fine.
	body := extensionsBody(1 << 20)
	server := newExtensionsServer(t, body)
	defer server.Close()

	client := NewClient(server.URL, "", "", WithMaxResponseSize(8))
	var result map[string]any
	if err := client.Get(context.Background(), "/extensions", &result); err != nil {
		t.Fatalf("under-limit response must decode without error, got: %v", err)
	}
	exts, ok := result["extensions"].([]any)
	if !ok || len(exts) != 1 {
		t.Fatalf("expected one extension in decoded result, got: %#v", result["extensions"])
	}
}

// TestClientLimitIsConfigurable verifies that the limit is genuinely
// configurable: the same body is rejected with a small limit and accepted
// with a large one.
func TestClientLimitIsConfigurable(t *testing.T) {
	body := extensionsBody(3 << 20) // 3 MiB
	server := newExtensionsServer(t, body)
	defer server.Close()

	// Small limit (1 MiB) — rejected.
	small := NewClient(server.URL, "", "", WithMaxResponseSize(1))
	var r1 map[string]any
	if err := small.Get(context.Background(), "/extensions", &r1); err == nil {
		t.Fatal("expected rejection with a 1 MiB limit")
	}

	// Large limit (16 MiB) — accepted.
	large := NewClient(server.URL, "", "", WithMaxResponseSize(16))
	var r2 map[string]any
	if err := large.Get(context.Background(), "/extensions", &r2); err != nil {
		t.Fatalf("expected acceptance with a 16 MiB limit, got: %v", err)
	}
}

// TestClientMalformedResponseStillDecodeError verifies that a genuinely
// malformed (non-size-related) under-limit response still produces a normal
// decode error, not the over-limit error.
func TestClientMalformedResponseStillDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"extensions": [ this is not valid json`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "", WithMaxResponseSize(64))
	var result map[string]any
	err := client.Get(context.Background(), "/extensions", &result)
	if err == nil {
		t.Fatal("expected a decode error for malformed JSON")
	}
	if strings.Contains(err.Error(), "--max-response-size") {
		t.Fatalf("malformed-JSON error must not be the over-limit error, got: %v", err)
	}
}

// TestClientTrailingNewlineAtLimit guards against a false-positive in the
// over-limit detection: a valid response whose JSON value occupies exactly the
// configured limit, followed by a trailing newline (as 1C HTTP services
// commonly emit), must decode successfully. The trailing byte pushes the total
// to limit+1, but since Decode consumed the complete JSON value the response
// is not over the limit.
func TestClientTrailingNewlineAtLimit(t *testing.T) {
	// JSON value is exactly 1 MiB; append a trailing newline => 1 MiB + 1 byte.
	body := append(exactExtensionsBody(t, 1<<20), '\n')
	server := newExtensionsServer(t, body)
	defer server.Close()

	// Limit of 1 MiB covers the JSON exactly, not the trailing newline.
	client := NewClient(server.URL, "", "", WithMaxResponseSize(1))
	var result map[string]any
	if err := client.Get(context.Background(), "/extensions", &result); err != nil {
		t.Fatalf("response with a trailing newline at the limit must decode without error, got: %v", err)
	}
	exts, ok := result["extensions"].([]any)
	if !ok || len(exts) != 1 {
		t.Fatalf("expected one extension in decoded result, got: %#v", result["extensions"])
	}
}
