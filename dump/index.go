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

	"github.com/blevesearch/bleve/v2"
	scorchIndex "github.com/blevesearch/bleve/v2/index/scorch"
	"github.com/blevesearch/bleve/v2/search/query"
)

// utf8BOM is the 3-byte UTF-8 Byte Order Mark (U+FEFF) that 1C DumpConfigToFiles
// prepends to BSL files. It must be stripped before indexing or returning content.
const utf8BOM = "\xEF\xBB\xBF"

// stripBOM removes the UTF-8 BOM prefix from s if present.
func stripBOM(s string) string {
	return strings.TrimPrefix(s, utf8BOM)
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
	synonymMapOnce  sync.Once
	cachedSynonymMap map[string]string
)

// moduleNameSuffixes maps BSL file names to their module type suffix.
var moduleNameSuffixes = map[string]string{
	"ObjectModule.bsl":    "МодульОбъекта",
	"ManagerModule.bsl":   "МодульМенеджера",
	"Module.bsl":          "МодульФормы",
	"RecordSetModule.bsl": "МодульНабораЗаписей",
	"CommandModule.bsl":   "МодульКоманды",
	"Ext.Module.bsl":      "МодульФормы",
}

// bslPathToModuleName converts a relative file path from the dump to a human-readable module name.
// Example: "Documents/РеализацияТоваров/Ext/ObjectModule.bsl" -> "Документ.РеализацияТоваров.МодульОбъекта"
func bslPathToModuleName(relPath string) string {
	// Normalise separators.
	relPath = filepath.ToSlash(relPath)
	parts := strings.Split(relPath, "/")

	if len(parts) < 2 {
		return relPath
	}

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

	// If the path has a Forms subdirectory, include form name.
	for i, p := range parts {
		if p == "Forms" && i+1 < len(parts) {
			formName := parts[i+1]
			return prefix + "." + objectName + ".Форма." + formName + "." + suffix
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
	contentByName map[string]string // cache: docID -> content (lazy populated)
	pathByName    map[string]string // docID -> absolute file path (always populated)
	pathToDocID   map[string]string // relative path (ToSlash) -> module name
	pathIndex     *PathIndex        // decomposed path index for fast category/module filtering
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

	// Lazy load from disk under write lock.
	idx.contentMu.Lock()
	defer idx.contentMu.Unlock()

	// Double-check after acquiring write lock.
	if content, ok := idx.contentByName[id]; ok {
		return content, true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	content := stripBOM(string(data))
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
func NewIndex(dir string, reindex bool) (*Index, error) {
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

	cpath, cacheErr := cachePath(dir)
	useCache := cacheErr == nil

	if useCache && reindex {
		os.RemoveAll(cpath)
	}

	// Try to open existing sharded cache.
	if useCache && !reindex {
		if shardDirs := cacheShardDirs(cpath); len(shardDirs) > 0 {
			shards, err := openCachedShards(shardDirs)
			if err == nil {
				idx.shards = shards
				idx.alias.Add(shards...)

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
					fmt.Fprintf(os.Stderr, "Индекс загружен из кэша: %d модулей\n", len(idx.names))
				}()
				return idx, nil
			}
			// Cache corrupt — remove and rebuild.
			os.RemoveAll(cpath)
		}
	}

	// No usable cache — full sharded build in background.
	go func() {
		defer close(idx.done)
		idx.buildShards(cpath, useCache)
	}()

	return idx, nil
}

// setBuildErr stores a build error atomically.
func (idx *Index) setBuildErr(err error) {
	idx.buildErr.Store(&err)
}

// buildShards loads BSL files and builds N shards in parallel.
func (idx *Index) buildShards(cpath string, useCache bool) {
	if err := idx.loadBSLFiles(idx.dir); err != nil {
		idx.setBuildErr(fmt.Errorf("loading BSL files: %w", err))
		return
	}

	total := len(idx.names)
	if total == 0 {
		idx.pathIndex = NewPathIndex(nil)
		idx.ready.Store(true)
		slog.Info("No BSL modules found, index is empty")
		fmt.Fprintf(os.Stderr, "Внимание: в директории %s не найдено .bsl файлов\n", idx.dir)
		return
	}

	n := shardCount(total)
	groups := splitByHash(idx.names, n)
	slog.Info("Building index", "modules", total, "shards", n)
	fmt.Fprintf(os.Stderr, "Индексация: найдено %d модулей...\n", total)

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

	// Disable GC for entire parallel build.
	oldGC := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGC)

	type shardResult struct {
		index bleve.Index
		id    int
		err   error
	}
	results := make(chan shardResult, n)

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

			shard, err := buildShard(shardPath, groups[shardID], idx.contentByName, shardID, n, bslMapping)
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
	if firstErr != nil {
		for _, s := range shards {
			if s != nil {
				s.Close()
			}
		}
		if cpath != "" {
			os.RemoveAll(cpath)
		}
		idx.setBuildErr(firstErr)
		return
	}

	idx.shards = shards
	idx.alias.Add(shards...)
	idx.pathIndex = NewPathIndex(idx.names)
	idx.ready.Store(true)

	// Save manifest for future incremental updates.
	if cpath != "" && useCache {
		idx.saveManifest(cpath)
	}

	slog.Info("Index ready", "modules", total, "shards", n)
	fmt.Fprintf(os.Stderr, "Индексация завершена: %d модулей готово к поиску\n", total)
}

// openCachedShards opens pre-built Bleve shard indexes from disk.
// On any error, all previously opened shards are closed.
func openCachedShards(dirs []string) ([]bleve.Index, error) {
	shards := make([]bleve.Index, len(dirs))
	for i, dir := range dirs {
		blevIdx, err := bleve.Open(dir)
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
		"forceSegmentVersion": 16,
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

// ensureAllContentLoaded bulk-loads all file content that hasn't been lazily loaded yet.
// Used by searchLineByLine (regex/exact modes) which need to scan all modules.
// Reads files in parallel using a worker pool for performance.
//
// Lock ordering: mu.RLock is acquired and released first (phase 1), then contentMu
// is used separately (phase 2, 3). This avoids deadlock with GetContent which takes
// contentMu then mu.
func (idx *Index) ensureAllContentLoaded() {
	// Phase 1: collect all name->path mappings (under mu only).
	idx.mu.RLock()
	allPaths := make(map[string]string, len(idx.pathByName))
	for name, path := range idx.pathByName {
		allPaths[name] = path
	}
	idx.mu.RUnlock()

	// Phase 2: find which ones need loading (under contentMu only).
	idx.contentMu.RLock()
	var toLoad []struct{ name, path string }
	for name, path := range allPaths {
		if _, ok := idx.contentByName[name]; !ok {
			toLoad = append(toLoad, struct{ name, path string }{name, path})
		}
	}
	idx.contentMu.RUnlock()

	if len(toLoad) == 0 {
		return
	}

	// Phase 3: load files in parallel, write to contentByName.
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for _, item := range toLoad {
		wg.Add(1)
		sem <- struct{}{}
		go func(name, path string) {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := os.ReadFile(path)
			if err != nil {
				return
			}
			idx.contentMu.Lock()
			idx.contentByName[name] = stripBOM(string(data))
			idx.contentMu.Unlock()
		}(item.name, item.path)
	}
	wg.Wait()
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

	// Bulk-load all content for line-by-line scan.
	idx.ensureAllContentLoaded()

	idx.contentMu.RLock()
	defer idx.contentMu.RUnlock()

	var matches []Match
	total := 0

	for _, name := range candidates {
		content := idx.contentByName[name]
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			matchLine := line
			if preLower {
				matchLine = strings.ToLower(line)
			}
			if match(matchLine, q) {
				total++
				if len(matches) < params.Limit {
					ctx := extractContext(lines, i, 2)
					matches = append(matches, Match{
						Module:  name,
						Line:    i + 1,
						Context: ctx,
					})
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
	idx.mu.Lock()
	idx.pathToDocID = make(map[string]string, len(manifest.Files))
	for relPath, entry := range manifest.Files {
		absPath := filepath.Join(idx.dir, filepath.FromSlash(relPath))
		idx.names = append(idx.names, entry.DocID)
		idx.pathByName[entry.DocID] = absPath
		idx.pathToDocID[relPath] = entry.DocID
	}
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
	var firstErr error
	for _, shard := range idx.shards {
		if err := shard.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
