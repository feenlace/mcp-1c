package dump

import (
	"context"
	"os"
	"strings"
	"testing"
)

// nestedRealDumpDir is a real 1C 8.3.27 DumpConfigToFiles output, produced on the
// owner's Windows VM specifically to settle the premise the branch's fixture could
// only cite: that CalculationRegisters/Recalculations, ExternalDataSources/Tables,
// ExternalDataSources/Cubes and Cubes/DimensionTables are the directory names the
// platform actually writes. See dump/testdata/nested_kind_dirs.txt for the citation
// this dump replaces with an observation.
const nestedRealDumpDir = "testdata/nested_real_8327"

// nestedRealDumpMarker is the one BSL file this real dump keys for every
// nested-kind module, with the marker comment the owner placed in it and the key
// this branch (schema 8) mints for it, measured by running the branch's own
// --build-index over a copy of this fixture.
type nestedRealDumpMarker struct {
	relPath string
	marker  string
	keyV8   string
	// keyV7 is the key the SAME path minted before this branch, measured by
	// building the v1.19.0 tag (dafe4969) over the identical fixture. Schema 7's
	// derivation is not reachable from this tree (index.go was edited in place,
	// not branched by schema), so this is a pinned observation, not a call to old
	// code.
	keyV7 string
}

// nestedRealDumpMarkers is the complete file list:
// nine .bsl files, one marker each, no file omitted. Completeness is asserted by
// TestNestedRealDumpHasExactlyTheseNineModules below rather than trusted here.
var nestedRealDumpMarkers = []nestedRealDumpMarker{
	{
		relPath: "CalculationRegisters/Начисления/Ext/RecordSetModule.bsl",
		marker:  "МАРКЕР_РЕГИСТР_НАБОР",
		keyV8:   "РегистрРасчета.Начисления.МодульНабораЗаписей",
		keyV7:   "РегистрРасчета.Начисления.МодульНабораЗаписей",
	},
	{
		relPath: "CalculationRegisters/Начисления/Recalculations/Перерасчет1/Ext/RecordSetModule.bsl",
		marker:  "МАРКЕР_ПЕРЕРАСЧЕТ_1",
		keyV8:   "РегистрРасчета.Начисления.Перерасчет.Перерасчет1.МодульНабораЗаписей",
		keyV7:   "РегистрРасчета.Начисления.МодульНабораЗаписей",
	},
	{
		relPath: "CalculationRegisters/Начисления/Recalculations/Перерасчет2/Ext/RecordSetModule.bsl",
		marker:  "МАРКЕР_ПЕРЕРАСЧЕТ_2",
		keyV8:   "РегистрРасчета.Начисления.Перерасчет.Перерасчет2.МодульНабораЗаписей",
		keyV7:   "РегистрРасчета.Начисления.МодульНабораЗаписей",
	},
	{
		relPath: "ExternalDataSources/Источник1/Cubes/К1/DimensionTables/И1/Ext/ObjectModule.bsl",
		marker:  "МАРКЕР_ИЗМЕРЕНИЕ_1",
		keyV8:   "ВнешнийИсточникДанных.Источник1.Куб.К1.ТаблицаИзмерения.И1.МодульОбъекта",
		keyV7:   "ExternalDataSources.Источник1.МодульОбъекта",
	},
	{
		relPath: "ExternalDataSources/Источник1/Cubes/К1/Ext/RecordSetModule.bsl",
		marker:  "МАРКЕР_КУБ_1",
		keyV8:   "ВнешнийИсточникДанных.Источник1.Куб.К1.МодульНабораЗаписей",
		keyV7:   "ExternalDataSources.Источник1.МодульНабораЗаписей",
	},
	{
		relPath: "ExternalDataSources/Источник1/Tables/Т1/Ext/RecordSetModule.bsl",
		marker:  "МАРКЕР_ТАБЛИЦА_1",
		keyV8:   "ВнешнийИсточникДанных.Источник1.Таблица.Т1.МодульНабораЗаписей",
		keyV7:   "ExternalDataSources.Источник1.МодульНабораЗаписей",
	},
	{
		relPath: "ExternalDataSources/Источник1/Tables/Т1/Forms/Ф1/Ext/Form/Module.bsl",
		marker:  "МАРКЕР_ФОРМА_Т1",
		keyV8:   "ВнешнийИсточникДанных.Источник1.Таблица.Т1.Форма.Ф1.МодульФормы",
		keyV7:   "ExternalDataSources.Источник1.Форма.Ф1.МодульФормы",
	},
	{
		relPath: "IntegrationServices/Сервис1/Ext/Module.bsl",
		marker:  "МАРКЕР_СЕРВИС_1",
		keyV8:   "СервисИнтеграции.Сервис1.Модуль",
		keyV7:   "IntegrationServices.Сервис1.МодульФормы",
	},
	{
		relPath: "WebSocketClients/Клиент1/Ext/Module.bsl",
		marker:  "МАРКЕР_КЛИЕНТ_1",
		keyV8:   "WebSocketКлиент.Клиент1.Модуль",
		keyV7:   "WebSocketClients.Клиент1.МодульФормы",
	},
}

// newRealDumpIndex builds an *Index the way bench_test.go's loadTestModules does:
// enough of the struct for loadBSLFiles to run without a full NewIndex build.
func newRealDumpIndex(t *testing.T, dir string) *Index {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	idx := &Index{
		dir:           dir,
		contentByName: make(map[string]cachedModule),
		pathByName:    make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	t.Cleanup(func() { close(idx.done) })
	if err := idx.loadBSLFiles(dir); err != nil {
		t.Fatalf("loadBSLFiles(%q): %v", dir, err)
	}
	return idx
}

// TestNestedRealDumpHasExactlyTheseNineModules guards nestedRealDumpMarkers itself:
// if the fixture ever gains or loses a .bsl file, this fails before either test
// below can silently check fewer files than the fixture holds.
func TestNestedRealDumpHasExactlyTheseNineModules(t *testing.T) {
	idx := newRealDumpIndex(t, nestedRealDumpDir)
	if got, want := len(idx.pathToDocID), len(nestedRealDumpMarkers); got != want {
		t.Fatalf("testdata/nested_real_8327 holds %d .bsl files, nestedRealDumpMarkers "+
			"declares %d; update the table, this is a real dump and not a synthetic one",
			got, want)
	}
	for _, m := range nestedRealDumpMarkers {
		if _, ok := idx.pathToDocID[m.relPath]; !ok {
			t.Errorf("fixture is missing declared path %q", m.relPath)
		}
	}
}

// TestNestedRealDumpMintsNineDistinctKeysAtSchema8 is the branch's own claim,
// checked against the real dump rather than the branch's own fixture: every one of
// the nine module files gets the exact key the design derived, and the nine keys
// are pairwise distinct.
func TestNestedRealDumpMintsNineDistinctKeysAtSchema8(t *testing.T) {
	idx := newRealDumpIndex(t, nestedRealDumpDir)

	seen := make(map[string]string, len(nestedRealDumpMarkers))
	for _, m := range nestedRealDumpMarkers {
		got, ok := idx.pathToDocID[m.relPath]
		if !ok {
			t.Errorf("path %q not indexed", m.relPath)
			continue
		}
		if got != m.keyV8 {
			t.Errorf("path %q keyed as %q, want %q", m.relPath, got, m.keyV8)
		}
		if prevPath, dup := seen[got]; dup {
			t.Errorf("key %q claimed by both %q and %q", got, prevPath, m.relPath)
		}
		seen[got] = m.relPath
	}
	if len(seen) != len(nestedRealDumpMarkers) {
		t.Fatalf("got %d distinct keys for %d files, want all distinct",
			len(seen), len(nestedRealDumpMarkers))
	}

	// Every key's own content carries its own marker and no other: proves the key
	// is not just distinct as a string but actually addresses the right file.
	for _, m := range nestedRealDumpMarkers {
		key, ok := idx.pathToDocID[m.relPath]
		if !ok {
			continue // already reported above
		}
		entry, ok := idx.contentByName[key]
		if !ok {
			t.Errorf("key %q for %q has no content entry", key, m.relPath)
			continue
		}
		if !strings.Contains(entry.content, m.marker) {
			t.Errorf("key %q (from %q) content does not contain marker %q; content: %q",
				key, m.relPath, m.marker, entry.content)
		}
		for _, other := range nestedRealDumpMarkers {
			if other.relPath == m.relPath {
				continue
			}
			if strings.Contains(entry.content, other.marker) {
				t.Errorf("key %q (from %q) content unexpectedly contains marker %q from %q",
					key, m.relPath, other.marker, other.relPath)
			}
		}
	}
}

// TestNestedRealDumpCollidedAtSchema7 pins the collision the same real dump
// produced before this branch. Schema 7's derivation was edited in place (no
// version branch survives in dump/index.go: see generation.go:199's own comment
// that the derivation "moved again"), so this is not a call to old code; it is the
// key list measured by building the v1.19.0 tag (dafe4969) over a
// byte-identical copy of this same fixture.
func TestNestedRealDumpCollidedAtSchema7(t *testing.T) {
	seen := make(map[string][]string, len(nestedRealDumpMarkers))
	for _, m := range nestedRealDumpMarkers {
		seen[m.keyV7] = append(seen[m.keyV7], m.relPath)
	}

	const wantDistinctKeys = 6
	if len(seen) != wantDistinctKeys {
		t.Fatalf("pinned schema-7 measurement collapses %d files onto %d keys, "+
			"table now says %d; the table was edited without re-measuring v1.19.0",
			len(nestedRealDumpMarkers), wantDistinctKeys, len(seen))
	}

	wantCollisions := map[string]int{
		"РегистрРасчета.Начисления.МодульНабораЗаписей":     3,
		"ExternalDataSources.Источник1.МодульНабораЗаписей": 2,
	}
	collisions := 0
	for key, paths := range seen {
		if len(paths) < 2 {
			continue
		}
		collisions++
		want, known := wantCollisions[key]
		if !known {
			t.Errorf("unexpected schema-7 collision at key %q: %v", key, paths)
			continue
		}
		if len(paths) != want {
			t.Errorf("key %q collided %d files, measured v1.19.0 gave %d: %v",
				key, len(paths), want, paths)
		}
	}
	if collisions != len(wantCollisions) {
		t.Fatalf("found %d colliding keys, measured v1.19.0 gave %d", collisions, len(wantCollisions))
	}

	// Positive control: the recalculation paths collided at
	// v1.19.0 precisely because subdirSegmentNames did not know Recalculations,
	// Tables, Cubes or DimensionTables yet, and dumpDirNames did not know
	// ExternalDataSources. If a future edit widens either table so widely that the
	// pinned schema-7 keys stop matching what the real *v1.19.0 binary* produced,
	// this whole test is comparing against a moved target, not a stable measurement
	// of a tagged release; the digest guard in module_key_guard_test.go is the
	// instrument for schema 8 itself, this one is only for the historical crossing.
	if _, err := os.Stat(nestedRealDumpDir); err != nil {
		t.Fatalf("fixture directory missing: %v", err)
	}
}
