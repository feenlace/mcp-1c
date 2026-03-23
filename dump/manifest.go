package dump

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const manifestVersion = 1
const manifestFileName = "manifest.json"

// FileEntry tracks metadata for a single BSL file in the manifest.
type FileEntry struct {
	ModTime int64  `json:"m"`
	Size    int64  `json:"s"`
	DocID   string `json:"d"`
}

// Manifest tracks mtime+size per BSL file to detect changes for incremental indexing.
type Manifest struct {
	Version int                  `json:"v"`
	Files   map[string]FileEntry `json:"f"` // key = relative path (forward-slash normalized)
}

// DiffResult describes what changed between the manifest and the current filesystem.
type DiffResult struct {
	Added    []string // relative paths of new files
	Modified []string // relative paths of changed files (mtime or size differ)
	Deleted  []string // relative paths removed from disk
}

// Empty reports whether no changes were detected.
func (d *DiffResult) Empty() bool {
	return len(d.Added) == 0 && len(d.Modified) == 0 && len(d.Deleted) == 0
}

// manifestPath returns the full path to manifest.json inside the cache directory.
func manifestPath(cacheDir string) string {
	return filepath.Join(cacheDir, manifestFileName)
}

// LoadManifest reads a manifest from the cache directory.
// Returns nil, nil if the file does not exist (first run or after --reindex).
func LoadManifest(cacheDir string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath(cacheDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	if m.Version != manifestVersion {
		// Incompatible version — treat as missing.
		return nil, nil
	}

	return &m, nil
}

// Save writes the manifest to the cache directory atomically.
func (m *Manifest) Save(cacheDir string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("creating cache dir for manifest: %w", err)
	}

	target := manifestPath(cacheDir)
	tmp := target + ".tmp"

	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing manifest tmp: %w", err)
	}

	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming manifest: %w", err)
	}

	return nil
}

// Diff walks dumpDir and compares the current BSL files against the manifest.
// Returns which files were added, modified, or deleted.
func (m *Manifest) Diff(dumpDir string) (*DiffResult, error) {
	result := &DiffResult{}
	seen := make(map[string]bool, len(m.Files))

	err := filepath.WalkDir(dumpDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".bsl") {
			return nil
		}

		rel, err := filepath.Rel(dumpDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		seen[rel] = true

		info, err := d.Info()
		if err != nil {
			return err
		}

		existing, ok := m.Files[rel]
		if !ok {
			result.Added = append(result.Added, rel)
			return nil
		}

		if existing.ModTime != info.ModTime().UnixMilli() || existing.Size != info.Size() {
			result.Modified = append(result.Modified, rel)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking dump dir for diff: %w", err)
	}

	// Files present in manifest but not on disk.
	for rel := range m.Files {
		if !seen[rel] {
			result.Deleted = append(result.Deleted, rel)
		}
	}

	return result, nil
}

// buildManifest creates a new manifest from the current filesystem state.
// pathToDocID maps relative paths (forward-slash) to their document IDs (module names).
func buildManifest(dumpDir string, pathToDocID map[string]string) (*Manifest, error) {
	m := &Manifest{
		Version: manifestVersion,
		Files:   make(map[string]FileEntry, len(pathToDocID)),
	}

	for relPath, docID := range pathToDocID {
		absPath := filepath.Join(dumpDir, filepath.FromSlash(relPath))
		info, err := os.Stat(absPath)
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", relPath, err)
		}
		m.Files[relPath] = FileEntry{
			ModTime: info.ModTime().UnixMilli(),
			Size:    info.Size(),
			DocID:   docID,
		}
	}

	return m, nil
}
