package dump

import (
	"context"
	"fmt"
	"io/fs"
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

// dumpDirNames maps 1C metadata type names (English) used in DumpConfigToFiles
// to their Russian display name prefixes.
var dumpDirNames = map[string]string{
	"Catalogs":               "Справочник",
	"Documents":              "Документ",
	"DataProcessors":         "Обработка",
	"Reports":                "Отчет",
	"InformationRegisters":   "РегистрСведений",
	"AccumulationRegisters":  "РегистрНакопления",
	"AccountingRegisters":    "РегистрБухгалтерии",
	"CalculationRegisters":   "РегистрРасчета",
	"ChartsOfAccounts":       "ПланСчетов",
	"ChartsOfCharacteristicTypes": "ПланВидовХарактеристик",
	"ChartsOfCalculationTypes":    "ПланВидовРасчета",
	"ExchangePlans":          "ПланОбмена",
	"BusinessProcesses":      "БизнесПроцесс",
	"Tasks":                  "Задача",
	"CommonModules":          "ОбщийМодуль",
	"Enums":                  "Перечисление",
	"Constants":              "Константа",
}

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
	contentByName map[string]string
	ready         atomic.Bool
	mu            sync.RWMutex
	buildErr      atomic.Pointer[error]
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
}

// Ready reports whether the index has finished building and is available for search.
func (idx *Index) Ready() bool {
	return idx.ready.Load()
}

// loadedModule holds the result of reading a single .bsl file.
type loadedModule struct {
	name    string
	content string
}

// NewIndex walks the dump directory, loads all .bsl files in parallel and builds
// a Bleve index. The index is cached on disk for fast subsequent opens.
// If reindex is true, any existing cache is discarded and rebuilt from scratch.
// Progress is printed to stderr.
func NewIndex(dir string, reindex bool) (*Index, error) {
	ctx, cancel := context.WithCancel(context.Background())
	idx := &Index{
		dir:           dir,
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	defer close(idx.done)

	// Determine cache path; fall back to in-memory if it fails.
	cpath, cacheErr := cachePath(dir)
	useCache := cacheErr == nil

	// Try to open existing cache.
	if useCache && !reindex {
		if blevIdx, err := bleve.Open(cpath); err == nil {
			idx.shards = []bleve.Index{blevIdx}
			idx.alias.Add(blevIdx)

			// Load contentByName from .bsl files (needed for regex/exact mode).
			if err := idx.loadBSLFiles(dir); err != nil {
				blevIdx.Close()
				cancel()
				return nil, err
			}
			idx.ready.Store(true)
			fmt.Fprintf(os.Stderr, "Opened cached index for %d BSL modules\n", len(idx.names))
			return idx, nil
		}
	}

	// No usable cache — full build required.

	// Load all .bsl file contents.
	if err := idx.loadBSLFiles(dir); err != nil {
		cancel()
		return nil, err
	}

	// Prepare on-disk or in-memory Bleve index path.
	indexPath := "" // in-memory fallback
	if useCache {
		// Remove stale cache if it exists.
		os.RemoveAll(cpath)
		if err := os.MkdirAll(filepath.Dir(cpath), 0o755); err == nil {
			indexPath = cpath
		}
	}

	// Disable GC entirely during bulk indexing; restore after.
	oldGC := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGC)

	// Use NewUsing+batch approach (benchmarked faster than NewBuilder for our workload).
	// NewBuilder is available via buildIndexBuilder() for experimentation.
	blevIdx, err := buildIndexBatch(indexPath, idx.names, idx.contentByName)
	if err != nil {
		cancel()
		return nil, err
	}
	idx.shards = []bleve.Index{blevIdx}
	idx.alias.Add(blevIdx)
	idx.ready.Store(true)

	return idx, nil
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

	// Adaptive progress step: ~5% increments, minimum 100.
	step := max(total/20, 100)

	for i, name := range names {
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

		if (i+1)%step == 0 || i+1 == total {
			fmt.Fprintf(os.Stderr, "\rIndexing BSL modules... %d/%d", i+1, total)
		}
	}
	if total > 0 {
		fmt.Fprintln(os.Stderr, " done")
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

	// Adaptive progress step: ~5% increments, minimum 100.
	step := max(total/20, 100)

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

		if (i+1)%step == 0 || i+1 == total {
			fmt.Fprintf(os.Stderr, "\rIndexing BSL modules... %d/%d", i+1, total)
		}
	}
	if total > 0 {
		fmt.Fprintln(os.Stderr, " done")
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
			name := bslPathToModuleName(rel)
			results <- loadedModule{name: name, content: string(data)}
		}(p)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results from workers.
	for m := range results {
		idx.names = append(idx.names, m.name)
		idx.contentByName[m.name] = m.content
	}

	return nil
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
		}, params.Query)
	case SearchModeExact:
		lower := strings.ToLower(params.Query)
		return idx.searchLineByLine(params, func(line, q string) bool {
			return strings.Contains(strings.ToLower(line), q)
		}, lower)
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

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var matches []Match
	for _, hit := range result.Hits {
		content, ok := idx.contentByName[hit.ID]
		if !ok {
			continue
		}
		// Find the first line containing any query term for context.
		lines := strings.Split(content, "\n")
		lower := strings.ToLower(params.Query)
		tokens := strings.Fields(lower)

		lineNum := 0
		for i, line := range lines {
			ll := strings.ToLower(line)
			for _, tok := range tokens {
				if strings.Contains(ll, tok) {
					lineNum = i + 1
					break
				}
			}
			if lineNum > 0 {
				break
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
func (idx *Index) searchLineByLine(params SearchParams, match func(line, q string) bool, q string) ([]Match, int, error) {
	candidates, err := idx.filterModules(params.Category, params.Module)
	if err != nil {
		return nil, 0, err
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var matches []Match
	total := 0

	for _, name := range candidates {
		content := idx.contentByName[name]
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if match(line, q) {
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
// If no filters are set, returns all names. Uses Bleve for efficient filtering.
func (idx *Index) filterModules(category, moduleType string) ([]string, error) {
	if category == "" && moduleType == "" {
		return idx.names, nil
	}

	// Use Bleve to filter by category/module.
	queries := []query.Query{bleve.NewMatchAllQuery()}
	if category != "" {
		tq := bleve.NewTermQuery(category)
		tq.SetField("category")
		queries = append(queries, tq)
	}
	if moduleType != "" {
		tq := bleve.NewTermQuery(moduleType)
		tq.SetField("module")
		queries = append(queries, tq)
	}

	q := bleve.NewConjunctionQuery(queries...)
	req := bleve.NewSearchRequestOptions(q, len(idx.names), 0, false)
	result, err := idx.alias.Search(req)
	if err != nil {
		return nil, fmt.Errorf("bleve filter: %w", err)
	}

	names := make([]string, 0, len(result.Hits))
	for _, hit := range result.Hits {
		if _, ok := idx.contentByName[hit.ID]; ok {
			names = append(names, hit.ID)
		}
	}
	return names, nil
}

// ModuleCount returns the number of indexed BSL modules.
func (idx *Index) ModuleCount() int {
	return len(idx.names)
}

// Dir returns the dump directory path.
func (idx *Index) Dir() string {
	return idx.dir
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
