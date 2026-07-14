package dump

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// safeJoin constructs an absolute path by joining segments onto dumpRoot and
// verifies the result cannot escape the root directory, guarding both classic
// ".." path traversal (lexical check) and symlink escape (a crafted symlink whose
// real target leaves the dump). It is the single containment primitive the offline
// subsystem walker uses before opening any file under a dump.
//
// A path that does not exist yet is tolerated (ErrNotExist): a path that is never
// opened cannot escape through a symlink, and the lexical check already enforces
// containment for the name itself. Pure stdlib.
func safeJoin(dumpRoot string, segments ...string) (string, error) {
	rootAbs, err := filepath.Abs(filepath.Clean(dumpRoot))
	if err != nil {
		return "", fmt.Errorf("safeJoin root: %w", err)
	}

	parts := append([]string{rootAbs}, segments...)
	abs, err := filepath.Abs(filepath.Clean(filepath.Join(parts...)))
	if err != nil {
		return "", fmt.Errorf("safeJoin abs: %w", err)
	}

	// Lexical guard: allow the root itself or any path strictly beneath it.
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes dump root", abs)
	}

	// Symlink-aware guard: if abs exists, its real path (symlinks resolved) must
	// still be within the root, so a symlinked component cannot smuggle a read
	// outside the dump.
	if err := symlinkWithinRoot(rootAbs, abs); err != nil {
		return "", err
	}
	return abs, nil
}

// symlinkWithinRoot verifies abs does not escape rootAbs once symbolic links are
// resolved. Both sides are resolved so a symlinked root (e.g. macOS
// /var -> /private/var) compares correctly. ErrNotExist is tolerated: there is
// nothing to open, and the caller's lexical check already enforced containment.
func symlinkWithinRoot(rootAbs, abs string) error {
	realRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		realRoot = rootAbs
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("safeJoin resolve: %w", err)
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(os.PathSeparator)) {
		return fmt.Errorf("path escapes dump root via symlink")
	}
	return nil
}
