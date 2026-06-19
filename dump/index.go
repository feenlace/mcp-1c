package dump

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	scorchIndex "github.com/blevesearch/bleve/v2/index/scorch"
	"github.com/blevesearch/bleve/v2/search/query"
)

// utf8BOM is the 3-byte UTF-8 Byte Order Mark (U+FEFF) that 1C DumpConfigToFiles
// prepends to BSL files. It must be stripped before indexing or returning content.
const utf8BOM = "\xEF\xBB\xBF"

// showProgress controls whether this package prints progress and info messages
// to stderr. When false (the default, matching the cautious v1.6.1 behaviour),
// no stderr writes are performed, so strict MCP clients do not see them as
// errors. When true, the v1.6.0 progress ticker and informational lines are
// restored for interactive terminal launches.
var showProgress atomic.Bool

// SetShowProgress toggles progress output on stderr. Called from main once the
// effective TTY mode is known (pipe/terminal plus --quiet/--verbose overrides).
func SetShowProgress(v bool) { showProgress.Store(v) }

// stripBOM removes the UTF-8 BOM prefix from s if present.
func stripBOM(s string) string {
	return strings.TrimPrefix(s, utf8BOM)
}

// readErrLogInterval bounds how often module-read-error warnings are emitted, so
// a broadly unreadable dump (a locked directory, an antivirus quarantine, a
// paused cloud-sync folder) cannot flood the log with one line per file.
const readErrLogInterval = 5 * time.Second

// readRetryDelay is the pause before the single retry readModuleContent makes
// when a file read fails. Transient Windows file locks (antivirus / OneDrive /
// the OS search indexer briefly holding the handle) usually clear within it.
const readRetryDelay = 50 * time.Millisecond

var (
	readErrLogLast       atomic.Int64 // UnixNano of the last emitted read-error warning
	readErrLogSuppressed atomic.Int64 // read-error warnings suppressed since the last emit
)

// warnModuleReadErr emits a rate-limited warning that a module file could not be
// read, so its exclusion from a build or a search result is observable instead of
// a silent false-negative. At most one warning per readErrLogInterval is emitted;
// the number suppressed in between is folded into the next emitted line.
func warnModuleReadErr(path string, err error) {
	now := time.Now().UnixNano()
	prev := readErrLogLast.Load()
	if now-prev < int64(readErrLogInterval) || !readErrLogLast.CompareAndSwap(prev, now) {
		readErrLogSuppressed.Add(1)
		return
	}
	slog.Warn("dump: module file unreadable, excluded from this result "+
		"(check file lock / antivirus / cloud-sync)",
		"path", path,
		"error", err,
		"suppressed_since_last", readErrLogSuppressed.Swap(0))
}

// readModuleContent reads a BSL module's source from disk and strips the UTF-8
// BOM. It retries once after a short pause on failure, because module files are
// occasionally locked for a moment by antivirus / cloud-sync / the OS search
// indexer (common on Windows). If the read still fails the module is reported as
// unavailable (so callers exclude it) and a rate-limited warning is logged — the
// file does not silently disappear from results without a trace.
func readModuleContent(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		time.Sleep(readRetryDelay)
		data, err = os.ReadFile(path)
	}
	if err != nil {
		warnModuleReadErr(path, err)
		return "", false
	}
	return stripBOM(string(data)), true
}

// Match represents a single search hit in a BSL module.
type Match struct {
	Module  string  // Human-readable module path (e.g. "Документ.РеализацияТоваров.МодульОбъекта")
	Line    int     // 1-based line number of the match
	Context string  // Surrounding lines for context
	Score   float64 // BM25 relevance score (smart mode only)
}

// extractContext returns lines around the given index with a context window.
func extractContext(lines []string, idx, window int) string {
	start := max(idx-window, 0)
	end := min(idx+window+1, len(lines))
	return strings.Join(lines[start:end], "\n")
}

// synonymMapOnce ensures buildSynonymMap is called only once.
var (
	synonymMapOnce   sync.Once
	cachedSynonymMap map[string]string
)

// moduleNameSuffixes maps BSL file names to their module type suffix.
// The lookup key is the bare file name (last path segment), so each entry
// covers both the XML dump layout (.../Ext/<File>.bsl) and the EDT layout
// (.../<File>.bsl).
var moduleNameSuffixes = map[string]string{
	"ObjectModule.bsl":       "МодульОбъекта",
	"ManagerModule.bsl":      "МодульМенеджера",
	"Module.bsl":             "МодульФормы",
	"RecordSetModule.bsl":    "МодульНабораЗаписей",
	"CommandModule.bsl":      "МодульКоманды",
	"ValueManagerModule.bsl": "МодульМенеджераЗначения",
}

// subdirSegmentNames maps a dump path subdirectory to the Russian segment that
// names its child in a module name. A path passing through such a subdirectory
// gets an extra ".<segment>.<childName>." inserted (e.g. Forms/ФормаДок ->
// ".Форма.ФормаДок.").
var subdirSegmentNames = map[string]string{
	"Forms":    "Форма",
	"Commands": "Команда",
}

// extensionDirName is the top-level dump directory that holds configuration
// extensions ("Расширения"). Inside it each extension owns a subtree that
// mirrors the base-config layout: Расширения/<ext>/<Kind>/<name>/Ext/<File>.bsl.
const extensionDirName = "Расширения"

// bslPathToModuleName converts a relative file path from the dump to a human-readable module name.
// Example: "Documents/РеализацияТоваров/Ext/ObjectModule.bsl" -> "Документ.РеализацияТоваров.МодульОбъекта"
//
// Extension modules live under "Расширения/<ext>/" and mirror the base-config
// layout below that prefix. They are keyed as "ext.<ext>.<base-config name>",
// e.g. "Расширения/Доработки3D/CommonModules/WA_ПовтИсп/Ext/Module.bsl" ->
// "ext.Доработки3D.ОбщийМодуль.WA_ПовтИсп.Модуль". This matches the storage key
// the module resolver derives from a normalised user path
// (ext.<extension>.<Normalize(module)>), so code_read/module_code can find
// extension modules in a Hierarchical dump.
func bslPathToModuleName(relPath string) string {
	// Normalise separators.
	relPath = filepath.ToSlash(relPath)
	parts := strings.Split(relPath, "/")

	if len(parts) < 2 {
		return relPath
	}

	// Extension subtree: Расширения/<ext>/<Kind>/<name>/.../<File>.bsl. Strip the
	// two leading segments and run the base-config parser on the remainder so the
	// CommonModules->Модуль special-case and the dumpDirNames/moduleNameSuffixes
	// maps apply exactly as for base config, then prefix with "ext.<ext>.".
	// A path too short to carry a full <Kind>/<name>/<File> remainder
	// (len(parts) < 4) falls through to the base parser unchanged, which keeps
	// the previous behaviour and never panics.
	if parts[0] == extensionDirName && len(parts) >= 4 {
		extName := parts[1]
		return "ext." + extName + "." + baseConfigModuleName(parts[2:])
	}

	return baseConfigModuleName(parts)
}

// baseConfigModuleName maps the path segments of a base-configuration BSL file
// to its human-readable module name (e.g. ["Documents","РеализацияТоваров",
// "Ext","ObjectModule.bsl"] -> "Документ.РеализацияТоваров.МодульОбъекта").
// It is also reused for the per-extension subtree by bslPathToModuleName, which
// passes the segments below "Расширения/<ext>/" and adds the "ext.<ext>." prefix.
//
// parts must have at least two segments; callers guarantee this.
func baseConfigModuleName(parts []string) string {
	// First part is the category directory.
	category := parts[0]
	prefix, ok := dumpDirNames[category]
	if !ok {
		prefix = category
	}

	objectName := parts[1]

	// Determine suffix from the file name.
	fileName := parts[len(parts)-1]
	suffix, ok := moduleNameSuffixes[fileName]
	if !ok {
		suffix = strings.TrimSuffix(fileName, ".bsl")
	}

	// Fix: CommonModules use "Модуль", not "МодульФормы" for Module.bsl.
	if category == "CommonModules" && fileName == "Module.bsl" {
		if !slices.Contains(parts, "Forms") {
			suffix = "Модуль"
		}
	}

	// If the path has a Forms/Commands subdirectory, include the form/command
	// name as an extra segment (e.g. ".Форма.ФормаДок." or ".Команда.Печать.").
	for i, p := range parts {
		if kind, ok := subdirSegmentNames[p]; ok && i+1 < len(parts) {
			childName := parts[i+1]
			return prefix + "." + objectName + "." + kind + "." + childName + "." + suffix
		}
	}

	return prefix + "." + objectName + "." + suffix
}

// SearchMode determines the search strategy.
type SearchMode string

const (
	SearchModeSmart SearchMode = "smart"
	SearchModeRegex SearchMode = "regex"
	SearchModeExact SearchMode = "exact"
)

// SearchParams holds all parameters for a search query.
type SearchParams struct {
	Query    string
	Category string     // filter by metadata type, empty = all
	Module   string     // filter by module type, empty = all
	Mode     SearchMode // default: SearchModeSmart
	Limit    int        // default: 50, max: 500
}

// bslDocument is the struct indexed by Bleve. Field names must match mapping keys.
// Implements mapping.Classifier so Bleve routes it to the "module" document mapping.
type bslDocument struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Module   string `json:"module"`
	Content  string `json:"content"`
}

func (bslDocument) Type() string { return "module" }

// Index provides full-text search over BSL modules using Bleve.
type Index struct {
	dir           string
	alias         bleve.IndexAlias
	shards        []bleve.Index
	names         []string
	contentByName map[string]string   // cache: docID -> content (lazy populated)
	pathByName    map[string]string   // docID -> absolute file path (always populated)
	pathToDocID   map[string]string   // relative path (ToSlash) -> module name
	pathIndex     *PathIndex          // decomposed path index for fast category/module filtering
	lockDir       string              // cache dir whose serve-lock this index holds (empty = none); released in Close
	readOnly      bool                // true when shards were opened read-only (immutable generation serve); runtime base writes are rejected
	readerReg     *readerRegistration // live reader-registry handle for the served generation (nil = none); deregistered in Close
	ready         atomic.Bool
	mu            sync.RWMutex
	contentMu     sync.RWMutex // protects lazy content loading
	buildErr      atomic.Pointer[error]
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
}

// Ready reports whether the index has finished building and is available for search.
func (idx *Index) Ready() bool {
	return idx.ready.Load()
}

// Done returns a channel that is closed when the background index build completes.
// This allows waiting for readiness without polling: <-index.Done()
func (idx *Index) Done() <-chan struct{} {
	return idx.done
}

// GetContent returns the BSL source code for the given module ID.
// Returns empty string and false if the module is not found or index is not ready.
// Content is lazy-loaded from disk on first access and cached for subsequent calls.
func (idx *Index) GetContent(id string) (string, bool) {
	if !idx.ready.Load() {
		return "", false
	}

	// Fast path: check content cache under read lock.
	idx.contentMu.RLock()
	if content, ok := idx.contentByName[id]; ok {
		idx.contentMu.RUnlock()
		return content, true
	}
	idx.contentMu.RUnlock()

	// Check if we have a path for lazy loading.
	idx.mu.RLock()
	path, hasPath := idx.pathByName[id]
	idx.mu.RUnlock()
	if !hasPath {
		return "", false
	}

	// Read (with one retry) WITHOUT holding contentMu, so a transient read
	// failure's retry pause does not block other readers. A concurrent caller may
	// read the same file in parallel; that is harmless (identical content) and is
	// resolved by the double-check below.
	content, ok := readModuleContent(path)
	if !ok {
		return "", false
	}

	idx.contentMu.Lock()
	defer idx.contentMu.Unlock()
	// Double-check: another goroutine may have populated the cache meanwhile.
	if existing, ok := idx.contentByName[id]; ok {
		return existing, true
	}
	idx.contentByName[id] = content
	return content, true
}

// loadedModule holds the result of reading a single .bsl file.
type loadedModule struct {
	name    string
	relPath string // forward-slash normalized relative path
	content string
}

// NewIndex creates a new Index for the given dump directory. The index is built
// asynchronously in a background goroutine and becomes available when Ready()
// returns true. If reindex is true, any existing cache is discarded and rebuilt.
func NewIndex(dir, cacheDir string, reindex bool) (*Index, error) {
	ctx, cancel := context.WithCancel(context.Background())
	idx := &Index{
		dir:           dir,
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]string),
		pathByName:    make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	cpath, cacheErr := cachePath(dir, cacheDir)
	useCache := cacheErr == nil

	if !useCache {
		// No writable cache location: os.UserCacheDir() failed and no
		// MCP_1C_CACHE_DIR override is set. Shards are then built in memory and
		// NOT persisted, so every start pays the full cold build (slow and
		// memory-intensive). Make this visible instead of failing silently.
		slog.Warn("Dump index cache disabled: no writable cache directory "+
			"(os.UserCacheDir failed and MCP_1C_CACHE_DIR is unset/unwritable). "+
			"The full-text index will be rebuilt in memory on every start. Set "+
			"MCP_1C_CACHE_DIR to a writable, persistent directory to enable the "+
			"on-disk cache.", "error", cacheErr)
		if showProgress.Load() {
			fmt.Fprintf(os.Stderr, "Внимание: кэш индекса отключён — нет доступной для "+
				"записи кэш-директории. Индекс будет строиться в памяти при каждом "+
				"запуске. Задайте MCP_1C_CACHE_DIR на постоянный каталог с правом записи.\n")
		}
	}

	if useCache && reindex {
		// Generation-aware reindex. The old behavior os.RemoveAll(cpath)'d the WHOLE
		// per-dump cache dir — including g/ and any immutable generation a concurrent
		// read-only serve holds (an unlink storm on unix, a hard failure on Windows
		// mmap'd files, and corruption of the holder's view). Instead, build a fresh
		// immutable generation (temp→READY→adopt) which by construction never touches
		// a live generation's files, then serve it read-only. The heavy build runs in
		// the background (Ready() flips when done) so `serve --reindex` start never
		// blocks the MCP initialize handshake.
		go func() {
			defer close(idx.done)
			if err := idx.reindexGeneration(dir, cacheDir); err != nil {
				idx.setBuildErr(err)
			}
		}()
		return idx, nil
	}

	// Try to open existing sharded cache.
	if useCache && !reindex {
		if shardDirs := cacheShardDirs(cpath); len(shardDirs) > 0 {
			// Legacy flat layout stays read-WRITE: this path runs the incremental
			// warm-start diff (loadFromManifestAndDiff) which mutates the base
			// shards on drift. Concurrent same-dump serve uses the read-only
			// immutable-generation path (OpenGenerationReadOnly) instead.
			shards, err := openCachedShards(shardDirs, false, "")
			if err == nil {
				idx.shards = shards
				idx.alias.Add(shards...)
				idx.acquireCacheLock(cpath)

				// Fast startup: populate index from manifest, then apply incremental diff.
				go func() {
					defer close(idx.done)
					if err := idx.loadFromManifestAndDiff(cpath); err != nil {
						// Fallback: walk filesystem if manifest-based load fails.
						slog.Warn("Manifest load failed, falling back to walk", "error", err)
						if err := idx.loadBSLPaths(dir); err != nil {
							idx.setBuildErr(err)
							return
						}
					}

					idx.pathIndex = NewPathIndex(idx.names)
					idx.ready.Store(true)
					slog.Info("Opened cached index",
						"shards", len(shards), "modules", len(idx.names))
					if showProgress.Load() {
						fmt.Fprintf(os.Stderr, "[%s] Индекс загружен из кэша: %d модулей\n",
							time.Now().Format("15:04:05"), len(idx.names))
					}
				}()
				return idx, nil
			}
			// Cache corrupt — remove and rebuild.
			if err := os.RemoveAll(cpath); err != nil {
				slog.Warn("could not remove corrupt index cache before rebuild; "+
					"the rebuild may fail or mix stale shards",
					"path", cpath, "error", err)
			}
		}
	}

	// No usable cache — full sharded build in background.
	if useCache {
		idx.acquireCacheLock(cpath)
	}
	go func() {
		defer close(idx.done)
		idx.buildShards(cpath, useCache)
	}()

	return idx, nil
}

// reindexGeneration performs a generation-aware reindex: it builds a fresh
// immutable generation for the current dump signature and attaches it to idx
// read-only. It NEVER wipes a generation a live reader holds — that is the core
// safety property. Old generations are left on disk and reaped by the GC pass at
// the end. Runs in NewIndex's background goroutine; the caller closes idx.done.
func (idx *Index) reindexGeneration(dir, cacheDir string) error {
	gensig, err := GenSig(dir)
	if err != nil {
		// Cannot compute a generation signature (e.g. the dump dir is unreadable).
		// Fall back to a legacy flat rebuild that still preserves g/ — never wipe the
		// generations subtree, which a concurrent reader may hold.
		slog.Warn("reindex: could not compute generation signature; falling back to a "+
			"flat rebuild (generations preserved)", "dir", dir, "error", err)
		if cpath, cerr := cachePath(dir, cacheDir); cerr == nil {
			removeFlatCacheContents(cpath)
			idx.acquireCacheLock(cpath)
			idx.buildShards(cpath, true)
		} else {
			idx.buildShards("", false) // no writable cache: in-memory build
		}
		return idx.BuildError()
	}

	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return fmt.Errorf("reindex: no writable cache directory: %w", err)
	}
	genDir := generationDir(cpath, gensig)

	// Force-rebuild semantics: BuildGeneration is content-addressed and no-ops on an
	// already-READY gensig, so to honor --reindex (e.g. recovering a corrupt cache)
	// drop the current generation first — but ONLY if no live reader holds it.
	// Never wipe a generation a concurrent serve has memory-mapped.
	if generationHasLiveReader(genDir) {
		slog.Warn("reindex: a live reader holds the current generation; serving the "+
			"existing generation WITHOUT an in-place rebuild (stop other servers on this "+
			"dump to force a full rebuild)", "gen", gensig)
	} else if err := os.RemoveAll(genDir); err != nil {
		slog.Warn("reindex: could not drop the current generation before rebuild; "+
			"BuildGeneration will reuse it if still adoptable", "gen", gensig, "error", err)
	}

	if err := BuildGeneration(dir, cacheDir, gensig); err != nil {
		return fmt.Errorf("reindex: building generation: %w", err)
	}

	if err := idx.attachReadOnlyShards(genDir); err != nil {
		return err
	}
	if err := idx.loadNamesReadOnly(genDir); err != nil {
		return err
	}
	idx.pathIndex = NewPathIndex(idx.names)
	idx.ready.Store(true)

	// Now that a fresh generation is current, reap old, unheld generations.
	if dropped, gcErr := GCGenerations(dir, cacheDir, gensig); gcErr != nil {
		slog.Warn("reindex: GC of old generations failed", "error", gcErr)
	} else if len(dropped) > 0 {
		slog.Info("reindex: GC removed old generations", "count", len(dropped))
	}

	slog.Info("Reindex built and adopted a fresh generation",
		"gen", gensig, "modules", len(idx.names))
	if showProgress.Load() {
		fmt.Fprintf(os.Stderr, "[%s] Переиндексация завершена: %d модулей (поколение %s)\n",
			time.Now().Format("15:04:05"), len(idx.names), gensig)
	}
	return nil
}

// acquireCacheLock marks cpath as in use by this process for the lifetime of the
// index (released in Close), so a concurrent `--build-index` does not clobber a
// cache the running server has memory-mapped. Best-effort: a failure to write the
// lock is logged but does not stop the server.
func (idx *Index) acquireCacheLock(cpath string) {
	if cpath == "" {
		return
	}
	if err := writeCacheLock(cpath); err != nil {
		slog.Warn("dump: could not write cache lock; a concurrent --build-index "+
			"could clobber this cache", "path", cpath, "error", err)
		return
	}
	idx.lockDir = cpath
}

// BuildCache synchronously builds (or refreshes) the on-disk search-index cache
// for dir and returns once the build has completed and been persisted. It is the
// offline pre-warm entry point behind the --build-index CLI flag: running it
// before `serve` lets the server open a warm cache instead of paying the
// expensive in-memory cold build (high transient RSS) on first start.
//
// cacheDir follows NewIndex semantics: empty selects the platform cache dir
// (os.UserCacheDir), otherwise the given directory is used. reindex forces a full
// rebuild. BuildCache returns an error if no writable cache location is available
// (nothing would be persisted, so every serve would rebuild) or if the build
// fails.
func BuildCache(dir, cacheDir string, reindex bool) error {
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return fmt.Errorf("no writable cache directory (set MCP_1C_CACHE_DIR to a writable path): %w", err)
	}

	// Refuse to rebuild a cache that a running server (or another build) has open
	// and memory-mapped: a destructive rebuild would corrupt that process's view
	// and/or race its writes. The lock is written by NewIndex while a cache is
	// open and removed on Close.
	if pid, present := readCacheLock(cpath); present {
		who := "another mcp-1c process"
		if pid > 0 {
			who = fmt.Sprintf("mcp-1c (pid %d)", pid)
		}
		return fmt.Errorf("index cache %s is in use by %s; stop the running server "+
			"before using --build-index. If no server is running this is a stale lock — "+
			"delete %s and retry", cpath, who, filepath.Join(cpath, serveLockName))
	}

	idx, err := NewIndex(dir, cacheDir, reindex)
	if err != nil {
		return err
	}
	defer idx.Close()

	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		return fmt.Errorf("building index cache: %w", err)
	}
	if !idx.Ready() {
		return fmt.Errorf("index build did not complete")
	}
	return nil
}

// setBuildErr stores a build error atomically.
func (idx *Index) setBuildErr(err error) {
	idx.buildErr.Store(&err)
}

// buildShards loads BSL files and builds N shards in parallel.
func (idx *Index) buildShards(cpath string, useCache bool) {
	// Load module paths only (no file content). The shard builders below read
	// each .bsl from disk on demand, so the full corpus (hundreds of MB) is never
	// resident at once — the dominant cold-build memory peak. Sort names into the
	// same lexicographic order loadBSLFiles produced, which reproducible shard
	// keys and stable regex/exact scan order depend on.
	if err := idx.loadBSLPaths(idx.dir); err != nil {
		idx.setBuildErr(fmt.Errorf("loading BSL paths: %w", err))
		return
	}
	slices.Sort(idx.names)

	total := len(idx.names)
	if total == 0 {
		idx.pathIndex = NewPathIndex(nil)
		idx.ready.Store(true)
		slog.Info("No BSL modules found, index is empty")
		if showProgress.Load() {
			fmt.Fprintf(os.Stderr, "Внимание: в директории %s не найдено .bsl файлов\n", idx.dir)
		}
		return
	}

	n := shardCount(total)
	groups := splitByHash(idx.names, n)
	slog.Info("Building index", "modules", total, "shards", n)
	if showProgress.Load() {
		fmt.Fprintf(os.Stderr, "[%s] Индексация: найдено %d модулей...\n",
			time.Now().Format("15:04:05"), total)
	}

	var basePath string
	if cpath != "" && useCache {
		os.MkdirAll(cpath, 0o755)
		basePath = cpath
	}

	// Increase persister nap time to favour in-memory segment merging.
	// Set once before shard goroutines start. Not restored because the persister
	// goroutines inside each shard continue reading this global after buildShards
	// returns — restoring it would race with those reads.
	scorchIndex.DefaultPersisterNapTimeMSec = 500

	// Build the BSL mapping once and share across all shards.
	bslMapping := buildBSLMapping()

	// Content resolver: shard builders read each module's source from disk on
	// demand (bounded memory) instead of from a fully-resident content map.
	// pathByName is only read during the build, so concurrent access from the
	// shard goroutines needs no lock.
	pathByName := idx.pathByName
	getContent := func(name string) string {
		path := pathByName[name]
		if path == "" {
			return ""
		}
		// readModuleContent retries once and logs a rate-limited warning on
		// failure, so a file unreadable at build time (lock / antivirus /
		// cloud-sync) is not silently indexed with empty content.
		content, _ := readModuleContent(path)
		return content
	}

	// Tighten GC for the parallel build instead of disabling it. Previously the
	// whole build ran with GC OFF (debug.SetGCPercent(-1)), so tokenization and
	// analysis transients accumulated across every shard and inflated peak RSS
	// many-fold on a cold build — the serve-time OOM. A lower-than-default GC
	// target keeps heap headroom small during this allocation-heavy phase and is
	// restored afterwards. It is relative (no fixed byte budget), so it adapts to
	// any config size. With GC enabled, a process memory limit set via
	// debug.SetMemoryLimit (e.g. the Advanced --memory-limit flag) is now also
	// honoured — it was a no-op while GC was disabled.
	oldGC := debug.SetGCPercent(buildGCPercent)
	defer debug.SetGCPercent(oldGC)

	start := time.Now()
	var indexed atomic.Int64

	type shardResult struct {
		index bleve.Index
		id    int
		err   error
	}
	results := make(chan shardResult, n)

	// Progress ticker: only active for interactive terminal launches. Writing to
	// stderr in pipe/MCP mode can trigger restart loops in strict MCP clients.
	stopProgress := make(chan struct{})
	tickerActive := showProgress.Load()
	if tickerActive {
		ticker := time.NewTicker(500 * time.Millisecond)
		go func() {
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					done := indexed.Load()
					pct := done * 100 / int64(total)
					fmt.Fprintf(os.Stderr, "\rИндексация: %d/%d (%d%%)   ", done, total, pct)
				case <-stopProgress:
					fmt.Fprintf(os.Stderr, "\r%80s\r", "")
					return
				}
			}
		}()
	}

	for i := range n {
		go func(shardID int) {
			select {
			case <-idx.ctx.Done():
				results <- shardResult{id: shardID, err: idx.ctx.Err()}
				return
			default:
			}

			var shardPath string
			if basePath != "" {
				shardPath = filepath.Join(basePath, fmt.Sprintf("shard_%d", shardID))
			}

			shard, err := buildShard(shardPath, groups[shardID], getContent, shardID, n, bslMapping, &indexed)
			results <- shardResult{index: shard, id: shardID, err: err}
		}(i)
	}

	// Collect. Always receive all n to avoid goroutine leak.
	shards := make([]bleve.Index, n)
	var firstErr error
	for range n {
		res := <-results
		if res.err != nil && firstErr == nil {
			firstErr = res.err
			idx.cancel()
		}
		if res.index != nil {
			shards[res.id] = res.index
		}
	}
	if tickerActive {
		close(stopProgress)
	}
	if firstErr != nil {
		for _, s := range shards {
			if s != nil {
				s.Close()
			}
		}
		if cpath != "" {
			if err := os.RemoveAll(cpath); err != nil {
				slog.Warn("could not remove partial index cache after build failure",
					"path", cpath, "error", err)
			}
		}
		idx.setBuildErr(firstErr)
		return
	}

	idx.shards = shards
	idx.alias.Add(shards...)
	idx.pathIndex = NewPathIndex(idx.names)

	// contentByName is intentionally left empty: the cold build streamed content
	// from disk (see getContent above) and never populated it. GetContent lazily
	// loads individual modules from disk via pathByName, and regex/exact scans
	// stream content (see searchLineByLine/contentForScan), so the full corpus is
	// never resident after the build either.

	idx.ready.Store(true)

	// Save manifest for future incremental updates.
	if cpath != "" && useCache {
		idx.saveManifest(cpath)
	}

	slog.Info("Index ready", "modules", total, "shards", n, "elapsed", time.Since(start))
	if showProgress.Load() {
		fmt.Fprintf(os.Stderr, "Индексация завершена за %.1fс: %d модулей готово к поиску\n",
			time.Since(start).Seconds(), total)
	}
}

// openCachedShards opens pre-built Bleve shard indexes from disk.
// On any error, all previously opened shards are closed.
//
// When readOnly is true the shards are opened with scorch's read_only mode
// (bleve.OpenUsing(..., {"read_only": true})), which takes a bbolt LOCK_SH on
// each shard's root.bolt instead of the default exclusive LOCK_EX. That lets N
// processes open the SAME generation concurrently — the core of concurrent
// same-dump serve. boltTimeout bounds how long a conflicting open waits for the
// flock before failing; it MUST be a Go duration STRING (e.g. "5s"): scorch
// reads bolt_timeout via config["bolt_timeout"].(string)+time.ParseDuration, so
// a wrong type (int / time.Duration) is silently dropped and the open reverts to
// the wait-forever default (Timeout=0) — the original infinite hang. An empty
// boltTimeout leaves scorch's default (wait indefinitely); pass a non-empty
// value whenever a conflicting holder is possible.
func openCachedShards(dirs []string, readOnly bool, boltTimeout string) ([]bleve.Index, error) {
	shards := make([]bleve.Index, len(dirs))
	for i, dir := range dirs {
		var (
			blevIdx bleve.Index
			err     error
		)
		if readOnly {
			cfg := map[string]any{"read_only": true}
			if boltTimeout != "" {
				cfg["bolt_timeout"] = boltTimeout // MUST be a duration STRING — see doc above
			}
			blevIdx, err = bleve.OpenUsing(dir, cfg)
		} else {
			blevIdx, err = bleve.Open(dir)
		}
		if err != nil {
			for j := range i {
				shards[j].Close()
			}
			return nil, fmt.Errorf("opening shard %d: %w", i, err)
		}
		shards[i] = blevIdx
	}
	return shards, nil
}

// buildIndexBuilder creates a Bleve index using the offline builder (bleve.NewBuilder).
// This approach bypasses Scorch persister/merger goroutines and is faster for bulk loading.
// The builder writes segments to disk, merges them, and produces a ready-to-open index.
// After builder.Close(), the index is opened with bleve.Open().
// Requires a non-empty indexPath (cannot work in-memory).
func buildIndexBuilder(indexPath string, names []string, contentByName map[string]string) (bleve.Index, error) {
	bslMapping := buildBSLMapping()

	builder, err := bleve.NewBuilder(indexPath, bslMapping, map[string]any{
		"forceSegmentType":    "zap",
		"forceSegmentVersion": zapSegmentVersion, // folded into GenSig — see BUMP PROTOCOL
		"batchSize":           5000,
	})
	if err != nil {
		return nil, fmt.Errorf("creating bleve builder: %w", err)
	}

	total := len(names)

	for _, name := range names {
		parts := parseModuleName(name)

		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  contentByName[name],
		}

		if err := builder.Index(name, doc); err != nil {
			builder.Close()
			return nil, fmt.Errorf("builder indexing doc %q: %w", name, err)
		}
	}
	if total > 0 {
		slog.Info("Indexing BSL modules done", "count", total)
	}

	if err := builder.Close(); err != nil {
		return nil, fmt.Errorf("closing bleve builder: %w", err)
	}

	blevIdx, err := bleve.Open(indexPath)
	if err != nil {
		return nil, fmt.Errorf("opening built index: %w", err)
	}

	return blevIdx, nil
}

// buildIndexBatch creates a Bleve index using NewUsing + manual batch operations.
// This is the fallback for in-memory builds where NewBuilder cannot be used.
func buildIndexBatch(indexPath string, names []string, contentByName map[string]string) (bleve.Index, error) {
	// Increase persister nap time to favour in-memory segment merging with unsafe_batch.
	oldNap := scorchIndex.DefaultPersisterNapTimeMSec
	scorchIndex.DefaultPersisterNapTimeMSec = 500
	defer func() { scorchIndex.DefaultPersisterNapTimeMSec = oldNap }()

	bslMapping := buildBSLMapping()

	blevIdx, err := bleve.NewUsing(indexPath, bslMapping, "scorch", "scorch", map[string]any{
		"unsafe_batch": true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating bleve index: %w", err)
	}

	total := len(names)
	const batchSize = 5000

	batch := blevIdx.NewBatch()
	for i, name := range names {
		parts := parseModuleName(name)

		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  contentByName[name],
		}

		batch.Index(name, doc)

		if (i+1)%batchSize == 0 || i+1 == total {
			if err := blevIdx.Batch(batch); err != nil {
				blevIdx.Close()
				return nil, fmt.Errorf("indexing batch: %w", err)
			}
			batch = blevIdx.NewBatch()
		}

	}
	if total > 0 {
		slog.Info("Indexing BSL modules done", "count", total)
	}

	return blevIdx, nil
}

// loadBSLFiles walks the dump directory and reads all .bsl files in parallel,
// populating idx.names and idx.contentByName.
func (idx *Index) loadBSLFiles(dir string) error {
	// Phase 1: collect all .bsl file paths (fast directory walk, no file I/O).
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".bsl") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking dump directory: %w", err)
	}

	// Phase 2: read files in parallel using a worker pool.
	results := make(chan loadedModule, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())

	for _, p := range paths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := os.ReadFile(path)
			if err != nil {
				return // skip unreadable files
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return
			}
			relSlash := filepath.ToSlash(rel)
			name := bslPathToModuleName(rel)
			results <- loadedModule{name: name, relPath: relSlash, content: stripBOM(string(data))}
		}(p)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results from workers.
	if idx.pathToDocID == nil {
		idx.pathToDocID = make(map[string]string, len(paths))
	}
	for m := range results {
		idx.names = append(idx.names, m.name)
		idx.contentByName[m.name] = m.content
		idx.pathToDocID[m.relPath] = m.name
		// Also store absolute path for lazy-load compatibility.
		absPath := filepath.Join(dir, filepath.FromSlash(m.relPath))
		idx.pathByName[m.name] = absPath
	}

	// Workers finish in nondeterministic timing order, so the resulting
	// idx.names slice depends on goroutine scheduling. Sort lexicographically
	// to guarantee a stable enumeration order across runs on the same dump,
	// which is required for reproducible cache keys, chunking, vocabulary
	// and TF-IDF downstream. Maps (contentByName, pathToDocID, pathByName)
	// are unaffected because they are keyed by name/relPath.
	slices.Sort(idx.names)

	return nil
}

// loadBSLPaths walks the dump directory and collects file paths without reading content.
// Populates idx.names, idx.pathByName, and idx.pathToDocID.
// This is the fast startup path (~0.5s) used when cached shards exist.
func (idx *Index) loadBSLPaths(dir string) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".bsl") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		name := bslPathToModuleName(rel)
		absPath, err := filepath.Abs(path)
		if err != nil {
			absPath = path
		}
		idx.names = append(idx.names, name)
		idx.pathByName[name] = absPath
		if idx.pathToDocID == nil {
			idx.pathToDocID = make(map[string]string)
		}
		idx.pathToDocID[relSlash] = name
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking dump directory: %w", err)
	}
	return nil
}

// contentForScan returns the BSL source for name for a full-index scan. It uses
// the in-memory content cache when the module is present there, otherwise it
// reads the file from disk via pathByName WITHOUT caching the result.
//
// Regex/exact search scans every candidate module, so caching here would re-grow
// contentByName to the full corpus size — the very allocation the cold-build fix
// drops after building (see buildShards). Streaming instead keeps a full scan's
// memory bounded to one file at a time, reclaimed by the GC. The returned content
// is identical to the cached value (same stripBOM), so search results are
// unchanged.
func (idx *Index) contentForScan(name string) (string, bool) {
	idx.contentMu.RLock()
	c, ok := idx.contentByName[name]
	idx.contentMu.RUnlock()
	if ok {
		return c, true
	}

	idx.mu.RLock()
	path, hasPath := idx.pathByName[name]
	idx.mu.RUnlock()
	if !hasPath {
		return "", false
	}

	return readModuleContent(path)
}

// moduleNameParts holds the parsed components of a human-readable module name.
type moduleNameParts struct {
	category string // e.g. "Справочник"
	name     string // e.g. "Номенклатура"
	module   string // e.g. "МодульОбъекта"
}

// parseModuleName splits "Справочник.Номенклатура.МодульОбъекта" into parts.
// For form paths like "Документ.Док.Форма.ФормаДок.МодульФормы", the module type
// is the last dot-separated segment ("МодульФормы"), not the third segment.
func parseModuleName(fullName string) moduleNameParts {
	parts := strings.Split(fullName, ".")
	switch {
	case len(parts) >= 3:
		return moduleNameParts{
			category: parts[0],
			name:     parts[1],
			module:   parts[len(parts)-1],
		}
	case len(parts) == 2:
		return moduleNameParts{category: parts[0], name: parts[1]}
	default:
		return moduleNameParts{name: fullName}
	}
}

// IndexDoc adds or replaces a document in the index at runtime.
// The document is routed to a shard by FNV-1a hash of the id.
// It updates contentByName and names (with dedup), so ModuleCount and all
// search modes (regex, exact, smart) reflect the new document immediately.
// Requires Ready() == true.
func (idx *Index) IndexDoc(id string, content string) error {
	if !idx.ready.Load() {
		return fmt.Errorf("index not ready: cannot IndexDoc while building")
	}
	if idx.readOnly {
		return fmt.Errorf("index opened read-only: cannot IndexDoc (extension overlay not yet available)")
	}
	if len(idx.shards) == 0 {
		return fmt.Errorf("index has no shards")
	}

	parts := parseModuleName(id)
	doc := bslDocument{
		Name:     parts.name,
		Category: parts.category,
		Module:   parts.module,
		Content:  content,
	}

	si := shardForID(id, len(idx.shards))
	if err := idx.shards[si].Index(id, doc); err != nil {
		return fmt.Errorf("indexing doc %q in shard %d: %w", id, si, err)
	}

	// Check existence under both locks to decide whether this is a new doc.
	idx.contentMu.RLock()
	_, inContent := idx.contentByName[id]
	idx.contentMu.RUnlock()

	idx.mu.Lock()
	_, inPath := idx.pathByName[id]
	if !inContent && !inPath {
		idx.names = append(idx.names, id)
		if idx.pathIndex != nil {
			idx.pathIndex.AddEntry(id)
		}
	}
	idx.mu.Unlock()

	idx.contentMu.Lock()
	idx.contentByName[id] = content
	idx.contentMu.Unlock()

	return nil
}

// IndexDocWithMeta adds or replaces a document in the index with explicit metadata.
// Unlike IndexDoc, it does NOT call parseModuleName — category and module are set directly.
// The document is routed to a shard by FNV-1a hash of the id.
// Requires Ready() == true.
func (idx *Index) IndexDocWithMeta(id, content, category, module string) error {
	if !idx.ready.Load() {
		return fmt.Errorf("index not ready: cannot IndexDocWithMeta while building")
	}
	if idx.readOnly {
		// Base shards are immutable (read-only generation serve). Runtime
		// ingest (e.g. live extensions) belongs in a per-process in-memory
		// overlay; that overlay is a later chunk. Reject rather than write to a
		// read-only shard.
		return fmt.Errorf("index opened read-only: cannot IndexDocWithMeta (extension overlay not yet available)")
	}
	if len(idx.shards) == 0 {
		return fmt.Errorf("index has no shards")
	}

	doc := bslDocument{
		Name:     id,
		Category: category,
		Module:   module,
		Content:  content,
	}

	si := shardForID(id, len(idx.shards))
	if err := idx.shards[si].Index(id, doc); err != nil {
		return fmt.Errorf("indexing doc %q in shard %d: %w", id, si, err)
	}

	// Check existence under both locks to decide whether this is a new doc.
	idx.contentMu.RLock()
	_, inContent := idx.contentByName[id]
	idx.contentMu.RUnlock()

	idx.mu.Lock()
	_, inPath := idx.pathByName[id]
	if !inContent && !inPath {
		idx.names = append(idx.names, id)
		if idx.pathIndex != nil {
			idx.pathIndex.AddEntryWithMeta(id, category, module)
		}
	}
	idx.mu.Unlock()

	idx.contentMu.Lock()
	idx.contentByName[id] = content
	idx.contentMu.Unlock()

	return nil
}

// DeleteDoc removes a document from the index at runtime.
// The shard is determined by FNV-1a hash of the id (same routing as IndexDoc).
// It removes from both contentByName and names, so ModuleCount and all search
// modes (regex, exact, smart) no longer see the deleted document.
// Requires Ready() == true.
func (idx *Index) DeleteDoc(id string) error {
	if !idx.ready.Load() {
		return fmt.Errorf("index not ready: cannot DeleteDoc while building")
	}
	if idx.readOnly {
		return fmt.Errorf("index opened read-only: cannot DeleteDoc (extension overlay not yet available)")
	}
	if len(idx.shards) == 0 {
		return fmt.Errorf("index has no shards")
	}

	si := shardForID(id, len(idx.shards))
	if err := idx.shards[si].Delete(id); err != nil {
		return fmt.Errorf("deleting doc %q from shard %d: %w", id, si, err)
	}

	idx.contentMu.Lock()
	delete(idx.contentByName, id)
	idx.contentMu.Unlock()

	idx.mu.Lock()
	delete(idx.pathByName, id)
	if idx.pathIndex != nil {
		idx.pathIndex.RemoveEntry(id)
	}
	for i, n := range idx.names {
		if n == id {
			idx.names = append(idx.names[:i], idx.names[i+1:]...)
			break
		}
	}
	idx.mu.Unlock()

	return nil
}

// Search finds matches in indexed BSL modules. Dispatches by mode.
func (idx *Index) Search(params SearchParams) ([]Match, int, error) {
	if !idx.ready.Load() {
		if errPtr := idx.buildErr.Load(); errPtr != nil {
			return nil, 0, fmt.Errorf("index build failed: %w", *errPtr)
		}
		return nil, 0, fmt.Errorf("search index is building, please retry")
	}

	if params.Mode == "" {
		params.Mode = SearchModeSmart
	}
	if params.Limit <= 0 {
		params.Limit = 50
	}
	if params.Limit > 500 {
		params.Limit = 500
	}

	switch params.Mode {
	case SearchModeSmart:
		return idx.searchSmart(params)
	case SearchModeRegex:
		re, err := regexp.Compile(params.Query)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid regex %q: %w", params.Query, err)
		}
		return idx.searchLineByLine(params, func(line, _ string) bool {
			return re.MatchString(line)
		}, params.Query, false)
	case SearchModeExact:
		lower := strings.ToLower(params.Query)
		return idx.searchLineByLine(params, func(line, _ string) bool {
			return strings.Contains(line, lower)
		}, lower, true)
	default:
		return nil, 0, fmt.Errorf("unknown search mode: %q", params.Mode)
	}
}

// searchSmart performs full-text BM25 search via Bleve.
func (idx *Index) searchSmart(params SearchParams) ([]Match, int, error) {
	mq := bleve.NewMatchQuery(params.Query)
	mq.SetField("content")
	mq.Analyzer = analyzerBSL

	var q query.Query = mq

	// Apply category/module filters as conjunction.
	if params.Category != "" || params.Module != "" {
		queries := []query.Query{mq}
		if params.Category != "" {
			tq := bleve.NewTermQuery(params.Category)
			tq.SetField("category")
			queries = append(queries, tq)
		}
		if params.Module != "" {
			tq := bleve.NewTermQuery(params.Module)
			tq.SetField("module")
			queries = append(queries, tq)
		}
		q = bleve.NewConjunctionQuery(queries...)
	}

	req := bleve.NewSearchRequestOptions(q, params.Limit, 0, false)
	result, err := idx.alias.Search(req)
	if err != nil {
		return nil, 0, fmt.Errorf("bleve search: %w", err)
	}

	lower := strings.ToLower(params.Query)
	tokens := strings.Fields(lower)

	// Pre-build synonym-expanded token set for fallback when Bleve matched
	// via synonym expansion but original tokens do not appear in the source.
	synonymMapOnce.Do(func() { cachedSynonymMap = buildSynonymMap() })
	synMap := cachedSynonymMap
	expandedTokens := make([]string, 0, len(tokens)*2)
	for _, tok := range tokens {
		expandedTokens = append(expandedTokens, tok)
		if syn, ok := synMap[tok]; ok {
			expandedTokens = append(expandedTokens, syn)
		}
	}

	var matches []Match
	for _, hit := range result.Hits {
		content, ok := idx.GetContent(hit.ID)
		if !ok {
			continue
		}
		lines := strings.Split(content, "\n")

		// Score each line by counting how many distinct query tokens it contains.
		// Pick the line with the highest score; on ties, prefer the first occurrence.
		lineNum := 0
		bestScore := 0
		for i, line := range lines {
			ll := strings.ToLower(line)
			score := 0
			for _, tok := range tokens {
				if strings.Contains(ll, tok) {
					score++
				}
			}
			if score > bestScore {
				bestScore = score
				lineNum = i + 1
			}
		}

		// Synonym fallback: if no original token matched any line, try expanded tokens.
		if lineNum == 0 && len(expandedTokens) > len(tokens) {
			for i, line := range lines {
				ll := strings.ToLower(line)
				for _, tok := range expandedTokens {
					if strings.Contains(ll, tok) {
						lineNum = i + 1
						break
					}
				}
				if lineNum > 0 {
					break
				}
			}
		}

		if lineNum == 0 {
			lineNum = 1
		}

		ctx := extractContext(lines, lineNum-1, 2)
		matches = append(matches, Match{
			Module:  hit.ID,
			Line:    lineNum,
			Context: ctx,
			Score:   hit.Score,
		})
	}

	return matches, int(result.Total), nil
}

// searchLineByLine performs line-by-line search using a matcher function.
// Used for regex and exact modes. Optionally pre-filters modules via Bleve.
// When preLower is true, each line is pre-lowered once and the lowered version
// is passed to the match function (avoids redundant ToLower per line).
func (idx *Index) searchLineByLine(params SearchParams, match func(line, q string) bool, q string, preLower bool) ([]Match, int, error) {
	candidates, err := idx.filterModules(params.Category, params.Module)
	if err != nil {
		return nil, 0, err
	}

	var matches []Match
	total := 0

	// Scan candidates in bounded parallel chunks. Each candidate's content is
	// streamed from disk (or taken from cache if present) and discarded after
	// scanning — nothing is permanently cached, so a full regex/exact scan stays
	// memory-bounded (the cold-build fix removed the all-content map). Chunk
	// results are merged in candidate order, so the output — match order, total
	// count, and first-Limit cap — is byte-identical to a sequential scan.
	type candResult struct {
		matches []Match
		count   int
	}

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	chunkSize := workers * 8

	for start := 0; start < len(candidates); start += chunkSize {
		end := min(start+chunkSize, len(candidates))
		chunk := candidates[start:end]
		results := make([]candResult, len(chunk))

		var wg sync.WaitGroup
		sem := make(chan struct{}, workers)
		for i, name := range chunk {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, name string) {
				defer wg.Done()
				defer func() { <-sem }()

				content, ok := idx.contentForScan(name)
				if !ok {
					return
				}
				lines := strings.Split(content, "\n")
				var ms []Match
				cnt := 0
				for li, line := range lines {
					matchLine := line
					if preLower {
						matchLine = strings.ToLower(line)
					}
					if match(matchLine, q) {
						cnt++
						// Buffer at most Limit matches per candidate; the global
						// cap is applied during the ordered merge below.
						if len(ms) < params.Limit {
							ms = append(ms, Match{
								Module:  name,
								Line:    li + 1,
								Context: extractContext(lines, li, 2),
							})
						}
					}
				}
				results[i] = candResult{matches: ms, count: cnt}
			}(i, name)
		}
		wg.Wait()

		for _, r := range results {
			total += r.count
			for _, m := range r.matches {
				if len(matches) < params.Limit {
					matches = append(matches, m)
				}
			}
		}
	}

	return matches, total, nil
}

// filterModules returns the subset of module names matching category/module filters.
// If no filters are set, returns a copy of all names. Uses PathIndex for fast in-memory filtering.
// The returned slice is always a fresh copy safe for concurrent use.
func (idx *Index) filterModules(category, moduleType string) ([]string, error) {
	if category == "" && moduleType == "" {
		idx.mu.RLock()
		result := slices.Clone(idx.names)
		idx.mu.RUnlock()
		return result, nil
	}

	// Use PathIndex for fast in-memory filtering (no Bleve query needed).
	if idx.pathIndex != nil {
		idx.mu.RLock()
		result := idx.pathIndex.FilterDocIDs(category, moduleType)
		idx.mu.RUnlock()
		return result, nil
	}

	// Fallback: linear scan if pathIndex is not yet built (should not happen
	// since filterModules is only called after Ready() == true).
	idx.mu.RLock()
	allNames := slices.Clone(idx.names)
	idx.mu.RUnlock()

	var names []string
	for _, name := range allNames {
		parts := parseModuleName(name)
		if category != "" && parts.category != category {
			continue
		}
		if moduleType != "" && parts.module != moduleType {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// loadFromManifestAndDiff populates the index from a cached manifest and applies
// incremental updates using a single filesystem walk (via Diff). This is the fastest
// startup path: manifest provides names/paths, Diff detects changes.
// Returns an error if no manifest exists or if Diff fails.
func (idx *Index) loadFromManifestAndDiff(cacheDir string) error {
	manifest, err := LoadManifest(cacheDir)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}
	if manifest == nil {
		// No manifest — need full walk to create one.
		if err := idx.loadBSLPaths(idx.dir); err != nil {
			return err
		}
		idx.saveManifest(cacheDir)
		return nil
	}

	// Populate names, pathByName, pathToDocID from manifest (no filesystem I/O).
	// Go map iteration order is randomised, so idx.names must be sorted after
	// the loop to preserve the same lexicographic enumeration invariant as
	// loadBSLFiles. Maps (pathByName, pathToDocID) are unaffected.
	idx.mu.Lock()
	idx.pathToDocID = make(map[string]string, len(manifest.Files))
	for relPath, entry := range manifest.Files {
		absPath := filepath.Join(idx.dir, filepath.FromSlash(relPath))
		idx.names = append(idx.names, entry.DocID)
		idx.pathByName[entry.DocID] = absPath
		idx.pathToDocID[relPath] = entry.DocID
	}
	slices.Sort(idx.names)
	idx.mu.Unlock()

	// Diff walks the filesystem once to detect changes.
	diff, err := manifest.Diff(idx.dir)
	if err != nil {
		return fmt.Errorf("computing diff: %w", err)
	}

	if diff.Empty() {
		return nil
	}

	// Apply deletions.
	for _, relPath := range diff.Deleted {
		entry, ok := manifest.Files[relPath]
		if !ok {
			continue
		}
		docID := entry.DocID
		si := shardForID(docID, len(idx.shards))
		if err := idx.shards[si].Delete(docID); err != nil {
			slog.Warn("Failed to delete from shard", "docID", docID, "error", err)
		}
		idx.contentMu.Lock()
		delete(idx.contentByName, docID)
		idx.contentMu.Unlock()

		idx.mu.Lock()
		delete(idx.pathByName, docID)
		delete(idx.pathToDocID, relPath)
		for i, n := range idx.names {
			if n == docID {
				idx.names = append(idx.names[:i], idx.names[i+1:]...)
				break
			}
		}
		idx.mu.Unlock()
	}

	// Apply additions and modifications.
	for _, relPath := range append(diff.Added, diff.Modified...) {
		absPath := filepath.Join(idx.dir, filepath.FromSlash(relPath))
		data, err := os.ReadFile(absPath)
		if err != nil {
			slog.Warn("Cannot read file", "path", relPath, "error", err)
			continue
		}
		docID := bslPathToModuleName(relPath)
		content := stripBOM(string(data))

		parts := parseModuleName(docID)
		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  content,
		}

		si := shardForID(docID, len(idx.shards))
		if err := idx.shards[si].Index(docID, doc); err != nil {
			slog.Warn("Failed to index in shard", "docID", docID, "error", err)
			continue
		}

		idx.contentMu.RLock()
		_, inContent := idx.contentByName[docID]
		idx.contentMu.RUnlock()

		idx.mu.Lock()
		_, inPath := idx.pathByName[docID]
		if !inContent && !inPath {
			idx.names = append(idx.names, docID)
		}
		idx.pathByName[docID] = absPath
		idx.pathToDocID[relPath] = docID
		idx.mu.Unlock()

		// Pre-warm content cache for recently changed files.
		idx.contentMu.Lock()
		idx.contentByName[docID] = content
		idx.contentMu.Unlock()
	}

	if len(diff.Added) > 0 || len(diff.Modified) > 0 || len(diff.Deleted) > 0 {
		slog.Info("Incremental update", "added", len(diff.Added), "modified", len(diff.Modified), "deleted", len(diff.Deleted))
	}

	// Save updated manifest.
	idx.saveManifest(cacheDir)

	return nil
}

// ModuleCount returns the number of indexed BSL modules.
func (idx *Index) ModuleCount() int {
	idx.mu.RLock()
	n := len(idx.names)
	idx.mu.RUnlock()
	return n
}

// ModuleNames returns a defensive copy of the indexed BSL module names.
// Each entry is the human-readable, russian-translated ID as produced by
// bslPathToModuleName (e.g. "Документ.РеализацияТоваров.МодульОбъекта"),
// which is the same key used by GetContent.
//
// Returns an empty slice (never nil) when no modules are indexed. The copy
// is taken under idx.mu.RLock to be safe against concurrent index updates
// (IndexDoc/DeleteDoc) — callers may modify or sort the returned slice
// without affecting the index.
func (idx *Index) ModuleNames() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.names == nil {
		return []string{}
	}
	return slices.Clone(idx.names)
}

// BuildError returns the most recent error captured during background index
// build (loadBSLFiles failure, shard error, or manifest-fallback failure), or
// nil if no error was recorded. Safe to call after <-idx.Done() has unblocked
// to distinguish "build completed successfully" (Ready() == true, BuildError()
// == nil) from "build aborted" (Ready() == false, BuildError() != nil).
//
// Read is unconditional: consumers may observe a non-nil BuildError() even
// while Ready() is still false during the build, but the field is set exactly
// once on error paths so the return value is stable across repeated reads.
func (idx *Index) BuildError() error {
	if errPtr := idx.buildErr.Load(); errPtr != nil {
		return *errPtr
	}
	return nil
}

// Dir returns the dump directory path.
func (idx *Index) Dir() string {
	return idx.dir
}

// GetPathIndex returns the path index for fast category/module filtering.
// Returns nil if the index is not yet ready.
func (idx *Index) GetPathIndex() *PathIndex {
	if !idx.ready.Load() {
		return nil
	}
	return idx.pathIndex
}

// applyIncrementalUpdate loads the manifest, diffs against the filesystem,
// and applies IndexDoc/DeleteDoc for changed files. If no manifest exists
// (first run after upgrade), it only saves a new one for future runs.
func (idx *Index) applyIncrementalUpdate(cacheDir string) error {
	manifest, err := LoadManifest(cacheDir)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}

	if manifest == nil {
		// No manifest yet (first run with incremental support).
		// Save one now so next start can diff.
		idx.saveManifest(cacheDir)
		return nil
	}

	diff, err := manifest.Diff(idx.dir)
	if err != nil {
		return fmt.Errorf("computing diff: %w", err)
	}

	if diff.Empty() {
		return nil
	}

	// Apply deletions.
	for _, relPath := range diff.Deleted {
		entry, ok := manifest.Files[relPath]
		if !ok {
			continue
		}
		docID := entry.DocID
		si := shardForID(docID, len(idx.shards))
		if err := idx.shards[si].Delete(docID); err != nil {
			slog.Warn("Failed to delete from shard", "docID", docID, "error", err)
		}
		idx.contentMu.Lock()
		delete(idx.contentByName, docID)
		idx.contentMu.Unlock()

		idx.mu.Lock()
		delete(idx.pathByName, docID)
		delete(idx.pathToDocID, relPath)
		for i, n := range idx.names {
			if n == docID {
				idx.names = append(idx.names[:i], idx.names[i+1:]...)
				break
			}
		}
		idx.mu.Unlock()
	}

	// Apply additions and modifications.
	for _, relPath := range append(diff.Added, diff.Modified...) {
		absPath := filepath.Join(idx.dir, filepath.FromSlash(relPath))
		data, err := os.ReadFile(absPath)
		if err != nil {
			slog.Warn("Cannot read file", "path", relPath, "error", err)
			continue
		}
		docID := bslPathToModuleName(relPath)
		content := stripBOM(string(data))

		parts := parseModuleName(docID)
		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  content,
		}

		si := shardForID(docID, len(idx.shards))
		if err := idx.shards[si].Index(docID, doc); err != nil {
			slog.Warn("Failed to index in shard", "docID", docID, "error", err)
			continue
		}

		idx.contentMu.RLock()
		_, inContent := idx.contentByName[docID]
		idx.contentMu.RUnlock()

		idx.mu.Lock()
		_, inPath := idx.pathByName[docID]
		if !inContent && !inPath {
			idx.names = append(idx.names, docID)
		}
		idx.pathByName[docID] = absPath
		idx.pathToDocID[relPath] = docID
		idx.mu.Unlock()

		// Pre-warm content cache for recently changed files.
		idx.contentMu.Lock()
		idx.contentByName[docID] = content
		idx.contentMu.Unlock()
	}

	slog.Info("Incremental update", "added", len(diff.Added), "modified", len(diff.Modified), "deleted", len(diff.Deleted))

	// Save updated manifest.
	idx.saveManifest(cacheDir)

	return nil
}

// saveManifest builds and persists a manifest from current pathToDocID state.
func (idx *Index) saveManifest(cacheDir string) {
	idx.mu.RLock()
	pathCopy := make(map[string]string, len(idx.pathToDocID))
	for k, v := range idx.pathToDocID {
		pathCopy[k] = v
	}
	idx.mu.RUnlock()

	manifest, err := buildManifest(idx.dir, pathCopy)
	if err != nil {
		slog.Warn("Cannot build manifest", "error", err)
		return
	}
	if err := manifest.Save(cacheDir); err != nil {
		slog.Warn("Cannot save manifest", "error", err)
	}
}

// Close cancels the background context, waits for any in-progress build to
// finish, and closes all shard indexes.
func (idx *Index) Close() error {
	idx.cancel()
	<-idx.done
	if idx.readerReg != nil {
		// Deregister from the generation's readers/ registry so GC can reclaim the
		// generation once no live reader holds it.
		idx.readerReg.Close()
	}
	if idx.lockDir != "" {
		removeCacheLock(idx.lockDir)
	}
	var firstErr error
	for _, shard := range idx.shards {
		if err := shard.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
