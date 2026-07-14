//go:build !unix

package dump

import "os"

// openSubsystemFileFinal is the portable fallback for platforms without unix's
// O_NOFOLLOW / O_NONBLOCK open flags (e.g. Windows). Containment across the
// check->use window is still enforced by the caller: the pre-open lstat rejects a
// non-regular or symlinked final component, and the post-open os.SameFile check
// rejects a file that was swapped for a different one between the lstat and the open.
func openSubsystemFileFinal(path string) (*os.File, error) {
	return os.Open(path)
}
