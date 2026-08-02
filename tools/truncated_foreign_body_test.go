package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// truncated_foreign_body_test.go covers the ONE combination the renderer had no
// branch for.
//
// The truncation notice lived only under BodyKindExtension. Truncating a JSON
// envelope makes it unparsable, extensionEnvelopeDetail then declines, and the
// class becomes BodyKindForeign, whose branch returns before the notice is ever
// added. So the notice sat in the one branch truncation practically cannot reach
// while the reachable one lost the fact in silence, and the model was told
// «длина тела в байтах: N» about a body whose length is not N.
//
// The premise is DRIVEN, never assumed: every row asserts the (BodyKind,
// Truncated) pair the real client produced before it looks at the rendering, so
// a fixture that stopped being truncated cannot pass as a fixed renderer.
//
// Everything binds 127.0.0.1 on a port in the 19800-19999 range, the same range
// cmd/mcp-1c/extension_version_check_test.go already reserves for local probes.

const (
	truncListenPortLow  = 19800
	truncListenPortHigh = 19999
)

// localListener binds the first free port in the reserved range.
func localListener(t *testing.T) net.Listener {
	t.Helper()
	for port := truncListenPortLow; port <= truncListenPortHigh; port++ {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			return ln
		}
	}
	t.Fatalf("no free port in %d-%d on 127.0.0.1", truncListenPortLow, truncListenPortHigh)
	return nil
}

// bodyServer answers every path with one status, one Content-Type and one body,
// and returns the base URL to point a client at.
func bodyServer(t *testing.T, status int, contentType, body string) string {
	t.Helper()
	ln := localListener(t)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String()
}

// statusErrorFromBase drives the REAL onec client and returns what it built, so
// the premise of each row is measured rather than constructed by hand.
func statusErrorFromBase(t *testing.T, base string) *onec.StatusError {
	t.Helper()
	var sink onec.ConfigurationInfo
	err := onec.NewClient(base, "", "").Get(context.Background(), "/configuration", &sink)
	var se *onec.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("the client did not produce a *onec.StatusError, so this row measures nothing: %v", err)
	}
	return se
}

// envelopeBody is the shape the extension really sends: {"error":"…"}.
//
// The single ASCII byte before the Cyrillic run shifts the cut off the rune
// grid, which is what the live acceptance run hit: the cap counts bytes, «Ж» is
// two of them, and the trim then leaves 65535 rather than 65536.
func envelopeBody(runes int) string {
	return `{"error":"a` + strings.Repeat("Ж", runes) + `"}`
}

// foreignBody is the shape a web server sends: markup, no envelope anywhere.
func foreignBody(runes int) string {
	return `<!DOCTYPE html><html><head><title>500</title></head><body>` +
		strings.Repeat("Ж", runes) + `</body></html>`
}

// readCap measures the read cap instead of repeating its value.
//
// The probe body is ASCII, so trimPartialRune cannot move the figure and the
// reading is exact. A number typed here would be a second copy of a constant
// this package cannot see, free to drift from the one that does the cutting.
func readCap(t *testing.T) int {
	t.Helper()
	const probe = 200000
	base := bodyServer(t, http.StatusInternalServerError, "text/plain", strings.Repeat("a", probe))
	se := statusErrorFromBase(t, base)
	if !se.Truncated {
		t.Fatalf("a %d byte body came back whole, so this probe never reached the cap", probe)
	}
	if se.BodyBytes <= 0 || se.BodyBytes >= probe {
		t.Fatalf("the probe read %d bytes of a %d byte body, which is not a cap", se.BodyBytes, probe)
	}
	return se.BodyBytes
}

// TestTruncatedExtensionEnvelopeIsReachable settles the question the acceptance
// run left open by calling the combination unreachable.
//
// It is reachable, and the condition is exact: an envelope whose closing brace
// lands on the last byte the cap allows is read WHOLE, so it parses, and one
// further byte on the wire is enough to set Truncated. 1С's own HTTP service
// supplies that byte, a trailing newline (onec/client.go says so where it
// tolerates the same newline on the success path). So the notice on the
// extension branch is not dead code and must not be deleted as such.
func TestTruncatedExtensionEnvelopeIsReachable(t *testing.T) {
	capBytes := readCap(t)

	const open, closing = `{"error":"`, `"}`
	fill := capBytes - len(open) - len(closing)
	if fill <= 0 {
		t.Fatalf("the cap is %d bytes, too small to hold an envelope at all", capBytes)
	}
	// ASCII fill: the brace has to land on a byte the cap allows, and a
	// multi-byte rune could put it one byte past.
	envelope := open + strings.Repeat("a", fill) + closing
	if len(envelope) != capBytes {
		t.Fatalf("the fixture is %d bytes and the cap is %d; it must be exactly the cap or this "+
			"test measures a different case", len(envelope), capBytes)
	}

	base := bodyServer(t, http.StatusInternalServerError, "application/json; charset=utf-8", envelope+"\n")
	se := statusErrorFromBase(t, base)
	if se.BodyKind != onec.BodyKindExtension || !se.Truncated {
		t.Fatalf("the combination is (%s, truncated=%v); this test exists to drive "+
			"(%s, truncated=true)", se.BodyKind, se.Truncated, onec.BodyKindExtension)
	}
	t.Logf("envelope %d bytes + 1 trailing byte: read %d bytes, kind=%s, truncated=%v",
		len(envelope), se.BodyBytes, se.BodyKind, se.Truncated)

	res, err := drive(t, NewConfigurationInfoHandler(onec.NewClient(base, "", "")), `{}`)
	text := failureText(t, res, err)
	if !strings.Contains(text, strings.TrimRight(fmt.Sprintf(bodyTruncatedNotice, se.BodyBytes), "\n")) {
		t.Errorf("the reachable extension branch does not announce the cut:\n%s", text)
	}

	// Error() IS A SECOND CHANNEL TO THE MODEL, quoted into a successful answer
	// by tools/form.go formServiceCallFailedNote, and it dropped the same fact
	// on this same branch by returning at Detail. The two losses are mirror
	// images: the renderer kept truncation only where the class made it rare,
	// Error() kept it only where the class made it common.
	if got := se.Error(); !strings.Contains(got, "truncated") {
		t.Errorf("Error() loses the truncation fact on the extension branch: %q", got)
	}
}

// TestTruncatedBodyIsReportedAsTruncated drives a real wired handler against a
// local listener whose body is over the read cap, in both classifications, and
// asserts what the model is told.
func TestTruncatedBodyIsReportedAsTruncated(t *testing.T) {
	// 40000 Cyrillic runes is 80000 bytes, comfortably over the 65536 cap in
	// either wrapper; 1000 is comfortably under it in either.
	const (
		overCapRunes  = 40000
		underCapRunes = 1000
	)

	cases := []struct {
		name        string
		body        string
		contentType string
		wantKind    string
		wantTrunc   bool
	}{
		{
			name: "valid JSON envelope over the cap", body: envelopeBody(overCapRunes),
			contentType: "application/json; charset=utf-8",
			// The envelope is valid on the wire and unparsable after the cut, so
			// the class the renderer sees is foreign. That is the whole defect.
			wantKind: onec.BodyKindForeign, wantTrunc: true,
		},
		{
			name: "non JSON body over the cap", body: foreignBody(overCapRunes),
			contentType: "text/html",
			wantKind:    onec.BodyKindForeign, wantTrunc: true,
		},
		{
			// CONTROL, one thing varied: the same envelope under the cap parses,
			// so the class flips back. Without it "foreign" above could be a
			// malformed fixture rather than the cut.
			name: "valid JSON envelope under the cap", body: envelopeBody(underCapRunes),
			contentType: "application/json; charset=utf-8",
			wantKind:    onec.BodyKindExtension, wantTrunc: false,
		},
		{
			// CONTROL: an ordinary foreign body still gets the ordinary foreign
			// rendering, so the repair cannot be a blanket rewrite of the branch.
			name: "non JSON body under the cap", body: foreignBody(underCapRunes),
			contentType: "text/html",
			wantKind:    onec.BodyKindForeign, wantTrunc: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := bodyServer(t, http.StatusInternalServerError, c.contentType, c.body)

			se := statusErrorFromBase(t, base)
			if se.BodyKind != c.wantKind || se.Truncated != c.wantTrunc {
				t.Fatalf("premise broken: the client classified this body as (%s, truncated=%v), "+
					"want (%s, truncated=%v); the rendering assertions below would be about "+
					"another case", se.BodyKind, se.Truncated, c.wantKind, c.wantTrunc)
			}

			res, err := drive(t, NewConfigurationInfoHandler(onec.NewClient(base, "", "")), `{}`)
			text := failureText(t, res, err)
			t.Logf("body on the wire: %d bytes; client read %d bytes, truncated=%v, kind=%s",
				len(c.body), se.BodyBytes, se.Truncated, se.BodyKind)
			t.Logf("MODEL FACING TEXT (%d bytes):\n%s", len(text), text)

			notice := strings.TrimRight(fmt.Sprintf(bodyTruncatedNotice, se.BodyBytes), "\n")
			bodyLengthLine := fmt.Sprintf(lineStatusForeign, se.StatusCode, se.BodyBytes)
			hasNotice := strings.Contains(text, notice)
			hasBodyLength := strings.Contains(text, bodyLengthLine)
			hasAssertingRemedy := strings.Contains(text, strings.TrimRight(remedyForeignBody, "\n"))

			if !c.wantTrunc {
				// The controls: nothing announces a cut that did not happen, and
				// the untouched branches keep their own text.
				if hasNotice {
					t.Errorf("a body that fits announces truncation anyway:\n%s", text)
				}
				if c.wantKind == onec.BodyKindForeign && !(hasBodyLength && hasAssertingRemedy) {
					t.Errorf("the ordinary foreign rendering lost its own line or remedy:\n%s", text)
				}
				return
			}

			if !hasNotice {
				t.Errorf("the model is not told the body was cut; the truncation fact is lost:\n%s", text)
			}
			if hasBodyLength {
				t.Errorf("the model is told %d is «длина тела в байтах», and the cut is exactly why "+
					"it is not:\n%s", se.BodyBytes, text)
			}
			if hasAssertingRemedy {
				t.Errorf("the remedy asserts the body was foreign, which the cut makes unknowable: "+
					"a cut envelope is unparsable no matter who sent it:\n%s", text)
			}
			if !strings.Contains(text, "get_configuration_info") {
				t.Errorf("the answer offers no way to tell the two possible authors apart:\n%s", text)
			}
		})
	}
}
