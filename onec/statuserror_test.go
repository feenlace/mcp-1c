package onec

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// statusServer answers every path with the given status, content type and body,
// so a test cannot accidentally measure a 404 from a mux it forgot to register.
func statusServer(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// callGet drives the real client and returns whatever error came back.
func callGet(t *testing.T, c *Client, endpoint string) error {
	t.Helper()
	var out map[string]any
	return c.Get(context.Background(), endpoint, &out)
}

func statusErrorFrom(t *testing.T, err error) *StatusError {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error for a non 200 response, got nil")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As found no *StatusError in %#v (%v)", err, err)
	}
	return se
}

// TestStatusError_ExtensionEnvelope pins the one body class that is safe to
// echo: the extension builds it from ОписаниеОшибки(), so it is 1C's own
// diagnostic rather than some intermediary's page.
func TestStatusError_ExtensionEnvelope(t *testing.T) {
	const detail = `Поле не найдено "Номенклатура"`
	srv := statusServer(t, http.StatusBadRequest, "application/json; charset=utf-8",
		`{"error":"Поле не найдено \"Номенклатура\""}`)

	c := NewClient(srv.URL, "", "")
	se := statusErrorFrom(t, callGet(t, c, "/query"))

	if se.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", se.StatusCode, http.StatusBadRequest)
	}
	if se.BodyKind != "extension" {
		t.Errorf("BodyKind = %q, want %q", se.BodyKind, "extension")
	}
	if se.Detail != detail {
		t.Errorf("Detail = %q, want %q", se.Detail, detail)
	}
	if se.Endpoint != "/query" {
		t.Errorf("Endpoint = %q, want %q", se.Endpoint, "/query")
	}
	if want := DisplayBase(srv.URL); se.Base != want {
		t.Errorf("Base = %q, want the display base %q", se.Base, want)
	}
	if se.Truncated {
		t.Error("Truncated is set for a body of a few dozen bytes")
	}
	// The detail is the thing the model is supposed to read, so it has to be in
	// the error text as well.
	if !strings.Contains(se.Error(), detail) {
		t.Errorf("Error() = %q, want it to carry the 1C diagnostic", se.Error())
	}
}

// TestStatusError_MalformedEnvelopeDegradesNeverDiscards covers the shape older
// extensions produced: an envelope whose inner quotes were never escaped, so it
// is not JSON at all. Degrading to foreign is correct; losing the bytes is not.
func TestStatusError_MalformedEnvelopeDegradesNeverDiscards(t *testing.T) {
	const body = `{"error": "Поле не найдено "Номенклатура""}`
	srv := statusServer(t, http.StatusBadRequest, "application/json; charset=utf-8", body)

	c := NewClient(srv.URL, "", "")
	se := statusErrorFrom(t, callGet(t, c, "/query"))

	if se.BodyKind != "foreign" {
		t.Errorf("BodyKind = %q, want %q for a body that is not JSON", se.BodyKind, "foreign")
	}
	if se.Detail != "" {
		t.Errorf("Detail = %q, want it empty when the envelope did not parse", se.Detail)
	}
	if se.RawBody != body {
		t.Errorf("RawBody = %q, want the bytes verbatim %q", se.RawBody, body)
	}
	if se.BodyBytes != len(body) {
		t.Errorf("BodyBytes = %d, want %d", se.BodyBytes, len(body))
	}
}

// TestStatusError_EnvelopeShapesThatAreNotDiagnostics is the control that keeps
// the extension classification from being «the body was JSON». A body that
// parses but carries no diagnostic must not be announced as one.
func TestStatusError_EnvelopeShapesThatAreNotDiagnostics(t *testing.T) {
	for name, body := range map[string]string{
		"empty object":      `{}`,
		"empty error value": `{"error":""}`,
		// A value that is all whitespace carries no diagnostic either, and it
		// used to be admitted: three spaces are not the empty string, so the
		// envelope was announced as the extension's own and Detail became "   ",
		// which is the empty block this classification exists to avoid.
		"error is three spaces":        `{"error":"   "}`,
		"error is a tab and a newline": `{"error":"\t\n"}`,
		"error is a no-break space":    `{"error":"\u00a0"}`,
		"error is a number":            `{"error":42}`,
		"error is an object":           `{"error":{"text":"boom"}}`,
		"json array":                   `["error"]`,
		"other key":                    `{"message":"boom"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := statusServer(t, http.StatusBadRequest, "application/json", body)
			c := NewClient(srv.URL, "", "")
			se := statusErrorFrom(t, callGet(t, c, "/query"))
			if se.BodyKind != "foreign" {
				t.Errorf("BodyKind = %q for body %s, want %q", se.BodyKind, body, "foreign")
			}
			if se.Detail != "" {
				t.Errorf("Detail = %q for body %s, want it empty", se.Detail, body)
			}
		})
	}

	// POSITIVE CONTROL in the same test: a real envelope in the same driver is
	// still classified as one, so the loop above cannot be passing because
	// everything is foreign.
	srv := statusServer(t, http.StatusBadRequest, "application/json", `{"error":"boom"}`)
	c := NewClient(srv.URL, "", "")
	se := statusErrorFrom(t, callGet(t, c, "/query"))
	if se.BodyKind != "extension" || se.Detail != "boom" {
		t.Errorf("the control envelope was classified BodyKind=%q Detail=%q, want extension/boom", se.BodyKind, se.Detail)
	}
}

const (
	iisPhysicalPath = `C:\inetpub\wwwroot\1c\`
	iisServiceAcct  = `DOMAIN\svc_1c`
)

func iisErrorPage() string {
	return "<!DOCTYPE html><html><head><title>401 - Unauthorized</title></head><body>" +
		"<h2>401.5 Authorization failed by ISAPI/CGI application</h2>" +
		"<p>Physical Path: " + iisPhysicalPath + "default.vrd</p>" +
		"<p>Logon Method: Anonymous</p><p>Logon User: " + iisServiceAcct + "</p>" +
		"<!-- x --></body></html>"
}

// TestStatusError_ForeignBody covers what an on prem IIS actually answers. The
// page carries a physical path and the account the pool runs as, and the bug
// report template routes logs into a public issue, so the bytes must not travel
// with the error text.
func TestStatusError_ForeignBody(t *testing.T) {
	page := iisErrorPage()
	srv := statusServer(t, http.StatusUnauthorized, "text/html", page)

	c := NewClient(srv.URL, "", "")
	se := statusErrorFrom(t, callGet(t, c, "/metadata"))

	if se.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want %d", se.StatusCode, http.StatusUnauthorized)
	}
	if se.BodyKind != "foreign" {
		t.Errorf("BodyKind = %q, want %q", se.BodyKind, "foreign")
	}
	if se.Detail != "" {
		t.Errorf("Detail = %q, want it empty for a foreign body", se.Detail)
	}
	if se.ContentType != "text/html" {
		t.Errorf("ContentType = %q, want %q", se.ContentType, "text/html")
	}

	text := se.Error()
	for _, secret := range []string{iisPhysicalPath, iisServiceAcct, "<!-- x -->"} {
		// POSITIVE CONTROL first: the string really is in the body we captured,
		// so a miss below is an absence rather than a filter selecting nothing.
		if !strings.Contains(se.RawBody, secret) {
			t.Fatalf("premise broken: RawBody does not contain %q, so its absence from Error() proves nothing", secret)
		}
		if strings.Contains(text, secret) {
			t.Errorf("Error() carries %q from a foreign body:\n%s", secret, text)
		}
	}
	// The class has to be reported even though the bytes are not.
	for _, want := range []string{"401", "text/html"} {
		if !strings.Contains(text, want) {
			t.Errorf("Error() = %q, want it to name %q", text, want)
		}
	}
}

// TestStatusError_TruncationIsRuneSafe drives the read cap with a body far over
// it. The cap counts BYTES and Cyrillic is two bytes per rune, so the cut lands
// mid rune half the time; the table walks the cut across the rune grid so both
// phases are exercised in one run.
func TestStatusError_TruncationIsRuneSafe(t *testing.T) {
	const bodyBytes = 70000
	// One pad per possible phase of the cut against the rune grid.
	const padRows = utf8.UTFMax
	midRuneRows := 0

	for pad := 0; pad < padRows; pad++ {
		t.Run(strings.Repeat("a", pad)+"-pad", func(t *testing.T) {
			var b strings.Builder
			b.Grow(bodyBytes + 8)
			b.WriteString(strings.Repeat("a", pad))
			for b.Len() < bodyBytes {
				b.WriteString("Ж")
			}
			body := b.String()

			srv := statusServer(t, http.StatusInternalServerError, "text/plain", body)
			c := NewClient(srv.URL, "", "")
			se := statusErrorFrom(t, callGet(t, c, "/metadata"))

			if !se.Truncated {
				t.Fatalf("Truncated is false for a %d byte body against a %d byte cap", len(body), maxErrorBodyBytes)
			}
			if len(se.RawBody) > maxErrorBodyBytes {
				t.Errorf("RawBody is %d bytes, over the %d byte cap", len(se.RawBody), maxErrorBodyBytes)
			}
			if !utf8.ValidString(se.RawBody) {
				t.Errorf("RawBody is not valid UTF-8: the cut left a partial rune")
			}
			if se.BodyBytes != len(se.RawBody) {
				t.Errorf("BodyBytes = %d but RawBody is %d bytes", se.BodyBytes, len(se.RawBody))
			}

			// CONTROL: the untrimmed cut at this same offset. On the rows where
			// it is invalid, the trim is what made the assertion above true; on
			// the rows where it is already valid, the trim is a no op and
			// RawBody keeps every byte the cap allowed.
			untrimmed := body[:maxErrorBodyBytes]
			if utf8.ValidString(untrimmed) {
				if len(se.RawBody) != maxErrorBodyBytes {
					t.Errorf("the cut landed on a rune boundary, so RawBody should be %d bytes, got %d",
						maxErrorBodyBytes, len(se.RawBody))
				}
				return
			}
			midRuneRows++
			if len(se.RawBody) >= maxErrorBodyBytes {
				t.Errorf("the cut landed mid rune but RawBody still has %d bytes, so nothing was trimmed",
					len(se.RawBody))
			}
		})
	}

	// Without this the whole table could pass on four boundary aligned rows,
	// which would prove the trim was never asked to do anything. The count is
	// LOGGED as well as asserted, so any figure quoted about this table is a
	// figure the run printed.
	t.Logf("truncation table: rows=%d mid-rune cuts=%d cap=%d", padRows, midRuneRows, maxErrorBodyBytes)
	if midRuneRows == 0 {
		t.Error("no row in the table produced a mid rune cut, so this test exercised nothing")
	}
}

// TestStatusError_ErrorTextNamesTheDisplayBaseAndNeverACredential is the plan's
// «carries no base» check, named after what it asserts. The base is the DISPLAY
// base, which cannot carry userinfo by construction, and the raw body of a
// foreign response never travels with the text.
func TestStatusError_ErrorTextNamesTheDisplayBaseAndNeverACredential(t *testing.T) {
	const (
		user = "svcadmin7"
		pass = "p4ssw0rdZZ"
	)
	page := iisErrorPage()
	srv := statusServer(t, http.StatusUnauthorized, "text/html", page)

	withCreds := strings.Replace(srv.URL, "http://", "http://"+user+":"+pass+"@", 1)
	c := NewClient(withCreds, "", "")
	if c.baseErr != nil {
		t.Fatalf("premise broken: the base was refused: %v", c.baseErr)
	}
	se := statusErrorFrom(t, callGet(t, c, "/metadata"))

	text := se.Error()
	if want := DisplayBase(srv.URL); !strings.Contains(text, want) {
		t.Errorf("Error() = %q, want it to name the display base %q", text, want)
	}
	if !strings.Contains(text, "/metadata") {
		t.Errorf("Error() = %q, want it to name the endpoint", text)
	}
	for _, secret := range []string{user, pass} {
		if strings.Contains(text, secret) {
			t.Errorf("Error() carries the credential %q:\n%s", secret, text)
		}
	}
	if strings.Contains(text, se.RawBody) {
		t.Errorf("Error() carries the whole foreign body:\n%s", text)
	}
	// CONTROL: the credential really was in play, or the absence above is free.
	if c.User != user || c.Password != pass {
		t.Fatalf("premise broken: the split did not move the credential, User=%q", c.User)
	}
}

// TestTransportError covers the case where no answer arrives at all.
func TestTransportError(t *testing.T) {
	const (
		user = "svcadmin7"
		pass = "p4ssw0rdZZ"
	)
	// A listener that is closed immediately gives a port nothing is listening on.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := srv.URL
	srv.Close()

	withCreds := strings.Replace(dead, "http://", "http://"+user+":"+pass+"@", 1)
	c := NewClient(withCreds, "", "")
	err := callGet(t, c, "/metadata")
	if err == nil {
		t.Fatal("expected an error against a closed port")
	}

	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("errors.As found no *TransportError in %#v (%v)", err, err)
	}
	if te.Endpoint != "/metadata" {
		t.Errorf("Endpoint = %q, want %q", te.Endpoint, "/metadata")
	}
	if want := DisplayBase(dead); te.Base != want {
		t.Errorf("Base = %q, want %q", te.Base, want)
	}
	if te.Unwrap() == nil {
		t.Error("Unwrap returned nil, so the cause is unreachable")
	}
	text := te.Error()
	if !strings.Contains(text, DisplayBase(dead)) || !strings.Contains(text, "/metadata") {
		t.Errorf("Error() = %q, want it to name the display base and the endpoint", text)
	}
	for _, secret := range []string{user, pass} {
		if strings.Contains(text, secret) {
			t.Errorf("Error() carries the credential %q:\n%s", secret, text)
		}
	}
}

// TestRequestError covers the case where the address built from --base and the
// method name is not a URL at all, so no request is ever made.
func TestRequestError(t *testing.T) {
	const (
		user = "svcadmin7"
		pass = "p4ssw0rdZZ"
	)
	base := "http://127.0.0.1:1"
	c := NewClient("http://"+user+":"+pass+"@127.0.0.1:1", "", "")
	if c.baseErr != nil {
		t.Fatalf("premise broken: the base was refused: %v", c.baseErr)
	}

	// A DEL byte is an ASCII control character, which net/url rejects before any
	// connection is attempted.
	err := callGet(t, c, "/\x7f")
	if err == nil {
		t.Fatal("expected an error for an endpoint that is not a valid URL")
	}

	var re *RequestError
	if !errors.As(err, &re) {
		t.Fatalf("errors.As found no *RequestError in %#v (%v)", err, err)
	}
	if want := DisplayBase(base); re.Base != want {
		t.Errorf("Base = %q, want %q", re.Base, want)
	}
	if re.Unwrap() == nil {
		t.Error("Unwrap returned nil, so the cause is unreachable")
	}
	text := re.Error()
	if !strings.Contains(text, DisplayBase(base)) {
		t.Errorf("Error() = %q, want it to name the display base", text)
	}
	for _, secret := range []string{user, pass} {
		if strings.Contains(text, secret) {
			t.Errorf("Error() carries the credential %q:\n%s", secret, text)
		}
	}

	// CONTROL: a well formed endpoint on the same client does NOT produce a
	// *RequestError, so the assertion above is about the endpoint rather than
	// about every call this client makes.
	var te *TransportError
	if err := callGet(t, c, "/metadata"); !errors.As(err, &te) {
		t.Errorf("a valid endpoint on a dead port gave %#v, want a *TransportError", err)
	}
}

// TestTrimPartialRune pins the helper directly, including the case that
// separates a broken byte from a correctly encoded U+FFFD.
func TestTrimPartialRune(t *testing.T) {
	const two = "Ж" // two bytes
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"empty", []byte{}, []byte{}},
		{"ascii", []byte("abc"), []byte("abc")},
		{"whole runes", []byte(two + two), []byte(two + two)},
		{"split two byte rune", []byte(two + two)[:3], []byte(two)},
		{"encoded replacement char is kept", []byte("a\uFFFD"), []byte("a\uFFFD")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := trimPartialRune(append([]byte(nil), c.in...))
			if string(got) != string(c.want) {
				t.Errorf("trimPartialRune(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	// CONTROL: the split input really is invalid before the trim, so the case
	// above is not asserting on something that was already fine.
	if utf8.Valid([]byte(two + two)[:3]) {
		t.Fatal("premise broken: the three byte cut of two Cyrillic runes is valid UTF-8")
	}
}
