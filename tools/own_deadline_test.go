package tools

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/onec"
)

// ---------------------------------------------------------------------------
// THIS SERVER'S OWN WAIT LIMIT IS NOT 1С BEING OUT OF REACH, and both used to be
// rendered as the second: onec/client.go do() wraps every failure of
// HTTPClient.Do in a *onec.TransportError without inspecting it, and
// tools/toolerror.go renderFailure answered that class with remedyUnreachable,
// a checklist about the address, the web server, the network and the firewall.
//
// The discriminator is errors.Is(err, context.DeadlineExceeded) and NOT
// net.Error.Timeout(). deadlinePredicates below measures both on every row, so
// the difference is asserted rather than remembered.
// ---------------------------------------------------------------------------

// Fragments of the two answers, taken from what the model reads rather than
// from the constants, so a rename cannot make these tests vacuous.
const (
	deadlineFragment    = "истёк лимит ожидания"
	deadlineFlagRemedy  = "`--request-timeout`"
	deadlineValueLead   = "текущее значение"
	unreachableFragment = "До 1С не удалось достучаться"
	unreachableFirewall = "брандмауэр"
)

// urlErrOf is the shape net/http hands back: a *url.Error around the cause.
func urlErrOf(inner error) error {
	return &url.Error{Op: "Post", URL: "http://server/query", Err: inner}
}

// dialErrOf is the shape a failed dial has: *url.Error around *net.OpError.
func dialErrOf(inner error) error {
	return urlErrOf(&net.OpError{Op: "dial", Net: "tcp", Err: inner})
}

// deadlinePredicates reports what each predicate says about one error.
func deadlinePredicates(err error) (isDeadline, netTimeout bool) {
	var ne net.Error
	if errors.As(err, &ne) {
		netTimeout = ne.Timeout()
	}
	return errors.Is(err, context.DeadlineExceeded), netTimeout
}

// TestRenderFailure_OwnDeadlineIsNotUnreachability drives the four transport
// causes that reach this renderer and pins which answer each one gets.
//
// The ETIMEDOUT row is the one that separates the two predicates: a node that
// never answered reports Timeout() true, so a renderer keyed on Timeout() would
// announce somebody else's unreachability as this server's own limit.
func TestRenderFailure_OwnDeadlineIsNotUnreachability(t *testing.T) {
	cases := []struct {
		name           string
		inner          error
		wantIsDeadline bool
		wantNetTimeout bool
		wantDeadline   bool
	}{
		{
			name:           "our own deadline",
			inner:          urlErrOf(context.DeadlineExceeded),
			wantIsDeadline: true,
			wantNetTimeout: true,
			wantDeadline:   true,
		},
		{
			name:           "connection refused",
			inner:          dialErrOf(os.NewSyscallError("connect", syscall.ECONNREFUSED)),
			wantIsDeadline: false,
			wantNetTimeout: false,
			wantDeadline:   false,
		},
		{
			name:           "system ETIMEDOUT",
			inner:          dialErrOf(os.NewSyscallError("connect", syscall.ETIMEDOUT)),
			wantIsDeadline: false,
			wantNetTimeout: true,
			wantDeadline:   false,
		},
		{
			name:           "DNS no such host",
			inner:          dialErrOf(&net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}),
			wantIsDeadline: false,
			wantNetTimeout: false,
			wantDeadline:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// CONTROL, and it is what keeps the row discriminating: the two
			// predicates disagree on the ETIMEDOUT row, and a toolchain on which
			// they stopped disagreeing would make that row prove nothing.
			isDeadline, netTimeout := deadlinePredicates(c.inner)
			if isDeadline != c.wantIsDeadline || netTimeout != c.wantNetTimeout {
				t.Fatalf("predicates moved: errors.Is(DeadlineExceeded)=%v (want %v), "+
					"net.Error.Timeout()=%v (want %v) for %v",
					isDeadline, c.wantIsDeadline, netTimeout, c.wantNetTimeout, c.inner)
			}

			got := renderFailure(headingQuery, &onec.TransportError{
				Base: "http://server", Endpoint: "/query", Err: c.inner,
			})

			hasDeadline := strings.Contains(got, deadlineFragment)
			hasUnreachable := strings.Contains(got, unreachableFragment)
			if hasDeadline != c.wantDeadline {
				t.Errorf("the deadline answer is %v, want %v:\n%s", hasDeadline, c.wantDeadline, got)
			}
			if hasUnreachable == c.wantDeadline {
				t.Errorf("the unreachability answer is %v, want %v:\n%s",
					hasUnreachable, !c.wantDeadline, got)
			}
			if c.wantDeadline {
				if !strings.Contains(got, deadlineFlagRemedy) {
					t.Errorf("the deadline answer does not say how to raise the limit:\n%s", got)
				}
				if strings.Contains(got, unreachableFirewall) {
					t.Errorf("the deadline answer still sends the reader to the firewall:\n%s", got)
				}
			}
		})
	}
}

// TestOwnDeadlineWithNoValueNamesNoValue covers the TransportError that carries
// no timeout: a zero would print as «0s» and state a limit nobody set.
func TestOwnDeadlineWithNoValueNamesNoValue(t *testing.T) {
	got := renderFailure(headingQuery, &onec.TransportError{
		Base: "http://server", Endpoint: "/query", Err: urlErrOf(context.DeadlineExceeded),
	})
	// CONTROL: the answer really is the deadline one, or the absence below is free.
	if !strings.Contains(got, deadlineFragment) {
		t.Fatalf("this is not the deadline answer at all:\n%s", got)
	}
	if strings.Contains(got, deadlineValueLead) {
		t.Errorf("a timeout value is announced for an error that carries none:\n%s", got)
	}
}

// TestOwnDeadlineSurvivesTheRealClientChain drives a real onec.Client against a
// real listener, so it measures the whole wrapper chain: http.Client, url.Error,
// ScrubbedURLError and TransportError.Unwrap. The table above builds its own
// inputs and cannot see a break in any of them.
func TestOwnDeadlineSurvivesTheRealClientChain(t *testing.T) {
	// The handler never answers within the client's limit. It is released at
	// cleanup, before Close, because httptest.Server.Close waits for handlers
	// that are still running.
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-released:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(released)
		srv.Close()
	})

	cases := []struct {
		name      string
		client    func() *onec.Client
		ctx       func(t *testing.T) context.Context
		wantValue string
	}{
		{
			name: "http.Client.Timeout",
			client: func() *onec.Client {
				return onec.NewClient(srv.URL, "", "", onec.WithRequestTimeout(50*time.Millisecond))
			},
			ctx:       func(*testing.T) context.Context { return context.Background() },
			wantValue: "50ms",
		},
		{
			name: "credentials on BaseURL, so ScrubbedURLError rebuilds the error",
			client: func() *onec.Client {
				c := onec.NewClient(srv.URL, "", "", onec.WithRequestTimeout(50*time.Millisecond))
				c.BaseURL = strings.Replace(srv.URL, "http://", "http://svcadmin7:p4ssw0rdZZ@", 1)
				return c
			},
			ctx:       func(*testing.T) context.Context { return context.Background() },
			wantValue: "50ms",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out map[string]any
			err := c.client().Post(c.ctx(t), "/query", map[string]any{"query": "ВЫБРАТЬ 1"}, &out)
			if err == nil {
				t.Fatal("the request succeeded, so no deadline was measured")
			}
			// CONTROL: the class really is the transport one, or the assertions
			// below would be about a different branch of the renderer.
			var te *onec.TransportError
			if !errors.As(err, &te) {
				t.Fatalf("errors.As found no *onec.TransportError in %#v (%v)", err, err)
			}

			got := renderFailure(headingQuery, err)
			if !strings.Contains(got, deadlineFragment) {
				t.Errorf("a deadline this server set is still rendered as unreachability:\n%s", got)
			}
			if strings.Contains(got, unreachableFragment) {
				t.Errorf("the unreachability checklist is still shown:\n%s", got)
			}
			if !strings.Contains(got, c.wantValue) {
				t.Errorf("the effective limit %q is not named:\n%s", c.wantValue, got)
			}
			for _, secret := range []string{"svcadmin7", "p4ssw0rdZZ"} {
				if strings.Contains(got, secret) {
					t.Errorf("the answer carries the credential %q:\n%s", secret, got)
				}
			}
		})
	}
}
