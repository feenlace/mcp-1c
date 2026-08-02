package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// extension_version_check_test.go measures what the /version probe TELLS THE
// OPERATOR, which is a different question from what it computes.
//
// WHAT THE PROBE CAN ACTUALLY OBSERVE, from source and nowhere else:
// ВерсияGET in extension/src/HTTPServices/MCPService/Ext/Module.bsl builds a
// structure with exactly one key, "version", holding a hardcoded literal, and
// onec.VersionInfo declares exactly one field to receive it. No edition, no
// vendor, no product name crosses that wire. So a Community binary CANNOT see
// which edition's extension answered it. Anything the message says about
// editions would be invented, and this file asserts it is not said.
//
// What the string does support is ORDER, and that is what the constant means:
// expectedExtensionVersion is the LOWEST extension this binary can rely on,
// because extension releases added endpoints the Go side calls (/subsystems
// arrived in ext 0.4.4, commit 2f784df). Below it, endpoints may be missing and
// the pairing is genuinely broken. At or above it, everything this binary calls
// exists — which is why running the paid editions' newer extension against the
// Community binary is a supported combination, and why reporting it as an error
// was a false alarm on a healthy system.
//
// Two defects are therefore pinned here at once: a supported pairing must stop
// being reported as a fault, and a probe that never reached the extension must
// stop being byte for byte identical to a healthy one.
//
// Every listener binds 127.0.0.1 on a port in the 19800-19999 range.

const (
	probeListenPortLow  = 19800
	probeListenPortHigh = 19999
)

// loopbackListenerInRange returns a listener on 127.0.0.1 bound inside the
// permitted port range, rather than letting httptest pick an arbitrary one.
func loopbackListenerInRange(t *testing.T) net.Listener {
	t.Helper()
	for port := probeListenPortLow; port <= probeListenPortHigh; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln
		}
	}
	t.Fatalf("no free port in %d-%d", probeListenPortLow, probeListenPortHigh)
	return nil
}

// syncBuffer keeps the captured log safe to read under -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// captureProbeLogAgainst runs the probe against a base URL and returns
// everything it logged.
//
// The capturing handler sits at LevelInfo DELIBERATELY, so an assertion can see
// a confirmation logged below ERROR. Which level each outcome deserves is then
// asserted from the captured text, not assumed by the capture.
func captureProbeLogAgainst(t *testing.T, baseURL string) string {
	t.Helper()

	var out syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	checkExtensionVersion(onec.NewClient(baseURL, "", ""))
	return out.String()
}

// captureProbeLog serves one canned /version answer and returns the probe's log.
func captureProbeLog(t *testing.T, status int, contentType, body string) string {
	t.Helper()

	ln := loopbackListenerInRange(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	return captureProbeLogAgainst(t, srv.URL)
}

// versionAnswer is the ordinary success shape: HTTP 200 with a JSON version.
func versionAnswer(t *testing.T, v string) string {
	t.Helper()
	return captureProbeLog(t, http.StatusOK, "application/json", `{"version":`+strconv.Quote(v)+`}`)
}

// slogAttrValue pulls one key=value attribute out of slog's text format,
// unquoting it when slog quoted it. Returns "" when the key is absent.
func slogAttrValue(line, key string) string {
	i := strings.Index(line, " "+key+"=")
	if i < 0 {
		return ""
	}
	rest := line[i+len(key)+2:]
	if strings.HasPrefix(rest, `"`) {
		if v, err := strconv.Unquote(rest[:strings.Index(rest[1:], "\n")+1]); err == nil {
			return v
		}
		// Fall back to the quoted run up to the last quote on the line.
		end := strings.Index(rest[1:], "\n")
		if end < 0 {
			end = len(rest) - 1
		}
		if v, err := strconv.Unquote(strings.TrimRight(rest[:end+1], " ")); err == nil {
			return v
		}
		return rest[:end+1]
	}
	if end := strings.IndexAny(rest, " \n"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// hasError reports whether the probe emitted an ERROR-level line.
//
// ERROR is the level that matters and not a stylistic choice: in pipe mode, how
// every MCP client launches the server, main installs the handler at LevelError,
// so an outcome reported below ERROR is discarded and the log is silent again in
// exactly the mode the defect lives in.
func hasError(log string) bool { return strings.Contains(log, "level=ERROR") }

// TestVersionProbe_SupportedPairingIsNotAnError is the false alarm.
//
// The paid editions ship a HIGHER-numbered extension and running it against the
// Community binary is supported: every endpoint this binary calls exists there.
// The old check compared for equality, so it reported that healthy, supported,
// officially-fine deployment as an ERROR on every single start.
func TestVersionProbe_SupportedPairingIsNotAnError(t *testing.T) {
	for _, newer := range []string{"0.7.4", "0.4.7", "1.0.0", "0.5.0-beta"} {
		t.Run(newer, func(t *testing.T) {
			got := versionAnswer(t, newer)
			if hasError(got) {
				t.Errorf("extension %s is at or above the %s this binary needs, so every endpoint "+
					"it calls exists; reporting it as an ERROR is a false alarm on a healthy, "+
					"supported deployment.\ngot: %s", newer, expectedExtensionVersion, got)
			}
			// And it must not tell that operator to run --install, which would
			// replace their extension with the one this binary carries.
			if strings.Contains(got, "--install") {
				t.Errorf("the probe advised --install against a newer extension; that overwrites "+
					"it with %s, downgrading a working install.\ngot: %s", expectedExtensionVersion, got)
			}
		})
	}
}

// TestVersionProbe_TooOldIsStillReported keeps the fix from becoming useless.
// Below the constant the pairing is genuinely broken, and that must still speak.
func TestVersionProbe_TooOldIsStillReported(t *testing.T) {
	for _, older := range []string{"0.4.5", "0.4.0", "0.3.9"} {
		t.Run(older, func(t *testing.T) {
			got := versionAnswer(t, older)
			if !hasError(got) {
				t.Errorf("extension %s predates the %s this binary requires, so endpoints it calls "+
					"may not exist; that must still be reported.\ngot: %s",
					older, expectedExtensionVersion, got)
			}
			if !strings.Contains(got, older) || !strings.Contains(got, expectedExtensionVersion) {
				t.Errorf("the line must name both what was found and what is required.\ngot: %s", got)
			}
			// Here --install IS the remedy, and it must say what it installs.
			hint := slogAttrValue(got, "hint")
			if hint == "" {
				t.Fatalf("no hint on the one outcome that has a remedy:\n%s", got)
			}
			if !strings.Contains(hint, expectedExtensionVersion) {
				t.Errorf("the hint does not name the version it would install.\nhint: %s", hint)
			}
		})
	}
}

// TestVersionProbe_MessageInventsNoEdition pins the limit of what the probe saw.
// A bare version string cannot identify an edition, so no outcome may name one.
func TestVersionProbe_MessageInventsNoEdition(t *testing.T) {
	for _, v := range []string{"0.7.4", "0.4.0", "0.4.6"} {
		got := strings.ToLower(versionAnswer(t, v))
		for _, invented := range []string{"advanced", "enterprise", "professional", "edition", "редакц"} {
			if strings.Contains(got, invented) {
				t.Errorf("version %s: the message says %q, which GET /version does not carry "+
					"(one key, one bare string, no edition marker).\ngot: %s", v, invented, got)
			}
		}
	}
}

// TestVersionProbe_UnreachableIsNotSilent is the original half of the defect.
// A probe that never reached the extension must not render as a healthy start.
//
// These are the inputs a real deployment produces: wrong publication path (404),
// authentication refused (401), IIS answering instead of 1C, nothing listening.
func TestVersionProbe_UnreachableIsNotSilent(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{"404 wrong publication path", http.StatusNotFound, "text/plain", "not found"},
		{"401 authentication refused", http.StatusUnauthorized, "text/plain", "denied"},
		{"IIS answered, not 1C", http.StatusOK, "text/html", "<html><body>IIS</body></html>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := captureProbeLog(t, tc.status, tc.contentType, tc.body)
			if strings.TrimSpace(got) == "" {
				t.Errorf("the probe logged NOTHING for %s, byte for byte what a healthy start logs; "+
					"an operator cannot tell the two apart", tc.name)
				return
			}
			if !hasError(got) {
				t.Errorf("%s was reported below ERROR, so pipe mode discards it and the outcome is "+
					"silent again in the mode that matters.\ngot: %s", tc.name, got)
			}
		})
	}

	t.Run("nothing listening at all", func(t *testing.T) {
		ln := loopbackListenerInRange(t)
		addr := ln.Addr().String()
		_ = ln.Close()

		got := captureProbeLogAgainst(t, "http://"+addr+"/hs/mcp-1c")
		if strings.TrimSpace(got) == "" {
			t.Errorf("a base nothing listens on logged NOTHING, identical to a healthy start")
			return
		}
		if !hasError(got) {
			t.Errorf("an unreachable base was reported below ERROR.\ngot: %s", got)
		}
	})
}

// TestVersionProbe_ConfirmedMatchIsDistinguishable is the other side of the
// same coin. Once every failure speaks, silence has to MEAN something, so the
// healthy case must be positively recorded where levels are not filtered.
func TestVersionProbe_ConfirmedMatchIsDistinguishable(t *testing.T) {
	got := versionAnswer(t, expectedExtensionVersion)

	if strings.TrimSpace(got) == "" {
		t.Errorf("a confirmed match logged nothing at all, so a reader with an unfiltered log " +
			"(terminal launch, or --debug) cannot see that the check ever ran")
	}
	if hasError(got) {
		t.Errorf("a matching extension produced an ERROR line.\ngot: %s", got)
	}
}

// TestVersionProbe_AnswerWithoutAVersionIsNotAMismatch pins a claim the probe
// used to make and could not support. A body with no "version" key decodes to
// the zero value, and the old code reported that as a mismatch with got="".
// Nothing was mismatched: the responder never stated a version. That is the same
// class of unknown as an unreachable base, not a measurement of what is
// installed, and it must not be dressed up as one.
func TestVersionProbe_AnswerWithoutAVersionIsNotAMismatch(t *testing.T) {
	got := captureProbeLog(t, http.StatusOK, "application/json", `{"ver":"0.7.4"}`)

	if strings.TrimSpace(got) == "" {
		t.Fatalf("an answer carrying no version logged nothing")
	}
	if strings.Contains(got, `got=""`) {
		t.Errorf("the probe reported an empty version as if it had measured one:\n%s", got)
	}
	if !hasError(got) {
		t.Errorf("an unusable answer was reported below ERROR.\ngot: %s", got)
	}
}

// TestCompareExtensionVersions is the ordering itself, exercised directly so a
// wrong verdict above can be told from a wrong comparison here.
func TestCompareExtensionVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.4.6", "0.4.6", 0, true},
		{"0.7.4", "0.4.6", 1, true},
		{"0.4.5", "0.4.6", -1, true},
		{"0.4.10", "0.4.9", 1, true},     // numeric, not lexicographic
		{"0.10.0", "0.9.9", 1, true},     // the component that bit every naive compare
		{"1.0", "0.4.6", 1, true},        // shorter but larger
		{"0.4.6.1", "0.4.6", 1, true},    // longer, trailing component counts
		{"0.4.6", "0.4.6.0", 0, true},    // trailing zero is not a difference
		{"0.5.0-beta", "0.4.6", 1, true}, // a pre-release suffix must not make it unparseable
		{"", "0.4.6", 0, false},
		{"not-a-version", "0.4.6", 0, false},
	}
	for _, tc := range cases {
		got, ok := compareExtensionVersions(tc.a, tc.b)
		if ok != tc.ok {
			t.Errorf("compareExtensionVersions(%q, %q) ok = %v, want %v", tc.a, tc.b, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("compareExtensionVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
