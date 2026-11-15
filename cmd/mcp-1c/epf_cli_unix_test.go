//go:build !windows

package main

import (
	"os"
	"testing"
)

func writeEPFCLIToken(t *testing.T, path, token string) {
	t.Helper()
	if os.WriteFile(path, []byte(token), 0600) != nil {
		t.Fatal("token fixture creation failed")
	}
}
