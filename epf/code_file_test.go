package epf

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func managedBridge(t *testing.T, path string) *Bridge {
	t.Helper()
	b, err := NewFromFile(path, nil)
	if err != nil {
		t.Fatal("managed file construction refused")
	}
	t.Cleanup(b.Close)
	stop, err := b.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal("managed listener refused:", err)
	}
	t.Cleanup(stop)
	return b
}
func currentCode(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(raw), codeFileHeader) || len(raw) != len(codeFileHeader)+65 {
		t.Fatal("managed file format")
	}
	return strings.TrimSuffix(string(raw[len(codeFileHeader):]), "\n")
}
func TestOneTimeCodeAtomicAndSessionExpirySeparation(t *testing.T) {
	b, _ := New(token, nil)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if request(b, "POST", "/pair", token, `{"protocol":1,"identity":"native-owner"}`).Code == 200 {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("one-success admission violated")
	}
	b.mu.Lock()
	session := b.session
	b.pairingExpires = time.Now().Add(-time.Second)
	b.mu.Unlock()
	if request(b, "POST", "/pair", token, `{"protocol":1,"identity":"native-owner"}`).Code != 401 {
		t.Fatal("consumed code accepted")
	}
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 204 {
		t.Fatal("code expiry invalidated established session")
	}
}
func TestInitialCodeDeadlineAndMalformedRetry(t *testing.T) {
	b, _ := New(token, nil)
	deadline := b.pairingExpires
	if request(b, "POST", "/pair", token, `{"protocol":99,"identity":"native-owner"}`).Code != 400 {
		t.Fatal("protocol refusal")
	}
	if b.pairing != token || !b.pairingExpires.Equal(deadline) {
		t.Fatal("bad request changed issuance")
	}
	b.pairingExpires = time.Now()
	if request(b, "POST", "/pair", token, `{"protocol":1,"identity":"native-owner"}`).Code != 401 || b.session != "" {
		t.Fatal("deadline accepted")
	}
}
func TestManagedCodeStopRestartAndLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code")
	b := managedBridge(t, path)
	first := currentCode(t, path)
	if _, err := NewFromFile(path, nil); err == nil {
		t.Fatal("second issuer accepted")
	}
	session := pair(t, b)
	if request(b, "DELETE", "/session", session, "").Code != 204 {
		t.Fatal("stop refused")
	}
	second := currentCode(t, path)
	if second == first || request(b, "POST", "/pair", first, `{"protocol":1,"identity":"native-owner"}`).Code != 401 {
		t.Fatal("Stop revived old code")
	}
	newSession := pair(t, b)
	if request(b, "DELETE", "/session", session, "").Code != 401 || currentCode(t, path) != second {
		t.Fatal("old Stop rotated newer session")
	}
	if request(b, "POST", "/poll", newSession, `{"identity":"native-owner"}`).Code != 204 {
		t.Fatal("newer session lost")
	}
	b.Close()
	next := managedBridge(t, path)
	third := currentCode(t, path)
	if third == second || third == first {
		t.Fatal("restart retained code")
	}
	for _, old := range []string{first, second} {
		if request(next, "POST", "/pair", old, `{"protocol":1,"identity":"native-owner"}`).Code != 401 {
			t.Fatal("restart accepted prior code")
		}
	}
	_ = pair(t, next)
}
func TestManagedFilePreservesInputAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "old-input")
	if os.WriteFile(path, []byte(token+"\n"), 0600) != nil {
		t.Fatal("input fixture")
	}
	if _, err := NewFromFile(path, nil); err == nil {
		t.Fatal("old input adopted")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != token+"\n" {
		t.Fatal("old input changed")
	}
	link := filepath.Join(root, "link")
	if os.Symlink(path, link) == nil {
		if _, err := NewFromFile(link, nil); err == nil {
			t.Fatal("symlink adopted")
		}
	}
}

func TestUnstartedManagedFileCanBeRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code")
	first, err := NewFromFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	code := currentCode(t, path)
	if request(first, "POST", "/pair", code, `{"protocol":1,"identity":"native-owner"}`).Code != 401 {
		t.Fatal("unstarted file activated pairing")
	}
	first.Close()
	second, err := NewFromFile(path, nil)
	if err != nil {
		t.Fatal("unstarted own file blocks retry", err)
	}
	defer second.Close()
	stop, err := second.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if currentCode(t, path) == code {
		t.Fatal("startup reused inactive code")
	}
	pair(t, second)
}
