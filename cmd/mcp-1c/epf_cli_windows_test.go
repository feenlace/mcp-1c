package main

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func writeEPFCLIToken(t *testing.T, path, token string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal("token fixture user unavailable")
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")")
	if err != nil {
		t.Fatal("token fixture descriptor failed")
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal("token fixture path failed")
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal("token fixture creation failed")
	}
	f := os.NewFile(uintptr(h), path)
	_, writeErr := f.WriteString(token)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("token fixture write failed")
	}
}
