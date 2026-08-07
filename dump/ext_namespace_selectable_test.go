package dump

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// SELECTING THE EXTENSION NAMESPACE.
//
// splitModuleKey derives the REAL category of an extension module, which is what
// lets a caller filtering by «Справочник» reach an extension at all. That fix is
// right and nothing here weakens it. What it left behind is the other half of the
// question: having stopped putting the literal "ext" in the CATEGORY slot, nothing
// put it anywhere else, so «every module that belongs to an extension» became a
// set no caller could name. The namespace was addressable only as long as it was
// occupying a slot that belonged to something else.
//
// The namespace is a SEPARATE slot from the category, which is what splitModuleKey's
// own comment already says about the grammar of a key. These tests hold both slots
// open at once: the category still selects across both namespaces, the namespace
// selects across every category, and the two intersect.

// extAndBaseDump builds a dump root holding BOTH namespaces: two extension
// subtrees in the -AllExtensions shape and a base configuration tree beside them.
// It returns the root.
//
// Both namespaces in ONE index is the point. A fixture holding only extensions
// cannot tell «the namespace filter selected the extensions» apart from «the
// filter did nothing and everything came back».
func extAndBaseDump(t *testing.T, matchLines int) string {
	t.Helper()
	root := t.TempDir()

	body := func(marker string) string {
		var b strings.Builder
		b.WriteString("Процедура " + marker + "()\n")
		for i := 0; i < matchLines; i++ {
			fmt.Fprintf(&b, "\tСообщить(\"игольчатыймаркер %d\");\n", i)
		}
		b.WriteString("КонецПроцедуры\n")
		return b.String()
	}
	write := func(rel, marker string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		mkdirAllOrFatal(t, filepath.Dir(p))
		writeFileOrFatal(t, p, body(marker))
	}

	// Two extensions, each declaring its own name in its own manifest.
	mkExtensionDump(t, filepath.Join(root, "dirA"), extManifestClassic, "РасширениеА")
	mkExtensionDump(t, filepath.Join(root, "dirB"), extManifestClassic, "РасширениеБ")
	write("dirA/CommonModules/ОбщийА/Ext/Module.bsl", "ОбщийА")
	write("dirA/Catalogs/НоменклатураА/Ext/ObjectModule.bsl", "СправочникА")
	write("dirB/CommonModules/ОбщийБ/Ext/Module.bsl", "ОбщийБ")

	// The base configuration, in the same root, outside both extension subtrees.
	write("CommonModules/ОбщийБаз/Ext/Module.bsl", "ОбщийБаз")
	write("Catalogs/НоменклатураБаз/Ext/ObjectModule.bsl", "СправочникБаз")

	return root
}

const (
	extKeyCommonA  = "ext.РасширениеА.ОбщийМодуль.ОбщийА.Модуль"
	extKeyCatalogA = "ext.РасширениеА.Справочник.НоменклатураА.МодульОбъекта"
	extKeyCommonB  = "ext.РасширениеБ.ОбщийМодуль.ОбщийБ.Модуль"
	baseKeyCommon  = "ОбщийМодуль.ОбщийБаз.Модуль"
	baseKeyCatalog = "Справочник.НоменклатураБаз.МодульОбъекта"
)

// TestTheFixtureHoldsBothNamespaces is the premise every test below rests on,
// checked rather than assumed. If the deriver stops producing these keys the
// tests that follow would still pass by selecting nothing from nothing.
func TestTheFixtureHoldsBothNamespaces(t *testing.T) {
	idx := openBothNamespaces(t, 3)
	names := idx.ModuleNames()
	for _, want := range []string{extKeyCommonA, extKeyCatalogA, extKeyCommonB, baseKeyCommon, baseKeyCatalog} {
		if !slices.Contains(names, want) {
			t.Fatalf("premise moved: %q is not in the index. Names: %v", want, names)
		}
	}
	if len(names) != 5 {
		t.Fatalf("premise moved: the fixture holds %d modules, want 5: %v", len(names), names)
	}
}

func openBothNamespaces(t *testing.T, matchLines int) *Index {
	t.Helper()
	root := extAndBaseDump(t, matchLines)
	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	<-idx.Done()
	return idx
}

// modulesOf is the SET of modules a result names, sorted so two sets can be
// compared. Order is deliberately not what these tests are about: which modules a
// filter selects is a different question from what order they come back in, and
// the ordering guarantee is pinned separately by the determinism tests.
func modulesOf(matches []Match) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.Module)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// sortedSet normalises an expected set the same way modulesOf normalises an
// observed one, so a test states which modules it wants without also having to
// state the collation order of Cyrillic against Latin.
func sortedSet(keys ...string) []string {
	out := slices.Clone(keys)
	slices.Sort(out)
	return slices.Compact(out)
}

// TestTheExtensionNamespaceIsSelectableInEverySearchMode is the restored
// capability, and it is checked in every mode because the modes resolve the
// filter through two INDEPENDENT mechanisms: smart goes through a bleve term
// query over an indexed field, regex and exact go through PathIndex. A fix that
// reached only one of them would answer the same question two different ways.
func TestTheExtensionNamespaceIsSelectableInEverySearchMode(t *testing.T) {
	idx := openBothNamespaces(t, 3)

	wantExt := sortedSet(extKeyCatalogA, extKeyCommonA, extKeyCommonB)
	for _, mode := range []SearchMode{SearchModeSmart, SearchModeExact, SearchModeRegex} {
		t.Run(string(mode), func(t *testing.T) {
			got, _, err := idx.SearchWithStats(SearchParams{
				Query:     "игольчатыймаркер",
				Namespace: extKeyNamespace,
				Mode:      mode,
				Limit:     500,
			})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			mods := modulesOf(got)
			if !slices.Equal(mods, wantExt) {
				t.Errorf("Namespace=%q selected %v, want exactly the extension modules %v",
					extKeyNamespace, mods, wantExt)
			}

			// Negative control on the SAME query and mode: with no namespace filter
			// the base modules come back too, so the assertion above is measuring the
			// filter rather than a fixture that holds only extensions.
			all, _, err := idx.SearchWithStats(SearchParams{
				Query: "игольчатыймаркер",
				Mode:  mode,
				Limit: 500,
			})
			if err != nil {
				t.Fatalf("unfiltered search: %v", err)
			}
			allMods := modulesOf(all)
			if len(allMods) != 5 {
				t.Fatalf("control failed: the unfiltered search returned %v, want all 5 modules", allMods)
			}
		})
	}
}

// TestTheNamespaceAndTheCategorySelectIndependentlyAndTogether. The category
// filter must keep reaching across both namespaces — that is the v1.14.0 fix and
// it is not being traded away — while the namespace narrows within it.
func TestTheNamespaceAndTheCategorySelectIndependentlyAndTogether(t *testing.T) {
	idx := openBothNamespaces(t, 3)

	cases := []struct {
		name      string
		namespace string
		category  string
		want      []string
	}{
		{
			name:     "category alone reaches into both namespaces",
			category: "ОбщийМодуль",
			want:     sortedSet(baseKeyCommon, extKeyCommonA, extKeyCommonB),
		},
		{
			name:      "namespace alone reaches across every category",
			namespace: extKeyNamespace,
			want:      sortedSet(extKeyCatalogA, extKeyCommonA, extKeyCommonB),
		},
		{
			name:      "the two intersect",
			namespace: extKeyNamespace,
			category:  "ОбщийМодуль",
			want:      sortedSet(extKeyCommonA, extKeyCommonB),
		},
		{
			name:      "a namespace nothing carries selects nothing",
			namespace: "нетакогопространства",
			want:      nil,
		},
	}

	for _, mode := range []SearchMode{SearchModeSmart, SearchModeExact} {
		for _, tc := range cases {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				got, _, err := idx.SearchWithStats(SearchParams{
					Query:     "игольчатыймаркер",
					Namespace: tc.namespace,
					Category:  tc.category,
					Mode:      mode,
					Limit:     500,
				})
				if err != nil {
					t.Fatalf("search: %v", err)
				}
				mods := modulesOf(got)
				if len(tc.want) == 0 {
					if len(mods) != 0 {
						t.Errorf("selected %v, want nothing", mods)
					}
					return
				}
				if !slices.Equal(mods, tc.want) {
					t.Errorf("selected %v, want %v", mods, tc.want)
				}
			})
		}
	}
}

// TestTheNamespaceSlotComesFromTheOneSplitter. The namespace must be derived
// where every other slot is derived. A second private copy of the "is this
// namespaced" rule is exactly the drift splitModuleKey was written to end.
func TestTheNamespaceSlotComesFromTheOneSplitter(t *testing.T) {
	tests := []struct {
		key       string
		namespace string
		category  string
	}{
		{"Справочник.Ном.МодульОбъекта", "", "Справочник"},
		{"ext.МоёРасш.Справочник.Ном.МодульОбъекта", extKeyNamespace, "Справочник"},
		{"ext.МоёРасш.ОбщийМодуль.Общий.Модуль", extKeyNamespace, "ОбщийМодуль"},
		{"ext.МоёРасш.Конфигурация.МодульСеанса.Модуль", extKeyNamespace, "Конфигурация"},
		// NOT namespaced: «Форма» is a form infix, not a metadata kind, so this key
		// is a base-configuration key that merely begins with a directory named ext.
		{"ext.Ном.Форма.Ф.МодульФормы", "", "ext"},
		// NOT namespaced: «Справочники» is not a category this package emits.
		{"ext.Расш.Справочники.Объект.МодульОбъекта", "", "ext"},
		{"Двасегмента.Ключ", "", "Двасегмента"},
		{"Односегментный", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			ns, cat, _, _ := splitModuleKey(tt.key)
			if ns != tt.namespace || cat != tt.category {
				t.Errorf("splitModuleKey(%q) namespace=%q category=%q, want %q / %q",
					tt.key, ns, cat, tt.namespace, tt.category)
			}
			// The path index splits a docID independently, in bulk and one at a
			// time. All three must agree or the same filter answers differently
			// depending on how the entry was built.
			bulk := NewPathIndex([]string{tt.key}).entries[0]
			one := NewPathIndex(nil)
			one.AddEntry(tt.key)
			if bulk.Namespace != tt.namespace {
				t.Errorf("NewPathIndex namespace = %q, want %q", bulk.Namespace, tt.namespace)
			}
			if bulk != one.entries[0] {
				t.Errorf("NewPathIndex and AddEntry disagree about %q: %+v vs %+v",
					tt.key, bulk, one.entries[0])
			}
		})
	}
}

// TestEveryNamespacedKeyCarriesTheNamespaceForEveryKnownCategory ties the
// namespace slot to the derived category table, the same way the category slot is
// tied to it. A kind added to metadata_types.go must not silently stop being
// namespaced.
func TestEveryNamespacedKeyCarriesTheNamespaceForEveryKnownCategory(t *testing.T) {
	checked := 0
	for _, ru := range dumpDirNames {
		checked++
		key := "ext.Расш." + ru + ".Объект.МодульОбъекта"
		ns, cat, _, _ := splitModuleKey(key)
		if ns != extKeyNamespace || cat != ru {
			t.Errorf("splitModuleKey(%q) = namespace %q / category %q, want %q / %q",
				key, ns, cat, extKeyNamespace, ru)
		}
	}
	if checked == 0 {
		t.Fatal("dumpDirNames is empty, so the loop measured nothing")
	}
}

// TestTheNamespaceLegCountsEveryMatchingLineNotJustTheShownOnes.
//
// The line-by-line legs report a total over EVERY candidate they scanned; the
// limit caps the matches carried back and nothing else. That is what makes the
// count on a large extension namespace exact rather than a floor, and it is a
// property of the scan that a namespace filter must not quietly change.
func TestTheNamespaceLegCountsEveryMatchingLineNotJustTheShownOnes(t *testing.T) {
	const linesPerModule = 30
	const limit = 4
	idx := openBothNamespaces(t, linesPerModule)

	// Three extension modules, each carrying linesPerModule matching lines.
	const wantTotal = 3 * linesPerModule

	for _, mode := range []SearchMode{SearchModeExact, SearchModeRegex} {
		t.Run(string(mode), func(t *testing.T) {
			matches, stats, err := idx.SearchWithStats(SearchParams{
				Query:     "игольчатыймаркер",
				Namespace: extKeyNamespace,
				Mode:      mode,
				Limit:     limit,
			})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(matches) != limit {
				t.Errorf("returned %d matches, want the limit %d", len(matches), limit)
			}
			if stats.Total != wantTotal {
				t.Errorf("Total = %d, want %d: the count must be the exact number of matching "+
					"lines in the namespace, not a floor capped by the limit", stats.Total, wantTotal)
			}
			if stats.Unit != SearchUnitLines {
				t.Errorf("Unit = %q, want %q", stats.Unit, SearchUnitLines)
			}
		})
	}
}

// TestTheIndexSchemaVersionMovedWithTheDocumentShape.
//
// The namespace is an INDEXED field, so a generation built by a binary that did
// not know about it holds shards in which no document carries one. Served by this
// binary the namespace filter would then select nothing and report it as an empty
// result — a silent wrong answer, which is precisely the state the BUMP PROTOCOL
// exists to prevent. The gensig folds the schema version in, so bumping it is what
// forces the rebuild.
func TestTheIndexSchemaVersionMovedWithTheDocumentShape(t *testing.T) {
	const schemaVersionBeforeTheNamespaceField = 4
	if dumpIndexSchemaVersion <= schemaVersionBeforeTheNamespaceField {
		t.Errorf("dumpIndexSchemaVersion = %d, want more than %d: bslDocument gained an "+
			"indexed field, so every warm generation built without it must be rebuilt "+
			"rather than served with a filter that silently matches nothing",
			dumpIndexSchemaVersion, schemaVersionBeforeTheNamespaceField)
	}
	// The gensig must actually move with it, or the bump is a number nobody reads.
	dir := t.TempDir()
	writeFileOrFatal(t, filepath.Join(dir, "M.bsl"), "Процедура П() КонецПроцедуры\n")
	cur, err := genSig(dir, dumpIndexSchemaVersion, zapSegmentVersion)
	if err != nil {
		t.Fatalf("genSig: %v", err)
	}
	prev, err := genSig(dir, schemaVersionBeforeTheNamespaceField, zapSegmentVersion)
	if err != nil {
		t.Fatalf("genSig: %v", err)
	}
	if cur == prev {
		t.Errorf("genSig is identical (%q) across the schema bump, so a generation built "+
			"before the namespace field would be adopted as current", cur)
	}
}
