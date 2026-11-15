//go:build !windows

package epf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTokenPrivateFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	token := strings.Repeat("ab", 32)
	if os.WriteFile(p, []byte(token+"\n"), 0600) != nil {
		t.Fatal("create fixture")
	}
	if got, err := ReadToken(p); err != nil || got != token {
		t.Fatal("private token refused")
	}
	if os.Chmod(p, 0644) != nil {
		t.Fatal("share fixture")
	}
	if _, err := ReadToken(p); err == nil {
		t.Fatal("shared token accepted")
	}
	if os.Chmod(p, 0600) != nil {
		t.Fatal("restore fixture")
	}
	link := p + "-link"
	if os.Symlink(p, link) != nil {
		t.Fatal("symlink fixture")
	}
	if _, err := ReadToken(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := ReadToken(filepath.Dir(p)); err == nil {
		t.Fatal("directory accepted")
	}
	if os.WriteFile(p, []byte("invalid"), 0600) != nil {
		t.Fatal("invalid fixture")
	}
	if _, err := ReadToken(p); err == nil {
		t.Fatal("invalid token accepted")
	}
}

func TestManagedFileFailureStopsSessionAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code")
	b := managedBridge(t, path)
	old := currentCode(t, path)
	session := pair(t, b)
	if os.Chmod(path, 0644) != nil {
		t.Fatal("permission fixture")
	}
	if request(b, "DELETE", "/session", session, "").Code != 503 {
		t.Fatal("Stop must report issuance failure")
	}
	if b.session != "" || b.pairing != "" {
		t.Fatal("failed issuance retained authority")
	}
	if request(b, "POST", "/poll", session, `{"identity":"native-owner"}`).Code != 401 {
		t.Fatal("failed Stop retained session")
	}
	if os.Chmod(path, 0600) != nil {
		t.Fatal("restore permission")
	}
	if request(b, "POST", "/pair", old, `{"protocol":1,"identity":"native-owner"}`).Code != 401 {
		t.Fatal("permission repair revived consumed code")
	}
	b.Close()
	recovered := managedBridge(t, path)
	if currentCode(t, path) == old {
		t.Fatal("recovery reused code")
	}
	_ = pair(t, recovered)
}
func TestManagedFileReplacementAndWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code")
	b := managedBridge(t, path)
	old := currentCode(t, path)
	if os.Rename(path, path+"-retained") != nil || os.WriteFile(path, []byte("unrelated"), 0600) != nil {
		t.Fatal("replacement fixture")
	}
	if request(b, "POST", "/pair", old, `{"protocol":1,"identity":"native-owner"}`).Code != 401 {
		t.Fatal("replacement accepted")
	}
	b.mu.Lock()
	err := b.issueCode()
	b.mu.Unlock()
	if err == nil || b.pairing != "" {
		t.Fatal("replacement issued code")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "unrelated" {
		t.Fatal("replacement overwritten")
	}
	path2 := filepath.Join(t.TempDir(), "code")
	other := managedBridge(t, path2)
	session := pair(t, other)
	other.codeFile.Close()
	if request(other, "DELETE", "/session", session, "").Code != 503 || other.pairing != "" || other.session != "" {
		t.Fatal("closed handle write revived authority")
	}
}
