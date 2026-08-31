package dump

import (
	"os"
	"path/filepath"
	"testing"
)

// THE REMAINDER BELOW A RECOGNISED DEPTH TWO EXTENSION IS STILL MEASURED.
//
// wrapDepth's byPrefix arm returns anchorIndex(parts[2:]), and the two halves of
// that expression answer two different questions. The [2:] is what stops a
// correctly pointed nested extension from warning about itself: the two prefix
// segments are the namespace, the layout accounts for them, and counting them would
// put a notice in front of every operator whose tree the depth two descent has just
// repaired. The anchorIndex is what keeps the arm a MEASUREMENT: a path that
// carries further levels above its kind directory, INSIDE the extension, is still
// keyed with its anchor moved and still has to be counted.
//
// NOTHING PINNED THE SECOND HALF. Measured on this branch by replacing the arm's
// body with «return 0»: the whole suite stayed green, so every namespaced extension
// could have under reported its wrapping without a single test moving. The tree
// below carries one module at each answer, so one number tells the two halves apart:
// 2 would mean the prefix was counted, 0 would mean the remainder was not.
const (
	// The wrapper that is not itself an extension, and the extension inside it.
	wrapDepthWrapper = "Обёртка"
	wrapDepthNested  = "расширение"
	wrapDepthPrefix  = wrapDepthWrapper + "/" + wrapDepthNested
	wrapDepthExtName = "ГлубокоеРасширение"

	// Inside the extension: one module at its own root, and one under a further
	// directory level that the key derivation has to skip.
	wrapDepthAnchored = "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl"
	wrapDepthExtra    = "лишний/Documents/ПоступлениеТоваров/Ext/ObjectModule.bsl"

	wrapDepthAnchoredKey = "ext.ГлубокоеРасширение.Документ.ПеремещениеЗапасов.МодульОбъекта"
	wrapDepthExtraKey    = "ext.ГлубокоеРасширение.Документ.ПоступлениеТоваров.МодульОбъекта"

	wrapDepthAnchoredBody = "// на своём месте\n"
	wrapDepthExtraBody    = "// уровнем выше\n"
)

// mkWrapDepthTree writes a base configuration root, a wrapper that is not an
// extension, the extension two levels down, and the two modules inside it.
func mkWrapDepthTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkExtensionDump(t, filepath.Join(root, wrapDepthWrapper, wrapDepthNested),
		extManifestClassic, wrapDepthExtName)
	mkBSLFile(t, root, wrapDepthPrefix+"/"+wrapDepthAnchored, wrapDepthAnchoredBody)
	mkBSLFile(t, root, wrapDepthPrefix+"/"+wrapDepthExtra, wrapDepthExtraBody)
	return root
}

// TestWrappedPaths_ADepthTwoExtensionStillMeasuresWhatIsLeft.
func TestWrappedPaths_ADepthTwoExtensionStillMeasuresWhatIsLeft(t *testing.T) {
	for _, k := range []string{wrapDepthAnchoredKey, wrapDepthExtraKey} {
		if NFC(k) != k {
			t.Fatalf("test literal %q is not NFC, so it can never match an index key", k)
		}
	}

	root := mkWrapDepthTree(t)
	l := detectExtensionLayout(root)

	// PREMISE: the arm under test is the arm that answers. byPrefix is keyed by a
	// TWO segment prefix and byDir by one, so this also says the extension was
	// reached by the DESCENT and not by the depth one detection.
	if got := l.byPrefix[wrapDepthPrefix]; got != wrapDepthExtName {
		t.Fatalf("byPrefix[%q] = %q, want %q: the descent did not record this extension, so "+
			"wrapDepth never reaches the arm this test is about. Full layout: %+v",
			wrapDepthPrefix, got, wrapDepthExtName, l)
	}

	// THE TWO HALVES OF THE ARM, AS TWO NUMBERS ON ONE LAYOUT.
	//
	// The prefix is consumed: a module at the extension's own root is not wrapped.
	if got := l.wrapDepth(filepath.FromSlash(wrapDepthPrefix + "/" + wrapDepthAnchored)); got != 0 {
		t.Errorf("wrapDepth(%q) = %d, want 0: the two segments the namespace accounts for "+
			"were counted as a wrap, so a correctly pointed nested extension warns about "+
			"itself", wrapDepthPrefix+"/"+wrapDepthAnchored, got)
	}
	// And what is left is still measured.
	if got := l.wrapDepth(filepath.FromSlash(wrapDepthPrefix + "/" + wrapDepthExtra)); got != 1 {
		t.Errorf("wrapDepth(%q) = %d, want 1: below the prefix this path carries one more "+
			"directory level than the key derivation keeps, and the arm stopped measuring "+
			"the remainder", wrapDepthPrefix+"/"+wrapDepthExtra, got)
	}

	// AND THE NUMBER REACHES THE REPORT. wrapDepth is unexported and the operator
	// never sees it; WrappedPaths is what the notices are built from.
	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the build finished: %v", err)
	}

	// PREMISE: both modules were indexed, and both under the extension's namespace,
	// or the count below is a count over something else.
	for key, want := range map[string]string{
		wrapDepthAnchoredKey: wrapDepthAnchoredBody,
		wrapDepthExtraKey:    wrapDepthExtraBody,
	} {
		got, ok := idx.GetContent(key)
		if !ok {
			t.Fatalf("GetContent(%q) reported the module missing. Names: %v", key, idx.ModuleNames())
		}
		if got != want {
			t.Fatalf("GetContent(%q) = %q, want %q", key, got, want)
		}
	}

	if st := idx.WrappedPaths(); st.Files != 1 || st.Total != 2 {
		t.Errorf("WrappedPaths() = %+v, want {Files:1 Total:2}: one of the two modules "+
			"carries a directory level the key derivation had to skip and the other does "+
			"not, so any other pair of numbers is the arm counting the namespace or "+
			"measuring nothing below it", st)
	}
}

// TestWrappedPaths_TheDepthTwoArmControlCanStillCountAWrap is the control for the
// test above, and it is a SEPARATE test because it has to be able to fail on its own.
//
// «One wrapped file of two» is a claim about the ARM. The same tree with the nested
// manifest taken away has no byPrefix entry at all, so wrapDepth falls through to
// the whole path: both modules are then wrapped, and at DIFFERENT depths, which is
// the counter being alive rather than stuck.
func TestWrappedPaths_TheDepthTwoArmControlCanStillCountAWrap(t *testing.T) {
	root := mkWrapDepthTree(t)
	if err := os.Remove(filepath.Join(root, wrapDepthWrapper, wrapDepthNested, extManifestClassic)); err != nil {
		t.Fatal(err)
	}

	l := detectExtensionLayout(root)
	if len(l.byPrefix) != 0 {
		t.Fatalf("byPrefix = %v, want empty with the nested manifest removed", l.byPrefix)
	}
	if got := l.wrapDepth(filepath.FromSlash(wrapDepthPrefix + "/" + wrapDepthAnchored)); got != 2 {
		t.Errorf("wrapDepth(%q) = %d, want 2: with no namespace to account for them, both "+
			"prefix segments sit above the kind directory", wrapDepthPrefix+"/"+wrapDepthAnchored, got)
	}
	if got := l.wrapDepth(filepath.FromSlash(wrapDepthPrefix + "/" + wrapDepthExtra)); got != 3 {
		t.Errorf("wrapDepth(%q) = %d, want 3: the extra level inside the extension is on "+
			"top of the two prefix segments", wrapDepthPrefix+"/"+wrapDepthExtra, got)
	}

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the build finished: %v", err)
	}
	if st := idx.WrappedPaths(); st.Files != 2 || st.Total != 2 {
		t.Errorf("WrappedPaths() = %+v, want {Files:2 Total:2}: with no extension "+
			"recognised every path is keyed from below two directory levels, and a counter "+
			"that cannot reach 2 here says nothing about the 1 next door", st)
	}
}
