//go:build unix

package dump

import (
	"os"
	"syscall"
)

// openSubsystemFileFinal opens a subsystem file for reading on unix with the final
// component's symlink-following and blocking-open behaviour disabled, closing the
// check->use (TOCTOU) window a plain os.Open leaves after the caller's pre-open
// lstat:
//
//   - O_NOFOLLOW makes the open fail (ELOOP) if the final component was swapped for
//     a symlink after the lstat, so a link planted in the window cannot redirect the
//     read out of the dump.
//   - O_NONBLOCK makes the open return immediately if the final component was
//     swapped for a writer-less FIFO (or a device) after the lstat, instead of
//     blocking forever; the caller's post-open os.SameFile check then refuses it.
//
// A plain regular file is unaffected by either flag (POSIX: O_NONBLOCK has no effect
// on regular-file reads), so the ordinary path is unchanged.
func openSubsystemFileFinal(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
