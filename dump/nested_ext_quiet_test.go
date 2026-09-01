package dump

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A HEALTHY DUMP ROOT SAYS NOTHING, AND THE DESCENT'S OWN LIMITS ARE NOT A DOUBT.
//
// THE TWO EVENTS ARE NOT THE SAME EVENT. At depth one, maxExtensionScan means a
// child that WOULD have been classified was not, and that is a loss against the
// shipped contract, because depth one has always been scanned in full. At depth
// two it means the server declined an optional extra look that did not exist
// before this branch; falling back to the contract that already shipped is not a
// doubt. TestTheChildScanRecordsWhereItStopped in extlayout_test.go pins the depth
// one half and stays exactly as it is.
//
// EVERY ARM IS A TREE A CUSTOMER CAN HAVE. ExternalDataProcessors is deliberately
// absent from dumpDirNames (see metadata_types.go), so a base configuration dump
// that holds one is listed by the descent on every start; a shop with 33 external
// data processors is an ordinary shop.

// mkFillerDirs creates n empty subdirectories under root/child. It is the ONE
// variable every control below moves.
func mkFillerDirs(t *testing.T, root, child string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := os.MkdirAll(filepath.Join(root, child, fmt.Sprintf("шум%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAHealthyDumpRootIsQuietPastTheDescentCaps.
//
// EACH ARM CARRIES ITS CONTROL
func TestAHealthyDumpRootIsQuietPastTheDescentCaps(t *testing.T) {
	// ARM A: the per listing entry cap. A base configuration whose
	// ExternalDataProcessors directory holds one entry more than the descent will
	// list through.
	//
	// THE COST IS THE PREMISE. The refused tree spends FEWER syscalls than the
	// control, not more, and that inversion is what proves the guard fired: at 33
	// entries the listing is refused whole and no child of it is ever asked the
	// manifest question, at 32 every one of them is. Without it «quiet» could mean
	// the descent never reached this directory at all.
	t.Run("ArmA_PerListingEntryCap", func(t *testing.T) {
		build := func(t *testing.T, entries int) extensionLayout {
			t.Helper()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, extManifestClassic),
				[]byte(baseConfigManifest()), 0o644); err != nil {
				t.Fatal(err)
			}
			mkFillerDirs(t, root, "ExternalDataProcessors", entries)
			return detectExtensionLayout(root)
		}

		past := build(t, maxNestedProbeEntries+1)
		s := past.summary()
		if s.ScanTruncated {
			t.Errorf("summary = %+v, want ScanTruncated false: this root lost nothing and "+
				"has nothing ambiguous in it, and the notice this flag raises tells the "+
				"user that modules were indexed without an extension name", s)
		}
		if !s.Quiet() {
			t.Errorf("Quiet() = false on %+v: a healthy base configuration must produce no "+
				"standing notice at all", s)
		}
		if s.Extensions != 0 || s.Undecided() != 0 {
			t.Errorf("summary = %+v, want no extension and no undecided directory", s)
		}

		// PREMISE AND CONTROL IN ONE PAIR: one filler directory fewer, the listing is
		// exactly ON the cap, and every child of it IS asked the manifest question.
		at := build(t, maxNestedProbeEntries)
		cs := at.summary()
		if !cs.Quiet() {
			t.Fatalf("control failed: summary = %+v, want quiet one entry below the cap", cs)
		}
		if past.cost.Lstats >= at.cost.Lstats {
			t.Fatalf("control failed: refused tree spent %d Lstats against %d for the tree "+
				"one entry smaller. The refusal must COST LESS, or the quiet above is not "+
				"the guard firing: past=%+v at=%+v",
				past.cost.Lstats, at.cost.Lstats, past.cost, at.cost)
		}
	})

	// ARM B: the total budget, at BOTH of the two places it is checked.
	//
	// b1 spends the budget exactly on two children so the third is refused BEFORE
	// its listing; b2 leaves four questions and gives the third child ten
	// subdirectories so the budget runs out INSIDE the listing. No directory in
	// either tree is over the per listing cap, so arm A's guard cannot be what
	// answers here.
	t.Run("ArmB_TotalBudget", func(t *testing.T) {
		const first, second, third = "об01", "об02", "об03"
		build := func(t *testing.T, a, b, c int) extensionLayout {
			t.Helper()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, extManifestClassic),
				[]byte(baseConfigManifest()), 0o644); err != nil {
				t.Fatal(err)
			}
			mkFillerDirs(t, root, first, a)
			mkFillerDirs(t, root, second, b)
			mkFillerDirs(t, root, third, c)
			for _, n := range []int{a, b, c} {
				if n > maxNestedProbeEntries {
					t.Fatalf("premise broken: %d entries is over the per listing cap of %d, "+
						"so this arm would be measuring arm A's guard", n, maxNestedProbeEntries)
				}
			}
			return detectExtensionLayout(root)
		}

		// PREMISE: two full listings really do reach the budget, or neither case is
		// the one this arm names.
		if 2*maxNestedProbeEntries < maxNestedProbeChildren {
			t.Fatalf("premise broken: two listings of %d cannot reach a budget of %d",
				maxNestedProbeEntries, maxNestedProbeChildren)
		}

		// b1: the budget is gone before the third child is listed at all.
		spent := build(t, maxNestedProbeEntries, maxNestedProbeEntries, 1)
		ss := spent.summary()
		if ss.ScanTruncated || !ss.Quiet() {
			t.Errorf("summary = %+v, want quiet: the budget ran out before an unclassified "+
				"child was listed, and declining an optional extra look is not a doubt "+
				"about what the index holds", ss)
		}
		// CONTROL: the same three children, fewer filler directories, so the third
		// child IS listed. The listing is the one variable, and only the counter
		// tells the two apart now that neither says anything.
		roomy := build(t, 20, 20, 1)
		rs := roomy.summary()
		if !rs.Quiet() {
			t.Fatalf("control failed: summary = %+v, want quiet with the budget large enough", rs)
		}
		if spent.cost.ReadDirs >= roomy.cost.ReadDirs {
			t.Fatalf("control failed: the exhausted tree listed %d directories against %d "+
				"for the roomy one; the third child must go unlisted, or the quiet above "+
				"is not the budget: spent=%+v roomy=%+v",
				spent.cost.ReadDirs, roomy.cost.ReadDirs, spent.cost, roomy.cost)
		}

		// b2: the budget is gone BETWEEN two subdirectories of the third child, which
		// IS listed. A different event from b1 and a different call site.
		midway := build(t, 30, 30, 10)
		ms := midway.summary()
		if ms.ScanTruncated || !ms.Quiet() {
			t.Errorf("summary = %+v, want quiet: the budget ran out inside a listing, which "+
				"is a different event from running out before one and is just as much a "+
				"refusal to spend rather than a report of a loss", ms)
		}
		// CONTROL: the same three children with the same third child, ten fewer
		// filler directories in each of the first two, so the budget survives.
		enough := build(t, 20, 20, 10)
		es := enough.summary()
		if !es.Quiet() {
			t.Fatalf("control failed: summary = %+v, want quiet with the budget large enough", es)
		}
		if midway.cost.Lstats <= enough.cost.Lstats {
			t.Fatalf("control failed: the exhausted tree spent %d Lstats against %d for the "+
				"roomy one; the two trees differ in filler directories, so the counter must "+
				"move: midway=%+v enough=%+v",
				midway.cost.Lstats, enough.cost.Lstats, midway.cost, enough.cost)
		}
	})

	// ARM C: the sharper shape. A CORRECTLY RECOGNISED flat extension root, with one
	// neighbour directory over the entry cap. The root's own namespace was derived
	// from its own manifest and is being served; the notice fires anyway.
	//
	// THE FLAG IS ASSERTED, NOT THE PREDICATE. Quiet() is false here whatever this
	// change does, because SelfNamed is true, and it is false at the base commit too:
	// a root that IS an extension always has something to say about itself.
	t.Run("ArmC_RecognisedFlatRootWithALargeNeighbour", func(t *testing.T) {
		const extName = "ПлоскоеРасш"
		build := func(t *testing.T, entries int) extensionLayout {
			t.Helper()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, extManifestClassic),
				[]byte(classicExtensionManifest(extName, true)), 0o644); err != nil {
				t.Fatal(err)
			}
			mkFillerDirs(t, root, "Соседний", entries)
			return detectExtensionLayout(root)
		}

		past := build(t, maxNestedProbeEntries+1)
		s := past.summary()
		if !s.SelfNamed {
			t.Fatalf("premise broken: summary = %+v, want SelfNamed: this arm is about a "+
				"root whose OWN manifest was read and accepted", s)
		}
		if s.ScanTruncated {
			t.Errorf("summary = %+v, want ScanTruncated false: the root's namespace comes "+
				"from its own manifest and is being served, and a neighbour too large to "+
				"look into says nothing about it", s)
		}
		if s.Undecided() != 0 {
			t.Errorf("summary = %+v, want no undecided directory", s)
		}

		// CONTROL: one filler directory fewer, and the neighbour IS listed.
		at := build(t, maxNestedProbeEntries)
		cs := at.summary()
		if cs.ScanTruncated {
			t.Fatalf("control failed: summary = %+v, want no truncation on the cap", cs)
		}
		if past.cost.Lstats >= at.cost.Lstats {
			t.Fatalf("control failed: refused tree spent %d Lstats against %d for the tree "+
				"one entry smaller; the refusal must cost less: past=%+v at=%+v",
				past.cost.Lstats, at.cost.Lstats, past.cost, at.cost)
		}
	})
}

// TestAMissedNestedExtensionIsStillReported is what makes the deletion above safe
// rather than merely quiet.
//
// WHERE AN EXTENSION REALLY WAS MISSED, TWO CHANNELS THAT MEASURE STILL SPEAK. The
// wrapper below holds one entry more than the descent lists through, and one of
// those entries is a genuine extension with a module under it. The descent refuses
// the listing, the extension keeps the keys it had before any of this existed, and
// the module then collides with the configuration's own module of the same name:
// CollapsedKeys counts the file that is no longer servable and WrappedPaths counts
// the file whose name was derived from below a directory level that is not the dump
// root. Both are counted AFTER the keys are derived, so neither can be a guess
// about the shape of a directory.
//
// KNOWN HOLE, AND IT IS NOT CLOSED BY ANYTHING HERE. An extension whose ONLY module
// is Ext/ConfigurationModule.bsl sits at wrapDepth 0, so a descent that misses it
// leaves both counters at zero and nothing is said. That is equally true at the base
// commit, where the descent does not exist at all, so it is not a regression; it is
// a limit of the two channels and it is stated rather than argued away.
func TestAMissedNestedExtensionIsStillReported(t *testing.T) {
	const (
		wrapper      = "Обёртка"
		nested       = "extension"
		extName      = "Спрятанное"
		relTail      = "/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl"
		collidingKey = "Документ.ПеремещениеЗапасов.МодульОбъекта"
	)
	if NFC(collidingKey) != collidingKey {
		t.Fatalf("test literal %q is not NFC, so it can never match an index key", collidingKey)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkBSLFile(t, root, "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl", issue46BaseBody)
	mkExtensionDump(t, filepath.Join(root, wrapper, nested), extManifestClassic, extName)
	mkBSLFile(t, root, wrapper+"/"+nested+relTail, issue46ExtBody)
	// The extension plus maxNestedProbeEntries filler directories, so the wrapper's
	// listing is ONE entry past the cap and the extension inside it is never asked
	// about.
	mkFillerDirs(t, root, wrapper, maxNestedProbeEntries)

	ents, err := os.ReadDir(filepath.Join(root, wrapper))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != maxNestedProbeEntries+1 {
		t.Fatalf("premise broken: the wrapper holds %d entries, want %d, so the descent "+
			"would not be refused at all", len(ents), maxNestedProbeEntries+1)
	}

	idx, names := buildNoManifestIndex(t, root)

	// PREMISE: the extension really was missed. Without this the two counters below
	// would be measuring a tree where nothing went wrong.
	layout := idx.ExtensionLayout()
	if layout.Extensions != 0 {
		t.Fatalf("premise broken: ExtensionLayout() = %+v, want Extensions 0: the descent "+
			"was supposed to refuse this wrapper", layout)
	}
	assertNoExtNamespace(t, names)

	// AND THE FLAG SAYS NOTHING, because a refusal to spend is not a measurement.
	if layout.ScanTruncated {
		t.Errorf("ExtensionLayout() = %+v, want ScanTruncated false: the two counters below "+
			"are what report this loss, and they report it as numbers", layout)
	}

	// CHANNEL ONE: the file that is no longer servable.
	if st := idx.CollapsedKeys(); st.Files != 1 || st.Keys != 1 {
		t.Errorf("CollapsedKeys() = %+v, want {Files:1 Keys:1}: the missed extension's "+
			"module and the configuration's own module derive one key, and one of them "+
			"can no longer be served", st)
	}
	// CHANNEL TWO: the file whose name was derived from below the dump root.
	if st := idx.WrappedPaths(); st.Files != 1 || st.Total != 2 {
		t.Errorf("WrappedPaths() = %+v, want {Files:1 Total:2}: the module under the "+
			"missed extension carries two directory levels above the dump shaped tail", st)
	}
}
