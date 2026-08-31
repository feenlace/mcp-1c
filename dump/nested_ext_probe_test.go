package dump

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The COST and the BOUNDS of the one-level descent that finds an extension sitting
// two directories below the dump root.
//
// WHY A SEPARATE FILE FROM THE OUTCOME TEST. nested_ext_depth_test.go states what
// the user must get; these state what the server is allowed to spend getting it.
// The two fail for different reasons and a reader chasing a cost regression should
// not have to read past the outcome to find the numbers.
//
// EVERY NUMBER BELOW IS A MEASUREMENT, not a budget somebody chose afterwards.
// TestLayoutDetectionCostIsBounded already pins the depth-one scan by exact
// equality over a five-kind base configuration; the descent added here must leave
// those numbers untouched, and the tests here say what it costs where it does fire.
//
// EACH TEST CARRIES ITS CONTROL, and every control differs from the case it guards
// in EXACTLY ONE VARIABLE: a directory NAME, the presence of ONE manifest, ONE
// filler directory. A control built from a differently shaped tree would pass for
// reasons that have nothing to do with the clause it is supposed to be measuring.

// nestedProbeKind is a directory name that IS a metadata kind, so it can only be
// the content of whatever sits above it and is never probed for a nested extension.
// Taken from dumpDirNames rather than typed, so an entry leaving that table is
// visible here instead of turning this test into a green nothing.
func nestedProbeKindNames(t *testing.T) []string {
	t.Helper()
	want := []string{"Catalogs", "Documents", "CommonModules", "Reports", "Enums"}
	for _, k := range want {
		if _, ok := dumpDirNames[k]; !ok {
			t.Fatalf("premise broken: %q is no longer in dumpDirNames, so this test would "+
				"be measuring the descent it means to prove is skipped", k)
		}
	}
	return want
}

// TestNestedProbeCostOnTheIssue46Tree pins what the descent spends on the tree of
// the issue, by exact equality in all three counters.
//
// PINNED AS MEASURED. The value derived by hand from the source before the code
// existed was ReadDirs 4, Lstats 4, Reads 2; the numbers below are what the
// detector actually returns, and the test says which by failing if either moves.
//
// The premise is asserted first on purpose: a scan that found nothing would spend
// less and pass a cost test that was written on its own.
func TestNestedProbeCostOnTheIssue46Tree(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkBSLFile(t, root, "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl", issue46BaseBody)
	mkExtensionDump(t, filepath.Join(root, "Mach3", "extension"), extManifestClassic, issue46ExtName)
	mkBSLFile(t, root, issue46ExtPrefix+"/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl",
		issue46ExtBody)

	l := detectExtensionLayout(root)

	// PREMISE. Without the extension actually found, every number below is the cost
	// of a scan that did nothing, and the test would pin the wrong thing.
	if got := l.summary(); got.Extensions != 1 {
		t.Fatalf("premise broken: summary = %+v, want exactly one extension found at %q",
			got, issue46ExtPrefix)
	}

	want := extensionScanCost{ReadDirs: 4, Lstats: 4, Reads: 2}
	if l.cost != want {
		t.Errorf("cost = %+v, want %+v. Exact equality, not a ceiling: a listing or a "+
			"manifest read that grows with the size of the dump is the regression this "+
			"pin exists to catch, and an inequality cannot see it.", l.cost, want)
	}
}

// TestNestedProbeDoesNotDescendIntoKindDirectories.
//
// A metadata kind directory of a dump is the configuration's own content. Listing
// one costs a syscall per kind on every start and can only ever find a manifest
// somebody planted, so the descent must refuse it BY NAME, before the listing.
//
// THE CONTROL IS THE SAME TREE WITH THE DIRECTORIES RENAMED. One variable, the
// name, and nothing else: same depth, same manifests, same extension names. If the
// name check were removed the first tree would start reporting the five planted
// extensions and its ReadDirs would jump, which is precisely what the control tree
// shows the descent doing when the name does not stop it.
func TestNestedProbeDoesNotDescendIntoKindDirectories(t *testing.T) {
	kinds := nestedProbeKindNames(t)

	build := func(t *testing.T, children []string) extensionLayout {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, extManifestClassic),
			[]byte(baseConfigManifest()), 0o644); err != nil {
			t.Fatal(err)
		}
		for i, c := range children {
			mkExtensionDump(t, filepath.Join(root, c, "Вложенное"), extManifestClassic,
				fmt.Sprintf("Подделка%d", i))
		}
		return detectExtensionLayout(root)
	}

	skipped := build(t, kinds)
	if !skipped.empty() {
		t.Errorf("a base configuration whose kind directories hold planted manifests "+
			"produced a layout: self=%q byDir=%v byPrefix=%v", skipped.self, skipped.byDir,
			skipped.byPrefix)
	}
	// ZERO EXTRA LISTINGS. Two is the whole budget of a flat base configuration: one
	// for the root, one to confirm the root manifest's name byte-exactly.
	if want := (extensionScanCost{ReadDirs: 2, Lstats: 1 + len(kinds), Reads: 1}); skipped.cost != want {
		t.Errorf("cost = %+v, want %+v: a kind directory was listed, so the cost of "+
			"detection now grows with the number of kinds in the dump", skipped.cost, want)
	}

	// CONTROL: the same tree, the same planted manifests, names that are NOT kinds.
	renamed := make([]string, len(kinds))
	for i := range kinds {
		renamed[i] = fmt.Sprintf("Обёртка%d", i)
	}
	probed := build(t, renamed)
	for i, c := range renamed {
		prefix := c + "/Вложенное"
		if got, want := probed.byPrefix[prefix], fmt.Sprintf("Подделка%d", i); got != want {
			t.Fatalf("control failed: byPrefix[%q] = %q, want %q. The descent has to fire "+
				"on a name that is not a kind, or the skip above proves nothing: byPrefix=%v",
				prefix, got, want, probed.byPrefix)
		}
	}
	if probed.cost.ReadDirs <= skipped.cost.ReadDirs {
		t.Fatalf("control failed: renaming the children left ReadDirs at %d against %d; "+
			"the two trees differ only in the directory names, so the counter must move",
			probed.cost.ReadDirs, skipped.cost.ReadDirs)
	}
}

// TestNestedProbeDoesNotDescendIntoARecognisedExtension.
//
// A child that declares its own extension OWNS its subtree, and the shipped rule in
// extlayout.go:moduleKey is that a child beats the root. Descending into it would
// look for a second namespace inside a namespace that is already assigned, and a
// hit would take the key away from the extension whose manifest named it.
//
// THE CONTROL IS THE SAME TREE MINUS ONE FILE: the container's own manifest. With
// it, the container is a recognised extension and nothing below is probed; without
// it, the container is unclassified and the very same nested manifest IS found.
func TestNestedProbeDoesNotDescendIntoARecognisedExtension(t *testing.T) {
	build := func(t *testing.T, withOwnManifest bool) extensionLayout {
		t.Helper()
		root := t.TempDir()
		dirA := filepath.Join(root, "dirA")
		mkExtensionDump(t, filepath.Join(dirA, "внутри"), extManifestClassic, "Вложенное")
		if withOwnManifest {
			if err := os.WriteFile(filepath.Join(dirA, extManifestClassic),
				[]byte(classicExtensionManifest("ИмяА", true)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return detectExtensionLayout(root)
	}

	recognised := build(t, true)
	if recognised.byDir["dirA"] != "ИмяА" {
		t.Fatalf("premise broken: byDir = %v, want dirA recognised as ИмяА", recognised.byDir)
	}
	if len(recognised.byPrefix) != 0 {
		t.Errorf("byPrefix = %v, want empty: the container is already an extension and "+
			"owns its subtree, so nothing below it may take a second namespace",
			recognised.byPrefix)
	}
	if want := (extensionScanCost{ReadDirs: 2, Lstats: 2, Reads: 1}); recognised.cost != want {
		t.Errorf("cost = %+v, want %+v: a recognised extension was listed as well as read",
			recognised.cost, want)
	}
	// The key still comes from the container's own manifest.
	const rel = "dirA/внутри/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl"
	if got, want := recognised.moduleKey(rel),
		"ext.ИмяА.Документ.ПеремещениеЗапасов.МодульОбъекта"; got != want {
		t.Errorf("moduleKey(%q) = %q, want %q", rel, got, want)
	}

	// CONTROL: delete the container's own manifest and nothing else.
	absent := build(t, false)
	if got, want := absent.byPrefix["dirA/внутри"], "Вложенное"; got != want {
		t.Fatalf("control failed: byPrefix[%q] = %q, want %q. With the container's own "+
			"manifest gone the descent must find the nested one, or the empty byPrefix "+
			"above says nothing about the recognised case: byPrefix=%v",
			"dirA/внутри", got, want, absent.byPrefix)
	}
	if len(absent.byDir) != 0 {
		t.Fatalf("control failed: byDir = %v, want empty with the manifest deleted", absent.byDir)
	}
}

// TestNestedProbeStopsAtALargeDirectoryAndSaysSo.
//
// The descent lists a directory it knows nothing about, so its cost is that
// directory's size. Past maxNestedProbeEntries it stops, and STOPPING IS REPORTED:
// «there is no extension below this» and «I did not look below this» are different
// answers, and the second one is the one that turns a lossless tree lossy without
// anything to show for it. The doubt is the same doubtScanTruncated the depth-one
// cap already records, so it reaches the operator through the channel that exists.
//
// THE CONTROL IS ONE FILLER DIRECTORY FEWER. It sits exactly ON the guard rather
// than far from it, so the pair measures the boundary and not the general idea that
// small trees are scanned.
func TestNestedProbeStopsAtALargeDirectoryAndSaysSo(t *testing.T) {
	const (
		wrapper  = "Обёртка"
		nested   = "extension"
		extName  = "Спрятанное"
		relTail  = "/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl"
		baseKey  = "Документ.ПеремещениеЗапасов.МодульОбъекта"
		nsdKey   = "ext." + extName + "." + baseKey
		relUnder = wrapper + "/" + nested + relTail
	)

	build := func(t *testing.T, filler int) extensionLayout {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, extManifestClassic),
			[]byte(baseConfigManifest()), 0o644); err != nil {
			t.Fatal(err)
		}
		mkExtensionDump(t, filepath.Join(root, wrapper, nested), extManifestClassic, extName)
		for i := 0; i < filler; i++ {
			if err := os.MkdirAll(filepath.Join(root, wrapper, fmt.Sprintf("шум%03d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return detectExtensionLayout(root)
	}

	// The wrapper holds the extension plus maxNestedProbeEntries filler directories,
	// so its listing is one entry PAST the guard.
	big := build(t, maxNestedProbeEntries)
	s := big.summary()
	if !s.ScanTruncated {
		t.Errorf("summary = %+v, want ScanTruncated: a directory too large to probe was "+
			"passed over in silence, which reads as «no extension below this»", s)
	}
	if len(big.byPrefix) != 0 || s.Extensions != 0 {
		t.Errorf("byPrefix = %v, Extensions = %d: the guard did not stop the descent",
			big.byPrefix, s.Extensions)
	}
	// THE KEYS ARE THE ONES THAT SHIPPED. Asserted against the literal base key, not
	// against bslPathToModuleName: with an empty layout moduleKey delegates straight
	// to it, so comparing the two would be one expression written twice.
	if got := big.moduleKey(relUnder); got != baseKey {
		t.Errorf("moduleKey(%q) = %q, want %q: refusing to probe must leave the key where "+
			"it has always been, never invent one", relUnder, got, baseKey)
	}

	// CONTROL: one filler directory fewer, so the listing is exactly ON the guard.
	small := build(t, maxNestedProbeEntries-1)
	cs := small.summary()
	if got, want := small.byPrefix[wrapper+"/"+nested], extName; got != want {
		t.Fatalf("control failed: byPrefix[%q] = %q, want %q. At exactly the guard the "+
			"descent must still run, or the refusal above is not the SIZE: byPrefix=%v",
			wrapper+"/"+nested, got, want, small.byPrefix)
	}
	if cs.ScanTruncated {
		t.Fatalf("control failed: summary = %+v, want no truncation one entry below the guard", cs)
	}
	if got := small.moduleKey(relUnder); got != nsdKey {
		t.Fatalf("control failed: moduleKey(%q) = %q, want %q", relUnder, got, nsdKey)
	}
}

// TestNestedProbeSpendsABoundedTotalAcrossChildren.
//
// maxNestedProbeEntries bounds ONE listing. It does not bound the sum: a root with
// many unclassified children, each just under that guard, would pay their product.
// maxNestedProbeChildren is the bound on the sum, and it is written as
// maxExtensionScan so the second level can never spend more manifest questions than
// the first is already allowed to.
//
// A CAP THAT IS NEVER REACHED IS A CAP NOTHING MEASURES. The budget is checked in
// TWO places and they are different events, so both are driven here: BEFORE a
// child is listed at all, and BETWEEN two subdirectories of one child. Removing
// either one leaves the other passing, which is exactly why one case would not do.
//
// EACH CASE DIFFERS FROM THE CONTROL IN ONE DIRECTORY. Not in a shape, not in a
// size class: one filler directory for the first, one sibling for the second.
func TestNestedProbeSpendsABoundedTotalAcrossChildren(t *testing.T) {
	const (
		extName = "ЗаБюджетом"
		relTail = "/Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl"
		baseKey = "Документ.ПеремещениеЗапасов.МодульОбъекта"
	)
	// os.ReadDir sorts by name, and it sorts by BYTES: strings.Compare over the
	// entry names. That is why the two filler children are listed before the third,
	// and why the sibling below is spelled with a leading ASCII "a" rather than a
	// Cyrillic one. A Cyrillic first letter is two bytes beginning 0xD0 and sorts
	// AFTER "extension", which is exactly what the first version of this test did:
	// the extension was then asked about first, found, and the case measured
	// nothing. The premise below holds that shut.
	const first, second, last, sibling = "аПервый", "бВторой", "вПоследний", "aДругой"
	relUnder := last + "/extension" + relTail

	build := func(t *testing.T, fillerInSecond int, withSibling bool) extensionLayout {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, extManifestClassic),
			[]byte(baseConfigManifest()), 0o644); err != nil {
			t.Fatal(err)
		}
		fill := func(child string, n int) {
			for i := 0; i < n; i++ {
				if err := os.MkdirAll(filepath.Join(root, child, fmt.Sprintf("шум%03d", i)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}
		fill(first, maxNestedProbeEntries)
		fill(second, fillerInSecond)
		mkExtensionDump(t, filepath.Join(root, last, "extension"), extManifestClassic, extName)
		if withSibling {
			if err := os.MkdirAll(filepath.Join(root, last, sibling), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return detectExtensionLayout(root)
	}

	// PREMISE: two full listings really can reach the budget, or neither case below
	// is the one this test names.
	if 2*maxNestedProbeEntries < maxNestedProbeChildren {
		t.Fatalf("premise broken: two listings of %d cannot reach a budget of %d, so "+
			"nothing here reaches the cap", maxNestedProbeEntries, maxNestedProbeChildren)
	}
	// PREMISE: the sibling really is listed BEFORE the extension. os.ReadDir sorts
	// with strings.Compare, which is this same comparison.
	if !(sibling < "extension") {
		t.Fatalf("premise broken: %q sorts after \"extension\", so the extension would be "+
			"asked about first and the mid-listing case below would measure nothing", sibling)
	}

	// CONTROL, stated first because both cases are read against it: with the budget
	// just large enough the extension IS found and nothing is doubted.
	left := build(t, maxNestedProbeEntries-1, false)
	cs := left.summary()
	if got, want := left.byPrefix[last+"/extension"], extName; got != want {
		t.Fatalf("control failed: byPrefix[%q] = %q, want %q. With one question left in "+
			"the budget the last child must still be probed, or neither miss below is "+
			"the BUDGET: byPrefix=%v", last+"/extension", got, want, left.byPrefix)
	}
	if cs.ScanTruncated {
		t.Fatalf("control failed: summary = %+v, want no truncation with the budget just "+
			"large enough", cs)
	}

	// ONE FILLER DIRECTORY MORE, and the budget is gone before the last child is
	// even listed.
	spent := build(t, maxNestedProbeEntries, false)
	s := spent.summary()
	if !s.ScanTruncated {
		t.Errorf("summary = %+v, want ScanTruncated: the budget ran out before the last "+
			"child and saying nothing reads as «no extension below this»", s)
	}
	if s.Extensions != 0 {
		t.Errorf("summary = %+v, want no extension found past an exhausted budget", s)
	}
	if got := spent.moduleKey(relUnder); got != baseKey {
		t.Errorf("moduleKey(%q) = %q, want %q: an exhausted budget must leave the key "+
			"where it has always been", relUnder, got, baseKey)
	}
	// AND THE LAST CHILD IS NEVER LISTED AT ALL. The budget is checked BEFORE the
	// listing as well as inside it, and only a cost assertion can tell the two
	// apart: with the earlier check removed every assertion above still holds, one
	// more os.ReadDir is spent, and it is spent once per remaining child on a tree
	// that may hold maxExtensionScan of them. MEASURED, not budgeted: the run that
	// first took these numbers is what they are pinned from.
	if want := (extensionScanCost{
		ReadDirs: 4,
		Lstats:   1 + 3 + 2*maxNestedProbeEntries,
		Reads:    1,
	}); spent.cost != want {
		t.Errorf("cost = %+v, want %+v. ReadDirs counts the root, the byte-exact "+
			"confirmation of its manifest name, and the TWO children that fit in the "+
			"budget. A third listing means the last child was opened after the budget "+
			"was already gone.", spent.cost, want)
	}

	// ONE SIBLING DIRECTORY MORE, and the budget is gone BETWEEN two subdirectories
	// of the last child: the sibling takes the final question and the extension
	// behind it is never asked about.
	midway := build(t, maxNestedProbeEntries-1, true)
	ms := midway.summary()
	if !ms.ScanTruncated {
		t.Errorf("summary = %+v, want ScanTruncated: the budget ran out inside a listing, "+
			"which is a different event from running out before one", ms)
	}
	if _, ok := midway.byPrefix[last+"/extension"]; ok {
		t.Errorf("byPrefix = %v: the extension was reached past an exhausted budget, so "+
			"the total the descent spends is bounded only by the per-listing guard",
			midway.byPrefix)
	}
}
