//go:build !windows

package epf

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func openPrivateToken(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("EPF token file must be a private regular file (0600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("EPF token file cannot be opened")
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) || !actual.Mode().IsRegular() || actual.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("EPF token file changed")
	}
	return f, nil
}

// openCodeFile holds an exclusive lease without replacing any existing file.
func openCodeFile(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	created := err == nil
	if os.IsExist(err) {
		f, err = os.OpenFile(path, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, false, codeFileError()
	}
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil || validateCodeFile(f, path) != nil {
		f.Close()
		return nil, false, codeFileError()
	}
	return f, created, nil
}

func validateCodeFile(f *os.File, path string) error {
	expected, err := os.Lstat(path)
	if err != nil || !expected.Mode().IsRegular() || expected.Mode().Perm() != 0600 {
		return codeFileError()
	}
	actual, err := f.Stat()
	if err != nil || !actual.Mode().IsRegular() || actual.Mode().Perm() != 0600 || !os.SameFile(expected, actual) {
		return codeFileError()
	}
	return nil
}
