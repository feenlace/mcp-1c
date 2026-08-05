package dump

import "slices"

// Collapsed keys: making a silent loss of module content countable.
//
// WHAT COLLAPSES. Every loader turns a dump-relative path into a module name via
// bslPathToModuleName and uses that name as the key of plain maps
// (contentByName[name], pathByName[name], pathToDocID's value). Two files
// deriving one name is therefore not a warning and not an error: the second write
// wins and the first file's content becomes unreachable through every read path
// the index has. Meanwhile idx.names keeps BOTH entries, so ModuleCount() reports
// the number of files walked and nothing reports the number that can still be
// read. The count lies, quietly, in the direction that flatters the server.
//
// The observed cause is a dump root pointed one level too high; the anchor scan
// in bslPathToModuleName removes that cause for every kind this package knows.
// This file is the OTHER half, and it is not redundant with the fix: it measures
// the outcome rather than predicting it, so a collapse arriving through a cause
// nobody has thought of is still counted.
//
// WHY IT IS EXACT AND NOT A RATIO. The first proposal was to flag a dump when
// distinct*2 < total. Arithmetic refutes it: a half-shifted tree yields 6793
// distinct keys over 13575 files, and 6793*2 = 13586 is not less than 13575, so
// the rule stays silent by a margin of 11 while 6782 files have already lost
// their content. Every threshold over the same two totals has a hole of that
// shape. What is counted here is the overwrites themselves.
//
// WHY IT IS RECOMPUTED AND NEVER PERSISTED. Nothing on disk needs to change, and
// that is a deliberate conclusion rather than an omission. Every load path
// already materialises the whole multiset of module names in memory before it
// finishes: the filesystem loaders derive one name per .bsl they actually load
// (loadBSLFiles skips a file it cannot stat or read, and such a file is absent
// from idx.names and from the maps alike, so the two stay consistent), and the
// manifest-backed loaders read one DocID per manifest entry. The duplicates are
// therefore present at the moment the load ends, and deriving the number costs
// one pass. Persisting it would put a new field in the generation manifest, which
// is the on-disk format the BUMP PROTOCOL in generation.go governs: that would
// force dumpIndexSchemaVersion up and invalidate every warm generation on every
// installation, to carry a number that can be recomputed for free. That reasoning
// is about THIS report and is unaffected by anything below it: a recomputed number
// needs no format, so it needs no bump.
//
// dumpIndexSchemaVersion HAS since gone from 3 to 4, and it was not this file that
// moved it. Two later changes did: dumpDirNames gained the kinds a configuration
// declares, and the wrongly rooted user whose collapsed DocIDs are PERSISTED in a
// generation manifest would otherwise never receive the anchor fix at all. The
// argument for that bump lives at dumpIndexSchemaVersion in generation.go and the
// proof of it in cache_invalidation_test.go; what is said here remains what it
// said, which is that the collapse REPORT costs no bump of its own.

// collapsedKeySampleLimit caps how many colliding module names are carried for
// display. The COUNTS are never capped: a report that rounded off the number of
// lost files would be the same defect as saying nothing. The sample exists so the
// message can name what collided without pasting thousands of lines into an MCP
// response that a client would truncate anyway.
const collapsedKeySampleLimit = 5

// CollapsedKeyState is the whole collapse report, delivered as ONE value.
//
// It is a struct and not three accessors for the reason UnprotectedState is: a
// caller composing a sentence must not be able to read the count from one load
// and the names from another. A reload can swap the attached generation between
// two calls, and a message built from both halves would then describe no dump
// that ever existed.
type CollapsedKeyState struct {
	// Files is how many dump files lost their content to an overwrite: the number
	// of module names assigned more than once, counted with multiplicity, which is
	// total assignments minus distinct names. It is the number of modules the
	// index counts but can no longer serve.
	Files int
	// Keys is how many distinct module names more than one file derived. It is
	// always <= Files and is what the sample below is drawn from.
	Keys int
	// Sample is up to collapsedKeySampleLimit of those names, sorted. Sorted and
	// not first-seen, because Go map iteration order is randomised and a sample
	// that changed between two identical loads would look like a changing dump.
	Sample []string
}

// collapsedKeysOf derives the report from a name slice. It is a pure function of
// its argument so it can be checked against hand-counted inputs without building
// an Index, and so every caller gets the same arithmetic.
func collapsedKeysOf(names []string) CollapsedKeyState {
	if len(names) == 0 {
		return CollapsedKeyState{}
	}
	seen := make(map[string]int, len(names))
	for _, n := range names {
		seen[n]++
	}
	var st CollapsedKeyState
	var colliding []string
	for n, c := range seen {
		if c > 1 {
			st.Files += c - 1
			st.Keys++
			colliding = append(colliding, n)
		}
	}
	if len(colliding) == 0 {
		return CollapsedKeyState{}
	}
	slices.Sort(colliding)
	if len(colliding) > collapsedKeySampleLimit {
		colliding = colliding[:collapsedKeySampleLimit]
	}
	st.Sample = colliding
	return st
}

// noteCollapsedKeys publishes the report for a freshly installed name slice.
//
// It is called where a name slice is INSTALLED rather than on every read, because
// deriving it per call would put an O(modules) map build on every tool response,
// and the answer only changes when the index is loaded or a generation is
// swapped. The failure mode of that choice — a loader added later that forgets to
// call this and leaves the report describing the previous load — is held shut by
// the census in collapsed_keys_test.go, which fails the build when a function
// starts writing idx.names without declaring whether it must record.
//
// Callers may hold idx.mu; the store itself is atomic, so a reader never takes
// the lock for it. That matters because the reader here is every MCP tool
// response.
func (idx *Index) noteCollapsedKeys(names []string) {
	st := collapsedKeysOf(names)
	idx.collapsed.Store(&st)
	// The wrap report is published HERE and nowhere else, deliberately.
	//
	// It is a second measurement of the same freshly installed state, and it was
	// briefly a second call beside this one at each install point. That is a fifth
	// copy of a rule waiting to fall out of step: a loader added later would call
	// one and forget the other, and the census in collapsed_keys_test.go, which
	// polices callers of THIS function, would not notice. Folding it in makes that
	// census cover both, so the two reports cannot describe different loads.
	//
	// It reads idx.pathToDocID, so every caller must have installed that map before
	// getting here. Reload publishes its generation inside one critical section and
	// this call sits after the assignment for exactly that reason.
	idx.noteWrappedPaths()
}

// CollapsedKeys returns the whole report in one atomic load.
//
// The zero value is the honest answer for an index that has not loaded yet: no
// collapse has been OBSERVED. It is not a claim that none will be.
func (idx *Index) CollapsedKeys() CollapsedKeyState {
	if idx == nil {
		return CollapsedKeyState{}
	}
	if p := idx.collapsed.Load(); p != nil {
		return *p
	}
	return CollapsedKeyState{}
}

// CollapsedKeyCount returns the number of dump FILES whose content was lost to an
// overwrite, i.e. CollapsedKeys().Files.
//
// The name says "keys" and the number counts files, and that mismatch is
// deliberate rather than sloppy: what a reader needs to act on is how much of the
// dump the server cannot serve, not how many names happen to be duplicated. Use
// CollapsedKeys().Keys for the latter.
func (idx *Index) CollapsedKeyCount() int {
	return idx.CollapsedKeys().Files
}
