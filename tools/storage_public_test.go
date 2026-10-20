package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/feenlace/mcp-1c/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type storageTransport func(*http.Request) (*http.Response, error)

func (f storageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A consumer can compose typed fetching, formatting and a complete handler with
// the same supplied client, including transports that dispatch by URL.Path.
func TestStoragePublicComposition(t *testing.T) {
	client := onec.NewClient("http://fixture/hs/mcp", "", "")
	calls := 0
	client.HTTPClient.Transport = storageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/hs/mcp/storage/Catalog/Товар_2" || r.URL.RawQuery != "" || r.URL.Fragment != "" {
			t.Fatalf("unexpected transport request: %s %s", r.Method, r.URL)
		}
		if !strings.Contains(r.URL.EscapedPath(), "%D0") {
			t.Fatal("metadata path segment must be URL encoded")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"name":"Товар_2","full_name":"Справочник.Товар_2","tables":[]}`)), Header: make(http.Header)}, nil
	})
	st, err := client.GetStorageStructure(context.Background(), "Catalog", "Товар_2")
	if err != nil || st == nil || st.Tables == nil {
		t.Fatalf("typed operation: structure=%+v err=%v", st, err)
	}
	want := tools.FormatStorageStructure(st)
	handler := tools.NewStorageStructureHandler(client)
	result, err := handler(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Name: tools.StorageStructureTool().Name, Arguments: json.RawMessage(`{"object_type":"Catalog","object_name":"Товар_2"}`),
	}})
	if err != nil || result == nil || result.IsError || result.Content[0].(*mcp.TextContent).Text != want || calls != 2 {
		t.Fatalf("full composition: result=%+v err=%v calls=%d", result, err, calls)
	}
}

func TestStoragePublicTransportFailures(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := onec.NewClient("http://fixture", "", "")
			client.HTTPClient.Transport = storageTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":"storage unavailable"}`)), Header: make(http.Header)}, nil
			})
			st, err := client.GetStorageStructure(context.Background(), "Catalog", "Товар")
			var se *onec.StatusError
			if st != nil || !errors.As(err, &se) || se.StatusCode != status {
				t.Fatalf("transport error must be preserved: structure=%+v err=%v", st, err)
			}
		})
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			client := onec.NewClient("http://fixture", "", "")
			client.HTTPClient.Transport = storageTransport(func(r *http.Request) (*http.Response, error) { return nil, cause })
			st, err := client.GetStorageStructure(context.Background(), "Catalog", "Товар")
			if st != nil || !errors.Is(err, cause) {
				t.Fatalf("cancellation/timeout must be preserved: structure=%+v err=%v", st, err)
			}
		})
	}
}
