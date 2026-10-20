package onec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCountRecordsStrictContract(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/count" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var req RecordCountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object_type": req.ObjectType, "object_name": req.ObjectName, "count": json.Number("9007199254740993"), "period": req.Period})
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "", "")
	req := RecordCountRequest{ObjectType: "Document", ObjectName: "Продажа", Period: &CountPeriod{Field: "Дата", Start: "2024-02-29T00:00:00", End: "2024-03-01T00:00:00"}}
	got, err := client.CountRecords(context.Background(), req)
	if err != nil || got.Count != 9007199254740993 {
		t.Fatalf("exact count: %+v %v", got, err)
	}
	for _, bad := range []string{"2023-02-29T00:00:00", "2024-02-29T00:00:00Z", "2024-02-29T00:00:00.1", "2024-02-29T00:00:00+03:00"} {
		req.Period.Start = bad
		if _, err := client.CountRecords(context.Background(), req); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid input reached transport: %d", calls)
	}
}

func TestCountRecordsRejectsMalformedResponses(t *testing.T) {
	req := RecordCountRequest{ObjectType: "Catalog", ObjectName: "Items"}
	for _, body := range []string{`{"object_type":"Catalog","object_name":"Items"}`, `{"object_type":"Catalog","object_name":"Items","count":null}`, `{"object_type":"Catalog","object_name":"Items","count":-1}`, `{"object_type":"Catalog","object_name":"Items","count":1.2}`, `{"object_type":"Catalog","object_name":"Items","count":9223372036854775808}`, `{"object_type":"Catalog","object_name":"Other","count":0}`, `{"object_type":"Catalog","object_name":"Items","count":0,"period":{"field":"Date","start":"2024-01-01T00:00:00","end":"2025-01-01T00:00:00"}}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			if _, err := NewClient(srv.URL, "", "").CountRecords(context.Background(), req); err == nil {
				t.Fatal("accepted malformed response")
			}
		})
	}
}
func TestCountRecordsFiltersNeverSilentlyDrop(t *testing.T) {
	for _, body := range []string{`{"object_type":"Catalog","object_name":"Items","period":null}`, `{"object_type":"Catalog","object_name":"Items","organization":null}`, `{"object_type":"Catalog","object_name":"Items","query":"SELECT *"}`, `{"object_type":"Catalog","object_name":"Items","period":{"field":"Date","start":"2024-01-01T00:00:00","end":"2025-01-01T00:00:00","where":"1=1"}}`} {
		var r RecordCountRequest
		if err := json.Unmarshal([]byte(body), &r); err == nil {
			t.Errorf("accepted unsupported filter %s", body)
		}
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	for _, uuid := range []string{"", "00000000-0000-0000-0000-000000000000", "wrong", "f63d3d44-944b-430e-ad5b-06977e2ff030'"} {
		r := RecordCountRequest{ObjectType: "Catalog", ObjectName: "Items", Organization: &CountOrganization{Field: "Organization", Reference: CountReference{CatalogName: "Organizations", UUID: uuid}}}
		if _, err := NewClient(srv.URL, "", "").CountRecords(context.Background(), r); err == nil {
			t.Errorf("accepted UUID %q", uuid)
		}
	}
	if calls != 0 {
		t.Fatal("invalid reference reached transport")
	}
}

func TestCountRecordsTransportCancellationAndDenied(t *testing.T) {
	request := RecordCountRequest{ObjectType: "Catalog", ObjectName: "Items"}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		client := NewClient("poll://selected-base/hs/mcp", "", "")
		client.HTTPClient.Transport = countTestTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/hs/mcp/count" {
				t.Fatalf("endpoint %s", r.URL.Path)
			}
			return nil, cause
		})
		result, err := client.CountRecords(context.Background(), request)
		if result != nil || !errors.Is(err, cause) {
			t.Fatalf("transport cancellation lost: %+v %v", result, err)
		}
	}
	for _, status := range []int{403, 404, 500} {
		client := NewClient("http://fixture", "", "")
		client.HTTPClient.Transport = countTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"count unavailable"}`))}, nil
		})
		result, err := client.CountRecords(context.Background(), request)
		var se *StatusError
		if result != nil || !errors.As(err, &se) || se.StatusCode != status {
			t.Fatalf("denial lost: %+v %v", result, err)
		}
	}
}

type countTestTransport func(*http.Request) (*http.Response, error)

func (f countTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
