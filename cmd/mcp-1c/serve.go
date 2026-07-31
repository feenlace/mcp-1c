package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/feenlace/mcp-1c/dump"
)

// openServeIndexLocal opens the --dump search index for SERVING without ever
// blocking the MCP initialize handshake and — crucially — WITHOUT taking the
// legacy read-write bbolt exclusive lock (LOCK_EX) that made a second co-located
// mcp-1c process on the same --dump HANG before it could answer initialize
// (issue #30). It returns a not-yet-ready *dump.Index placeholder IMMEDIATELY and
// prepares the on-disk generation in a background goroutine, then opens THAT
// generation READ-ONLY (bbolt LOCK_SH). Because every co-located process opens the
// same READY generation read-only, N processes coexist on one --dump+cache with no
// exclusive-lock hang.
//
// Unlike the paid editions, there is NO build-leader election: a concurrent build
// is content-addressed and concurrency-safe (BuildGeneration builds into a unique
// temp dir and the first to rename wins; the losers discard their temp dir), so
// each process simply builds if it must. This mirrors the paid path's lock-failure
// degrade branch, made unconditional.
//
// No-cache fallback: when no writable cache location can be resolved (a scrubbed
// environment with an unset HOME and no --cache-dir / MCP_1C_CACHE_DIR) there is no
// shared on-disk generation for processes to coexist on, and BuildGeneration would
// hard-fail. This then degrades to the legacy dump.NewIndex path, which builds the
// index in memory and still serves — behaviour identical to before this change for
// that single lone process.
//
// ctx is cancelled by main once s.Run returns, so a background build in flight at
// shutdown cannot wedge process exit: the goroutine checks ctx between steps and
// FinishServeOpen always closes the placeholder's Done() channel exactly once.
func openServeIndexLocal(ctx context.Context, dumpDir, cacheDir string, reindex bool) (*dump.Index, error) {
	// No writable cache => no shared on-disk generation for processes to coexist on.
	// Fall back to the legacy in-memory build, which still serves. CacheDir wraps the
	// same cachePath resolution BuildGeneration/BuildCache use to report "no writable
	// cache", so branching on it here keeps this path consistent with the rest of the
	// dump package.
	if _, err := dump.CacheDir(dumpDir, cacheDir); err != nil {
		slog.Warn("serve index: no writable cache directory; serving a single in-memory "+
			"index (no concurrent multi-process serve without a shared cache)",
			"dump", dumpDir, "error", err)
		return dump.NewIndex(dumpDir, cacheDir, reindex)
	}

	// Hand back a not-yet-ready placeholder NOW, before ANY dump-size-dependent work
	// (the GenSig directory walk and the adopt/build both run in the goroutine below),
	// so the MCP initialize handshake returns immediately regardless of dump size.
	placeholder := dump.NewServePlaceholder(dumpDir)
	go func() {
		var (
			gen     *dump.ServeGeneration
			prepErr error
		)
		// Recover a build panic into prepErr so it becomes a recorded BuildError the
		// readers surface, instead of crashing the process (the blocking legacy open
		// let such a panic crash, and the OS freed its locks; here the process lives on).
		func() {
			defer func() {
				if r := recover(); r != nil {
					prepErr = fmt.Errorf("panic during background serve build for %s: %v", dumpDir, r)
				}
			}()
			if ctx.Err() != nil {
				prepErr = ctx.Err()
				return
			}
			gen, prepErr = dump.PrepareServeGeneration(ctx, dumpDir, cacheDir, reindex)
		}()

		// Finish the open IN PLACE on the placeholder. FinishServeOpen opens the
		// generation gen names READ-ONLY and closes Done() exactly once on every path
		// (success, prepErr, ctx-cancel, recovered panic), so waiters — Close() in
		// particular — never block. It also TAKES OVER gen's reader claim on every
		// one of those paths, either into the index it opened or by releasing it, so
		// there is nothing left for this goroutine to clean up.
		placeholder.FinishServeOpen(cacheDir, gen, prepErr)
		if prepErr != nil {
			slog.Error("serve index: background prepare failed; search_code / code_read "+
				"report a build error for this dump until restart",
				"dump", dumpDir, "error", prepErr)
			return
		}

		// The generation has been claimed since before it entered the shared arena, so
		// reaping older unheld generations now cannot touch the one being served.
		// Best-effort: a GC error must never disturb serving.
		if removed, gcErr := dump.GCGenerations(dumpDir, cacheDir, gen.Gensig()); gcErr != nil {
			slog.Warn("serve index: old-generation GC failed", "dump", dumpDir, "error", gcErr)
		} else if len(removed) > 0 {
			slog.Info("serve index: reaped old generations", "dump", dumpDir, "removed", removed)
		}
	}()
	return placeholder, nil
}
