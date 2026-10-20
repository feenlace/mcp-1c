package tools

import (
	"encoding/json"
	"github.com/feenlace/mcp-1c/onec"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRecordCountHandlerRejectsCaseAliasBeforeTransport(t *testing.T) {
	client := onec.NewClient("http://synthetic", "", "")
	var calls int
	client.HTTPClient.Transport = passportRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		var input onec.RecordCountRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Period == nil {
			t.Fatal("period was silently dropped")
		}
		data, _ := json.Marshal(onec.RecordCountResult{ObjectType: input.ObjectType, ObjectName: input.ObjectName, Count: 37, Period: input.Period})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})
	handler := NewRecordCountHandler(client)
	const canonical = `{"object_type":"Document","object_name":"Orders","period":{"field":"Date","start":"2024-01-01T00:00:00","end":"2025-01-01T00:00:00"}}`
	alias := strings.TrimSuffix(canonical, "}") + `,"Period":null}`
	if result, err := drive(t, handler, alias); err == nil || result != nil {
		t.Fatalf("raw MCP alias accepted: %+v %v", result, err)
	}
	if calls != 0 {
		t.Fatalf("alias request reached /count: %d", calls)
	}
	result, err := drive(t, handler, canonical)
	if err != nil || result == nil || result.IsError || calls != 1 || !strings.Contains(resultText(t, result), `"period"`) {
		t.Fatalf("canonical retry failed: %+v %v calls=%d", result, err, calls)
	}
}
