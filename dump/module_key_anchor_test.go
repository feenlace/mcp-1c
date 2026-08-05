package dump

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The anchor scan in bslPathToModuleName.
//
// WHAT IT IS FOR. A dump root pointed one level too high (a customer pointed
// --dump at the directory ABOVE the dump root) shifts every relative path by one
// or more segments. The derivation then reads the WRAPPER as the metadata kind
// and the first real directory as the object name, so thousands of files collapse
// onto a handful of keys. Measured on the customer-shaped corpus of 13575 files
// wrapped in "Documents/dumps/": 2736 distinct keys, worst bucket 3396. Because
// loadBSLFiles writes plain maps (contentByName[m.name], pathByName[m.name]), the
// second file under a key silently overwrites the first: content is genuinely
// lost while ModuleCount() still reports 13575.
//
// The scan finds the first segment that both LOOKS like a dump root and carries a
// dump-shaped path below it, and derives the key from there.
//
// WHY A SHAPE CHECK AND NOT A MARKER TEST. 13571 of the 13575 real paths contain a
// root-marker name at some index > 0 — the inner "Ext" directory every object
// carries. A rule that anchored on the first marker alone would re-key almost the
// whole corpus. The shape check is the entire reason the scan is a no-op at a
// correct root: measured on the same corpus, anchorIndex == 0 on all 13575 paths.
// ---------------------------------------------------------------------------

// wrapperPrefixes are the extra path segments a mis-pointed --dump prepends to
// every relative path. They are chosen to be adversarial rather than convenient:
// "Documents/dumps/" and "Catalogs/Спр/" open with a REAL metadata kind, and
// "Ext/" is the name of the configuration-module root, so each of them offers the
// scan a marker at index 0 that it must reject on shape.
var wrapperPrefixes = []string{
	"dump_bsl/",
	"Documents/dumps/",
	"Ext/",
	"a/b/c/d/e/",
	"Catalogs/Спр/",
	"main/",
	"ext/",
}

// anchorCorpusRootOnly and anchorCorpusUnknownKind are the two keyDigestCorpus
// entries the recovery property cannot cover. They are named, and each carries a
// control below proving it really is outside the property, rather than being
// quietly skipped.
//
//   - anchorCorpusRootOnly: a .bsl sitting directly in the dump root has no kind
//     directory at all, so nothing below a wrapper can be recognised as a dump
//     shape. No 1C dump lays a module out that way — the configuration's own
//     modules live one level down, in the root Ext directory.
//
//   - anchorCorpusUnknownKind: "Styles" is the corpus's unknown-category row and
//     is deliberately NOT a dumpDirNames key, so it is not a root marker and no
//     anchor exists. This is a real and inherent limit: the scan recognises a
//     root only through the tables the package already has, so a wrapped dump
//     whose top-level directory is a kind the package does not know stays
//     un-anchored. The remedy is the same one Guard 1 in module_key_guard_test.go
//     already forces from measured ground truth — add the kind to
//     metadata_types.go — and it fixes both problems at once, because such a
//     directory yields raw-English-prefix keys even at a CORRECT root.
const (
	anchorCorpusRootOnly    = "Module.bsl"
	anchorCorpusUnknownKind = "Styles/Основной/Ext/Module.bsl"
)

// TestAnchorScanIsANoOpAtACorrectRoot pins the property the whole change rests
// on: at a correctly pointed root the scan must not move a single key.
//
// It asserts the MECHANISM (anchorIndex == 0), not merely the outcome, because
// the outcome is also produced by a scan that anchors somewhere else and happens
// to rebuild the same string. Only index 0 means "the existing body ran on the
// untouched parts slice".
func TestAnchorScanIsANoOpAtACorrectRoot(t *testing.T) {
	for _, p := range keyDigestCorpus {
		if i := anchorIndex(strings.Split(p, "/")); i != 0 {
			t.Errorf("anchorIndex(%q) = %d, want 0: the scan must not fire at a correct root",
				p, i)
		}
	}

	// Positive control: the scan MUST fire on a wrapped path, otherwise the zeros
	// above are the zeros of a function that never returns anything else.
	const wrapped = "Documents/dumps/Catalogs/Номенклатура/Ext/ObjectModule.bsl"
	if i := anchorIndex(strings.Split(wrapped, "/")); i != 2 {
		t.Fatalf("positive control failed: anchorIndex(%q) = %d, want 2. The scan never "+
			"fires, so the zeros asserted above prove nothing", wrapped, i)
	}
}

// TestAnchorScanNeverLeavesFewerThanTwoSegments pins the invariant that lets
// bslPathToModuleName keep its `len(parts) < 2` early return AFTER the scan
// without that return ever seeing a sliced value: every shape the scan accepts
// is at least two segments long, so a non-zero anchor cannot produce a slice the
// early return would take, and the `relPath` it returns is therefore always the
// whole path it was given.
//
// The shortest accepted shape is a configuration-module root, "Ext/<one of the
// four files>", which is exactly two.
func TestAnchorScanNeverLeavesFewerThanTwoSegments(t *testing.T) {
	shortest := -1
	for _, p := range keyDigestCorpus {
		for _, prefix := range wrapperPrefixes {
			parts := strings.Split(prefix+p, "/")
			i := anchorIndex(parts)
			if i == 0 {
				continue
			}
			if n := len(parts) - i; shortest == -1 || n < shortest {
				shortest = n
			}
		}
	}
	if shortest == -1 {
		t.Fatalf("no wrapped corpus path anchored at all; the loop measured nothing")
	}
	if shortest < 2 {
		t.Errorf("the scan left %d segment(s) on some wrapped corpus path; "+
			"bslPathToModuleName's len(parts) < 2 early return would then answer "+
			"with the WHOLE wrapped path instead of the sliced one", shortest)
	}
	if shortest != 2 {
		t.Logf("shortest anchored remainder over the wrapped corpus: %d segments", shortest)
	}
}

// TestAnchorScanRecoversEveryWrapperShape is the fix itself: for every corpus
// path and every wrapper, the wrapped path must derive the SAME key as the
// unwrapped one.
//
// The comparison is against the live deriver rather than against a literal table
// of expected keys on purpose: the claim is "the wrapper makes no difference",
// and a literal table would restate the whole of keyDigestCorpus and could drift
// from it.
func TestAnchorScanRecoversEveryWrapperShape(t *testing.T) {
	excluded := map[string]bool{
		anchorCorpusRootOnly:    false,
		anchorCorpusUnknownKind: false,
	}
	covered := 0
	for _, p := range keyDigestCorpus {
		if _, isExcluded := excluded[p]; isExcluded {
			excluded[p] = true
			continue
		}
		want := bslPathToModuleName(p)
		for _, prefix := range wrapperPrefixes {
			if got := bslPathToModuleName(prefix + p); got != want {
				t.Errorf("wrapper %q: bslPathToModuleName(%q) = %q, want %q",
					prefix, prefix+p, got, want)
			}
		}
		covered++
	}
	for p, seen := range excluded {
		if !seen {
			t.Fatalf("corpus no longer contains %q; the exclusion carved out above is "+
				"unexercised and the loop may be skipping rows silently", p)
		}
	}
	if covered != len(keyDigestCorpus)-len(excluded) {
		t.Fatalf("covered %d of %d corpus rows, want %d: the loop is skipping rows "+
			"nobody declared", covered, len(keyDigestCorpus), len(keyDigestCorpus)-len(excluded))
	}

	// Negative controls: each excluded row really is outside the property, so the
	// loop is not skipping a row that would have passed anyway.
	for p := range excluded {
		if got, want := bslPathToModuleName("Ext/"+p), bslPathToModuleName(p); got == want {
			t.Errorf("the %q exclusion is unnecessary: wrapping it recovered %q. "+
				"Remove the exclusion rather than leaving a row untested", p, want)
		}
	}

	// And the MECHANISM behind the unknown-kind exclusion, so the row above is
	// explained rather than merely observed: an unknown top-level directory is not
	// a root marker, so no anchor can exist below a wrapper.
	if dumpRootMarker("Styles") {
		t.Errorf("dumpRootMarker(\"Styles\") = true; the unknown-kind exclusion is " +
			"documented against a table state that no longer holds")
	}
	if !dumpRootMarker("Catalogs") {
		t.Fatalf("positive control failed: dumpRootMarker(\"Catalogs\") = false, so the " +
			"assertion above is the assertion of a predicate that is false for everything")
	}
}

// TestAnchorScanLeavesUnanchorablePathsAlone pins the fallback. A path with no
// segment that both marks a root and carries a dump shape below it must derive
// EXACTLY the key it derived before the scan existed, character for character.
// These are the literal keys the shipped binary produces.
func TestAnchorScanLeavesUnanchorablePathsAlone(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		// "Styles" is not a dumpDirNames key, so the raw-English-prefix fallback
		// applies. The inner "Ext" IS a root marker, so this row also proves the
		// shape check rejects it: "Module.bsl" is not one of the four
		// configModuleNames files, so Ext/Module.bsl is not a configuration-module
		// root.
		{"unknown kind keeps the English prefix",
			"Styles/Основной/Ext/Module.bsl", "Styles.Основной.МодульФормы"},
		// Nothing here marks a root at all.
		{"no marker anywhere", "foo/bar/baz.bsl", "foo.bar.baz"},
		// A wrapper over a path that is itself not dump-shaped: the scan finds no
		// anchor and the wrapped key survives unchanged, which is the honest answer
		// rather than a guess.
		{"wrapper over a non-dump path", "dump_bsl/foo/bar/baz.bsl", "dump_bsl.foo.baz"},
		// The documented no-directory early return, before any key is built.
		{"root-only file returns the path verbatim", "Module.bsl", "Module.bsl"},
		// "Расширения" with too little below it: shapeOK needs at least four
		// segments for the extension form, so this keeps the pre-existing fallback
		// the extension branch already had (len(parts) < 4).
		{"too short under Расширения falls back",
			"Расширения/TestExt/Module.bsl", "Расширения.TestExt.МодульФормы"},
		// A CommonForm that merely HAPPENS to be named "Расширения" must stay a
		// common form. This row exists because an earlier candidate rule keyed on
		// the directory name alone and fabricated an "ext.Ext." prefix out of
		// exactly this path.
		{"a CommonForm named Расширения is not an extension container",
			"CommonForms/Расширения/Ext/Form/Module.bsl", "ОбщаяФорма.Расширения.МодульФормы"},
		// "Расширения" at index 0 IS the extension container and keeps its meaning:
		// wrapping is not what this is, and the key must stay namespaced.
		{"Расширения at index 0 stays an extension container",
			"Расширения/X/Catalogs/Ном/Ext/ObjectModule.bsl",
			"ext.X.Справочник.Ном.МодульОбъекта"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bslPathToModuleName(tt.path); got != tt.want {
				t.Errorf("bslPathToModuleName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestAnchorScanAcceptedAndResidualBehaviours pins the three keys the change
// deliberately MOVES. They are pinned as tests rather than described in a comment
// so that a later edit to any of them is visible instead of silent.
func TestAnchorScanAcceptedAndResidualBehaviours(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		before string
		after  string
	}{
		// ACCEPTED. An extension subtree whose container is NOT named "Расширения"
		// merges into the base configuration and can collide with a same-named
		// configuration module. Measured on the 13575-path corpus wrapped in
		// "ext/Имя/": that tree alone goes from 2736 distinct keys / worst bucket
		// 3396 to 13575 distinct / worst bucket 1. Put a base configuration and
		// such a tree in one keyspace and the collision the merge accepts shows up
		// instead: 27150 paths give 13575 distinct / worst bucket 2, where before
		// they gave 16311 distinct / worst bucket 3396. Accepted deliberately with
		// that trade in view. The alternative — synthesising an extension namespace
		// from whatever directory name sits above the kind — was rejected: it
		// fabricates an "ext.<name>." prefix out of any directory that merely looks
		// the part.
		{"extension tree not named Расширения merges into base config",
			"ext/Доработки/Catalogs/Ном/Ext/ObjectModule.bsl",
			"ext.Доработки.МодульОбъекта",
			"Справочник.Ном.МодульОбъекта"},

		// RESIDUAL (a). A .bsl named after one of the four configModuleNames files
		// but sitting inside a NON-root "Ext" directory anchors on that inner "Ext"
		// and is read as a configuration module. 1C does not emit this shape (an
		// object's Ext holds ObjectModule/ManagerModule/... never
		// ManagedApplicationModule) and it does not occur in the 13575 real paths.
		{"residual: a configModuleNames file inside a non-root Ext",
			"Catalogs/Ном/Ext/ManagedApplicationModule.bsl",
			"Справочник.Ном.ManagedApplicationModule",
			"Конфигурация.МодульУправляемогоПриложения.Модуль"},

		// RESIDUAL (b). A directory named "Расширения" that is NOT the extension
		// container, sitting below a kind, with a full dump shape starting two
		// segments below it, anchors there and fabricates an "ext.<next segment>."
		// prefix. Also absent from the 13575 real paths, and not a shape 1C emits:
		// it needs an object literally named "Расширения" whose own subtree is a
		// second dump.
		{"residual: a non-container directory named Расширения",
			"Catalogs/Расширения/Y/Catalogs/Ном/Ext/ObjectModule.bsl",
			"Справочник.Расширения.МодульОбъекта",
			"ext.Y.Справочник.Ном.МодульОбъекта"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bslPathToModuleName(tt.path)
			if got == tt.before {
				t.Fatalf("bslPathToModuleName(%q) still returns the pre-change key %q. "+
					"The row documents a key this change MOVES; if the move was reverted, "+
					"delete the row rather than leaving it green", tt.path, tt.before)
			}
			if got != tt.after {
				t.Errorf("bslPathToModuleName(%q) = %q, want %q (was %q before the anchor scan)",
					tt.path, got, tt.after, tt.before)
			}
		})
	}
}

// TestConfigModuleNamesDisjointFromModuleSuffixes pins the invariant the shape
// check leans on. kindOK requires the last segment to be a moduleNameSuffixes
// key for an object subtree, and requires it to be a configModuleNames key for a
// configuration-module root. Those two rules must never both accept the same file
// name, or "Documents/dumps/Ext/SessionModule.bsl" would anchor at 0 (reading
// "dumps" as the object) instead of at the real "Ext".
func TestConfigModuleNamesDisjointFromModuleSuffixes(t *testing.T) {
	for name := range configModuleNames {
		if _, both := moduleNameSuffixes[name]; both {
			t.Errorf("%q is in BOTH configModuleNames and moduleNameSuffixes; the anchor "+
				"shape check cannot tell a configuration-module root from an object subtree",
				name)
		}
	}

	// Positive control: the same comparison must find an overlap when one exists.
	probe := make(map[string]string, len(moduleNameSuffixes)+1)
	for k, v := range moduleNameSuffixes {
		probe[k] = v
	}
	probe["SessionModule.bsl"] = "planted"
	overlap := 0
	for name := range configModuleNames {
		if _, both := probe[name]; both {
			overlap++
		}
	}
	if overlap != 1 {
		t.Fatalf("positive control failed: with an overlap planted the check found %d, want 1. "+
			"The green verdict above says nothing", overlap)
	}
}
