package dump

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
)

// utf8BOM is the 3-byte UTF-8 Byte Order Mark (U+FEFF) that 1C DumpConfigToFiles
// prepends to BSL files. It must be stripped before indexing or returning content.
const utf8BOM = "\xEF\xBB\xBF"

// showProgress controls whether this package prints progress and info messages
// to stderr. When false (the default, matching the cautious v1.6.1 behaviour),
// no stderr writes are performed, so strict MCP clients do not see them as
// errors. When true, the v1.6.0 progress ticker and informational lines are
// restored for interactive terminal launches.
var showProgress atomic.Bool

// SetShowProgress toggles progress output on stderr. Called from main once the
// effective TTY mode is known (pipe/terminal plus --quiet/--verbose overrides).
func SetShowProgress(v bool) { showProgress.Store(v) }

// stripBOM removes the UTF-8 BOM prefix from s if present.
func stripBOM(s string) string {
	return strings.TrimPrefix(s, utf8BOM)
}

// readErrLogInterval bounds how often module-read-error warnings are emitted, so
// a broadly unreadable dump (a locked directory, an antivirus quarantine, a
// paused cloud-sync folder) cannot flood the log with one line per file.
const readErrLogInterval = 5 * time.Second

// readRetryDelay is the pause before the single retry readModuleContent makes
// when a file read fails. Transient Windows file locks (antivirus / OneDrive /
// the OS search indexer briefly holding the handle) usually clear within it.
const readRetryDelay = 50 * time.Millisecond

var (
	readErrLogLast       atomic.Int64 // UnixNano of the last emitted read-error warning
	readErrLogSuppressed atomic.Int64 // read-error warnings suppressed since the last emit
)

// warnModuleReadErr emits a rate-limited warning that a module file could not be
// read, so its exclusion from a build or a search result is observable instead of
// a silent false-negative. At most one warning per readErrLogInterval is emitted;
// the number suppressed in between is folded into the next emitted line.
func warnModuleReadErr(path string, err error) {
	now := time.Now().UnixNano()
	prev := readErrLogLast.Load()
	if now-prev < int64(readErrLogInterval) || !readErrLogLast.CompareAndSwap(prev, now) {
		readErrLogSuppressed.Add(1)
		return
	}
	slog.Warn("dump: module file unreadable, excluded from this result "+
		"(check file lock / antivirus / cloud-sync)",
		"path", path,
		"error", err,
		"suppressed_since_last", readErrLogSuppressed.Swap(0))
}

// readModuleContent reads a BSL module's source from disk and strips the UTF-8
// BOM. It retries once after a short pause on failure, because module files are
// occasionally locked for a moment by antivirus / cloud-sync / the OS search
// indexer (common on Windows). If the read still fails the module is reported as
// unavailable (so callers exclude it) and a rate-limited warning is logged — the
// file does not silently disappear from results without a trace.
func readModuleContent(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		time.Sleep(readRetryDelay)
		data, err = os.ReadFile(path)
	}
	if err != nil {
		warnModuleReadErr(path, err)
		return "", false
	}
	return stripBOM(string(data)), true
}

// fileStamp identifies a revision of a file by its modification time (in
// milliseconds since the epoch) and its size. It is deliberately the same pair
// Manifest.Diff compares to decide that a .bsl changed on disk (manifest.go), so
// the in-memory content cache and the incremental indexer agree on what "the
// file changed" means instead of each carrying its own notion.
//
// Two edits that land in the same millisecond AND keep the byte count identical
// are indistinguishable by this pair; that limit is inherited from the manifest
// and is not introduced here.
type fileStamp struct {
	modTime int64
	size    int64
}

// stampOf builds the stamp of an already-stat'ed file.
func stampOf(info os.FileInfo) fileStamp {
	return fileStamp{modTime: info.ModTime().UnixMilli(), size: info.Size()}
}

// statStamp stats path and returns its stamp. ok is false when the file cannot
// be stat'ed at all (removed, permission denied, a racing rename), so callers
// fail closed and treat the revision as unknown rather than as unchanged.
func statStamp(path string) (fileStamp, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, false
	}
	return stampOf(info), true
}

// cachedModule is one entry of the lazy content cache.
//
// fromFile marks an entry that was read from a file under the dump root and can
// therefore be revalidated against stamp. It is false for documents ingested at
// runtime through IndexDoc / IndexDocWithMeta: those have no file behind them,
// so there is nothing to compare against and they are always served as stored.
type cachedModule struct {
	content  string
	stamp    fileStamp
	fromFile bool
}

// resolveReal returns the absolute, fully symlink-resolved form of p. It returns
// ok=false when the path cannot be resolved (missing file, dangling symlink, or a
// race), so callers fail closed rather than trusting an unresolved path.
func resolveReal(p string) (string, bool) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	return real, true
}

// pathWithinRoot reports whether the file at path, with every symlink resolved,
// lies inside root (also resolved). It mirrors the EvalSymlinks containment used
// by the depgraph / compat dump-path validators: an in-root file — including an
// in-root symlink whose target stays under root — is contained and served, while
// a symlink whose real target escapes root is refused. Both sides are resolved so
// a symlinked root (e.g. macOS /var -> /private/var) does not cause a false
// mismatch. Anything that fails to resolve is treated as NOT contained.
//
// This guards against a malicious dump smuggling an outside host file in as a
// .bsl symlink (e.g. CommonModules/X/Ext/Module.bsl -> /etc/passwd), which would
// otherwise be read verbatim and exposed through search_code / GetContent.
func pathWithinRoot(root, path string) bool {
	rootReal, ok := resolveReal(root)
	if !ok {
		return false
	}
	pathReal, ok := resolveReal(path)
	if !ok {
		return false
	}
	if pathReal == rootReal {
		return true
	}
	return strings.HasPrefix(pathReal, rootReal+string(filepath.Separator))
}

// Match represents a single search hit in a BSL module.
type Match struct {
	Module string // Human-readable module path (e.g. "Документ.РеализацияТоваров.МодульОбъекта")
	// Line is the 1-based line number of the match, or 0 when the module matched
	// but no line of its current content could be identified as the hit (smart
	// mode only; regex and exact always report a real line). Context is empty
	// whenever Line is 0, because there is no line to quote.
	Line    int
	Context string  // Surrounding lines for context
	Score   float64 // BM25 relevance score (smart mode only)
}

// extractContext returns lines around the given index with a context window.
func extractContext(lines []string, idx, window int) string {
	start := max(idx-window, 0)
	end := min(idx+window+1, len(lines))
	return strings.Join(lines[start:end], "\n")
}

// synonymMapOnce ensures buildSynonymMap is called only once.
var (
	synonymMapOnce   sync.Once
	cachedSynonymMap map[string]string
)

// moduleNameSuffixes maps BSL file names to their module type suffix.
// The lookup key is the bare file name (last path segment), so each entry
// covers both the XML dump layout (.../Ext/<File>.bsl) and the EDT layout
// (.../<File>.bsl).
var moduleNameSuffixes = map[string]string{
	"ObjectModule.bsl":       "МодульОбъекта",
	"ManagerModule.bsl":      "МодульМенеджера",
	"Module.bsl":             "МодульФормы",
	"RecordSetModule.bsl":    "МодульНабораЗаписей",
	"CommandModule.bsl":      "МодульКоманды",
	"ValueManagerModule.bsl": "МодульМенеджераЗначения",
}

// subdirSegmentNames maps a dump path subdirectory to the Russian segment that
// names its child in a module name. A path passing through such a subdirectory
// gets an extra ".<segment>.<childName>." inserted (e.g. Forms/ФормаДок ->
// ".Форма.ФормаДок.").
var subdirSegmentNames = map[string]string{
	"Forms":    "Форма",
	"Commands": "Команда",
}

// plainModuleDirs lists the dump directories whose object stores its OWN module
// in a file literally named Module.bsl. moduleNameSuffixes maps "Module.bsl" to
// "МодульФормы" because that is what the name means for every kind that has
// forms; for these kinds the same file name is the object module and the suffix
// must be "Модуль" instead.
//
// CommonModules was the original and, for a while, the only member. HTTPServices
// and WebServices behave identically: the handler code of an HTTP or web service
// lives in <Kind>/<name>/Ext/Module.bsl. Membership here only ever narrows a
// suffix from "МодульФормы" to "Модуль"; the prefix is unaffected.
var plainModuleDirs = map[string]bool{
	"CommonModules": true,
	"HTTPServices":  true,
	"WebServices":   true,
}

// configModuleDirName is the top-level dump directory that holds the modules of
// the CONFIGURATION itself. Unlike every other top-level directory it is not a
// metadata kind: its .bsl files sit directly inside it, with no <objectName>
// level in between, so it must never get a dumpDirNames entry.
const configModuleDirName = "Ext"

// configModulePrefix is the key prefix for those modules. It stands in the slot
// a metadata kind occupies for every other module, because the owner of these
// four files is the configuration as a whole.
const configModulePrefix = "Конфигурация"

// configModuleSuffix is the module type of those modules, the third and last
// segment of their key. Both consumers that split a docID (parseModuleName and
// PathIndex.AddEntry) fill the module-type slot only from three segments up, so
// a shorter key would leave that slot empty and no module-type filter could ever
// select these four. Inside an extension the "ext.<ext>." prefix pushes the same
// key past that threshold, so a shorter shape would also make one logical module
// answer filters differently depending on where it lives.
//
// "Модуль" is not new vocabulary invented for this slot: moduleNameSuffixes and
// the plainModuleDirs rule above already give exactly this type to an object's
// own Module.bsl (ОбщийМодуль.ОбщегоНазначения.Модуль), and it is already one of
// the values documented for the "module" filter in tools/search.go.
const configModuleSuffix = "Модуль"

// configModuleNames maps the file names found directly under configModuleDirName
// to the Russian names of the configuration modules they hold. On a real dump
// that directory contains exactly these four files and nothing else.
//
// The Russian names are the property names of the platform type
// ОбъектМетаданныхКонфигурация, recorded in
// testdata/config_metadata_properties.txt (lines 49-52). The English-to-Russian
// pairing is by literal component correspondence, which is unambiguous here:
// each English name shares exactly one Russian counterpart (session -> Сеанс,
// managed application -> УправляемоеПриложение, external connection ->
// ВнешнееСоединение, ordinary application -> ОбычноеПриложение).
var configModuleNames = map[string]string{
	"ManagedApplicationModule.bsl":  "МодульУправляемогоПриложения",
	"SessionModule.bsl":             "МодульСеанса",
	"ExternalConnectionModule.bsl":  "МодульВнешнегоСоединения",
	"OrdinaryApplicationModule.bsl": "МодульОбычногоПриложения",
}

// extensionDirName is the top-level dump directory that holds configuration
// extensions ("Расширения"). Inside it each extension owns a subtree that
// mirrors the base-config layout: Расширения/<ext>/<Kind>/<name>/Ext/<File>.bsl.
const extensionDirName = "Расширения"

// bslPathToModuleName converts a relative file path from the dump to a human-readable module name.
// Example: "Documents/РеализацияТоваров/Ext/ObjectModule.bsl" -> "Документ.РеализацияТоваров.МодульОбъекта"
//
// Extension modules live under "Расширения/<ext>/" and mirror the base-config
// layout below that prefix. They are keyed as "ext.<ext>.<base-config name>",
// e.g. "Расширения/Доработки3D/CommonModules/WA_ПовтИсп/Ext/Module.bsl" ->
// "ext.Доработки3D.ОбщийМодуль.WA_ПовтИсп.Модуль". This matches the storage key
// the module resolver derives from a normalised user path
// (ext.<extension>.<Normalize(module)>), so code_read/module_code can find
// extension modules in a Hierarchical dump.
func bslPathToModuleName(relPath string) string {
	// Normalise separators.
	relPath = filepath.ToSlash(relPath)
	parts := strings.Split(relPath, "/")

	// The returned name is the KEY for every downstream map (idx.names,
	// contentByName, pathByName, pathToDocID, the Bleve doc id and the
	// PathIndex). macOS unpacks dumps with the decomposable Cyrillic letters
	// (short-I / IO) in Unicode NFD, whereas queries and XML-content-derived
	// names are NFC, so an NFD path-derived key would never match an NFC lookup.
	// Each return is therefore wrapped in NFC(...) to normalise the key at this
	// single chokepoint. The raw on-disk path used for file I/O is kept separately
	// by callers (pathByName values), so files still open. NFC is an allocation-
	// free no-op on already-NFC input (prod/Windows/HTTP).

	if len(parts) < 2 {
		return NFC(relPath)
	}

	// Extension subtree: Расширения/<ext>/<Kind>/<name>/.../<File>.bsl. Strip the
	// two leading segments and run the base-config parser on the remainder so the
	// CommonModules->Модуль special-case and the dumpDirNames/moduleNameSuffixes
	// maps apply exactly as for base config, then prefix with "ext.<ext>.".
	// A path too short to carry a full <Kind>/<name>/<File> remainder
	// (len(parts) < 4) falls through to the base parser unchanged, which keeps
	// the previous behaviour and never panics.
	if parts[0] == extensionDirName && len(parts) >= 4 {
		extName := parts[1]
		return NFC("ext." + extName + "." + baseConfigModuleName(parts[2:]))
	}

	return NFC(baseConfigModuleName(parts))
}

// baseConfigModuleName maps the path segments of a base-configuration BSL file
// to its human-readable module name (e.g. ["Documents","РеализацияТоваров",
// "Ext","ObjectModule.bsl"] -> "Документ.РеализацияТоваров.МодульОбъекта").
// It is also reused for the per-extension subtree by bslPathToModuleName, which
// passes the segments below "Расширения/<ext>/" and adds the "ext.<ext>." prefix.
//
// parts must have at least two segments; callers guarantee this.
func baseConfigModuleName(parts []string) string {
	// The configuration's own modules live directly under the root "Ext"
	// directory, so the path carries no object name and the generic mapping
	// misreads it: the file name lands in the object-name slot and the key ends
	// up carrying a literal ".bsl" in the middle with the name repeated as the
	// suffix ("Ext.SessionModule.bsl.SessionModule").
	//
	// The key keeps the generic three-slot <prefix>.<objectName>.<suffix> shape
	// and every slot means something: the owner is the configuration itself
	// (configModulePrefix), the module's own Russian name stands where an object
	// name stands, and the type is configModuleSuffix — the same "Модуль" an
	// object's own Module.bsl gets. Three segments is also the threshold below
	// which parseModuleName and PathIndex.AddEntry stop filling the module-type
	// slot, so a shorter key would be unreachable by any module-type filter, and
	// would behave differently inside an extension, where the "ext.<ext>." prefix
	// pushes the same key past that threshold on its own.
	//
	// The branch is deliberately narrow: exactly two segments AND a file name the
	// platform defines. Anything else directly under the root "Ext" keeps the old
	// fallback, so an unexpected file stays visibly odd instead of being silently
	// relabelled as a configuration module.
	//
	// bslPathToModuleName strips "Расширения/<ext>/" before calling this, so the
	// same branch covers an extension's own configuration modules: an extension
	// root mirrors the configuration root, Ext directory included.
	if len(parts) == 2 && parts[0] == configModuleDirName {
		if ru, ok := configModuleNames[parts[1]]; ok {
			return configModulePrefix + "." + ru + "." + configModuleSuffix
		}
	}

	// First part is the category directory.
	category := parts[0]
	prefix, ok := dumpDirNames[category]
	if !ok {
		prefix = category
	}

	objectName := parts[1]

	// Determine suffix from the file name.
	fileName := parts[len(parts)-1]
	suffix, ok := moduleNameSuffixes[fileName]
	if !ok {
		suffix = strings.TrimSuffix(fileName, ".bsl")
	}

	// Fix: the kinds in plainModuleDirs use "Модуль", not "МодульФормы", for
	// Module.bsl — that file is their object module, not a form module. The
	// Forms guard is the original one and keeps its intent: a path passing
	// through a plural "Forms" segment IS a form module and must stay
	// "МодульФормы" regardless of the kind.
	if plainModuleDirs[category] && fileName == "Module.bsl" {
		if !slices.Contains(parts, "Forms") {
			suffix = "Модуль"
		}
	}

	// If the path has a Forms/Commands subdirectory, include the form/command
	// name as an extra segment (e.g. ".Форма.ФормаДок." or ".Команда.Печать.").
	for i, p := range parts {
		if kind, ok := subdirSegmentNames[p]; ok && i+1 < len(parts) {
			childName := parts[i+1]
			return prefix + "." + objectName + "." + kind + "." + childName + "." + suffix
		}
	}

	return prefix + "." + objectName + "." + suffix
}

// SearchMode determines the search strategy.
type SearchMode string

const (
	SearchModeSmart SearchMode = "smart"
	SearchModeRegex SearchMode = "regex"
	SearchModeExact SearchMode = "exact"
)

// SearchStats reports how the matches a search hands back relate to the number
// it counted, so a caller can render an answer that does not contradict itself.
//
// It exists because the two numbers are produced by different halves of the
// search. Total comes from the index, which counts documents; the matches come
// from the render path, which re-reads each hit's module and refuses to serve
// content the file no longer holds. A dump rewritten under a live server (the
// normal middle state of a re-dump followed by reload_dump) makes the second
// number smaller than the first while nothing about it is an error, and the
// difference has to travel with the result rather than be inferred from it.
type SearchStats struct {
	// Total is the number of matches the index counts for the query. It is what
	// the search would return if every module behind it could still be read; for
	// regex and exact search it is the number of matching lines actually read.
	Total int

	// Unreadable is the number of hits this answer selected and then dropped
	// because the module's content could no longer be read: the file changed,
	// moved or vanished after it was indexed, and GetContent refuses to serve a
	// revision the dump does not hold. It counts hits WITHIN the answer the limit
	// selected, never the whole corpus: a search that never looked at the rest of
	// the index cannot know how many of those are readable.
	//
	// It is always 0 for regex and exact search, where an unreadable module drops
	// out of the scan before it can contribute to Total, so those two numbers
	// cannot disagree in the first place.
	Unreadable int
}

// SearchParams holds all parameters for a search query.
type SearchParams struct {
	Query    string
	Category string     // filter by metadata type, empty = all
	Module   string     // filter by module type, empty = all
	Mode     SearchMode // default: SearchModeSmart
	Limit    int        // default: 50, max: 500
}

// bslDocument is the struct indexed by Bleve. Field names must match mapping keys.
// Implements mapping.Classifier so Bleve routes it to the "module" document mapping.
type bslDocument struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Module   string `json:"module"`
	Content  string `json:"content"`
}

func (bslDocument) Type() string { return "module" }

// Index provides full-text search over BSL modules using Bleve.
type Index struct {
	dir     string
	alias   bleve.IndexAlias
	shards  []bleve.Index
	overlay bleve.Index // per-process in-memory bleve overlay for runtime extension ingest when base is read-only; merged into alias for smart search; nil in RW mode (writes go to shards as before)
	names   []string
	// contentByName caches module source keyed by docID, populated lazily by
	// GetContent and eagerly by the incremental updater. Every file-backed entry
	// carries the mtime+size of the revision it was read from and is discarded on
	// the next read once either moves, so an edited module is never served from a
	// copy the dump no longer holds.
	//
	// The cache is deliberately UNBOUNDED: no size cap, no TTL, no eviction. A
	// long-lived process that eventually reads every module ends up holding the
	// whole corpus. That is the accepted trade-off today, because entries are only
	// created on demand: a cold build leaves the map empty (see buildShards) and
	// regex/exact scans stream instead of caching (see contentForScan), so the map
	// grows only to what was actually requested rather than to dump size. Revisit
	// if a deployment reports resident memory tracking the corpus.
	contentByName map[string]cachedModule
	pathByName    map[string]string   // docID -> absolute file path (always populated)
	pathToDocID   map[string]string   // relative path (ToSlash) -> module name
	pathIndex     *PathIndex          // decomposed path index for fast category/module filtering
	lockDir       string              // cache dir whose serve-lock this index holds (empty = none); released in Close
	readOnly      bool                // true when shards were opened read-only (immutable generation serve); runtime base writes are rejected
	readerReg     *readerRegistration // live reader-registry handle for the served generation (nil = none); deregistered in Close
	// unprotected carries WHY the attached generation is being served without a
	// reader claim, or "" while it is protected. It is what UnprotectedReason
	// surfaces into the MCP tool response.
	//
	// It is an atomic and not a plain field guarded by mu because of who writes it
	// and who reads it: attachReadOnlyShards writes it from the background open
	// goroutine without mu, swapGeneration replaces it under mu, and every tool call
	// reads it on the request goroutine. An atomic is the one shape correct for all
	// three; a mu-guarded field would be a data race against the open, and reading
	// idx.readerReg directly would be the same race one level down.
	unprotected atomic.Pointer[string]
	// cacheDir is the cache location this index was opened with, in NewIndex
	// semantics (empty = the platform cache dir). Reload needs it to build the
	// replacement generation under the SAME cache the current one lives in;
	// re-resolving it from the environment could pick a different directory.
	cacheDir string
	// gensig names the immutable generation currently attached, or "" when the
	// index serves a legacy flat cache or an in-memory build (those have no
	// generation). Reload compares it against a freshly computed dump signature
	// to tell "nothing changed on disk" from "a rebuild is needed"; an empty
	// value therefore always rebuilds. Written under mu by the swap.
	gensig string
	// reloadMu serialises Reload against itself and against Close, so two
	// reloads never race to swap the shards and Close never frees shards a
	// reload is publishing. It is NOT taken by any read path.
	reloadMu  sync.Mutex
	closed    atomic.Bool // set by Close; Reload refuses afterwards
	ready     atomic.Bool
	mu        sync.RWMutex
	contentMu sync.RWMutex // protects lazy content loading
	buildErr  atomic.Pointer[error]
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
}

// Ready reports whether the index has finished building and is available for search.
func (idx *Index) Ready() bool {
	return idx.ready.Load()
}

// Done returns a channel that is closed when the background index build completes.
// This allows waiting for readiness without polling: <-index.Done()
func (idx *Index) Done() <-chan struct{} {
	return idx.done
}

// GetContent returns the BSL source code for the given module ID.
// Returns empty string and false if the module is not found or index is not ready.
//
// Content is lazy-loaded from disk on first access and cached. The cached copy is
// NOT served unconditionally: on every call the backing file is stat'ed and the
// entry is used only while its modification time and size both still match the
// revision it was read from — the same pair Manifest.Diff uses to detect a
// changed .bsl. When either moves, or the file can no longer be stat'ed, the
// entry counts as a miss and the file is re-read, so a module edited after
// indexing is never served stale. The invalidation is per read; there is no
// background watcher.
//
// Documents ingested at runtime through IndexDoc / IndexDocWithMeta have no file
// behind them and are always served exactly as stored.
func (idx *Index) GetContent(id string) (string, bool) {
	if !idx.ready.Load() {
		return "", false
	}

	// Defensive: normalise the lookup key to NFC so an NFD id (e.g. copy-pasted
	// from a macOS path) resolves against the NFC-keyed maps. No-op on NFC input.
	id = NFC(id)

	idx.mu.RLock()
	path, hasPath := idx.pathByName[id]
	idx.mu.RUnlock()

	// Fast path: serve the cached copy, but only while it still describes the
	// file it came from.
	idx.contentMu.RLock()
	entry, cached := idx.contentByName[id]
	idx.contentMu.RUnlock()
	if cached {
		if !entry.fromFile || !hasPath {
			// Runtime-ingested document: nothing on disk to compare against.
			return entry.content, true
		}
		if stamp, ok := statStamp(path); ok && stamp == entry.stamp {
			return entry.content, true
		}
		// The file moved on (or vanished): fall through and re-read it.
	}

	if !hasPath {
		return "", false
	}

	// Refuse to read a path whose real location escapes the dump root — a symlink
	// planted by a malicious dump pointing at an outside host file. In-root files
	// and in-root symlinks resolve within root and are served as before. This is
	// belt-and-suspenders with loadBSLPaths and the safety net for a path loaded
	// from a manifest written by a pre-fix binary. It runs before every disk read,
	// including a re-read triggered by revalidation.
	if !pathWithinRoot(idx.dir, path) {
		return "", false
	}

	// Stamp BEFORE reading, never after: if the file is rewritten between the two
	// syscalls we then cache newer content under an older stamp, and the next call
	// re-reads. Stamping after the read could pin stale content under the stamp of
	// a revision we never read, which would hide the change permanently.
	//
	// Read (with one retry) WITHOUT holding contentMu, so a transient read
	// failure's retry pause does not block other readers. A concurrent caller may
	// read the same file in parallel; that is harmless (identical content) and is
	// resolved by the double-check below.
	stamp, stamped := statStamp(path)
	content, ok := readModuleContent(path)
	if !ok {
		return "", false
	}
	if !stamped {
		// Content in hand but its revision is unknown, so it cannot be
		// revalidated later. Serve it and cache nothing rather than pin a copy
		// that would never be refreshed.
		return content, true
	}

	idx.contentMu.Lock()
	defer idx.contentMu.Unlock()
	// Double-check: another goroutine may have populated the cache meanwhile.
	// Keep what it stored when that entry is a runtime document or already the
	// same revision; replace it only when it is a superseded file revision.
	if existing, ok := idx.contentByName[id]; ok && (!existing.fromFile || existing.stamp == stamp) {
		return existing.content, true
	}
	idx.contentByName[id] = cachedModule{content: content, stamp: stamp, fromFile: true}
	return content, true
}

// loadedModule holds the result of reading a single .bsl file.
type loadedModule struct {
	name    string
	relPath string // forward-slash normalized relative path
	content string
	stamp   fileStamp // revision the content was read from, for cache revalidation
}

// NewIndex creates a new Index for the given dump directory. The index is built
// asynchronously in a background goroutine and becomes available when Ready()
// returns true. If reindex is true, any existing cache is discarded and rebuilt.
func NewIndex(dir, cacheDir string, reindex bool) (*Index, error) {
	ctx, cancel := context.WithCancel(context.Background())
	idx := &Index{
		dir:           dir,
		cacheDir:      cacheDir,
		alias:         bleve.NewIndexAlias(),
		contentByName: make(map[string]cachedModule),
		pathByName:    make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	cpath, cacheErr := cachePath(dir, cacheDir)
	useCache := cacheErr == nil

	if !useCache {
		// No writable cache location: os.UserCacheDir() failed and no
		// MCP_1C_CACHE_DIR override is set. Shards are then built in memory and
		// NOT persisted, so every start pays the full cold build (slow and
		// memory-intensive). Make this visible instead of failing silently.
		slog.Warn("Dump index cache disabled: no writable cache directory "+
			"(os.UserCacheDir failed and MCP_1C_CACHE_DIR is unset/unwritable). "+
			"The full-text index will be rebuilt in memory on every start. Set "+
			"MCP_1C_CACHE_DIR to a writable, persistent directory to enable the "+
			"on-disk cache.", "error", cacheErr)
		if showProgress.Load() {
			fmt.Fprintf(os.Stderr, "Внимание: кэш индекса отключён — нет доступной для "+
				"записи кэш-директории. Индекс будет строиться в памяти при каждом "+
				"запуске. Задайте MCP_1C_CACHE_DIR на постоянный каталог с правом записи.\n")
		}
	}

	if useCache && reindex {
		// Generation-aware reindex. The old behavior os.RemoveAll(cpath)'d the WHOLE
		// per-dump cache dir — including g/ and any immutable generation a concurrent
		// read-only serve holds (an unlink storm on unix, a hard failure on Windows
		// mmap'd files, and corruption of the holder's view). Instead, build a fresh
		// immutable generation (temp→READY→adopt) which by construction never touches
		// a live generation's files, then serve it read-only. The heavy build runs in
		// the background (Ready() flips when done) so `serve --reindex` start never
		// blocks the MCP initialize handshake.
		go func() {
			defer close(idx.done)
			if err := idx.reindexGeneration(dir, cacheDir); err != nil {
				idx.setBuildErr(err)
			}
		}()
		return idx, nil
	}

	// Try to open existing sharded cache.
	if useCache && !reindex {
		if shardDirs := cacheShardDirs(cpath); len(shardDirs) > 0 {
			// Schema gate: a legacy flat cache whose manifest was stamped under an
			// index schema / zap format the running binary cannot reuse (see the BUMP
			// PROTOCOL in generation.go) must NOT be served. Its shard docIDs and
			// manifest keys were produced under a different schema — e.g. before module
			// names were NFC-normalised, so a macOS dump's decomposable (short-I / IO)
			// names were stored NFD and never match an NFC query — so reusing it would
			// silently break module_code / resolve for those names. Drop the flat cache
			// (preserving any immutable g/ generations) and fall through to a cold
			// rebuild, which is non-blocking on the consumer side. A schema MATCH (or an
			// absent / incompatible-version manifest, which loadFromManifestAndDiff
			// handles via its own fallback walk) still reuses, so there is no gratuitous
			// rebuild.
			if flatCacheSchemaStale(cpath) {
				slog.Info("dump: dropping a legacy flat index cache built under an "+
					"incompatible index schema; it will be cold-rebuilt", "path", cpath)
				removeFlatCacheContents(cpath)
			} else if shards, err := openCachedShards(shardDirs, false, ""); err == nil {
				// Legacy flat layout stays read-WRITE: this path runs the incremental
				// warm-start diff (loadFromManifestAndDiff) which mutates the base
				// shards on drift. Concurrent same-dump serve uses the read-only
				// immutable-generation path (OpenGenerationReadOnly) instead.
				idx.shards = shards
				idx.alias.Add(shards...)
				idx.acquireCacheLock(cpath)

				// Fast startup: populate index from manifest, then apply incremental diff.
				go func() {
					defer close(idx.done)
					if err := idx.loadFromManifestAndDiff(cpath); err != nil {
						// Fallback: walk filesystem if manifest-based load fails.
						slog.Warn("Manifest load failed, falling back to walk", "error", err)
						if err := idx.loadBSLPaths(dir); err != nil {
							idx.setBuildErr(err)
							return
						}
					}

					idx.pathIndex = NewPathIndex(idx.names)
					idx.ready.Store(true)
					slog.Info("Opened cached index",
						"shards", len(shards), "modules", len(idx.names))
					if showProgress.Load() {
						fmt.Fprintf(os.Stderr, "[%s] Индекс загружен из кэша: %d модулей\n",
							time.Now().Format("15:04:05"), len(idx.names))
					}
				}()
				return idx, nil
			} else {
				// Cache corrupt — remove and rebuild.
				if err := os.RemoveAll(cpath); err != nil {
					slog.Warn("could not remove corrupt index cache before rebuild; "+
						"the rebuild may fail or mix stale shards",
						"path", cpath, "error", err)
				}
			}
		}
	}

	// No usable cache — full sharded build in background.
	if useCache {
		idx.acquireCacheLock(cpath)
	}
	go func() {
		defer close(idx.done)
		idx.buildShards(cpath, useCache)
	}()

	return idx, nil
}

// reindexGeneration performs a generation-aware reindex: it builds a fresh
// immutable generation for the current dump signature and attaches it to idx
// read-only. It NEVER wipes a generation a live reader holds — that is the core
// safety property. Old generations are left on disk and reaped by the GC pass at
// the end. Runs in NewIndex's background goroutine; the caller closes idx.done.
func (idx *Index) reindexGeneration(dir, cacheDir string) error {
	gensig, err := GenSig(dir)
	if err != nil {
		// Cannot compute a generation signature (e.g. the dump dir is unreadable).
		// Fall back to a legacy flat rebuild that still preserves g/ — never wipe the
		// generations subtree, which a concurrent reader may hold.
		slog.Warn("reindex: could not compute generation signature; falling back to a "+
			"flat rebuild (generations preserved)", "dir", dir, "error", err)
		if cpath, cerr := cachePath(dir, cacheDir); cerr == nil {
			removeFlatCacheContents(cpath)
			idx.acquireCacheLock(cpath)
			idx.buildShards(cpath, true)
		} else {
			idx.buildShards("", false) // no writable cache: in-memory build
		}
		return idx.BuildError()
	}

	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return fmt.Errorf("reindex: no writable cache directory: %w", err)
	}
	genDir := generationDir(cpath, gensig)

	// Force-rebuild semantics: a build is content-addressed and no-ops on an
	// already-READY gensig, so to honor --reindex (e.g. recovering a corrupt cache)
	// drop the current generation first — but ONLY if no live reader holds it (never
	// wipe a generation a concurrent serve has memory-mapped). forceDropGeneration is
	// the single force-drop primitive shared with the concurrent serve path.
	//
	// withClaim, and the claim is handed straight to the attach below. Building and
	// only then claiming would publish this generation into the shared arena READY
	// and held by nobody, and a co-located reaper firing in that window deletes the
	// generation this reindex is about to serve.
	reg, err := forceRebuildGeneration(dir, cacheDir, gensig, withClaim)
	if err != nil {
		return fmt.Errorf("reindex: building generation: %w", err)
	}

	if err := idx.attachReadOnlyShards(genDir, reg); err != nil {
		return err
	}
	if err := idx.loadNamesReadOnly(genDir); err != nil {
		return err
	}
	idx.pathIndex = NewPathIndex(idx.names)
	idx.ready.Store(true)

	// Now that a fresh generation is current, reap old, unheld generations.
	if dropped, gcErr := GCGenerations(dir, cacheDir, gensig); gcErr != nil {
		slog.Warn("reindex: GC of old generations failed", "error", gcErr)
	} else if len(dropped) > 0 {
		slog.Info("reindex: GC removed old generations", "count", len(dropped))
	}

	slog.Info("Reindex built and adopted a fresh generation",
		"gen", gensig, "modules", len(idx.names))
	if showProgress.Load() {
		fmt.Fprintf(os.Stderr, "[%s] Переиндексация завершена: %d модулей (поколение %s)\n",
			time.Now().Format("15:04:05"), len(idx.names), gensig)
	}
	return nil
}

// acquireCacheLock marks cpath as in use by this process for the lifetime of the
// index (released in Close), so a concurrent `--build-index` does not clobber a
// cache the running server has memory-mapped. Best-effort: a failure to write the
// lock is logged but does not stop the server.
func (idx *Index) acquireCacheLock(cpath string) {
	if cpath == "" {
		return
	}
	if err := writeCacheLock(cpath); err != nil {
		slog.Warn("dump: could not write cache lock; a concurrent --build-index "+
			"could clobber this cache", "path", cpath, "error", err)
		return
	}
	idx.lockDir = cpath
}

// BuildCache synchronously builds (or refreshes) the on-disk search-index cache
// for dir and returns once the build has completed and been persisted. It is the
// offline pre-warm entry point behind the --build-index CLI flag: running it
// before `serve` lets the server open a warm cache instead of paying the
// expensive in-memory cold build (high transient RSS) on first start.
//
// cacheDir follows NewIndex semantics: empty selects the platform cache dir
// (os.UserCacheDir), otherwise the given directory is used. reindex forces a full
// rebuild. BuildCache returns an error if no writable cache location is available
// (nothing would be persisted, so every serve would rebuild) or if the build
// fails.
func BuildCache(dir, cacheDir string, reindex bool) error {
	start := time.Now()
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		return fmt.Errorf("no writable cache directory (set MCP_1C_CACHE_DIR to a writable path): %w", err)
	}

	// Refuse to rebuild a cache that a running server (or another build) has open
	// and memory-mapped: a destructive rebuild would corrupt that process's view
	// and/or race its writes. The lock is written by NewIndex while a cache is
	// open and removed on Close.
	if pid, present := readCacheLock(cpath); present {
		who := "another mcp-1c process"
		if pid > 0 {
			who = fmt.Sprintf("mcp-1c (pid %d)", pid)
		}
		return fmt.Errorf("index cache %s is in use by %s; stop the running server "+
			"before using --build-index. If no server is running this is a stale lock — "+
			"delete %s and retry", cpath, who, filepath.Join(cpath, serveLockName))
	}

	idx, err := NewIndex(dir, cacheDir, reindex)
	if err != nil {
		return err
	}
	defer idx.Close()

	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		return fmt.Errorf("building index cache: %w", err)
	}
	if !idx.Ready() {
		return fmt.Errorf("index build did not complete")
	}

	// Issue #26: the cache folder is named after an opaque hash of the dump's
	// absolute path, so it is not obvious which dump a folder maps to. Print the
	// folder and drop a human-readable dump.json (dump path, module count, build
	// timing) into it. Both run only after a successful build and are best-effort,
	// so a failure here never fails the build.
	fmt.Fprintf(os.Stderr, "Папка индексов: %s\n", cpath)
	writeDumpInfo(cpath, dir, idx.ModuleCount(), time.Since(start))

	return nil
}

// BuildVersion is the mcp-1c binary version string (the same value printed by
// `mcp-1c version`, injected via -ldflags "-X main.version=..."). main sets it at
// startup. It is recorded in dump.json so a cache folder's mapping file shows
// which build produced it. The dump package cannot import main, hence this var;
// it is empty for non-main callers (e.g. tests), in which case the field is
// omitted from the JSON.
var BuildVersion string

// dumpInfo is the human-readable mapping written to <hash>/dump.json. The cache
// folder is named sha256(abs dump path)[:8], which is opaque; this file records
// the dump path and build facts so a folder can be traced back to its dump
// (issue #26). The schema field lets the format grow compatibly.
type dumpInfo struct {
	Schema       int     `json:"schema"`
	DumpPath     string  `json:"dump_path"`
	Modules      int     `json:"modules"`
	BuildSeconds float64 `json:"build_seconds"`
	BuiltAt      string  `json:"built_at"`
	Version      string  `json:"mcp_1c_version,omitempty"`
	Platform     string  `json:"platform"`
}

// writeDumpInfo writes the human-readable dump.json into the TOP-LEVEL cache
// folder cpath, mapping it to dumpDir. dumpDir is absolutized so dump_path matches
// the absolute path the folder hash is derived from (cachePath hashes the abs
// path; idx.dir is the raw, possibly relative, dir passed to NewIndex). cpath must
// be the stable top-level <hash> dir, never a per-generation dir under g/ (those
// are reaped by GCGenerations). Best-effort: any failure is logged and ignored so
// it never fails an otherwise-successful index build.
func writeDumpInfo(cpath, dumpDir string, modules int, elapsed time.Duration) {
	absDir := dumpDir
	if abs, err := filepath.Abs(dumpDir); err == nil {
		absDir = abs
	}
	data, err := json.MarshalIndent(dumpInfo{
		Schema:       1,
		DumpPath:     absDir,
		Modules:      modules,
		BuildSeconds: elapsed.Seconds(),
		BuiltAt:      time.Now().Format(time.RFC3339),
		Version:      BuildVersion,
		Platform:     runtime.GOOS,
	}, "", "  ")
	if err != nil {
		slog.Warn("dump: could not encode dump.json; cache folder mapping skipped", "error", err)
		return
	}
	out := filepath.Join(cpath, "dump.json")
	if err := os.WriteFile(out, data, 0o644); err != nil {
		slog.Warn("dump: could not write dump.json; cache folder mapping skipped",
			"path", out, "error", err)
	}
}

// setBuildErr stores a build error atomically.
func (idx *Index) setBuildErr(err error) {
	idx.buildErr.Store(&err)
}

// buildShards loads BSL files and builds N shards in parallel.
func (idx *Index) buildShards(cpath string, useCache bool) {
	// Load module paths only (no file content). The shard builders below read
	// each .bsl from disk on demand, so the full corpus (hundreds of MB) is never
	// resident at once — the dominant cold-build memory peak. Sort names into the
	// same lexicographic order loadBSLFiles produced, which reproducible shard
	// keys and stable regex/exact scan order depend on.
	if err := idx.loadBSLPaths(idx.dir); err != nil {
		idx.setBuildErr(fmt.Errorf("loading BSL paths: %w", err))
		return
	}
	slices.Sort(idx.names)

	total := len(idx.names)
	if total == 0 {
		idx.pathIndex = NewPathIndex(nil)
		idx.ready.Store(true)
		slog.Info("No BSL modules found, index is empty")
		if showProgress.Load() {
			fmt.Fprintf(os.Stderr, "Внимание: в директории %s не найдено .bsl файлов\n", idx.dir)
		}
		return
	}

	n := shardCount(total)
	groups := splitByHash(idx.names, n)
	slog.Info("Building index", "modules", total, "shards", n)
	if showProgress.Load() {
		fmt.Fprintf(os.Stderr, "[%s] Индексация: найдено %d модулей...\n",
			time.Now().Format("15:04:05"), total)
	}

	var basePath string
	if cpath != "" && useCache {
		os.MkdirAll(cpath, 0o755)
		basePath = cpath
	}

	// Persister nap time (favouring in-memory segment merging) is applied per-index
	// via scorchPersisterOptions inside buildShard, not by writing scorch's process
	// global DefaultPersisterNapTimeMSec. The global raced when a sibling base
	// attached read-only shards while this base was building (see scorchPersisterCfg).

	// Build the BSL mapping once and share across all shards.
	bslMapping := buildBSLMapping()

	// Content resolver: shard builders read each module's source from disk on
	// demand (bounded memory) instead of from a fully-resident content map.
	// pathByName is only read during the build, so concurrent access from the
	// shard goroutines needs no lock.
	pathByName := idx.pathByName
	getContent := func(name string) string {
		path := pathByName[name]
		if path == "" {
			return ""
		}
		// readModuleContent retries once and logs a rate-limited warning on
		// failure, so a file unreadable at build time (lock / antivirus /
		// cloud-sync) is not silently indexed with empty content.
		content, _ := readModuleContent(path)
		return content
	}

	// Tighten GC for the parallel build instead of disabling it. Previously the
	// whole build ran with GC OFF (debug.SetGCPercent(-1)), so tokenization and
	// analysis transients accumulated across every shard and inflated peak RSS
	// many-fold on a cold build — the serve-time OOM. A lower-than-default GC
	// target keeps heap headroom small during this allocation-heavy phase and is
	// restored afterwards. It is relative (no fixed byte budget), so it adapts to
	// any config size. With GC enabled, a process memory limit set via
	// debug.SetMemoryLimit (e.g. the Advanced --memory-limit flag) is now also
	// honoured — it was a no-op while GC was disabled.
	oldGC := debug.SetGCPercent(buildGCPercent)
	defer debug.SetGCPercent(oldGC)

	start := time.Now()
	var indexed atomic.Int64

	type shardResult struct {
		index bleve.Index
		id    int
		err   error
	}
	results := make(chan shardResult, n)

	// Progress ticker: only active for interactive terminal launches. Writing to
	// stderr in pipe/MCP mode can trigger restart loops in strict MCP clients.
	stopProgress := make(chan struct{})
	tickerActive := showProgress.Load()
	if tickerActive {
		ticker := time.NewTicker(500 * time.Millisecond)
		go func() {
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					done := indexed.Load()
					pct := done * 100 / int64(total)
					fmt.Fprintf(os.Stderr, "\rИндексация: %d/%d (%d%%)   ", done, total, pct)
				case <-stopProgress:
					fmt.Fprintf(os.Stderr, "\r%80s\r", "")
					return
				}
			}
		}()
	}

	for i := range n {
		go func(shardID int) {
			select {
			case <-idx.ctx.Done():
				results <- shardResult{id: shardID, err: idx.ctx.Err()}
				return
			default:
			}

			var shardPath string
			if basePath != "" {
				shardPath = filepath.Join(basePath, fmt.Sprintf("shard_%d", shardID))
			}

			shard, err := buildShard(shardPath, groups[shardID], getContent, shardID, n, bslMapping, &indexed)
			results <- shardResult{index: shard, id: shardID, err: err}
		}(i)
	}

	// Collect. Always receive all n to avoid goroutine leak.
	shards := make([]bleve.Index, n)
	var firstErr error
	for range n {
		res := <-results
		if res.err != nil && firstErr == nil {
			firstErr = res.err
			idx.cancel()
		}
		if res.index != nil {
			shards[res.id] = res.index
		}
	}
	if tickerActive {
		close(stopProgress)
	}
	if firstErr != nil {
		for _, s := range shards {
			if s != nil {
				s.Close()
			}
		}
		if cpath != "" {
			if err := os.RemoveAll(cpath); err != nil {
				slog.Warn("could not remove partial index cache after build failure",
					"path", cpath, "error", err)
			}
		}
		idx.setBuildErr(firstErr)
		return
	}

	idx.shards = shards
	idx.alias.Add(shards...)
	idx.pathIndex = NewPathIndex(idx.names)

	// contentByName is intentionally left empty: the cold build streamed content
	// from disk (see getContent above) and never populated it. GetContent lazily
	// loads individual modules from disk via pathByName, and regex/exact scans
	// stream content (see searchLineByLine/contentForScan), so the full corpus is
	// never resident after the build either.

	idx.ready.Store(true)

	// Save manifest for future incremental updates.
	if cpath != "" && useCache {
		idx.saveManifest(cpath)
	}

	slog.Info("Index ready", "modules", total, "shards", n, "elapsed", time.Since(start))
	if showProgress.Load() {
		fmt.Fprintf(os.Stderr, "Индексация завершена за %.1fс: %d модулей готово к поиску\n",
			time.Since(start).Seconds(), total)
	}
}

// scorchPersisterNapTimeMSec is the persister nap time (milliseconds) applied
// per-index via the "scorchPersisterOptions" scorch config to favour in-memory
// segment merging during indexing. It replaces a write to scorch's process-global
// DefaultPersisterNapTimeMSec, which raced when one base built shards while a
// sibling base concurrently attached read-only shards (parsePersisterOptions reads
// that global on every index open). Keeping the value out of the shared global
// removes the race while preserving the same 500ms nap behaviour.
const scorchPersisterNapTimeMSec = 500

// scorchPersisterCfg returns a fresh per-index persister-options map (a new map on
// every call so concurrently opened indexes never share one instance). Use it as
// the "scorchPersisterOptions" entry of a bleve scorch config map; scorch
// JSON-decodes it into persisterOptions.PersisterNapTimeMSec.
func scorchPersisterCfg() map[string]any {
	return map[string]any{"PersisterNapTimeMSec": scorchPersisterNapTimeMSec}
}

// openCachedShards opens pre-built Bleve shard indexes from disk.
// On any error, all previously opened shards are closed.
//
// When readOnly is true the shards are opened with scorch's read_only mode
// (bleve.OpenUsing(..., {"read_only": true})), which takes a bbolt LOCK_SH on
// each shard's root.bolt instead of the default exclusive LOCK_EX. That lets N
// processes open the SAME generation concurrently — the core of concurrent
// same-dump serve. boltTimeout bounds how long a conflicting open waits for the
// flock before failing; it MUST be a Go duration STRING (e.g. "5s"): scorch
// reads bolt_timeout via config["bolt_timeout"].(string)+time.ParseDuration, so
// a wrong type (int / time.Duration) is silently dropped and the open reverts to
// the wait-forever default (Timeout=0) — the original infinite hang. An empty
// boltTimeout leaves scorch's default (wait indefinitely); pass a non-empty
// value whenever a conflicting holder is possible.
func openCachedShards(dirs []string, readOnly bool, boltTimeout string) ([]bleve.Index, error) {
	shards := make([]bleve.Index, len(dirs))
	for i, dir := range dirs {
		var (
			blevIdx bleve.Index
			err     error
		)
		if readOnly {
			cfg := map[string]any{
				"read_only":              true,
				"scorchPersisterOptions": scorchPersisterCfg(),
			}
			if boltTimeout != "" {
				cfg["bolt_timeout"] = boltTimeout // MUST be a duration STRING — see doc above
			}
			blevIdx, err = bleve.OpenUsing(dir, cfg)
		} else {
			blevIdx, err = bleve.OpenUsing(dir, map[string]any{
				"scorchPersisterOptions": scorchPersisterCfg(),
			})
		}
		if err != nil {
			for j := range i {
				shards[j].Close()
			}
			return nil, fmt.Errorf("opening shard %d: %w", i, err)
		}
		shards[i] = blevIdx
	}
	return shards, nil
}

// buildIndexBuilder creates a Bleve index using the offline builder (bleve.NewBuilder).
// This approach bypasses Scorch persister/merger goroutines and is faster for bulk loading.
// The builder writes segments to disk, merges them, and produces a ready-to-open index.
// After builder.Close(), the index is opened with bleve.Open().
// Requires a non-empty indexPath (cannot work in-memory).
func buildIndexBuilder(indexPath string, names []string, contentByName map[string]string) (bleve.Index, error) {
	bslMapping := buildBSLMapping()

	// Co-locate the offline builder's scratch on the destination filesystem so its
	// final segment rename never crosses devices (EXDEV). See coLocateBuildScratch.
	scratchPrefix, cleanupScratch, err := coLocateBuildScratch(indexPath)
	if err != nil {
		return nil, err
	}
	defer cleanupScratch()

	builder, err := bleve.NewBuilder(indexPath, bslMapping, map[string]any{
		"forceSegmentType":    "zap",
		"forceSegmentVersion": zapSegmentVersion, // folded into GenSig — see BUMP PROTOCOL
		"batchSize":           5000,
		"buildPathPrefix":     scratchPrefix,
	})
	if err != nil {
		return nil, fmt.Errorf("creating bleve builder: %w", err)
	}

	total := len(names)

	for _, name := range names {
		parts := parseModuleName(name)

		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  contentByName[name],
		}

		if err := builder.Index(name, doc); err != nil {
			builder.Close()
			return nil, fmt.Errorf("builder indexing doc %q: %w", name, err)
		}
	}
	if total > 0 {
		slog.Info("Indexing BSL modules done", "count", total)
	}

	if err := builder.Close(); err != nil {
		return nil, fmt.Errorf("closing bleve builder: %w", err)
	}

	blevIdx, err := bleve.OpenUsing(indexPath, map[string]any{
		"scorchPersisterOptions": scorchPersisterCfg(),
	})
	if err != nil {
		return nil, fmt.Errorf("opening built index: %w", err)
	}

	return blevIdx, nil
}

// buildIndexBatch creates a Bleve index using NewUsing + manual batch operations.
// This is the fallback for in-memory builds where NewBuilder cannot be used.
func buildIndexBatch(indexPath string, names []string, contentByName map[string]string) (bleve.Index, error) {
	bslMapping := buildBSLMapping()

	// Persister nap time favours in-memory segment merging with unsafe_batch. Set
	// per-index via scorchPersisterOptions instead of scorch's process-global
	// DefaultPersisterNapTimeMSec, so it never races a concurrent attach.
	blevIdx, err := bleve.NewUsing(indexPath, bslMapping, "scorch", "scorch", map[string]any{
		"unsafe_batch":           true,
		"scorchPersisterOptions": scorchPersisterCfg(),
	})
	if err != nil {
		return nil, fmt.Errorf("creating bleve index: %w", err)
	}

	total := len(names)
	const batchSize = 5000

	batch := blevIdx.NewBatch()
	for i, name := range names {
		parts := parseModuleName(name)

		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  contentByName[name],
		}

		batch.Index(name, doc)

		if (i+1)%batchSize == 0 || i+1 == total {
			if err := blevIdx.Batch(batch); err != nil {
				blevIdx.Close()
				return nil, fmt.Errorf("indexing batch: %w", err)
			}
			batch = blevIdx.NewBatch()
		}

	}
	if total > 0 {
		slog.Info("Indexing BSL modules done", "count", total)
	}

	return blevIdx, nil
}

// loadBSLFiles walks the dump directory and reads all .bsl files in parallel,
// populating idx.names and idx.contentByName.
func (idx *Index) loadBSLFiles(dir string) error {
	// Phase 1: collect all .bsl file paths (fast directory walk, no file I/O).
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".bsl") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking dump directory: %w", err)
	}

	// Phase 2: read files in parallel using a worker pool.
	results := make(chan loadedModule, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())

	for _, p := range paths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Stamp before reading so the cached entry can never claim a revision
			// newer than the bytes it holds (see GetContent for why the order
			// matters). A file that cannot be stat'ed is skipped like an
			// unreadable one.
			stamp, stamped := statStamp(path)
			if !stamped {
				return
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return // skip unreadable files
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return
			}
			relSlash := filepath.ToSlash(rel)
			name := bslPathToModuleName(rel)
			results <- loadedModule{name: name, relPath: relSlash, content: stripBOM(string(data)), stamp: stamp}
		}(p)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results from workers.
	if idx.pathToDocID == nil {
		idx.pathToDocID = make(map[string]string, len(paths))
	}
	for m := range results {
		idx.names = append(idx.names, m.name)
		idx.contentByName[m.name] = cachedModule{content: m.content, stamp: m.stamp, fromFile: true}
		idx.pathToDocID[m.relPath] = m.name
		// Also store absolute path for lazy-load compatibility.
		absPath := filepath.Join(dir, filepath.FromSlash(m.relPath))
		idx.pathByName[m.name] = absPath
	}

	// Workers finish in nondeterministic timing order, so the resulting
	// idx.names slice depends on goroutine scheduling. Sort lexicographically
	// to guarantee a stable enumeration order across runs on the same dump,
	// which is required for reproducible cache keys, chunking, vocabulary
	// and TF-IDF downstream. Maps (contentByName, pathToDocID, pathByName)
	// are unaffected because they are keyed by name/relPath.
	slices.Sort(idx.names)

	return nil
}

// loadBSLPaths walks the dump directory and collects file paths without reading content.
// Populates idx.names, idx.pathByName, and idx.pathToDocID.
// This is the fast startup path (~0.5s) used when cached shards exist.
func (idx *Index) loadBSLPaths(dir string) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".bsl") {
			return nil
		}
		// Refuse a .bsl leaf that is a symlink escaping the dump root: following it
		// would index an outside host file (arbitrary-file exfiltration through
		// search_code / GetContent). WalkDir does not descend into symlinked
		// directories, so only a symlinked leaf can escape; an in-root symlink
		// (target under root) is kept and served. Mirrors the depgraph / compat
		// EvalSymlinks containment. The symlink-type gate keeps the fast startup
		// path allocation-free for the ordinary regular-file case.
		if d.Type()&fs.ModeSymlink != 0 && !pathWithinRoot(dir, path) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		name := bslPathToModuleName(rel)
		absPath, err := filepath.Abs(path)
		if err != nil {
			absPath = path
		}
		idx.names = append(idx.names, name)
		idx.pathByName[name] = absPath
		if idx.pathToDocID == nil {
			idx.pathToDocID = make(map[string]string)
		}
		idx.pathToDocID[relSlash] = name
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking dump directory: %w", err)
	}
	return nil
}

// contentForScan returns the BSL source for name for a full-index scan. It uses
// the in-memory content cache when the module is present there, otherwise it
// reads the file from disk via pathByName WITHOUT caching the result.
//
// Regex/exact search scans every candidate module, so caching here would re-grow
// contentByName to the full corpus size — the very allocation the cold-build fix
// drops after building (see buildShards). Streaming instead keeps a full scan's
// memory bounded to one file at a time, reclaimed by the GC. A cached entry is
// used only while its modification time and size still match the file it was read
// from (same rule as GetContent), so a scan never matches against a revision the
// dump no longer holds.
func (idx *Index) contentForScan(name string) (string, bool) {
	idx.mu.RLock()
	path, hasPath := idx.pathByName[name]
	idx.mu.RUnlock()

	idx.contentMu.RLock()
	entry, cached := idx.contentByName[name]
	idx.contentMu.RUnlock()
	if cached {
		if !entry.fromFile || !hasPath {
			// Runtime-ingested document: nothing on disk to compare against.
			return entry.content, true
		}
		if stamp, ok := statStamp(path); ok && stamp == entry.stamp {
			return entry.content, true
		}
		// Superseded revision: fall through to a fresh read of the file.
	}

	if !hasPath {
		return "", false
	}

	// Same dump-root containment as GetContent: never read a path that escapes
	// the dump root via a symlink (regex / exact scan path).
	if !pathWithinRoot(idx.dir, path) {
		return "", false
	}

	return readModuleContent(path)
}

// moduleNameParts holds the parsed components of a human-readable module name.
type moduleNameParts struct {
	category string // e.g. "Справочник"
	name     string // e.g. "Номенклатура"
	module   string // e.g. "МодульОбъекта"
}

// parseModuleName splits "Справочник.Номенклатура.МодульОбъекта" into parts.
// For form paths like "Документ.Док.Форма.ФормаДок.МодульФормы", the module type
// is the last dot-separated segment ("МодульФормы"), not the third segment.
func parseModuleName(fullName string) moduleNameParts {
	parts := strings.Split(fullName, ".")
	switch {
	case len(parts) >= 3:
		return moduleNameParts{
			category: parts[0],
			name:     parts[1],
			module:   parts[len(parts)-1],
		}
	case len(parts) == 2:
		return moduleNameParts{category: parts[0], name: parts[1]}
	default:
		return moduleNameParts{name: fullName}
	}
}

// IndexDoc adds or replaces a document in the index at runtime.
// The document is routed to a shard by FNV-1a hash of the id.
// It updates contentByName and names (with dedup), so ModuleCount and all
// search modes (regex, exact, smart) reflect the new document immediately.
// Requires Ready() == true.
func (idx *Index) IndexDoc(id string, content string) error {
	if !idx.ready.Load() {
		return fmt.Errorf("index not ready: cannot IndexDoc while building")
	}
	// Snapshot the write target under mu: Reload replaces readOnly and the shard
	// slice together while readers run, so reading them unlocked would race it.
	// The bleve write below then goes to the snapshotted shard rather than to
	// whatever the field points at by the time it runs; if a reload retired that
	// shard meanwhile, bleve returns ErrorIndexClosed (its Index() checks an open
	// flag under its own lock) and the caller can retry. It never panics, and a
	// half-written shard is impossible because a retired shard is closed only
	// after the swap has published its replacement.
	readOnly, shards := idx.writeTarget()
	if readOnly {
		return fmt.Errorf("index opened read-only: cannot IndexDoc (extension overlay not yet available)")
	}
	if len(shards) == 0 {
		return fmt.Errorf("index has no shards")
	}

	parts := parseModuleName(id)
	doc := bslDocument{
		Name:     parts.name,
		Category: parts.category,
		Module:   parts.module,
		Content:  content,
	}

	si := shardForID(id, len(shards))
	if err := shards[si].Index(id, doc); err != nil {
		return fmt.Errorf("indexing doc %q in shard %d: %w", id, si, err)
	}

	// Check existence under both locks to decide whether this is a new doc.
	idx.contentMu.RLock()
	_, inContent := idx.contentByName[id]
	idx.contentMu.RUnlock()

	idx.mu.Lock()
	_, inPath := idx.pathByName[id]
	if !inContent && !inPath {
		idx.names = append(idx.names, id)
		if idx.pathIndex != nil {
			idx.pathIndex.AddEntry(id)
		}
	}
	idx.mu.Unlock()

	// fromFile stays false: the caller supplied this source, it was not read from
	// a file under the dump root, so there is no revision to revalidate against
	// and GetContent must keep serving exactly what was ingested.
	idx.contentMu.Lock()
	idx.contentByName[id] = cachedModule{content: content}
	idx.contentMu.Unlock()

	return nil
}

// ensureOverlay lazily creates the per-process in-memory bleve overlay used for
// runtime extension ingest when the base shards are read-only (immutable
// generation serve). The overlay is added to the search alias so smart search
// merges base (read-only, shared) + overlay (in-memory, per-process). Created at
// most once per Index (guarded by idx.mu); closed in Close.
func (idx *Index) ensureOverlay() (bleve.Index, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.overlay != nil {
		return idx.overlay, nil
	}
	ov, err := bleve.NewUsing("", buildBSLMapping(), "scorch", "scorch", map[string]any{
		"unsafe_batch":           true,
		"scorchPersisterOptions": scorchPersisterCfg(),
	})
	if err != nil {
		return nil, fmt.Errorf("creating extension overlay: %w", err)
	}
	if idx.alias != nil {
		idx.alias.Add(ov)
	}
	idx.overlay = ov
	return ov, nil
}

// IndexDocWithMeta adds or replaces a document in the index with explicit metadata.
// Unlike IndexDoc, it does NOT call parseModuleName — category and module are set directly.
// The document is routed to a shard by FNV-1a hash of the id.
// Requires Ready() == true.
func (idx *Index) IndexDocWithMeta(id, content, category, module string) error {
	if !idx.ready.Load() {
		return fmt.Errorf("index not ready: cannot IndexDocWithMeta while building")
	}

	doc := bslDocument{
		Name:     id,
		Category: category,
		Module:   module,
		Content:  content,
	}

	readOnly, shards := idx.writeTarget() // snapshot under mu; see IndexDoc
	if readOnly {
		// Base shards are immutable (read-only generation serve). Live
		// extensions are per-process, so they go to an in-memory overlay that
		// is merged into the search alias; the immutable base is never written.
		// The overlay is NOT touched by a reload, so an ingested extension
		// survives one.
		ov, err := idx.ensureOverlay()
		if err != nil {
			return err
		}
		if err := ov.Index(id, doc); err != nil {
			return fmt.Errorf("indexing doc %q in overlay: %w", id, err)
		}
	} else {
		if len(shards) == 0 {
			return fmt.Errorf("index has no shards")
		}
		si := shardForID(id, len(shards))
		if err := shards[si].Index(id, doc); err != nil {
			return fmt.Errorf("indexing doc %q in shard %d: %w", id, si, err)
		}
	}

	// Check existence under both locks to decide whether this is a new doc.
	idx.contentMu.RLock()
	_, inContent := idx.contentByName[id]
	idx.contentMu.RUnlock()

	idx.mu.Lock()
	_, inPath := idx.pathByName[id]
	if !inContent && !inPath {
		idx.names = append(idx.names, id)
		if idx.pathIndex != nil {
			idx.pathIndex.AddEntryWithMeta(id, category, module)
		}
	}
	idx.mu.Unlock()

	// fromFile stays false for the same reason as in IndexDoc: a runtime-ingested
	// document has no file behind it to revalidate against.
	idx.contentMu.Lock()
	idx.contentByName[id] = cachedModule{content: content}
	idx.contentMu.Unlock()

	return nil
}

// DeleteDoc removes a document from the index at runtime.
// The shard is determined by FNV-1a hash of the id (same routing as IndexDoc).
// It removes from both contentByName and names, so ModuleCount and all search
// modes (regex, exact, smart) no longer see the deleted document.
// Requires Ready() == true.
func (idx *Index) DeleteDoc(id string) error {
	if !idx.ready.Load() {
		return fmt.Errorf("index not ready: cannot DeleteDoc while building")
	}

	readOnly, shards := idx.writeTarget() // snapshot under mu; see IndexDoc
	if readOnly {
		// Extension docs live only in the in-memory overlay (base is immutable,
		// and DeleteDoc is only ever called with ext.-prefixed IDs). Remove from
		// the overlay if one exists; if nothing was ever ingested there is
		// nothing to delete.
		idx.mu.RLock()
		ov := idx.overlay
		idx.mu.RUnlock()
		if ov != nil {
			if err := ov.Delete(id); err != nil {
				return fmt.Errorf("deleting doc %q from overlay: %w", id, err)
			}
		}
	} else {
		if len(shards) == 0 {
			return fmt.Errorf("index has no shards")
		}
		si := shardForID(id, len(shards))
		if err := shards[si].Delete(id); err != nil {
			return fmt.Errorf("deleting doc %q from shard %d: %w", id, si, err)
		}
	}

	idx.contentMu.Lock()
	delete(idx.contentByName, id)
	idx.contentMu.Unlock()

	idx.mu.Lock()
	delete(idx.pathByName, id)
	if idx.pathIndex != nil {
		idx.pathIndex.RemoveEntry(id)
	}
	for i, n := range idx.names {
		if n == id {
			idx.names = append(idx.names[:i], idx.names[i+1:]...)
			break
		}
	}
	idx.mu.Unlock()

	return nil
}

// Search finds matches in indexed BSL modules. Dispatches by mode.
func (idx *Index) Search(params SearchParams) ([]Match, int, error) {
	matches, stats, err := idx.SearchWithStats(params)
	return matches, stats.Total, err
}

// SearchWithStats is Search with the shortfall between the count and the
// returned matches attached. Search is kept as the two-value form for callers
// outside this module; every caller that renders the count for a human or an
// LLM should use this one, because the count alone cannot be presented honestly
// when part of the dump has moved out from under the index.
func (idx *Index) SearchWithStats(params SearchParams) ([]Match, SearchStats, error) {
	if !idx.ready.Load() {
		if errPtr := idx.buildErr.Load(); errPtr != nil {
			return nil, SearchStats{}, fmt.Errorf("index build failed: %w", *errPtr)
		}
		return nil, SearchStats{}, fmt.Errorf("search index is building, please retry")
	}

	if params.Mode == "" {
		params.Mode = SearchModeSmart
	}
	if params.Limit <= 0 {
		params.Limit = 50
	}
	if params.Limit > 500 {
		params.Limit = 500
	}

	switch params.Mode {
	case SearchModeSmart:
		return idx.searchSmart(params)
	case SearchModeRegex:
		re, err := regexp.Compile(params.Query)
		if err != nil {
			return nil, SearchStats{}, fmt.Errorf("invalid regex %q: %w", params.Query, err)
		}
		return idx.searchLineByLine(params, func(line, _ string) bool {
			return re.MatchString(line)
		}, params.Query, false)
	case SearchModeExact:
		lower := strings.ToLower(params.Query)
		return idx.searchLineByLine(params, func(line, _ string) bool {
			return strings.Contains(line, lower)
		}, lower, true)
	default:
		return nil, SearchStats{}, fmt.Errorf("unknown search mode: %q", params.Mode)
	}
}

// searchSmart performs full-text BM25 search via Bleve.
//
// The hits Bleve returns are re-read through GetContent, which refuses to serve
// a module whose file has changed or vanished since it was indexed. Every hit
// refused that way is counted into SearchStats.Unreadable: the refusal is right,
// but it makes the answer smaller than result.Total, and a count that outruns the
// answer silently is what turns a moved dump into "N совпадений" over an empty
// body.
func (idx *Index) searchSmart(params SearchParams) ([]Match, SearchStats, error) {
	mq := bleve.NewMatchQuery(params.Query)
	mq.SetField("content")
	mq.Analyzer = analyzerBSL

	var q query.Query = mq

	// Apply category/module filters as conjunction.
	if params.Category != "" || params.Module != "" {
		queries := []query.Query{mq}
		if params.Category != "" {
			tq := bleve.NewTermQuery(params.Category)
			tq.SetField("category")
			queries = append(queries, tq)
		}
		if params.Module != "" {
			tq := bleve.NewTermQuery(params.Module)
			tq.SetField("module")
			queries = append(queries, tq)
		}
		q = bleve.NewConjunctionQuery(queries...)
	}

	req := bleve.NewSearchRequestOptions(q, params.Limit, 0, false)
	result, err := idx.alias.Search(req)
	if err != nil {
		return nil, SearchStats{}, fmt.Errorf("bleve search: %w", err)
	}

	lower := strings.ToLower(params.Query)
	tokens := strings.Fields(lower)

	// Pre-build synonym-expanded token set for fallback when Bleve matched
	// via synonym expansion but original tokens do not appear in the source.
	synonymMapOnce.Do(func() { cachedSynonymMap = buildSynonymMap() })
	synMap := cachedSynonymMap
	expandedTokens := make([]string, 0, len(tokens)*2)
	for _, tok := range tokens {
		expandedTokens = append(expandedTokens, tok)
		if syn, ok := synMap[tok]; ok {
			expandedTokens = append(expandedTokens, syn)
		}
	}

	var matches []Match
	unreadable := 0
	for _, hit := range result.Hits {
		content, ok := idx.GetContent(hit.ID)
		if !ok {
			// The index still counts this hit but its module can no longer be
			// read, so nothing about it can be shown. Dropping it is correct;
			// dropping it WITHOUT a trace is what lets the count outrun the body.
			unreadable++
			continue
		}
		lines := strings.Split(content, "\n")

		// Score each line by counting how many distinct query tokens it contains.
		// Pick the line with the highest score; on ties, prefer the first occurrence.
		lineNum := 0
		bestScore := 0
		for i, line := range lines {
			ll := strings.ToLower(line)
			score := 0
			for _, tok := range tokens {
				if strings.Contains(ll, tok) {
					score++
				}
			}
			if score > bestScore {
				bestScore = score
				lineNum = i + 1
			}
		}

		// Synonym fallback: if no original token matched any line, try expanded tokens.
		if lineNum == 0 && len(expandedTokens) > len(tokens) {
			for i, line := range lines {
				ll := strings.ToLower(line)
				for _, tok := range expandedTokens {
					if strings.Contains(ll, tok) {
						lineNum = i + 1
						break
					}
				}
				if lineNum > 0 {
					break
				}
			}
		}

		if lineNum == 0 {
			// No line of the current file carries a query token or a synonym of
			// one, so there is nothing to point at. Report the module without a
			// line and without quoted source: claiming line 1 and pasting the head
			// of the file would present code that does not contain the match as if
			// it did.
			//
			// This is a routine outcome on an untouched dump, not only after an
			// edit: Bleve analyses the query with the BSL analyzer while the loop
			// above scans for the raw strings.Fields tokens, so a query carrying
			// punctuation ("ПередЗаписью()") matches the document yet matches no
			// line verbatim. Dropping the hit would therefore lose genuine results,
			// which is why it is kept and reported honestly instead.
			matches = append(matches, Match{
				Module: hit.ID,
				Line:   0,
				Score:  hit.Score,
			})
			continue
		}

		ctx := extractContext(lines, lineNum-1, 2)
		matches = append(matches, Match{
			Module:  hit.ID,
			Line:    lineNum,
			Context: ctx,
			Score:   hit.Score,
		})
	}

	return matches, SearchStats{Total: int(result.Total), Unreadable: unreadable}, nil
}

// searchLineByLine performs line-by-line search using a matcher function.
// Used for regex and exact modes. Optionally pre-filters modules via Bleve.
// When preLower is true, each line is pre-lowered once and the lowered version
// is passed to the match function (avoids redundant ToLower per line).
//
// SearchStats.Unreadable stays 0 here, and that is a statement about this scan
// rather than an omission: a module whose content cannot be read contributes
// neither matches nor count, because both are produced from the same content in
// the same pass. There is no shortfall between the two to report. Counting the
// skipped candidates would invent one and send the caller re-dumping over an
// answer that is already complete.
func (idx *Index) searchLineByLine(params SearchParams, match func(line, q string) bool, q string, preLower bool) ([]Match, SearchStats, error) {
	candidates, err := idx.filterModules(params.Category, params.Module)
	if err != nil {
		return nil, SearchStats{}, err
	}

	var matches []Match
	total := 0

	// Scan candidates in bounded parallel chunks. Each candidate's content is
	// streamed from disk (or taken from cache if present) and discarded after
	// scanning — nothing is permanently cached, so a full regex/exact scan stays
	// memory-bounded (the cold-build fix removed the all-content map). Chunk
	// results are merged in candidate order, so the output — match order, total
	// count, and first-Limit cap — is byte-identical to a sequential scan.
	type candResult struct {
		matches []Match
		count   int
	}

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	chunkSize := workers * 8

	for start := 0; start < len(candidates); start += chunkSize {
		end := min(start+chunkSize, len(candidates))
		chunk := candidates[start:end]
		results := make([]candResult, len(chunk))

		var wg sync.WaitGroup
		sem := make(chan struct{}, workers)
		for i, name := range chunk {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, name string) {
				defer wg.Done()
				defer func() { <-sem }()

				content, ok := idx.contentForScan(name)
				if !ok {
					return
				}
				lines := strings.Split(content, "\n")
				var ms []Match
				cnt := 0
				for li, line := range lines {
					matchLine := line
					if preLower {
						matchLine = strings.ToLower(line)
					}
					if match(matchLine, q) {
						cnt++
						// Buffer at most Limit matches per candidate; the global
						// cap is applied during the ordered merge below.
						if len(ms) < params.Limit {
							ms = append(ms, Match{
								Module:  name,
								Line:    li + 1,
								Context: extractContext(lines, li, 2),
							})
						}
					}
				}
				results[i] = candResult{matches: ms, count: cnt}
			}(i, name)
		}
		wg.Wait()

		for _, r := range results {
			total += r.count
			for _, m := range r.matches {
				if len(matches) < params.Limit {
					matches = append(matches, m)
				}
			}
		}
	}

	return matches, SearchStats{Total: total}, nil
}

// filterModules returns the subset of module names matching category/module filters.
// If no filters are set, returns a copy of all names. Uses PathIndex for fast in-memory filtering.
// The returned slice is always a fresh copy safe for concurrent use.
func (idx *Index) filterModules(category, moduleType string) ([]string, error) {
	if category == "" && moduleType == "" {
		idx.mu.RLock()
		result := slices.Clone(idx.names)
		idx.mu.RUnlock()
		return result, nil
	}

	// Use PathIndex for fast in-memory filtering (no Bleve query needed). The nil
	// check must happen UNDER the same lock as the call: Reload replaces the whole
	// *PathIndex while readers run, so testing the field outside the lock and
	// dereferencing it inside would be a torn read of a pointer another goroutine
	// is writing.
	idx.mu.RLock()
	if pi := idx.pathIndex; pi != nil {
		result := pi.FilterDocIDs(category, moduleType)
		idx.mu.RUnlock()
		return result, nil
	}
	idx.mu.RUnlock()

	// Fallback: linear scan if pathIndex is not yet built (should not happen
	// since filterModules is only called after Ready() == true).
	idx.mu.RLock()
	allNames := slices.Clone(idx.names)
	idx.mu.RUnlock()

	var names []string
	for _, name := range allNames {
		parts := parseModuleName(name)
		if category != "" && parts.category != category {
			continue
		}
		if moduleType != "" && parts.module != moduleType {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// loadFromManifestAndDiff populates the index from a cached manifest and applies
// incremental updates using a single filesystem walk (via Diff). This is the fastest
// startup path: manifest provides names/paths, Diff detects changes.
// Returns an error if no manifest exists or if Diff fails.
func (idx *Index) loadFromManifestAndDiff(cacheDir string) error {
	manifest, err := LoadManifest(cacheDir)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}
	if manifest == nil {
		// No manifest — need full walk to create one.
		if err := idx.loadBSLPaths(idx.dir); err != nil {
			return err
		}
		idx.saveManifest(cacheDir)
		return nil
	}

	// Populate names, pathByName, pathToDocID from manifest (no filesystem I/O).
	// Go map iteration order is randomised, so idx.names must be sorted after
	// the loop to preserve the same lexicographic enumeration invariant as
	// loadBSLFiles. Maps (pathByName, pathToDocID) are unaffected.
	idx.mu.Lock()
	idx.pathToDocID = make(map[string]string, len(manifest.Files))
	for relPath, entry := range manifest.Files {
		absPath := filepath.Join(idx.dir, filepath.FromSlash(relPath))
		// NFC the manifest docID at this chokepoint, mirroring bslPathToModuleName on
		// the cold-build path. A manifest written by a pre-NFC-fix binary on macOS
		// stored decomposable (short-I / IO) names in NFD; loading them verbatim would
		// key idx.names / pathByName / pathToDocID — and the PathIndex built from
		// idx.names — in NFD, so an NFC GetContent/resolve query would never match.
		// NFC is an allocation-free no-op on already-NFC keys (prod/Windows/HTTP and
		// any cache this binary rebuilt). The relPath KEY is the raw on-disk path and
		// stays unchanged so file I/O and the Diff below still match.
		docID := NFC(entry.DocID)
		idx.names = append(idx.names, docID)
		idx.pathByName[docID] = absPath
		idx.pathToDocID[relPath] = docID
	}
	slices.Sort(idx.names)
	idx.mu.Unlock()

	// Diff walks the filesystem once to detect changes.
	diff, err := manifest.Diff(idx.dir)
	if err != nil {
		return fmt.Errorf("computing diff: %w", err)
	}

	if diff.Empty() {
		return nil
	}

	// Apply deletions.
	for _, relPath := range diff.Deleted {
		entry, ok := manifest.Files[relPath]
		if !ok {
			continue
		}
		// NFC the manifest docID for the same reason as the load loop above: the
		// in-memory maps and the PathIndex are NFC-keyed, and a current-schema cache's
		// shard docIDs are NFC too, so the shard delete and the map deletes all target
		// the right key. No-op on already-NFC keys.
		docID := NFC(entry.DocID)
		si := shardForID(docID, len(idx.shards))
		if err := idx.shards[si].Delete(docID); err != nil {
			slog.Warn("Failed to delete from shard", "docID", docID, "error", err)
		}
		idx.contentMu.Lock()
		delete(idx.contentByName, docID)
		idx.contentMu.Unlock()

		idx.mu.Lock()
		delete(idx.pathByName, docID)
		delete(idx.pathToDocID, relPath)
		for i, n := range idx.names {
			if n == docID {
				idx.names = append(idx.names[:i], idx.names[i+1:]...)
				break
			}
		}
		idx.mu.Unlock()
	}

	// Apply additions and modifications.
	for _, relPath := range append(diff.Added, diff.Modified...) {
		absPath := filepath.Join(idx.dir, filepath.FromSlash(relPath))
		// Skip a changed .bsl whose real path escapes the dump root (a symlink
		// planted by a malicious dump). Mirrors loadBSLPaths / GetContent and stops
		// the incremental path from re-indexing an outside host file into the
		// searchable shard + content cache on a warm re-open.
		if !pathWithinRoot(idx.dir, absPath) {
			slog.Warn("Skipping .bsl whose real path escapes the dump root", "path", relPath)
			continue
		}
		// Stamp before reading, never after, so the pre-warmed entry can never
		// claim a revision newer than the bytes it holds (see GetContent).
		stamp, stamped := statStamp(absPath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			slog.Warn("Cannot read file", "path", relPath, "error", err)
			continue
		}
		docID := bslPathToModuleName(relPath)
		content := stripBOM(string(data))

		parts := parseModuleName(docID)
		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  content,
		}

		si := shardForID(docID, len(idx.shards))
		if err := idx.shards[si].Index(docID, doc); err != nil {
			slog.Warn("Failed to index in shard", "docID", docID, "error", err)
			continue
		}

		idx.contentMu.RLock()
		_, inContent := idx.contentByName[docID]
		idx.contentMu.RUnlock()

		idx.mu.Lock()
		_, inPath := idx.pathByName[docID]
		if !inContent && !inPath {
			idx.names = append(idx.names, docID)
		}
		idx.pathByName[docID] = absPath
		idx.pathToDocID[relPath] = docID
		idx.mu.Unlock()

		// Pre-warm content cache for recently changed files.
		idx.contentMu.Lock()
		if stamped {
			idx.contentByName[docID] = cachedModule{content: content, stamp: stamp, fromFile: true}
		} else {
			// Revision unknown, so this copy could never be revalidated: drop any
			// cached entry and let the next read go to disk.
			delete(idx.contentByName, docID)
		}
		idx.contentMu.Unlock()
	}

	if len(diff.Added) > 0 || len(diff.Modified) > 0 || len(diff.Deleted) > 0 {
		slog.Info("Incremental update", "added", len(diff.Added), "modified", len(diff.Modified), "deleted", len(diff.Deleted))
	}

	// Save updated manifest.
	idx.saveManifest(cacheDir)

	return nil
}

// ModuleCount returns the number of indexed BSL modules.
func (idx *Index) ModuleCount() int {
	idx.mu.RLock()
	n := len(idx.names)
	idx.mu.RUnlock()
	return n
}

// ModuleNames returns a defensive copy of the indexed BSL module names.
// Each entry is the human-readable, russian-translated ID as produced by
// bslPathToModuleName (e.g. "Документ.РеализацияТоваров.МодульОбъекта"),
// which is the same key used by GetContent.
//
// Returns an empty slice (never nil) when no modules are indexed. The copy
// is taken under idx.mu.RLock to be safe against concurrent index updates
// (IndexDoc/DeleteDoc) — callers may modify or sort the returned slice
// without affecting the index.
func (idx *Index) ModuleNames() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.names == nil {
		return []string{}
	}
	return slices.Clone(idx.names)
}

// BuildError returns the most recent error captured during background index
// build (loadBSLFiles failure, shard error, or manifest-fallback failure), or
// nil if no error was recorded. Safe to call after <-idx.Done() has unblocked
// to distinguish "build completed successfully" (Ready() == true, BuildError()
// == nil) from "build aborted" (Ready() == false, BuildError() != nil).
//
// Read is unconditional: consumers may observe a non-nil BuildError() even
// while Ready() is still false during the build, but the field is set exactly
// once on error paths so the return value is stable across repeated reads.
func (idx *Index) BuildError() error {
	if errPtr := idx.buildErr.Load(); errPtr != nil {
		return *errPtr
	}
	return nil
}

// Dir returns the dump directory path.
func (idx *Index) Dir() string {
	return idx.dir
}

// UnprotectedReason returns why the index generation currently attached is being
// served WITHOUT a reader claim, or "" when it is protected.
//
// THIS IS THE GUARANTEE'S OTHER HALF, and it is exported for exactly one purpose:
// the server must never SILENTLY serve a generation it could not protect, and the
// log is not where a user is looking. A non-empty return means the generation is
// being served and answering correctly, and that a co-located process could remove
// it while it is in use. The MCP tool layer turns it into a notice on the response
// (see tools.withIndexProtectionNotice).
//
// "" covers both a generation held by a live claim and one on a read-only
// filesystem, where the kernel refuses every write and there is no reaper to
// protect it from. Those are not distinguished here because the user has nothing to
// do about either.
//
// It is read per call, never cached by the caller: the open finishes in the
// background after the index is handed out, and Reload swaps the attached
// generation, so the answer changes during the process's life.
func (idx *Index) UnprotectedReason() string {
	if idx == nil {
		return ""
	}
	if p := idx.unprotected.Load(); p != nil {
		return *p
	}
	return ""
}

// setUnprotected records the reason the attached generation is unprotected, or ""
// when it is protected. Every site that installs or replaces idx.readerReg must
// call it, so the notice can never describe a generation that is no longer the one
// being served.
func (idx *Index) setUnprotected(reason string) {
	idx.unprotected.Store(&reason)
}

// GetPathIndex returns the path index for fast category/module filtering.
// Returns nil if the index is not yet ready.
//
// The pointer is read under mu because Reload swaps in a new *PathIndex while
// readers run. The caller receives the instance current AT THE MOMENT OF THE
// CALL: a reload that lands afterwards leaves the caller holding the previous
// (still valid, still self-consistent) path index, not a torn one.
func (idx *Index) GetPathIndex() *PathIndex {
	if !idx.ready.Load() {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.pathIndex
}

// applyIncrementalUpdate loads the manifest, diffs against the filesystem,
// and applies IndexDoc/DeleteDoc for changed files. If no manifest exists
// (first run after upgrade), it only saves a new one for future runs.
func (idx *Index) applyIncrementalUpdate(cacheDir string) error {
	manifest, err := LoadManifest(cacheDir)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}

	if manifest == nil {
		// No manifest yet (first run with incremental support).
		// Save one now so next start can diff.
		idx.saveManifest(cacheDir)
		return nil
	}

	diff, err := manifest.Diff(idx.dir)
	if err != nil {
		return fmt.Errorf("computing diff: %w", err)
	}

	if diff.Empty() {
		return nil
	}

	// Apply deletions.
	for _, relPath := range diff.Deleted {
		entry, ok := manifest.Files[relPath]
		if !ok {
			continue
		}
		docID := entry.DocID
		si := shardForID(docID, len(idx.shards))
		if err := idx.shards[si].Delete(docID); err != nil {
			slog.Warn("Failed to delete from shard", "docID", docID, "error", err)
		}
		idx.contentMu.Lock()
		delete(idx.contentByName, docID)
		idx.contentMu.Unlock()

		idx.mu.Lock()
		delete(idx.pathByName, docID)
		delete(idx.pathToDocID, relPath)
		for i, n := range idx.names {
			if n == docID {
				idx.names = append(idx.names[:i], idx.names[i+1:]...)
				break
			}
		}
		idx.mu.Unlock()
	}

	// Apply additions and modifications.
	for _, relPath := range append(diff.Added, diff.Modified...) {
		absPath := filepath.Join(idx.dir, filepath.FromSlash(relPath))
		// Skip a changed .bsl whose real path escapes the dump root (a symlink
		// planted by a malicious dump). Mirrors loadBSLPaths / GetContent and stops
		// the incremental path from re-indexing an outside host file into the
		// searchable shard + content cache on a warm re-open.
		if !pathWithinRoot(idx.dir, absPath) {
			slog.Warn("Skipping .bsl whose real path escapes the dump root", "path", relPath)
			continue
		}
		// Stamp before reading, never after, so the pre-warmed entry can never
		// claim a revision newer than the bytes it holds (see GetContent).
		stamp, stamped := statStamp(absPath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			slog.Warn("Cannot read file", "path", relPath, "error", err)
			continue
		}
		docID := bslPathToModuleName(relPath)
		content := stripBOM(string(data))

		parts := parseModuleName(docID)
		doc := bslDocument{
			Name:     parts.name,
			Category: parts.category,
			Module:   parts.module,
			Content:  content,
		}

		si := shardForID(docID, len(idx.shards))
		if err := idx.shards[si].Index(docID, doc); err != nil {
			slog.Warn("Failed to index in shard", "docID", docID, "error", err)
			continue
		}

		idx.contentMu.RLock()
		_, inContent := idx.contentByName[docID]
		idx.contentMu.RUnlock()

		idx.mu.Lock()
		_, inPath := idx.pathByName[docID]
		if !inContent && !inPath {
			idx.names = append(idx.names, docID)
		}
		idx.pathByName[docID] = absPath
		idx.pathToDocID[relPath] = docID
		idx.mu.Unlock()

		// Pre-warm content cache for recently changed files.
		idx.contentMu.Lock()
		if stamped {
			idx.contentByName[docID] = cachedModule{content: content, stamp: stamp, fromFile: true}
		} else {
			// Revision unknown, so this copy could never be revalidated: drop any
			// cached entry and let the next read go to disk.
			delete(idx.contentByName, docID)
		}
		idx.contentMu.Unlock()
	}

	slog.Info("Incremental update", "added", len(diff.Added), "modified", len(diff.Modified), "deleted", len(diff.Deleted))

	// Save updated manifest.
	idx.saveManifest(cacheDir)

	return nil
}

// saveManifest builds and persists a manifest from current pathToDocID state.
func (idx *Index) saveManifest(cacheDir string) {
	idx.mu.RLock()
	pathCopy := make(map[string]string, len(idx.pathToDocID))
	for k, v := range idx.pathToDocID {
		pathCopy[k] = v
	}
	idx.mu.RUnlock()

	manifest, err := buildManifest(idx.dir, pathCopy)
	if err != nil {
		slog.Warn("Cannot build manifest", "error", err)
		return
	}
	if err := manifest.Save(cacheDir); err != nil {
		slog.Warn("Cannot save manifest", "error", err)
	}
}

// Close cancels the background context, waits for any in-progress build to
// finish, and closes all shard indexes.
//
// It takes reloadMu, so a Reload in flight completes its swap first and Close
// then frees the shards the reload published rather than the ones it retired.
// A reload cannot be interrupted mid-build (BuildGeneration is synchronous, the
// same limitation PrepareServeGeneration documents), so Close can wait for one.
func (idx *Index) Close() error {
	idx.cancel()
	<-idx.done
	idx.reloadMu.Lock()
	defer idx.reloadMu.Unlock()
	idx.closed.Store(true)
	if idx.readerReg != nil {
		// Deregister from the generation's readers/ registry so GC can reclaim the
		// generation once no live reader holds it.
		idx.readerReg.Close()
	}
	if idx.lockDir != "" {
		removeCacheLock(idx.lockDir)
	}
	var firstErr error
	for _, shard := range idx.shards {
		if err := shard.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if idx.overlay != nil {
		if err := idx.overlay.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		idx.overlay = nil
	}
	return firstErr
}
