package dump

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// createBSLFile creates a .bsl file at the given relative path under dir with content.
func createBSLFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	absPath := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManifest_SaveLoad(t *testing.T) {
	cacheDir := t.TempDir()

	m := &Manifest{
		Version: manifestVersion,
		Files: map[string]FileEntry{
			"Documents/Sale/Ext/ObjectModule.bsl": {
				ModTime: 1000,
				Size:    42,
				DocID:   "Документ.Sale.МодульОбъекта",
			},
		},
	}

	if err := m.Save(cacheDir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := LoadManifest(cacheDir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil manifest")
	}
	if loaded.Version != manifestVersion {
		t.Errorf("version = %d, want %d", loaded.Version, manifestVersion)
	}
	if len(loaded.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(loaded.Files))
	}

	entry := loaded.Files["Documents/Sale/Ext/ObjectModule.bsl"]
	if entry.ModTime != 1000 || entry.Size != 42 || entry.DocID != "Документ.Sale.МодульОбъекта" {
		t.Errorf("unexpected entry: %+v", entry)
	}
}

func TestLoadManifest_Missing(t *testing.T) {
	m, err := LoadManifest(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Error("expected nil manifest for missing file")
	}
}

func TestLoadManifest_BadVersion(t *testing.T) {
	cacheDir := t.TempDir()
	data := `{"v":999,"f":{}}`
	if err := os.WriteFile(filepath.Join(cacheDir, manifestFileName), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := LoadManifest(cacheDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Error("expected nil for incompatible version")
	}
}

func TestManifest_Diff_NoChanges(t *testing.T) {
	dumpDir := t.TempDir()
	createBSLFile(t, dumpDir, "CommonModules/MyMod/Ext/Module.bsl", "// hello")

	info, err := os.Stat(filepath.Join(dumpDir, "CommonModules/MyMod/Ext/Module.bsl"))
	if err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Version: manifestVersion,
		Files: map[string]FileEntry{
			"CommonModules/MyMod/Ext/Module.bsl": {
				ModTime: info.ModTime().UnixMilli(),
				Size:    info.Size(),
				DocID:   "ОбщийМодуль.MyMod.Модуль",
			},
		},
	}

	diff, err := m.Diff(dumpDir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !diff.Empty() {
		t.Errorf("expected no changes, got added=%v modified=%v deleted=%v",
			diff.Added, diff.Modified, diff.Deleted)
	}
}

func TestManifest_Diff_Added(t *testing.T) {
	dumpDir := t.TempDir()
	createBSLFile(t, dumpDir, "CommonModules/Mod1/Ext/Module.bsl", "// old")
	createBSLFile(t, dumpDir, "CommonModules/Mod2/Ext/Module.bsl", "// new")

	info, err := os.Stat(filepath.Join(dumpDir, "CommonModules/Mod1/Ext/Module.bsl"))
	if err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Version: manifestVersion,
		Files: map[string]FileEntry{
			"CommonModules/Mod1/Ext/Module.bsl": {
				ModTime: info.ModTime().UnixMilli(),
				Size:    info.Size(),
				DocID:   "ОбщийМодуль.Mod1.Модуль",
			},
		},
	}

	diff, err := m.Diff(dumpDir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Added) != 1 || diff.Added[0] != "CommonModules/Mod2/Ext/Module.bsl" {
		t.Errorf("expected 1 added file, got %v", diff.Added)
	}
	if len(diff.Modified) != 0 {
		t.Errorf("expected no modified, got %v", diff.Modified)
	}
	if len(diff.Deleted) != 0 {
		t.Errorf("expected no deleted, got %v", diff.Deleted)
	}
}

func TestManifest_Diff_Deleted(t *testing.T) {
	dumpDir := t.TempDir()
	// Only Mod1 exists on disk, but manifest has Mod1 + Mod2.
	createBSLFile(t, dumpDir, "CommonModules/Mod1/Ext/Module.bsl", "// exists")

	info, err := os.Stat(filepath.Join(dumpDir, "CommonModules/Mod1/Ext/Module.bsl"))
	if err != nil {
		t.Fatal(err)
	}

	m := &Manifest{
		Version: manifestVersion,
		Files: map[string]FileEntry{
			"CommonModules/Mod1/Ext/Module.bsl": {
				ModTime: info.ModTime().UnixMilli(),
				Size:    info.Size(),
				DocID:   "ОбщийМодуль.Mod1.Модуль",
			},
			"CommonModules/Mod2/Ext/Module.bsl": {
				ModTime: 1000,
				Size:    10,
				DocID:   "ОбщийМодуль.Mod2.Модуль",
			},
		},
	}

	diff, err := m.Diff(dumpDir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Added) != 0 {
		t.Errorf("expected no added, got %v", diff.Added)
	}
	if len(diff.Modified) != 0 {
		t.Errorf("expected no modified, got %v", diff.Modified)
	}
	if len(diff.Deleted) != 1 || diff.Deleted[0] != "CommonModules/Mod2/Ext/Module.bsl" {
		t.Errorf("expected 1 deleted file, got %v", diff.Deleted)
	}
}

func TestManifest_Diff_Modified(t *testing.T) {
	dumpDir := t.TempDir()
	createBSLFile(t, dumpDir, "Documents/Sale/Ext/ObjectModule.bsl", "// original content")

	info, err := os.Stat(filepath.Join(dumpDir, "Documents/Sale/Ext/ObjectModule.bsl"))
	if err != nil {
		t.Fatal(err)
	}

	// Manifest has same path but different mtime (simulating file change).
	m := &Manifest{
		Version: manifestVersion,
		Files: map[string]FileEntry{
			"Documents/Sale/Ext/ObjectModule.bsl": {
				ModTime: info.ModTime().UnixMilli() - 5000, // older
				Size:    info.Size(),
				DocID:   "Документ.Sale.МодульОбъекта",
			},
		},
	}

	diff, err := m.Diff(dumpDir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Modified) != 1 || diff.Modified[0] != "Documents/Sale/Ext/ObjectModule.bsl" {
		t.Errorf("expected 1 modified file, got %v", diff.Modified)
	}
}

func TestManifest_Diff_SizeChanged(t *testing.T) {
	dumpDir := t.TempDir()
	createBSLFile(t, dumpDir, "Documents/Sale/Ext/ObjectModule.bsl", "// content")

	info, err := os.Stat(filepath.Join(dumpDir, "Documents/Sale/Ext/ObjectModule.bsl"))
	if err != nil {
		t.Fatal(err)
	}

	// Same mtime but different size.
	m := &Manifest{
		Version: manifestVersion,
		Files: map[string]FileEntry{
			"Documents/Sale/Ext/ObjectModule.bsl": {
				ModTime: info.ModTime().UnixMilli(),
				Size:    info.Size() + 100, // different
				DocID:   "Документ.Sale.МодульОбъекта",
			},
		},
	}

	diff, err := m.Diff(dumpDir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Modified) != 1 {
		t.Errorf("expected 1 modified file, got %v", diff.Modified)
	}
}

func TestManifest_Diff_Combined(t *testing.T) {
	dumpDir := t.TempDir()

	// Create 2 files on disk: unchanged + added.
	createBSLFile(t, dumpDir, "CommonModules/Unchanged/Ext/Module.bsl", "// same")
	createBSLFile(t, dumpDir, "CommonModules/NewMod/Ext/Module.bsl", "// new")

	infoUnchanged, err := os.Stat(filepath.Join(dumpDir, "CommonModules/Unchanged/Ext/Module.bsl"))
	if err != nil {
		t.Fatal(err)
	}

	// Manifest: Unchanged (matching) + Deleted (not on disk).
	m := &Manifest{
		Version: manifestVersion,
		Files: map[string]FileEntry{
			"CommonModules/Unchanged/Ext/Module.bsl": {
				ModTime: infoUnchanged.ModTime().UnixMilli(),
				Size:    infoUnchanged.Size(),
				DocID:   "ОбщийМодуль.Unchanged.Модуль",
			},
			"CommonModules/OldMod/Ext/Module.bsl": {
				ModTime: 1000,
				Size:    5,
				DocID:   "ОбщийМодуль.OldMod.Модуль",
			},
		},
	}

	diff, err := m.Diff(dumpDir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Added) != 1 {
		t.Errorf("expected 1 added, got %v", diff.Added)
	}
	if len(diff.Modified) != 0 {
		t.Errorf("expected 0 modified, got %v", diff.Modified)
	}
	if len(diff.Deleted) != 1 {
		t.Errorf("expected 1 deleted, got %v", diff.Deleted)
	}
}

func TestDiffResult_Empty(t *testing.T) {
	d := &DiffResult{}
	if !d.Empty() {
		t.Error("expected empty diff to be empty")
	}

	d.Added = []string{"x"}
	if d.Empty() {
		t.Error("expected non-empty diff when Added has entries")
	}
}

func TestBuildManifest(t *testing.T) {
	dumpDir := t.TempDir()
	createBSLFile(t, dumpDir, "CommonModules/Mod1/Ext/Module.bsl", "// content")

	pathToDocID := map[string]string{
		"CommonModules/Mod1/Ext/Module.bsl": "ОбщийМодуль.Mod1.Модуль",
	}

	m, err := buildManifest(dumpDir, pathToDocID)
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}

	if m.Version != manifestVersion {
		t.Errorf("version = %d, want %d", m.Version, manifestVersion)
	}
	if len(m.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(m.Files))
	}

	entry := m.Files["CommonModules/Mod1/Ext/Module.bsl"]
	if entry.DocID != "ОбщийМодуль.Mod1.Модуль" {
		t.Errorf("docID = %q, want %q", entry.DocID, "ОбщийМодуль.Mod1.Модуль")
	}
	if entry.Size <= 0 {
		t.Error("expected positive size")
	}

	// Verify mtime is recent (within last minute).
	now := time.Now().UnixMilli()
	if entry.ModTime > now || entry.ModTime < now-60000 {
		t.Errorf("mtime %d seems wrong, now=%d", entry.ModTime, now)
	}
}
