package dump

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxUniverseObjects is a pure DoS ceiling on how many applied objects the
// universe enumerator will collect. A real configuration is orders of magnitude
// below it (even a large ERP has tens of thousands of applied objects), so it only
// bounds a pathological or malicious dump. Declared as a var so tests can tighten it.
var maxUniverseObjects = 1_000_000

// EnumerateAppliedObjects walks the dump's applied-kind folders and returns the
// canonical Russian full name ("<Вид>.<Имя>") of every applied object, sorted and
// de-duplicated. This is the universe analyze_subsystems action=orphans subtracts
// subsystem membership from: an object present here but in no subsystem's Состав is
// an orphan.
//
// It handles both on-disk layouts per folder:
//
//	Root: <Folder>/<Name>.xml                 (Hierarchical, 8.3.10+)
//	Ext:  <Folder>/<Name>/Ext/<Name>.xml      (ConfigurationDump / legacy)
//
// and both folder spellings (plural English "Documents" and legacy singular
// "Document"), so a mixed dump is handled. Only the 15 applied kinds are walked
// (Constant, subsystems and service kinds like ОбщийМодуль are intentionally
// excluded, matching the live extension's AllObjects). The Russian prefix comes
// from the SAME table (metadataTypes, via appliedFolderToRu) that
// canonicalizeContentPath uses for subsystem Content, so an applied object's
// universe string and its membership string are byte-identical by construction,
// which is what makes orphans set-cancel correctly. Object names are NFC-normalised
// so an NFD-decomposed name from a macOS-unpacked dump matches its membership key.
//
// A dump directory that cannot be read yields an empty universe (nil), which
// computeOrphans reports honestly as "пуст или недоступен" rather than a false
// "everything is distributed".
func EnumerateAppliedObjects(dumpDir string) []string {
	root, err := os.OpenRoot(dumpDir)
	if err != nil {
		return nil
	}
	defer func() { _ = root.Close() }()
	top, err := readDirInRoot(root, ".")
	if err != nil {
		return nil
	}
	set := make(map[string]struct{})
	for _, d := range top {
		if !d.IsDir() {
			continue
		}
		ru, ok := appliedFolderToRu[d.Name()]
		if !ok {
			continue // not an applied kind folder
		}
		objs, rerr := readDirInRoot(root, d.Name())
		if rerr != nil {
			continue
		}
		for _, obj := range objs {
			if len(set) >= maxUniverseObjects {
				break
			}
			name, ok := appliedObjectName(root, d.Name(), obj)
			if !ok {
				continue
			}
			set[ru+"."+NFC(name)] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for q := range set {
		out = append(out, q)
	}
	sort.Strings(out)
	return out
}

// appliedObjectName resolves one entry of an applied-kind folder to its object
// name, covering both on-disk shapes and confirming the object's XML actually
// exists (so a stray dir/file is not counted). Every candidate path is resolved
// through the dump's os.Root, so an escaping symlink at ANY component (including an
// intermediate object-dir or Ext symlink) is refused instead of being followed to
// probe out-of-dump existence or metadata; root.Lstat does not follow the final
// component, so a symlinked object .xml is not counted either.
func appliedObjectName(root *os.Root, folder string, entry os.DirEntry) (string, bool) {
	entryName := entry.Name()
	if entry.IsDir() {
		// Ext shape: <Folder>/<Name>/Ext/<Name>.xml
		rel := filepath.Join(folder, entryName, "Ext", entryName+".xml")
		if info, serr := root.Lstat(rel); serr == nil && info.Mode().IsRegular() {
			return entryName, true
		}
		return "", false
	}
	// Root shape: <Folder>/<Name>.xml
	if !strings.HasSuffix(entryName, ".xml") {
		return "", false
	}
	objName := strings.TrimSuffix(entryName, ".xml")
	if objName == "" {
		return "", false
	}
	rel := filepath.Join(folder, entryName)
	if info, serr := root.Lstat(rel); serr == nil && info.Mode().IsRegular() {
		return objName, true
	}
	return "", false
}
