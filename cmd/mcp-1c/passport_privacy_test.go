package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/feenlace/mcp-1c/onec"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

const passportProbeSecret = "private-passport-host.invalid 192.0.2.231 /passport-secret/root password-PASSPORT-unique"

func TestVersionProbePrivacyBeforeLogging(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"startup denied", 403, `{"error":"` + passportProbeSecret + `"}`},
		{"startup native failure", 500, `{"error":"` + passportProbeSecret + `"}`},
		{"startup malformed", 200, `{"version":"` + passportProbeSecret},
		{"startup invalid version", 200, `{"version":"` + passportProbeSecret + `"}`},
		{"startup numeric prefix with secret suffix", 200, `{"version":"0.4.9-` + passportProbeSecret + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureProbeLog(t, tc.status, "application/json", tc.body)
			for _, s := range strings.Fields(passportProbeSecret) {
				if strings.Contains(logged, s) {
					t.Errorf("startup log contains upstream sentinel: %q", s)
				}
			}
			if strings.Contains(tc.name, "failure") && !hasError(logged) {
				t.Fatal("failure was hidden")
			}
		})
	}
	t.Run("transport error", func(t *testing.T) {
		var out syncBuffer
		old := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo})))
		defer slog.SetDefault(old)
		client := onec.NewClient("http://private-passport-host.invalid/passport-secret/root", "", "")
		client.HTTPClient.Transport = privacyProbeTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New(passportProbeSecret) })
		checkExtensionVersion(context.Background(), client)
		logged := out.String()
		for _, s := range strings.Fields(passportProbeSecret) {
			if strings.Contains(logged, s) {
				t.Errorf("transport log retained sentinel: %q", s)
			}
		}
		if !hasError(logged) {
			t.Fatal("transport failure hidden")
		}
	})
	// Every supported native version is still reported; only opaque suffixes are omitted.
	for _, version := range []string{"0.4.9", "0.5.0-beta", "8.3.27.2130"} {
		if got := versionForLog(version); got == "" || strings.Contains(got, "unavailable") {
			data, _ := json.Marshal(got)
			t.Fatalf("version diagnostic lost: %s", data)
		}
	}
}

type privacyProbeTransport func(*http.Request) (*http.Response, error)

func (f privacyProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
