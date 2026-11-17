package onec

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type timeoutProbe struct {
	managed   bool
	mu        sync.Mutex
	deadlines []bool
}

func (p *timeoutProbe) UseContextTimeout() bool { return p.managed }
func (p *timeoutProbe) RoundTrip(r *http.Request) (*http.Response, error) {
	_, has := r.Context().Deadline()
	p.mu.Lock()
	p.deadlines = append(p.deadlines, has)
	p.mu.Unlock()
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
}
func TestManagedTimeoutRequiresTransportAndExplicitOptionClearsIt(t *testing.T) {
	for _, marker := range []bool{false, true} {
		c := NewClient("http://epf.invalid", "", "", WithTransportManagedTimeout())
		probe := &timeoutProbe{managed: marker}
		c.HTTPClient.Transport = probe
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var out any
				if e := c.Get(context.Background(), "/configuration", &out); e != nil {
					t.Error(e)
				}
			}()
		}
		wg.Wait()
		for _, has := range probe.deadlines {
			if has == marker {
				t.Fatal("transport marker not honored", marker, has)
			}
		}
		if c.HTTPClient.Timeout != DefaultRequestTimeout {
			t.Fatal("shared timeout mutated")
		}
		WithRequestTimeout(DefaultRequestTimeout)(c)
		var out any
		if e := c.Get(context.Background(), "/configuration", &out); e != nil {
			t.Fatal(e)
		}
		if !probe.deadlines[len(probe.deadlines)-1] {
			t.Fatal("explicit equal-value timeout lost")
		}
		WithTransportManagedTimeout()(c)
		WithRequestTimeout(0)(c)
		if !c.transportManagedTimeout {
			t.Fatal("invalid explicit option cleared provenance")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if e := c.Get(ctx, "/configuration", &out); e != nil {
			t.Fatal(e)
		}
		if !probe.deadlines[len(probe.deadlines)-1] {
			t.Fatal("caller deadline removed")
		}
	}
}
