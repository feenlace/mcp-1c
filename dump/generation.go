package dump

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
)

// Immutable cache generations.
//
// A "generation" is a fully-built, immutable copy of the on-disk index for a
// specific content signature (gensig). Builds write a new generation to a temp
// directory, write the READY sentinel LAST, then atomically rename the temp
// directory into place — so a generation directory only ever becomes visible
// fully-formed and already containing READY. Readers open a generation that has
// a READY sentinel READ-ONLY (bbolt LOCK_SH), so N processes coexist on the same
// generation, and a concurrent rebuild — which produces a DIFFERENT generation
// directory (a different gensig) — never touches the files a live reader holds.
//
// Layout under the per-dump cache dir (cachePath(dumpDir, cacheDir)):
//
//	<cpath>/shard_*              ← LEGACY flat layout (pre-generations; still read for back-compat)
//	<cpath>/manifest.json        ← LEGACY flat manifest
//	<cpath>/g/<gensig>/shard_*   ← generation shards, immutable once READY
//	<cpath>/g/<gensig>/manifest.json
//	<cpath>/g/<gensig>/READY      ← sentinel, written LAST (before the atomic adopt)
//	<cpath>/g/<gensig>/readers/   ← liveness-checked multi-holder reader registry (see readers.go)
//	<cpath>/g/.building-<gensig>-<rand>/  ← in-progress build temp dir (renamed away on adopt)
//
// COORDINATOR-SAFETY (implemented here + in readers.go): generation-aware reindex
// (build a new generation, never wipe a live one — see NewIndex), the
// liveness-checked reader registry (readers/), and old-generation GC
// (GCGenerations, which never removes a generation a live reader holds).
//
// DEFERRED to later chunks (NOT implemented here): the build-leader election
// (instancelock) + async-readiness wiring (advanced layer, after re-vendor) and
// the per-process extension overlay. The schema/format version component of
// gensig and the legacy-flat → generation migration shim are implemented here.
const (
	generationsDirName = "g"
	readySentinelName  = "READY"
	buildTmpPrefix     = ".building-"

	// defaultBoltTimeout bounds how long a read-only open waits for a conflicting
	// flock before failing. MUST be a Go duration STRING (see openCachedShards).
	defaultBoltTimeout = "5s"

	// buildDirStaleAfter is how long a .building-* temp generation dir may go with
	// NO new write anywhere in its tree before ReapStaleBuildDirs treats it as
	// abandoned (its builder was SIGKILLed / OOM-killed / lost power mid-build) and
	// removes it. It is the staleness gate that makes reaping safe to run while a
	// CONCURRENT build-leader is still writing: a healthy build streams zap segments
	// into its shard subdirs continuously (bleve's offline builder, batchSize 5000,
	// with the shard subdirs created up front), so SOME file in a live build's tree
	// is always fresher than this window — only a build that has written nothing for
	// the whole window is dead. This is the no-write age (the newest mtime ANYWHERE
	// in the tree, not the dir's creation time), so it never false-reaps a build that
	// legitimately runs LONGER than the window — only one that has gone silent. It is
	// a constant rather than a flag to keep the single-binary surface small, matching
	// the reader-registry's readerStaleAfter; 30m is far beyond any healthy build's
	// inter-write gap yet short enough that a crash-cascade's leaks are reclaimed on
	// the next serve open rather than accumulating to ENOSPC.
	buildDirStaleAfter = 30 * time.Minute
)

// Index version components folded into GenSig.
//
// GenSig hashes THREE independent version integers alongside the dump content, so
// a bump of ANY of them yields a different gensig → a different generation
// directory (g/<gensig>/). A reader computes the gensig with the CURRENT versions
// and therefore only ever finds/adopts a generation built with the SAME versions:
// it NEVER opens a generation produced by an incompatible derivation, schema, or
// on-disk format. This is the reader schema-drift protection (design #2/#6).
//
// BUMP PROTOCOL — bump the matching const (and ONLY that const) when:
//
//   - genSigVersion: the gensig DERIVATION changes — i.e. HOW GenSig walks/hashes
//     the dump (today: sorted relpath+mtime+size of every .bsl). Bump if that
//     algorithm changes so old and new signatures can never spuriously collide.
//
//   - dumpIndexSchemaVersion: the LOGICAL index schema changes — the BSL field
//     mapping/analyzers (buildBSLMapping), the indexed document shape
//     (bslDocument), the document-ID derivation (bslPathToModuleName, including its
//     NFC key normalisation), or the shard-assignment hash (shardForID/splitByHash)
//     — any change after which a generation built by an OLDER binary would yield
//     wrong or degraded results if SERVED by a NEWER one. Bumping it forces every
//     reader onto a freshly-built generation rather than silently mis-reading old
//     shards.
//
//   - zapSegmentVersion: the on-disk scorch "zap" segment format version handed to
//     bleve.NewBuilder (forceSegmentVersion). Bump ONLY when intentionally moving
//     to a new zap format that the pinned bleve version supports. Bumping it makes
//     a new binary skip (and rebuild) generations written in the old binary
//     format instead of failing at open time.
//
// Each bump is one-way and additive: it changes the gensig, the new generation is
// built on demand, and the now-orphaned old-version generations are reaped by the
// normal old-generation GC (GCGenerations) once no live reader holds them.
const (
	// genSigVersion versions the gensig derivation. See BUMP PROTOCOL above.
	genSigVersion = 1

	// dumpIndexSchemaVersion versions the logical index schema. See BUMP PROTOCOL.
	//
	// v2: module-name docIDs are NFC-normalised at the build chokepoint
	// (bslPathToModuleName). A v1 cache built on macOS stored decomposable Cyrillic
	// names (short-I / IO letters) in NFD, so they never matched an NFC
	// GetContent/resolve query — module_code and resolve returned not-found for any
	// such name. The bump forces a fresh NFC-keyed build: a v1 generation now derives
	// a different gensig (rebuilt on serve, never adopted), and a v1 legacy-flat cache
	// is dropped by the schema gate in NewIndex (flatCacheSchemaStale) and cold-rebuilt.
	//
	// v3: the docID derivation (bslPathToModuleName) changed for six top-level dump
	// directories. Five service kinds (HTTPServices, WebServices, SettingsStorages,
	// FilterCriteria, Sequences) had no dumpDirNames entry, so their keys carried the
	// raw English directory name as the prefix; HTTPServices/WebServices additionally
	// got the form-module suffix for their own Module.bsl. The root "Ext" directory,
	// which holds the configuration's own four modules, produced keys with a literal
	// ".bsl" mid-key and the name repeated. Without this bump a warm generation or
	// flat cache built by a v2 binary keeps its old gensig, is adopted as current, and
	// serves the old keys — the whole fix would be inert for every existing user.
	dumpIndexSchemaVersion = 3

	// zapSegmentVersion is the scorch zap segment format version used by every
	// build path (buildShardOffline / buildIndexBuilder forceSegmentVersion) and
	// folded into the gensig. See BUMP PROTOCOL above.
	zapSegmentVersion = 16
)

// baselineSchemaVersion / baselineZapVersion are the schema and zap versions that
// shipped BEFORE the manifest stamped them (Manifest.SchemaVersion/ZapVersion were
// added together with this versioning). A legacy manifest written by an older
// binary carries neither field (they unmarshal to 0); such a manifest is, by
// construction, the only schema/format that ever existed pre-stamping — exactly
// these baseline values — so flat-cache adoption treats a 0 as the baseline.
//
// FROZEN: these record history. NEVER change them when bumping
// dumpIndexSchemaVersion / zapSegmentVersion — that would mis-classify genuinely
// old flat caches as current and adopt incompatible shards.
const (
	baselineSchemaVersion = 1
	baselineZapVersion    = 16
)

// generationsDir returns <cpath>/g.
func generationsDir(cpath string) string {
	return filepath.Join(cpath, generationsDirName)
}

// generationDir returns the immutable directory for a specific generation.
func generationDir(cpath, gensig string) string {
	return filepath.Join(cpath, generationsDirName, gensig)
}

// readySentinelPath returns the READY sentinel path inside a generation dir.
func readySentinelPath(genDir string) string {
	return filepath.Join(genDir, readySentinelName)
}

// generationReadyDir reports whether genDir holds a READY sentinel file. A
// generation without READY is partial / in-progress and MUST NOT be adopted.
func generationReadyDir(genDir string) bool {
	st, err := os.Stat(readySentinelPath(genDir))
	return err == nil && !st.IsDir()
}

// GenSig computes the content+schema signature of a dump directory: a short hex
// hash over the gensig derivation version, the index schema version, the on-disk
// zap segment version, and the sorted (relative-path, mtime-ms, size) tuples of
// every .bsl file. Two dumps with identical content built by the same-versioned
// binary yield the same signature and thus share one immutable generation; any
// drift (add / remove / modify) OR any version bump (see the BUMP PROTOCOL on the
// version consts) yields a new signature, so the result is a fresh generation
// directory rather than a mutated one in use or a mis-read incompatible one.
//
// It walks the dump once (the same cost as the warm-start manifest diff that
// already runs on every open today).
func GenSig(dir string) (string, error) {
	return genSig(dir, dumpIndexSchemaVersion, zapSegmentVersion)
}

// genSig is the version-parameterised core of GenSig. GenSig always passes the
// current dumpIndexSchemaVersion / zapSegmentVersion; the parameters exist so the
// schema-drift invariant (a bumped schema/format yields a different signature, and
// a generation built under a different schema is never adopted) is directly
// testable without rebuilding the binary.
func genSig(dir string, schemaVer, zapVer int) (string, error) {
	type fileSig struct {
		rel  string
		mod  int64
		size int64
	}
	var files []fileSig
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".bsl") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, fileSig{filepath.ToSlash(rel), info.ModTime().UnixMilli(), info.Size()})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("computing dump signature: %w", err)
	}

	slices.SortFunc(files, func(a, b fileSig) int { return strings.Compare(a.rel, b.rel) })

	h := sha256.New()
	// The version header folds the derivation, schema, and on-disk format versions
	// into the signature so a bump of any one yields a distinct gensig.
	fmt.Fprintf(h, "gensig-v%d schema-v%d zap-v%d\n", genSigVersion, schemaVer, zapVer)
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", f.rel, f.mod, f.size)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// GenerationReady reports whether a READY (adoptable) generation for gensig
// exists for the given dump+cache dir. It stats a single sentinel file — cheap
// enough to be the wake/selection predicate without walking the generation tree.
func GenerationReady(dir, cacheDir, gensig string) bool {
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return false
	}
	return generationReadyDir(generationDir(cpath, gensig))
}

// OpenGenerationReadOnly opens the immutable generation gensig READ-ONLY. N
// processes may call this concurrently on the same generation without blocking
// (bbolt LOCK_SH). It never writes into the generation directory: there is no
// serve-lock write, no warm-start diff, and no manifest rewrite — the generation
// is trusted to match its gensig by construction. Returns an error if the
// generation has no READY sentinel (partial / absent build).
func OpenGenerationReadOnly(dir, cacheDir, gensig string) (*Index, error) {
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return nil, err
	}
	genDir := generationDir(cpath, gensig)
	if !generationReadyDir(genDir) {
		return nil, fmt.Errorf("generation %q is not ready (no %s sentinel at %s)",
			gensig, readySentinelName, genDir)
	}
	return openReadOnlyFrom(dir, cacheDir, genDir, nil)
}

// openReadOnlyFrom builds an Index serving the already-built shards under genDir
// in read-only mode. Names/paths are loaded from the generation's manifest in a
// background goroutine (Ready()/Done() follow the usual contract).
//
// cacheDir is recorded on the Index (NewIndex semantics: empty = platform cache
// dir) so a later Reload builds its replacement generation under the same cache
// this one was opened from.
//
// held is a claim the caller already took on genDir, or nil; see
// attachReadOnlyShards, which owns it from here on either way.
func openReadOnlyFrom(dumpDir, cacheDir, genDir string, held *readerRegistration) (*Index, error) {
	ctx, cancel := context.WithCancel(context.Background())
	idx := &Index{
		dir:           dumpDir,
		cacheDir:      cacheDir,
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]cachedModule),
		pathByName:    make(map[string]string),
		pathToDocID:   make(map[string]string),
		readOnly:      true,
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	if err := idx.attachReadOnlyShards(genDir, held); err != nil {
		cancel()
		return nil, err
	}

	go func() {
		defer close(idx.done)
		if err := idx.loadNamesReadOnly(genDir); err != nil {
			idx.setBuildErr(err)
			return
		}
		idx.pathIndex = NewPathIndex(idx.names)
		idx.ready.Store(true)
		slog.Info("Opened read-only index generation",
			"shards", len(idx.shards), "modules", len(idx.names), "gen", filepath.Base(genDir))
		if showProgress.Load() {
			fmt.Fprintf(os.Stderr, "[%s] Индекс открыт только для чтения: %d модулей\n",
				time.Now().Format("15:04:05"), len(idx.names))
		}
	}()

	return idx, nil
}

// NewServePlaceholder returns a not-yet-ready, read-only serve Index for dumpDir.
// It is the async-serve analogue of openReadOnlyFrom: it allocates the Index with
// Ready()==false and an OPEN Done() channel but attaches NO shards and loads NO
// names yet. The advanced build-leader path hands this placeholder out IMMEDIATELY
// — so the MCP initialize handshake is never blocked on a cold BuildGeneration —
// then completes the open in a background goroutine via (*Index).FinishServeOpen
// once the generation is built (or records the build failure there).
//
// Until FinishServeOpen runs, every reader observes the usual not-ready contract:
// Search() reports "building", GetContent()/ModuleNames()/ModuleCount() return
// their not-ready zero values, GetPathIndex() returns nil, and BuildError() is
// nil. Dependents (the Pro analyzers, extension auto-ingest, Close()) may block on
// Done() to await readiness.
//
// The returned Index has a live ctx/cancel and an open done channel, so Close()
// (idx.cancel(); <-idx.done) is well-defined even if the open never succeeds: it
// blocks until FinishServeOpen closes done. FinishServeOpen MUST be called exactly
// once on the returned Index. The struct literal is kept identical to
// openReadOnlyFrom's so the two paths can never drift in what a placeholder/opened
// serve Index initializes, with ONE deliberate exception: cacheDir is not known
// here (the caller passes it to FinishServeOpen, which records it there), so a
// placeholder carries an empty cacheDir until the open finishes.
func NewServePlaceholder(dumpDir string) *Index {
	ctx, cancel := context.WithCancel(context.Background())
	return &Index{
		dir:           dumpDir,
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]cachedModule),
		pathByName:    make(map[string]string),
		pathToDocID:   make(map[string]string),
		readOnly:      true,
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
}

// FinishServeOpen completes — or fails — a placeholder created by
// NewServePlaceholder, IN PLACE on the SAME *Index, and ALWAYS closes Done()
// exactly once on every exit path (success, failure, a ctx-cancel funneled in via
// prepErr, or a panic while attaching). It MUST be called exactly once.
//
// On success it attaches the generation named by gen READ-ONLY, loads names from
// the generation manifest, builds the path index, and flips Ready() via a RELEASE
// store. The shards (attachReadOnlyShards) and names (loadNamesReadOnly) are
// therefore fully published BEFORE ready.Store(true), so any reader that observes
// Ready()==true (or passes the Search() gate, an ACQUIRE load) sees a
// fully-populated index with no torn read.
//
// gen CARRIES THE CLAIM, and that is the point of its existing at all. It is what
// PrepareServeGeneration came away holding, taken while the generation was still
// a private build directory, so the generation entered the arena already held and
// no reaper could take it — or rename it away — between the build and this attach.
// A gensig alone could not express that: the claim is a live handle with a
// heartbeat and a lifetime, and re-deriving one here would put back the very
// window it was taken early to avoid.
//
// On any failure it records BuildError(), RELEASES gen's claim, and returns
// WITHOUT flipping Ready():
//   - a non-nil prepErr handed in by the caller (signature compute, build error,
//     ctx cancellation, or a recovered build panic), in which case gen is nil,
//   - an unresolvable cache path or a generation with no READY sentinel,
//   - an attach/load error (corrupt shards, unreadable manifest),
//   - a panic raised while attaching/loading (converted to BuildError by the
//     deferred recover, so the goroutine never crashes the process).
//
// Done() still closes on every one of those paths, so waiters never block and
// Close()'s <-done never deadlocks; Search()/content readers then surface the
// recorded build error (index.go Search wraps BuildError) instead of a silent
// not-found or a hang.
//
// This mirrors openReadOnlyFrom (struct + background close) and the tail of
// reindexGeneration (attach -> loadNames -> NewPathIndex -> ready.Store(true)),
// but populates an existing placeholder rather than a freshly-allocated Index, so
// dependents that captured the placeholder pointer (and are waiting on its Done())
// transition with it instead of being orphaned by a pointer swap.
func (idx *Index) FinishServeOpen(cacheDir string, gen *ServeGeneration, prepErr error) {
	gensig := gen.Gensig()
	// Defers run LIFO: close(done) is registered first so it runs LAST — after the
	// recover below has recorded any panic as BuildError. A waiter unblocked by the
	// done-close therefore always sees a stable Ready()/BuildError() pair.
	defer close(idx.done)
	// The claim is released on every path that does NOT reach the attach. Past the
	// attach idx owns it and Close releases it, so releasing again here would
	// deregister a claim on the generation this index is now serving — hence the
	// flag rather than a bare defer.
	attached := false
	defer func() {
		if !attached {
			gen.Release()
		}
		if r := recover(); r != nil {
			idx.setBuildErr(fmt.Errorf("serve open: panic finishing generation %q: %v", gensig, r))
		}
	}()

	if prepErr != nil {
		idx.setBuildErr(prepErr)
		return
	}
	if gen == nil {
		idx.setBuildErr(fmt.Errorf("serve open: no prepared generation was handed in and no error explains why"))
		return
	}

	// Record the cache this serve was opened from BEFORE anything can fail, so a
	// later Reload rebuilds into the same cache the caller chose rather than
	// re-resolving one from the environment.
	idx.cacheDir = cacheDir

	cpath, err := cachePath(idx.dir, cacheDir)
	if err != nil {
		idx.setBuildErr(fmt.Errorf("serve open: resolve cache path: %w", err))
		return
	}
	genDir := generationDir(cpath, gensig)
	if !generationReadyDir(genDir) {
		idx.setBuildErr(fmt.Errorf("serve open: generation %q is not ready (no %s sentinel at %s)",
			gensig, readySentinelName, genDir))
		return
	}

	// From here idx owns the claim: attachReadOnlyShards releases it itself if it
	// fails, and Close releases it if it succeeds.
	attached = true
	if err := idx.attachReadOnlyShards(genDir, gen.claim()); err != nil {
		idx.setBuildErr(err)
		return
	}
	if err := idx.loadNamesReadOnly(genDir); err != nil {
		idx.setBuildErr(err)
		return
	}
	idx.pathIndex = NewPathIndex(idx.names)
	idx.ready.Store(true) // release: publishes shards+names to acquire-side readers

	slog.Info("Finished async serve index open",
		"shards", len(idx.shards), "modules", len(idx.names), "gen", gensig)
	if showProgress.Load() {
		fmt.Fprintf(os.Stderr, "[%s] Индекс открыт только для чтения: %d модулей\n",
			time.Now().Format("15:04:05"), len(idx.names))
	}
}

// attachReadOnlyShards opens the shards under genDir READ-ONLY (bbolt LOCK_SH) and
// attaches them to idx, marks idx read-only, and makes idx the holder of a live
// claim in the generation's readers/ registry, so concurrent old-generation GC
// never reaps a generation this process is serving. The claim exists BEFORE the
// shards are opened, so a live reader is visible to GC as early as possible; if
// the open fails the claim is released. The caller is responsible for loading
// names and flipping Ready().
//
// held is a claim the CALLER already took on this same genDir, or nil. Passing a
// held claim is what the paths that produce their own generation do: they claim
// it while it is still a private build directory and adopt the two together, so
// there is no instant at which the generation is READY in the arena and unheld
// (see buildGeneration). Taking a fresh claim here instead would put that instant
// back. nil means "this process did not produce this generation", and then the
// only thing left to do is registerReader's fail-closed post-adopt claim.
//
// OWNERSHIP: on entry idx owns held, on every path. It is released here if
// anything below fails, and released by Close otherwise.
func (idx *Index) attachReadOnlyShards(genDir string, held *readerRegistration) error {
	// Claim FIRST, and REFUSE if it cannot be done. This used to be best-effort:
	// the failure was a slog.Warn and the open carried on. cmd/mcp-1c/main.go pins
	// the default slog handler to LevelError, so that warning reached nobody, and
	// the process then served a generation no reaper in the arena had any reason to
	// keep — which is exactly what a co-located reaper deleted, while this process
	// kept answering out of unlinked inodes. Serving on without a claim is the
	// "degraded component reports a pass" failure; the only correct answer is to
	// stop here and say why, loudly enough to be heard at the default log level.
	reg := held
	if reg == nil {
		var err error
		if reg, err = registerReader(genDir); err != nil {
			slog.Error("dump: refusing to serve an index generation this process cannot claim; "+
				"another process could delete it while it is being served. Give this server its own "+
				"cache directory (MCP_1C_CACHE_DIR / --cache-dir), or make the cache directory writable.",
				"genDir", genDir, "error", err)
			return fmt.Errorf("refusing to serve generation %s without a reader claim: %w",
				filepath.Base(genDir), err)
		}
	}
	idx.readerReg = reg

	shardDirs := cacheShardDirs(genDir)
	shards, err := openCachedShards(shardDirs, true, defaultBoltTimeout)
	if err != nil {
		if idx.readerReg != nil {
			idx.readerReg.Close()
			idx.readerReg = nil
		}
		return fmt.Errorf("opening read-only generation shards: %w", err)
	}
	idx.readOnly = true
	idx.shards = shards
	// Record WHICH generation is attached. Reload compares this against a freshly
	// computed dump signature to decide whether anything changed on disk, and
	// replaces it when it swaps a new generation in. A generation directory is
	// named by its gensig, so the base name IS the signature.
	idx.gensig = filepath.Base(genDir)
	idx.alias.Add(shards...)
	return nil
}

// GCGenerations removes old immutable generations that are safe to delete: any
// adopted (READY) generation that is NEITHER the current one (keepGensig) NOR held
// by a live reader (consulted via each generation's readers/ registry). It never
// removes:
//   - the current generation (keepGensig),
//   - a generation a live reader still holds,
//   - an in-progress build (a .building-* temp dir),
//   - a non-adopted directory (no READY sentinel) — reaped instead by
//     ReapStaleBuildDirs, which also sweeps orphaned READY-less generations.
//
// Removal is best-effort and per-generation: a permission error (a cross-user
// generation on a shared cacheDir) is skipped, not fatal, so one undeletable
// generation never blocks reclaiming the others. Returns the gensigs actually
// removed.
//
// IT FAILS CLOSED. "No live reader is registered" is only acted on when the
// registry could actually be read AND could actually have been written to by a
// peer; anything else leaves the generation alone and says so at slog.Error (see
// removeUnheldGeneration and registryTrustworthy). The removal itself takes the
// generation OUT of the arena by an atomic rename BEFORE deleting it, so a
// process that claims it while this pass is deciding is detected and the
// generation is put back rather than deleted underneath it.
func GCGenerations(dir, cacheDir, keepGensig string) ([]string, error) {
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return nil, err
	}
	gensDir := generationsDir(cpath)
	entries, err := os.ReadDir(gensDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no generations arena yet — nothing to GC
		}
		return nil, fmt.Errorf("reading generations dir: %w", err)
	}

	var removed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, buildTmpPrefix) {
			continue // in-progress (or crashed) build temp dir — never GC here
		}
		if name == keepGensig {
			continue // current generation
		}
		genDir := generationDir(cpath, name)
		if !generationReadyDir(genDir) {
			continue // not an adopted generation
		}
		gone, err := removeUnheldGeneration(gensDir, name)
		if err != nil {
			// errors.Is, NOT os.IsPermission. os.IsPermission predates error
			// wrapping and unwraps only *PathError / *LinkError / *SyscallError, so
			// it answers FALSE for the fmt.Errorf("%w")-wrapped errors this path now
			// returns — which would push every cross-user generation on a shared
			// arena from Debug to Error on every pass. Measured, not assumed.
			if errors.Is(err, fs.ErrPermission) {
				slog.Debug("GC: skipping generation owned by another user", "gen", name, "error", err)
			} else {
				// NOT a Warn. Every reason this can fail is a reason a generation that
				// might be in use was left alone, and the operator has to be able to
				// see that the arena is unhealthy at the default log level.
				slog.Error("GC: refusing to remove a generation whose holders cannot be established",
					"gen", name, "error", err)
			}
			continue
		}
		if !gone {
			continue // a live reader holds it, or claimed it while we were deciding
		}
		removed = append(removed, name)
		slog.Info("GC: removed old unheld generation", "gen", name)
	}
	return removed, nil
}

// removeUnheldGeneration deletes the generation <gensDir>/<name> if and only if it
// can be ESTABLISHED that nothing is serving it, and reports whether it did. A
// non-nil error means the question could not be answered — never that the answer
// was "unheld".
//
// WHY THIS IS NOT JUST "scan readers/, then RemoveAll". A reaper's scan and its
// deletion are two moments, and a process can claim the generation in between: it
// would find READY present and its own entry visible, and go on to serve while the
// deletion ran. The claim-by-rename collapses the two moments into one observable
// event:
//
//	scan readers/  →  RENAME the generation out of the arena  →  scan readers/ again
//
// The rename is atomic, so afterwards no NEW claim can land: claimReader creates
// its entry with a single-level os.Mkdir under the ORIGINAL path, which no longer
// resolves, and its post-claim re-read of READY reads the original path too. So
// either a racing claimant got its entry in before the rename — in which case the
// entry moved with the directory, the second scan finds it, and the rename is
// undone — or it did not, and it fails and refuses to serve. Neither ends with a
// deleted generation that something is serving.
func removeUnheldGeneration(gensDir, name string) (bool, error) {
	genDir := filepath.Join(gensDir, name)

	live, err := generationHasLiveReader(genDir)
	if err != nil {
		return false, err
	}
	if live {
		return false, nil // a live reader still holds it — MUST NOT remove
	}
	// An empty registry only means "unheld" if it is one a peer could have written
	// to. See registryTrustworthy.
	if err := registryTrustworthy(genDir); err != nil {
		return false, err
	}

	claimed, err := claimGenerationForRemoval(gensDir, name)
	if err != nil {
		return false, err
	}

	live, err = generationHasLiveReader(claimed)
	if err != nil || live {
		// Someone claimed it in the window between the first scan and the rename.
		// Put it back where its holder expects it.
		if rbErr := os.Rename(claimed, genDir); rbErr != nil {
			slog.Error("GC: a reader claimed a generation while it was being removed and it could not be "+
				"moved back; the holder keeps serving from its open files, and the directory is reclaimed "+
				"once that holder exits",
				"gen", name, "claimed", claimed, "error", rbErr)
		} else {
			slog.Info("GC: a reader claimed a generation while it was being removed; kept it", "gen", name)
		}
		return false, err
	}

	if err := os.RemoveAll(claimed); err != nil {
		return false, err
	}
	return true, nil
}

// claimGenerationForRemoval atomically moves a generation OUT of the arena, under
// a name carrying buildTmpPrefix, and returns the new path. The prefix is what
// makes the claim self-cleaning: GCGenerations skips buildTmpPrefix dirs, and
// ReapStaleBuildDirs removes one once its whole tree has gone untouched for
// buildDirStaleAfter — so a claim whose removal fails (a live holder's mmap'd
// files on Windows) is reclaimed on a later pass instead of leaking.
//
// The rename also makes the removal ALL-OR-NOTHING, which os.RemoveAll is not.
// RemoveAll deletes depth-first, so on an arena this process may read but not
// write — a generation owned by another unix user — it strips READY and the
// shards and only THEN fails on the final rmdir, leaving a gutted directory
// behind and reporting an error. A rename that is refused changes nothing at all,
// so the generation is still whole and still servable. Measured: with the rename
// removed, the cross-user GC test finds its generation no longer READY.
//
// A holder that is still serving out of the renamed tree does NOT keep it fresh:
// its heartbeat re-touches readerRegistration.path, which names the ORIGINAL
// location and no longer resolves, so it raises the lost-claim alarm instead. The
// tree is therefore reclaimed once it goes stale even while a unix holder reads
// from its unlinked inodes — which is safe, because unlinking cannot disturb an
// open descriptor, and on Windows the removal simply fails again until the holder
// exits.
func claimGenerationForRemoval(gensDir, name string) (string, error) {
	claimed := filepath.Join(gensDir, fmt.Sprintf("%sreaping-%s-%d-%d",
		buildTmpPrefix, name, os.Getpid(), time.Now().UnixNano()))
	if err := os.Rename(filepath.Join(gensDir, name), claimed); err != nil {
		return "", fmt.Errorf("claiming generation %s for removal: %w", name, err)
	}
	return claimed, nil
}

// ReapStaleBuildDirs sweeps the generations arena (g/) for the two kinds of
// reclaimable remnant that nothing else removes and os.RemoveAll's each. It is meant
// to run on startup / BEFORE a build (see PrepareServeGeneration), not only after a
// successful build, so a leak is reclaimed even when the process that produced it
// never reached its own cleanup. Removal is best-effort and per-dir: a permission
// error (a cross-user dir on a shared cacheDir) is skipped, not fatal, so one
// undeletable dir never blocks reclaiming the others. Returns the dir names removed.
//
// 1) ABANDONED .building-* temp dirs — the partial generation a builder leaves
// behind when it dies mid-build (SIGKILL, OOM, power loss) before it can adopt the
// build (atomic rename into g/<gensig>/) or roll it back. GCGenerations deliberately
// SKIPS .building-* (it cannot tell an in-progress build from a dead one) and the
// post-build deferred cleanup only runs in the surviving process, so without this
// every interrupted build leaks hundreds of MB of shards until ENOSPC. The same
// prefix is used by BuildGeneration and adoptFlatShards, so an interrupted flat-cache
// adoption's temp dir is reaped too. SAFETY — a fresh in-progress build MUST survive:
// a .building-* dir is removed ONLY when the newest mtime ANYWHERE in its tree is
// older than buildDirStaleAfter, so a concurrent build-leader streaming segments
// (fresh tree) is never reaped; the caller has not yet started its own build, so it
// has no live temp dir of its own to protect. (A crash mid-adoptFlatShards can leave
// the only copy of the legacy flat shards inside the reaped temp dir — reaping loses
// a CACHE the next open rebuilds, never source data.)
//
// 2) ORPHANED READY-less committed generations — a g/<gensig>/ dir that carries
// shards but NO READY sentinel. A normal build renames a fully-READY temp into place
// atomically, so a committed dir can only lack READY when a prior GCGenerations'
// os.RemoveAll partially FAILED: classically on Windows, where after READY and
// readers/ were stripped the generation's still-mmap-locked shard files could not be
// deleted, leaving shards with no READY. GCGenerations then skips it forever (it
// requires READY) and the .building-* branch skips it (wrong prefix), so it leaks
// permanently. SAFETY — the three gates below are what make deleting a cache dir here
// safe, and a healthy build never lands in this branch: reap ONLY when there is no
// READY (a healthy adopted generation is left untouched), no live reader (never yank
// a generation a co-located serving process holds — consulted via readers/), AND the
// whole tree is stale (belt-and-suspenders). On Windows an orphan whose shards are
// still mmap-locked by a living holder simply fails os.RemoveAll again (the holder is
// never yanked) and is cleared on a later pass once that holder exits.
func ReapStaleBuildDirs(dir, cacheDir string) ([]string, error) {
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return nil, err
	}
	gensDir := generationsDir(cpath)
	entries, err := os.ReadDir(gensDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no generations arena yet — nothing to reap
		}
		return nil, fmt.Errorf("reading generations dir: %w", err)
	}

	cutoff := time.Now().Add(-buildDirStaleAfter)
	var removed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		genPath := filepath.Join(gensDir, name)

		if strings.HasPrefix(name, buildTmpPrefix) {
			// (1) Abandoned mid-build temp dir. A fresh tree means a live build may
			// still be writing it — MUST NOT remove.
			if !buildDirStale(genPath, cutoff) {
				continue
			}
			if removeReapable(genPath, name, "abandoned build temp dir") {
				removed = append(removed, name)
			}
			continue
		}

		// (2) Committed generation dir. Reap ONLY a stale, unheld orphan; any one of
		// the three gates failing means it is NOT a safe target. Checked cheapest
		// first: a single READY stat, then a readers/ scan, then a full-tree walk.
		if generationReadyDir(genPath) {
			continue // healthy adopted generation — MUST NOT remove
		}
		if live, err := generationHasLiveReader(genPath); err != nil {
			// The registry could not be read, so whether anything is serving this
			// orphan is UNKNOWN. Unknown is not "unheld".
			slog.Error("reap: refusing to remove an orphaned generation whose holders cannot be established",
				"dir", name, "error", err)
			continue
		} else if live {
			continue // a live reader still holds it — MUST NOT remove
		}
		if !buildDirStale(genPath, cutoff) {
			continue // fresh — belt-and-suspenders; never reap a just-touched dir
		}
		if removeReapable(genPath, name, "orphaned READY-less generation") {
			removed = append(removed, name)
		}
	}
	return removed, nil
}

// removeReapable os.RemoveAll's a single reclaimable dir (an abandoned .building-*
// temp or an orphaned READY-less generation) best-effort and reports whether it was
// removed. A permission error (a cross-user dir on a shared cacheDir) is logged at
// Debug and any other error at Warn — never fatal, so one undeletable dir never
// blocks reclaiming the rest. On Windows a partial failure (an orphan's shard files
// still mmap-locked by a living holder) returns false and leaves the shrinking
// remnant for a later pass once that holder exits; the holder's mapping is never
// yanked because Windows refuses to delete files it still has open.
func removeReapable(path, name, kind string) bool {
	if err := os.RemoveAll(path); err != nil {
		if os.IsPermission(err) {
			slog.Debug("reap: skipping "+kind+" owned by another user", "dir", name, "error", err)
		} else {
			slog.Warn("reap: could not remove "+kind, "dir", name, "error", err)
		}
		return false
	}
	slog.Info("reap: removed "+kind, "dir", name)
	return true
}

// buildDirStale reports whether EVERY entry in the temp build dir tree is older
// than cutoff — i.e. nothing has been written anywhere in it for buildDirStaleAfter,
// so the build that owns it is abandoned. It walks the tree but RETURNS EARLY
// (fs.SkipAll) the instant it finds any entry at-or-after cutoff, so detecting a
// live build (which has a fresh segment somewhere) is cheap and never reaps it. A
// walk error is treated as "not stale" (keep) so a transient read error or a dir
// vanishing mid-walk never causes a removal.
func buildDirStale(tmpDir string, cutoff time.Time) bool {
	stale := true
	walkErr := filepath.WalkDir(tmpDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			stale = false
			return fs.SkipAll // a fresh entry → this build is live; stop walking
		}
		return nil
	})
	if walkErr != nil {
		return false // walk failed → be conservative, do NOT reap
	}
	return stale
}

// loadNamesReadOnly populates names/pathByName/pathToDocID from the generation's
// manifest WITHOUT running the warm-start diff and WITHOUT any cache write. If
// the generation has no manifest (e.g. an empty-dump generation), it falls back
// to a read-only filesystem walk of the dump (loadBSLPaths), which also writes
// nothing to the cache. Drift between dump and generation is impossible by gensig
// construction, so no diff is needed.
func (idx *Index) loadNamesReadOnly(genDir string) error {
	names, pathByName, pathToDocID, err := readGenerationNames(idx.dir, genDir)
	if err != nil {
		return err
	}
	if pathByName == nil {
		// A nil map (never an empty one) is readGenerationNames' signal that the
		// generation carries NO manifest. Fall back to a read-only walk of the dump,
		// which populates the same three fields in place and writes nothing.
		return idx.loadBSLPaths(idx.dir)
	}

	idx.mu.Lock()
	// pathToDocID is not pre-initialized on every Index-creation path (NewIndex's
	// reindex path reuses its own idx, which leaves it nil); init it lazily, as
	// loadBSLPaths does, so this shared helper is safe regardless of caller.
	if idx.pathToDocID == nil {
		idx.pathToDocID = make(map[string]string, len(pathToDocID))
	}
	idx.names = append(idx.names, names...)
	for docID, absPath := range pathByName {
		idx.pathByName[docID] = absPath
	}
	for relPath, docID := range pathToDocID {
		idx.pathToDocID[relPath] = docID
	}
	slices.Sort(idx.names)
	idx.mu.Unlock()
	return nil
}

// readGenerationNames reads a generation's manifest and returns the three name /
// path collections it implies, WITHOUT touching any Index. It is the pure core of
// loadNamesReadOnly: the read-only open assigns the result into a fresh Index,
// while Reload builds the replacement state off to the side and only then swaps it
// in, so a failure here can never leave a live index half-updated.
//
// A generation with no manifest (an empty-dump build) returns three nil
// collections and a nil error; the caller then falls back to a filesystem walk.
// Returning nil rather than empty maps is what distinguishes "no manifest" from
// "a manifest listing no files" — the latter yields non-nil empty maps.
func readGenerationNames(dumpDir, genDir string) (names []string, pathByName, pathToDocID map[string]string, err error) {
	manifest, err := LoadManifest(genDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("loading generation manifest: %w", err)
	}
	if manifest == nil {
		return nil, nil, nil, nil
	}

	names = make([]string, 0, len(manifest.Files))
	pathByName = make(map[string]string, len(manifest.Files))
	pathToDocID = make(map[string]string, len(manifest.Files))
	for relPath, entry := range manifest.Files {
		absPath := filepath.Join(dumpDir, filepath.FromSlash(relPath))
		// Defensive NFC at the manifest chokepoint, mirroring bslPathToModuleName and
		// loadFromManifestAndDiff. A generation is gensig-keyed (the schema version is
		// folded into the signature), so a binary only ever opens a generation whose
		// manifest it wrote itself with NFC keys — but normalising here keeps this
		// read-only load correct by construction instead of relying on that argument,
		// and is an allocation-free no-op on already-NFC keys.
		docID := NFC(entry.DocID)
		names = append(names, docID)
		pathByName[docID] = absPath
		pathToDocID[relPath] = docID
	}
	slices.Sort(names)
	return names, pathByName, pathToDocID, nil
}

// BuildGeneration builds a fresh immutable generation for gensig and adopts it
// atomically: it builds the shards + manifest into a unique temp directory,
// writes the READY sentinel LAST, then renames the temp directory into place. If
// a READY generation for gensig already exists it is a no-op (generations are
// content-addressed, so the same gensig is the same content — nothing to do).
//
// It NEVER writes in-place into a live generation directory, so concurrent
// readers on an older generation are never blocked or corrupted. Old generations
// are left on disk (GC is a later chunk).
//
// NOTE: this does not elect a build leader — concurrent builders of the SAME
// gensig each build into their own temp dir and the first to rename wins (the
// losers discard their temp dir). That is safe but redundant; the single-leader
// optimization lives in the (deferred) advanced coordination layer.
func BuildGeneration(dir, cacheDir, gensig string) error {
	_, err := buildGeneration(dir, cacheDir, gensig, noClaim)
	return err
}

// buildClaim says whether a build should come away HOLDING a reader claim on the
// generation it produced. It is a named type rather than a bare bool so the call
// sites read as what they mean instead of as a trailing true/false.
type buildClaim bool

const (
	noClaim   buildClaim = false
	withClaim buildClaim = true
)

// buildGeneration is BuildGeneration plus the option to come away holding a
// reader claim on the result. It returns a live registration only when claim is
// withClaim, and only on success; the caller owns it and must Close it.
//
// WHY A BUILD CAN CLAIM WITHOUT A WINDOW AND AN OPEN CANNOT. Registering AFTER
// the generation is adopted always leaves a window: from the instant the atomic
// rename publishes g/<gensig>/, the generation is READY, unclaimed, and therefore
// a legal target for every reaper in the arena, and it stays that way until the
// claim lands. That window is not theoretical — a reload's manifest read sits
// inside it, and a co-located reaper firing there was measured to delete the
// generation the reload then published (with the old code the claim's os.MkdirAll
// even recreated the directory, so the reload reported success over an empty
// shell).
//
// A build has something an open does not: the generation is still a PRIVATE temp
// directory nothing else can see. Writing the claim there, BEFORE the rename,
// means the directory enters the arena already carrying a live reader. There is
// no instant at which it is visible and unclaimed, so for this caller the window
// is not narrowed, it is absent.
//
// Callers that cannot do this — the content-addressed no-op, and the loser of an
// adopt race — fall back to the ordinary registerReader path and its
// claim/READY barrier, which is fail-closed but, as registerReader documents,
// still has an open window.
func buildGeneration(dir, cacheDir, gensig string, claim buildClaim) (*readerRegistration, error) {
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return nil, fmt.Errorf("no writable cache directory (set MCP_1C_CACHE_DIR to a writable path): %w", err)
	}

	genDir := generationDir(cpath, gensig)
	if generationReadyDir(genDir) {
		return claimBuiltGeneration(genDir, claim) // already built and adopted
	}

	gensDir := generationsDir(cpath)
	if err := os.MkdirAll(gensDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating generations dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp(gensDir, buildTmpPrefix+gensig+"-")
	if err != nil {
		return nil, fmt.Errorf("creating generation temp dir: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Build failed or lost the adopt race — drop the partial temp dir.
			if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
				slog.Warn("could not remove generation temp dir", "path", tmpDir, "error", rmErr)
			}
		}
	}()

	if err := buildGenerationInto(dir, tmpDir); err != nil {
		return nil, fmt.Errorf("building generation %q: %w", gensig, err)
	}

	// An empty-dump build writes no shards/manifest; ensure the dir exists so the
	// sentinel can be written and the (empty) generation is still adoptable.
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensuring generation temp dir: %w", err)
	}

	// Write the READY sentinel LAST, into the temp dir, BEFORE the atomic adopt.
	// The final generation dir therefore appears (via rename) already containing
	// READY and is never visible in a half-written, READY-less state.
	if err := writeReadySentinel(tmpDir, gensig); err != nil {
		return nil, fmt.Errorf("writing READY sentinel: %w", err)
	}

	// The claim goes in BEFORE the adopt. See the doc comment.
	var reg *readerRegistration
	if claim {
		if reg, err = claimReader(tmpDir, genDir); err != nil {
			return nil, fmt.Errorf("claiming the generation %q this build produced: %w", gensig, err)
		}
	}

	// Adopt atomically. If another builder adopted the same gensig first, the
	// rename fails (target non-empty); treat an existing READY generation as
	// success and let the deferred cleanup drop our temp dir.
	if err := os.Rename(tmpDir, genDir); err != nil {
		// The claim never reached the arena. Close removes it from the temp dir it
		// is still in, and does not block, because its heartbeat never started.
		reg.Close()
		if generationReadyDir(genDir) {
			return claimBuiltGeneration(genDir, claim)
		}
		return nil, fmt.Errorf("adopting generation %q: %w", gensig, err)
	}
	committed = true
	// The entry moved with the directory; only now does reg.path name a real file.
	reg.start()
	slog.Info("Built and adopted index generation", "gen", gensig, "dir", genDir)
	return reg, nil
}

// claimBuiltGeneration takes an ordinary post-adopt claim on an already-published
// generation, or nothing at all when the caller did not ask for one.
func claimBuiltGeneration(genDir string, claim buildClaim) (*readerRegistration, error) {
	if !claim {
		return nil, nil
	}
	return registerReader(genDir)
}

// forceDropGeneration removes the immutable generation directory genDir to force a
// COLD rebuild of gensig, but ONLY when no live reader currently holds it. It is the
// single force-drop primitive shared by the legacy in-memory reindex
// (reindexGeneration) and the concurrent serve reindex (ForceRebuildGeneration).
//
// The live-reader check is the core safety property: when a co-located process has
// this generation memory-mapped (generationHasLiveReader), the drop is SKIPPED so
// the holder's shard files are never yanked — on Windows an mmap-held file cannot be
// deleted at all, and on unix deleting it would corrupt the holder's view. A skipped
// (or failed) drop is deliberately non-fatal: the BuildGeneration call that follows
// then no-ops on the still-READY gensig, so the existing generation simply keeps
// serving. Forcing a genuine rebuild in that case requires stopping the other
// servers on this dump first.
func forceDropGeneration(genDir, gensig string) {
	if _, err := os.Stat(genDir); os.IsNotExist(err) {
		return // nothing built yet — a cold --reindex has nothing to drop
	}
	// Goes through the same claim-by-rename removal GCGenerations uses, so a
	// --reindex can no more yank a generation a co-located process claimed
	// mid-drop than a background GC can.
	dropped, err := removeUnheldGeneration(filepath.Dir(genDir), filepath.Base(genDir))
	if err != nil {
		slog.Error("reindex: refusing to drop the current generation before a rebuild; "+
			"BuildGeneration will reuse it if it is still adoptable", "gen", gensig, "error", err)
		return
	}
	if !dropped {
		slog.Warn("reindex: a live reader holds the current generation; serving the "+
			"existing generation WITHOUT an in-place rebuild (stop other servers on this "+
			"dump to force a full rebuild)", "gen", gensig)
	}
}

// ForceRebuildGeneration forces a COLD rebuild of the immutable generation for
// gensig and returns once a fresh generation is READY (or a live reader made the
// drop a no-op — see below). It is the concurrent serve path's equivalent of the
// legacy reindexGeneration force-rebuild: `serve --reindex` must rebuild even when
// the dump content is unchanged (the operator is recovering a suspected-corrupt
// cache), but a plain BuildGeneration is content-addressed and no-ops on an
// already-READY gensig. This drops the current generation first (via
// forceDropGeneration, which respects live readers) and then rebuilds it.
//
// Concurrency: when another co-located process is actively serving this exact
// gensig, forceDropGeneration skips the drop and the subsequent build no-ops, so
// the in-use generation is preserved and this caller serves it as-is rather than
// yanking it from the other process.
//
// IT PUBLISHES AN UNCLAIMED GENERATION; see AdoptFlatGeneration for why a caller
// that intends to serve the result must use PrepareServeGeneration instead.
func ForceRebuildGeneration(dir, cacheDir, gensig string) error {
	_, err := forceRebuildGeneration(dir, cacheDir, gensig, noClaim)
	return err
}

// forceRebuildGeneration is ForceRebuildGeneration plus the option to come away
// HOLDING the rebuilt generation. It returns a live registration only for
// withClaim, and only on success; the caller owns it and must Close it.
//
// The drop-then-build shape means the claim can come from either of two places.
// A drop that went through leaves nothing behind, so the build produces the
// generation and claims it inside its own temp dir — no window. A drop that was
// SKIPPED (a co-located process still holds this exact generation) leaves the
// existing generation in place, the build no-ops on it, and the claim is then the
// ordinary post-adopt one on a generation that was already published. Both cases
// come back through buildGeneration, which picks the right one.
func forceRebuildGeneration(dir, cacheDir, gensig string, claim buildClaim) (*readerRegistration, error) {
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return nil, fmt.Errorf("force-rebuild: no writable cache directory (set MCP_1C_CACHE_DIR to a writable path): %w", err)
	}
	forceDropGeneration(generationDir(cpath, gensig), gensig)
	reg, err := buildGeneration(dir, cacheDir, gensig, claim)
	if err != nil {
		return nil, fmt.Errorf("force-rebuild: building generation %q: %w", gensig, err)
	}
	return reg, nil
}

// buildGenerationInto builds the shards + manifest for dumpDir into targetDir
// synchronously, then closes the freshly-built shards (releasing their exclusive
// flock) so the directory can be renamed and later opened read-only. On build
// failure buildShards removes targetDir; this returns the build error.
func buildGenerationInto(dumpDir, targetDir string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idx := &Index{
		dir:           dumpDir,
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]cachedModule),
		pathByName:    make(map[string]string),
		pathToDocID:   make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	// buildShards loads BSL paths from idx.dir (the dump) and writes shards +
	// manifest into targetDir. Run it synchronously (not in the NewIndex
	// goroutine) — we only want the on-disk side effects.
	idx.buildShards(targetDir, true)
	if err := idx.BuildError(); err != nil {
		return err
	}

	// Close the freshly-built shards: buildShardOffline opens each shard mutable
	// (LOCK_EX); they must be closed before the directory is renamed (Windows
	// cannot rename open files) and before any read-only reopen takes LOCK_SH.
	var firstErr error
	for _, s := range idx.shards {
		if s == nil {
			continue
		}
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// writeReadySentinel writes the READY sentinel into genDir. The file's PRESENCE
// is the authority that a generation is complete and adoptable; its contents are
// advisory (gensig + build timestamp + derivation version) for debugging.
func writeReadySentinel(genDir, gensig string) error {
	body := fmt.Sprintf("gensig=%s\ngensig_version=%d\nschema_version=%d\nzap_version=%d\nbuilt=%s\n",
		gensig, genSigVersion, dumpIndexSchemaVersion, zapSegmentVersion,
		time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(readySentinelPath(genDir), []byte(body), 0o644)
}

// OpenForServe opens dir for serving, preferring the immutable generation path.
// If a READY generation for the current dump signature exists it is opened
// READ-ONLY (so N concurrent serves on the same dump coexist). If not, but a
// LEGACY flat cache exists, it is migrated to the generation layout in place
// (migrateFlatToGeneration — adopt the existing shards, or one-time build if
// adoption is unsafe) and the resulting generation is opened read-only. Only when
// neither a generation nor a flat cache is present (or migration fails) does it
// fall back to the legacy flat NewIndex behavior (backward-compat read/build).
//
// This is the foundational read path. It does NOT build a missing generation FROM
// AN EMPTY CACHE or elect a build leader — that orchestration (build-on-miss,
// async readiness, leader election) is the deferred advanced layer; a first-ever
// open with no cache still degrades to the single-writer flat build.
func OpenForServe(dir, cacheDir string) (*Index, error) {
	gensig, err := GenSig(dir)
	if err != nil {
		slog.Warn("dump: could not compute generation signature; using legacy flat cache",
			"dir", dir, "error", err)
		return NewIndex(dir, cacheDir, false)
	}
	if GenerationReady(dir, cacheDir, gensig) {
		// A generation that was already in the arena. Nothing to claim early — see
		// registerReader for the residual window this path keeps.
		return OpenGenerationReadOnly(dir, cacheDir, gensig)
	}
	// No READY generation yet. If a legacy flat cache exists, migrate it once to
	// the generation layout so this and future serves use the concurrent read-only
	// path instead of the single-writer flat cache. The migration claims what it
	// produces BEFORE publishing it and hands the claim straight to the open, so
	// the generation this call created is never reapable while unheld.
	g, reg, migrated, mErr := migrateFlatToGeneration(dir, cacheDir, withClaim)
	if mErr != nil {
		slog.Warn("dump: flat→generation migration failed; using legacy flat cache",
			"dir", dir, "error", mErr)
	} else if migrated {
		return openClaimedGeneration(dir, cacheDir, g, reg)
	}
	return NewIndex(dir, cacheDir, false)
}

// openClaimedGeneration opens a generation this process just produced AND already
// holds reg on, read-only. It is OpenGenerationReadOnly with the claim supplied
// rather than taken here; the READY check is kept because a producer that returned
// no generation at all must not reach the attach.
func openClaimedGeneration(dir, cacheDir, gensig string, reg *readerRegistration) (*Index, error) {
	// No claim means the generation reached the arena unheld and a reaper may
	// already be removing it. Refuse rather than serve, exactly as the post-adopt
	// path does. Every producer returns either a claim or an error, so this guards
	// a future edit rather than a state reachable today.
	if reg == nil {
		return nil, fmt.Errorf("the migrated generation %s carries no reader claim, so it cannot be served",
			gensig)
	}
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		reg.Close()
		return nil, err
	}
	genDir := generationDir(cpath, gensig)
	if !generationReadyDir(genDir) {
		reg.Close()
		return nil, fmt.Errorf("generation %q is not ready (no %s sentinel at %s)",
			gensig, readySentinelName, genDir)
	}
	return openReadOnlyFrom(dir, cacheDir, genDir, reg)
}

// migrateFlatToGeneration migrates an existing LEGACY flat cache (shard_* directly
// under the per-dump cache dir, no generation for the current signature yet) to
// the immutable generation layout, WITHOUT a full rebuild storm. It returns the
// current gensig, whether a READY generation for it now exists, and any error.
//
// It PREFERS adopting the existing flat shards as the first generation: a metadata
// move (rename) of each shard_* dir into g/<gensig>/ plus a READY sentinel, which
// is O(number-of-shards) and re-indexes nothing. It falls back to a one-time
// BuildGeneration (logged) ONLY when adoption is unsafe — the flat cache is in use
// by another process, has no compatible manifest, was built under a different
// index schema / zap format, or has drifted from the current dump. The build
// fallback never rewrites the flat cache in place and never touches a generation a
// live reader holds.
//
// Backward-compat: when there is nothing to migrate (no flat cache) it is a no-op
// (migrated=false) and the caller's legacy flat path still opens/builds normally;
// a failed adoption rolls the flat shards back so the flat cache remains openable.
//
// claim says whether the caller wants to come away HOLDING the generation. With
// withClaim every branch that produces one claims it while it is still private
// (adoptFlatShards writes the claim into its temp dir, buildGeneration into its
// own), so the caller never has to claim a generation that has already been
// visible in the arena. The branch that finds a generation ALREADY READY has no
// such phase and returns no claim, so its caller takes the ordinary post-adopt
// one. A non-nil registration is returned ONLY on a successful migrated=true.
func migrateFlatToGeneration(dir, cacheDir string, claim buildClaim) (string, *readerRegistration, bool, error) {
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return "", nil, false, err
	}
	gensig, err := GenSig(dir)
	if err != nil {
		return "", nil, false, fmt.Errorf("computing generation signature for migration: %w", err)
	}
	genDir := generationDir(cpath, gensig)

	// Already a READY generation for this signature — nothing to migrate, and
	// nothing this call could have claimed early either.
	if generationReadyDir(genDir) {
		reg, cErr := claimBuiltGeneration(genDir, claim)
		if cErr != nil {
			return gensig, nil, false, cErr
		}
		return gensig, reg, true, nil
	}
	// No legacy flat shards under the cache root — nothing to migrate.
	shardDirs := cacheShardDirs(cpath)
	if len(shardDirs) == 0 {
		return gensig, nil, false, nil
	}

	ok, reason := flatCacheAdoptable(cpath, dir)
	if ok {
		reg, aErr := adoptFlatShards(cpath, gensig, shardDirs, claim)
		if aErr != nil {
			return gensig, nil, false, fmt.Errorf("adopting flat cache as generation %q: %w", gensig, aErr)
		}
		slog.Info("Migrated legacy flat cache to a generation by adopting its shards (no rebuild)",
			"gen", gensig, "shards", len(shardDirs))
		return gensig, reg, generationReadyDir(genDir), nil
	}

	// Adoption is unsafe — build a fresh generation ONCE instead. This never
	// rewrites the flat cache in place; the flat shards are left intact as a
	// backward-compatible fallback until a later (deferred) flat-cache GC.
	slog.Info("Legacy flat cache not safely adoptable; building a fresh generation once",
		"gen", gensig, "reason", reason)
	reg, err := buildGeneration(dir, cacheDir, gensig, claim)
	if err != nil {
		return gensig, nil, false, fmt.Errorf("building generation for migration: %w", err)
	}
	return gensig, reg, generationReadyDir(genDir), nil
}

// AdoptFlatGeneration is the exported entry point for the serve build-leader's
// flat→generation adoption shim. It attempts to migrate an existing LEGACY flat
// cache to the immutable generation layout via the cheap O(shards) adopt-by-rename
// (which also RECLAIMS the flat cache, since the shards are MOVED, not copied),
// falling back to a one-time build ONLY when adoption is unsafe — and NEVER to a
// flat NewIndex rebuild. It returns the current gensig, whether a READY generation
// for it now exists (true after an adopt or a one-time build), and any error.
//
// When there is no flat cache to migrate it is a no-op (migrated=false, err=nil):
// the caller is serving a genuinely cache-less dump and must build a fresh
// generation itself. Distinct from OpenForServe, this never builds a flat cache on
// a cache miss, so a build-leader can branch on "adopted vs needs-cold-build"
// WITHOUT regressing a fresh dump to the single-writer flat path.
//
// The flat-shard move is guarded by the read-cache lock (serve.lock): a flat cache
// another live process still serves is reported non-adoptable, so that process's
// memory-mapped shards are never moved out from under it.
//
// IT PUBLISHES AN UNCLAIMED GENERATION, so a caller that intends to SERVE the
// result must not use it: between this returning and that caller's claim the
// generation is READY and held by nobody, which is a legal target for every reaper
// in the arena. PrepareServeGeneration is the serving path and takes the claim
// with the migration instead. This entry point remains for callers that only want
// the flat cache reclaimed.
func AdoptFlatGeneration(dir, cacheDir string) (gensig string, migrated bool, err error) {
	gensig, _, migrated, err = migrateFlatToGeneration(dir, cacheDir, noClaim)
	return gensig, migrated, err
}

// flatCacheAdoptable reports whether the legacy flat cache under cpath can be
// SAFELY adopted as the generation for the current dump — i.e. moved into
// g/<gensig>/ and trusted to match that signature by construction — and, if not, a
// human-readable reason for the build fallback. Adoption is safe only when no
// other process holds the flat cache open, a version-compatible manifest is
// present, the manifest's schema+zap versions match the running binary, and the
// flat cache has not drifted from the current dump content.
func flatCacheAdoptable(cpath, dir string) (bool, string) {
	// A foreign process serving the flat cache (serve.lock) must not have its
	// shard files moved out from under it; build a separate generation instead. A
	// stale lock conservatively forces the (safe) build fallback rather than risk
	// corrupting a live process — at worst a one-time rebuild.
	if pid, present := readCacheLock(cpath); present && pid != os.Getpid() {
		return false, fmt.Sprintf("flat cache is in use (serve.lock pid=%d)", pid)
	}

	m, err := LoadManifest(cpath)
	if err != nil {
		return false, fmt.Sprintf("flat manifest unreadable: %v", err)
	}
	if m == nil {
		// No manifest (or an incompatible manifest version): cannot verify the flat
		// shards match the current dump+schema, so adopting them under this gensig
		// would be unfounded. Rebuild instead.
		return false, "flat manifest missing or incompatible version"
	}
	if m.schemaVersion() != dumpIndexSchemaVersion || m.zapVersion() != zapSegmentVersion {
		return false, fmt.Sprintf("flat cache schema/format mismatch (cache schema=%d zap=%d; current schema=%d zap=%d)",
			m.schemaVersion(), m.zapVersion(), dumpIndexSchemaVersion, zapSegmentVersion)
	}
	diff, err := m.Diff(dir)
	if err != nil {
		return false, fmt.Sprintf("flat cache drift check failed: %v", err)
	}
	if !diff.Empty() {
		return false, fmt.Sprintf("flat cache is stale (added=%d modified=%d deleted=%d)",
			len(diff.Added), len(diff.Modified), len(diff.Deleted))
	}
	return true, ""
}

// flatCacheSchemaStale reports whether the legacy flat cache under cpath was built
// under an index schema / on-disk zap format the running binary cannot safely reuse
// (see the BUMP PROTOCOL above). A stale flat cache must be dropped and cold-rebuilt
// rather than served: its shard docIDs and manifest keys were produced under a
// different schema — e.g. before module names were NFC-normalised, so a macOS dump's
// decomposable (short-I / IO) names were stored NFD and never match an NFC query —
// so reusing it silently breaks module_code / resolve for those names.
//
// It returns true ONLY for a readable, version-compatible manifest whose stamped
// schema or zap version differs from the current binary's. It returns false (reuse)
// when the manifest is absent or carries an incompatible manifest version
// (LoadManifest -> nil): loadFromManifestAndDiff then re-walks the dump with the
// current schema, so there is nothing trustworthy to gate on. It also returns false
// on a manifest read error, leaving the existing warm-start path to surface it
// rather than dropping a cache on a transient I/O hiccup.
func flatCacheSchemaStale(cpath string) bool {
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		return false
	}
	return m.schemaVersion() != dumpIndexSchemaVersion || m.zapVersion() != zapSegmentVersion
}

// adoptFlatShards adopts the legacy flat shards (shardDirs, all directly under
// cpath) as the immutable generation for gensig by MOVING them — not rebuilding
// them — into g/<gensig>/. It mirrors BuildGeneration's atomic-adopt invariant:
// the shards (and the flat manifest, if present) are renamed into a unique temp
// dir, the READY sentinel is written LAST, and the temp dir is renamed into place,
// so the generation only ever becomes visible already containing READY. The whole
// move is metadata-only (same filesystem), so adoption re-indexes nothing — the
// no-rebuild-storm guarantee.
//
// On any failure before the atomic adopt, the already-moved entries are renamed
// back so the flat cache is restored intact (a bypassed/failed migration must
// still leave an openable flat cache). If a concurrent migrator/builder adopts the
// same gensig first, this rolls back too and defers to that equivalent generation.
//
// claim mirrors buildGeneration's, for the same reason and at the same point: the
// claim is written into the temp dir BEFORE the atomic adopt, so the adopted
// generation enters the arena already held and is never observable as
// READY-and-unclaimed. On the concurrent-adopter branch there is no such phase —
// the generation being deferred to is someone else's, already published — so the
// claim taken there is the ordinary post-adopt one.
func adoptFlatShards(cpath, gensig string, shardDirs []string, claim buildClaim) (*readerRegistration, error) {
	gensDir := generationsDir(cpath)
	if err := os.MkdirAll(gensDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating generations dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(gensDir, buildTmpPrefix+gensig+"-")
	if err != nil {
		return nil, fmt.Errorf("creating migration temp dir: %w", err)
	}

	type move struct{ from, to string }
	var moved []move
	committed := false
	defer func() {
		if committed {
			return
		}
		// Restore the flat layout (reverse order) so a failed migration leaves a
		// working flat cache, then drop the temp dir.
		for i := len(moved) - 1; i >= 0; i-- {
			if rbErr := os.Rename(moved[i].to, moved[i].from); rbErr != nil {
				slog.Warn("migration rollback: could not restore flat cache entry",
					"from", moved[i].to, "to", moved[i].from, "error", rbErr)
			}
		}
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			slog.Warn("migration: could not remove temp dir after rollback", "path", tmpDir, "error", rmErr)
		}
	}()

	// Move each flat shard dir into the temp generation dir (rename = O(1), no
	// re-index — the whole point of adoption over a rebuild).
	for _, sd := range shardDirs {
		dst := filepath.Join(tmpDir, filepath.Base(sd))
		if err := os.Rename(sd, dst); err != nil {
			return nil, fmt.Errorf("moving flat shard %s: %w", filepath.Base(sd), err)
		}
		moved = append(moved, move{from: sd, to: dst})
	}

	// Move the flat manifest too so the adopted generation serves names from it
	// (loadNamesReadOnly reads <genDir>/manifest.json). Absence is tolerated:
	// loadNamesReadOnly falls back to a read-only dump walk.
	if src := manifestPath(cpath); fileExists(src) {
		dst := manifestPath(tmpDir)
		if err := os.Rename(src, dst); err != nil {
			return nil, fmt.Errorf("moving flat manifest: %w", err)
		}
		moved = append(moved, move{from: src, to: dst})
	}

	// Write READY LAST, then adopt atomically (temp → g/<gensig>).
	if err := writeReadySentinel(tmpDir, gensig); err != nil {
		return nil, fmt.Errorf("writing READY sentinel: %w", err)
	}

	// The claim goes in BEFORE the adopt, so the generation is already held the
	// instant it becomes visible. See buildGeneration's doc comment.
	var reg *readerRegistration
	if claim {
		if reg, err = claimReader(tmpDir, generationDir(cpath, gensig)); err != nil {
			return nil, fmt.Errorf("claiming the generation %q this migration adopted: %w", gensig, err)
		}
	}

	genDir := generationDir(cpath, gensig)
	if err := os.Rename(tmpDir, genDir); err != nil {
		// The claim never reached the arena. Close removes it from the temp dir it
		// is still in, and does not block, because its heartbeat never started.
		reg.Close()
		if generationReadyDir(genDir) {
			// A concurrent migrator/builder adopted this gensig first. Leave
			// committed=false so the deferred rollback restores our flat cache as a
			// fallback; the winner's equivalent (same-gensig) generation is what
			// callers open — and, being someone else's already-published generation,
			// it can only be claimed post-adopt.
			slog.Info("migration: a concurrent process adopted this generation first; "+
				"keeping the flat cache as fallback", "gen", gensig)
			return claimBuiltGeneration(genDir, claim)
		}
		return nil, fmt.Errorf("adopting migrated generation %q: %w", gensig, err)
	}
	committed = true
	// The entry moved with the directory; only now does reg.path name a real file.
	reg.start()
	return reg, nil
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}
