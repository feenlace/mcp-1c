package epf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func tokenTestDescriptor(t *testing.T, text string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(text)
	if err != nil {
		t.Fatal("security descriptor fixture failed")
	}
	return sd
}

func TestWindowsTokenDACL(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal("current user unavailable")
	}
	sid := user.User.Sid.String()
	for _, tc := range []struct {
		name, sddl string
		want       bool
	}{
		{"private", "O:" + sid + "D:P(A;;FA;;;" + sid + ")", true},
		{"read-only", "O:" + sid + "D:P(A;;FR;;;" + sid + ")", true},
		{"shared-read", "O:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;FR;;;WD)", false},
		{"shared-write", "O:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;FW;;;WD)", false},
		{"inheritable", "O:" + sid + "D:(A;;FA;;;" + sid + ")", false},
		{"null", "O:" + sid + "D:NO_ACCESS_CONTROL", false},
		{"empty", "O:" + sid + "D:P", false},
		{"other-owner", "O:WDD:P(A;;FA;;;" + sid + ")", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if privateTokenDACL(tokenTestDescriptor(t, tc.sddl), user.User.Sid) != tc.want {
				t.Fatal("DACL classification mismatch")
			}
		})
	}
}

func TestWindowsReadTokenPrivateFile(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal("current user unavailable")
	}
	sid := user.User.Sid.String()
	token := strings.Repeat("ab", 32)
	root := t.TempDir()
	create := func(name, dacl string) string {
		t.Helper()
		path := filepath.Join(root, name)
		sd := tokenTestDescriptor(t, "O:"+sid+dacl)
		sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
		name16, _ := windows.UTF16PtrFromString(path)
		h, err := windows.CreateFile(name16, windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Fatal("atomic token creation failed")
		}
		f := os.NewFile(uintptr(h), path)
		_, writeErr := f.WriteString(token + "\n")
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("token fixture write failed")
		}
		return path
	}
	private := create("private", "D:P(A;;FA;;;"+sid+")")
	for range 2 {
		if got, err := ReadToken(private); err != nil || got != token {
			t.Fatal("secure token reopen failed")
		}
	}
	shared := create("shared", "D:P(A;;FA;;;"+sid+")(A;;FR;;;WD)")
	if _, err := ReadToken(shared); err == nil {
		t.Fatal("shared token accepted")
	}
	if _, err := ReadToken(root); err == nil {
		t.Fatal("directory accepted")
	}
	name16, _ := windows.UTF16PtrFromString(private)
	h, err := windows.CreateFile(name16, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal("writer fixture failed")
	}
	_, readErr := ReadToken(private)
	windows.CloseHandle(h)
	if readErr == nil {
		t.Fatal("concurrent writer accepted")
	}
	if os.WriteFile(private, []byte("invalid"), 0600) != nil {
		t.Fatal("invalid fixture failed")
	}
	if _, err := ReadToken(private); err == nil {
		t.Fatal("invalid token accepted")
	}
	link := filepath.Join(root, "link")
	if os.Symlink(private, link) != nil {
		t.Skip("symlink creation requires Windows privilege")
	}
	if _, err := ReadToken(link); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestWindowsManagedIssuancePrivacyAndFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code")
	b := managedBridge(t, path)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal("current user unavailable")
	}
	h := windows.Handle(b.codeFile.Fd())
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || !privateTokenDACL(sd, user.User.Sid) {
		t.Fatal("created issuance file is not private")
	}
	old := currentCode(t, path)
	session := pair(t, b)
	b.codeFile.Close()
	if request(b, "DELETE", "/session", session, "").Code != 503 || b.session != "" || b.pairing != "" {
		t.Fatal("failed issuance retained authority")
	}
	if request(b, "POST", "/pair", old, `{"protocol":1,"identity":"native-owner"}`).Code != 401 {
		t.Fatal("failed write revived code")
	}
	b.Close()
	recovered := managedBridge(t, path)
	if currentCode(t, path) == old {
		t.Fatal("recovery retained code")
	}
	_ = pair(t, recovered)
}
