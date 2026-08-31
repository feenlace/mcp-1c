package dump

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The four ways this package can start, measured against one another on the tree
// of issue 46.
//
// WHY PARITY IS THE INSTRUMENT. The descent that gives a nested extension its
// namespace lives inside detectExtensionLayout, behind extLayoutOnce, and exactly
// one thing runs that Once: moduleKeyFor, which derives a key FROM A PATH. A cold
// build goes through it once per file. The warm manifest start and the read-only
// generation open take their DocIDs straight out of a manifest and never derive a
// key at all. So a layout that is resolved lazily at key derivation, or read as a
// FIELD instead of through the accessor, is right on the cold build and empty on
// the two warm starts, and no cold-only test can see the difference.
//
// THAT IS NOT HYPOTHETICAL; IT SHIPPED ONCE IN THIS PACKAGE. noteWrappedPaths read
// idx.extLayout as a field, and the doc comment on Index.layout carries what it
// cost: an -AllExtensions container measured {Files:0 Total:2} cold and
// {Files:2 Total:2} warm over byte-identical keys, and the notice built on that
// number told the operator on EVERY answer that the extension namespace was being
// lost and to restart against the dump root, which reproduces it.
//
// AND THE COLD READING IS THE ONE NOBODY LIVES IN. NewIndex takes the
// cached-shards branch whenever shards exist and flatCacheSchemaStale says no, so
// an installation cold-builds once and is warm from then on; a cache is dropped
// only by a dumpIndexSchemaVersion / zapSegmentVersion change, which GenSig folds
// into a generation's signature as well. The warm legs below are therefore the ones
// that describe what an installed server answers with, and a cold-only test
// measures the single start it never repeats.
//
// WHAT IS COMPARED, and why each part is not implied by the others: the key
// MULTISET (ModuleNames keeps one entry per FILE, so a lost file shows up as a
// missing element rather than as a missing key), the collapse report, the wrap
// report, the layout summary, and the BYTES BEHIND EVERY KEY. The last one is load
// bearing: two starts can agree on every key and one of them still serve the wrong
// file, which is the harm issue 46 is actually about.

const (
	// The sibling extension that sits DIRECTLY under the dump root, the
	// -AllExtensions shape, reached through byDir rather than through the nested
	// byPrefix the fix added.
	//
	// IT IS IN THIS FIXTURE FOR A MEASURED REASON, NOT FOR VARIETY. It is the only
	// extension here reached through byDir rather than through byPrefix, so both
	// halves of wrapDepth are exercised, and against an empty layout its leading
	// segment counts as one wrap while a resolved layout accounts for it. That is
	// what makes reading the layout as a field OBSERVABLE from the reports instead
	// of merely asserted.
	issue46FlatExtDir  = "Adapt"
	issue46FlatExtName = "АдаптацияУчета"
	issue46FlatExtKey  = "ext.АдаптацияУчета.Документ.Продажа.МодульОбъекта"
	issue46FlatExtBody = "// соседнее расширение\n"

	// A wrapper with no manifest anywhere in it. Its module has nothing to
	// distinguish it from the base configuration's own, so it lands on the base
	// key: a real collision, and the whole point of the companion control.
	issue46CollidingDir = "Mach4/nomanifest"
)

// mkIssue46ParityTree writes the tree the four starts are compared over: the
// nested extension of issue 46, the base configuration whose key it used to land
// on, and a sibling extension one level up.
//
// withCollision adds a fourth module under a wrapper that declares nothing, which
// therefore derives the base configuration's key and leaves one of the two files
// unreachable. ITS BYTES ARE DELIBERATELY IDENTICAL to the base file's. Which of
// two colliding files ends up in pathByName is decided by Go map iteration order
// on every start but the cold one: loadFromManifestAndDiff and readGenerationNames
// both range over manifest.Files, while the cold build fills the map from WalkDir
// in lexical order. A content assertion over a collided key would therefore be
// measuring map randomisation and would flake. Identical bytes make the served
// content well defined while leaving the collision itself, which is what the
// control counts, entirely real.
func mkIssue46ParityTree(t *testing.T, withCollision bool) string {
	t.Helper()
	root := t.TempDir()

	// Every key this package emits has been through NFC, so a decomposed literal
	// could never match one and the test would fail for a reason with nothing to do
	// with the defect. Checked rather than assumed.
	for _, k := range []string{issue46BaseKey, issue46ExtKey, issue46FlatExtKey} {
		if NFC(k) != k {
			t.Fatalf("test literal %q is not NFC, so it can never match an index key", k)
		}
	}

	// The base configuration: its own manifest, which declares neither
	// ObjectBelonging nor a purpose, and one document module.
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkBSLFile(t, root, "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl", issue46BaseBody)

	// The extension of issue 46, TWO levels down: a container that is not itself an
	// extension, and the extension root inside it.
	mkExtensionDump(t, filepath.Join(root, "Mach3", "extension"),
		extManifestClassic, issue46ExtName)
	mkBSLFile(t, root, issue46ExtPrefix+"/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
		issue46ExtBody)

	// The sibling extension, ONE level down.
	mkExtensionDump(t, filepath.Join(root, issue46FlatExtDir),
		extManifestClassic, issue46FlatExtName)
	mkBSLFile(t, root, issue46FlatExtDir+"/Documents/Продажа/Ext/ObjectModule.bsl",
		issue46FlatExtBody)

	if withCollision {
		mkBSLFile(t, root,
			issue46CollidingDir+"/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
			issue46BaseBody)
	}
	return root
}

// issue46Reading is everything ONE start says about a dump, taken as one value for
// the reason CollapsedKeyState is one value: a comparison must not be able to read
// half its evidence from one start and half from another.
type issue46Reading struct {
	leg       string
	names     []string // ONE ENTRY PER FILE, sorted. A multiset, never a set.
	collapsed CollapsedKeyState
	wrapped   WrappedPathState
	layout    ExtensionLayoutSummary
	content   map[string]string // distinct key -> the bytes this start serves for it
	missing   []string          // distinct keys GetContent refused, sorted
	logs      []string          // INFO messages this start emitted, its branch proof
}

// readIssue46 takes the whole reading off a started index.
func readIssue46(leg string, idx *Index, logs []string) issue46Reading {
	r := issue46Reading{
		leg:       leg,
		names:     idx.ModuleNames(),
		collapsed: idx.CollapsedKeys(),
		wrapped:   idx.WrappedPaths(),
		layout:    idx.ExtensionLayout(),
		content:   map[string]string{},
		logs:      logs,
	}
	slices.Sort(r.names)
	for _, n := range r.names {
		if _, done := r.content[n]; done {
			continue
		}
		if body, ok := idx.GetContent(n); ok {
			r.content[n] = body
		} else {
			r.missing = append(r.missing, n)
		}
	}
	slices.Sort(r.missing)
	return r
}

// issue46Disagreements lists every way other differs from base, in words a failure
// can be read from without re-running anything.
func issue46Disagreements(base, other issue46Reading) []string {
	var out []string
	if !slices.Equal(base.names, other.names) {
		out = append(out, fmt.Sprintf("key multiset: %s has %v, %s has %v",
			other.leg, other.names, base.leg, base.names))
	}
	if base.collapsed.Files != other.collapsed.Files ||
		base.collapsed.Keys != other.collapsed.Keys ||
		!slices.Equal(base.collapsed.Sample, other.collapsed.Sample) {
		out = append(out, fmt.Sprintf("CollapsedKeys: %s = %+v, %s = %+v",
			other.leg, other.collapsed, base.leg, base.collapsed))
	}
	if base.wrapped != other.wrapped {
		out = append(out, fmt.Sprintf("WrappedPaths: %s = %+v, %s = %+v",
			other.leg, other.wrapped, base.leg, base.wrapped))
	}
	if !issue46LayoutsAgree(base.layout, other.layout) {
		out = append(out, fmt.Sprintf("ExtensionLayout: %s = %+v, %s = %+v",
			other.leg, other.layout, base.leg, base.layout))
	}
	if !slices.Equal(base.missing, other.missing) {
		out = append(out, fmt.Sprintf("keys GetContent refused: %s = %v, %s = %v",
			other.leg, other.missing, base.leg, base.missing))
	}
	for k, want := range base.content {
		got, ok := other.content[k]
		if !ok {
			continue // already reported by the multiset or the refusal list
		}
		if got != want {
			out = append(out, fmt.Sprintf("GetContent(%q): %s serves %q, %s serves %q",
				k, other.leg, got, base.leg, want))
		}
	}
	return out
}

// issue46LayoutsAgree compares two summaries field by field, because the type
// carries a slice and is therefore not comparable with ==.
func issue46LayoutsAgree(a, b ExtensionLayoutSummary) bool {
	return a.SelfNamed == b.SelfNamed &&
		a.Extensions == b.Extensions &&
		slices.Equal(a.Dirs, b.Dirs) &&
		a.NotRegular == b.NotRegular &&
		a.Unreadable == b.Unreadable &&
		a.ReadTruncated == b.ReadTruncated &&
		a.NameRejected == b.NameRejected &&
		a.Malformed == b.Malformed &&
		a.Unscannable == b.Unscannable &&
		a.ScanTruncated == b.ScanTruncated
}

// issue46SawLog reports whether msg is among the messages a start emitted.
func issue46SawLog(logs []string, msg string) bool {
	return slices.Contains(logs, msg)
}

// issue46FourStarts makes the four starts over root and returns their readings in
// leg order: cold, warm, read-only generation, incremental.
//
// EVERY LEG PROVES WHICH PATH IT TOOK FROM THE PATH'S OWN LOG LINE, never from how
// long it took. "Opened cached index" is emitted only by the cached-shards branch
// of NewIndex, "Building index" only by buildShards, "Incremental update" only
// where a non-empty diff was applied, and "Opened read-only index generation" only
// by openReadOnlyFrom. Each is logged before the goroutine's deferred close of
// idx.done, so a record is guaranteed to be there once Done() has fired.
//
// It asserts the branch and the absence of build errors and NOTHING ELSE. The
// premises about what the numbers should BE belong to the caller, so the two tests
// in this file can share the machinery and still fail independently.
func issue46FourStarts(t *testing.T, root, cacheDir string) []issue46Reading {
	t.Helper()

	cpath, err := cachePath(root, cacheDir)
	if err != nil {
		t.Fatalf("cachePath(%s, %s): %v", root, cacheDir, err)
	}
	var out []issue46Reading

	// A. COLD. The build every installation makes exactly once.
	rec := captureLogs(t)
	cold, err := NewIndex(root, cacheDir, false)
	if err != nil {
		t.Fatalf("cold NewIndex: %v", err)
	}
	<-cold.Done()
	if err := cold.BuildError(); err != nil {
		t.Fatalf("cold build error: %v", err)
	}
	coldLogs := rec.atLevel(slog.LevelInfo)
	if !issue46SawLog(coldLogs, "Building index") {
		t.Fatalf("the cold start did not log \"Building index\"; it took some other "+
			"branch and the parity below would be comparing three warm starts. Logs: %v",
			coldLogs)
	}
	out = append(out, readIssue46("cold", cold, coldLogs))
	// WAIT FOR THE MANIFEST, not merely for readiness. buildShards flips
	// ready.Store(true) and only THEN calls saveManifest (see waitReady's own
	// comment), so a reopen gated on readiness alone can race the save and read a
	// state no installation ever starts from.
	waitManifest(t, cpath, 60*time.Second)
	if err := cold.Close(); err != nil {
		t.Fatalf("closing the cold index: %v", err)
	}

	// B. WARM MANIFEST REOPEN, against the SAME cache directory. This is the start
	// every installation makes after the first one, and its DocIDs come out of the
	// manifest rather than from a key derivation.
	rec = captureLogs(t)
	warm, err := NewIndex(root, cacheDir, false)
	if err != nil {
		t.Fatalf("warm NewIndex: %v", err)
	}
	<-warm.Done()
	if err := warm.BuildError(); err != nil {
		t.Fatalf("warm build error: %v", err)
	}
	warmLogs := rec.atLevel(slog.LevelInfo)
	if !issue46SawLog(warmLogs, "Opened cached index") {
		t.Fatalf("the warm reopen did not log \"Opened cached index\", so it did not take "+
			"the cached-shards branch and this leg is not the warm start it claims to be. "+
			"Logs: %v", warmLogs)
	}
	if issue46SawLog(warmLogs, "Building index") {
		t.Fatalf("the warm reopen logged \"Building index\": it rebuilt from the filesystem, "+
			"so it derived every key through moduleKeyFor and cannot witness a layout that "+
			"the warm path leaves unresolved. Logs: %v", warmLogs)
	}
	if issue46SawLog(warmLogs, "Incremental update") {
		t.Fatalf("the warm reopen applied a diff over an UNCHANGED tree, so this leg is the "+
			"incremental one and leg D no longer differs from it. Logs: %v", warmLogs)
	}
	out = append(out, readIssue46("warm", warm, warmLogs))
	if err := warm.Close(); err != nil {
		t.Fatalf("closing the warm index: %v", err)
	}

	// C. READ-ONLY GENERATION OPEN, the other start that derives no key:
	// loadNamesReadOnly takes names, paths and DocIDs from the generation's own
	// manifest and never calls moduleKeyFor.
	gensig := mustGenSig(t, root)
	if err := BuildGeneration(root, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	rec = captureLogs(t)
	ro, err := OpenGenerationReadOnly(root, cacheDir, gensig)
	if err != nil {
		t.Fatalf("OpenGenerationReadOnly: %v", err)
	}
	<-ro.Done()
	if err := ro.BuildError(); err != nil {
		t.Fatalf("read-only build error: %v", err)
	}
	roLogs := rec.atLevel(slog.LevelInfo)
	if !issue46SawLog(roLogs, "Opened read-only index generation") {
		t.Fatalf("the read-only open did not log \"Opened read-only index generation\". "+
			"Logs: %v", roLogs)
	}
	out = append(out, readIssue46("read-only", ro, roLogs))
	if err := ro.Close(); err != nil {
		t.Fatalf("closing the read-only index: %v", err)
	}

	// D. INCREMENTAL. One module is touched so the manifest diff is non-empty and
	// the addition-and-modification loop runs over it.
	//
	// THE MTIME MOVES AND THE BYTES DO NOT, deliberately. Manifest.Diff compares
	// ModTime().UnixMilli() and Size, so a pure mtime change is a Modified entry;
	// rewriting the content instead would make this leg serve different bytes from
	// the other three BY CONSTRUCTION and there would be nothing left to compare.
	//
	// The module chosen is the NESTED extension's, the one whose key exists only
	// because the layout was resolved.
	touched := filepath.Join(root, filepath.FromSlash(
		issue46ExtPrefix+"/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl"))
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(touched, past, past); err != nil {
		t.Fatalf("touching %s: %v", touched, err)
	}
	rec = captureLogs(t)
	incr, err := NewIndex(root, cacheDir, false)
	if err != nil {
		t.Fatalf("incremental NewIndex: %v", err)
	}
	<-incr.Done()
	if err := incr.BuildError(); err != nil {
		t.Fatalf("incremental build error: %v", err)
	}
	incrLogs := rec.atLevel(slog.LevelInfo)
	if !issue46SawLog(incrLogs, "Opened cached index") {
		t.Fatalf("the incremental start did not log \"Opened cached index\". Logs: %v", incrLogs)
	}
	if !issue46SawLog(incrLogs, "Incremental update") {
		t.Fatalf("the incremental start did not log \"Incremental update\", so the touch "+
			"produced no diff and this leg is a second copy of the warm one. Logs: %v", incrLogs)
	}
	out = append(out, readIssue46("incremental", incr, incrLogs))
	if err := incr.Close(); err != nil {
		t.Fatalf("closing the incremental index: %v", err)
	}

	return out
}

// TestIssue46_WarmAndReadOnlyStartsAgreeWithTheColdOne is the parity the fix
// depends on and that nothing else in this branch measures.
//
// The descent that produces byPrefix runs inside detectExtensionLayout, behind
// extLayoutOnce, so it is paid on every start. Filling byPrefix lazily during key
// derivation instead was considered and rejected for exactly the reason this test
// pins: two of the four starts derive no keys, so the map would still be empty when
// they answer.
func TestIssue46_WarmAndReadOnlyStartsAgreeWithTheColdOne(t *testing.T) {
	root := mkIssue46ParityTree(t, false)
	legs := issue46FourStarts(t, root, t.TempDir())
	cold := legs[0]

	// PREMISES, on the cold reading. Without them the parity below could be parity
	// over a tree that was never recognised as holding an extension at all, which
	// is the exact shape "everything agrees" is trivially true of.
	wantNames := []string{issue46BaseKey, issue46FlatExtKey, issue46ExtKey}
	slices.Sort(wantNames)
	if !slices.Equal(cold.names, wantNames) {
		t.Fatalf("cold key multiset = %v, want %v: the premise of every comparison below "+
			"is that the tree produced these three keys", cold.names, wantNames)
	}
	if cold.layout.Extensions != 2 {
		t.Fatalf("cold ExtensionLayout = %+v, want both extensions recognised: the nested "+
			"one at %q through byPrefix and the sibling at %q through byDir",
			cold.layout, issue46ExtPrefix, issue46FlatExtDir)
	}
	wantDirs := []string{issue46FlatExtDir, issue46ExtPrefix}
	if !slices.Equal(cold.layout.Dirs, wantDirs) {
		t.Fatalf("cold ExtensionLayout().Dirs = %v, want %v", cold.layout.Dirs, wantDirs)
	}
	if cold.collapsed.Files != 0 {
		t.Fatalf("cold CollapsedKeys = %+v, want no collapse: the fix exists so these two "+
			"documents keep separate keys", cold.collapsed)
	}
	// NOTHING IS WRAPPED ON THIS TREE. Both segments of the nested extension's
	// prefix are accounted for by the namespace it now has, exactly as the
	// sibling's one segment is. A zero cannot carry the instrument's aliveness on
	// its own, which is what TestIssue46_TheParityControlStillCountsACollisionAndAWrap
	// is for; what is asserted here is that the four starts AGREE on the number.
	if want := (WrappedPathState{Files: 0, Total: 3}); cold.wrapped != want {
		t.Fatalf("cold WrappedPaths = %+v, want %+v", cold.wrapped, want)
	}
	if len(cold.missing) != 0 {
		t.Fatalf("cold start could not serve %v", cold.missing)
	}
	if cold.content[issue46BaseKey] != issue46BaseBody ||
		cold.content[issue46ExtKey] != issue46ExtBody ||
		cold.content[issue46FlatExtKey] != issue46FlatExtBody {
		t.Fatalf("cold start serves the wrong bytes: base=%q ext=%q sibling=%q",
			cold.content[issue46BaseKey], cold.content[issue46ExtKey],
			cold.content[issue46FlatExtKey])
	}

	// THE PROPERTY.
	for _, leg := range legs[1:] {
		for _, d := range issue46Disagreements(cold, leg) {
			t.Errorf("the %s start disagrees with the cold one. %s\n\n"+
				"The layout is read once behind extLayoutOnce and only key derivation runs "+
				"that Once; a start that takes its DocIDs from a manifest derives no key, so "+
				"a layout resolved anywhere other than inside detectExtensionLayout is empty "+
				"here while the cold build had it. This is the shape that shipped once "+
				"already: see the doc comment on Index.layout.", leg.leg, d)
		}
	}
}

// TestIssue46_TheParityControlStillCountsACollisionAndAWrap is the positive
// control for the test above, and it is a SEPARATE test because it has to be able
// to fail on its own.
//
// Parity is satisfied by an instrument that has died exactly as well as by one that
// works: "the four agree" is true when all four report nothing for the wrong
// reason. So the same four starts are made over the same tree PLUS a wrapper that
// declares no extension, where a module really does land on the base
// configuration's key. All four must count the collision, count the wrapped file,
// still recognise both extensions, and still carry the base key TWICE in a multiset
// that a set-valued reading would silently repair.
func TestIssue46_TheParityControlStillCountsACollisionAndAWrap(t *testing.T) {
	root := mkIssue46ParityTree(t, true)
	legs := issue46FourStarts(t, root, t.TempDir())
	cold := legs[0]

	// The base key appears TWICE: one entry for the base configuration's own file
	// and one for the module under the undeclared wrapper.
	wantNames := []string{issue46BaseKey, issue46BaseKey, issue46FlatExtKey, issue46ExtKey}
	slices.Sort(wantNames)
	if !slices.Equal(cold.names, wantNames) {
		t.Fatalf("control cold key multiset = %v, want %v", cold.names, wantNames)
	}
	wantCollapsed := CollapsedKeyState{Files: 1, Keys: 1, Sample: []string{issue46BaseKey}}
	if cold.collapsed.Files != wantCollapsed.Files ||
		cold.collapsed.Keys != wantCollapsed.Keys ||
		!slices.Equal(cold.collapsed.Sample, wantCollapsed.Sample) {
		t.Fatalf("control cold CollapsedKeys = %+v, want %+v: without a real collision the "+
			"parity in the test above is parity over a counter that never counts",
			cold.collapsed, wantCollapsed)
	}
	if want := (WrappedPathState{Files: 1, Total: 4}); cold.wrapped != want {
		t.Fatalf("control cold WrappedPaths = %+v, want %+v", cold.wrapped, want)
	}
	if cold.layout.Extensions != 2 {
		t.Fatalf("control cold ExtensionLayout = %+v, want both extensions still recognised: "+
			"the undeclared wrapper must not have disturbed the descent", cold.layout)
	}
	if len(cold.missing) != 0 {
		t.Fatalf("control cold start could not serve %v", cold.missing)
	}

	// THE PROPERTY, over values that are all non-trivial.
	for _, leg := range legs[1:] {
		for _, d := range issue46Disagreements(cold, leg) {
			t.Errorf("control: the %s start disagrees with the cold one. %s\n\n"+
				"The four starts must agree on a LOSS as exactly as they agree on a clean "+
				"dump; a report that is silent on both proves nothing about either.",
				leg.leg, d)
		}
	}
}
