package dump

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// NFD/NFC building blocks, constructed from explicit rune values so the test
// exercises the real decomposed byte sequences regardless of how this source
// file is normalised on disk (NFD and NFC glyphs are visually identical).
var (
	tnfdCapShortI   = string([]rune{0x0418, 0x0306}) // NFD capital short I (base + breve)
	tnfdSmallShortI = string([]rune{0x0438, 0x0306}) // NFD small short I   (base + breve)
	tnfdCapIo       = string([]rune{0x0415, 0x0308}) // NFD capital IO      (base + diaeresis)
	tnfdSmallIo     = string([]rune{0x0435, 0x0308}) // NFD small IO        (base + diaeresis)

	tnfcCapShortI   = string(rune(0x0419)) // precomposed capital short I
	tnfcSmallShortI = string(rune(0x0439)) // precomposed small short I
	tnfcCapIo       = string(rune(0x0401)) // precomposed capital IO
	tnfcSmallIo     = string(rune(0x0451)) // precomposed small IO
)

func TestNFC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"NFD capital short I -> NFC", tnfdCapShortI, tnfcCapShortI},
		{"NFD small short I -> NFC", tnfdSmallShortI, tnfcSmallShortI},
		{"NFD capital IO -> NFC", tnfdCapIo, tnfcCapIo},
		{"NFD small IO -> NFC", tnfdSmallIo, tnfcSmallIo},
		{
			"already NFC unchanged",
			tnfcCapShortI + tnfcSmallShortI + tnfcCapIo + tnfcSmallIo,
			tnfcCapShortI + tnfcSmallShortI + tnfcCapIo + tnfcSmallIo,
		},
		{"ASCII unchanged", "Documents/Ext/ObjectModule.bsl", "Documents/Ext/ObjectModule.bsl"},
		{"empty", "", ""},
		{"mixed NFC and NFD", tnfcCapShortI + tnfdSmallIo, tnfcCapShortI + tnfcSmallIo},
		// A breve on a non-target base (ASCII 'a') must NOT be recomposed: the
		// replacer is scoped to exactly the four Cyrillic sequences.
		{"unrelated combining mark untouched", "a" + string(rune(0x0306)), "a" + string(rune(0x0306))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NFC(tt.in); got != tt.want {
				t.Errorf("NFC(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNFCFastPathNoAlloc(t *testing.T) {
	// Already-NFC input (no combining marks): the prod/Windows/HTTP case must hit
	// the allocation-free fast path and return the input unchanged.
	in := "Documents." + tnfcSmallShortI // precomposed short I, still NFC
	var out string
	allocs := testing.AllocsPerRun(1000, func() { out = NFC(in) })
	if out != in {
		t.Fatalf("NFC mutated already-NFC input: %q -> %q", in, out)
	}
	if allocs > 0 {
		t.Errorf("NFC fast path allocated %v times for NFC input, want 0", allocs)
	}
}

// TestIndex_NFDPathResolves is the core regression for the macOS bug: a dump
// whose on-disk object name is NFD must index under an NFC key and resolve via
// GetContent / PathIndex, while the file itself still opens from its raw path.
func TestIndex_NFDPathResolves(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()

	// "Тестовый" with the trailing short I built explicitly: NFD on disk, NFC for
	// the expected key.
	objBase := string([]rune{0x0422, 0x0435, 0x0441, 0x0442, 0x043e, 0x0432, 0x044b}) // base, no short I
	objNFD := objBase + tnfdSmallShortI
	objNFC := objBase + tnfcSmallShortI

	relPath := "Documents/" + objNFD + "/Ext/ObjectModule.bsl"
	mkBSLFile(t, dumpDir, relPath, "// module under test\n")

	// Precondition: the filesystem must have preserved the NFD bytes (true on
	// APFS/HFS for names written verbatim). If a normalising FS recomposed the
	// name there is no bug to reproduce here, so skip rather than give a false pass.
	if !onDiskNameIsNFD(t, filepath.Join(dumpDir, "Documents")) {
		t.Skip("filesystem normalised the NFD directory name; cannot reproduce the macOS NFD case here")
	}

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	// The chokepoint NFC-normalises, so the key stored for the NFD path must equal
	// the key derived from the NFC path. Derive it from the function under test.
	wantDocID := bslPathToModuleName("Documents/" + objNFC + "/Ext/ObjectModule.bsl")
	if strings.ContainsRune(wantDocID, 0x0306) || strings.ContainsRune(wantDocID, 0x0308) {
		t.Fatalf("expected an NFC docID, but it still contains a combining mark: %q", wantDocID)
	}
	if !strings.Contains(wantDocID, objNFC) {
		t.Fatalf("expected docID %q to contain the recomposed object name %q", wantDocID, objNFC)
	}

	// The stored key must be NFC despite the NFD on-disk path.
	names := idx.ModuleNames()
	if !slices.Contains(names, wantDocID) {
		t.Fatalf("indexed names %v do not contain NFC docID %q", names, wantDocID)
	}
	for _, n := range names {
		if strings.ContainsRune(n, 0x0306) || strings.ContainsRune(n, 0x0308) {
			t.Errorf("indexed name is not NFC (carries a combining mark): %q", n)
		}
	}

	// GetContent with the NFC docID resolves: the key is NFC, while the file is
	// opened via the raw on-disk path stored as the map value.
	if _, ok := idx.GetContent(wantDocID); !ok {
		t.Errorf("GetContent(NFC %q) = not found, want found", wantDocID)
	}

	// GetContent with an NFD docID also resolves (defensive query-side NFC).
	nfdDocID := strings.ReplaceAll(wantDocID, tnfcSmallShortI, tnfdSmallShortI)
	if nfdDocID == wantDocID {
		t.Fatalf("failed to build an NFD variant of %q for the query-side test", wantDocID)
	}
	if _, ok := idx.GetContent(nfdDocID); !ok {
		t.Errorf("GetContent(NFD %q) = not found, want found (query-side NFC)", nfdDocID)
	}

	// PathIndex.Filter returns the module by its NFC object name...
	dotParts := strings.Split(wantDocID, ".")
	if len(dotParts) < 3 {
		t.Fatalf("unexpected docID shape: %q", wantDocID)
	}
	category, objectName := dotParts[0], dotParts[1]
	if got := idx.pathIndex.Filter(category, objectName, ""); !containsDocID(got, wantDocID) {
		t.Errorf("PathIndex.Filter(NFC) did not return %q; got %v", wantDocID, docIDsOf(got))
	}
	// ...and by its NFD object name (defensive query-side NFC).
	nfdObjectName := strings.ReplaceAll(objectName, tnfcSmallShortI, tnfdSmallShortI)
	if got := idx.pathIndex.Filter(category, nfdObjectName, ""); !containsDocID(got, wantDocID) {
		t.Errorf("PathIndex.Filter(NFD) did not return %q; got %v", wantDocID, docIDsOf(got))
	}
	// Contains resolves an NFD docID too.
	if !idx.pathIndex.Contains(nfdDocID) {
		t.Errorf("PathIndex.Contains(NFD %q) = false, want true", nfdDocID)
	}
}

// TestIndex_NFCPathUnchanged is the prod/Windows regression guard: an already-NFC
// on-disk name must index and resolve unchanged (no normalisation side effects).
func TestIndex_NFCPathUnchanged(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()

	objNFC := string([]rune{0x0422, 0x0435, 0x0441, 0x0442, 0x043e, 0x0432, 0x044b}) + tnfcSmallShortI
	relPath := "Documents/" + objNFC + "/Ext/ObjectModule.bsl"
	mkBSLFile(t, dumpDir, relPath, "// module under test\n")

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	wantDocID := bslPathToModuleName("Documents/" + objNFC + "/Ext/ObjectModule.bsl")
	names := idx.ModuleNames()
	if !slices.Contains(names, wantDocID) {
		t.Fatalf("indexed names %v do not contain NFC docID %q", names, wantDocID)
	}
	if _, ok := idx.GetContent(wantDocID); !ok {
		t.Errorf("GetContent(%q) = not found, want found", wantDocID)
	}
}

// onDiskNameIsNFD reports whether any directory entry in dir still carries a
// combining mark (i.e. the filesystem preserved NFD bytes for a written name).
func onDiskNameIsNFD(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	for _, e := range entries {
		if strings.ContainsRune(e.Name(), 0x0306) || strings.ContainsRune(e.Name(), 0x0308) {
			return true
		}
	}
	return false
}

func containsDocID(entries []PathEntry, docID string) bool {
	for _, e := range entries {
		if e.DocID == docID {
			return true
		}
	}
	return false
}

func docIDsOf(entries []PathEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.DocID
	}
	return out
}
