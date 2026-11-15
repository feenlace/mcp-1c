package epf

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openPrivateToken(path string) (*os.File, error) {
	refused := errors.New("EPF token file must be a private regular file owned by the current user with a protected current-user-only DACL")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, refused
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, refused
	}
	// Inspect and read the same handle; forbid final reparse traversal and concurrent writers/replacement.
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, refused
	}
	f := os.NewFile(uintptr(h), path)
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		return nil, refused
	}
	var attrs windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &attrs) != nil || attrs.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return nil, refused
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, refused
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !privateTokenDACL(sd, user.User.Sid) {
		return nil, refused
	}
	ok = true
	return f, nil
}

func privateTokenDACL(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID) bool {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		return false
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 {
		return false
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || !sid.Equals(user) {
			return false
		}
	}
	return true
}

// Windows share mode leases the file against writers, deletion and replacement.
func openCodeFile(path string) (*os.File, bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, codeFileError()
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, false, codeFileError()
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")")
	if err != nil {
		return nil, false, codeFileError()
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	access := uint32(windows.GENERIC_READ | windows.GENERIC_WRITE | windows.READ_CONTROL)
	h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ, sa, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	created := err == nil
	if err == windows.ERROR_FILE_EXISTS || err == windows.ERROR_ALREADY_EXISTS {
		h, err = windows.CreateFile(name, access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	}
	if err != nil {
		return nil, false, codeFileError()
	}
	f := os.NewFile(uintptr(h), path)
	if validateCodeFile(f, path) != nil {
		f.Close()
		return nil, false, codeFileError()
	}
	return f, created, nil
}

func validateCodeFile(f *os.File, path string) error {
	expected, err := os.Lstat(path)
	if err != nil || !expected.Mode().IsRegular() {
		return codeFileError()
	}
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return codeFileError()
	}
	h := windows.Handle(f.Fd())
	var attrs windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &attrs) != nil || attrs.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return codeFileError()
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return codeFileError()
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !privateTokenDACL(sd, user.User.Sid) {
		return codeFileError()
	}
	return nil
}
