package dump

import (
	"os"
	"path/filepath"
	"slices"
)

// Recognising the root of a dump, and the root that sits one level below the path
// somebody typed.
//
// THE DEFECT THIS ANSWERS. A customer pointed --dump at a directory holding two
// dump roots side by side, "main" and "ext", and the server took it in silence.
// The anchor scan in index.go now derives the right key for each module in such a
// tree, which is a real repair and is not the whole one: with a base configuration
// and an extension of it under one path, the extension's modules land exactly on
// the base configuration's keys and OVERWRITE them. Measured on the 13575-path
// corpus paired with a copy of itself under "ext/Имя/": 27150 files, 13575 distinct
// keys, worst bucket 2, 13575 files lost to overwrite. The anchor alone does not
// rescue that customer. Being told does.
//
// THE RULE, and BOTH HALVES ARE REQUIRED. A directory is a dump root when it
// carries a manifest (ConfigDumpInfo.xml or Configuration.xml) OR when enough of
// its immediate children are metadata kind directories. Measured on this machine:
// dumps/dump_bsl holds 13575 .bsl and no manifest at any depth, so manifest-only
// would miss it; ~/Downloads/extdump_vm/mcp_service is a real extension dump with
// exactly ONE kind directory, so a kind threshold high enough to keep an ordinary
// home directory out would miss it. Each branch covers what the other cannot.
//
// THE BEHAVIOUR IS TO TELL, NEVER TO DESCEND. If the path is itself a root the
// answer is silence, exactly as before. If it is not one but a child is, the child
// is NAMED and the operator chooses. Descending silently would repeat the very
// silence the customer complained about, one level lower and harder to see.
//
// THE COST IS ONE ReadDir PER CANDIDATE AND NOTHING ELSE. Both branches are
// decided from the entries that single read already returned: a manifest shows up
// in the listing, so testing for it costs no extra syscall. A path that IS a root
// costs exactly one read and stops. The inspection never opens a file, never
// descends past a child, and never walks a tree.

const (
	// dumpManifestConfigDumpInfo and dumpManifestConfiguration are the two files
	// DumpConfigToFiles writes at the top of its output. Either one present is
	// enough: an extension dump carries both (measured at 798 B and 316 B for
	// ConfigDumpInfo.xml on the two extension dumps under ~/Downloads/extdump_vm,
	// which are named rather than counted because they are not the only ones), and a
	// configuration's own can be very large (20 591 903 B in dumps/dump_2), which is
	// exactly why only their PRESENCE is consulted here and never their contents.
	dumpManifestConfigDumpInfo = "ConfigDumpInfo.xml"
	dumpManifestConfiguration  = "Configuration.xml"

	// minKindDirsForRoot is how many immediate children must be metadata kind
	// directories for a directory with no manifest to count as a dump root.
	//
	// TWO is measured, not chosen for looking reasonable. Counting kind-named
	// children over the ordinary directories of a developer machine ($HOME and
	// paths under it, the repository checkout, and the usual system roots), exactly
	// one scores at all: $HOME scores 1, because «Documents» is both an ordinary
	// home directory and a 1C metadata kind. None scores 2. So 1 fires on the
	// user's home directory and 2 fires on none of them.
	//
	// The size of that sweep is deliberately NOT written down here. It was a
	// one-time count over paths that belong to one machine, so nothing in this tree
	// can resolve it and a written figure could only rot; the previous one had
	// already stopped agreeing with the breakdown printed beside it. What survives
	// is the part a test can check, and dumproot_test.go checks it on whatever
	// machine it runs: $HOME must not inspect as a root, and it must be the
	// threshold rather than luck that keeps it out.
	//
	// The root that scores 1 is a real extension dump and is NOT sacrificed to this
	// threshold: it carries a manifest, and that is the branch that recognises it.
	minKindDirsForRoot = 2

	// maxNestedRootScan bounds how many children are examined when the path itself
	// is not a root. It exists because that branch is the only one whose cost grows
	// with the directory, and a --dump pointed at something like a downloads folder
	// would otherwise pay one read per child of a directory with thousands of them.
	// A dump root has tens of children, not hundreds, so a path needing more than
	// this to find a root below it is not a shape anybody is looking for.
	maxNestedRootScan = 64
)

// DumpRootInspection is what a --dump path turned out to be.
//
// The cost counters are part of the result rather than a debug aside. The claim
// this code makes is that it does not walk the tree, and a claim like that is
// worth exactly as much as the test that can measure it: ReadDirs and Entries are
// what let TestInspectionCostIsBoundedAndMeasured assert the budget as numbers.
type DumpRootInspection struct {
	// IsRoot reports whether the inspected path is itself a dump root.
	IsRoot bool
	// NestedRoots are the immediate children that are dump roots, sorted, and empty
	// whenever IsRoot is true. Names only, never paths: the caller knows the parent.
	NestedRoots []string
	// Truncated reports that the child scan stopped at maxNestedRootScan, so
	// NestedRoots may be short. It is carried rather than dropped because "no root
	// below this" and "no root among the first 64" are different answers.
	Truncated bool
	// RootIsSymlink reports that the inspected path is itself a symbolic link.
	//
	// IT IS NOT A DETAIL, IT IS THE DIFFERENCE BETWEEN A CORRECT PATH AND AN EMPTY
	// INDEX. os.ReadDir follows the link, so everything else in this struct
	// describes the TARGET and can report a perfectly good dump root; the indexer
	// does not, because filepath.WalkDir lstats its root and a symlink is not a
	// directory, so the walk ends before it visits one file. Measured: a symlink to
	// a two-module dump inspects as IsRoot and indexes 0 of 2 modules. Saying
	// nothing about it is declaring a path correct that indexes nothing at all.
	//
	// The walk is not changed here. Making it descend means resolving the dump
	// directory once, at index creation, and that path feeds filepath.Rel, the
	// containment check and the generation signature alike; it is a change to what a
	// dump root IS, not a diagnostic. So the diagnosis is what this carries, and the
	// remedy it points at is the real path.
	RootIsSymlink bool
	// SymlinkedChildren is how many immediate children are symbolic links.
	//
	// They are invisible to both sides and invisible in the same way: os.ReadDir
	// reports a symlink as a non-directory, so a symlinked child that is a dump root
	// is never listed in NestedRoots, and WalkDir does not descend into one either.
	// The two agree, and both stay quiet, which is why the count is carried.
	SymlinkedChildren int
	// ReadDirs and Entries are what the inspection actually spent.
	ReadDirs int
	Entries  int
}

// rootnessOf decides both branches from one already-read listing, and returns the
// child directories in the order the listing gave them.
func rootnessOf(ents []os.DirEntry) (isRoot bool, childDirs []string) {
	kinds := 0
	manifest := false
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() {
			if name == dumpManifestConfigDumpInfo || name == dumpManifestConfiguration {
				manifest = true
			}
			continue
		}
		childDirs = append(childDirs, name)
		if _, ok := dumpDirNames[name]; ok {
			kinds++
		}
	}
	return manifest || kinds >= minKindDirsForRoot, childDirs
}

// InspectDumpRoot answers whether dir is a dump root and, when it is not, which of
// its immediate children are.
//
// An unreadable dir returns the zero value: it is neither a root nor the parent of
// one, and saying anything else would be a second answer to a question
// dumpPathFault in cmd/mcp-1c already answers.
func InspectDumpRoot(dir string) DumpRootInspection {
	return inspectDumpRootWith(dir, os.ReadDir)
}

// inspectDumpRootWith is InspectDumpRoot with its directory reader passed in.
//
// IT EXISTS SO THE SORT AT THE END CAN BE MEASURED. os.ReadDir returns entries
// already sorted by filename, and NestedRoots is a filtered subsequence of them, so
// wired straight to os.ReadDir the sort can never be OBSERVED to do anything: an
// assertion that the list comes back sorted holds identically on a build with no
// sort in it, and did. A reader handed in as an argument, rather than a package
// variable a test swaps, keeps that seam free of shared mutable state.
//
// The sort is kept rather than deleted because os.ReadDir's ordering is the only
// thing supplying it. A later change to a budgeted read (os.File.ReadDir(n), which
// makes no ordering promise) would take that away silently, and this is the line
// that would still be right.
func inspectDumpRootWith(dir string, readDir func(string) ([]os.DirEntry, error)) DumpRootInspection {
	var got DumpRootInspection

	// LSTAT BEFORE READDIR, because ReadDir would follow the link and hide the one
	// fact the indexer disagrees with this inspection about.
	if lst, err := os.Lstat(dir); err == nil && lst.Mode()&os.ModeSymlink != 0 {
		got.RootIsSymlink = true
	}

	ents, err := readDir(dir)
	if err != nil {
		return got
	}
	got.ReadDirs = 1
	got.Entries = len(ents)
	for _, e := range ents {
		if e.Type()&os.ModeSymlink != 0 {
			got.SymlinkedChildren++
		}
	}

	isRoot, childDirs := rootnessOf(ents)
	if isRoot {
		// A correctly pointed path costs one read and says nothing. This early
		// return is the behaviour, not an optimisation: a root that happens to
		// contain another root below it is still the root the operator chose.
		got.IsRoot = true
		return got
	}

	scanned := 0
	for _, name := range childDirs {
		if scanned == maxNestedRootScan {
			got.Truncated = true
			break
		}
		scanned++
		childEnts, err := readDir(filepath.Join(dir, name))
		if err != nil {
			continue // unreadable child: not a root as far as anything here can tell
		}
		got.ReadDirs++
		got.Entries += len(childEnts)
		if childIsRoot, _ := rootnessOf(childEnts); childIsRoot {
			got.NestedRoots = append(got.NestedRoots, name)
		}
	}
	// Sorted, because os.ReadDir's order is the filesystem's and a list that
	// reordered between two identical runs would read as a changing dump.
	slices.Sort(got.NestedRoots)
	return got
}
