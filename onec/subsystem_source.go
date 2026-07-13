package onec

import "context"

// SubsystemForestFunc is an optional offline source for the subsystem forest that
// analyze_subsystems normally fetches from the live 1C extension over the
// /subsystems endpoint. When a non-nil value is passed to
// NewAnalyzeSubsystemsHandlerWithSource, the handler calls it instead of making
// the HTTP request, so the same forest can be served from an offline config dump.
// A nil value selects the live HTTP path, byte-for-byte identical to the handler
// built by the plain NewAnalyzeSubsystemsHandler constructor.
type SubsystemForestFunc func(ctx context.Context) (SubsystemForest, error)

// SubsystemStructFunc is an optional offline source for get_object_structure.
// When a non-nil value is passed to NewObjectStructureHandlerWithSource, the
// handler consults it first: if it reports handled==true, the (obj, err) it
// returns is used verbatim (offline path, no HTTP); if it reports handled==false,
// the handler falls through to the live /object/<type>/<name> request. This lets
// object_structure serve one metadata type (e.g. Subsystem) from an offline dump
// while every other type stays live. A nil value selects the live HTTP path for
// all types, byte-for-byte identical to the handler built by the plain
// NewObjectStructureHandler constructor.
type SubsystemStructFunc func(ctx context.Context, objectType, objectName string) (obj ObjectStructure, handled bool, err error)
