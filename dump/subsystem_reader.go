package dump

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file reads a configuration dump's Subsystems/ tree into a canonical forest
// (Name / Path / Synonym / Content / Children), supporting BOTH on-disk layouts a
// 1C dump uses, and it does so under hard defensive bounds so a malformed or
// malicious dump can never traverse out of the dump, exhaust memory, or overflow
// the stack. Every dropped subsystem is NAMED in a warning (never silently lost).

// ---------------------------------------------------------------------------
// Malicious-dump bounds (traversal / DoS). Declared as vars, not consts, so tests
// can tighten them without building multi-GB fixtures. They are defensive ceilings
// only; a real configuration dump is orders of magnitude below every one.
// ---------------------------------------------------------------------------
var (
	// maxSubsystemFileBytes caps how many bytes a single subsystem XML file may
	// contribute before it is rejected (guards a multi-GB or infinite-stream file,
	// e.g. a symlink to /dev/zero that stays in-dump).
	maxSubsystemFileBytes int64 = 16 << 20

	// maxSubsystemXMLDepth caps element nesting inside one file so the recursive
	// xml.Unmarshal cannot overflow the goroutine stack on a deeply nested body.
	maxSubsystemXMLDepth = 256

	// maxSubsystemTreeDepth caps subsystem-tree recursion across directories,
	// which bounds both a symlink cycle and pathologically deep on-disk nesting.
	maxSubsystemTreeDepth = 64

	// maxSubsystemNodes caps how many subsystem files the whole walk parses.
	maxSubsystemNodes = 100_000
)

// errReadSubsystemsRoot is the path-free client-facing error for an unreadable
// top-level Subsystems/ directory. The underlying *PathError carries the absolute
// server-side dump path, which must never reach the client, so it is intentionally
// dropped rather than wrapped. Customer-facing RU: no тире.
var errReadSubsystemsRoot = errors.New("не удалось прочитать каталог подсистем дампа")

// errNotDirectory marks a dump path that occupies a directory position but is not a
// directory: a FIFO, socket, device, plain file, or a symlink standing in for the
// directory. Opening such a position unconditionally is the DoS this guard closes: a
// writer-less FIFO at a directory position blocks the open forever and, unlike a
// bounded read, a blocked open() cannot be interrupted by ctx. Every directory read
// refuses a non-directory BEFORE that blocking open, mirroring the subsystem-file
// guard, and NAMES the refusal so the drop is never silent.
var errNotDirectory = errors.New("dump path is not a directory")

// errDumpDirNotDirectory is the path-free RU refusal returned by ParseAllSubsystemsCtx
// when dumpDir ITSELF is a non-directory node (a FIFO, socket, device, plain file, or a
// symlink resolving to one). It is errNotDirectory's guard one level up, at the dump
// root: os.OpenRoot(dumpDir) on a writer-less FIFO blocks on an open() that ctx cannot
// interrupt, so the node type is checked with os.Stat (which does not open it) BEFORE
// the open. Customer-facing RU: no тире, never an absolute path.
var errDumpDirNotDirectory = errors.New("каталог дампа имеет неверный тип")

// dumpDirIsNonDir reports whether dumpDir exists but is NOT a directory: a FIFO,
// socket, device, plain file, or a symlink resolving to one of those. os.Stat follows a
// symlink and, crucially, does NOT open the node, so it returns immediately on a
// writer-less FIFO where os.OpenRoot would block on the ctx-uninterruptible open (the
// DoS this guards). A missing path or a permission error (err != nil) and a genuine
// directory (including a symlink to a directory) all report false, so every
// os.OpenRoot(dumpDir) entry point falls through to its existing missing / unreadable /
// symlink-to-directory contract unchanged; only a non-directory dumpDir is refused
// before the blocking open.
func dumpDirIsNonDir(dumpDir string) bool {
	fi, err := os.Stat(dumpDir)
	return err == nil && !fi.IsDir()
}

// Subsystem is one node of the dump's subsystem forest: its canonical full path,
// display synonym, direct member composition (Content, canonical RU full names)
// and any nested child subsystems.
type Subsystem struct {
	Name     string
	Path     string // canonical "Подсистема.<Root>[.<Child>...]"
	Synonym  string
	Content  []string
	Children []Subsystem
}

// ---------------------------------------------------------------------------
// On-disk XML shapes (only the fields the reader uses are deserialized).
// ---------------------------------------------------------------------------

type xmlSubsystem struct {
	XMLName    xml.Name             `xml:"Subsystem"`
	Properties xmlSubsystemProps    `xml:"Properties"`
	Children   xmlSubsystemChildren `xml:"ChildObjects"`
}

type xmlSubsystemProps struct {
	Name    string             `xml:"Name"`
	Synonym xmlLocalizedString `xml:"Synonym"`
	Content xmlContentList     `xml:"Content"`
}

// xmlLocalizedItem is one localized entry inside a 1C LocalStringType envelope
// (e.g. <Synonym>): a language tag plus its content. Matched by local name so the
// v8: namespace prefix is irrelevant.
type xmlLocalizedItem struct {
	Lang    string `xml:"lang"`
	Content string `xml:"content"`
}

// xmlLocalizedString is the canonical 1C <Synonym> envelope: a list of localized
// <item>s. pick() returns the Russian content, else the first non-empty content.
type xmlLocalizedString struct {
	Items []xmlLocalizedItem `xml:"item"`
}

func (s xmlLocalizedString) pick() string {
	for _, it := range s.Items {
		if it.Lang == "ru" && it.Content != "" {
			return it.Content
		}
	}
	for _, it := range s.Items {
		if it.Content != "" {
			return it.Content
		}
	}
	return ""
}

// xmlContentList holds a subsystem <Content> block's <Item> lines, each a full
// metadata reference to a member object. Matched by local name so the xr: prefix
// on <xr:Item> is irrelevant.
type xmlContentList struct {
	Items []string `xml:"Item"`
}

type xmlSubsystemChildren struct {
	Subsystems []xmlSubsystem `xml:"Subsystem"`
}

type xmlMetaDataObject struct {
	XMLName   xml.Name     `xml:"MetaDataObject"`
	Subsystem xmlSubsystem `xml:"Subsystem"`
}

// decodeMetaObject reads an XML stream under hard size and nesting bounds so a
// malicious or corrupt dump file cannot exhaust memory or overflow the decode
// stack. It reads at most maxSubsystemFileBytes, rejects nesting beyond
// maxSubsystemXMLDepth (verified with a constant-stack token scan before the
// recursive unmarshal), then unmarshals the already-bounded bytes.
func decodeMetaObject(r io.Reader) (xmlMetaDataObject, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxSubsystemFileBytes+1))
	if err != nil {
		return xmlMetaDataObject{}, err
	}
	if int64(len(data)) > maxSubsystemFileBytes {
		return xmlMetaDataObject{}, fmt.Errorf("subsystem xml exceeds %d bytes", maxSubsystemFileBytes)
	}
	if err := ensureXMLDepth(data, maxSubsystemXMLDepth); err != nil {
		return xmlMetaDataObject{}, err
	}
	var doc xmlMetaDataObject
	if err := xml.Unmarshal(data, &doc); err != nil {
		return xmlMetaDataObject{}, err
	}
	return doc, nil
}

// ensureXMLDepth scans tokens with the streaming decoder (constant stack, no
// recursion) and fails if element nesting exceeds max, so the subsequent
// xml.Unmarshal (which recurses one frame per open element) cannot overflow the
// goroutine stack on a deeply nested document. A malformed stream returns nil so
// the caller's Unmarshal surfaces the real parse error.
func ensureXMLDepth(data []byte, max int) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil // EOF or malformed: let Unmarshal produce the real error
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
			if depth > max {
				return fmt.Errorf("subsystem xml nesting exceeds %d", max)
			}
		case xml.EndElement:
			depth--
		}
	}
}

// parseSubsystemXML decodes a single Ext-layout Subsystem.xml stream into the
// canonical Subsystem struct, with deterministic ordering applied to children and
// content. Ext layout carries nested children inside <ChildObjects>.
func parseSubsystemXML(r io.Reader) (Subsystem, error) {
	doc, err := decodeMetaObject(r)
	if err != nil {
		return Subsystem{}, fmt.Errorf("parse subsystem xml: %w", err)
	}
	if doc.Subsystem.Properties.Name == "" {
		return Subsystem{}, fmt.Errorf("parse subsystem xml: missing Subsystem name")
	}
	return convertSubsystem(doc.Subsystem, ""), nil
}

// convertSubsystem walks the parsed XML structure and assembles the Subsystem with
// a fully qualified canonical path. parentPath is empty at the root (yields
// "Подсистема.<Name>") and accumulates as we recurse.
func convertSubsystem(x xmlSubsystem, parentPath string) Subsystem {
	path := parentPath
	if path == "" {
		path = "Подсистема." + x.Properties.Name
	} else {
		path = parentPath + "." + x.Properties.Name
	}
	s := Subsystem{
		Name:    x.Properties.Name,
		Path:    path,
		Synonym: x.Properties.Synonym.pick(),
	}
	for _, raw := range x.Properties.Content.Items {
		if p := canonicalizeContentPath(raw); p != "" {
			s.Content = append(s.Content, p)
		}
	}
	sort.Strings(s.Content)

	for _, c := range x.Children.Subsystems {
		s.Children = append(s.Children, convertSubsystem(c, path))
	}
	sort.Slice(s.Children, func(i, j int) bool {
		return s.Children[i].Name < s.Children[j].Name
	})
	return s
}

// parseSubsystemHierarchical decodes a single Hierarchical Subsystem XML file,
// extracting Name, Synonym and Content. <ChildObjects> is intentionally ignored:
// in Hierarchical layout it is a flat text-name list, not a nested struct, and
// children are discovered via disk traversal in walkHierarchical.
func parseSubsystemHierarchical(r io.Reader, parentPath string) (Subsystem, error) {
	doc, err := decodeMetaObject(r)
	if err != nil {
		return Subsystem{}, fmt.Errorf("parse subsystem hierarchical: %w", err)
	}
	name := doc.Subsystem.Properties.Name
	if name == "" {
		return Subsystem{}, fmt.Errorf("parse subsystem hierarchical: missing Name")
	}
	path := parentPath
	if path == "" {
		path = "Подсистема." + name
	} else {
		path = parentPath + "." + name
	}
	s := Subsystem{Name: name, Path: path, Synonym: doc.Subsystem.Properties.Synonym.pick()}
	for _, raw := range doc.Subsystem.Properties.Content.Items {
		if p := canonicalizeContentPath(raw); p != "" {
			s.Content = append(s.Content, p)
		}
	}
	sort.Strings(s.Content)
	return s, nil
}

// ---------------------------------------------------------------------------
// Layout detection: Ext vs Hierarchical, probed from the Subsystems/ dir itself.
// ---------------------------------------------------------------------------

type subsystemLayout int

const (
	// layoutExt is the ConfigurationDump / ConfigurationRepositoryDumpCfg layout:
	// Subsystems/<N>/Ext/Subsystem.xml.
	layoutExt subsystemLayout = iota
	// layoutRoot is the Hierarchical layout: Subsystems/<Name>.xml (+ recursion).
	layoutRoot
)

// detectSubsystemLayout inspects the top-level Subsystems/ directory and reports
// which on-disk layout it uses. It opens the dump as an os.Root and probes through
// it, so every path component is confined beneath the dump root and a crafted
// symlink at any depth is refused rather than followed out of the dump. The
// presence of any direct <Name>.xml file is unambiguous evidence of the
// Hierarchical layout (Конфигуратор 8.3.10+), so it wins; otherwise a present
// <N>/Ext/Subsystem.xml selects the Ext layout. An absent (or unopenable) dump, an
// absent Subsystems/, or an escaping Subsystems/ symlink all yield (layoutExt, nil)
// so the walk returns an empty tree; a present but unreadable Subsystems/ yields
// the path-free errReadSubsystemsRoot.
func detectSubsystemLayout(dumpDir string) (subsystemLayout, error) {
	if dumpDirIsNonDir(dumpDir) {
		return layoutExt, nil // non-directory dumpDir: default layout, empty tree (never open it)
	}
	root, err := os.OpenRoot(dumpDir)
	if err != nil {
		return layoutExt, nil
	}
	defer func() { _ = root.Close() }()
	return detectLayoutInRoot(root)
}

// detectLayoutInRoot is detectSubsystemLayout's core, operating on an already-open
// os.Root so ParseAllSubsystemsCtx can share a single root with the walker.
func detectLayoutInRoot(root *os.Root) (subsystemLayout, error) {
	entries, err := readDirInRoot(root, "Subsystems")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return layoutExt, nil // no Subsystems/: empty tree
		}
		if errors.Is(err, os.ErrPermission) {
			return layoutExt, errReadSubsystemsRoot // present but unreadable: path-free
		}
		// Containment refusal (an escaping Subsystems/ symlink, os.Root "path escapes"),
		// a non-directory Subsystems position (errNotDirectory: a FIFO/socket/device/
		// plain file, refused BEFORE the blocking open by openDirInRoot so detection
		// never hangs), or any other read error: never probe outside; default layout so
		// the walk returns an empty tree (the walk re-encounters and NAMES it).
		return layoutExt, nil
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".xml") {
			return layoutRoot, nil
		}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// os.Root.Lstat confines every component (an escaping intermediate Ext
		// symlink is refused) and does not follow the final component, so a symlinked
		// Subsystem.xml is not treated as a valid Ext marker (the walk refuses it too).
		rel := filepath.Join("Subsystems", e.Name(), "Ext", "Subsystem.xml")
		if st, serr := root.Lstat(rel); serr == nil && st.Mode().IsRegular() {
			return layoutExt, nil
		}
	}
	return layoutExt, nil
}

// ---------------------------------------------------------------------------
// Entry points.
// ---------------------------------------------------------------------------

// ParseAllSubsystems is the ctx-free, warning-free wrapper for callers that parse
// a trusted dump and neither cancel nor surface per-subsystem drop diagnostics. It
// applies the same containment and DoS guards as ParseAllSubsystemsCtx.
func ParseAllSubsystems(dumpDir string) ([]Subsystem, error) {
	subs, _, err := ParseAllSubsystemsCtx(context.Background(), dumpDir)
	return subs, err
}

// ParseAllSubsystemsCtx detects the dump layout and dispatches to the appropriate
// walker, honouring ctx cancellation and returning per-subsystem drop diagnostics
// (warnings) alongside the parsed tree. Hierarchical uses a disk-walk recursion;
// Ext preserves the nested-children-from-XML behaviour. Every filesystem access is
// confined to the dump via an os.Root (a crafted symlink that escapes the dump at
// ANY path component is refused by the OS primitive, never followed), every directory
// AND file position is type-checked before it is opened (a writer-less FIFO planted at
// any position is refused before the blocking open rather than hanging the walk),
// recursion is depth-capped, and each dropped subsystem or directory is NAMED in
// warnings (never silently dropped).
func ParseAllSubsystemsCtx(ctx context.Context, dumpDir string) ([]Subsystem, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if dumpDirIsNonDir(dumpDir) {
		return nil, nil, errDumpDirNotDirectory // dumpDir itself is a FIFO/socket/device/file: refuse before the blocking open
	}
	root, err := os.OpenRoot(dumpDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil // absent dump: empty tree
		}
		return nil, nil, errReadSubsystemsRoot // not a directory / unreadable root: path-free
	}
	defer func() { _ = root.Close() }()

	layout, err := detectLayoutInRoot(root)
	if err != nil {
		return nil, nil, err
	}
	w := &subsystemWalker{ctx: ctx, root: root}
	var subs []Subsystem
	if layout == layoutRoot {
		subs, err = w.walkHierarchical("Subsystems", "", 0)
	} else {
		subs, err = w.walkExt("Subsystems")
	}
	if err != nil {
		return nil, nil, err
	}
	return subs, w.warnings, nil
}

// subsystemWalker carries per-walk state: the cancellation context, the dump root
// (an os.Root that confines every path access beneath the dump), a parsed-node
// counter (breadth cap), and the accumulated drop diagnostics (each names the
// affected subsystem).
type subsystemWalker struct {
	ctx      context.Context
	root     *os.Root
	nodes    int
	warnings []string
}

// warn records a path-free diagnostic that NAMES the affected subsystem (name the
// drop, not just a count). Customer-facing RU: no тире, never an absolute path.
func (w *subsystemWalker) warn(name, reason string) {
	n := strings.TrimSpace(name)
	if n == "" {
		n = "(без имени)"
	}
	w.warnings = append(w.warnings, fmt.Sprintf("подсистема %s: %s", n, reason))
}

// warnSubsystemsRoot NAMES a refused top-level Subsystems catalog: a non-directory at
// the Subsystems position (a FIFO, socket, device, plain file, or a symlink standing in
// for it) drops the entire tree, which must never be silent. Path-free, no тире.
func (w *subsystemWalker) warnSubsystemsRoot() {
	w.warnings = append(w.warnings, "каталог подсистем дампа имеет неверный тип и пропущен")
}

// readDir reads a dump-relative directory confined to the walker's os.Root.
func (w *subsystemWalker) readDir(rel string) ([]os.DirEntry, error) {
	return readDirInRoot(w.root, rel)
}

// readDirInRoot reads a directory confined to root and returns its entries sorted
// by name (matching os.ReadDir), so the hierarchical walk's on-disk child ordering
// is deterministic. The directory is opened through openDirInRoot, which refuses any
// non-directory at that position (a FIFO/socket/device/plain file, or a symlink
// standing in for the directory) BEFORE the blocking open, so a planted writer-less
// FIFO at a directory position can never hang the walk. os.Root confines EVERY path
// component beneath the root using the OS primitive (openat2 RESOLVE_BENEATH on Linux,
// equivalents elsewhere), so an escaping symlink at ANY depth is refused rather than
// followed out of the dump. os.File.ReadDir (unlike os.ReadDir) returns entries in
// directory order, so they are sorted here.
func readDirInRoot(root *os.Root, rel string) ([]os.DirEntry, error) {
	f, err := openDirInRoot(root, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// openDirInRoot opens rel as a directory confined to root, refusing any non-directory
// at that position. It mirrors openSubsystemFile's guard, for directories: it lstats
// and requires Mode().IsDir() BEFORE the open, so a writer-less FIFO, socket, device,
// or a symlink standing in for the directory can never reach the blocking open() that
// ctx cannot interrupt. It then opens with O_NONBLOCK on unix, so a directory swapped
// for a FIFO in the check->use window still returns immediately instead of blocking,
// and fstats the descriptor to require it be the very directory the lstat saw (IsDir
// plus os.SameFile), which closes that TOCTOU window. Containment across every path
// component is enforced by os.Root, so an escaping symlink at ANY depth is refused
// rather than followed. A genuinely absent path returns os.ErrNotExist (callers treat
// it as a normal empty position); a non-directory returns errNotDirectory (callers
// NAME it); permission and containment ("path escapes") errors propagate unchanged.
func openDirInRoot(root *os.Root, rel string) (*os.File, error) {
	li, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !li.Mode().IsDir() {
		return nil, errNotDirectory
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|nonblockOpenFlag, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.IsDir() || !os.SameFile(li, fi) {
		_ = f.Close()
		return nil, errNotDirectory
	}
	return f, nil
}

// openSubsystemFile opens a dump-relative subsystem file for reading, confined to
// the walker's os.Root, and returns it only when it is a plain regular file.
// Containment across every path component is enforced by os.Root: an escaping
// symlink at ANY depth (intermediate directory OR final component) is refused, so
// no read can be redirected out of the dump. The pre-open lstat additionally
// rejects a non-regular final component (FIFO, socket, device, directory, or a
// symlink) with a NAMED, path-free warning: refusing a non-regular final component
// BEFORE the open is what stops a planted writer-less FIFO from blocking the walk
// forever (a DoS that even ctx cancellation could not interrupt), and refusing a
// symlinked final component keeps a crafted link from being followed at all. A
// genuinely absent file (ENOENT) is NOT a drop: it returns ok=false WITHOUT a
// warning, so the caller treats it as a normal non-subsystem entry (e.g. a
// directory that carries no Ext/Subsystem.xml).
func (w *subsystemWalker) openSubsystemFile(name, rel string) (*os.File, bool) {
	li, lerr := w.root.Lstat(rel)
	if lerr != nil {
		if errors.Is(lerr, os.ErrNotExist) {
			return nil, false // a genuinely absent file is a normal non-subsystem entry
		}
		// Containment refusal (an escaping symlink at any component, os.Root "path
		// escapes"), permission, or another metadata error: NAME it, never silently
		// drop (a silent drop would understate the tree without a trace).
		w.warn(name, "файл подсистемы недоступен и пропущен")
		return nil, false
	}
	if !li.Mode().IsRegular() {
		w.warn(name, "файл подсистемы не является обычным файлом и пропущен")
		return nil, false
	}
	f, ferr := w.root.OpenFile(rel, os.O_RDONLY|nonblockOpenFlag, 0)
	if ferr != nil {
		if errors.Is(ferr, os.ErrNotExist) {
			return nil, false // vanished after the lstat: treat as absent
		}
		w.warn(name, "не удалось открыть файл подсистемы")
		return nil, false
	}
	// Close the check->use (TOCTOU) window: the descriptor just opened must be the
	// very regular file the lstat saw. os.Root re-confines every component on this
	// call, so an escaping symlink swapped in after the lstat is refused; os.SameFile
	// rejects a final component swapped for a different in-root file; and O_NONBLOCK
	// (unix) stops a swapped-in writer-less FIFO from blocking the open.
	fi, serr := f.Stat()
	if serr != nil || !os.SameFile(li, fi) {
		_ = f.Close()
		w.warn(name, "файл подсистемы изменился во время чтения и пропущен")
		return nil, false
	}
	return f, true
}

// walkExt walks Subsystems/<N>/Ext/Subsystem.xml and returns the parsed list
// sorted alphabetically by subsystem name. A symlinked child dir has
// IsDir()==false and is skipped; an Ext/Subsystem.xml reached through an escaping
// symlink at ANY component is refused by the os.Root confinement in
// openSubsystemFile.
func (w *subsystemWalker) walkExt(relRoot string) ([]Subsystem, error) {
	if err := w.ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := w.readDir(relRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if errors.Is(err, os.ErrPermission) {
			return nil, errReadSubsystemsRoot // path-free
		}
		if errors.Is(err, errNotDirectory) {
			w.warnSubsystemsRoot() // a non-directory Subsystems position: NAME the drop
			return nil, nil
		}
		return nil, nil // containment refusal or other: empty tree, never leak
	}
	out := make([]Subsystem, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if w.nodes >= maxSubsystemNodes {
			w.warn(e.Name(), "превышено число подсистем в дампе")
			break
		}
		rel := filepath.Join(relRoot, e.Name(), "Ext", "Subsystem.xml")
		// A missing Ext/Subsystem.xml (ENOENT) is a normal non-subsystem dir and is
		// skipped silently by openSubsystemFile; a non-regular (FIFO/socket/device/
		// symlink), escaping, or otherwise unreadable file is NAMED there instead of
		// silently dropped (a silent drop would understate the tree without a trace).
		f, ok := w.openSubsystemFile(e.Name(), rel)
		if !ok {
			continue
		}
		s, perr := parseSubsystemXML(f)
		_ = f.Close()
		if perr != nil {
			w.warn(e.Name(), "не удалось разобрать XML подсистемы")
			continue
		}
		w.nodes++
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// walkHierarchical walks Subsystems/<Name>.xml then recursively
// Subsystems/<Name>/Subsystems/<Child>.xml. relRoot is the path relative to the
// dump root (read through the walker's os.Root, which confines every component so a
// crafted directory symlink at any depth is refused, not followed); parentPath
// threads the canonical "Подсистема.<Name>[.<Child>...]" form. depth caps recursion
// so a symlink cycle (also cut immediately by the os.Root refusal of an escaping
// recursion directory) or pathologically deep on-disk nesting terminates.
func (w *subsystemWalker) walkHierarchical(relRoot, parentPath string, depth int) ([]Subsystem, error) {
	if err := w.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > maxSubsystemTreeDepth {
		w.warn(parentPath, "превышена глубина вложенности подсистем")
		return nil, nil
	}
	entries, err := w.readDir(relRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if depth == 0 {
			if errors.Is(err, os.ErrPermission) {
				return nil, errReadSubsystemsRoot // path-free, top level only
			}
			if errors.Is(err, errNotDirectory) {
				w.warnSubsystemsRoot() // a non-directory Subsystems position: NAME the drop
				return nil, nil
			}
			return nil, nil // containment refusal or other at top: empty tree
		}
		if errors.Is(err, errNotDirectory) {
			// A non-directory at the recursion position (a FIFO/socket/device, or a
			// symlink standing in for the child Subsystems/ directory): refused BEFORE
			// the blocking open by openDirInRoot; skip its subtree, NAMED by its parent.
			w.warn(parentPath, "вложенный каталог подсистем имеет неверный тип и пропущен")
			return nil, nil
		}
		// A nested recursion directory that is unreadable or escapes the dump (an
		// intermediate directory symlink refused by os.Root): skip its subtree, NAMED.
		w.warn(parentPath, "каталог подсистемы недоступен и пропущен")
		return nil, nil
	}
	out := make([]Subsystem, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".xml") {
			continue
		}
		stem := strings.TrimSuffix(name, ".xml")
		if w.nodes >= maxSubsystemNodes {
			w.warn(stem, "превышено число подсистем в дампе")
			break
		}
		rel := filepath.Join(relRoot, name)
		f, ok := w.openSubsystemFile(stem, rel)
		if !ok {
			continue
		}
		s, perr := parseSubsystemHierarchical(f, parentPath)
		_ = f.Close()
		if perr != nil {
			w.warn(stem, "не удалось разобрать XML подсистемы")
			continue
		}
		w.nodes++
		children, cerr := w.walkHierarchical(filepath.Join(relRoot, stem, "Subsystems"), s.Path, depth+1)
		if cerr != nil {
			return nil, cerr // ctx cancellation propagates and aborts the walk
		}
		s.Children = children
		out = append(out, s)
	}
	return out, nil
}
