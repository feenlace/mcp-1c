package dump

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// cachePath returns the platform-specific cache directory for a dump index.
// Uses os.UserCacheDir():
//
//	macOS: ~/Library/Caches/mcp-1c/<hash>
//	Linux: ~/.cache/mcp-1c/<hash>  (or $XDG_CACHE_HOME)
//	Windows: %LocalAppData%/mcp-1c/<hash>
func cachePath(dumpDir, cacheDir string) (string, error) {
	absDir, err := filepath.Abs(dumpDir)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(absDir))
	hash := hex.EncodeToString(h[:8]) // first 16 hex chars

	if cacheDir != "" {
		return filepath.Join(cacheDir, hash), nil
	}

	cacheBase, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheBase, "mcp-1c", hash), nil
}

// cacheShardDirs returns sorted paths of shard_* subdirectories in cacheDir.
// Returns nil if the directory does not exist or contains no shards.
func cacheShardDirs(cacheDir string) []string {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "shard_") {
			dirs = append(dirs, filepath.Join(cacheDir, e.Name()))
		}
	}
	slices.Sort(dirs)
	return dirs
}

// removeFlatCacheContents removes the LEGACY flat cache artifacts directly under
// cpath (shard_* dirs, manifest.json, serve.lock, the stderr/server logs, ...)
// WITHOUT touching the immutable generations subtree (g/). It is the safe
// replacement for os.RemoveAll(cpath) on the reindex fallback path: a flat rebuild
// must never destroy a generation a concurrent read-only serve may hold. Best-effort.
func removeFlatCacheContents(cpath string) {
	if cpath == "" {
		return
	}
	entries, err := os.ReadDir(cpath)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == generationsDirName {
			continue // preserve immutable generations
		}
		_ = os.RemoveAll(filepath.Join(cpath, e.Name()))
	}
}

// serveLockName is the lock file an Index writes into its cache directory while
// the cache is open. Its presence tells an offline `--build-index` run that a
// server (or another build) is using the cache, so a destructive rebuild does not
// clobber memory-mapped shard files out from under the live process.
const serveLockName = "serve.lock"

// writeCacheLock records this process as the holder of the cache at cpath by
// writing serveLockName with the current PID. Best-effort: callers log and
// continue on error rather than refusing to serve.
func writeCacheLock(cpath string) error {
	if cpath == "" {
		return nil
	}
	if err := os.MkdirAll(cpath, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cpath, serveLockName), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// removeCacheLock removes the cache lock at cpath, but only if it still records
// this process. A foreign lock (another running server/build acquired it after
// us) is left untouched.
func removeCacheLock(cpath string) {
	if cpath == "" {
		return
	}
	lock := filepath.Join(cpath, serveLockName)
	if data, err := os.ReadFile(lock); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid != os.Getpid() {
			return
		}
	}
	_ = os.Remove(lock)
}

// readCacheLock reports the PID recorded in the cache lock at cpath and whether a
// lock is present. A present lock means another process currently has this cache
// open; clobbering it would corrupt that process's mmap'd view and/or race its
// writes. When the lock exists but its contents are not a PID, pid is 0 and
// present is true (treated as in use).
func readCacheLock(cpath string) (pid int, present bool) {
	if cpath == "" {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(cpath, serveLockName))
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, true
	}
	return pid, true
}
