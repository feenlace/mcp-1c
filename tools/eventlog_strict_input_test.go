package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

func TestEventLogFilterShapeNeverDisappears(t *testing.T) {
	for _, args := range []string{
		`{"from_date":"2026-01-01T00:00:00"}`, `{"Event":["MCP.Probe"]}`, `{"event":null}`, `{"user":null}`, `{"start_date":null}`, `{"end_date":null}`, `{"level":null}`, `{"limit":null}`,
		`{"event":["MCP.Probe"],"event":["MCP.Other"]}`, `null`, `[]`,
	} {
		t.Run(args, func(t *testing.T) {
			c := onec.NewClient("http://synthetic", "", "")
			calls := 0
			c.HTTPClient.Transport = passportRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"events":[],"total":0}`))}, nil
			})
			result, err := drive(t, NewEventLogHandler(c), args)
			if err == nil && (result == nil || !result.IsError) {
				t.Fatal("invalid filter became successful log read")
			}
			if calls != 0 {
				t.Fatal("invalid filter reached transport, potentially reading an unfiltered log")
			}
		})
	}
}
