package dump

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
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

// CacheDir resolves the per-dump cache directory for (dumpDir, cacheDir) using the
// same rules as the internal cache path: an explicit cacheDir wins, otherwise the
// platform user cache base (os.UserCacheDir). It returns an error ONLY when no cache
// location can be resolved at all — os.UserCacheDir() fails and no explicit cacheDir
// was given (a scrubbed environment with an unset HOME). That error is the canonical
// "no writable cache" signal BuildGeneration and BuildCache already report, so the
// serve coordinator can branch on it to fall back to the in-memory NewIndex path.
func CacheDir(dumpDir, cacheDir string) (string, error) {
	return cachePath(dumpDir, cacheDir)
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
// cpath (shard_* dirs, manifest.json, serve.lock, ...) WITHOUT touching the
// immutable generations subtree (g/). It is the safe replacement for
// os.RemoveAll(cpath) on every path that discards a flat cache: a flat rebuild must
// never destroy a generation a concurrent read-only serve may hold. Best-effort.
//
// IT DOES NOT TOUCH THE LOGS, and this comment used to say it did. server.log and
// stderr.log are opened by openLogFile in cmd/mcp-1c against the CACHE DIRECTORY,
// while cpath is the per-dump subdirectory one level below it, so a ReadDir of
// cpath cannot see them and a recovery here has never removed one. Measured on a
// real run: the log sits beside the per-dump hash directory, not inside it.
//
// It RETURNS THE NAMES IT ACTUALLY UNLINKED, and only those: an entry whose
// removal failed is left out. That is what lets a caller report a destruction
// instead of announcing one, and it is the difference between a log line an
// operator can act on and a sentence that is true by construction. The list is
// what the callers' "removed" attribute carries.
func removeFlatCacheContents(cpath string) []string {
	if cpath == "" {
		return nil
	}
	entries, err := os.ReadDir(cpath)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		if e.Name() == generationsDirName {
			continue // preserve immutable generations
		}
		if err := os.RemoveAll(filepath.Join(cpath, e.Name())); err == nil {
			removed = append(removed, e.Name())
		}
	}
	slices.Sort(removed)
	return removed
}

// dropFlatCacheForRecovery discards a flat index cache that could not be opened,
// and SAYS WHAT IT DISCARDED. It reports whether the cache dir may now be built
// into.
//
// TWO THINGS WERE WRONG WITH THE RECOVERY IT REPLACES, and they are independent.
//
// THE BLAST RADIUS. It was os.RemoveAll(cpath) over the whole per-dump cache
// dir, so a flat shard that would not open cost every immutable generation in
// g/ beside it. The two are different artefacts with different lifecycles: a
// generation is written once, sealed with a READY sentinel and never mutated,
// while the flat cache is opened read-write and rewritten in place by the
// warm-start diff. Nothing about a bad flat shard is evidence about a
// generation, and this branch's GCGenerations change makes the loss strictly
// larger, because the arena now retains the newest generation per foreign stamp
// instead of just this binary's own.
//
// THE SILENCE. The removal was reported by nothing at all: measured with real
// binaries, `--build-index` over a corrupt flat cache deleted the arena and
// exited 0 printing «Index cache built in 0.1s». An operator following our own
// stale-lock advice («delete serve.lock and retry») lost every generation and
// was told the build had succeeded. So the removal is announced with the names
// that were actually unlinked, and on a terminal launch in Russian too.
//
// IT IS slog.Error AND THAT IS NOT SEVERITY INFLATION, it is the only level this
// binary does not throw away. cmd/mcp-1c/main.go installs four logging
// configurations and TWO of them sit at LevelError: the early default at
// main.go:52, which is the one `--build-index` runs under from start to exit,
// and the MCP pipe launch at main.go:167. A WARN here would be dropped by
// exactly the two configurations in which this message is the operator's only
// evidence, including the offline --build-index the loss was measured on. The
// remaining two (terminal serve, --debug) sit at LevelInfo and print it either
// way.
//
// A FOREIGN HOLDER KEEPS ITS CACHE, and keeps it whole. This is the rule
// e3f540c wrote for the --reindex flat-cache drop, applied here for the same
// reason: a flat cache another live process has memory-mapped must not be
// unlinked under it. It returns false in that case, and the caller must then
// build WITHOUT the cache rather than into it, because buildShardOffline begins
// every shard with os.RemoveAll(<cpath>/shard_N) and would destroy the peer's
// files by a different syscall. This start is simply not durable, which is the
// outcome the reindex precedent chose for the same situation and is strictly
// better than corrupting a peer.
//
// Customer-facing RU: no тире.
func dropFlatCacheForRecovery(cpath, why string) bool {
	if cpath == "" {
		return false
	}
	if pid, present := readCacheLock(cpath); present && pid != os.Getpid() {
		slog.Error("dump: the flat index cache could not be opened, but another process holds "+
			"it; leaving it untouched and building this index in memory instead",
			"path", cpath, "holder_pid", pid, "reason", why)
		if showProgress.Load() {
			fmt.Fprintf(os.Stderr, "Внимание: кэш индекса не открылся, но им пользуется другой процесс "+
				"(pid %d). Кэш оставлен без изменений, индекс для этого запуска построен в памяти. "+
				"Каталог: %s\n", pid, cpath)
		}
		return false
	}

	removed := removeFlatCacheContents(cpath)
	if len(removed) == 0 {
		// THE SENTENCE MATCHES THE ATTRIBUTE OR IT IS NOT WORTH THE ATTRIBUTE. The
		// message below used to announce a removal unconditionally, so a recovery that
		// unlinked nothing at all still printed "removed a flat index cache" beside an
		// EMPTY removed= attribute. An operator reading the prose then believes the
		// cache is gone, and the whole reason removeFlatCacheContents returns the names
		// it actually unlinked is that this line must not be true by construction.
		//
		// Nothing was unlinked, and the two ways of arriving there are not
		// distinguished on purpose: a directory holding only g/ has nothing to remove,
		// and a directory whose every removal failed removed nothing. Both are "nothing
		// was unlinked", and claiming which one it was would be the same invention in a
		// smaller size.
		slog.Error("dump: the flat index cache could not be opened and nothing was unlinked; "+
			"it will be rebuilt. The immutable generations under g/ were kept",
			"path", cpath, "removed", "", "reason", why)
		if showProgress.Load() {
			fmt.Fprintf(os.Stderr, "Внимание: кэш индекса не открылся, но ничего не было "+
				"удалено. Индекс будет построен заново. Каталог: %s. Неизменяемые "+
				"поколения сохранены.\n", cpath)
		}
		return true
	}
	slog.Error("dump: removed a flat index cache that could not be opened; it will be rebuilt. "+
		"The immutable generations under g/ were kept",
		"path", cpath, "removed", strings.Join(removed, " "), "reason", why)
	if showProgress.Load() {
		fmt.Fprintf(os.Stderr, "Внимание: кэш индекса не открылся и был удалён, индекс будет "+
			"построен заново. Каталог: %s. Удалено: %s. Неизменяемые поколения сохранены.\n",
			cpath, strings.Join(removed, " "))
	}
	return true
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
