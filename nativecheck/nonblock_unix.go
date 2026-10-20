//go:build unix

package nativecheck

import "syscall"

const checkNonblock = syscall.O_NONBLOCK
