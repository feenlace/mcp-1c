package dump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadFromManifest_NFCNormalisesStaleNFDDocID is the PRIMARY-fix regression for
// the stale-NFD-manifest-cache bug. A manifest written by a pre-NFC-fix binary on
// macOS stored decomposable module names (short-I / IO) in NFD. The current binary
// reuses such a cache via the warm-start path (loadFromManifestAndDiff); before the
// fix it loaded those NFD keys verbatim, so an NFC GetContent / PathIndex.Filter
// query never matched. The fix NFC-normalises the manifest docID at the load
// chokepoint, so module_code and resolve work again WITHOUT a rebuild. The schema
// stamp is kept CURRENT here so the schema gate reuses the cache and we exercise the
// in-place NFC fix in isolation (not the drop+rebuild path).
func TestLoadFromManifest_NFCNormalisesStaleNFDDocID(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()

	// Object name with a decomposable trailing short-I. The on-disk path is NFC —
	// the bug under test is the MANIFEST storing NFD, not the filesystem.
	objBase := string([]rune{0x0422, 0x0435, 0x0441, 0x0442, 0x043e, 0x0432, 0x044b}) // "Тестовы"
	objNFC := objBase + tnfcSmallShortI                                               // "Тестовый" (NFC)

	relPath := "Documents/" + objNFC + "/Ext/ObjectModule.bsl"
	mkBSLFile(t, dumpDir, relPath, "// module under test\n")

	// Cold-build a flat cache. bslPathToModuleName NFC-normalises, so the manifest
	// docID, the shard docIDs and the maps are all NFC and stamped current.
	if err := BuildCache(dumpDir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}

	wantDocID := bslPathToModuleName(relPath) // NFC
	if strings.ContainsRune(wantDocID, 0x0306) || strings.ContainsRune(wantDocID, 0x0308) {
		t.Fatalf("precondition: expected an NFC docID, got a combining mark: %q", wantDocID)
	}

	// Corrupt the on-disk manifest the way a pre-NFC-fix macOS binary would have:
	// rewrite the stored docID to its NFD byte-form, keeping the schema stamp current
	// and ModTime/Size unchanged so the warm-start Diff stays empty (pure manifest
	// fast-path) and the schema gate reuses the cache.
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: m=%v err=%v", m, err)
	}
	rewrote := false
	for rel, entry := range m.Files {
		if strings.Contains(entry.DocID, tnfcSmallShortI) {
			entry.DocID = strings.ReplaceAll(entry.DocID, tnfcSmallShortI, tnfdSmallShortI)
			m.Files[rel] = entry
			rewrote = true
		}
	}
	if !rewrote {
		t.Fatalf("did not find a composed short-I in any manifest docID to make NFD: %v", docIDsFromManifest(m))
	}
	if err := m.Save(cpath); err != nil {
		t.Fatalf("save NFD-tampered manifest: %v", err)
	}
	// Sanity: the on-disk manifest now genuinely carries an NFD docID.
	reloaded, err := LoadManifest(cpath)
	if err != nil || reloaded == nil {
		t.Fatalf("reload tampered manifest: m=%v err=%v", reloaded, err)
	}
	sawNFD := false
	for _, entry := range reloaded.Files {
		if strings.ContainsRune(entry.DocID, 0x0306) {
			sawNFD = true
		}
	}
	if !sawNFD {
		t.Fatal("precondition: tampered manifest does not carry an NFD docID")
	}

	// Reopen via the warm path. Schema matches -> the gate reuses the cache, so the
	// only source of idx.names is the NFD manifest; the NFC-at-load chokepoint must
	// recompose it.
	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex (warm reopen): %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	// module_code: GetContent(NFC) resolves despite the stale NFD manifest.
	if _, ok := idx.GetContent(wantDocID); !ok {
		t.Errorf("GetContent(NFC %q) = not found; stale NFD manifest key was not NFC-normalised on load", wantDocID)
	}
	// No combining marks leaked from the manifest into idx.names.
	for _, n := range idx.ModuleNames() {
		if strings.ContainsRune(n, 0x0306) || strings.ContainsRune(n, 0x0308) {
			t.Errorf("indexed name is not NFC (carries a combining mark): %q", n)
		}
	}
	// resolve: the PathIndex (built from idx.names) returns the module by its NFC
	// object name — the path that the same stale NFD names break.
	parts := strings.Split(wantDocID, ".")
	if len(parts) < 3 {
		t.Fatalf("unexpected docID shape: %q", wantDocID)
	}
	pi := idx.GetPathIndex()
	if pi == nil {
		t.Fatal("GetPathIndex() returned nil after Ready()")
	}
	if got := pi.Filter(parts[0], parts[1], ""); !containsDocID(got, wantDocID) {
		t.Errorf("PathIndex.Filter(NFC) did not return %q; got %v", wantDocID, docIDsOf(got))
	}
}

// TestNewIndex_DropsStaleSchemaFlatCache is the HARDENING regression: a legacy flat
// cache whose manifest records an OLDER index schema (as every pre-schema-bump cache
// does) must be dropped and cold-rebuilt by the schema gate in NewIndex, not reused.
// The rebuild also re-creates the Bleve shard docIDs under the current schema (the
// part the in-place NFC fix at the maps cannot reach). A drop is detected via a
// marker file the gate's removeFlatCacheContents deletes; a reuse would leave it.
func TestNewIndex_DropsStaleSchemaFlatCache(t *testing.T) {
	if dumpIndexSchemaVersion == baselineSchemaVersion {
		t.Skip("current binary is still on the baseline schema; no older schema to simulate")
	}
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dumpDir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\nКонецПроцедуры\n")

	if err := BuildCache(dumpDir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}

	// Marker distinguishes reuse (survives) from drop+rebuild (removeFlatCacheContents
	// deletes everything under cpath except the g/ generations subtree).
	marker := filepath.Join(cpath, "reuse-marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Tamper the manifest to record the frozen baseline schema — the value every
	// pre-bump flat cache carries (stamped at the baseline, or unstamped -> baseline).
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: m=%v err=%v", m, err)
	}
	m.SchemaVersion = baselineSchemaVersion
	if err := m.Save(cpath); err != nil {
		t.Fatalf("save stale-schema manifest: %v", err)
	}

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex (warm reopen): %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	// Dropped: the marker is gone.
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Errorf("stale-schema flat cache was reused (marker survived, stat err=%v); expected drop+rebuild", statErr)
	}
	// Rebuilt: the new manifest is re-stamped to the current schema.
	reM, err := LoadManifest(cpath)
	if err != nil || reM == nil {
		t.Fatalf("reload manifest after rebuild: m=%v err=%v", reM, err)
	}
	if reM.schemaVersion() != dumpIndexSchemaVersion {
		t.Errorf("rebuilt manifest schema = %d, want current %d", reM.schemaVersion(), dumpIndexSchemaVersion)
	}
	// Still resolvable, and exactly the one module is present.
	if _, ok := idx.GetContent("Справочник.Номенклатура.МодульОбъекта"); !ok {
		t.Error("module not resolvable after stale-schema drop+rebuild")
	}
	if idx.ModuleCount() != 1 {
		t.Errorf("expected 1 module after rebuild, got %d", idx.ModuleCount())
	}
}

// TestNewIndex_ReusesCurrentSchemaFlatCache is the no-regression guard for the schema
// gate: a flat cache stamped with the CURRENT schema must be reused, not rebuilt, so
// the gate never triggers a gratuitous rebuild. Reuse is detected via a marker file
// that survives because the cache dir is left untouched.
func TestNewIndex_ReusesCurrentSchemaFlatCache(t *testing.T) {
	dumpDir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dumpDir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\nКонецПроцедуры\n")

	if err := BuildCache(dumpDir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}

	// Sanity: a fresh cache is stamped with the current schema/zap.
	m, err := LoadManifest(cpath)
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: m=%v err=%v", m, err)
	}
	if m.schemaVersion() != dumpIndexSchemaVersion || m.zapVersion() != zapSegmentVersion {
		t.Fatalf("precondition: fresh cache not stamped current (schema=%d zap=%d)", m.schemaVersion(), m.zapVersion())
	}

	marker := filepath.Join(cpath, "reuse-marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	idx, err := NewIndex(dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex (warm reopen): %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)

	// Reused: the marker survived (no drop, no rebuild).
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("current-schema flat cache was dropped (marker gone: %v); expected reuse", statErr)
	}
	if _, ok := idx.GetContent("Справочник.Номенклатура.МодульОбъекта"); !ok {
		t.Error("module not resolvable from reused current-schema cache")
	}
	if idx.ModuleCount() != 1 {
		t.Errorf("expected 1 module, got %d", idx.ModuleCount())
	}
}

// docIDsFromManifest collects every docID in a manifest (test diagnostics only).
func docIDsFromManifest(m *Manifest) []string {
	out := make([]string, 0, len(m.Files))
	for _, e := range m.Files {
		out = append(out, e.DocID)
	}
	return out
}
