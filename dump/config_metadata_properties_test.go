package dump

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// snapshotFile / snapshotOriginFile name the in-repo ground truth for 1C metadata
// collection names: the property set of the platform type ОбъектМетаданныхКонфигурация,
// i.e. exactly the names it is legal to index Метаданные[...] by. Both files are
// generated from the 1C syntax corpus; the origin file records where from.
const (
	snapshotFile       = "config_metadata_properties.txt"
	snapshotOriginFile = "config_metadata_properties.origin.txt"
)

// collectionAddRe matches every string added to either metadata-collection list a BSL
// handler feeds to Метаданные[ИмяКоллекции]: ИменаКоллекций (МетаданныеGET, the raw
// metadata tree) and ПрикладныеКоллекции (ПодсистемыGET, the orphans universe).
var collectionAddRe = regexp.MustCompile(`(?:ПрикладныеКоллекции|ИменаКоллекций)\.Добавить\("([^"]+)"\)`)

// TestBSLCollectionNamesAreRealMetadataProperties is the correctness guard the
// cross-file drift check (TestBSLUniverseInSyncWithGo) cannot provide. That check only
// compares two of our own copies with each other, so it stays green while BOTH copies
// carry the same wrong string; it is also blind to ИменаКоллекций, which it does not
// parse at all. This test compares every collection literal we ship against ground
// truth instead: a name that is not a real property of ОбъектМетаданныхКонфигурация
// makes Метаданные[name] throw at runtime, and the WHOLE collection silently vanishes
// from the response.
//
// The check is deliberately ONE-WAY (our literals must be in the snapshot, not the
// reverse): the type has 141 properties and we enumerate a chosen subset of them.
func TestBSLCollectionNamesAreRealMetadataProperties(t *testing.T) {
	snapshot := loadMetadataPropertySnapshot(t)

	for _, bsl := range bslCollectionSources(t) {
		data, err := os.ReadFile(bsl.path)
		if err != nil {
			t.Fatalf("cannot read %s (it must ship in this repo): %v", bsl.label, err)
		}
		names, arrays := parseCollectionAdds(string(data))
		if len(names) == 0 {
			t.Fatalf("%s: no collection names extracted; the regex or the file structure changed, "+
				"so this guard would pass vacuously", bsl.label)
		}
		for _, want := range bsl.arrays {
			if !arrays[want] {
				t.Fatalf("%s: array %s produced no entries; it was renamed or removed, "+
					"so this guard no longer covers it", bsl.label, want)
			}
		}
		for _, name := range names {
			if snapshot[name] {
				continue
			}
			t.Errorf("%s: collection %q is NOT a property of ОбъектМетаданныхКонфигурация in %s.\n"+
				"Two possible causes, and they need different fixes:\n"+
				"  1. the literal is wrong: Метаданные[%q] throws at runtime and the whole collection "+
				"disappears from the response (this is the bug this guard exists for);\n"+
				"  2. the snapshot is stale: the property is real but newer than the corpus recorded in %s, "+
				"so the snapshot must be regenerated from that source.\n"+
				"Resolve it by looking the name up in the source named by the origin file. Never hand-add a "+
				"name to the snapshot to silence this.",
				bsl.label, name, snapshotFile, name, snapshotOriginFile)
		}
	}
}

// TestUniverseServiceKindCollectionsAreRealMetadataProperties pins the Go side of the
// same invariant directly, without parsing any source: every universeServiceKinds
// bslCollection names a live Метаданные[...] property. TestBSLUniverseInSyncWithGo then
// keeps the BSL list and this table equal, so the two guards together mean neither side
// can hold a name the platform would reject.
func TestUniverseServiceKindCollectionsAreRealMetadataProperties(t *testing.T) {
	snapshot := loadMetadataPropertySnapshot(t)

	for _, k := range universeServiceKinds {
		if snapshot[k.bslCollection] {
			continue
		}
		t.Errorf("universeServiceKinds[%s].bslCollection = %q, which is NOT a property of "+
			"ОбъектМетаданныхКонфигурация in %s. Either the literal is wrong (the live path loses that "+
			"whole collection) or the snapshot is stale, see %s.",
			k.singularEn, k.bslCollection, snapshotFile, snapshotOriginFile)
	}
}

// TestMetadataPropertySnapshotIsWellFormed proves the snapshot is a usable oracle: it
// is non-empty, sorted, duplicate-free, and holds exactly the number of properties its
// origin file records. A truncated or hand-appended snapshot would otherwise weaken
// every guard above without failing anything.
func TestMetadataPropertySnapshotIsWellFormed(t *testing.T) {
	lines := readSnapshotLines(t)

	if len(lines) == 0 {
		t.Fatalf("%s is empty", snapshotFile)
	}
	if !sort.StringsAreSorted(lines) {
		t.Errorf("%s is not sorted; it is generated output and must stay canonical", snapshotFile)
	}
	seen := make(map[string]bool, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l) != l || l == "" {
			t.Errorf("%s line %d: %q is blank or padded", snapshotFile, i+1, l)
		}
		if seen[l] {
			t.Errorf("%s line %d: duplicate entry %q", snapshotFile, i+1, l)
		}
		seen[l] = true
	}

	origin := loadSnapshotOrigin(t)
	declared, err := strconv.Atoi(origin["snapshot_properties"])
	if err != nil {
		t.Fatalf("%s: snapshot_properties is not a number (%q): %v", snapshotOriginFile, origin["snapshot_properties"], err)
	}
	if declared != len(lines) {
		t.Errorf("%s holds %d names but %s declares snapshot_properties=%d; one of the two was edited alone",
			snapshotFile, len(lines), snapshotOriginFile, declared)
	}
	for _, key := range []string{"source_repo", "source_path", "source_bytes", "source_sha256"} {
		if origin[key] == "" {
			t.Errorf("%s: missing provenance field %q; a snapshot whose source cannot be named cannot be "+
				"shown to be stale", snapshotOriginFile, key)
		}
	}
}

// bslSource is one in-repo BSL file carrying metadata-collection literals, plus the
// arrays it is required to contain (so a renamed array fails loudly instead of
// shrinking this guard's coverage in silence).
type bslSource struct {
	label  string
	path   string
	arrays []string
}

// bslCollectionSources lists every BSL file in this repo that indexes Метаданные by a
// literal collection name: the shipped extension module and the documentation copy
// users are told to paste into their own configuration.
func bslCollectionSources(t *testing.T) []bslSource {
	t.Helper()
	return []bslSource{
		{
			label:  "extension/src/HTTPServices/MCPService/Ext/Module.bsl",
			path:   moduleBSLPath(t),
			arrays: []string{"ИменаКоллекций", "ПрикладныеКоллекции"},
		},
		{
			label:  "docs/bsl/metadata.bsl",
			path:   repoPath(t, "docs", "bsl", "metadata.bsl"),
			arrays: []string{"ИменаКоллекций"},
		},
	}
}

// parseCollectionAdds returns the distinct collection literals in src and the set of
// array names they were added to.
func parseCollectionAdds(src string) (names []string, arrays map[string]bool) {
	arrays = map[string]bool{}
	seen := map[string]bool{}
	for _, m := range collectionAddRe.FindAllStringSubmatch(src, -1) {
		arrays[strings.SplitN(m[0], ".", 2)[0]] = true
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		names = append(names, m[1])
	}
	return names, arrays
}

// loadMetadataPropertySnapshot returns the ground-truth property set.
func loadMetadataPropertySnapshot(t *testing.T) map[string]bool {
	t.Helper()
	lines := readSnapshotLines(t)
	set := make(map[string]bool, len(lines))
	for _, l := range lines {
		set[l] = true
	}
	if len(set) == 0 {
		t.Fatalf("%s yielded no property names", snapshotFile)
	}
	return set
}

// readSnapshotLines reads the snapshot as a list of lines, dropping only the trailing
// newline. It does NOT skip blanks or comments: the snapshot is pure generated data and
// anything else in it is a defect the well-formedness test must see.
func readSnapshotLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(repoPath(t, "dump", "testdata", snapshotFile))
	if err != nil {
		t.Fatalf("reading %s: %v", snapshotFile, err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// loadSnapshotOrigin parses the provenance file's key=value lines ('#' starts a comment).
func loadSnapshotOrigin(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(repoPath(t, "dump", "testdata", snapshotOriginFile))
	if err != nil {
		t.Fatalf("reading %s: %v", snapshotOriginFile, err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Errorf("%s: line %q is neither a comment nor key=value", snapshotOriginFile, line)
			continue
		}
		out[k] = v
	}
	return out
}

// repoPath resolves a path relative to the repository root, derived from THIS test
// file's location, so the checks are independent of the working directory.
func repoPath(t *testing.T, parts ...string) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate the repository root")
	}
	return filepath.Join(append([]string{filepath.Dir(self), ".."}, parts...)...)
}
