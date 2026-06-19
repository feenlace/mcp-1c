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
// (instancelock) + async-readiness wiring (advanced layer, after re-vendor), the
// per-process extension overlay, the schema/format version component of gensig,
// and the legacy-flat → generation migration shim.
const (
	generationsDirName = "g"
	readySentinelName  = "READY"
	buildTmpPrefix     = ".building-"

	// defaultBoltTimeout bounds how long a read-only open waits for a conflicting
	// flock before failing. MUST be a Go duration STRING (see openCachedShards).
	defaultBoltTimeout = "5s"

	// genSigVersion versions the gensig DERIVATION (not the index format). Bump it
	// only if the way GenSig hashes the dump changes. The index schema/format
	// version is deliberately NOT folded in yet (deferred); when added it becomes
	// another component below, naturally yielding a fresh generation on a bump.
	genSigVersion = 1
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

// GenSig computes the content signature of a dump directory: a short hex hash
// over the sorted (relative-path, mtime-ms, size) tuples of every .bsl file. Two
// dumps with identical file content+metadata yield the same signature and thus
// share one immutable generation; any drift (add / remove / modify) yields a new
// signature, so a rebuild produces a fresh generation directory rather than
// mutating one in use.
//
// It walks the dump once (the same cost as the warm-start manifest diff that
// already runs on every open today). The schema/format version is not part of
// the signature yet (deferred to a later chunk).
func GenSig(dir string) (string, error) {
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
	fmt.Fprintf(h, "v%d\n", genSigVersion)
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
	body := fmt.Sprintf("gensig=%s\ngensig_version=%d\nbuilt=%s\n",
		gensig, genSigVersion, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(readySentinelPath(genDir), []byte(body), 0o644)
}

// OpenForServe opens dir for serving, preferring the immutable generation path.
// If a READY generation for the current dump signature exists it is opened
// READ-ONLY (so N concurrent serves on the same dump coexist); otherwise it
// falls back to the legacy flat NewIndex behavior (backward-compat read).
//
// This is the foundational read path. It does NOT build a missing generation or
// elect a build leader — that orchestration (build-on-miss, async readiness,
// leader election) is the deferred advanced layer; until then a missing
// generation simply degrades to the existing single-writer flat cache.
func OpenForServe(dir, cacheDir string) (*Index, error) {
	if gensig, err := GenSig(dir); err == nil {
		if GenerationReady(dir, cacheDir, gensig) {
			return OpenGenerationReadOnly(dir, cacheDir, gensig)
		}
	} else {
		slog.Warn("dump: could not compute generation signature; using legacy flat cache",
			"dir", dir, "error", err)
	}
	return NewIndex(dir, cacheDir, false)
}
