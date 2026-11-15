package epf

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectionConfiguration(t *testing.T) {
	root := t.TempDir()
	l := ConnectionFlags{"a=127.0.0.1:9090", "b=127.0.0.1:9092"}
	f := ConnectionFlags{"a=" + filepath.Join(root, "a"), "b=" + filepath.Join(root, "b")}
	good, err := Connections(l, f, ConnectionFlags{"b=http://localhost:59342"})
	if err != nil || len(good) != 2 || good[0].Endpoint != "epf://a" || good[1].WebOrigin != "http://localhost:59342" {
		t.Fatal("named configuration", err)
	}
	for _, tc := range []struct{ l, f, o ConnectionFlags }{
		{ConnectionFlags{"127.0.0.1:9090", "a=127.0.0.1:9092"}, f, nil},
		{l, ConnectionFlags{f[0]}, nil},
		{l, f, ConnectionFlags{"c=http://localhost"}},
		{ConnectionFlags{l[0], "b=127.0.0.1:9090"}, f, nil},
		{l, ConnectionFlags{f[0], "b=" + filepath.Join(root, "a")}, nil},
		{ConnectionFlags{"A=127.0.0.1:9090"}, ConnectionFlags{f[0]}, nil},
		{ConnectionFlags{"a=localhost:9090"}, ConnectionFlags{f[0]}, nil},
		{l, f, ConnectionFlags{"a=http://localhost/path"}},
	} {
		if _, err := Connections(tc.l, tc.f, tc.o); err == nil {
			t.Fatal("conflicting configuration accepted")
		}
	}
	legacy, err := Connections(ConnectionFlags{"127.0.0.1:9090"}, ConnectionFlags{filepath.Join(root, "code=legacy")}, nil)
	if err != nil || len(legacy) != 1 || !legacy[0].Legacy || legacy[0].Name != "default" || legacy[0].Endpoint != "epf://local" {
		t.Fatal("legacy changed", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "a"), filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := Connections(l, ConnectionFlags{f[0], "b=" + filepath.Join(root, "hardlink")}, nil); err == nil {
		t.Fatal("same file accepted")
	}
}

func TestIndependentConfiguredConnections(t *testing.T) {
	root := t.TempDir()
	configs, err := Connections(ConnectionFlags{"a=127.0.0.1:9090", "b=127.0.0.1:9092"}, ConnectionFlags{"a=" + filepath.Join(root, "a"), "b=" + filepath.Join(root, "b")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bridges := make([]*Bridge, 2)
	for i, c := range configs {
		b, e := NewConnection(c, nil)
		if e != nil {
			t.Fatal(e)
		}
		bridges[i] = b
		t.Cleanup(b.Close)
		stop, e := b.Start("127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(stop)
	}
	a, b := bridges[0], bridges[1]
	codeA, codeB := pairCode(a), pairCode(b)
	if codeA == codeB || request(b, "POST", "/pair", codeA, `{"protocol":1,"identity":"native-owner"}`).Code != 401 {
		t.Fatal("code crossed base")
	}
	sa, sb := pair(t, a), pair(t, b)
	if sa == sb || request(b, "POST", "/poll", sa, `{"identity":"native-owner"}`).Code != 401 {
		t.Fatal("session crossed base")
	}
	for _, address := range []string{"epf://b/configuration", "http://a/configuration", "epf://a/configuration?x=1"} {
		r, _ := http.NewRequest("GET", address, nil)
		if _, e := a.RoundTrip(r); e == nil {
			t.Fatal("foreign endpoint accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		r, _ := http.NewRequestWithContext(ctx, "GET", "epf://a/configuration", nil)
		_, e := a.RoundTrip(r)
		done <- e
	}()
	var task *pending
	select {
	case task = <-a.queue:
		a.queue <- task
	case <-time.After(time.Second):
		t.Fatal("A task unavailable")
	}
	if len(b.queue) != 0 {
		t.Fatal("queue alias")
	}
	if request(b, "POST", "/result/"+task.task.ID, sb, `{"identity":"native-owner","status":200,"body":{}}`).Code != 409 {
		t.Fatal("foreign task accepted")
	}
	if request(a, "DELETE", "/session", sa, "").Code != 204 {
		t.Fatal("A stop")
	}
	if request(b, "POST", "/poll", sb, `{"identity":"native-owner"}`).Code != 204 || pairCode(b) != "" {
		t.Fatal("A stop changed B")
	}
	if request(a, "POST", "/result/"+task.task.ID, sa, `{"identity":"native-owner","status":200,"body":{}}`).Code == 204 {
		t.Fatal("late old result accepted")
	}
	pair(t, a)
	b.mu.Lock()
	b.pairingExpires = time.Now().Add(-time.Second)
	b.mu.Unlock()
	if request(b, "POST", "/poll", sb, `{"identity":"native-owner"}`).Code != 204 {
		t.Fatal("code expiry killed B session")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request did not end")
	}
}

func TestCanonicalEndpointOwnership(t *testing.T) {
	for _, raw := range []string{"epf://a", "epf://local", "epf://a_b-2"} {
		if !IsEndpoint(raw) {
			t.Fatal("canonical endpoint refused")
		}
	}
	for _, raw := range []string{"epf://A", "epf://a/", "epf://a?x=1", "epf://u@a", "epf://a#x", "http://a", "epf://a:9090", "epf://a%2fb"} {
		if IsEndpoint(raw) {
			t.Fatal("noncanonical endpoint accepted")
		}
	}
	configs, err := Connections(ConnectionFlags{"a=127.0.0.1:9090"}, ConnectionFlags{"a=" + filepath.Join(t.TempDir(), "code")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewConnection(configs[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.OwnsEndpoint("epf://a") || b.OwnsEndpoint("epf://b") || b.OwnsEndpoint("epf://local") {
		t.Fatal("endpoint ownership lost")
	}
}
