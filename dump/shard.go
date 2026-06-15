package dump

import (
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
)

// offlineBuilderBatchSize bounds how many documents the offline builder keeps in
// memory before flushing an analyzed segment to disk. Smaller batches lower peak
// RSS during the cold build (the in-flight batch is analyzed and turned into a
// segment all at once, and shards build in parallel) at the cost of more on-disk
// segments to merge. The builder merges all segments into one at Close, so the
// resulting index — and search results — are independent of this value.
const offlineBuilderBatchSize = 64

// inMemoryShardBatchSize is the batch size for the in-memory fallback path
// (no persistent cache directory available).
const inMemoryShardBatchSize = 5000

// buildGCPercent is the GC target (debug.SetGCPercent) applied for the duration
// of the parallel cold build, then restored. Lower than the default 100 so the
// allocation-heavy build keeps less heap headroom; relative, so it adapts to any
// config size.
const buildGCPercent = 40

// shardCount returns the optimal number of index shards for the given file count.
// Uses 1 shard per 2000 files, clamped to [1, runtime.NumCPU()].
func shardCount(totalFiles int) int {
	if totalFiles <= 0 {
		return 1
	}
	n := totalFiles / 2000
	return max(1, min(runtime.NumCPU(), n))
}

// shardForID returns a deterministic shard index for the given document ID
// using FNV-32a hash. Result is in [0, n).
func shardForID(id string, n int) int {
	if n <= 1 {
		return 0
	}
	h := fnv.New32a()
	h.Write([]byte(id))
	return int(h.Sum32() % uint32(n))
}

// splitByHash distributes items across n groups using shardForID.
// Each item lands in exactly one group determined by its hash.
func splitByHash(items []string, n int) [][]string {
	groups := make([][]string, n)
	for _, item := range items {
		i := shardForID(item, n)
		groups[i] = append(groups[i], item)
	}
	return groups
}

// buildShard creates a single Bleve shard index for the provided names using
// content from contentByName. shardID and totalShards are used for progress
// reporting. The caller must supply a pre-built bslMapping to avoid rebuilding it
// per shard.
//
// When a persistent path is supplied (the normal cached cold build) the shard is
// built with the offline builder (bleve.NewBuilder), which streams analyzed
// segments to disk and merges them there, keeping only ~offlineBuilderBatchSize
// documents in memory at a time. This bounds peak RSS during the cold build,
// which previously retained the entire shard in RAM (in-memory scorch with
// unsafe_batch). The builder emits a standard scorch index, so the opened result
// stays fully mutable for runtime IndexDoc/DeleteDoc and incremental warm-start
// updates.
//
// When path is empty (no writable cache directory) the offline builder cannot be
// used, so it falls back to an in-memory scorch index.
//
// getContent resolves a module name to its BSL source. The cold build passes a
// resolver that reads each file from disk on demand, so the full corpus is never
// resident at once (the dominant cold-build memory peak).
func buildShard(path string, names []string, getContent func(name string) string, shardID, totalShards int, bslMapping *mapping.IndexMappingImpl, progress *atomic.Int64) (bleve.Index, error) {
	if path != "" {
		return buildShardOffline(path, names, getContent, shardID, totalShards, bslMapping, progress)
	}
	return buildShardInMemory(names, getContent, shardID, totalShards, bslMapping, progress)
}

// buildShardOffline builds a shard on disk via the bounded-memory offline
// builder and returns the opened (mutable) index.
func buildShardOffline(path string, names []string, getContent func(name string) string, shardID, totalShards int, bslMapping *mapping.IndexMappingImpl, progress *atomic.Int64) (bleve.Index, error) {
	// Start from a clean directory: bleve.NewBuilder writes index_meta.json with
	// O_CREATE|O_EXCL and fails if a stale shard directory is present (e.g. from a
	// previously interrupted build). A cold build always wants fresh shards.
	if err := os.RemoveAll(path); err != nil {
		return nil, fmt.Errorf("clearing shard %d path: %w", shardID, err)
	}

	builder, err := bleve.NewBuilder(path, bslMapping, map[string]any{
		"forceSegmentType":    "zap",
		"forceSegmentVersion": 16,
		"batchSize":           offlineBuilderBatchSize,
	})
	if err != nil {
		return nil, fmt.Errorf("creating bleve builder for shard %d: %w", shardID, err)
	}

	for _, name := range names {
		parts := parseModuleName(name)
		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  getContent(name),
		}
		if err := builder.Index(name, doc); err != nil {
			builder.Close()
			return nil, fmt.Errorf("shard %d builder index %q: %w", shardID, name, err)
		}
		progress.Add(1)
	}

	if err := builder.Close(); err != nil {
		return nil, fmt.Errorf("closing bleve builder for shard %d: %w", shardID, err)
	}

	blevIdx, err := bleve.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening built shard %d: %w", shardID, err)
	}

	if len(names) > 0 {
		slog.Info("Shard indexed (offline builder)", "shard", shardID+1, "totalShards", totalShards, "modules", len(names))
	}
	return blevIdx, nil
}

// buildShardInMemory builds a shard as an in-memory scorch index. Used only when
// no persistent cache directory is available; memory is NOT bounded here, but
// this path runs only with the cache disabled.
func buildShardInMemory(names []string, getContent func(name string) string, shardID, totalShards int, bslMapping *mapping.IndexMappingImpl, progress *atomic.Int64) (bleve.Index, error) {
	blevIdx, err := bleve.NewUsing("", bslMapping, "scorch", "scorch", map[string]any{
		"unsafe_batch": true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating in-memory bleve shard %d: %w", shardID, err)
	}

	total := len(names)
	batch := blevIdx.NewBatch()
	for i, name := range names {
		parts := parseModuleName(name)
		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  getContent(name),
		}
		batch.Index(name, doc)
		progress.Add(1)

		if (i+1)%inMemoryShardBatchSize == 0 || i+1 == total {
			if err := blevIdx.Batch(batch); err != nil {
				blevIdx.Close()
				return nil, fmt.Errorf("shard %d batch: %w", shardID, err)
			}
			batch = blevIdx.NewBatch()
		}

	}
	if total > 0 {
		slog.Info("Shard indexed (in-memory)", "shard", shardID+1, "totalShards", totalShards, "modules", total)
	}

	return blevIdx, nil
}
