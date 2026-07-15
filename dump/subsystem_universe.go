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

// EnumerateAppliedObjects walks the dump's universe kind-folders and returns the sorted,
// de-duplicated canonical Russian full names ("<Вид>.<Имя>") of every object it finds.
// This is the universe analyze_subsystems action=orphans subtracts subsystem membership
// from: an object present here but in no subsystem's Состав is an orphan. It is the
// warning-free wrapper over EnumerateUniverseObjects, kept for callers that do not need
// the coverage diagnostic. (The name is historical: the universe now spans far more than
// the applied kinds.)
func EnumerateAppliedObjects(dumpDir string) []string {
	objs, _ := EnumerateUniverseObjects(dumpDir, nil)
	return objs
}

// EnumerateUniverseObjects walks the dump's universe kind-folders and returns the sorted,
// de-duplicated canonical Russian full names of every object it finds, together with any
// non-fatal coverage warnings.
//
// The universe now spans the FULL set of top-level, independently-enumerable, Состав
// eligible metadata kinds (the applied 15 PLUS Константа and the service kinds in
// universeServiceKinds), not just the applied 15, so `orphans` is consistent with what
// `containing` recognises: a common module, role, defined type or constant that belongs
// to no subsystem is now reported instead of silently missed. Every kind's Russian prefix
// comes from the SAME table the membership canonicalizer uses (universeFolderToRu, sourced
// from metadataTypes / serviceKindEnToRu), so an object's universe string and its
// membership string are byte-identical and orphans set-cancels. Object names are
// NFC-normalised so an NFD-decomposed name from a macOS-unpacked dump matches its
// membership key.
//
// It handles both on-disk layouts per folder:
//
//	Root: <Folder>/<Name>.xml                 (Hierarchical, 8.3.10+)
//	Ext:  <Folder>/<Name>/Ext/<Name>.xml      (ConfigurationDump / legacy)
//
// and both folder spellings (modern plural "Documents"/"CommonModules" and legacy
// singular "Document"/"CommonModule"), so a mixed dump is handled.
//
// COVERAGE DIAGNOSTIC (non-silent): when membershipSubs is non-nil, any universe kind
// that a subsystem's Состав references but whose dump folder is ABSENT yields a path-free
// RU warning NAMING the kind, so a wrong/missing universe folder (or a partial dump) can
// never silently under-report that kind's orphans. A kind whose folder is present but
// empty does NOT warn, and a kind that is simply unused (absent AND unreferenced) does NOT
// warn, mirroring the live extension, which warns only when Метаданные[<collection>]
// actually throws. Residual ambiguity is intentional and documented: a config that
// genuinely lacks a kind also lacks its folder, so the diagnostic is scoped to kinds
// proven in-use by membership.
//
// A dump directory that cannot be read yields an empty universe (nil), which
// computeOrphans reports honestly as "пуст или недоступен" rather than a false
// "everything is distributed".
func EnumerateUniverseObjects(dumpDir string, membershipSubs []Subsystem) (objects []string, warnings []string) {
	if dumpDirIsNonDir(dumpDir) {
		return nil, nil // dumpDir itself is a non-directory node: empty universe, refused before the blocking open
	}
	root, err := os.OpenRoot(dumpDir)
	if err != nil {
		return nil, nil
	}
	defer func() { _ = root.Close() }()
	top, err := readDirInRoot(root, ".")
	if err != nil {
		return nil, nil
	}
	set := make(map[string]struct{})
	presentKinds := make(map[string]bool)
	for _, d := range top {
		if !d.IsDir() {
			continue
		}
		ru, ok := universeFolderToRu[d.Name()]
		if !ok {
			continue // not a universe kind folder
		}
		objs, rerr := readDirInRoot(root, d.Name())
		if rerr != nil {
			continue // folder unreadable: treat as absent (a referenced kind then warns below)
		}
		presentKinds[ru] = true // folder present (even if it yields no valid objects)
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
	return out, universeCoverageWarnings(membershipSubs, presentKinds)
}

// universeCoverageWarnings names every universe kind that subsystem membership references
// but whose dump folder was absent during enumeration, so a partial dump or a wrong
// universe folder name surfaces instead of silently under-reporting that kind's orphans.
// Warnings are path-free, contain no тире, name the kind, and are deterministically
// ordered. An empty membership yields no warnings.
func universeCoverageWarnings(membershipSubs []Subsystem, presentKinds map[string]bool) []string {
	if len(membershipSubs) == 0 {
		return nil
	}
	var missing []string
	for ru := range membershipKinds(membershipSubs) {
		if universeRuKinds[ru] && !presentKinds[ru] {
			missing = append(missing, ru)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	warnings := make([]string, 0, len(missing))
	for _, ru := range missing {
		warnings = append(warnings, "вид "+ru+": каталог этого вида в дампе не найден, но объекты вида есть в составе подсистем; объекты вне подсистем могут быть не учтены")
	}
	return warnings
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
