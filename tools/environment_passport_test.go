package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/feenlace/mcp-1c/onec"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type passportRoundTrip func(*http.Request) (*http.Response, error)

func (f passportRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPassportFailurePrivacyAndPartialSources(t *testing.T) {
	const secrets = "private-host-passport.invalid 192.0.2.231 /secret/passport/root password-PASSPORT-unique"
	for _, probe := range []struct {
		name   string
		status int
		body   string
		err    error
		want   string
	}{
		{"denied", 403, `{"error":"` + secrets + `"}`, nil, "denied"},
		{"old", 404, `{"error":"` + secrets + `"}`, nil, "unsupported"},
		{"malformed", 200, `{"version":"` + secrets + `"`, nil, "unavailable"},
		{"native", 500, `{"error":"` + secrets + `"}`, nil, "unavailable"},
		{"network", 0, "", errors.New(secrets), "unavailable"},
		{"timeout", 0, "", context.DeadlineExceeded, "timeout"},
		{"cancelled", 0, "", context.Canceled, "cancelled"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			logs := captureSlog(t)
			slog.Error("passport capture control", "safe", "control")
			client := onec.NewClient("http://private-host-passport.invalid/secret/passport/root", "password-PASSPORT-unique", "password-PASSPORT-unique")
			client.HTTPClient.Transport = passportRoundTrip(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/configuration") {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"name":"Fixture","version":"1.2.3","vendor":"FixtureVendor","platform_version":"8.3.27.2130","mode":"file"}`))}, nil
				}
				if probe.err != nil {
					return nil, probe.err
				}
				return &http.Response{StatusCode: probe.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(probe.body))}, nil
			})
			pc := PassportContext{Mode: ModeLive, ProductVersion: "1.2.3", Edition: "Advanced", Transport: "polling", SourceRoot: "/secret/passport/root"}
			got, err := ReadEnvironmentPassport(context.Background(), client, pc)
			if err != nil || got.ConfigurationName != "Fixture" || got.ConfigurationStatus.Status != "available" || got.VersionStatus.Status != probe.want {
				t.Fatalf("partial %+v err=%v", got, err)
			}
			res, err := drive(t, NewEnvironmentPassportHandler(client, pc), `{}`)
			if err != nil || res.IsError {
				t.Fatalf("safe partial response %v", err)
			}
			data, _ := json.Marshal(got)
			captured := string(data) + resultText(t, res) + fmt.Sprint(*logs) + fmt.Sprint(err)
			for _, secret := range strings.Fields(secrets) {
				if strings.Contains(captured, secret) {
					t.Errorf("secret reached response/log: %q", secret)
				}
			}
			if len(*logs) != 1 {
				t.Fatalf("unexpected raw failure log: %+v", *logs)
			}
		})
	}
}
func TestPassportOfflineMakesZeroClientCalls(t *testing.T) {
	var calls int
	client := onec.NewClient("http://offline-must-not-call.invalid", "", "")
	client.HTTPClient.Transport = passportRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not call") })
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "Configuration.xml"), []byte(`<MetaDataObject><Configuration><Properties><Name>OfflineFixture</Name><Version>1.0.0</Version><Vendor>Vendor</Vendor></Properties><ChildObjects/></Configuration></MetaDataObject>`), 0600)
	for _, c := range []*onec.Client{nil, client} {
		p, err := ReadEnvironmentPassport(context.Background(), c, PassportContext{Mode: ModeOffline, Edition: "Community", ProductVersion: "1.2.3", SourceRoot: root})
		if err != nil || p.ConfigurationName != "OfflineFixture" || p.ConfigurationStatus.Source != "dump_xml" || p.PlatformVersion != "" || p.ExtensionVersion != "" || p.Transport != "offline" {
			t.Fatalf("offline %+v %v", p, err)
		}
	}
	if calls != 0 {
		t.Fatalf("offline made %d calls", calls)
	}
}

func TestPassportVersionsNeverEchoOpaqueSuffixes(t *testing.T) {
	const suffix = "gate-a-private-host.invalid.password-GATEA-unique"
	client := onec.NewClient("http://fixture", "", "")
	client.HTTPClient.Transport = passportRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"version":"0.4.9-` + suffix + `"}`
		if strings.HasSuffix(r.URL.Path, "/configuration") {
			body = `{"name":"Fixture","version":"1.2.3+` + suffix + `","vendor":"Vendor","platform_version":"8.3.27.2130-` + suffix + `","mode":"file"}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	pc := PassportContext{Mode: ModeLive, ProductVersion: "v1.22.4-" + suffix, Edition: "Community", Transport: "http"}
	p, err := ReadEnvironmentPassport(context.Background(), client, pc)
	if err != nil || p.ExtensionVersion != "0.4.9" || p.ConfigurationVersion != "1.2.3" || p.PlatformVersion != "8.3.27.2130" || p.ProductVersion != "1.22.4" || p.VersionStatus.Status != "available" {
		t.Fatalf("numeric version semantics lost: %+v %v", p, err)
	}
	res, err := drive(t, NewEnvironmentPassportHandler(client, pc), `{}`)
	if err != nil || res.IsError {
		t.Fatalf("passport response: %+v %v", res, err)
	}
	for _, sentinel := range []string{suffix, "gate-a-private-host.invalid", "password-GATEA-unique"} {
		if strings.Contains(resultText(t, res), sentinel) {
			t.Fatalf("opaque sentinel exposed: %q", sentinel)
		}
	}
	for input, want := range map[string]string{"0.4.9": "0.4.9", "0.5.0-beta": "0.5.0", "v1.2.3+build": "1.2.3", "8.3.27.2130": "8.3.27.2130", "not-version": ""} {
		if got := safePassportVersion(input); got != want {
			t.Errorf("version %q = %q, want %q", input, got, want)
		}
	}
}

// The server-selected transport, never a worker's version claim, selects the namespace.
func TestPassportEPFVersionIsNotAnInstalledExtension(t *testing.T) {
	for _, tc := range []struct {
		name, transport, body, wantExtension, wantConnector, wantStatus string
		wantProtocol                                                    int
	}{
		{"epf", "epf", `{"version":"9.9.9","connector_version":"0.1.0+private-host.invalid","protocol":1}`, "", "0.1.0", "available", 1},
		{"http", "http", `{"version":"9.9.9","connector_version":[],"protocol":"unrelated"}`, "9.9.9", "", "available", 0},
		{"epf_cfe_claim_only", "epf", `{"version":"9.9.9"}`, "", "", "unavailable", 0},
		{"epf_unknown_protocol", "epf", `{"connector_version":"0.1.0","protocol":99}`, "", "", "unsupported", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := onec.NewClient("http://fixture", "", "")
			client.HTTPClient.Transport = passportRoundTrip(func(r *http.Request) (*http.Response, error) {
				body := tc.body
				if strings.HasSuffix(r.URL.Path, "/configuration") {
					body = `{"name":"Fixture"}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			p, err := ReadEnvironmentPassport(context.Background(), client, PassportContext{Mode: ModeLive, Transport: tc.transport})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(p)
			var fields map[string]any
			_ = json.Unmarshal(b, &fields)
			connector, _ := fields["native_connector_version"].(string)
			protocol, _ := fields["native_connector_protocol"].(float64)
			source := "live_extension"
			if tc.transport == "epf" {
				source = "live_connector"
			}
			if p.ExtensionVersion != tc.wantExtension || connector != tc.wantConnector || int(protocol) != tc.wantProtocol || p.VersionStatus.Status != tc.wantStatus || p.VersionStatus.Source != source {
				t.Fatalf("wrong version namespace/status: extension=%q connector=%q protocol=%v source=%q status=%q", p.ExtensionVersion, connector, protocol, p.VersionStatus.Source, p.VersionStatus.Status)
			}
			if strings.Contains(string(b), "private-host.invalid") {
				t.Fatal("opaque version suffix exposed")
			}
		})
	}
}
