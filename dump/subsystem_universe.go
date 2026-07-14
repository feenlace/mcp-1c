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
	top, err := os.ReadDir(dumpDir)
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
		folderPath := filepath.Join(dumpDir, d.Name())
		objs, rerr := os.ReadDir(folderPath)
		if rerr != nil {
			continue
		}
		for _, obj := range objs {
			if len(set) >= maxUniverseObjects {
				break
			}
			name, ok := appliedObjectName(dumpDir, d.Name(), obj)
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
// exists (so a stray dir/file is not counted). Every candidate path is safeJoin
// validated, so a symlink that escapes the dump is refused (a symlinked object dir
// already has IsDir()==false and is skipped as a non-.xml entry).
func appliedObjectName(dumpRoot, folder string, entry os.DirEntry) (string, bool) {
	entryName := entry.Name()
	if entry.IsDir() {
		// Ext shape: <Folder>/<Name>/Ext/<Name>.xml
		cand, err := safeJoin(dumpRoot, folder, entryName, "Ext", entryName+".xml")
		if err != nil {
			return "", false
		}
		if info, serr := os.Stat(cand); serr != nil || !info.Mode().IsRegular() {
			return "", false
		}
		return entryName, true
	}
	// Root shape: <Folder>/<Name>.xml
	if !strings.HasSuffix(entryName, ".xml") {
		return "", false
	}
	objName := strings.TrimSuffix(entryName, ".xml")
	if objName == "" {
		return "", false
	}
	cand, err := safeJoin(dumpRoot, folder, entryName)
	if err != nil {
		return "", false
	}
	if info, serr := os.Stat(cand); serr != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return objName, true
}
