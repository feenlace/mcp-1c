package dump

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/blevesearch/bleve/v2"
)

// In-process reload of a dump index.
//
// PROBLEM. A serving index is opened once, at process start, from an immutable
// generation named by the dump's content signature. Everything derived from that
// generation is then frozen for the life of the process: the module name list
// (idx.names / pathByName / pathToDocID), the PathIndex built from it, and the
// bleve shards. Re-running DumpConfigToFiles therefore changes nothing a running
// server can see. The three search modes fail differently, which is why a partial
// fix is not a fix:
//
//   - a module ADDED after the open is invisible to ALL THREE modes, because
//     regex/exact iterate idx.names and smart needs a bleve document;
//   - a module MODIFIED after the open is stale in SMART only (the shard still
//     holds the old text, and still scores it) while regex/exact already read the
//     current file from disk on every query;
//   - a module DELETED after the open still counts toward the SMART total (the
//     document is still in the shard) while regex/exact already skip it, because
//     its file no longer reads.
//
// APPROACH. Reload does NOT mutate the live index. It computes the dump's current
// signature, builds a COMPLETE new generation for it through the ordinary
// BuildGeneration path (temp dir -> READY sentinel -> atomic rename, which never
// writes into a generation a reader holds), opens that generation read-only off to
// the side, and only then swaps it in. Every step before the swap can fail freely:
// nothing the live index serves has been touched yet, so a failed reload leaves
// the previous index serving exactly as before.
//
// COST. The rebuild is a full cold build. A reload is therefore NOT faster than
// restarting the process and rebuilding; it costs the same work. What it buys is
// that the old index keeps answering throughout, and that a client does not have
// to restart the server to see a new dump. The report the tool prints states the
// elapsed time so the caller can judge that for itself rather than being told it
// was fast.
//
// CONCURRENCY. Searches running during a reload are safe by two independent
// mechanisms, either of which suffices:
//
//  1. bleve.IndexAlias.Swap takes the alias WRITE lock, and alias Search holds the
//     alias READ lock for the whole of its call (including the per-shard search).
//     So the swap cannot start while a search is inside the old shards, and no
//     search started after it can reach them.
//  2. bleve's own indexImpl.Close takes its write lock and clears an open flag
//     that Search/Index check under the read lock. A search that somehow still
//     reached a retired shard gets ErrorIndexClosed back. An error, never a panic.
//
// The one window that remains is inside smart search, which asks the alias for
// hit IDs and then resolves each hit's source through GetContent: a swap landing
// between those two steps means a hit from the old generation is looked up in the
// new name map. A module that exists in both resolves normally; one that the
// reload deleted does not, and the hit is dropped by the same `if !ok { continue }`
// that already handles a file deleted underneath a running server. It cannot
// panic and it cannot empty a result set that had other hits.

// ReloadReport describes what one Reload actually did. Every field is measured,
// not assumed: ModulesAfter is the count of the generation that is now attached,
// not a prediction from the diff.
type ReloadReport struct {
	// Changed reports whether a new generation was swapped in. False means the
	// dump's signature was identical to the attached generation's, so no work was
	// done and none was needed.
	Changed bool
	// Rebuilt reports whether the new generation had to be BUILT. False with
	// Changed true means a READY generation for the new signature already existed
	// on disk (an earlier build, or another process's), so the reload only
	// re-opened it.
	Rebuilt bool
	// ModulesBefore / ModulesAfter are ModuleCount() around the swap. They are
	// equal when Changed is false.
	ModulesBefore int
	ModulesAfter  int
	// SigBefore is the signature of the generation that was attached, or "" when
	// the index served a legacy flat cache or an in-memory build (neither has a
	// generation, so such an index always rebuilds on the first reload).
	SigBefore string
	// SigAfter is the dump's signature as computed at the start of this reload.
	SigAfter string
	// Elapsed covers the whole call, including the signature walk, so it is the
	// number the caller actually waited.
	Elapsed time.Duration
}

// ErrReloadInProgress is returned when a reload is already running on this index.
// Reloads are not queued: a second caller is told to wait rather than being made
// to sit through the first one and then repeat its work.
var ErrReloadInProgress = errors.New("dump: reload already in progress")

// ErrReloadNotReady is returned when the index has not finished its initial build.
// There is nothing to swap yet, and the build in flight will pick up the current
// dump state anyway.
var ErrReloadNotReady = errors.New("dump: index is still building, nothing to reload yet")

// ErrReloadClosed is returned when Reload is called on a closed index.
var ErrReloadClosed = errors.New("dump: index is closed")

// reloadBuildGeneration is the build step of Reload, indirected through a
// variable so a test can inject a failure at exactly the point where the live
// index has not yet been touched and prove that a failed reload keeps serving.
// Production always builds WITH a claim: a reload must come away already holding
// the generation it is about to serve, because a generation that is merely READY
// in the arena is a legal target for every reaper there (see buildGeneration).
var reloadBuildGeneration = func(dir, cacheDir, gensig string) (*readerRegistration, error) {
	return buildGeneration(dir, cacheDir, gensig, withClaim)
}

// Reload picks up on-disk changes to the dump WITHOUT restarting the process, so
// a fresh DumpConfigToFiles becomes visible to search_code in all three modes and
// to every other reader of this index.
//
// It returns a report of what happened. A nil error with Changed == false means
// the dump has not changed since the index was opened and nothing was rebuilt.
// A non-nil error means the reload did not happen AND the previously working
// index is still attached and still serving; the error never leaves the index in
// a partially updated state.
//
// Reload is safe to call while searches run. It is serialised against itself and
// against Close.
func (idx *Index) Reload() (ReloadReport, error) {
	start := time.Now()

	// TryLock rather than Lock: a queued second reload would rebuild the same
	// generation the first one just built, and the caller would wait twice for one
	// result. Telling it a reload is running is both faster and truthful.
	if !idx.reloadMu.TryLock() {
		return ReloadReport{Elapsed: time.Since(start)}, ErrReloadInProgress
	}
	defer idx.reloadMu.Unlock()

	if idx.closed.Load() {
		return ReloadReport{Elapsed: time.Since(start)}, ErrReloadClosed
	}
	if !idx.ready.Load() {
		if buildErr := idx.BuildError(); buildErr != nil {
			return ReloadReport{Elapsed: time.Since(start)},
				fmt.Errorf("dump: the initial index build failed, reload cannot recover it: %w", buildErr)
		}
		return ReloadReport{Elapsed: time.Since(start)}, ErrReloadNotReady
	}

	idx.mu.RLock()
	sigBefore := idx.gensig
	cacheDir := idx.cacheDir
	before := len(idx.names)
	idx.mu.RUnlock()

	rep := ReloadReport{
		ModulesBefore: before,
		ModulesAfter:  before,
		SigBefore:     sigBefore,
	}

	// The signature walk is the same walk (sorted relpath + mtime + size of every
	// .bsl) the manifest diff uses to decide a file changed, so "the signature is
	// unchanged" means exactly "no .bsl was added, removed, or rewritten".
	sigAfter, err := GenSig(idx.dir)
	if err != nil {
		rep.Elapsed = time.Since(start)
		return rep, fmt.Errorf("dump: reading the dump directory %s: %w", idx.dir, err)
	}
	rep.SigAfter = sigAfter

	// Nothing on disk moved. Say so instead of rebuilding an identical generation
	// and reporting it as work.
	if sigBefore != "" && sigAfter == sigBefore {
		rep.Elapsed = time.Since(start)
		return rep, nil
	}

	cpath, err := cachePath(idx.dir, cacheDir)
	if err != nil {
		rep.Elapsed = time.Since(start)
		return rep, fmt.Errorf("dump: no writable cache directory for a reload "+
			"(set MCP_1C_CACHE_DIR or --cache-dir): %w", err)
	}
	genDir := generationDir(cpath, sigAfter)

	// Decide "was it built" BEFORE building: BuildGeneration is content-addressed
	// and silently no-ops on an already-READY signature, so asking afterwards
	// could not tell a fresh build from a reuse.
	rep.Rebuilt = !generationReadyDir(genDir)

	// Recover a build panic into buildErr so it becomes an ordinary failed
	// reload instead of killing the process. Same shape, and for the same
	// reason, as the recover around the identical build call in
	// cmd/mcp-1c/serve.go: a panic raised here is not caught anywhere above,
	// neither by the MCP SDK's tool dispatch nor by a goroutine of ours, so it
	// would take down the whole server and every session on it.
	//
	// A recovered panic is safe to treat as a plain failure at THIS point
	// precisely because of where the point is: nothing below the swap has run
	// yet, so the live index has not been touched and the old generation keeps
	// serving, exactly as it does when the build returns an error. The deferred
	// idx.reloadMu.Unlock() would have run either way; recovering additionally
	// keeps the caller, and the process, alive to use it.
	var buildErr error
	var reg *readerRegistration
	func() {
		defer func() {
			if r := recover(); r != nil {
				buildErr = fmt.Errorf("panic during the reload build of %s: %v", idx.dir, r)
			}
		}()
		reg, buildErr = reloadBuildGeneration(idx.dir, cacheDir, sigAfter)
	}()
	// From here on the reload HOLDS a claim on the new generation, so every early
	// return has to release it — otherwise the generation would stay pinned for the
	// life of the process and never be reclaimed.
	if buildErr != nil {
		reg.Close()
		rep.Elapsed = time.Since(start)
		return rep, fmt.Errorf("dump: building the new index generation: %w", buildErr)
	}
	// A build that reported success WITHOUT a claim is a build this process cannot
	// safely serve: the generation is in the arena, reapable, and nothing records
	// that it is about to be served. Refusing keeps the previous generation, which
	// is what a failed reload has always promised.
	if reg == nil {
		slog.Error("dump: refusing to swap in an index generation this process cannot claim; "+
			"the previous generation keeps serving. Retry the reload, or give this server its own "+
			"cache directory (MCP_1C_CACHE_DIR / --cache-dir).", "genDir", genDir)
		rep.Elapsed = time.Since(start)
		return rep, fmt.Errorf("dump: the new index generation %s was built without a reader claim", sigAfter)
	}
	if !generationReadyDir(genDir) {
		reg.Close()
		rep.Elapsed = time.Since(start)
		return rep, fmt.Errorf("dump: the new generation %s has no %s sentinel after a build that reported success",
			sigAfter, readySentinelName)
	}

	// Build the replacement state off to the side. Nothing below this point has
	// touched the live index yet, so every failure returns with the old index
	// intact and serving.
	names, pathByName, pathToDocID, err := readGenerationNames(idx.dir, genDir)
	if err != nil {
		reg.Close()
		rep.Elapsed = time.Since(start)
		return rep, fmt.Errorf("dump: reading the new generation's manifest: %w", err)
	}
	if pathByName == nil {
		// No manifest: the generation is an empty-dump build. The live open path
		// falls back to a filesystem walk here, but that walk writes into the
		// Index in place, which is exactly what a reload must not do before the
		// swap. Treat it as an empty generation instead and let the emptiness
		// guard below decide.
		names, pathByName, pathToDocID = []string{}, map[string]string{}, map[string]string{}
	}

	// Emptiness guard. A dump directory that momentarily reads as empty — an
	// unmounted share, a dump interrupted halfway, a wrong --dump path that now
	// resolves somewhere bare — walks cleanly and builds a valid, EMPTY
	// generation. Swapping it in would replace a working index with one that
	// answers nothing, which is precisely the "worse state than before" a reload
	// must never produce. Refuse and keep serving; an operator who really did
	// empty the dump can restart.
	if len(names) == 0 && before > 0 {
		reg.Close()
		rep.Elapsed = time.Since(start)
		return rep, fmt.Errorf("dump: the new state of %s has no .bsl modules while the "+
			"current index has %d; refusing to replace a working index with an empty one",
			idx.dir, before)
	}

	shards, err := openCachedShards(cacheShardDirs(genDir), true, defaultBoltTimeout)
	if err != nil {
		reg.Close()
		rep.Elapsed = time.Since(start)
		return rep, fmt.Errorf("dump: opening the new generation's shards: %w", err)
	}
	// A generation whose manifest lists modules but whose directory holds no shard
	// to open is not a generation. openCachedShards cannot say so, because an empty
	// input list is not an error to it — which is how a reaped generation became a
	// live index that reported success and then answered nothing.
	if len(shards) == 0 && len(names) > 0 {
		reg.Close()
		rep.Elapsed = time.Since(start)
		return rep, fmt.Errorf("dump: the new generation %s lists %d modules but contains no shards to open; "+
			"refusing to replace a working index with it", sigAfter, len(names))
	}

	oldShards, oldReg := idx.swapGeneration(sigAfter, shards, reg,
		names, pathByName, pathToDocID, NewPathIndex(names))

	// Retire the old generation only AFTER the swap has published its replacement.
	// alias.Swap took the alias write lock, so no search is inside these shards and
	// none started later can reach them.
	for _, s := range oldShards {
		if s == nil {
			continue
		}
		if err := s.Close(); err != nil {
			slog.Warn("dump: closing a retired shard after reload", "error", err)
		}
	}
	oldReg.Close()

	// Best-effort: reclaim generations no live reader holds. A GC failure must
	// never turn a successful reload into a failed one.
	if dropped, gcErr := GCGenerations(idx.dir, cacheDir, sigAfter); gcErr != nil {
		slog.Warn("dump: reload GC of old generations failed", "error", gcErr)
	} else if len(dropped) > 0 {
		slog.Info("dump: reload reaped old generations", "count", len(dropped))
	}

	rep.Changed = true
	rep.ModulesAfter = len(names)
	rep.Elapsed = time.Since(start)
	slog.Info("dump: reloaded index generation",
		"from", sigBefore, "to", sigAfter,
		"modules_before", rep.ModulesBefore, "modules_after", rep.ModulesAfter,
		"rebuilt", rep.Rebuilt, "elapsed", rep.Elapsed)
	return rep, nil
}

// swapGeneration publishes an already-opened generation as the index's current
// one and returns the shards and reader registration it replaced, for the caller
// to retire.
//
// The bleve alias swap happens INSIDE the same mu critical section as the
// name/path/PathIndex replacement, so no reader can observe the shards of one
// generation together with the name map of another: every path that reads those
// maps (filterModules, GetContent, contentForScan, ModuleCount, ModuleNames)
// takes mu, and alias.Swap is one call, so the alias is never momentarily empty
// and smart search never sees ErrorAliasEmpty.
//
// The content cache is cleared of FILE-BACKED entries only. Those are keyed by a
// path and revision that the new generation may contradict, and dropping them
// costs nothing but a re-read. Runtime-ingested entries (IndexDoc /
// IndexDocWithMeta, which have no file behind them) are kept, because a reload
// replaces the dump, not the documents a caller pushed in.
func (idx *Index) swapGeneration(
	gensig string,
	shards []bleve.Index,
	reg *readerRegistration,
	names []string,
	pathByName, pathToDocID map[string]string,
	pathIndex *PathIndex,
) (oldShards []bleve.Index, oldReg *readerRegistration) {
	idx.mu.Lock()
	oldShards = idx.shards
	oldReg = idx.readerReg

	idx.alias.Swap(shards, oldShards)
	idx.shards = shards
	idx.readerReg = reg
	// The notice follows the generation, in the same critical section that publishes
	// it. A reload onto a cache that has since become unwritable starts warning; one
	// that lands back on a writable cache stops. A notice left describing the RETIRED
	// generation would be a claim about an index nobody is serving any more.
	idx.setUnprotected(reg.unprotectedReason())
	idx.gensig = gensig
	idx.names = names
	idx.pathByName = pathByName
	idx.pathToDocID = pathToDocID
	idx.pathIndex = pathIndex
	// The attached generation is always opened read-only, so an index that was
	// serving a legacy read-write flat cache becomes read-only after its first
	// reload. Runtime writes are rejected from here on, exactly as they are for a
	// process that opened a generation at start.
	idx.readOnly = true
	idx.mu.Unlock()

	idx.contentMu.Lock()
	for id, entry := range idx.contentByName {
		if entry.fromFile {
			delete(idx.contentByName, id)
		}
	}
	idx.contentMu.Unlock()

	return oldShards, oldReg
}

// writeTarget snapshots, under mu, the two fields a runtime write needs: whether
// the base is read-only and which shards it would write to. Reload replaces both
// together, so reading them separately and unlocked would race the swap.
func (idx *Index) writeTarget() (readOnly bool, shards []bleve.Index) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.readOnly, idx.shards
}
