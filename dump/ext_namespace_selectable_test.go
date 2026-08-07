package dump

import (
	"crypto/sha256"
	"encoding/hex"
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

// TestTheNamespaceSlotComesFromTheOneSplitter WAS HERE and is now
// TestNamespaceMembershipIsThePrefixAndCategoryDerivationIsSeparate, at the bottom
// of this file. It is a replacement rather than a deletion: every row it held is in
// the new table in the same relative order, with two rows inserted among them, and
// the property it checked — that all the splitters agree — is checked there over
// THREE PathIndex entry points instead of two, the third being AddEntryWithMeta,
// which is the one the live .cfe ingest uses and the one no test here reached.
//
// TWO OF ITS ROWS ARE THERE WITH THE OPPOSITE EXPECTATION, deliberately, and that
// is the change: "ext.Ном.Форма.Ф.МодульФормы" and
// "ext.Расш.Справочники.Объект.МодульОбъекта" are in the namespace now. Their old
// expectation was written when one condition answered both «is this in the
// namespace» and «what is its category», and the consumer downstream never agreed
// with it about either row. The argument is at splitModuleKey.

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

// THE SECOND PRODUCER OF ext.* KEYS, AND THE ONE THE NAMESPACE FILTER MISSED.
//
// A key that begins with "ext." reaches this package from TWO producers, not one.
// The dump deriver mints "ext.<Расш>.<Категория>.<Объект>.<ТипМодуля>" — five
// segments, the shape every test above uses. The SECOND producer is a live .cfe
// ingest: it composes an id from the extension's name and the module's container
// path inside the .cfe, and the documented container path
// "MCP_HTTPService/93b52b96-uuid/text" loses its "/text" tail and becomes two
// segments, so the whole id is FOUR. Neither the count nor the third segment is
// anything this package chose; they are what the container holds.
//
// Both producers agree with each other about what the key MEANS — it belongs to an
// extension — and the consumer downstream tests exactly that, the "ext." prefix and
// nothing else. Membership of the namespace is therefore a property of the prefix.
// It is the CATEGORY that cannot be derived from a four-segment id, and deriving a
// category is a separate question from being in the namespace.
const liveIngestKey = "ext.ЖивоеРасширение.MCP_HTTPService.93b52b96-uuid"

// hasExtPrefix is the CONSUMER's predicate, restated here verbatim rather than
// approximated. It is a bare prefix test, and this test's whole point is that the
// namespace filter selects neither more nor less than it does.
func hasExtPrefix(moduleID string) bool {
	return strings.HasPrefix(moduleID, extKeyNamespace+".")
}

// TestNamespaceMembershipIsThePrefixAndCategoryDerivationIsSeparate.
//
// Two rules, deliberately pulled apart. MEMBERSHIP of the namespace is the "ext."
// prefix. DERIVING the real category needs five segments with a category this
// package emits sitting after the extension name, and that condition keeps doing
// only that — a key it rejects keeps the category it had before, it does not lose
// the namespace as well.
func TestNamespaceMembershipIsThePrefixAndCategoryDerivationIsSeparate(t *testing.T) {
	tests := []struct {
		key       string
		namespace string
		category  string
		why       string
	}{
		{"Справочник.Ном.МодульОбъекта", "", "Справочник", "no prefix, no namespace"},
		{"ext.МоёРасш.Справочник.Ном.МодульОбъекта", extKeyNamespace, "Справочник", "dump deriver, category derived"},
		{"ext.МоёРасш.ОбщийМодуль.Общий.Модуль", extKeyNamespace, "ОбщийМодуль", "dump deriver, category derived"},
		{"ext.МоёРасш.Конфигурация.МодульСеанса.Модуль", extKeyNamespace, "Конфигурация", "dump deriver, category derived"},
		// The live .cfe ingest. In the namespace on its prefix; four segments, so
		// no category can be derived and the slot keeps what the split gives it.
		{liveIngestKey, extKeyNamespace, "ext", "live .cfe ingest, four segments"},
		// «Форма» is a form infix, not a metadata kind, so no category is derived
		// and the category slot stays "ext". The namespace does not depend on that.
		{"ext.Ном.Форма.Ф.МодульФормы", extKeyNamespace, "ext", "no derivable category"},
		// «Справочники» is not a category this package emits — same story.
		{"ext.Расш.Справочники.Объект.МодульОбъекта", extKeyNamespace, "ext", "no derivable category"},
		// "ext" alone is not the prefix "ext.", and the consumer's predicate
		// rejects it, so this one must stay out of the namespace.
		{"ext", "", "", "bare token, not the prefix"},
		{"Двасегмента.Ключ", "", "Двасегмента", "no prefix"},
		{"Односегментный", "", "", "no prefix"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			// The premise: the expectation agrees with the consumer's predicate.
			// Written out so a row that drifts away from it fails here rather than
			// quietly redefining what this test is measuring.
			if want := hasExtPrefix(tt.key); want != (tt.namespace == extKeyNamespace) {
				t.Fatalf("row is self-inconsistent: hasExtPrefix(%q) = %v but the row wants namespace %q (%s)",
					tt.key, want, tt.namespace, tt.why)
			}
			ns, cat, _, _ := splitModuleKey(tt.key)
			if ns != tt.namespace || cat != tt.category {
				t.Errorf("splitModuleKey(%q) namespace=%q category=%q, want %q / %q (%s)",
					tt.key, ns, cat, tt.namespace, tt.category, tt.why)
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
			// AddEntryWithMeta is the third entry point, the one the live ingest
			// actually uses, and it takes the category from its caller. Its
			// NAMESPACE must still be the same.
			meta := NewPathIndex(nil)
			meta.AddEntryWithMeta(tt.key, "Расширение", "Расширение")
			if meta.entries[0].Namespace != tt.namespace {
				t.Errorf("AddEntryWithMeta namespace = %q, want %q", meta.entries[0].Namespace, tt.namespace)
			}
		})
	}
}

// TestTheNamespaceSelectsExactlyWhatThePrefixPredicateAccepts is the exit
// criterion, and it is stated as SET EQUALITY rather than as a count.
//
// The fixture holds both producers at once: three modules the dump deriver keyed,
// one the live .cfe ingest planted through the call provider.go makes. A namespace
// filter that reached only the first would still return three modules and still
// look like it worked, which is why the assertion compares against the consumer's
// own predicate applied to the whole index rather than against a literal.
//
// All three modes, because the filter resolves through two INDEPENDENT mechanisms:
// smart goes through a bleve term query over the indexed namespace field, regex and
// exact go through PathIndex.FilterDocIDsIn. A fix landing in one is a divergence.
func TestTheNamespaceSelectsExactlyWhatThePrefixPredicateAccepts(t *testing.T) {
	idx := openBothNamespaces(t, 3)

	// Planted exactly as internal/extensions/provider.go does it: the id from
	// buildExtensionID, the category the live literal «Расширение», the module the
	// extension's name. Neither of the last two is derived from the id.
	live := "Процедура Живая()\n\tСообщить(\"игольчатыймаркер 0\");\n\tСообщить(\"игольчатыймаркер 1\");\n\tСообщить(\"игольчатыймаркер 2\");\nКонецПроцедуры\n"
	if err := idx.IndexDocWithMeta(liveIngestKey, live, "Расширение", "ЖивоеРасширение"); err != nil {
		t.Fatalf("IndexDocWithMeta(%q): %v", liveIngestKey, err)
	}

	// The expected set is DERIVED from the consumer's predicate over every module
	// the index holds, so it cannot be a stale literal.
	var want []string
	for _, n := range idx.ModuleNames() {
		if hasExtPrefix(n) {
			want = append(want, n)
		}
	}
	want = sortedSet(want...)

	// FIXTURE CONTROL. Both producers are represented, and the live one is the
	// shape the dump deriver cannot mint. Without this the assertion could pass on
	// a fixture holding only five-segment keys.
	if len(want) != 4 {
		t.Fatalf("fixture control: the predicate accepts %d modules, want 4 (three derived, one live): %v", len(want), want)
	}
	if !slices.Contains(want, liveIngestKey) {
		t.Fatalf("fixture control: the live-ingested key %q never reached ModuleNames", liveIngestKey)
	}
	if n := len(strings.Split(liveIngestKey, ".")); n != 4 {
		t.Fatalf("fixture control: the live key has %d segments, want 4 — the shape the "+
			"five-segment condition cannot accept", n)
	}

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
			if !slices.Equal(mods, want) {
				t.Errorf("Namespace=%q selected %v,\nwant exactly what the prefix predicate accepts: %v",
					extKeyNamespace, mods, want)
			}

			// Negative control on the SAME query and mode: unfiltered, the base
			// modules come back too, so the assertion above measures the filter and
			// not a fixture that holds only extensions.
			all, _, err := idx.SearchWithStats(SearchParams{
				Query: "игольчатыймаркер",
				Mode:  mode,
				Limit: 500,
			})
			if err != nil {
				t.Fatalf("unfiltered search: %v", err)
			}
			if allMods := modulesOf(all); len(allMods) != 6 {
				t.Fatalf("control failed: the unfiltered search returned %d modules %v, want all 6",
					len(allMods), allMods)
			}
		})
	}
}

// THE GUARD THAT WAS MISSING, AND WHY IT WAS MISSING.
//
// bslKeyCorpusDigest pins how a PATH becomes a KEY, and it is what forces a schema
// bump when that derivation moves. It is blind to this change by construction: the
// namespace rule moved from the five-segment condition to the "ext." prefix and not
// one key in that corpus is different, so the digest sat still while a PERSISTED,
// INDEXED field changed value under four of six probe shapes. The bump was argued
// and taken by hand. Nothing would have failed if it had not been.
//
// So the namespace assignment gets a digest of its own, pinned to the schema
// version the same way, and the next change to this rule is stopped by a test
// rather than by somebody remembering.
var namespaceDigestCorpus = []string{
	// The dump deriver's shape, on both sides of the category condition.
	"ext.МоёРасш.Справочник.Ном.МодульОбъекта",
	"ext.МоёРасш.ОбщийМодуль.Общий.Модуль",
	"ext.МоёРасш.Конфигурация.МодульСеанса.Модуль",
	"ext.Расш.Справочники.Объект.МодульОбъекта",
	"ext.Ном.Форма.Ф.МодульФормы",
	// What a root holding a directory literally named «ext» derives. MEASURED off
	// the deriver, not invented: these are three of the four shapes whose namespace
	// moved when the rule changed.
	"ext.Ном.МодульОбъекта",
	// LATIN A, from a directory literally named A. Not the Cyrillic А it looks
	// like: this row was first written with the Cyrillic letter, which is a key
	// the deriver never emits. Correcting it either way moves the digest below.
	"ext.A.МодульОбъекта",
	"ext.Расш.МодульОбъекта",
	// The live .cfe ingest, four segments.
	liveIngestKey,
	"ext.ЖивоеРасширение.CommonModule0000.uuid0000",
	// The boundary of the prefix itself, in both directions.
	"ext",
	"ext.",
	"extra.Ном.МодульОбъекта",
	// Ordinary base-configuration keys, so a rule that namespaced everything would
	// move the digest too.
	"Справочник.Ном.МодульОбъекта",
	"ОбщийМодуль.Общий.Модуль",
	"Двасегмента.Ключ",
	"Односегментный",
}

const (
	bslNamespaceCorpusDigest              = "1526cd4d57389758fbf3833ea75faff115eaa486a093a342c88dc6a534ea8871"
	pinnedSchemaVersionForNamespaceDigest = 6
)

// namespaceCorpusDigest is sha256 over "<key>\t<namespace>\n" for
// namespaceDigestCorpus in the order declared above.
func namespaceCorpusDigest() (string, string) {
	var sb strings.Builder
	for _, k := range namespaceDigestCorpus {
		ns, _, _, _ := splitModuleKey(k)
		sb.WriteString(k)
		sb.WriteByte('\t')
		sb.WriteString(ns)
		sb.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:]), sb.String()
}

// TestTheNamespaceRuleIsPinnedToTheSchemaVersion fails whenever splitModuleKey puts
// a different namespace on any corpus key, and whenever dumpIndexSchemaVersion moves
// without the digest being re-taken. The namespace is an INDEXED, PERSISTED field
// and PathIndex re-derives it in-process on every start, so a rule change served
// over an un-rebuilt generation makes smart disagree with regex and exact inside one
// process. That is the case this pin exists to stop.
func TestTheNamespaceRuleIsPinnedToTheSchemaVersion(t *testing.T) {
	if dumpIndexSchemaVersion != pinnedSchemaVersionForNamespaceDigest {
		t.Fatalf("dumpIndexSchemaVersion = %d but the namespace digest was pinned under %d. "+
			"Re-take the digest and update pinnedSchemaVersionForNamespaceDigest together with the bump.",
			dumpIndexSchemaVersion, pinnedSchemaVersionForNamespaceDigest)
	}

	got, table := namespaceCorpusDigest()
	if got != bslNamespaceCorpusDigest {
		t.Errorf("the namespace assignment changed.\n"+
			"digest got  = %s\ndigest want = %s\n\n"+
			"The namespace is part of the indexed document shape, which the BUMP PROTOCOL in "+
			"generation.go names: bump dumpIndexSchemaVersion (currently %d), then re-pin both "+
			"bslNamespaceCorpusDigest and pinnedSchemaVersionForNamespaceDigest. Without the bump a "+
			"warm generation keeps the old value in its shards while PathIndex derives the new one, "+
			"and smart answers the namespace filter differently from regex and exact.\n\n"+
			"current assignment:\n%s",
			got, bslNamespaceCorpusDigest, dumpIndexSchemaVersion, table)
	}
}

// TestTheNamespaceDigestReallyDependsOnTheRule is the positive control for the pin
// above. A digest taken over a corpus none of whose keys is namespaced must differ
// from the real one — otherwise the guard would hold whatever the rule did.
func TestTheNamespaceDigestReallyDependsOnTheRule(t *testing.T) {
	real, table := namespaceCorpusDigest()

	var sb strings.Builder
	namespaced := 0
	for _, k := range namespaceDigestCorpus {
		ns, _, _, _ := splitModuleKey(k)
		if ns != "" {
			namespaced++
			ns = "" // the one thing varied
		}
		sb.WriteString(k)
		sb.WriteByte('\t')
		sb.WriteString(ns)
		sb.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(sb.String()))
	flattened := hex.EncodeToString(sum[:])

	if namespaced == 0 {
		t.Fatalf("control failed: not one corpus key is in the namespace, so flattening it "+
			"varies nothing.\n%s", table)
	}
	if flattened == real {
		t.Errorf("control failed: flattening the namespace on all %d namespaced keys left the "+
			"digest at %s, so the pin does not measure the rule", namespaced, real)
	}
}
