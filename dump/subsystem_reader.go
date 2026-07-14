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
// which on-disk layout it uses. The presence of any direct <Name>.xml file is
// unambiguous evidence of the Hierarchical layout (Конфигуратор 8.3.10+), so it
// wins; otherwise a present <N>/Ext/Subsystem.xml selects the Ext layout. An
// absent Subsystems/ yields (layoutExt, nil) so the walk returns an empty tree;
// an unreadable Subsystems/ yields the path-free errReadSubsystemsRoot.
func detectSubsystemLayout(dumpDir string) (subsystemLayout, error) {
	root := filepath.Join(dumpDir, "Subsystems")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return layoutExt, nil
		}
		return layoutExt, errReadSubsystemsRoot
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
		cand := filepath.Join(root, e.Name(), "Ext", "Subsystem.xml")
		if st, serr := os.Stat(cand); serr == nil && st.Mode().IsRegular() {
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
// confined to dumpDir via safeJoin (a crafted symlink that escapes the dump is
// skipped, never followed), recursion is depth-capped, and each dropped subsystem
// is NAMED in warnings (never silently dropped).
func ParseAllSubsystemsCtx(ctx context.Context, dumpDir string) ([]Subsystem, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	layout, err := detectSubsystemLayout(dumpDir)
	if err != nil {
		return nil, nil, err
	}
	w := &subsystemWalker{ctx: ctx, dumpRoot: dumpDir}
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
// every path is validated against, a parsed-node counter (breadth cap), and the
// accumulated drop diagnostics (each names the affected subsystem).
type subsystemWalker struct {
	ctx      context.Context
	dumpRoot string
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

// safe validates a relative-to-dumpRoot path and returns its safe absolute form,
// or ok=false if it escapes the dump (a crafted symlink or "..").
func (w *subsystemWalker) safe(rel ...string) (string, bool) {
	abs, err := safeJoin(w.dumpRoot, rel...)
	if err != nil {
		return "", false
	}
	return abs, true
}

// openSubsystemFile lstat-guards a subsystem file and opens it only when the final
// path is a plain regular file. Anything else (FIFO, socket, device, directory, or
// a symlink) is refused with a NAMED, path-free warning and ok=false. Refusing a
// non-regular final component BEFORE the open is what stops a planted writer-less
// FIFO from blocking the walk forever (a DoS that even ctx cancellation could not
// interrupt), and refusing a symlinked final component keeps a crafted link from
// redirecting the read out of the dump. A genuinely absent file (ENOENT) is NOT a
// drop: it returns ok=false WITHOUT a warning, so the caller treats it as a normal
// non-subsystem entry (e.g. a directory that carries no Ext/Subsystem.xml).
func (w *subsystemWalker) openSubsystemFile(name, filePath string) (*os.File, bool) {
	li, lerr := os.Lstat(filePath)
	if lerr != nil {
		if !os.IsNotExist(lerr) {
			w.warn(name, "не удалось открыть файл подсистемы")
		}
		return nil, false
	}
	if !li.Mode().IsRegular() {
		w.warn(name, "файл подсистемы не является обычным файлом и пропущен")
		return nil, false
	}
	f, ferr := openSubsystemFileFinal(filePath)
	if ferr != nil {
		if !os.IsNotExist(ferr) {
			w.warn(name, "не удалось открыть файл подсистемы")
		}
		return nil, false
	}
	// Close the check->use (TOCTOU) window: the descriptor just opened must be the
	// very file lstat saw as a regular file. If the final component was swapped
	// between the lstat and the open (e.g. for a symlink now resolving outside the
	// dump, or a FIFO), os.SameFile is false and the file is refused. On unix
	// openSubsystemFileFinal also opens with O_NOFOLLOW|O_NONBLOCK so a swapped-in
	// symlink fails the open outright and a swapped-in FIFO cannot block it.
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
// IsDir()==false and is skipped; an Ext/Subsystem.xml symlink that escapes the
// dump is rejected by safeJoin.
func (w *subsystemWalker) walkExt(relRoot string) ([]Subsystem, error) {
	if err := w.ctx.Err(); err != nil {
		return nil, err
	}
	root, ok := w.safe(relRoot)
	if !ok {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errReadSubsystemsRoot // path-free
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
		filePath, ok := w.safe(relRoot, e.Name(), "Ext", "Subsystem.xml")
		if !ok {
			w.warn(e.Name(), "файл вне дампа пропущен (символическая ссылка)")
			continue
		}
		// A missing Ext/Subsystem.xml (ENOENT) is a normal non-subsystem dir and is
		// skipped silently by openSubsystemFile; a non-regular (FIFO/socket/device/
		// symlink) or otherwise unreadable file is NAMED there instead of silently
		// dropped (a silent drop would understate the tree without a trace).
		f, ok := w.openSubsystemFile(e.Name(), filePath)
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
// Subsystems/<Name>/Subsystems/<Child>.xml. relRoot is the path relative to
// dumpRoot (safeJoin-validated each hop); parentPath threads the canonical
// "Подсистема.<Name>[.<Child>...]" form. depth caps recursion so a symlink cycle
// or pathologically deep on-disk nesting terminates. os.ReadDir returns entries
// sorted by filename.
func (w *subsystemWalker) walkHierarchical(relRoot, parentPath string, depth int) ([]Subsystem, error) {
	if err := w.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > maxSubsystemTreeDepth {
		w.warn(parentPath, "превышена глубина вложенности подсистем")
		return nil, nil
	}
	root, ok := w.safe(relRoot)
	if !ok {
		// relRoot escapes the dump (a crafted directory symlink): skip its subtree.
		w.warn(parentPath, "каталог вне дампа пропущен (символическая ссылка)")
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		if depth == 0 {
			return nil, errReadSubsystemsRoot // path-free, top level only
		}
		w.warn(parentPath, "не удалось прочитать каталог подсистемы")
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
		filePath, ok := w.safe(relRoot, name)
		if !ok {
			w.warn(stem, "файл вне дампа пропущен (символическая ссылка)")
			continue
		}
		f, ok := w.openSubsystemFile(stem, filePath)
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
