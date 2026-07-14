//go:build !unix

package dump

// nonblockOpenFlag is 0 on platforms without unix's O_NONBLOCK (e.g. Windows).
// Containment is enforced by os.Root at the open site; the pre-open lstat rejects a
// non-regular or symlinked final component and the post-open os.SameFile check
// rejects a file swapped for a different one in the check->use window. Windows has
// no mkfifo, so the blocking-FIFO vector O_NONBLOCK guards against does not arise.
const nonblockOpenFlag = 0
