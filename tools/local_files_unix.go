//go:build unix

package tools

import "syscall"

const localNonblock = syscall.O_NONBLOCK
