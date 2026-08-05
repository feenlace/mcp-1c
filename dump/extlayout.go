package dump

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The two dump shapes 1C actually emits for configuration extensions.
//
// WHAT WAS WRONG. This package keyed extensions off a top-level directory
// literally named "Расширения". THE PLATFORM NEVER WRITES THAT DIRECTORY. It came
// from one customer's hand made folder and was generalised into a rule. What 1C
// really produces is:
//
//	-AllExtensions <dir>      <dir>/<ExtensionName>/<Kind>/...   one subdir each,
//	                          named after the extension, no wrapper
//	-Extension <Name> <dir>   FLAT: <dir>/Configuration.xml, <dir>/ConfigDumpInfo.xml
//	                          and the kind directories at the top level
//
// Both were verified against real output on this machine; ~/Downloads/extdump_vm
// holds two roots of the flat kind side by side. Neither shape has anything a
// path-only rule could recognise: the flat one is byte for byte the layout of a
// base configuration dump, and the container one is a wrapper like any other. So
// the layout is a property OF THE DUMP ROOT, read once when the index is opened,
// and not something bslPathToModuleName could ever derive on its own. That is why
// this is a separate value threaded through the loaders rather than another branch
// in the key function: bslPathToModuleName stays a pure function of a path, and
// its pinned digests keep meaning what they meant.
//
// THE NAME COMES FROM THE MANIFEST, NEVER FROM THE DIRECTORY. The directory is
// reported to disagree with the extension name in real EDT projects (src/cfe/yaxunit
// holding YAXUNIT is the example given), and a directory-derived namespace would
// then file a module under a name nobody can ask for. It also keeps the old hazard
// shut: a real configuration contains a CommonForm literally named «Расширения»,
// and nothing here reads that word at all.
//
// WHAT COULD NOT BE VERIFIED HERE, said plainly rather than left to look measured.
// The two real extension dumps on this machine are the CLASSIC export, and their
// directory happens to agree with their <Name>, so they do not exercise the
// disagreement. There is no Configuration.mdo anywhere on this machine, so the EDT
// branch below is written from the reported byte shape and is exercised only by a
// fixture built to that shape. If it is wrong, it is wrong in the direction of
// recognising nothing: a .mdo whose markers differ simply fails to match, and the
// dump keys exactly as it did before this file existed.
//
// WHAT GATES IT. <ObjectBelonging>Adopted</ObjectBelonging> in the classic export,
// or the camelCase <objectBelonging> plus the mdclassExtension:ConfigurationExtension
// xsi:type in EDT. NOT the purpose element: ConfigurationExtensionPurpose arrived
// in format 2.16 defaulting to Patch, so an export older than that carries none,
// and reading "no purpose element" as "not an extension" would drop every one of
// them.

const (
	// extManifestClassic and extManifestEDT are the two manifests that can carry
	// the marker. ConfigDumpInfo.xml is deliberately NOT among them: it says nothing
	// about extension-ness, and on a configuration it is enormous (20 591 903 bytes
	// in dumps/dump_2), so never opening it is worth stating rather than leaving to
	// chance.
	extManifestClassic = "Configuration.xml"
	extManifestEDT     = "Configuration.mdo"

	// maxManifestHeadBytes bounds how much of a manifest is ever read.
	//
	// A CAP ON THE READ RATHER THAN ON THE FILE SIZE, and that is the stronger of
	// the two: a size cap makes the cost depend on the file, and this makes it
	// depend on nothing. A configuration's Configuration.xml is 1 339 696 bytes in
	// dumps/dump_2 and is answered from its first 256 KiB like everything else.
	//
	// The window is far larger than it needs to be for the shapes measured. In the
	// real base configuration <Properties> begins at byte 2378 and <ChildObjects> at
	// 12735, so the whole decision is settled inside the first 13 KB; the two real
	// extension manifests are 3754 and 3536 bytes end to end. What consumes the
	// window is <InternalInfo>, which grows with the number of contained objects, so
	// the margin is there for an extension that adopts a great many of them.
	//
	// A marker beyond the window reads as "not an extension", which is exactly the
	// behaviour that shipped before any of this existed. Degrading, never failing.
	maxManifestHeadBytes = 256 << 10

	// maxExtensionScan bounds how many children are examined when looking for the
	// -AllExtensions shape, for the same reason maxNestedRootScan exists: the cost
	// of that branch grows with the directory, and a dump root has tens of children,
	// not thousands.
	maxExtensionScan = 64
)

// extensionLayout says how a dump ROOT relates to configuration extensions. The
// zero value means "an ordinary dump", and it changes no key whatsoever, which is
// the property that keeps every existing installation on the keys it already has.
type extensionLayout struct {
	// self is the extension name when the root ITSELF is one extension, the
	// -Extension shape. Empty otherwise.
	self string
	// byDir maps an immediate child directory to the extension name its own
	// manifest declares, the -AllExtensions shape. Nil otherwise.
	byDir map[string]string
}

// empty reports whether the layout changes nothing.
func (l extensionLayout) empty() bool { return l.self == "" && len(l.byDir) == 0 }

// moduleKey derives the index key for a dump-relative path under this layout.
//
// It delegates to bslPathToModuleName for everything except the namespace, so an
// extension's modules go through exactly the same derivation the base
// configuration does and land on the same "ext.<Имя>." shape the Расширения layout
// has always produced. One key shape, three ways of arriving at it.
func (l extensionLayout) moduleKey(relPath string) string {
	if l.self != "" {
		return NFC("ext." + l.self + "." + bslPathToModuleName(relPath))
	}
	if len(l.byDir) > 0 {
		parts := strings.Split(filepath.ToSlash(relPath), "/")
		// At least a directory and a file, or there is no extension subtree to be
		// inside of.
		if len(parts) >= 2 {
			if name, ok := l.byDir[parts[0]]; ok {
				return NFC("ext." + name + "." + bslPathToModuleName(strings.Join(parts[1:], "/")))
			}
		}
	}
	return bslPathToModuleName(relPath)
}

// detectExtensionLayout reads dir and decides which of the two shapes it is, or
// neither.
//
// ORDER MATTERS AND IS CHEAPEST FIRST. If the root itself carries an extension
// manifest it IS the extension and no child is examined at all, which is both the
// right answer and one file read. Only when it does not are the children looked
// at, and then a base configuration dump costs one ReadDir plus a failed open per
// child, all of which are ENOENT.
//
// KNOWN LIMIT, stated because it is real: GenSig hashes the .bsl files of a dump
// and nothing else, so ADDING a Configuration.xml to a dump that already has a
// warm generation does not change the signature and the old keys keep being
// served until something else invalidates it. The condition needs an operator to
// drop a manifest into an already-indexed tree, and the remedy is the one that
// already exists for every such case, `--reindex`.
func detectExtensionLayout(dir string) extensionLayout {
	if name, ok := extensionNameOf(dir); ok {
		return extensionLayout{self: name}
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		return extensionLayout{}
	}
	var byDir map[string]string
	scanned := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if scanned == maxExtensionScan {
			break
		}
		scanned++
		name, ok := extensionNameOf(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		if byDir == nil {
			byDir = make(map[string]string, 4)
		}
		byDir[e.Name()] = name
	}
	return extensionLayout{byDir: byDir}
}

// extensionNameOf reports the extension name declared by a manifest in dir, and
// whether dir is an extension at all.
//
// A directory with no manifest, an unreadable one, or one whose marker is absent
// is simply not an extension. There is no error result on purpose: every failure
// here has the same consequence, which is that the dump keys exactly as it did
// before this file existed.
func extensionNameOf(dir string) (string, bool) {
	if head, ok := manifestHead(filepath.Join(dir, extManifestClassic)); ok {
		if name, ok := classicExtensionName(head); ok {
			return name, true
		}
	}
	if head, ok := manifestHead(filepath.Join(dir, extManifestEDT)); ok {
		if name, ok := edtExtensionName(head); ok {
			return name, true
		}
	}
	return "", false
}

// manifestHead reads at most maxManifestHeadBytes from path and strips a UTF-8
// BOM. 1C writes its XML as UTF-8 WITH a BOM, and bytes.TrimSpace does not remove
// U+FEFF, so stripping it here is what stops the first element of the document
// from being invisible to every match below.
func manifestHead(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, maxManifestHeadBytes))
	if err != nil {
		return nil, false
	}
	return bytes.TrimPrefix(head, []byte("\ufeff")), true
}

// classicExtensionName reads the classic XML export. The gate is ObjectBelonging;
// the name is the FIRST <Name> after it, which is where the platform writes it
// (<Properties> holds ObjectBelonging immediately followed by Name) and which
// cannot be satisfied by a <Name> belonging to something else earlier in the file.
func classicExtensionName(head []byte) (string, bool) {
	i := bytes.Index(head, []byte("<ObjectBelonging>Adopted</ObjectBelonging>"))
	if i < 0 {
		return "", false
	}
	return elementText(head[i:], "Name")
}

// edtExtensionName reads the EDT .mdo export. Either marker is enough: the
// camelCase objectBelonging, or the xsi:type that declares the mdclass an
// extension. The name is the first <name>, which in that format is a child of the
// root element and precedes both markers, so it is searched from the start rather
// than from the marker.
func edtExtensionName(head []byte) (string, bool) {
	if !bytes.Contains(head, []byte("<objectBelonging>Adopted</objectBelonging>")) &&
		!bytes.Contains(head, []byte("mdclassExtension:ConfigurationExtension")) {
		return "", false
	}
	return elementText(head, "name")
}

// elementText returns the text of the first <tag>...</tag> in b. It is a byte
// scan and not an XML parse on purpose: the input is a bounded head that may end
// mid-document, which a real parser would reject outright, and the two elements
// read here are simple text with no attributes and no nesting.
func elementText(b []byte, tag string) (string, bool) {
	open := []byte("<" + tag + ">")
	i := bytes.Index(b, open)
	if i < 0 {
		return "", false
	}
	rest := b[i+len(open):]
	j := bytes.Index(rest, []byte("</"+tag+">"))
	if j < 0 {
		return "", false
	}
	name := strings.TrimSpace(string(rest[:j]))
	if name == "" {
		return "", false
	}
	// NFC at the same chokepoint every other key component goes through: the name
	// becomes part of a docID, and a decomposed one would never match an NFC query.
	return NFC(name), true
}
