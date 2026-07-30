package dump

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
)

// realDumpDir points to a real 1C dump for benchmarking.
// Benchmarks are skipped if this directory does not exist.
const realDumpDir = "/Users/igoroot/GolandProjects/mcp/dumps/dump_2"

// loadTestModules reads all BSL files from the real dump directory into memory.
// Returns names and contentByName for use in build benchmarks.
// Calls b.Skip if the dump directory is missing.
func loadTestModules(b *testing.B) ([]string, map[string]string) {
	b.Helper()

	if _, err := os.Stat(realDumpDir); os.IsNotExist(err) {
		b.Skipf("dump directory %s does not exist, skipping benchmark", realDumpDir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	idx := &Index{
		dir:           realDumpDir,
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]cachedModule),
		pathByName:    make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	defer close(idx.done)

	if err := idx.loadBSLFiles(realDumpDir); err != nil {
		b.Fatalf("loadBSLFiles: %v", err)
	}
	b.Logf("Loaded %d BSL modules from %s", len(idx.names), realDumpDir)

	// The shard builders take a plain name -> source map; the cache entries'
	// revalidation stamps are irrelevant to a build benchmark.
	contents := make(map[string]string, len(idx.contentByName))
	for name, entry := range idx.contentByName {
		contents[name] = entry.content
	}

	return idx.names, contents
}

// BenchmarkBuildIndex_Batch measures the current NewUsing + manual batch approach.
func BenchmarkBuildIndex_Batch(b *testing.B) {
	names, contentByName := loadTestModules(b)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		dir := b.TempDir()
		indexPath := dir + "/index"

		blevIdx, err := buildIndexBatch(indexPath, names, contentByName)
		if err != nil {
			b.Fatalf("buildIndexBatch: %v", err)
		}
		blevIdx.Close()
	}
}

// BenchmarkBuildIndex_Builder measures the offline NewBuilder approach.
func BenchmarkBuildIndex_Builder(b *testing.B) {
	names, contentByName := loadTestModules(b)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		dir := b.TempDir()
		indexPath := dir + "/index"

		blevIdx, err := buildIndexBuilder(indexPath, names, contentByName)
		if err != nil {
			b.Fatalf("buildIndexBuilder: %v", err)
		}
		blevIdx.Close()
	}
}

// BenchmarkBuildIndex_Sharded measures parallel shard build performance.
func BenchmarkBuildIndex_Sharded(b *testing.B) {
	names, contentByName := loadTestModules(b)
	n := shardCount(len(names))
	b.Logf("Shard count: %d (for %d modules)", n, len(names))

	b.ResetTimer()
	b.ReportAllocs()

	bslMapping := buildBSLMapping()

	for b.Loop() {
		dir := b.TempDir()
		groups := splitByHash(names, n)

		type result struct {
			idx bleve.Index
			err error
		}
		results := make(chan result, n)

		for i := range n {
			go func(shardID int) {
				path := dir + fmt.Sprintf("/shard_%d", shardID)
				idx, err := buildShard(path, groups[shardID], func(name string) string { return contentByName[name] }, shardID, n, bslMapping, &atomic.Int64{})
				results <- result{idx: idx, err: err}
			}(i)
		}

		for range n {
			r := <-results
			if r.err != nil {
				b.Fatalf("buildShard: %v", r.err)
			}
			r.idx.Close()
		}
	}
}

// openRealIndex builds a fresh index from the real dump for search benchmarks.
func openRealIndex(b *testing.B) *Index {
	b.Helper()

	if _, err := os.Stat(realDumpDir); os.IsNotExist(err) {
		b.Skipf("dump directory %s does not exist, skipping benchmark", realDumpDir)
	}

	idx, err := NewIndex(realDumpDir, "", true)
	if err != nil {
		b.Fatalf("NewIndex: %v", err)
	}

	deadline := time.After(5 * time.Minute)
	for !idx.Ready() {
		select {
		case <-deadline:
			idx.Close()
			b.Fatal("timed out waiting for index build")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	b.Logf("Index built: %d modules, %d shards", idx.ModuleCount(), len(idx.shards))
	return idx
}

// BenchmarkSearch_Smart measures BM25 full-text search performance.
func BenchmarkSearch_Smart(b *testing.B) {
	idx := openRealIndex(b)
	defer idx.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		_, _, err := idx.Search(SearchParams{
			Query: "Процедура ПередЗаписью",
			Mode:  SearchModeSmart,
			Limit: 50,
		})
		if err != nil {
			b.Fatalf("Search smart: %v", err)
		}
	}
}

// BenchmarkSearch_Regex measures regex search performance.
func BenchmarkSearch_Regex(b *testing.B) {
	idx := openRealIndex(b)
	defer idx.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		_, _, err := idx.Search(SearchParams{
			Query: `Процедура\s+\w+Записью`,
			Mode:  SearchModeRegex,
			Limit: 50,
		})
		if err != nil {
			b.Fatalf("Search regex: %v", err)
		}
	}
}

// BenchmarkGetContent measures a warm content-cache read. In this repository
// searchSmart is its only in-tree caller, once per returned hit, so a search
// capped at Limit hits pays this cost Limit times; GetContent is exported, so
// embedders may call it directly as well. It is the cost the cache revalidation
// stat comes out of.
//
// Unlike the search benchmarks this one builds its own one-module dump instead
// of using realDumpDir: GetContent's per-call cost does not depend on corpus
// size, and a self-contained fixture keeps the number reproducible on a machine
// that does not have the local benchmark dump.
func BenchmarkGetContent(b *testing.B) {
	dir := b.TempDir()
	writeBSLTB(b, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		strings.Repeat("Процедура ПередЗаписью(Отказ)\n\t// проверка\nКонецПроцедуры\n", 200))

	idx, err := NewIndex(dir, "", true)
	if err != nil {
		b.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReadyTB(b, idx, 2*time.Minute)

	const docID = "Справочник.Номенклатура.МодульОбъекта"
	// Warm the cache outside the timer: the benchmark measures a cache hit, not
	// the one-off lazy load.
	if _, ok := idx.GetContent(docID); !ok {
		b.Fatalf("GetContent(%q) = not found: the benchmark fixture never loaded", docID)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if _, ok := idx.GetContent(docID); !ok {
			b.Fatalf("GetContent(%q) = not found", docID)
		}
	}
}

// BenchmarkSearch_Exact measures exact (case-insensitive substring) search performance.
func BenchmarkSearch_Exact(b *testing.B) {
	idx := openRealIndex(b)
	defer idx.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		_, _, err := idx.Search(SearchParams{
			Query: "ОбработкаПроведения",
			Mode:  SearchModeExact,
			Limit: 50,
		})
		if err != nil {
			b.Fatalf("Search exact: %v", err)
		}
	}
}
