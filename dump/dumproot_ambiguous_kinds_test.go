package dump

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Kind names that an unrelated codebase can hold too.
//
// dumpDirNames is read by two things that want different answers from it. Key
// derivation wants EVERY kind, because a module under one of them has a Russian
// prefix whether or not the directory name is distinctive. Root detection wants
// only names that are EVIDENCE, and minKindDirsForRoot is 2, so any two names in
// one directory decide it.
//
// Three entries added for the prefix alone are ordinary English compounds, and a
// services tree holding two of them side by side was being called a dump root.
// ---------------------------------------------------------------------------

// TestOrdinaryEnglishKindNamesDoNotMakeAServicesTreeARoot is the false positive and
// its controls. The controls are what make the first half mean anything: the same
// threshold on ordinary kind directories must still fire, or this test would pass on
// a build where root detection was simply switched off.
func TestOrdinaryEnglishKindNamesDoNotMakeAServicesTreeARoot(t *testing.T) {
	// PREMISE: every excluded name is a real entry of the table it is excluded FROM.
	// A typo here would silently exclude nothing.
	var excluded []string
	for name := range ambiguousKindDirs {
		if _, ok := dumpDirNames[name]; !ok {
			t.Errorf("ambiguousKindDirs holds %q, which dumpDirNames does not, so the "+
				"exclusion applies to nothing", name)
		}
		excluded = append(excluded, name)
	}
	sort.Strings(excluded)
	if len(excluded) < 2 {
		t.Fatalf("ambiguousKindDirs holds %d names; with fewer than two, no combination "+
			"of them could reach minKindDirsForRoot = %d and this test measures nothing",
			len(excluded), minKindDirsForRoot)
	}

	// THE FALSE POSITIVE. Every pair of the excluded names, alone and beside the
	// directories an ordinary Go service actually has.
	for i := range excluded {
		for j := i + 1; j < len(excluded); j++ {
			pair := []string{excluded[i], excluded[j]}
			bare := writeKindDirs(t, filepath.Join(t.TempDir(), "svc"), pair...)
			if InspectDumpRoot(bare).IsRoot {
				t.Errorf("a directory holding only %v inspects as a dump root", pair)
			}
			mixed := writeKindDirs(t, filepath.Join(t.TempDir(), "svc"),
				append(append([]string{}, pair...), "cmd", "internal", "pkg")...)
			if InspectDumpRoot(mixed).IsRoot {
				t.Errorf("a services tree holding %v beside cmd/internal/pkg inspects as a "+
					"dump root", pair)
			}
		}
	}

	// CONTROL A, THE OTHER DIRECTION: the threshold still fires on ordinary kinds, so
	// the result above is these names being excluded and not root detection being
	// dead. Two kinds that are NOT excluded, chosen from the table itself.
	var plain []string
	for name := range dumpDirNames {
		if !ambiguousKindDirs[name] {
			plain = append(plain, name)
		}
	}
	sort.Strings(plain)
	if len(plain) < minKindDirsForRoot {
		t.Fatalf("only %d kind names are left unexcluded; the control cannot be built", len(plain))
	}
	ok := writeKindDirs(t, filepath.Join(t.TempDir(), "cfg"), plain[:minKindDirsForRoot]...)
	if !InspectDumpRoot(ok).IsRoot {
		t.Fatalf("control failed: a directory holding %v is not a dump root, so the kind "+
			"branch is not firing at all and the assertions above prove nothing",
			plain[:minKindDirsForRoot])
	}

	// CONTROL B: an excluded name does not stop a directory that qualifies anyway.
	// Exclusion must remove a VOTE, never veto a root.
	both := writeKindDirs(t, filepath.Join(t.TempDir(), "cfg"),
		append(append([]string{}, plain[:minKindDirsForRoot]...), excluded[0])...)
	if !InspectDumpRoot(both).IsRoot {
		t.Errorf("control failed: adding %q to a directory that was already a root stopped "+
			"it being one; the exclusion vetoes rather than abstains", excluded[0])
	}

	// CONTROL C: a manifest still decides on its own, whatever the directories are.
	man := writeKindDirs(t, filepath.Join(t.TempDir(), "cfg"), excluded[0])
	writeManifest(t, man, dumpManifestConfigDumpInfo)
	if !InspectDumpRoot(man).IsRoot {
		t.Errorf("control failed: a directory carrying %s is not a root; the exclusion "+
			"reached the manifest branch, which it must not",
			dumpManifestConfigDumpInfo)
	}
}

// TestExcludingAKindFromRootDetectionLeavesKeysAndTheAnchorScanAlone is the blast
// radius. The exclusion belongs to rootnessOf and nothing else: a module under one
// of these kinds still keys off its Russian prefix, and the anchor scan still
// recognises the kind as a root marker, because dumpRootMarker reads dumpDirNames
// and not this set.
func TestExcludingAKindFromRootDetectionLeavesKeysAndTheAnchorScanAlone(t *testing.T) {
	for name := range ambiguousKindDirs {
		ru, ok := dumpDirNames[name]
		if !ok {
			continue // reported by the test above
		}
		p := name + "/Объект1/Ext/ManagerModule.bsl"
		if got, want := bslPathToModuleName(p), ru+".Объект1.МодульМенеджера"; got != want {
			t.Errorf("%s keys as %q, want %q: the exclusion reached key derivation", p, got, want)
		}
		if !dumpRootMarker(name) {
			t.Errorf("dumpRootMarker(%q) = false: the exclusion reached the anchor scan", name)
		}
		wrapped := "wrapper/" + p
		if got := anchorIndex(strings.Split(wrapped, "/")); got != 1 {
			t.Errorf("anchorIndex(%q) = %d, want 1: the exclusion reached the anchor scan",
				wrapped, got)
		}
	}
}
