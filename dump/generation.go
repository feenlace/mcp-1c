package dump

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
//     (bslDocument), or the shard-assignment hash (shardForID/splitByHash) — any
//     change after which a generation built by an OLDER binary would yield wrong
//     or degraded results if SERVED by a NEWER one. Bumping it forces every reader
//     onto a freshly-built generation rather than silently mis-reading old shards.
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
	dumpIndexSchemaVersion = 1

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
	return openReadOnlyFrom(dir, genDir)
}

// openReadOnlyFrom builds an Index serving the already-built shards under genDir
// in read-only mode. Names/paths are loaded from the generation's manifest in a
// background goroutine (Ready()/Done() follow the usual contract).
func openReadOnlyFrom(dumpDir, genDir string) (*Index, error) {
	ctx, cancel := context.WithCancel(context.Background())
	idx := &Index{
		dir:           dumpDir,
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]string),
		pathByName:    make(map[string]string),
		pathToDocID:   make(map[string]string),
		readOnly:      true,
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	if err := idx.attachReadOnlyShards(genDir); err != nil {
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

// attachReadOnlyShards opens the shards under genDir READ-ONLY (bbolt LOCK_SH) and
// attaches them to idx, marks idx read-only, and registers idx as a live holder of
// the generation in its readers/ registry so concurrent old-generation GC never
// reaps a generation this process is serving. The reader is registered BEFORE the
// shards are opened, so a live reader becomes visible to GC as early as possible;
// if the open fails the registration is rolled back. The caller is responsible for
// loading names and flipping Ready().
func (idx *Index) attachReadOnlyShards(genDir string) error {
	// Register first so a live reader is visible to GC before any shard FD/mmap
	// exists. Registration is best-effort: serving without it only risks a benign
	// GC race (unix readers keep their open shards; Windows removal fails on held
	// files), so a registration error is logged, not fatal.
	if reg, err := registerReader(genDir); err != nil {
		slog.Warn("dump: could not register reader; concurrent GC could reap this "+
			"generation while it is served", "genDir", genDir, "error", err)
	} else {
		idx.readerReg = reg
	}

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
//   - a non-adopted directory (no READY sentinel) — left for a future cleanup.
//
// Removal is best-effort and per-generation: a permission error (a cross-user
// generation on a shared cacheDir) is skipped, not fatal, so one undeletable
// generation never blocks reclaiming the others. Returns the gensigs actually
// removed.
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
		if generationHasLiveReader(genDir) {
			continue // a live reader still holds it — MUST NOT remove
		}
		if err := os.RemoveAll(genDir); err != nil {
			if os.IsPermission(err) {
				slog.Debug("GC: skipping generation owned by another user", "gen", name, "error", err)
			} else {
				slog.Warn("GC: could not remove old generation", "gen", name, "error", err)
			}
			continue
		}
		removed = append(removed, name)
		slog.Info("GC: removed old unheld generation", "gen", name)
	}
	return removed, nil
}

// ReapStaleBuildDirs removes ABANDONED .building-* temp generation dirs — the
// partial generation a builder leaves behind when it dies mid-build (SIGKILL, OOM,
// power loss) before it can either adopt the build (atomic rename into g/<gensig>/)
// or roll it back. Nothing else reaps these: GCGenerations deliberately SKIPS
// .building-* (from its readers/-registry vantage it cannot tell an in-progress
// build from a dead one), and the post-build deferred cleanup only runs in the
// process that survives the build. So without this, every interrupted build leaks a
// partial generation (hundreds of MB of shards) that grows on disk unbounded until
// ENOSPC — the realistic trigger being an async-readiness kill/rebuild cascade that
// SIGKILLs the builder repeatedly. The same prefix is used by BuildGeneration and
// adoptFlatShards, so this reaps an interrupted flat-cache adoption's temp dir too.
//
// It is meant to be called on startup / BEFORE electing a build-leader (see the
// serve open path), not only after a successful build, so leaks are reclaimed even
// when the leaking process never reaches its own post-build cleanup.
//
// SAFETY — a fresh in-progress build MUST survive: a .building-* dir is removed
// ONLY when the newest mtime ANYWHERE in its tree is older than buildDirStaleAfter,
// i.e. nothing has been written to it for far longer than any healthy build pauses.
// A CONCURRENT build-leader actively streaming segments keeps its tree fresh and is
// therefore never reaped; the caller of this function has not yet started its own
// build, so it has no live temp dir of its own to protect. Removal is best-effort
// and per-dir: a permission error (a cross-user temp dir on a shared cacheDir) is
// skipped, not fatal, so one undeletable dir never blocks reclaiming the others.
//
// A crash mid-adoptFlatShards can leave the only copy of the legacy flat shards
// inside the reaped temp dir (they are MOVED, not copied, and the rollback never
// ran); reaping it loses a CACHE that the next open rebuilds, never source data.
// Returns the temp-dir names actually removed.
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
		if !e.IsDir() || !strings.HasPrefix(e.Name(), buildTmpPrefix) {
			continue
		}
		tmpDir := filepath.Join(gensDir, e.Name())
		if !buildDirStale(tmpDir, cutoff) {
			continue // fresh — a live build may still be writing it; MUST NOT remove
		}
		if err := os.RemoveAll(tmpDir); err != nil {
			if os.IsPermission(err) {
				slog.Debug("reap: skipping build temp dir owned by another user", "dir", e.Name(), "error", err)
			} else {
				slog.Warn("reap: could not remove stale build temp dir", "dir", e.Name(), "error", err)
			}
			continue
		}
		removed = append(removed, e.Name())
		slog.Info("reap: removed abandoned build temp dir", "dir", e.Name())
	}
	return removed, nil
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
	manifest, err := LoadManifest(genDir)
	if err != nil {
		return fmt.Errorf("loading generation manifest: %w", err)
	}
	if manifest == nil {
		return idx.loadBSLPaths(idx.dir)
	}

	idx.mu.Lock()
	// pathToDocID is not pre-initialized on every Index-creation path (NewIndex's
	// reindex path reuses its own idx, which leaves it nil); init it lazily, as
	// loadBSLPaths does, so this shared helper is safe regardless of caller.
	if idx.pathToDocID == nil {
		idx.pathToDocID = make(map[string]string, len(manifest.Files))
	}
	for relPath, entry := range manifest.Files {
		absPath := filepath.Join(idx.dir, filepath.FromSlash(relPath))
		idx.names = append(idx.names, entry.DocID)
		idx.pathByName[entry.DocID] = absPath
		idx.pathToDocID[relPath] = entry.DocID
	}
	slices.Sort(idx.names)
	idx.mu.Unlock()
	return nil
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
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return fmt.Errorf("no writable cache directory (set MCP_1C_CACHE_DIR to a writable path): %w", err)
	}

	genDir := generationDir(cpath, gensig)
	if generationReadyDir(genDir) {
		return nil // already built and adopted
	}

	gensDir := generationsDir(cpath)
	if err := os.MkdirAll(gensDir, 0o755); err != nil {
		return fmt.Errorf("creating generations dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp(gensDir, buildTmpPrefix+gensig+"-")
	if err != nil {
		return fmt.Errorf("creating generation temp dir: %w", err)
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
		return fmt.Errorf("building generation %q: %w", gensig, err)
	}

	// An empty-dump build writes no shards/manifest; ensure the dir exists so the
	// sentinel can be written and the (empty) generation is still adoptable.
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("ensuring generation temp dir: %w", err)
	}

	// Write the READY sentinel LAST, into the temp dir, BEFORE the atomic adopt.
	// The final generation dir therefore appears (via rename) already containing
	// READY and is never visible in a half-written, READY-less state.
	if err := writeReadySentinel(tmpDir, gensig); err != nil {
		return fmt.Errorf("writing READY sentinel: %w", err)
	}

	// Adopt atomically. If another builder adopted the same gensig first, the
	// rename fails (target non-empty); treat an existing READY generation as
	// success and let the deferred cleanup drop our temp dir.
	if err := os.Rename(tmpDir, genDir); err != nil {
		if generationReadyDir(genDir) {
			return nil
		}
		return fmt.Errorf("adopting generation %q: %w", gensig, err)
	}
	committed = true
	slog.Info("Built and adopted index generation", "gen", gensig, "dir", genDir)
	return nil
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
		contentByName: make(map[string]string),
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
		return OpenGenerationReadOnly(dir, cacheDir, gensig)
	}
	// No READY generation yet. If a legacy flat cache exists, migrate it once to
	// the generation layout so this and future serves use the concurrent read-only
	// path instead of the single-writer flat cache.
	if g, migrated, mErr := migrateFlatToGeneration(dir, cacheDir); mErr != nil {
		slog.Warn("dump: flat→generation migration failed; using legacy flat cache",
			"dir", dir, "error", mErr)
	} else if migrated {
		return OpenGenerationReadOnly(dir, cacheDir, g)
	}
	return NewIndex(dir, cacheDir, false)
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
func migrateFlatToGeneration(dir, cacheDir string) (string, bool, error) {
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return "", false, err
	}
	gensig, err := GenSig(dir)
	if err != nil {
		return "", false, fmt.Errorf("computing generation signature for migration: %w", err)
	}
	genDir := generationDir(cpath, gensig)

	// Already a READY generation for this signature — nothing to migrate.
	if generationReadyDir(genDir) {
		return gensig, true, nil
	}
	// No legacy flat shards under the cache root — nothing to migrate.
	shardDirs := cacheShardDirs(cpath)
	if len(shardDirs) == 0 {
		return gensig, false, nil
	}

	ok, reason := flatCacheAdoptable(cpath, dir)
	if ok {
		if err := adoptFlatShards(cpath, gensig, shardDirs); err != nil {
			return gensig, false, fmt.Errorf("adopting flat cache as generation %q: %w", gensig, err)
		}
		slog.Info("Migrated legacy flat cache to a generation by adopting its shards (no rebuild)",
			"gen", gensig, "shards", len(shardDirs))
		return gensig, generationReadyDir(genDir), nil
	}

	// Adoption is unsafe — build a fresh generation ONCE instead. This never
	// rewrites the flat cache in place; the flat shards are left intact as a
	// backward-compatible fallback until a later (deferred) flat-cache GC.
	slog.Info("Legacy flat cache not safely adoptable; building a fresh generation once",
		"gen", gensig, "reason", reason)
	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		return gensig, false, fmt.Errorf("building generation for migration: %w", err)
	}
	return gensig, generationReadyDir(genDir), nil
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
func AdoptFlatGeneration(dir, cacheDir string) (gensig string, migrated bool, err error) {
	return migrateFlatToGeneration(dir, cacheDir)
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
func adoptFlatShards(cpath, gensig string, shardDirs []string) error {
	gensDir := generationsDir(cpath)
	if err := os.MkdirAll(gensDir, 0o755); err != nil {
		return fmt.Errorf("creating generations dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(gensDir, buildTmpPrefix+gensig+"-")
	if err != nil {
		return fmt.Errorf("creating migration temp dir: %w", err)
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
			return fmt.Errorf("moving flat shard %s: %w", filepath.Base(sd), err)
		}
		moved = append(moved, move{from: sd, to: dst})
	}

	// Move the flat manifest too so the adopted generation serves names from it
	// (loadNamesReadOnly reads <genDir>/manifest.json). Absence is tolerated:
	// loadNamesReadOnly falls back to a read-only dump walk.
	if src := manifestPath(cpath); fileExists(src) {
		dst := manifestPath(tmpDir)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("moving flat manifest: %w", err)
		}
		moved = append(moved, move{from: src, to: dst})
	}

	// Write READY LAST, then adopt atomically (temp → g/<gensig>).
	if err := writeReadySentinel(tmpDir, gensig); err != nil {
		return fmt.Errorf("writing READY sentinel: %w", err)
	}

	genDir := generationDir(cpath, gensig)
	if err := os.Rename(tmpDir, genDir); err != nil {
		if generationReadyDir(genDir) {
			// A concurrent migrator/builder adopted this gensig first. Leave
			// committed=false so the deferred rollback restores our flat cache as a
			// fallback; the winner's equivalent (same-gensig) generation is what
			// callers open.
			slog.Info("migration: a concurrent process adopted this generation first; "+
				"keeping the flat cache as fallback", "gen", gensig)
			return nil
		}
		return fmt.Errorf("adopting migrated generation %q: %w", gensig, err)
	}
	committed = true
	return nil
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}
