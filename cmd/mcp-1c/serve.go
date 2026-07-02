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
			gensig  string
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
			gensig, prepErr = prepareServeGeneration(ctx, dumpDir, cacheDir, reindex)
		}()

		// Finish the open IN PLACE on the placeholder. FinishServeOpen opens the READY
		// generation named by gensig READ-ONLY and closes Done() exactly once on every
		// path (success, prepErr, ctx-cancel, recovered panic), so waiters — Close() in
		// particular — never block.
		placeholder.FinishServeOpen(cacheDir, gensig, prepErr)
		if prepErr != nil {
			slog.Error("serve index: background prepare failed; search_code / code_read "+
				"report a build error for this dump until restart",
				"dump", dumpDir, "error", prepErr)
			return
		}

		// FinishServeOpen registered this process as a live reader of the generation, so
		// reaping older unheld generations now preserves the register-then-GC ordering.
		// Best-effort: a GC error must never disturb serving.
		if removed, gcErr := dump.GCGenerations(dumpDir, cacheDir, gensig); gcErr != nil {
			slog.Warn("serve index: old-generation GC failed", "dump", dumpDir, "error", gcErr)
		} else if len(removed) > 0 {
			slog.Info("serve index: reaped old generations", "dump", dumpDir, "removed", removed)
		}
	}()
	return placeholder, nil
}

// prepareServeGeneration ensures a READY immutable generation for the current dump
// signature exists and returns its gensig (which FinishServeOpen then opens
// read-only). It mirrors the paid serve path minus the build-leader election: every
// process is a builder, safe because BuildGeneration is concurrency-safe (unique
// temp dir, first-to-rename wins).
//
// Order: compute GenSig -> reap abandoned .building-* temp dirs (best-effort) -> if a
// READY generation already exists (and no --reindex) use it -> else adopt an existing
// legacy flat cache by rename (no rebuild), or when nothing is adoptable — or
// --reindex forces it — build a fresh generation. ctx is honoured between steps so
// shutdown aborts before starting heavy work; GenSig and BuildGeneration are
// themselves synchronous and cannot be interrupted mid-flight.
func prepareServeGeneration(ctx context.Context, dumpDir, cacheDir string, reindex bool) (string, error) {
	gensig, err := dump.GenSig(dumpDir)
	if err != nil {
		return "", fmt.Errorf("computing dump signature for %s: %w", dumpDir, err)
	}

	// Reap abandoned .building-* temp generation dirs left by a builder that died
	// mid-build (SIGKILL / OOM / power loss) before adopting or rolling back — nothing
	// else reaps them. Best-effort: a reap error must never fail a serve open.
	if reaped, rErr := dump.ReapStaleBuildDirs(dumpDir, cacheDir); rErr != nil {
		slog.Warn("serve index: stale build-dir reap failed", "dump", dumpDir, "error", rErr)
	} else if len(reaped) > 0 {
		slog.Info("serve index: reaped abandoned build temp dirs", "dump", dumpDir, "reaped", reaped)
	}

	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	// Read fast-path: a READY generation for this signature lets N processes coexist
	// read-only. Skipped while --reindex forces a fresh build.
	if !reindex && dump.GenerationReady(dumpDir, cacheDir, gensig) {
		return gensig, nil
	}

	// Adopt an existing legacy flat cache as a generation first (an O(shards) rename
	// that re-indexes nothing and reclaims the flat cache); only a genuinely cache-less
	// dump — or an explicit --reindex — falls through to the full cold BuildGeneration.
	if !reindex {
		if g, migrated, adoptErr := dump.AdoptFlatGeneration(dumpDir, cacheDir); adoptErr != nil {
			slog.Warn("serve index: flat->generation adopt failed; falling back to a full build",
				"dump", dumpDir, "error", adoptErr)
		} else if migrated {
			slog.Info("serve index: adopted existing cache as generation without reindex",
				"dump", dumpDir, "gensig", g)
			return g, nil
		}
	}

	slog.Info("serve index: building generation", "dump", dumpDir, "gensig", gensig, "reindex", reindex)
	if reindex {
		// --reindex must FORCE a cold rebuild of the current signature even when the
		// dump content is unchanged (the operator is recovering a suspected-corrupt
		// cache). A plain BuildGeneration is content-addressed and no-ops on an
		// already-READY gensig, so it would rebuild nothing. ForceRebuildGeneration
		// drops the current generation first — but only when no co-located process is
		// still serving this exact generation, so a concurrent reader is never yanked
		// (in that case the drop is skipped and BuildGeneration no-ops, preserving the
		// in-use generation). Mirrors the legacy reindexGeneration force-rebuild.
		if err := dump.ForceRebuildGeneration(dumpDir, cacheDir, gensig); err != nil {
			return "", fmt.Errorf("force-rebuilding dump generation for %s: %w", dumpDir, err)
		}
		return gensig, nil
	}
	if err := dump.BuildGeneration(dumpDir, cacheDir, gensig); err != nil {
		return "", fmt.Errorf("building dump generation for %s: %w", dumpDir, err)
	}
	return gensig, nil
}
