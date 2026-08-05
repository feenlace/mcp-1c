package dump

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
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
// holds two roots of the container kind side by side, and ~/Downloads/canon_vm and
// ~/Downloads/mcp-modified are flat ones carrying .bsl. Neither shape has anything
// a path-only rule could recognise: the flat one is byte for byte the layout of a
// base configuration dump, and the container one is a wrapper like any other. So
// the layout is a property OF THE DUMP ROOT, read once when the index is opened,
// and not something bslPathToModuleName could ever derive on its own. That is why
// this is a separate value threaded through the loaders rather than another branch
// in the key function: bslPathToModuleName stays a pure function of a path, and
// its pinned digests keep meaning what they meant.
//
// THE NAME COMES FROM THE MANIFEST, NEVER FROM THE DIRECTORY, and that is measured
// here rather than reported from elsewhere: ~/Downloads/canon_vm declares
// <Name>FeenlaceMCPService</Name> and ~/Downloads/mcp-modified declares
// <Name>MCP_Polling</Name>, so two of the four real extension dumps on this machine
// have a directory name that is not the extension name. A directory-derived
// namespace would file their modules under a name nobody can ask for. It also keeps
// the old hazard shut: a real configuration contains a CommonForm literally named
// «Расширения», and nothing here reads that word as evidence of anything.
//
// ---------------------------------------------------------------------------
// THE CONTRACT. Detection answers a question that HAS THREE ANSWERS, and the type
// below can hold all three. The previous version could hold two, and every
// ambiguity, every hostile input and every self-imposed limit was folded into the
// confident half of a boolean.
//
//	IS an extension        -> the manifest was read to its end and declares one,
//	                          under a name that can be part of a key
//	is NOT an extension    -> the manifest was read to its end and declares none,
//	                          or there is no manifest at all
//	UNDECIDED              -> evidence was absent, truncated, refused or hostile
//
// UNDECIDED NEVER PRODUCES A NAMESPACE. It produces the keys that shipped before
// any of this existed, plus a recorded doubt that both diagnostic channels report.
// The asymmetry is the whole reason for the third value: a false positive on a
// dump ROOT re-keys EVERY module in the dump (measured: all 13575 of
// dumps/dump_bsl), and a fabricated "ext.<Name>." prefix is an invention presented
// as fact, because the user cannot tell "not in the dump" from "filed under a name
// the server made up". A false negative loses one namespace, leaves the keys where
// they have always been, and the collapse counter in collapsed_keys.go measures
// whatever content that costs. A measured loss beats an unmeasurable invention.
//
// EVIDENCE AND PRECEDENCE, cheapest first:
//
//  1. The root's own manifest decides whether the root IS one extension.
//  2. Every immediate child directory is examined too, ALWAYS, including when the
//     root is itself an extension. That is what makes the container-with-a-manifest
//     case decidable at all rather than resolved by precedence: a child that
//     declares its own name owns its own subtree, and the root's name covers only
//     what is left. Skipping the children when the root matched is what filed
//     every child of such a container under the container's name.
//  3. A child whose name is a metadata kind directory (or "Ext", or the legacy
//     extension container) is NOT eligible to be a nested extension WHEN THE ROOT
//     IS ITSELF ONE: in that tree those directories are the root extension's own
//     content. When the root is not an extension there is no such tree to belong
//     to, and the manifest decides alone.
//
// WHAT GATES IT. <ObjectBelonging>Adopted</ObjectBelonging> INSIDE the <Properties>
// element, which is where the platform writes it (measured at byte 2340 of
// ~/Downloads/extdump_vm/FeenlaceMCPService/Configuration.xml, 17 bytes into a
// <Properties> that opens at 2323). NOT the purpose element:
// ConfigurationExtensionPurpose arrived in format 2.16 defaulting to Patch, so an
// export older than that carries none, and reading "no purpose element" as "not an
// extension" would drop every one of them.
//
// WHAT IS NOT HERE, said plainly rather than left to look measured. There is no
// EDT branch. There is no Configuration.mdo anywhere on this machine, so any
// strictness written for it would be invented from the same reported byte shape as
// the loose version it replaced, and a guard invented from a specification nobody
// can check is not a guard. The loose version was not theoretical: a base
// configuration head carrying the string "mdclassExtension:ConfigurationExtension"
// anywhere at all, a comment included, was accepted as an extension and named after
// the configuration itself. An EDT project therefore falls through to exactly the
// behaviour that shipped before this file existed, and no test in this tree claims
// EDT coverage. See TestEDTIsNotClaimedAndNotDetected.
// ---------------------------------------------------------------------------

const (
	// extManifestClassic is the ONE manifest that can carry the marker.
	// ConfigDumpInfo.xml is deliberately not among them: it says nothing about
	// extension-ness, and on a configuration it is enormous (20 591 903 bytes in
	// dumps/dump_2), so never opening it is worth stating rather than leaving to
	// chance.
	extManifestClassic = "Configuration.xml"

	// maxManifestHeadBytes bounds how much of a manifest is ever read.
	//
	// A CAP ON THE READ RATHER THAN ON THE FILE SIZE, and that is the stronger of
	// the two: a size cap makes the cost depend on the file, and this makes it
	// depend on nothing. A configuration's Configuration.xml is 1 339 696 bytes in
	// dumps/dump_2 and is answered from its first 256 KiB like everything else.
	//
	// The window is far larger than it needs to be for the shapes measured. In the
	// real base configuration <Properties> spans bytes 2378..12718, and the four
	// real extension manifests close theirs by byte 3571 at the latest, so every decision this
	// file makes is settled inside the first 13 KB. What could consume the window
	// is <InternalInfo>, which grows with the number of contained objects.
	//
	// A <Properties> that does not CLOSE inside the window is UNDECIDED and says
	// so. It is not read as "no marker found", which is what silently demoted such
	// a manifest to the base keyspace with nothing to show for it.
	maxManifestHeadBytes = 256 << 10

	// maxExtensionScan bounds how many candidate child directories are examined.
	// Reaching it is recorded as a doubt, because "no extension below this" and
	// "no extension among the first 64 directories" are different answers and the
	// difference is the one that turns a lossless tree lossy: an extension the scan
	// never reached keys into the base keyspace and collides with every other one
	// it never reached.
	maxExtensionScan = 64

	// maxExtensionNameRunes bounds an accepted extension name. It is a bound on
	// what becomes a key component and a rendered string, not a platform limit.
	maxExtensionNameRunes = 128
)

// manifestVerdict is the three-valued answer described in the contract above.
type manifestVerdict uint8

const (
	// manifestAbsent: there is no manifest here. Not an extension, no doubt.
	manifestAbsent manifestVerdict = iota
	// manifestNotExtension: the manifest was read to its end and declares none.
	manifestNotExtension
	// manifestExtension: it declares one, under a usable name.
	manifestExtension
	// manifestUndecided: a manifest is there and the question was not answered.
	manifestUndecided
)

// layoutDoubtReason names why a directory could not be decided. It is a closed
// enum rather than a free string so both delivery channels can render it without
// ever echoing bytes that came off disk.
type layoutDoubtReason uint8

const (
	doubtManifestUnreadable layoutDoubtReason = iota + 1
	doubtManifestNotRegular
	doubtManifestTruncated
	doubtNameRejected
	doubtScanTruncated
)

// layoutDoubt is one recorded "I do not know".
//
// dir is kept for the OPERATOR LOG only and is never rendered into an MCP answer:
// it is a directory name off disk, and a directory name off disk is untrusted text
// (a real customer tree contains «Доработки — копия»). The MCP side gets counts and
// the closed reason above, which is why it cannot carry a тире no matter what the
// tree is called.
type layoutDoubt struct {
	dir    string // "" means the dump root itself
	reason layoutDoubtReason
}

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
	// doubts is every directory whose extension-ness could not be decided. It is
	// never a reason to guess; it is the thing that gets reported.
	doubts []layoutDoubt
	// cost is what the detection actually spent, for the same reason
	// DumpRootInspection carries ReadDirs and Entries: the claim that this does not
	// walk the tree is worth exactly as much as the test that can measure it.
	cost extensionScanCost
}

// extensionScanCost is the syscall budget one detection spent.
type extensionScanCost struct {
	ReadDirs int // os.ReadDir calls, including the byte-exact name confirmations
	Lstats   int // os.Lstat calls, one per candidate directory
	Reads    int // manifests actually opened and read
}

// empty reports whether the layout changes nothing.
func (l extensionLayout) empty() bool { return l.self == "" && len(l.byDir) == 0 }

// moduleKey derives the index key for a dump-relative path under this layout.
//
// It delegates to bslPathToModuleName for everything except the namespace, so an
// extension's modules go through exactly the same derivation the base
// configuration does and land on the same "ext.<Имя>." shape the Расширения layout
// has always produced. One key shape, three ways of arriving at it.
//
// A CHILD BEATS THE ROOT, because a child match is the more specific evidence: in
// a container that carries its own manifest, "ExtA/..." is ExtA's and only what is
// left over is the container's. Resolving that by precedence instead, the way this
// did before, filed every child under the container's name and collided them with
// each other.
func (l extensionLayout) moduleKey(relPath string) string {
	slash := filepath.ToSlash(relPath)
	if len(l.byDir) > 0 {
		parts := strings.Split(slash, "/")
		// At least a directory and a file, or there is no extension subtree to be
		// inside of.
		if len(parts) >= 2 {
			if name, ok := l.byDir[parts[0]]; ok {
				return NFC("ext." + name + "." + bslPathToModuleName(strings.Join(parts[1:], "/")))
			}
		}
	}
	if l.self != "" {
		return NFC("ext." + l.self + "." + bslPathToModuleName(relPath))
	}
	return bslPathToModuleName(relPath)
}

// detectExtensionLayout reads dir and decides which of the two shapes it is, or
// neither, or that it does not know.
//
// THE COST. One ReadDir of dir, one Lstat per child directory, and a read of a
// manifest only where an Lstat found one. The extra ReadDir that confirms a
// manifest's name byte-exactly is paid ONLY on that same Lstat hit, which on a base
// configuration dump means once, at the root. TestLayoutDetectionCostIsBounded
// measures it as numbers rather than arguing it.
//
// KNOWN LIMIT, stated because it is real: GenSig hashes the .bsl files of a dump
// and nothing else, so ADDING a Configuration.xml to a dump that already has a
// warm generation does not change the signature and the old keys keep being
// served until something else invalidates it. The condition needs an operator to
// drop a manifest into an already-indexed tree, and the remedy is the one that
// already exists for every such case, `--reindex`.
func detectExtensionLayout(dir string) extensionLayout {
	var l extensionLayout

	ents, err := os.ReadDir(dir)
	if err != nil {
		// Unreadable root: not an extension and not the parent of one. The path
		// itself is a question dumpPathFault in cmd/mcp-1c already answers.
		return l
	}
	l.cost.ReadDirs++

	switch verdict, name, reason := manifestVerdictOf(dir, &l.cost); verdict {
	case manifestExtension:
		l.self = name
	case manifestUndecided:
		l.doubts = append(l.doubts, layoutDoubt{reason: reason})
	}

	scanned := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		child := e.Name()
		if l.self != "" && belongsToSelfExtension(child) {
			continue
		}
		if scanned == maxExtensionScan {
			l.doubts = append(l.doubts, layoutDoubt{reason: doubtScanTruncated})
			break
		}
		scanned++
		switch verdict, name, reason := manifestVerdictOf(filepath.Join(dir, child), &l.cost); verdict {
		case manifestExtension:
			if l.byDir == nil {
				l.byDir = make(map[string]string, 4)
			}
			l.byDir[child] = name
		case manifestUndecided:
			l.doubts = append(l.doubts, layoutDoubt{dir: child, reason: reason})
		}
	}
	return l
}

// belongsToSelfExtension reports whether a child directory of a root that is
// ITSELF an extension can only be that extension's own content. A metadata kind
// directory, the configuration-module directory and the legacy extension container
// are exactly those: inside a flat -Extension dump they are the extension's tree,
// and treating one as a nested extension would strip its first segment and derive
// a key from the remainder.
//
// The check applies ONLY when the root is an extension. When it is not, there is no
// tree for such a directory to belong to, and the manifest decides on its own.
func belongsToSelfExtension(child string) bool {
	if _, ok := dumpDirNames[child]; ok {
		return true
	}
	return child == configModuleDirName || child == extensionDirName
}

// extensionNameOf reports the extension name declared by a manifest in dir, and
// whether dir is an extension at all. Undecided is not an extension.
func extensionNameOf(dir string) (string, bool) {
	var cost extensionScanCost
	verdict, name, _ := manifestVerdictOf(dir, &cost)
	return name, verdict == manifestExtension
}

// manifestVerdictOf classifies the extension manifest of dir.
func manifestVerdictOf(dir string, cost *extensionScanCost) (manifestVerdict, string, layoutDoubtReason) {
	path := filepath.Join(dir, extManifestClassic)
	cost.Lstats++

	// Lstat FIRST, and lstat rather than stat: a symlink named Configuration.xml is
	// refused here rather than followed, which is the one read in this package that
	// was not contained by anything. It is also what keeps a FIFO from being opened
	// at all; nonblockOpenFlag below only closes the window between this check and
	// the open.
	lst, err := os.Lstat(path)
	if err != nil {
		return manifestAbsent, "", 0
	}

	// macOS and Windows answer Lstat for "configuration.xml" too, and dumproot.go
	// decides the very same question from a directory listing, where the comparison
	// is byte-exact. Two rules for one question is how the two files came to
	// disagree about the same tree, so the name is confirmed against the listing
	// here as well. The listing is read only because an Lstat already hit, which on
	// an ordinary dump means once.
	cost.ReadDirs++
	if !hasExactEntry(dir, extManifestClassic) {
		return manifestAbsent, "", 0
	}

	if !lst.Mode().IsRegular() {
		return manifestUndecided, "", doubtManifestNotRegular
	}

	cost.Reads++
	head, complete, reason := readManifestHead(path, lst)
	if reason != 0 {
		return manifestUndecided, "", reason
	}
	return classifyManifest(head, complete)
}

// hasExactEntry reports whether dir holds an entry whose name is byte-exactly name.
func hasExactEntry(dir, name string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(ents, func(e os.DirEntry) bool { return e.Name() == name })
}

// readManifestHead reads at most maxManifestHeadBytes from path and strips a UTF-8
// BOM. 1C writes its XML as UTF-8 WITH a BOM, and bytes.TrimSpace does not remove
// U+FEFF, so stripping it here is what stops the first element of the document
// from being invisible to every match below.
//
// complete reports that the whole file fit in the window. It is what lets a
// decision distinguish "the marker is not in this document" from "the marker may
// be past where I stopped reading", which the caller then reports instead of
// silently answering no.
//
// The open mirrors the pattern subsystem_reader.go already uses: nonblockOpenFlag
// so a final component swapped for a writer-less FIFO in the check-to-use window
// returns instead of blocking forever, and an os.SameFile against the pre-open
// lstat so a component swapped for a different file is refused.
func readManifestHead(path string, lst os.FileInfo) ([]byte, bool, layoutDoubtReason) {
	f, err := os.OpenFile(path, os.O_RDONLY|nonblockOpenFlag, 0)
	if err != nil {
		return nil, false, doubtManifestUnreadable
	}
	defer f.Close()

	fst, err := f.Stat()
	if err != nil {
		return nil, false, doubtManifestUnreadable
	}
	if !fst.Mode().IsRegular() || !os.SameFile(lst, fst) {
		return nil, false, doubtManifestNotRegular
	}

	// One byte past the window, so a file that exactly fills it is still known to
	// have ended.
	buf, err := io.ReadAll(io.LimitReader(f, maxManifestHeadBytes+1))
	if err != nil {
		return nil, false, doubtManifestUnreadable
	}
	complete := len(buf) <= maxManifestHeadBytes
	if !complete {
		buf = buf[:maxManifestHeadBytes]
	}
	return bytes.TrimPrefix(buf, []byte("\ufeff")), complete, 0
}

// classifyManifest decides from the <Properties> element and from nothing else.
//
// SCOPING THE MARKER TO <Properties> is what makes "not an extension" a real
// answer rather than the absence of a match anywhere in a truncated document. The
// platform writes ObjectBelonging as a child of Properties, immediately followed by
// Name; a match outside that element is not evidence about the configuration
// object, and a <Name> outside it belongs to something else entirely.
//
// It is a byte scan and not an XML parse on purpose: the input is a bounded head
// that may end mid-document, which a real parser would reject outright, and the two
// elements read here are simple text with no attributes and no nesting.
func classifyManifest(head []byte, complete bool) (manifestVerdict, string, layoutDoubtReason) {
	const openProps, closeProps = "<Properties>", "</Properties>"

	i := bytes.Index(head, []byte(openProps))
	if i < 0 {
		if complete {
			return manifestNotExtension, "", 0
		}
		return manifestUndecided, "", doubtManifestTruncated
	}
	rest := head[i+len(openProps):]
	j := bytes.Index(rest, []byte(closeProps))
	if j < 0 {
		if complete {
			return manifestNotExtension, "", 0
		}
		return manifestUndecided, "", doubtManifestTruncated
	}
	props := rest[:j]

	if !bytes.Contains(props, []byte("<ObjectBelonging>Adopted</ObjectBelonging>")) {
		return manifestNotExtension, "", 0
	}
	name, ok := elementText(props, "Name")
	if !ok || !validExtensionName(name) {
		// An extension we cannot name. Refusing is the whole point: the name is
		// about to become a key component and part of a rendered answer, and a
		// name that is not an identifier would arrive there as one.
		return manifestUndecided, "", doubtNameRejected
	}
	return manifestExtension, name, 0
}

// validExtensionName reports whether a manifest-declared name may become part of a
// key.
//
// AN ALLOWLIST BY UNICODE CATEGORY, not a list of characters to strip. Letters,
// decimal digits and the underscore, not starting with a digit, bounded in length.
// Every extension name on this machine passes it: FeenlaceMCPService, mcp_service,
// MCP_Polling, and the Доработки3D of the repository's own fixtures. Nothing else
// does, and that is the point rather than a side effect:
//
//   - the key is dot separated, so a name holding a dot would silently move every
//     other component one slot along;
//   - the name is rendered into one line of an MCP answer, so a newline, a control
//     character, a backtick or a leading ">" would leave the structure it was put
//     in;
//   - customer-facing RU carries no тире, and «Доработки — копия» is a real
//     directory name from a real customer tree.
//
// A REJECTED NAME IS NOT REPAIRED. Replacing the offending runes would invent a
// name the user cannot ask for and cannot recognise; the extension simply keeps the
// keys it had before this file existed, and the refusal is reported.
func validExtensionName(s string) bool {
	if s == "" {
		return false
	}
	n := 0
	for i, r := range s {
		n++
		if n > maxExtensionNameRunes {
			return false
		}
		switch {
		case r == '_':
		case unicode.IsLetter(r):
		case unicode.IsDigit(r):
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// elementText returns the text of the first <tag>...</tag> in b.
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

// ---------------------------------------------------------------------------
// Reporting what the detection decided, and what it could not.
// ---------------------------------------------------------------------------

// ExtensionLayoutSummary is a dump root's extension layout, in the shape a caller
// OUTSIDE this package can describe it in.
//
// THE SPLIT BETWEEN COUNTS AND NAMES IS THE POINT. Everything a customer-facing
// sentence may contain is a number or a boolean. Dirs holds directory names read
// off disk, and a directory name read off disk is untrusted text: a real customer
// tree contains «Доработки — копия», so a name spliced into RU prose puts a тире in
// it whatever the guard on that prose says. Dirs is for the operator log, where it
// belongs in an attribute beside the message rather than inside it.
type ExtensionLayoutSummary struct {
	// SelfNamed reports that the inspected path is itself one extension.
	SelfNamed bool
	// Extensions is how many immediate child directories are extensions.
	Extensions int
	// Dirs are those child directories, sorted. OPERATOR LOG ONLY; see above.
	Dirs []string
	// The four ways a directory can be undecided, counted separately because the
	// thing to do about each of them differs.
	NotRegular    int // a Configuration.xml that is not a regular file
	Unreadable    int // present, and the read failed
	ReadTruncated int // <Properties> did not close inside the read window
	NameRejected  int // declared a name that cannot be part of a key
	// ScanTruncated reports that the child scan stopped at maxExtensionScan, so
	// extensions below it were never looked for.
	ScanTruncated bool
}

// Undecided is how many directories the detection could not answer for.
func (s ExtensionLayoutSummary) Undecided() int {
	return s.NotRegular + s.Unreadable + s.ReadTruncated + s.NameRejected
}

// Quiet reports that there is nothing at all to say about this layout.
func (s ExtensionLayoutSummary) Quiet() bool {
	return !s.SelfNamed && s.Extensions == 0 && s.Undecided() == 0 && !s.ScanTruncated
}

// summary renders the internal layout into the reportable one.
func (l extensionLayout) summary() ExtensionLayoutSummary {
	var s ExtensionLayoutSummary
	s.SelfNamed = l.self != ""
	s.Extensions = len(l.byDir)
	for dir := range l.byDir {
		s.Dirs = append(s.Dirs, dir)
	}
	slices.Sort(s.Dirs)
	for _, d := range l.doubts {
		switch d.reason {
		case doubtManifestNotRegular:
			s.NotRegular++
		case doubtManifestUnreadable:
			s.Unreadable++
		case doubtManifestTruncated:
			s.ReadTruncated++
		case doubtNameRejected:
			s.NameRejected++
		case doubtScanTruncated:
			s.ScanTruncated = true
		}
	}
	return s
}

// InspectExtensionLayout reads dir and reports what its extension layout amounts
// to. It is the startup-time entry point, used before an index exists; the index's
// own answer, taken from the layout it actually keyed with, is ExtensionLayout.
func InspectExtensionLayout(dir string) ExtensionLayoutSummary {
	return detectExtensionLayout(dir).summary()
}

// layout resolves this index's extension layout, reading the manifests the first
// time and reusing that answer afterwards. It is the ONLY place idx.extLayout is
// read, and TestExtensionLayoutIsNeverReadAroundItsOnce fails the build when a
// second one appears.
//
// THE FIELD IS NOT THE LAYOUT UNTIL THE ONCE HAS RUN, and that distinction is not
// pedantry; it was a shipped defect. noteWrappedPaths read idx.extLayout directly,
// and the only caller that ever ran the Once was moduleKeyFor, which derives keys
// FROM PATHS. A cold build goes through it for every file. The warm manifest start
// and the read-only generation open take their DocIDs out of a manifest and never
// derive a key at all, so on those paths the field was still the zero value: an
// -AllExtensions container measured {Files:0 Total:2} cold and {Files:2 Total:2}
// warm over byte-identical keys, and the wrapped-path notice then told the operator
// on EVERY answer that the extension namespace was being lost and to restart
// against the dump root, which reproduces it. Reading the field is therefore an
// ordering assumption about a caller in another file, which is exactly the kind of
// assumption a sync.Once exists to remove.
//
// Cost on the warm paths is what detectExtensionLayout's own COST paragraph states
// and TestLayoutDetectionCostIsBounded measures, paid once per process. It was
// already being paid: withIndexProtectionNotice in tools/index_notice.go asks this
// index for ExtensionLayout on the first tool response either way.
//
// Safe to call with idx.mu held. detectExtensionLayout takes no index lock; it
// reads the filesystem from idx.dir, which is fixed at construction.
func (idx *Index) layout() extensionLayout {
	idx.extLayoutOnce.Do(func() { idx.extLayout = detectExtensionLayout(idx.dir) })
	return idx.extLayout
}

// ExtensionLayout reports the layout THIS INDEX derived its keys with, which is
// the one a tool answer should describe. It is the layout read once behind
// extLayoutOnce, so asking for it does not read the disk again and cannot disagree
// with the keys already served.
func (idx *Index) ExtensionLayout() ExtensionLayoutSummary {
	if idx == nil {
		return ExtensionLayoutSummary{}
	}
	return idx.layout().summary()
}
