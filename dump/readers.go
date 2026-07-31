package dump

import (
	"errors"
	"fmt"
	"io/fs"
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
// REGISTRATION IS MANDATORY, NOT BEST-EFFORT. It used to be best-effort: a process
// that could not create its entry logged a slog.Warn and served anyway. That was
// measured to lose a generation three independent ways, because the warning is
// invisible (cmd/mcp-1c/main.go pins the default slog handler to LevelError) and
// because "no entry in readers/" is exactly what every reaper in the arena reads
// as "nobody is serving this, safe to delete". A process that cannot prove its
// generation is protected must refuse loudly instead, so the primitives here all
// fail closed:
//
//   - claimReader returns an ERROR, never a silent degradation, and that error is
//     fatal to the open or reload that asked for it — unless claimOrProveUnreapable
//     can PROVE nothing is able to remove the generation, which is the one case
//     where there is no protection to lose;
//   - a claim is only accepted once it has been read back through the same
//     os.ReadDir a peer's reaper uses, and once the generation is confirmed to
//     still carry its READY marker AFTER the claim became visible;
//   - generationHasLiveReader distinguishes "no reader is registered" from "the
//     registry cannot be trusted", so a reaper can refuse to act on the second.
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
	// arena to find out whether ANY process could remove a generation from it (see
	// arenaUnreapable). It is the mirror of readerProbePrefix — that one asks "could
	// a peer have claimed?", this one asks "could a peer reap?" — and it is a
	// distinct prefix so the two are told apart on disk. It is created and removed
	// within one call, and it goes into g/, not into a generation, so no registry
	// scan ever sees it.
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
	// unreapable, when non-empty, marks a registration that records NOTHING on disk
	// because nothing needed recording: no claim could be written AND it was proven
	// that no process can remove the generation (see arenaUnreapable, which supplies
	// this string as its proof). path and tmpPath are empty, there is no entry, the
	// heartbeat never starts and Close removes nothing.
	//
	// THIS IS THE ONE SHAPE THAT MUST NEVER BE HANDED OUT CASUALLY. A registration
	// that holds nothing while callers treat it as a hold is precisely the silent
	// degradation the whole registry exists to prevent, so it is constructed in
	// exactly one place — unreapableClaim, called only by claimOrProveUnreapable and
	// only after a proof — and never from a nil check, a fallback, or an error path
	// that merely failed to claim.
	unreapable string
}

// unreapableClaim returns the claim-less registration described on the unreapable
// field. reason is the proof arenaUnreapable produced and is kept for diagnostics.
// A caller other than claimOrProveUnreapable has no business calling this.
//
// The channels are made even though nothing sends on them. A registration whose
// stop/done are nil is a footgun rather than a saving: close(nil) panics and a
// receive on nil blocks for ever, so a future edit that reached the heartbeat
// teardown for one of these would crash or hang instead of failing an assertion.
func unreapableClaim(reason string) *readerRegistration {
	return &readerRegistration{
		unreapable: reason,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// unreapableReason returns the proof under which this registration holds no entry,
// or "" for an ordinary registration that holds a real one.
func (r *readerRegistration) unreapableReason() string {
	if r == nil {
		return ""
	}
	return r.unreapable
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
// whole idle life, and the only sound answer is the fail-closed one below: claim
// it, verify the claim is visible and READY survived, and refuse if either fails.
// PrepareServeGeneration's ready fast path and OpenForServe's are the two callers
// that land here — both through claimOrProveUnreapable, which is what decides what
// a FAILED claim means.
func registerReader(genDir string) (*readerRegistration, error) {
	reg, err := claimReader(genDir, genDir)
	if err != nil {
		return nil, err
	}
	reg.start()
	return reg, nil
}

// claimOrProveUnreapable takes the ordinary post-adopt reader claim on the
// already-published generation genDir, and decides what to do when that claim
// cannot be written. It is the single place that decision is made; every path that
// claims a generation this process did not produce goes through it.
//
// WHY A FAILED CLAIM IS NOT AUTOMATICALLY A REFUSAL. The claim exists for exactly
// one reason: a reaper deletes a READY generation that nothing records as held. If
// no process can remove the generation, there is no reaper to protect it from and
// the missing claim protects nothing. Refusing there would make a cache this
// process may read but not write — one published by a root install, a shared team
// cache, a read-only container mount — permanently and silently unservable, which
// is what happened when the claim was first made mandatory.
//
// SO IT PROVES, IT DOES NOT ASSUME. A claim-less serve is permitted ONLY when
// arenaUnreapable establishes that no process can rename or unlink the generation
// directory, which is what every removal in this package does. "Unwritable for us"
// is NOT that proof: an arena its owner can still write is one its owner can still
// reap, and there the refusal stands. What the proof does and does not cover is
// stated in full on arenaUnreapable.
//
// It returns exactly one of:
//   - a live registration holding a real entry, the ordinary outcome;
//   - an unreapable registration holding nothing, when the proof succeeded;
//   - an error, when the claim failed AND the proof did not, which the caller MUST
//     turn into a refusal to serve.
func claimOrProveUnreapable(genDir string) (*readerRegistration, error) {
	reg, claimErr := registerReader(genDir)
	if claimErr == nil {
		return reg, nil
	}
	// The parent of a generation directory IS the generations arena; deriving it
	// here rather than plumbing a cpath through keeps this callable from every claim
	// site with what those sites already have.
	reason, proofErr := arenaUnreapable(filepath.Dir(genDir))
	if proofErr != nil {
		return nil, fmt.Errorf("%w; and it could not be established that the generation is safe to "+
			"serve unclaimed: %v", claimErr, proofErr)
	}
	// slog.Error, and not because this is a failure — the server is serving and the
	// operator need do nothing. It is Error because it is the ONE line that says the
	// cache is frozen, and cmd/mcp-1c/main.go pins the default handler to LevelError,
	// so a Warn here is written nowhere in exactly the stdio mode where this arises.
	// MEASURED: as a Warn it produced zero lines across the read-only-cache and
	// read-only-mount runs that it describes. The level costs nothing in client noise
	// either, because in stdio mode this handler already writes to a file rather than
	// to the client's pipe.
	slog.Error("dump: serving an index generation without a reader claim: the claim cannot be written "+
		"and it is established that no process can remove the generation either. The server is serving "+
		"normally; a read-only cache cannot pick up a changed dump until it is writable again.",
		"gen", filepath.Base(genDir), "proof", reason, "claim_error", claimErr)
	return unreapableClaim(reason), nil
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
// An unreapable registration has no entry, so there is nothing to heartbeat and no
// path to touch; starting one would raise the lost-claim alarm every interval about
// a claim that was never taken.
func (r *readerRegistration) start() {
	if r == nil || r.unreapable != "" || !r.started.CompareAndSwap(false, true) {
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
		if r.unreapable != "" {
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
// a peer running as a different user. That residual case is closed from the other
// end: a peer that cannot register now refuses to serve rather than serving
// unprotected, so there is nothing left to protect.
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

// arenaUnreapable returns the PROOF that no process can remove a generation from
// the generations arena gensDir, or an error saying why no such proof could be
// made. It is what lets a process that cannot write its reader claim tell "nobody
// is protecting this generation" apart from "nothing can touch this generation".
//
// WHAT IT ASKS, AND WHY THAT IS THE RIGHT QUESTION. Every removal of a generation
// in this package goes through gensDir: GCGenerations renames the generation out of
// the arena (claimGenerationForRemoval) before deleting it, forceDropGeneration
// reuses that same removal, and ReapStaleBuildDirs unlinks its targets there too.
// All three need permission to create, rename and unlink entries IN gensDir. So
// "can this generation be reaped" reduces to "can anything write in gensDir", and
// that is a question about one directory, answerable now, rather than a guess about
// which peers might exist.
//
// HOW IT ANSWERS. By writing, because that is the only portable way to learn what a
// write would do — an access(2)-style mode inspection alone answers the wrong
// question, as the read-only-mount case below proves. The probe is created and
// removed inside this call.
//
//   - The probe SUCCEEDS: the arena is writable by this process, so a reaper with
//     the same rights removes generations from it freely. No proof; error.
//   - The probe fails with EROFS: the filesystem is mounted read-only, so nothing in
//     this mount namespace can rename or unlink anything here. PROVEN. This branch
//     is not redundant with the mode check below and cannot be folded into it:
//     MEASURED on a real read-only mount (macOS UDRO image), the directory still
//     reports mode 0755 — owner write bit SET — and the failure is EROFS, not a
//     permission error. A mode-only test would have called that arena writable.
//   - The probe fails with a permission error AND gensDir's mode carries no write
//     bit for its owner, its group or others: no process bound by the permission
//     bits can create, rename or unlink here. PROVEN.
//   - The probe fails with a permission error but the mode DOES grant write to the
//     owner or the group: it is unwritable for US and writable for THEM. A peer
//     running as that owner reaps normally and reads our missing claim as "unheld".
//     No proof; error. This is the case that must not be waved through, and it is
//     why "we cannot write" is not itself an answer.
//   - Any other failure: unknown, so no proof; error.
//
// WHAT THE PROOF DOES NOT COVER, stated rather than left to be discovered. It binds
// processes subject to the checks it made, and nothing else:
//
//   - root bypasses the permission bits entirely and can reap either way;
//   - so can any process that first chmods the arena back to writable;
//   - EROFS binds this mount namespace only, so a host process reaching the same
//     files through a writable mount (a read-only container bind mount) still can;
//   - on Windows directory rights are ACLs and Mode().Perm() does not describe
//     them, so the mode branch never fires there and a process that cannot claim
//     keeps refusing — fail-closed, and no worse than before this existed;
//   - it proves the generation cannot be REAPED, which is the whole of what a reader
//     claim ever bought. It says nothing about the shard files being immutable in
//     general, and neither did the claim.
func arenaUnreapable(gensDir string) (string, error) {
	f, err := os.CreateTemp(gensDir, reapProbePrefix)
	if err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("the generations arena %s is writable by this process, so a reaper "+
			"running with the same rights can remove a generation from it", gensDir)
	}
	// The stat runs before the classification and not inside its permission branch:
	// on a read-only mount it SUCCEEDS and reports an ordinary writable-looking mode,
	// which is the measurement classifyArenaProbe exists to keep from being lost.
	st, statErr := os.Stat(gensDir)
	if statErr != nil {
		return "", fmt.Errorf("stat of the generations arena %s after its write probe was refused: %w",
			gensDir, statErr)
	}
	return classifyArenaProbe(gensDir, err, st.Mode().Perm())
}

// classifyArenaProbe turns a REFUSED write probe on gensDir, whose permission bits
// are perm, into the proof that nothing can remove a generation there — or into the
// reason no such proof exists. See arenaUnreapable for the full argument.
//
// It is split out from the probe so the EROFS branch can be pinned without a
// read-only mount, which no unit test can portably create. The inputs it is fed for
// that branch are not invented: on a macOS UDRO image the probe fails with EROFS
// while os.Stat reports perm 0755, so a mode-only test calls that arena writable.
func classifyArenaProbe(gensDir string, probeErr error, perm fs.FileMode) (string, error) {
	if errors.Is(probeErr, syscall.EROFS) {
		return fmt.Sprintf("the generations arena %s is on a read-only mount, so no process in this "+
			"mount namespace can remove a generation from it", gensDir), nil
	}
	if !errors.Is(probeErr, fs.ErrPermission) {
		return "", fmt.Errorf("probing whether the generations arena %s can be written: %w", gensDir, probeErr)
	}
	if perm&0o222 != 0 {
		return "", fmt.Errorf("the generations arena %s is not writable by this process, but its mode "+
			"%04o grants write to its owner or its group, so a process running as one of them can "+
			"remove a generation from it: %w", gensDir, perm, probeErr)
	}
	return fmt.Sprintf("the generations arena %s carries no write permission for its owner, its group "+
		"or others (mode %04o), so no process bound by those bits can remove a generation from it",
		gensDir, perm), nil
}
