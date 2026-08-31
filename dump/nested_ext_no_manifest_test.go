package dump

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// THE RULING THIS FILE PINS: a nested tree with NO extension manifest keeps
// merging into the base namespace, and keeps being counted by the collapse
// notice. It is a DECISION, not an oversight, and it is written down here so that
// changing it is a change to a test rather than a quiet change to a keyspace.
//
// WHY THE DECISION GOES THAT WAY. There is nothing to act on. The only thing such
// a tree offers is the name of a directory, and this package refuses to read a
// directory name as evidence of extension-ness. extlayout.go says so in its own
// header, and it says so from measurement rather than from taste:
//
//	«THE NAME COMES FROM THE MANIFEST, NEVER FROM THE DIRECTORY»
//
// backed there by two NAMED dumps on the machine that comment was written on,
// ~/Downloads/canon_vm declaring <Name>FeenlaceMCPService</Name> and
// ~/Downloads/mcp-modified declaring <Name>MCP_Polling</Name>, each disagreeing
// with its own directory name. That comment NAMES the two rather than counting
// them against a total, and says why: the named pair is checkable and a machine
// wide total is not. Neither of the two is an EDT project, and there is no EDT
// anywhere in that record; the same header states there is no Configuration.mdo
// on that machine at all. So the evidence here is «two real dumps, both of them
// wrong about their own directory», and this file does not enlarge it.
//
// index.go carries the sentence «A directory name is not a claim; it is the path»,
// and it is worth saying what that sentence does there, because it is not this
// argument. It sits at the legacy Расширения branch of bslPathToModuleName and
// argues the OPPOSITE direction: a directory name may be echoed into a key WITHOUT
// validExtensionName, precisely because passing a path segment through is not the
// server making a claim about a tree. Minting a NAMESPACE from that same segment
// would be one, which is why detection never does it. The two sentences agree and
// they are not interchangeable.
//
// WHAT THE MERGE COSTS IS MEASURED AND NOT HIDDEN. collapsed_keys.go counts every
// file that lost its content to an overwrite, so the loss shows up as a number in
// a tool answer rather than as a module that quietly serves the wrong bytes. A
// measured loss beats an invented namespace, and that trade is the whole of the
// three valued contract at the top of extlayout.go. The tests below assert BOTH
// halves: the keys did not move, AND the counter still reports what that cost.

const (
	// The wrapper is deliberately Cyrillic while its sibling is not. os.ReadDir
	// sorts by BYTES, so a Cyrillic initial sorts AFTER a latin initial one; no
	// assertion here depends on which of two colliding files the walk reached
	// last, and this fixture is shaped so that a reader cannot accidentally add
	// one.
	noManifestWrapper = "Обёртка"
	noManifestInner   = "Внутри"

	noManifestRootKey  = "Документ.Основной.МодульОбъекта"
	noManifestInnerKey = "Документ.Вложенный.МодульОбъекта"
)

// mkNoManifestTree builds the shape all three tests share: a base configuration at
// the dump root, one module of its own, and a wrapper holding a second dump two
// levels down whose object name differs, so the two keys do not collide unless a
// test wants them to. manifestBody is written at the two segment prefix
// «<wrapper>/<inner>»; an empty string writes no manifest there at all.
func mkNoManifestTree(t *testing.T, manifestBody string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkBSLFile(t, root, "Documents/Основной/Ext/ObjectModule.bsl", "// корень\n")
	mkBSLFile(t, root,
		noManifestWrapper+"/"+noManifestInner+"/Documents/Вложенный/Ext/ObjectModule.bsl",
		"// внутри обёртки\n")
	if manifestBody != "" {
		if err := os.WriteFile(
			filepath.Join(root, noManifestWrapper, noManifestInner, extManifestClassic),
			[]byte(manifestBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// buildNoManifestIndex builds a cold index over root and returns its sorted key
// slice. Sorted, because ModuleNames keeps one entry per FILE and its order is the
// walk's, which is not a property any assertion here is about.
func buildNoManifestIndex(t *testing.T, root string) (*Index, []string) {
	t.Helper()
	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("BuildError after the build finished: %v", err)
	}
	names := slices.Clone(idx.ModuleNames())
	slices.Sort(names)
	return idx, names
}

// assertNoExtNamespace fails if any key carries the extension prefix. It is the
// assertion that does not depend on knowing which name a wrong implementation
// would have invented: a namespace minted from a directory, from a manifest that
// declares none, or from a name that was refused all land here.
func assertNoExtNamespace(t *testing.T, names []string) {
	t.Helper()
	for _, n := range names {
		if strings.HasPrefix(n, "ext.") {
			t.Errorf("the index emitted %q: no namespace may be minted where no manifest "+
				"declares one. Names: %v", n, names)
		}
	}
}

// TestWrapperWithoutAManifestStillMergesAndIsCounted is the ruling itself. The
// wrapper carries no manifest at any depth, its content merges into the base
// keyspace exactly as it did before the descent existed, and the collapse counter
// measures what that costs.
func TestWrapperWithoutAManifestStillMergesAndIsCounted(t *testing.T) {
	const collidingKey = "Документ.Ном.МодульОбъекта"
	if NFC(collidingKey) != collidingKey {
		t.Fatalf("test literal %q is not NFC, so it can never match an index key", collidingKey)
	}

	// A collision on purpose: the same object name on both sides of the wrapper.
	// index.go:anchorIndex strips the wrapper segments to reach the dump shaped
	// tail, so both files derive the SAME key and one of them loses its content.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, extManifestClassic),
		[]byte(baseConfigManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	mkBSLFile(t, root, "Documents/Ном/Ext/ObjectModule.bsl", "// корень\n")
	mkBSLFile(t, root, noManifestWrapper+"/Documents/Ном/Ext/ObjectModule.bsl",
		"// обёртка\n")

	idx, names := buildNoManifestIndex(t, root)

	// The keys did not move. Two files, one key, kept with multiplicity by
	// ModuleNames, which is what lets the counter below be checked at all.
	want := []string{collidingKey, collidingKey}
	if !slices.Equal(names, want) {
		t.Errorf("module keys = %v, want %v: a wrapper with no manifest must merge into "+
			"the base namespace, not acquire one of its own", names, want)
	}
	assertNoExtNamespace(t, names)

	// The layout has nothing to say, and nothing is the correct answer: a doubt
	// invented here would tell an operator to act where there is nothing to act on.
	summary := idx.ExtensionLayout()
	if summary.Extensions != 0 {
		t.Errorf("ExtensionLayout().Extensions = %d, want 0. Full summary: %+v",
			summary.Extensions, summary)
	}
	if !summary.Quiet() {
		t.Errorf("ExtensionLayout().Quiet() = false: a directory that declares nothing is "+
			"not a doubt, it is an answer. Full summary: %+v", summary)
	}

	// AND IT IS COUNTED. This is the half that makes the ruling defensible: the
	// merge is lossy, and the loss is reported as a number rather than left to be
	// discovered. Two files on one key is Files 1 and Keys 1 by the arithmetic
	// collapsedKeysOf states, total assignments minus distinct names.
	st := idx.CollapsedKeys()
	if st.Files <= 0 {
		t.Errorf("CollapsedKeys().Files = %d, want more than 0: the wrapper's module and "+
			"the configuration's module derive one key and one of them is no longer "+
			"servable. Full state: %+v", st.Files, st)
	}
	if st.Files != 1 || st.Keys != 1 {
		t.Errorf("CollapsedKeys() = %+v, want Files 1 and Keys 1 for two files on one key", st)
	}
	if !slices.Equal(st.Sample, []string{collidingKey}) {
		t.Errorf("CollapsedKeys().Sample = %v, want [%q]: the notice must name the key that "+
			"collapsed", st.Sample, collidingKey)
	}
}

// TestNonAdoptedManifestAtAStrippedPrefixGivesNoNamespace covers the manifest that
// IS there and declares no extension.
//
// manifestNotExtension has TWO inhabitants and they are not the same document, so
// both are built rather than one being taken as representative:
//
//	a  a closed <Properties> carrying no <ObjectBelonging>Adopted</ObjectBelonging>.
//	   This is the base configuration shape, and baseConfigManifest is exactly it:
//	   <Properties> opens, declares <Name>УправлениеТорговлей</Name>, and closes.
//	b  a COMPLETE document with no <Properties> element at all. classifyManifest
//	   reaches this through its own branch and never looks at ObjectBelonging.
//
// Reading manifestNotExtension as «a base configuration» would be wrong about b,
// and a fixture that built only a would never have shown the difference.
func TestNonAdoptedManifestAtAStrippedPrefixGivesNoNamespace(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"a closed Properties with no ObjectBelonging: the base configuration shape",
			baseConfigManifest()},
		{"a complete document with no Properties element at all",
			"\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
				"<MetaDataObject xmlns=\"http://v8.1c.ru/8.3/MDClasses\" version=\"2.20\">\n" +
				"\t<Configuration uuid=\"bbbb\"/>\n</MetaDataObject>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := mkNoManifestTree(t, tt.body)

			// The premise, read off the detector itself: this manifest answered, and
			// the answer was «not an extension». Without it a green below could be
			// reached by a manifest that was never read at all.
			prefix := noManifestWrapper + "/" + noManifestInner
			verdict, name, reason := manifestVerdictOf(
				filepath.Join(root, noManifestWrapper, noManifestInner), &extensionScanCost{})
			if verdict != manifestNotExtension {
				t.Errorf("manifestVerdictOf(%q) = verdict %d, name %q, reason %d; want "+
					"manifestNotExtension (%d)", prefix, verdict, name, reason, manifestNotExtension)
			}

			idx, names := buildNoManifestIndex(t, root)

			want := []string{noManifestInnerKey, noManifestRootKey}
			slices.Sort(want)
			if !slices.Equal(names, want) {
				t.Errorf("module keys = %v, want %v: a manifest that declares no extension "+
					"leaves the keys where they were", names, want)
			}
			assertNoExtNamespace(t, names)

			summary := idx.ExtensionLayout()
			if summary.Extensions != 0 {
				t.Errorf("ExtensionLayout().Extensions = %d, want 0. Full summary: %+v",
					summary.Extensions, summary)
			}
			// A manifest that ANSWERED is not a doubt. Counting one here would send an
			// operator after a tree that is behaving exactly as intended.
			if summary.Undecided() != 0 {
				t.Errorf("ExtensionLayout().Undecided() = %d, want 0: the manifest was read "+
					"to its end and declared none. Full summary: %+v", summary.Undecided(), summary)
			}
			if !summary.Quiet() {
				t.Errorf("ExtensionLayout().Quiet() = false. Full summary: %+v", summary)
			}
			if st := idx.CollapsedKeys(); st.Files != 0 {
				t.Errorf("CollapsedKeys() = %+v, want Files 0: the two objects are named "+
					"differently and nothing collides here", st)
			}
		})
	}
}

// TestUndecidedManifestAtAStrippedPrefixLeavesKeysAndReportsADoubt is the third
// answer, and the one where silence would be the failure.
//
// The manifest is a real extension manifest: <ObjectBelonging>Adopted</...> is
// present and the descent reaches it. What it declares is a name that cannot
// become part of a key, «Плохое.Имя», and the reason is structural rather than
// aesthetic: the key is dot separated, so accepting that name would move every
// component after it one slot along. validExtensionName refuses it, and the
// contract at the top of extlayout.go then requires two things at once, because
// either alone is a defect: the keys stay where they were, AND the refusal is
// reported. A layout that quietly declined would leave an operator with an
// extension that is simply missing and no way to find out why.
func TestUndecidedManifestAtAStrippedPrefixLeavesKeysAndReportsADoubt(t *testing.T) {
	const rejectedName = "Плохое.Имя"
	if validExtensionName(rejectedName) {
		t.Fatalf("validExtensionName(%q) = true: this fixture needs a name the package "+
			"refuses, and this one is no longer refused", rejectedName)
	}

	root := mkNoManifestTree(t, classicExtensionManifest(rejectedName, true))
	prefix := noManifestWrapper + "/" + noManifestInner

	// PREMISE. The descent reached the manifest and could not decide, for the one
	// reason this fixture is built around.
	layout := detectExtensionLayout(root)
	foundDoubt := false
	for _, d := range layout.doubts {
		if d.dir == prefix && d.reason == doubtNameRejected {
			foundDoubt = true
		}
	}
	if !foundDoubt {
		t.Errorf("doubts = %+v, want one at %q with doubtNameRejected: the descent must "+
			"record WHERE it could not decide", layout.doubts, prefix)
	}
	if len(layout.byPrefix) != 0 {
		t.Errorf("byPrefix = %v, want empty: undecided never produces a namespace",
			layout.byPrefix)
	}

	idx, names := buildNoManifestIndex(t, root)

	// The keys are exactly the ones a tree with no manifest at all would produce.
	want := []string{noManifestInnerKey, noManifestRootKey}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("module keys = %v, want %v: a refused name leaves the keys that shipped "+
			"before any of this existed", names, want)
	}
	assertNoExtNamespace(t, names)
	for _, n := range names {
		if strings.Contains(n, rejectedName) {
			t.Errorf("the index emitted %q, carrying the refused name: refusing a name and "+
				"then keying with it is worse than either", n)
		}
	}

	// AND THE DOUBT IS REPORTED. Silence is the failure mode this half exists for.
	summary := idx.ExtensionLayout()
	if summary.NameRejected != 1 {
		t.Errorf("ExtensionLayout().NameRejected = %d, want 1. Full summary: %+v",
			summary.NameRejected, summary)
	}
	if summary.Undecided() != 1 {
		t.Errorf("ExtensionLayout().Undecided() = %d, want 1. Full summary: %+v",
			summary.Undecided(), summary)
	}
	if summary.Quiet() {
		t.Errorf("ExtensionLayout().Quiet() = true: an extension was found, refused and "+
			"then not mentioned. Full summary: %+v", summary)
	}
	if summary.Extensions != 0 {
		t.Errorf("ExtensionLayout().Extensions = %d, want 0: a refused name is not a "+
			"recognised extension. Full summary: %+v", summary.Extensions, summary)
	}
	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Errorf("CollapsedKeys() = %+v, want Files 0", st)
	}
}
