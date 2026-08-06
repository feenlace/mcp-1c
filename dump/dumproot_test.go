package dump

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// dumproot_test.go covers the question a --dump path has to answer before the
// index is built: is this the root of a dump, and if it is not, is a root sitting
// one level below it.
//
// THE MEASUREMENT THAT SET THE THRESHOLD. A detector that fires on an ordinary
// directory is worse than no detector, so the kind-directory rule was measured
// against directories that are not dumps at all before a threshold was chosen.
// Counting immediate children that are dumpDirNames kinds:
//
//	$HOME                                   1   ("Documents")
//	$HOME/Documents, /Downloads, /Desktop   0
//	$HOME/GolandProjects and mcp under it   0
//	the mcp-1c-go checkout and its dump/    0
//	/usr/local, /usr, /etc, /, /Users       0
//	/Applications, /Library, /System        0
//	/opt, /private/tmp, /usr/share, /usr/lib 0
//
// Exactly one of them scores 1 and none scores 2. So at N=1 the detector fires on
// the user's home directory, and at N=2 it fires on none of them.
//
// THE SIZE OF THAT SWEEP AND THE SCORES OF THE REAL ROOTS ARE NOT WRITTEN DOWN,
// for the reason dump/dumproot.go:minKindDirsForRoot already gives after the same
// figure rotted there: it was a one-time walk over the paths of one machine,
// nothing in this tree re-derives it, and the dumps it scored are not all still on
// disk. What survives is what the tests below check on whatever machine they run
// on: $HOME must not inspect as a root, and the threshold and not luck must be
// what keeps it out.
//
// That last one is why the manifest branch is not optional: the extension dump at
// ~/Downloads/extdump_vm/mcp_service has exactly ONE kind directory (Languages)
// and would be missed by any threshold that keeps $HOME out. It carries a
// manifest, and the manifest is what recognises it. Neither branch alone is
// enough, which is the whole reason both exist.
//
// A directory whose entries cannot be read is not a dump root and not a parent of
// one, and is reported as neither: dumpPathFault in cmd/mcp-1c already says why a
// path is unusable, and a second guess from here would be a second answer to a
// question that already has one.

// writeKindDirs creates dir plus the named subdirectories under it.
func writeKindDirs(t *testing.T, dir string, names ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeManifest(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("<x/>"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDumpRootDetectionNeedsBothBranches pins the rule itself: a manifest OR
// enough kind directories, and neither alone covers the measured cases.
func TestDumpRootDetectionNeedsBothBranches(t *testing.T) {
	tests := []struct {
		name     string
		kinds    []string
		manifest string
		want     bool
	}{
		{"kinds alone are enough at the threshold",
			[]string{"Catalogs", "Documents"}, "", true},
		{"a full dump shape",
			[]string{"Catalogs", "Documents", "CommonModules", "Ext"}, "", true},
		{"one kind below the threshold is not a root on its own",
			[]string{"Languages"}, "", false},
		{"one kind WITH a manifest is a root: this is the real mcp_service shape",
			[]string{"Languages"}, "ConfigDumpInfo.xml", true},
		{"a manifest with no kind directory at all is still a root",
			nil, "Configuration.xml", true},
		{"the other manifest name works too",
			[]string{"Roles"}, "Configuration.xml", true},
		{"a home directory shape is NOT a root",
			[]string{"Documents", "Downloads", "Desktop", "Pictures", "Music"}, "", false},
		{"an empty directory is not a root",
			nil, "", false},
		{"non-kind directories do not count however many there are",
			[]string{"src", "internal", "cmd", "docs", "vendor", "testdata", "build"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeKindDirs(t, filepath.Join(t.TempDir(), "d"), tt.kinds...)
			if tt.manifest != "" {
				writeManifest(t, dir, tt.manifest)
			}
			got := InspectDumpRoot(dir)
			if got.IsRoot != tt.want {
				t.Errorf("InspectDumpRoot(%v, manifest=%q).IsRoot = %v, want %v",
					tt.kinds, tt.manifest, got.IsRoot, tt.want)
			}
		})
	}
}

// TestNestedRootsAreNamedAndNeverDescendedInto is the customer's shape: a parent
// holding two dump roots side by side.
func TestNestedRootsAreNamedAndNeverDescendedInto(t *testing.T) {
	parent := t.TempDir()
	writeKindDirs(t, filepath.Join(parent, "main"), "Catalogs", "Documents", "Ext")
	writeManifest(t, filepath.Join(parent, "main"), "ConfigDumpInfo.xml")
	writeKindDirs(t, filepath.Join(parent, "ext"), "Catalogs", "CommonModules")
	writeKindDirs(t, filepath.Join(parent, "notes"), "drafts")

	got := InspectDumpRoot(parent)
	if got.IsRoot {
		t.Fatalf("the parent must not be a root itself; it scored one")
	}
	if want := []string{"ext", "main"}; !slices.Equal(got.NestedRoots, want) {
		t.Errorf("NestedRoots = %v, want %v (sorted, and the non-dump sibling excluded)",
			got.NestedRoots, want)
	}
	// SORTED WHATEVER ORDER THE SCAN FOUND THEM IN, which is the only form of this
	// question that can be answered wrongly. os.ReadDir already returns entries
	// sorted by filename and NestedRoots is a filtered subsequence of them, so
	// slices.IsSorted over a real directory read is an assertion no build can fail,
	// including one with no sort in it at all. The reader is handed in reversed so
	// the scan really does collect the names the other way round.
	reversed := func(name string) ([]os.DirEntry, error) {
		ents, err := os.ReadDir(name)
		slices.Reverse(ents)
		return ents, err
	}
	if unsorted := inspectDumpRootWith(parent, reversed); !slices.Equal(unsorted.NestedRoots, []string{"ext", "main"}) {
		t.Errorf("NestedRoots = %v, want [ext main]: the list carries the order the "+
			"directory happened to be read in", unsorted.NestedRoots)
	}

	// Negative control: a parent whose children are NOT roots reports none, so the
	// assertion above is not satisfied by a detector that names every child.
	plain := t.TempDir()
	writeKindDirs(t, filepath.Join(plain, "a"), "x", "y")
	writeKindDirs(t, filepath.Join(plain, "b"), "z")
	if n := InspectDumpRoot(plain); n.IsRoot || len(n.NestedRoots) != 0 {
		t.Errorf("a parent of two ordinary directories reported IsRoot=%v NestedRoots=%v, want false and none",
			n.IsRoot, n.NestedRoots)
	}
}

// TestARootIsNotSearchedForNestedRoots pins the ordering that keeps the common
// case cheap AND keeps today's behaviour: a correctly pointed path is silent.
func TestARootIsNotSearchedForNestedRoots(t *testing.T) {
	dir := writeKindDirs(t, filepath.Join(t.TempDir(), "d"), "Catalogs", "Documents")
	// A root that also happens to contain a nested root below it.
	writeKindDirs(t, filepath.Join(dir, "inner"), "Catalogs", "Documents")

	got := InspectDumpRoot(dir)
	if !got.IsRoot {
		t.Fatalf("the directory is a root and was not recognised as one")
	}
	if len(got.NestedRoots) != 0 {
		t.Errorf("NestedRoots = %v, want none: a path that IS a root must stay silent, "+
			"which is the behaviour that shipped", got.NestedRoots)
	}
	if got.ReadDirs != 1 {
		t.Errorf("ReadDirs = %d, want 1: recognising a root must cost one directory read "+
			"and must not look below it", got.ReadDirs)
	}
}

// TestInspectionCostIsBoundedAndMeasured pins the budget as numbers rather than
// as a promise. It is the check that this never becomes a tree walk.
func TestInspectionCostIsBoundedAndMeasured(t *testing.T) {
	// The customer's shape: a parent with two children, one of them a big root.
	parent := t.TempDir()
	big := filepath.Join(parent, "main")
	kinds := []string{"Catalogs", "Documents", "CommonModules", "Enums", "Reports",
		"DataProcessors", "InformationRegisters", "AccumulationRegisters", "Ext"}
	writeKindDirs(t, big, kinds...)
	// Depth the inspection must NOT pay for.
	writeKindDirs(t, filepath.Join(big, "Catalogs", "Ном", "Ext"), "Form")
	writeKindDirs(t, filepath.Join(parent, "ext"), "Catalogs", "CommonModules")

	got := InspectDumpRoot(parent)
	if got.IsRoot {
		t.Fatalf("the parent is not a root")
	}
	// One read of the parent plus one of each of its two children.
	if got.ReadDirs != 3 {
		t.Errorf("ReadDirs = %d, want 3 (the path itself plus its two children)", got.ReadDirs)
	}
	wantEntries := 2 + len(kinds) + 2
	if got.Entries != wantEntries {
		t.Errorf("Entries = %d, want %d: the inspection is looking at more than the "+
			"immediate children of the path and of each child", got.Entries, wantEntries)
	}
	if got.Truncated {
		t.Errorf("Truncated = true on a two-child path; the budget is being spent wrongly")
	}

	// The depth control: the nested Catalogs/Ном/Ext tree exists and was never
	// read, or ReadDirs would exceed 3. Assert it really is there, so the number
	// above is a statement about restraint and not about an empty tree.
	if _, err := os.Stat(filepath.Join(big, "Catalogs", "Ном", "Ext", "Form")); err != nil {
		t.Fatalf("the depth this test claims not to walk does not exist: %v", err)
	}
}

// TestNestedScanIsTruncatedRatherThanUnbounded pins what happens on a directory
// with very many children: the scan stops and SAYS it stopped.
func TestNestedScanIsTruncatedRatherThanUnbounded(t *testing.T) {
	parent := t.TempDir()
	for i := 0; i < maxNestedRootScan+10; i++ {
		writeKindDirs(t, filepath.Join(parent, "child"+string(rune('a'+i%26))+string(rune('a'+i/26))), "junk")
	}
	got := InspectDumpRoot(parent)
	if !got.Truncated {
		t.Errorf("Truncated = false on a path with more than %d children", maxNestedRootScan)
	}
	if got.ReadDirs > maxNestedRootScan+1 {
		t.Errorf("ReadDirs = %d, want at most %d: the scan is not bounded",
			got.ReadDirs, maxNestedRootScan+1)
	}
}

// TestRealDumpsOnDiskAreRecognised runs the detector against the artefacts on this
// machine rather than against fixtures alone. A rule that only ever meets its own
// test data has not met a dump.
func TestRealDumpsOnDiskAreRecognised(t *testing.T) {
	cases := []struct {
		path        string
		wantRoot    bool
		wantNested  []string
		description string
	}{
		{"/Users/igoroot/GolandProjects/mcp/dumps/dump_bsl", true, nil,
			"13575 .bsl and no manifest at any depth: the kind branch alone carries it"},
		{"/Users/igoroot/GolandProjects/mcp/dumps/dump_2", true, nil,
			"a full ERP dump, 41 kind directories and a manifest"},
		{"/Users/igoroot/Downloads/extdump_vm/mcp_service", true, nil,
			"a real extension dump with ONE kind directory: only the manifest branch sees it"},
		{"/Users/igoroot/Downloads/extdump_vm", false,
			[]string{"FeenlaceMCPService", "mcp_service"},
			"two extension roots side by side, which is the customer's shape"},
	}
	for _, c := range cases {
		if _, err := os.Stat(c.path); err != nil {
			t.Skipf("artefact %s is absent on this machine", c.path)
		}
	}
	for _, c := range cases {
		t.Run(filepath.Base(c.path), func(t *testing.T) {
			got := InspectDumpRoot(c.path)
			if got.IsRoot != c.wantRoot {
				t.Errorf("IsRoot = %v, want %v (%s)", got.IsRoot, c.wantRoot, c.description)
			}
			if c.wantNested != nil && !slices.Equal(got.NestedRoots, c.wantNested) {
				t.Errorf("NestedRoots = %v, want %v", got.NestedRoots, c.wantNested)
			}
		})
	}
}

// TestHomeDirectoryIsNotADumpRoot is the false-positive control, run against the
// real home directory rather than a fixture imitating it. It is the single
// measured directory that scores at all, so it is the one that decides the
// threshold, and pinning it here means a later addition to dumpDirNames that
// happens to name a second common directory is caught by a test rather than by a
// customer seeing an alarm on their own home.
func TestHomeDirectoryIsNotADumpRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	got := InspectDumpRoot(home)
	if got.IsRoot {
		t.Errorf("InspectDumpRoot(%q).IsRoot = true. A detector that fires on the home "+
			"directory is worse than no detector; raise minKindDirsForRoot (now %d) or "+
			"reconsider the entry in dumpDirNames that made it match.", home, minKindDirsForRoot)
	}
	// And the reason it is not a root is the threshold, not an accident of the
	// machine: it really does contain a kind-named directory.
	ents, err := os.ReadDir(home)
	if err != nil {
		t.Skip("home directory is not readable")
	}
	kinds := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, ok := dumpDirNames[e.Name()]; ok {
			kinds++
		}
	}
	if kinds == 0 {
		t.Skipf("this home directory has no kind-named child at all, so it does not "+
			"exercise the threshold (measured elsewhere: %d)", 1)
	}
	if kinds >= minKindDirsForRoot {
		t.Errorf("the home directory has %d kind-named children and minKindDirsForRoot is %d, "+
			"so the only thing keeping it out is that IsRoot above happened to be false",
			kinds, minKindDirsForRoot)
	}
}
