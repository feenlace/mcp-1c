package dump

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
//
// THE GUARANTEE: the server never SILENTLY serves a generation it could not
// protect. Registration used to be best-effort in the worst possible way — a
// process that could not create its entry logged a slog.Warn and served on, and
// cmd/mcp-1c/main.go pins the default slog handler to LevelError, so that warning
// reached nobody. Meanwhile "no entry in readers/" is exactly what every reaper in
// the arena reads as "nobody is serving this, safe to delete". Measured, on two
// real OS processes, a generation was deleted out from under a serve three
// independent ways.
//
// The answer is NOT to refuse. A cache this process may read but not write — a
// root install, a shared team cache, a read-only mount — is an ordinary working
// setup, and refusing there breaks it to guard against a case needing three
// coincidences (an unwritable cache, a peer that CAN write it, and that peer
// actually reaping). The answer is to make the unprotected state impossible to
// miss:
//
//   - a claim that CANNOT BE WRITTEN never refuses. claimOrServeUnprotected serves,
//     and says so in the log AND in the MCP tool response the user is reading (see
//     (*Index).UnprotectedReason). The one exception is a probe that fails with
//     EROFS, where the kernel itself asserts nothing can write here, so there is no
//     protection to lose and nothing to report;
//   - a claim is only accepted once it has been read back through the same
//     os.ReadDir a peer's reaper uses, and once the generation is confirmed to
//     still carry its READY marker AFTER the claim became visible;
//   - generationHasLiveReader distinguishes "no reader is registered" from "the
//     registry cannot be trusted", so a reaper can refuse to act on the second.
//
// None of this is what closed the defect the release is for. That was closed
// structurally: every path that PRODUCES a generation claims it while it is still
// a private temp directory and adopts claim and generation with one rename, so the
// generation is never observable as READY-and-unclaimed. See buildGeneration and
// registerReader.
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

	// readerProbePrefix names the throwaway file a REAPER writes to prove the
	// registry is writable before it trusts an empty registry to mean "unheld"
	// (see registryTrustworthy). It is created and removed within one call; the
	// prefix exists so a probe left behind by a process killed mid-check is never
	// miscounted as a live reader's claim, and so it is recognisable on disk.
	readerProbePrefix = ".probe-"

	// reapProbePrefix names the throwaway file a READER writes into the generations
	// arena to find out whether the arena is on a read-only FILESYSTEM (see
	// arenaWriteProbe). It is the mirror of readerProbePrefix — that one asks "could
	// a peer have claimed?", this one asks "is writing here refused by the mount
	// rather than by our credentials?" — and it is a distinct prefix so the two are
	// told apart on disk. It is created and removed within one call, and it goes into
	// g/, not into a generation, so no registry scan ever sees it.
	reapProbePrefix = ".reapprobe-"
)

// readerRegistration is a live handle on this process's reader entry for one
// generation. It heartbeats the entry's mtime in the background so other processes'
// GC can tell a live reader still holds the generation; Close stops the heartbeat
// and removes the entry. The zero value is not usable; obtain one via registerReader.
type readerRegistration struct {
	// path is where the entry lives once the generation is published. For a claim
	// taken inside a build's private temp dir it is the POST-adopt location, which
	// is why the heartbeat only starts after the adopt.
	path string
	// tmpPath is where the entry was actually created. It equals path for every
	// claim taken on an already-published generation.
	tmpPath string
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	// started records whether the heartbeat goroutine is running, i.e. whether the
	// entry has reached path. Close consults it for two reasons: there is no done
	// channel to wait on before start, and an un-started claim must be removed from
	// tmpPath, which is where it still is.
	started atomic.Bool
	// lost latches the first time the heartbeat finds the entry gone, so the "this
	// serve is no longer protected" alarm is raised once rather than every
	// readerHeartbeatInterval.
	lost atomic.Bool
	// claimless marks a registration that records NOTHING on disk, because the claim
	// could not be written. path and tmpPath are empty, there is no entry, the
	// heartbeat never starts and Close removes nothing.
	//
	// THIS IS THE ONE SHAPE THAT MUST NEVER BE HANDED OUT SILENTLY. A registration
	// that holds nothing while callers treat it as a hold is precisely the silent
	// degradation the whole registry exists to prevent, so it is constructed in
	// exactly one place — claimlessRegistration, called only by
	// claimOrServeUnprotected — and never from a nil check or a fallback.
	claimless bool
	// unprotected is what the USER is told about a claimless registration, and it is
	// non-empty for every one of them EXCEPT the read-only-filesystem case, where
	// there is nothing to tell: EROFS is the kernel asserting that no process can
	// write here, so no reaper exists to protect the generation from.
	//
	// It is what (*Index).UnprotectedReason surfaces into the MCP tool response. An
	// empty string on a claimless registration is therefore a positive claim that the
	// serve is provably safe, not an absence of information.
	unprotected string
}

// claimlessRegistration returns the registration described on the claimless field:
// one that holds no entry because none could be written. unprotected is the reason
// the user must be told, or "" when the filesystem itself proves the generation
// cannot be removed and there is nothing to tell. A caller other than
// claimOrServeUnprotected has no business calling this.
//
// The channels are made even though nothing sends on them. A registration whose
// stop/done are nil is a footgun rather than a saving: close(nil) panics and a
// receive on nil blocks for ever, so a future edit that reached the heartbeat
// teardown for one of these would crash or hang instead of failing an assertion.
func claimlessRegistration(unprotected string) *readerRegistration {
	return &readerRegistration{
		claimless:   true,
		unprotected: unprotected,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// unprotectedReason returns why the generation this registration stands for is
// being served without protection, or "" when it is protected — either by a real
// claim, or by a filesystem on which nothing can be removed.
func (r *readerRegistration) unprotectedReason() string {
	if r == nil {
		return ""
	}
	return r.unprotected
}

// registerReader records this process as a live reader of the ALREADY-PUBLISHED
// generation under genDir and starts its heartbeat. See claimReader for what has
// to hold before a claim is accepted.
//
// RESIDUAL WINDOW, DOCUMENTED AND STILL OPEN — do not read the guards below as
// closing it. Claiming a generation that is ALREADY in the arena cannot be made
// atomic from this side: between the instant it became READY and the instant this
// claim lands, it is visible, unclaimed, and therefore a legal target for every
// reaper in the arena. The claim/READY barrier below, plus the reaper's
// claim-by-rename (claimGenerationForRemoval), make that window FAIL-CLOSED and
// LOUD — the loser refuses to serve and says why — but they do not remove it.
//
// WHAT NO LONGER SITS INSIDE IT. Every path that PRODUCES the generation it is
// about to serve now claims it while it is still private and adopts the two
// together, so none of them reaches this function: the build (buildGeneration
// with withClaim), the flat-cache adopt (adoptFlatShards), the --reindex cold
// rebuild (forceRebuildGeneration) and the serve open built on them
// (PrepareServeGeneration). A generation this process produced is therefore never
// observable as READY-and-unclaimed, so a reaper never has anything to take and
// never renames it away underneath the open.
//
// WHAT STILL DOES, and cannot be moved out of it. A generation this process did
// NOT produce — one already READY in the arena when the open started, built by a
// co-located process or by a previous run — has no private phase this process
// could have claimed it in. It has been sitting unclaimed for as long as it has
// existed, so the exposure is not a window this code opens but the generation's
// whole idle life. What this function does about it is claim the generation, verify
// the claim is visible and that READY survived, and return an error when either
// fails. PrepareServeGeneration's ready fast path and OpenForServe's are the two
// callers that land here — both through claimOrServeUnprotected, which is what
// decides what a FAILED claim means, and which never turns one into a refusal.
func registerReader(genDir string) (*readerRegistration, error) {
	reg, err := claimReader(genDir, genDir)
	if err != nil {
		return nil, err
	}
	reg.start()
	return reg, nil
}

// claimOrServeUnprotected takes the ordinary post-adopt reader claim on the
// already-published generation genDir and, when that claim cannot be written,
// decides what the user is told about serving without one. It is the single place
// that decision is made; every path that claims a generation this process did not
// produce goes through it. IT NEVER FAILS, and that is its whole shape: a claim
// that cannot be written is not a reason to stop serving.
//
// WHY NOT REFUSE. The claim exists for exactly one reason: a reaper deletes a READY
// generation that nothing records as held. Refusing when the claim cannot be
// written breaks every cache this process may read but not write — a root install,
// a shared team cache, a read-only mount — to guard against a case that needs three
// coincidences: an unwritable cache, a peer that CAN write it, and that peer
// actually reaping. On Unix even then the served process keeps answering correctly
// out of unlinked inodes. What was actually broken in the shipped release is not
// that such a cache serves; it is that it served SILENTLY, because the warning sat
// below the effective log level. So it serves, and it is made visible.
//
// WHAT IT DOES NOT DO ANY MORE, because the measurement killed it. It used to allow
// a silent claim-less serve against a "proof" assembled from os.Stat().Mode().Perm()
// — a permission-refused probe plus a mode with no write bit for anyone. That proof
// is FALSE. Measured on three fixtures all reporting mode 0555: the one carrying an
// ACL granting write to a group was declared unreapable, and the real reap — the
// same os.Rename out of the arena that claimGenerationForRemoval performs — then
// succeeded on a twin fixture, emptied the arena, and left the mode reading 0555.
// Mode().Perm() does not describe ACLs, and ~/Library/Caches on macOS carries one
// already. A mode bit is evidence about nothing here and is no longer consulted.
//
// It returns exactly one of:
//   - a live registration holding a real entry: the ordinary outcome, silent;
//   - a claimless registration with NO reason, when the write probe failed with
//     EROFS: the kernel asserts nobody can write in this filesystem, so there is no
//     reaper and nothing to report;
//   - a claimless registration carrying the reason, for every other claim failure:
//     served, logged at ERROR, and surfaced in the MCP tool response.
func claimOrServeUnprotected(genDir string) *readerRegistration {
	reg, claimErr := registerReader(genDir)
	if claimErr == nil {
		return reg
	}
	// The parent of a generation directory IS the generations arena; deriving it
	// here rather than plumbing a cpath through keeps this callable from every claim
	// site with what those sites already have.
	gensDir := filepath.Dir(genDir)
	probeErr := arenaWriteProbe(gensDir)
	if probeProvesReadOnlyFilesystem(probeErr) {
		// Nothing to warn about, so nothing is warned about. A warning here would be
		// the same defect as a refusal, just quieter: it would train the user to
		// ignore the line that matters.
		slog.Info("dump: serving an index generation without a reader claim; the cache is on a "+
			"read-only filesystem, so no process can remove the generation",
			"gen", filepath.Base(genDir), "arena", gensDir)
		return claimlessRegistration("")
	}
	reason := fmt.Sprintf("the reader claim on the index generation %s could not be written (%v), "+
		"and the generations arena %s is not on a read-only filesystem, so another process may "+
		"remove the generation while it is being served", filepath.Base(genDir), claimErr, gensDir)
	// slog.Error, and not because the server is failing — it is serving and the
	// answers are correct. It is Error because it is the ONE line that says the serve
	// is unprotected, and cmd/mcp-1c/main.go pins the default handler to LevelError,
	// so a Warn here is written nowhere in exactly the stdio mode where this arises.
	// MEASURED: as a Warn it produced zero lines across the read-only-cache runs that
	// it describes. The level costs nothing in client noise either, because in stdio
	// mode this handler already writes to a file rather than to the client's pipe.
	//
	// The log is NOT the whole answer. This same state is put in front of the user in
	// the MCP tool response, because a log line under the cache directory is not
	// where anyone running a search is looking. See (*Index).UnprotectedReason.
	slog.Error("dump: serving an index generation without a reader claim: the claim cannot be "+
		"written and nothing establishes that the generation is safe from removal. The server is "+
		"answering from this generation now, but another process may remove it while it is in use, "+
		"and if the dump changes a cache that cannot be written cannot build the new generation. "+
		"Give this server its own cache directory (MCP_1C_CACHE_DIR / --cache-dir), or make the "+
		"cache directory writable.",
		"gen", filepath.Base(genDir), "arena", gensDir, "claim_error", claimErr, "probe_error", probeErr)
	return claimlessRegistration(reason)
}

// claimReader writes this process's claim into workDir's reader registry and
// returns a registration naming where that entry lives, or WILL live, under
// finalDir. The heartbeat is NOT running yet; the caller starts it with start().
//
// The two directories differ for exactly one caller: a build that claims its
// generation while it is still a private temp directory and only then renames it
// into the arena. That is the only way to publish a generation that is never, at
// any instant, observable as READY-and-unclaimed. For every other caller
// workDir == finalDir and the checks below are what make the claim safe.
//
// It FAILS rather than degrading. Four things must hold, and each of them was a
// way the old best-effort version lost a generation that was being served:
//
//  1. The readers/ directory is created with a SINGLE-LEVEL os.Mkdir, never
//     os.MkdirAll (see ensureReaderRegistryDir). MkdirAll recreates the parent
//     chain, so registering against a generation a reaper had just deleted
//     RESURRECTED the generation directory as an empty shell; the reload that
//     followed then attached ZERO shards (cacheShardDirs finds none,
//     openCachedShards returns no error for an empty list) and reported success.
//
//  2. workDir must carry its READY marker. A generation without READY has either
//     not been published yet or has already been claimed for removal by a reaper
//     (GCGenerations renames it out of the arena before deleting it), and neither
//     is something to serve.
//
//  3. The entry must be read back through os.ReadDir — the same call a peer's
//     reaper uses. A claim nothing can list is not a claim.
//
//  4. READY must STILL be present after the entry is visible. Together with the
//     reaper's claim-by-rename this makes the outcome decidable: either the
//     reaper's scan sees this entry and it backs off, or its rename happened
//     first and this re-read fails.
func claimReader(workDir, finalDir string) (*readerRegistration, error) {
	gen := filepath.Base(finalDir)

	// Order matters. The Mkdir runs FIRST so that a generation which no longer
	// exists is reported as the ENOENT it is, by the one call that can tell —
	// rather than being absorbed into the READY check, which cannot distinguish a
	// deleted generation from an unpublished one.
	readersDir, err := ensureReaderRegistryDir(workDir)
	if err != nil {
		return nil, fmt.Errorf("creating the reader registry dir for generation %s: %w", gen, err)
	}
	if !generationReadyDir(workDir) {
		return nil, fmt.Errorf("generation %s carries no %s marker, so it cannot be claimed "+
			"(it was never published, or a concurrent reaper claimed it for removal)", gen, readySentinelName)
	}

	f, err := os.CreateTemp(readersDir, strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		return nil, fmt.Errorf("creating the reader registry entry for generation %s: %w", gen, err)
	}
	// The body is advisory (for debugging); the file's mtime is the liveness signal.
	fmt.Fprintf(f, "pid=%d\nstarted=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_ = f.Close()

	entry := filepath.Base(f.Name())
	if err := readerClaimVisible(readersDir, entry); err != nil {
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("confirming the reader claim on generation %s: %w", gen, err)
	}
	// The barrier. READY is re-read only now, AFTER the claim is visible to a
	// peer's scan, so a reaper that renamed the generation away in between is
	// detected here rather than after this process has started serving it.
	if !generationReadyDir(workDir) {
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("generation %s lost its %s marker while this process was claiming it "+
			"(a concurrent reaper removed it); retry the open", gen, readySentinelName)
	}

	return &readerRegistration{
		path:    filepath.Join(finalDir, readersDirName, entry),
		tmpPath: f.Name(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

// ensureReaderRegistryDir creates genDir's readers/ subdirectory if it is not
// already there and returns its path. It exists as a named function because the
// property it carries is load-bearing and easy to undo by accident: the create is
// SINGLE-LEVEL, so a genDir that no longer exists yields ENOENT instead of being
// recreated as an empty shell around a generation that has been reaped. Never
// change this to os.MkdirAll.
func ensureReaderRegistryDir(genDir string) (string, error) {
	readersDir := filepath.Join(genDir, readersDirName)
	if err := os.Mkdir(readersDir, 0o755); err != nil && !os.IsExist(err) {
		return "", err
	}
	return readersDir, nil
}

// readerClaimVisible re-reads the registry through os.ReadDir and reports whether
// the just-written entry is listed. A registry that is writable but not readable,
// or an entry that never landed, would leave every peer's reaper seeing an empty
// registry — which is precisely what it reads as "safe to delete".
func readerClaimVisible(readersDir, name string) error {
	entries, err := os.ReadDir(readersDir)
	if err != nil {
		return fmt.Errorf("re-reading the reader registry %s: %w", readersDir, err)
	}
	for _, e := range entries {
		if e.Name() == name {
			return nil
		}
	}
	return fmt.Errorf("the reader registry entry %s is not listed by %s, so no other process can see this claim",
		name, readersDir)
}

// start begins heartbeating the claim. It must be called once the entry actually
// lives at reg.path: immediately for a claim taken on a published generation, and
// after the adopt for a claim taken inside a build's temp dir.
//
// A claimless registration has no entry, so there is nothing to heartbeat and no
// path to touch; starting one would raise the lost-claim alarm every interval about
// a claim that was never taken.
func (r *readerRegistration) start() {
	if r == nil || r.claimless || !r.started.CompareAndSwap(false, true) {
		return
	}
	go r.heartbeat()
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
			if err := os.Chtimes(r.path, now, now); err != nil {
				// The entry is gone. This process is serving a generation nothing
				// records it as holding, so any reaper in the arena is free to delete
				// it. This is the last line of defence behind the claim and reaper
				// guards, and it must be AUDIBLE: cmd/mcp-1c/main.go pins the default
				// slog handler to LevelError, so anything below Error goes nowhere.
				// Latched, so one incident is one line.
				//
				// A reaper that took the generation by rename and then rolled the
				// rename back also lands here. That is not a false alarm: a reaper did
				// try to remove a generation this process is serving, and the operator
				// should know the arena is contended.
				if r.lost.CompareAndSwap(false, true) {
					slog.Error("dump: this process no longer holds a reader claim on the index "+
						"generation it is serving; another process may delete that generation while "+
						"it is being served. Restart the server, or give it its own cache directory "+
						"(MCP_1C_CACHE_DIR / --cache-dir).",
						"entry", r.path, "error", err)
				}
			}
		}
	}
}

// Close stops the heartbeat and removes this reader's registry entry. It is safe
// to call multiple times, on a nil registration, and on one whose heartbeat was
// never started — a claim taken inside a build temp dir that was then abandoned
// is removed from where it still is (tmpPath) rather than from where it would
// have gone. Closing an un-started registration must not block: there is no
// heartbeat goroutine to wait for, so there is no done channel to receive from.
func (r *readerRegistration) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		if r.claimless {
			return // no entry was ever written; there is nothing to deregister
		}
		if !r.started.Load() {
			_ = os.Remove(r.tmpPath)
			return
		}
		close(r.stop)
		<-r.done
		_ = os.Remove(r.path)
	})
}

// generationHasLiveReader reports whether any LIVE reader currently holds the
// generation under genDir, reaping stale (dead) entries as a side effect. An entry
// is live if its mtime is within readerStaleAfter; a staler entry belongs to a
// crashed/exited reader and is removed so it can never wedge GC.
//
// THE ERROR IS THE POINT. It used to return a bare bool, so "the registry says
// nobody is holding this" and "the registry could not be read at all" were the
// same answer — and both let a reaper delete a generation another process was
// serving. A non-nil error means the registry could not be trusted to answer;
// every caller MUST treat that as "do not remove", never as "unheld". A MISSING
// readers/ directory is NOT an error: that is the ordinary state of a generation
// nobody has ever opened, and it is genuinely reapable.
func generationHasLiveReader(genDir string) (bool, error) {
	readersDir := filepath.Join(genDir, readersDirName)
	entries, err := os.ReadDir(readersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // no registry: nobody has ever claimed this generation
		}
		return false, fmt.Errorf("reading the reader registry %s: %w", readersDir, err)
	}
	cutoff := time.Now().Add(-readerStaleAfter)
	live := false
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), readerProbePrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue // the entry vanished mid-scan: its holder deregistered
			}
			// The entry exists but its age is unknowable, so it can be neither
			// trusted as live nor dismissed as dead.
			return false, fmt.Errorf("stat of reader registry entry %s: %w", e.Name(), err)
		}
		if info.ModTime().Before(cutoff) {
			// Stale → the reader is gone. Reap it (best-effort) and keep scanning so
			// all dead entries are cleared, not just the first.
			_ = os.Remove(filepath.Join(readersDir, e.Name()))
			continue
		}
		live = true
	}
	return live, nil
}

// registryTrustworthy reports whether an EMPTY answer from generationHasLiveReader
// may be acted on, i.e. whether a peer that wanted to claim this generation could
// actually have done so. It returns an error when it could not.
//
// This is the shape no bool could express, and it was measured: readers/ was made
// unwritable, the co-located serving process therefore registered nothing, and a
// reaper read the resulting empty registry as "unheld" and deleted the generation
// out from under it. An empty registry only means "unheld" if the registry works.
//
// The proof is a create-and-remove probe, which is the portable way to ask "is
// this writable" without racing an access(2)-then-open. When readers/ does not
// exist yet the probe goes into the generation directory instead, since that is
// where a claimant would have to create readers/.
//
// LIMIT, stated rather than hidden: this proves the registry is writable BY THIS
// PROCESS. On a cache arena shared between UNIX users it cannot prove the same for
// a peer running as a different user, so a peer that could not register is still
// invisible to this reaper. That residual case is not closed; it is REPORTED, from
// the other end — the peer serves, and says in its own tool responses that its
// generation is unprotected (claimOrServeUnprotected).
func registryTrustworthy(genDir string) error {
	dir := filepath.Join(genDir, readersDirName)
	if _, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat of the reader registry %s: %w", dir, err)
		}
		dir = genDir
	}
	f, err := os.CreateTemp(dir, readerProbePrefix)
	if err != nil {
		return fmt.Errorf("the reader registry under %s is not writable, so a process that wanted to "+
			"record that it is serving this generation could not have: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("removing the reader registry write probe %s: %w", name, err)
	}
	return nil
}

// arenaWriteProbe tries to create and remove a throwaway file in the generations
// arena gensDir, and returns the error the attempt produced — nil when it worked.
//
// WHY THE ARENA IS THE RIGHT DIRECTORY TO ASK ABOUT. Every removal of a generation
// in this package goes through gensDir: GCGenerations renames the generation out of
// the arena (claimGenerationForRemoval) before deleting it, forceDropGeneration
// reuses that same removal, and ReapStaleBuildDirs unlinks its targets there too.
// All three need to create, rename and unlink entries IN gensDir.
//
// WHY IT WRITES INSTEAD OF INSPECTING. Because writing is the only thing that
// answers. os.Stat().Mode().Perm() cannot see ACLs, and it is not a rounding error:
// measured on this machine, a directory reporting mode 0555 with an ACL granting a
// group write took the real reap — os.Rename of the generation out of the arena,
// then RemoveAll — and still reported 0555 afterwards. It also fails the other way:
// measured on a real read-only mount (a macOS UDRO image), the directory reports
// mode 0755, owner write bit SET, and the write fails with EROFS. The probe is
// created and removed inside this call.
func arenaWriteProbe(gensDir string) error {
	f, err := os.CreateTemp(gensDir, reapProbePrefix)
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	if rmErr := os.Remove(name); rmErr != nil {
		// The create worked, so the arena takes writes; a failure to clean up is not
		// evidence of the opposite and must not be reported as a refused probe.
		slog.Warn("dump: removing the generations-arena write probe", "probe", name, "error", rmErr)
	}
	return nil
}

// probeProvesReadOnlyFilesystem reports whether probeErr is the kernel asserting
// that NOTHING can write in this filesystem — which is the one and only case where
// a generation that could not be claimed is provably safe to serve anyway.
//
// EROFS IS A PROPERTY OF THE MOUNT, NOT OF OUR CREDENTIALS, and that is why it is
// the only accepted proof. A permission refusal says "not you"; someone else — the
// owner, an ACL holder, root — may still write, and their reaper reads our missing
// claim as "unheld". EROFS says "nobody, in this mount namespace", and it does so
// regardless of what the mode bits happen to read. That distinction is measured in
// both directions: a real read-only mount reports mode 0755 and fails with EROFS,
// while a 0555 directory carrying an ACL is fully reapable. The probe, never the
// mode, is what decides.
//
// WHAT IT STILL DOES NOT COVER, stated rather than left to be discovered. EROFS
// binds this mount namespace only, so a host process reaching the same files
// through a writable mount — a read-only container bind mount — can still reap. A
// process there is unprotected and is not told so. That is the accepted residual;
// every other unwritable arena, including one this process merely lacks permission
// on, is reported to the user.
//
// It is split from arenaWriteProbe so it can be pinned without a read-only mount,
// which no unit test can portably create.
func probeProvesReadOnlyFilesystem(probeErr error) bool {
	return errors.Is(probeErr, syscall.EROFS)
}
