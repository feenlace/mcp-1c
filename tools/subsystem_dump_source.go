package tools

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
)

// This file is the offline adapter that lets analyze_subsystems and
// get_object_structure(object_type=Subsystem) answer from a configuration dump
// instead of the live 1C /subsystems and /object/Subsystem HTTP endpoints. It
// builds an onec.SubsystemForest / onec.ObjectStructure from the dump reader and
// injects it through the handlers' WithSource constructors, so ALL compute and
// formatting (computeOrphans / computeContaining / computeIntersections /
// formatObjectStructure) stays shared and byte-identical between the live and the
// dump path. When there is no dump the constructors return nil, selecting the live
// path unchanged.

// errDumpSubsystemPanic is the generic, path-free client-facing error a recovered
// panic in the dump parse is converted to: one malformed dump must never crash the
// process, and the panic value (which could embed a server path) must not reach the
// client. Customer-facing RU: no тире.
var errDumpSubsystemPanic = errors.New("не удалось обработать дамп подсистем")

// recoverToDumpError converts a panic raised inside a dump-source closure into the
// path-free sentinel error, so a single malicious or corrupt dump cannot crash the
// process or leak internals.
func recoverToDumpError(errp *error) {
	if r := recover(); r != nil {
		*errp = errDumpSubsystemPanic
	}
}

// recoverToStructError is the object_structure variant: it also forces handled to
// true, because the offline path owns the Subsystem type end to end (a recovered
// panic must not silently fall through to the live HTTP endpoint).
func recoverToStructError(obj *onec.ObjectStructure, handled *bool, errp *error) {
	if r := recover(); r != nil {
		*obj, *handled, *errp = onec.ObjectStructure{}, true, errDumpSubsystemPanic
	}
}

// DumpSubsystemForestFunc returns an offline SubsystemForestFunc backed by the
// configuration dump at dumpDir, or nil when dumpDir == "" so the analyze_subsystems
// handler keeps its live HTTP path, byte-for-byte identical to today. A parse error
// (or a recovered panic) is surfaced verbatim; the handler never falls back to live
// when a source is present (offline-when-dump-present).
func DumpSubsystemForestFunc(dumpDir string) onec.SubsystemForestFunc {
	if dumpDir == "" {
		return nil
	}
	return func(ctx context.Context) (forest onec.SubsystemForest, err error) {
		defer recoverToDumpError(&err)
		return buildDumpForest(ctx, dumpDir)
	}
}

// DumpObjectStructFunc returns an offline SubsystemStructFunc backed by the dump at
// dumpDir, or nil when dumpDir == "" so the get_object_structure handler stays fully
// live. The closure OWNS object_type == "Subsystem" (handled == true for every
// Subsystem outcome, success or error) and declines every other type
// (handled == false) so the handler serves those live: the dump has no attribute /
// tabular / enum reader.
func DumpObjectStructFunc(dumpDir string) onec.SubsystemStructFunc {
	if dumpDir == "" {
		return nil
	}
	return func(ctx context.Context, objectType, objectName string) (obj onec.ObjectStructure, handled bool, err error) {
		defer recoverToStructError(&obj, &handled, &err)
		if objectType != "Subsystem" {
			return onec.ObjectStructure{}, false, nil // fall through to live HTTP
		}
		subs, warnings, perr := dump.ParseAllSubsystemsCtx(ctx, dumpDir)
		if perr != nil {
			return onec.ObjectStructure{}, true, perr
		}
		return resolveDumpSubsystemStruct(convertDumpNodes(subs), objectName, warnings)
	}
}

// buildDumpForest parses the dump's subsystem tree and applied-object universe into
// the onec.SubsystemForest the analyze_subsystems handler consumes. The tree comes
// from the dump reader (Content already RU-canonical + sorted, drop diagnostics
// threaded into Warnings); the universe comes from the applied-kind enumerator. A
// missing Subsystems/ yields an empty tree (no error); an unreadable / empty
// universe yields empty AllObjects, which computeOrphans reports honestly.
func buildDumpForest(ctx context.Context, dumpDir string) (onec.SubsystemForest, error) {
	subs, warnings, err := dump.ParseAllSubsystemsCtx(ctx, dumpDir)
	if err != nil {
		return onec.SubsystemForest{}, err
	}
	return onec.SubsystemForest{
		Subsystems: convertDumpNodes(subs),
		AllObjects: dump.EnumerateAppliedObjects(dumpDir),
		Warnings:   warnings,
	}, nil
}

// convertDumpNodes maps the dump reader's subsystem tree onto the onec.SubsystemNode
// tree the handlers render, recursively (Path -> FullName, Children -> Subsystems).
func convertDumpNodes(subs []dump.Subsystem) []onec.SubsystemNode {
	if len(subs) == 0 {
		return nil
	}
	out := make([]onec.SubsystemNode, 0, len(subs))
	for _, s := range subs {
		out = append(out, onec.SubsystemNode{
			Name:       s.Name,
			FullName:   s.Path,
			Synonym:    s.Synonym,
			Content:    s.Content,
			Subsystems: convertDumpNodes(s.Children),
		})
	}
	return out
}

// resolveDumpSubsystemStruct resolves an object_structure(Subsystem) request against
// the converted tree, mirroring the live /object/Subsystem contract:
//
//   - objectName contains "." -> exact FullName match.
//   - otherwise               -> every node whose short Name equals objectName,
//     searched recursively.
//
// Exactly one match -> that subsystem's structure; more than one -> Ambiguous
// (sorted full paths); zero -> a "не найдена в дампе" error, EXCEPT when the parse
// dropped one or more subsystems (warnings present), in which case the drops are
// surfaced instead of masquerading as a clean 404 (the requested subsystem may be
// the one that failed to parse). handled is always true: the offline path owns the
// Subsystem type end to end.
func resolveDumpSubsystemStruct(nodes []onec.SubsystemNode, objectName string, warnings []string) (onec.ObjectStructure, bool, error) {
	byPath := strings.Contains(objectName, ".")

	var matches []onec.SubsystemNode
	var walk func([]onec.SubsystemNode)
	walk = func(ns []onec.SubsystemNode) {
		for _, n := range ns {
			if byPath {
				if n.FullName == objectName {
					matches = append(matches, n)
				}
			} else if n.Name == objectName {
				matches = append(matches, n)
			}
			walk(n.Subsystems)
		}
	}
	walk(nodes)

	switch len(matches) {
	case 0:
		if len(warnings) > 0 {
			return onec.ObjectStructure{Name: objectName, Warnings: warnings}, true, nil
		}
		return onec.ObjectStructure{}, true, errors.New("подсистема " + objectName + " не найдена в дампе")
	case 1:
		n := matches[0]
		return onec.ObjectStructure{
			Name:       n.Name,
			Synonym:    n.Synonym,
			Content:    n.Content,
			Subsystems: n.Subsystems,
			Warnings:   warnings,
		}, true, nil
	default:
		paths := make([]string, 0, len(matches))
		for _, m := range matches {
			paths = append(paths, m.FullName)
		}
		sort.Strings(paths)
		return onec.ObjectStructure{Ambiguous: paths, Warnings: warnings}, true, nil
	}
}
