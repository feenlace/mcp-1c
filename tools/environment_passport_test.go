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
