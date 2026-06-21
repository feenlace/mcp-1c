package dump

import (
	"sort"
	"strings"
)

// PathEntry represents a single BSL module with its decomposed path components.
type PathEntry struct {
	DocID      string // e.g. "Документ.РеализацияТоваров.МодульОбъекта"
	Category   string // e.g. "Документ"
	ObjectName string // e.g. "РеализацияТоваров"
	ModuleType string // e.g. "МодульОбъекта"
}

// PathIndex provides fast in-memory lookups over decomposed BSL module paths.
// It indexes modules by category, object name, and module type for instant filtering
// without filesystem walks or Bleve queries.
type PathIndex struct {
	entries    []PathEntry
	byCategory map[string][]int // category -> indices into entries
	byModule   map[string][]int // moduleType -> indices into entries
	byObject   map[string][]int // objectName -> indices into entries
	docIDSet   map[string]int   // docID -> index in entries (for fast membership checks)
}

// NewPathIndex builds a PathIndex from a slice of docIDs (human-readable module names).
// Each docID is expected to have the form "Category.ObjectName.ModuleType" (or longer
// for form modules like "Category.Object.Форма.FormName.ModuleType").
func NewPathIndex(names []string) *PathIndex {
	pi := &PathIndex{
		entries:    make([]PathEntry, 0, len(names)),
		byCategory: make(map[string][]int, 32),
		byModule:   make(map[string][]int, 16),
		byObject:   make(map[string][]int, len(names)/3),
		docIDSet:   make(map[string]int, len(names)),
	}

	for _, name := range names {
		parts := strings.Split(name, ".")

		var entry PathEntry
		entry.DocID = name

		switch {
		case len(parts) >= 3:
			entry.Category = parts[0]
			entry.ObjectName = parts[1]
			entry.ModuleType = parts[len(parts)-1]
		case len(parts) == 2:
			entry.Category = parts[0]
			entry.ObjectName = parts[1]
		default:
			entry.ObjectName = name
		}

		idx := len(pi.entries)
		pi.entries = append(pi.entries, entry)
		pi.docIDSet[name] = idx

		if entry.Category != "" {
			pi.byCategory[entry.Category] = append(pi.byCategory[entry.Category], idx)
		}
		if entry.ModuleType != "" {
			pi.byModule[entry.ModuleType] = append(pi.byModule[entry.ModuleType], idx)
		}
		if entry.ObjectName != "" {
			pi.byObject[entry.ObjectName] = append(pi.byObject[entry.ObjectName], idx)
		}
	}

	return pi
}

// Filter returns all entries matching the given filters.
// Empty string means no filtering on that field.
func (pi *PathIndex) Filter(category, objectName, moduleType string) []PathEntry {
	if pi == nil || len(pi.entries) == 0 {
		return nil
	}

	// Defensive: normalise lookup args to NFC so an NFD-supplied filter matches
	// the NFC keys (byCategory/byObject/byModule are keyed by NFC docID parts).
	// No-op on already-NFC input.
	category = NFC(category)
	objectName = NFC(objectName)
	moduleType = NFC(moduleType)

	// If no filters, return all non-deleted entries.
	if category == "" && objectName == "" && moduleType == "" {
		result := make([]PathEntry, 0, len(pi.entries))
		for _, e := range pi.entries {
			if e.DocID != "" {
				result = append(result, e)
			}
		}
		return result
	}

	// Start with the smallest candidate set for efficiency.
	var candidates []int
	initialized := false

	if category != "" {
		idxs, ok := pi.byCategory[category]
		if !ok {
			return nil
		}
		candidates = idxs
		initialized = true
	}

	if objectName != "" {
		idxs, ok := pi.byObject[objectName]
		if !ok {
			return nil
		}
		if !initialized {
			candidates = idxs
			initialized = true
		} else {
			candidates = intersect(candidates, idxs)
			if len(candidates) == 0 {
				return nil
			}
		}
	}

	if moduleType != "" {
		idxs, ok := pi.byModule[moduleType]
		if !ok {
			return nil
		}
		if !initialized {
			candidates = idxs
		} else {
			candidates = intersect(candidates, idxs)
			if len(candidates) == 0 {
				return nil
			}
		}
	}

	result := make([]PathEntry, len(candidates))
	for i, idx := range candidates {
		result[i] = pi.entries[idx]
	}
	return result
}

// FilterDocIDs returns just the docIDs matching the given filters.
// This is more efficient than Filter when only IDs are needed.
func (pi *PathIndex) FilterDocIDs(category, moduleType string) []string {
	if pi == nil || len(pi.entries) == 0 {
		return nil
	}

	if category == "" && moduleType == "" {
		result := make([]string, 0, len(pi.entries))
		for i := range pi.entries {
			if pi.entries[i].DocID != "" {
				result = append(result, pi.entries[i].DocID)
			}
		}
		return result
	}

	var candidates []int
	initialized := false

	if category != "" {
		idxs, ok := pi.byCategory[category]
		if !ok {
			return nil
		}
		candidates = idxs
		initialized = true
	}

	if moduleType != "" {
		idxs, ok := pi.byModule[moduleType]
		if !ok {
			return nil
		}
		if !initialized {
			candidates = idxs
		} else {
			candidates = intersect(candidates, idxs)
		}
	}

	result := make([]string, len(candidates))
	for i, idx := range candidates {
		result[i] = pi.entries[idx].DocID
	}
	return result
}

// Categories returns all unique category names, sorted alphabetically.
func (pi *PathIndex) Categories() []string {
	if pi == nil {
		return nil
	}
	cats := make([]string, 0, len(pi.byCategory))
	for cat := range pi.byCategory {
		cats = append(cats, cat)
	}
	sort.Strings(cats)
	return cats
}

// Objects returns all unique object names within a category, sorted alphabetically.
// If category is empty, returns all object names across all categories.
func (pi *PathIndex) Objects(category string) []string {
	if pi == nil {
		return nil
	}

	seen := make(map[string]struct{})

	if category == "" {
		for obj := range pi.byObject {
			seen[obj] = struct{}{}
		}
	} else {
		idxs, ok := pi.byCategory[category]
		if !ok {
			return nil
		}
		for _, i := range idxs {
			seen[pi.entries[i].ObjectName] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))
	for obj := range seen {
		result = append(result, obj)
	}
	sort.Strings(result)
	return result
}

// ModuleTypes returns all unique module types for a given category and object name,
// sorted alphabetically. Empty category/objectName means no filtering on that field.
func (pi *PathIndex) ModuleTypes(category, objectName string) []string {
	if pi == nil {
		return nil
	}

	entries := pi.Filter(category, objectName, "")
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.ModuleType != "" {
			seen[e.ModuleType] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))
	for mt := range seen {
		result = append(result, mt)
	}
	sort.Strings(result)
	return result
}

// Count returns the total number of entries in the index.
func (pi *PathIndex) Count() int {
	if pi == nil {
		return 0
	}
	return len(pi.entries)
}

// Contains reports whether the given docID is present in the index.
func (pi *PathIndex) Contains(docID string) bool {
	if pi == nil {
		return false
	}
	// Defensive: normalise to NFC so an NFD docID matches the NFC keys. No-op on
	// already-NFC input.
	_, ok := pi.docIDSet[NFC(docID)]
	return ok
}

// AddEntry adds a new entry to the path index by parsing the docID.
// If the docID already exists, this is a no-op.
func (pi *PathIndex) AddEntry(docID string) {
	if pi == nil {
		return
	}
	if _, exists := pi.docIDSet[docID]; exists {
		return
	}

	parts := strings.Split(docID, ".")
	var entry PathEntry
	entry.DocID = docID

	switch {
	case len(parts) >= 3:
		entry.Category = parts[0]
		entry.ObjectName = parts[1]
		entry.ModuleType = parts[len(parts)-1]
	case len(parts) == 2:
		entry.Category = parts[0]
		entry.ObjectName = parts[1]
	default:
		entry.ObjectName = docID
	}

	pi.addEntryInternal(entry)
}

// AddEntryWithMeta adds a new entry with explicit category and module type,
// bypassing docID parsing. Used by IndexDocWithMeta where metadata differs
// from what would be derived from the docID.
func (pi *PathIndex) AddEntryWithMeta(docID, category, moduleType string) {
	if pi == nil {
		return
	}
	if _, exists := pi.docIDSet[docID]; exists {
		return
	}

	parts := strings.Split(docID, ".")
	objectName := ""
	if len(parts) >= 2 {
		objectName = parts[1]
	} else if len(parts) == 1 {
		objectName = docID
	}

	pi.addEntryInternal(PathEntry{
		DocID:      docID,
		Category:   category,
		ObjectName: objectName,
		ModuleType: moduleType,
	})
}

func (pi *PathIndex) addEntryInternal(entry PathEntry) {
	idx := len(pi.entries)
	pi.entries = append(pi.entries, entry)
	pi.docIDSet[entry.DocID] = idx

	if entry.Category != "" {
		pi.byCategory[entry.Category] = append(pi.byCategory[entry.Category], idx)
	}
	if entry.ModuleType != "" {
		pi.byModule[entry.ModuleType] = append(pi.byModule[entry.ModuleType], idx)
	}
	if entry.ObjectName != "" {
		pi.byObject[entry.ObjectName] = append(pi.byObject[entry.ObjectName], idx)
	}
}

// RemoveEntry removes an entry from the path index by docID.
// Uses lazy deletion: marks the entry as removed by clearing its DocID.
// The index maps are updated to remove references to the deleted entry.
func (pi *PathIndex) RemoveEntry(docID string) {
	if pi == nil {
		return
	}
	idx, exists := pi.docIDSet[docID]
	if !exists {
		return
	}

	entry := pi.entries[idx]
	delete(pi.docIDSet, docID)

	// Clear the entry (lazy deletion -- slot remains but is empty).
	pi.entries[idx] = PathEntry{}

	// Remove from index maps.
	if entry.Category != "" {
		pi.byCategory[entry.Category] = removeInt(pi.byCategory[entry.Category], idx)
		if len(pi.byCategory[entry.Category]) == 0 {
			delete(pi.byCategory, entry.Category)
		}
	}
	if entry.ModuleType != "" {
		pi.byModule[entry.ModuleType] = removeInt(pi.byModule[entry.ModuleType], idx)
		if len(pi.byModule[entry.ModuleType]) == 0 {
			delete(pi.byModule, entry.ModuleType)
		}
	}
	if entry.ObjectName != "" {
		pi.byObject[entry.ObjectName] = removeInt(pi.byObject[entry.ObjectName], idx)
		if len(pi.byObject[entry.ObjectName]) == 0 {
			delete(pi.byObject, entry.ObjectName)
		}
	}
}

// removeInt removes the first occurrence of v from a sorted int slice.
func removeInt(s []int, v int) []int {
	for i, x := range s {
		if x == v {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

// intersect returns the sorted intersection of two sorted int slices.
// Both inputs must be sorted in ascending order (which they are, since indices
// are appended in order during NewPathIndex).
func intersect(a, b []int) []int {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}

	// Use set-based intersection for small slices against large ones.
	if len(a) > len(b)*4 {
		return intersectSmallInLarge(b, a)
	}
	if len(b) > len(a)*4 {
		return intersectSmallInLarge(a, b)
	}

	// Merge-based intersection for similarly-sized slices.
	result := make([]int, 0, min(len(a), len(b)))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			i++
		case a[i] > b[j]:
			j++
		default:
			result = append(result, a[i])
			i++
			j++
		}
	}
	return result
}

// intersectSmallInLarge finds elements of small that exist in large using binary search.
// large must be sorted.
func intersectSmallInLarge(small, large []int) []int {
	result := make([]int, 0, len(small))
	for _, v := range small {
		idx := sort.SearchInts(large, v)
		if idx < len(large) && large[idx] == v {
			result = append(result, v)
		}
	}
	return result
}
