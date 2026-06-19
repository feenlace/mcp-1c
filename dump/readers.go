package dump

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Liveness-checked multi-holder reader registry.
//
// A read-only serve of an immutable generation registers itself in that
// generation's readers/ subdirectory for the lifetime of the serve, and
// deregisters on Close. The presence of a live reader is what tells old-generation
// GC (GCGenerations) that a generation is still in use and MUST NOT be removed —
// it replaces the single-PID serve.lock, which could only represent one holder and
// could not distinguish a live reader from a stale PID left by a crash.
//
// Liveness is signalled by the entry file's mtime: a registered reader heartbeats
// (re-touches) its entry every readerHeartbeatInterval, so an entry whose mtime is
// older than readerStaleAfter is from a crashed/exited reader and is reaped. This
// is fully portable (no flock, no signal-0 PID probe), works on any filesystem,
// and is crash-safe (a dead reader stops heartbeating and goes stale within a
// bounded window). The build-leader flock layer (instancelock) is a separate,
// deferred concern; this registry only answers "is any live reader holding this
// generation?".
//
// Layout: <cpath>/g/<gensig>/readers/<pid>-<rand>  (one file per live reader)
const (
	readersDirName = "readers"

	// readerHeartbeatInterval is how often a live reader re-touches its registry
	// entry's mtime to prove liveness.
	readerHeartbeatInterval = 10 * time.Second

	// readerStaleAfter is the age past which a registry entry is considered dead
	// (its reader crashed/exited without deregistering) and is reaped. It is a
	// multiple of the heartbeat interval so a live-but-momentarily-slow reader is
	// never false-reaped.
	readerStaleAfter = 3 * readerHeartbeatInterval
)

// readerRegistration is a live handle on this process's reader entry for one
// generation. It heartbeats the entry's mtime in the background so other processes'
// GC can tell a live reader still holds the generation; Close stops the heartbeat
// and removes the entry. The zero value is not usable; obtain one via registerReader.
type readerRegistration struct {
	path string
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// registerReader records this process as a live reader of the generation under
// genDir by creating a unique entry in genDir/readers and starting a heartbeat
// that keeps the entry's mtime fresh. The unique <pid>-<rand> name lets one
// process hold several concurrent reads of the same generation (e.g. a resync
// hot-swap window). Best-effort: on failure the caller logs and serves anyway —
// the only consequence is that GC might reap the generation while it is served
// (benign: a live reader keeps its open shards on unix; the removal fails while
// files are held on Windows).
func registerReader(genDir string) (*readerRegistration, error) {
	readersDir := filepath.Join(genDir, readersDirName)
	if err := os.MkdirAll(readersDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating readers registry dir: %w", err)
	}
	f, err := os.CreateTemp(readersDir, strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		return nil, fmt.Errorf("creating reader registry entry: %w", err)
	}
	// The body is advisory (for debugging); the file's mtime is the liveness signal.
	fmt.Fprintf(f, "pid=%d\nstarted=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_ = f.Close()

	reg := &readerRegistration{
		path: f.Name(),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go reg.heartbeat()
	return reg, nil
}

// heartbeat keeps the registry entry's mtime fresh until Close stops it.
func (r *readerRegistration) heartbeat() {
	defer close(r.done)
	t := time.NewTicker(readerHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case now := <-t.C:
			// Best-effort: if the entry vanished (reaped / cache wiped) there is
			// nothing useful to do — the serve's open shards remain valid.
			_ = os.Chtimes(r.path, now, now)
		}
	}
}

// Close stops the heartbeat and removes this reader's registry entry. It is safe
// to call multiple times and on a nil registration.
func (r *readerRegistration) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		close(r.stop)
		<-r.done
		_ = os.Remove(r.path)
	})
}

// generationHasLiveReader reports whether any LIVE reader currently holds the
// generation under genDir, reaping stale (dead) entries as a side effect. An entry
// is live if its mtime is within readerStaleAfter; a staler entry belongs to a
// crashed/exited reader and is removed so it can never wedge GC. A missing readers/
// directory means no reader ever registered → not held.
func generationHasLiveReader(genDir string) bool {
	readersDir := filepath.Join(genDir, readersDirName)
	entries, err := os.ReadDir(readersDir)
	if err != nil {
		return false
	}
	cutoff := time.Now().Add(-readerStaleAfter)
	live := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			// Stale → the reader is gone. Reap it (best-effort) and keep scanning so
			// all dead entries are cleared, not just the first.
			_ = os.Remove(filepath.Join(readersDir, e.Name()))
			continue
		}
		live = true
	}
	return live
}
