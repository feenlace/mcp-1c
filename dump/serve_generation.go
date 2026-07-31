package dump

import (
	"context"
	"fmt"
	"log/slog"
)

// ServeGeneration is a READY index generation this process has prepared for
// serving AND already holds a live reader claim on. It is the handle that carries
// that claim from PrepareServeGeneration to (*Index).FinishServeOpen.
//
// WHY THE CLAIM HAS TO TRAVEL, rather than being taken again at the open. A
// generation becomes reapable the instant it is READY in the shared arena: every
// co-located process runs a reaper (GCGenerations), and a reaper deletes any READY
// generation that is not its own current one and that nothing records as held.
// A build that publishes its generation and only then claims it therefore hands
// every reaper in the arena a window on the generation this process is about to
// serve. Measured, across two OS processes: with the claim taken after the
// publish, 4 of 8 cold serve opens lost their freshly built generation to a
// co-located reaper — some to an outright removal, more to the reaper's
// claim-by-rename moving the directory out from under an open that had already
// resolved paths inside it (shard opens failing with "path does not exist" and
// "metadata missing"), even on the passes where the reaper correctly decided not
// to delete and rolled the rename back.
//
// So the claim is taken where the generation is still PRIVATE — inside the build's
// temp directory, adopted into the arena together with it — and then carried. The
// generation is never observable as READY-and-unclaimed, so the reaper's very
// first look already sees a holder and it never renames anything.
//
// A nil *ServeGeneration is usable: Gensig returns "" and Release does nothing, so
// the failure paths that have no generation need no special case.
type ServeGeneration struct {
	gensig string
	reg    *readerRegistration
}

// Gensig returns the signature of the prepared generation, or "" for a nil handle.
func (g *ServeGeneration) Gensig() string {
	if g == nil {
		return ""
	}
	return g.gensig
}

// claim returns the live registration this handle carries. It stays unexported:
// a caller outside this package has no way to honour a readerRegistration's
// lifetime, and the whole point of the handle is that the claim is transferred as
// one thing rather than reconstructed.
func (g *ServeGeneration) claim() *readerRegistration {
	if g == nil {
		return nil
	}
	return g.reg
}

// Release drops the claim without serving the generation, so a reaper may reclaim
// it. Call it on any path that prepared a generation and then decided not to open
// it; FinishServeOpen already does this for the caller on every path of its own.
// It is safe on a nil handle and safe to call more than once.
func (g *ServeGeneration) Release() {
	if g == nil {
		return
	}
	g.reg.Close()
}

// PrepareServeGeneration ensures a READY immutable generation for the current dump
// signature exists, claims it, and returns the handle FinishServeOpen then opens
// read-only. Every process is a builder — there is no build-leader election here —
// which is safe because a build is content-addressed and concurrency-safe (unique
// temp dir, first-to-rename wins).
//
// Order: compute GenSig -> reap abandoned .building-* temp dirs (best-effort) -> if
// a READY generation already exists (and no reindex) claim and use it -> else adopt
// an existing legacy flat cache by rename (no rebuild), or when nothing is
// adoptable — or reindex forces it — build a fresh generation. ctx is honoured
// between steps so shutdown aborts before starting heavy work; GenSig and the build
// are themselves synchronous and cannot be interrupted mid-flight.
//
// EVERY BRANCH THAT PRODUCES THE GENERATION CLAIMS IT BEFORE PUBLISHING IT. The
// build, the flat-cache adopt and the reindex rebuild all write their claim into
// the private temp directory and adopt it together with the generation, so none of
// them ever leaves a READY-and-unheld generation for a reaper to take. The one
// branch that cannot do this is the fast path, where the generation was already in
// the arena before this call: it has no private phase, so its claim is the ordinary
// post-adopt one and it can lose to a reaper. That residual case is stated where the
// primitive lives, in registerReader.
//
// A claim that cannot be WRITTEN is decided by claimOrServeUnprotected, not by this
// function, and it is never a refusal: the generation is served either way, and any
// arena whose unwritability is not the kernel's EROFS is reported to the user.
//
// The returned handle owns a live claim. The caller MUST either hand it to
// FinishServeOpen or Release it, or the generation stays pinned for the life of the
// process and is never reclaimed.
//
// This used to live in cmd/mcp-1c as prepareServeGeneration, alongside the caller
// rather than alongside the registry it has to talk to. It moved here because the
// claim it must now carry is a live handle with a heartbeat and a lifetime, and
// keeping the whole of that lifetime inside one package is what stops it becoming
// exported machinery that any caller could half-use.
func PrepareServeGeneration(ctx context.Context, dumpDir, cacheDir string, reindex bool) (*ServeGeneration, error) {
	gensig, err := GenSig(dumpDir)
	if err != nil {
		return nil, fmt.Errorf("computing dump signature for %s: %w", dumpDir, err)
	}

	// Reap abandoned .building-* temp generation dirs left by a builder that died
	// mid-build (SIGKILL / OOM / power loss) before adopting or rolling back — nothing
	// else reaps them. Best-effort: a reap error must never fail a serve open.
	if reaped, rErr := ReapStaleBuildDirs(dumpDir, cacheDir); rErr != nil {
		slog.Warn("serve index: stale build-dir reap failed", "dump", dumpDir, "error", rErr)
	} else if len(reaped) > 0 {
		slog.Info("serve index: reaped abandoned build temp dirs", "dump", dumpDir, "reaped", reaped)
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Read fast-path: a READY generation for this signature lets N processes coexist
	// read-only. Skipped while reindex forces a fresh build.
	if !reindex && GenerationReady(dumpDir, cacheDir, gensig) {
		return claimExistingGeneration(dumpDir, cacheDir, gensig)
	}

	// Adopt an existing legacy flat cache as a generation first (an O(shards) rename
	// that re-indexes nothing and reclaims the flat cache); only a genuinely cache-less
	// dump — or an explicit reindex — falls through to the full cold build.
	if !reindex {
		g, reg, migrated, adoptErr := migrateFlatToGeneration(dumpDir, cacheDir, withClaim)
		switch {
		case adoptErr != nil:
			slog.Warn("serve index: flat->generation adopt failed; falling back to a full build",
				"dump", dumpDir, "error", adoptErr)
		case migrated:
			slog.Info("serve index: adopted existing cache as generation without reindex",
				"dump", dumpDir, "gensig", g)
			return heldGeneration(g, reg)
		}
	}

	slog.Info("serve index: building generation", "dump", dumpDir, "gensig", gensig, "reindex", reindex)
	if reindex {
		// A reindex must FORCE a cold rebuild of the current signature even when the
		// dump content is unchanged (the operator is recovering a suspected-corrupt
		// cache). A plain build is content-addressed and no-ops on an already-READY
		// gensig, so it would rebuild nothing. forceRebuildGeneration drops the current
		// generation first — but only when no co-located process is still serving this
		// exact generation, so a concurrent reader is never yanked (in that case the
		// drop is skipped and the build no-ops, preserving the in-use generation).
		reg, rErr := forceRebuildGeneration(dumpDir, cacheDir, gensig, withClaim)
		if rErr != nil {
			return nil, fmt.Errorf("force-rebuilding dump generation for %s: %w", dumpDir, rErr)
		}
		return heldGeneration(gensig, reg)
	}
	reg, err := buildGeneration(dumpDir, cacheDir, gensig, withClaim)
	if err != nil {
		return nil, fmt.Errorf("building dump generation for %s: %w", dumpDir, err)
	}
	return heldGeneration(gensig, reg)
}

// claimExistingGeneration claims a generation that was ALREADY in the arena when
// this open started, i.e. one this process did not produce and therefore never had
// a private phase to claim it in. This is the post-adopt claim, and it can lose to
// a reaper that took the generation first.
//
// LOSING THE CLAIM DOES NOT REFUSE THE OPEN, in any of its forms — that is what
// claimOrServeUnprotected settles, and it never returns an error. What a failure
// costs is protection, not service: the generation is served, the state is logged
// at ERROR, and the user is told in the MCP tool response itself. Measured on the
// real binary when this DID refuse: a read-only cache produced "search: index build
// failed: claiming the existing generation ...: permission denied" on every start
// for ever, while the shipped v1.12.0 answered the same query from the same cache.
//
// A generation that was actually REAPED mid-claim is still caught, by the checks
// that can see it: FinishServeOpen re-tests the READY sentinel before attaching,
// and openCachedShards fails on the vanished shard directories.
func claimExistingGeneration(dumpDir, cacheDir, gensig string) (*ServeGeneration, error) {
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		return nil, fmt.Errorf("resolving the cache path for %s: %w", dumpDir, err)
	}
	return heldGeneration(gensig, claimOrServeUnprotected(generationDir(cpath, gensig)))
}

// heldGeneration wraps the registration a producing path came away with, and
// refuses when there is NONE AT ALL. A nil registration is not the same thing as a
// claimless one: a claimless registration is a decision this package made and
// reports, whereas nil means a producing path returned success while forgetting to
// say anything about the claim, and there is no honest notice to attach to that.
// Every producer either returns a registration or an error — claimOrServeUnprotected
// always returns one — so this is a guard against a future edit rather than a
// reachable state today, which is why it names what went wrong rather than
// trying to recover.
func heldGeneration(gensig string, reg *readerRegistration) (*ServeGeneration, error) {
	if reg == nil {
		slog.Error("dump: refusing to serve an index generation that was prepared without a reader claim; "+
			"another process could delete it while it is being served. Give this server its own cache "+
			"directory (MCP_1C_CACHE_DIR / --cache-dir), or make the cache directory writable.",
			"gen", gensig)
		return nil, fmt.Errorf("the prepared generation %s carries no reader claim, so it cannot be served",
			gensig)
	}
	return &ServeGeneration{gensig: gensig, reg: reg}, nil
}
