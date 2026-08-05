package dump

import (
	"path/filepath"
	"strings"
)

// Wrapped paths: making "the --dump path is not the dump root" a NUMBER instead of
// a guess.
//
// WHAT THIS ANSWERS. The anchor scan in bslPathToModuleName recovers the right key
// when a dump root is pointed at from one level too high, and collapsed_keys.go
// counts what a collision costs. Between them sits a case neither one reports: a
// path two levels above a SINGLE extension. The anchor scan finds the kind
// directory and derives a base-configuration key, so nothing collides and the
// collapse counter is silent; detectExtensionLayout looks one level down, finds a
// wrapper rather than a manifest, and produces no layout. The extension namespace
// simply disappears, every module of that extension is filed as though it belonged
// to the configuration, and both channels say nothing at all.
//
// WHAT IS COUNTED. A path is WRAPPED when the derivation had to skip leading
// segments that the extension layout did not account for, i.e. anchorIndex moved.
// That is a direct property of the tree, not an inference from one: at a correctly
// pointed root it is 0 on all 13575 paths of dumps/dump_bsl, and two levels above
// an extension it is every path in the dump.
//
// WHY IT IS NOT FOLDED INTO THE COLLAPSE REPORT. The two measure different things
// and can each be zero while the other is not. A collapse says content is
// unreachable; a wrap says names are wrong. A reader who is told only about
// collisions will conclude a silent index is a healthy one, which is exactly the
// case this file exists for.
//
// RECOMPUTED, NEVER PERSISTED, for the reason collapsed_keys.go gives at length:
// every load path already materialises the whole set of dump-relative paths
// (pathToDocID) before it finishes, so the number costs one pass and no on-disk
// format, and therefore no dumpIndexSchemaVersion bump.

// WrappedPathState is the whole wrap report, delivered as ONE value for the same
// reason CollapsedKeyState is: a caller composing a sentence must not be able to
// read the count from one load and the total from another.
type WrappedPathState struct {
	// Files is how many indexed files were keyed from a path carrying directory
	// levels above the dump root.
	Files int
	// Total is how many files the report was taken over, so a reader can tell
	// "three odd files" from "the whole dump".
	Total int
}

// wrapDepth reports how many leading segments the key derivation had to skip for
// relPath under this layout, or 0 when it skipped none.
//
// A segment the LAYOUT consumes is not a wrap: under the -AllExtensions shape the
// first segment is a recognised extension directory and the namespace accounts for
// it, so the question is asked of what is left.
func (l extensionLayout) wrapDepth(relPath string) int {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(l.byDir) > 0 && len(parts) >= 2 {
		if _, ok := l.byDir[parts[0]]; ok {
			return anchorIndex(parts[1:])
		}
	}
	return anchorIndex(parts)
}

// noteWrappedPaths publishes the report for the paths currently installed.
//
// It reads idx.pathToDocID rather than taking an argument, because that map is the
// one thing every load path fills with dump-RELATIVE paths: the two filesystem
// loaders write it as they walk, and the manifest loader writes it from the
// manifest, so a warm start reports the same number as a cold one. Callers may
// hold idx.mu; the store itself is atomic.
//
// THE LAYOUT COMES FROM idx.layout() AND NEVER FROM THE FIELD. That sentence above
// about a warm start reporting the same number as a cold one was FALSE for as long
// as this function read idx.extLayout directly: the field is filled by a sync.Once
// that only key derivation runs, and the warm paths derive no keys, so the warm
// measurement was taken against an empty layout and every segment the layout
// accounts for counted as a wrap. See the doc comment on Index.layout for the
// numbers; TestWrappedPaths_WarmAndReadOnlyStartsAgreeWithTheColdOne is what now
// holds the sentence shut.
func (idx *Index) noteWrappedPaths() {
	l := idx.layout()
	var st WrappedPathState
	st.Total = len(idx.pathToDocID)
	for rel := range idx.pathToDocID {
		if l.wrapDepth(rel) > 0 {
			st.Files++
		}
	}
	idx.wrapped.Store(&st)
}

// WrappedPaths returns the whole report in one atomic load.
//
// The zero value is the honest answer for an index that has not loaded yet: no
// wrapping has been OBSERVED. It is not a claim that none will be.
func (idx *Index) WrappedPaths() WrappedPathState {
	if idx == nil {
		return WrappedPathState{}
	}
	if p := idx.wrapped.Load(); p != nil {
		return *p
	}
	return WrappedPathState{}
}
