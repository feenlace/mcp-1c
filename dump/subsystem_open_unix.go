//go:build unix

package dump

import "syscall"

// nonblockOpenFlag is OR'd into the subsystem-file open so the open returns
// immediately if the final component was swapped for a writer-less FIFO (or a
// device) in the check->use window after the pre-open lstat, instead of blocking
// forever; the caller's post-open os.SameFile check then refuses it. Path-component
// containment is enforced by os.Root at the open site, so no O_NOFOLLOW is needed
// here: an escaping symlink at ANY component is refused by os.Root, and a symlinked
// final component is already rejected by the pre-open lstat. A plain regular file is
// unaffected (POSIX: O_NONBLOCK has no effect on regular-file reads).
const nonblockOpenFlag = syscall.O_NONBLOCK
