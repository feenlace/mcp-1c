package dump

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFlatCollisionMembershipRekeysBeforeShardWrites(t *testing.T) {
	const a = "A/Catalogs/Товары/Ext/ObjectModule.bsl"
	const b = "B/Catalogs/Товары/Ext/ObjectModule.bsl"
	const plain = "Справочник.Товары.МодульОбъекта"
	const aKey = "Справочник.Товары@/A/Catalogs/Товары/Ext/ObjectModule%2Ebsl.МодульОбъекта"
	const bKey = "Справочник.Товары@/B/Catalogs/Товары/Ext/ObjectModule%2Ebsl.МодульОбъекта"
	const aBody, bBody = "// commonmarker firstmarker\n", "// commonmarker secondmarker\n"

	dir, cache := t.TempDir(), t.TempDir()
	mkBSLFile(t, dir, a, aBody)
	openFlat := func(cacheDir string, want map[string]string) {
		t.Helper()
		idx := migOpen(t, dir, cacheDir, false)
		defer idx.Close()
		requireCollisionBodies(t, idx, want)
		requireModuleHits(t, idx, "commonmarker", "Справочник", "МодульОбъекта", want)
	}
	requireManifest := func(cacheDir string, want ...string) {
		t.Helper()
		m, err := LoadManifest(migCachePath(t, dir, cacheDir))
		if err != nil || m == nil {
			t.Fatalf("original flat manifest unavailable: %+v, %v", m, err)
		}
		got := docIDsFromManifest(m)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("original flat manifest changed: %v, want %v", got, want)
		}
	}

	openFlat(cache, map[string]string{plain: aBody})
	mkBSLFile(t, dir, b, bBody)
	pair := map[string]string{aKey: aBody, bKey: bBody}
	openFlat(cache, pair)
	requireManifest(cache, plain)

	// Make the offside prewarm destination unusable. An index with new path
	// maps over the original shards must never become ready on failure.
	if err := os.WriteFile(generationsDir(migCachePath(t, dir, cache)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	idx, err := (IndexOptions{}).newIndex(dir, cache, false, true)
	if err != nil {
		t.Fatal(err)
	}
	<-idx.Done()
	if idx.Ready() || idx.BuildError() == nil {
		t.Error("failed offside rebuild published a ready index or lost its build error")
	}
	idx.Close()
	requireManifest(cache, plain)

	pairCache := t.TempDir()
	openFlat(pairCache, pair)
	openFlat(pairCache, pair)
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(b))); err != nil {
		t.Fatal(err)
	}
	openFlat(pairCache, map[string]string{plain: aBody})
	requireManifest(pairCache, aKey, bKey)
}

func TestStandaloneFormPathsPersistAcrossWarmOpenAndReload(t *testing.T) {
	const owner = "custom/oc-exchange/Настройки"
	const first = owner + "/Forms/Форма/Ext/Form/Module.bsl"
	const second = owner + "/Forms/ФормаСписок/Ext/Form/Module.bsl"
	const firstKey = "Форма." + owner + "/Forms/Форма.МодульФормы"
	const secondKey = "Форма." + owner + "/Forms/ФормаСписок.МодульФормы"
	const firstBody, secondBody = "// commonmarker firstmarker\n", "// commonmarker secondmarker\n"

	dir, cache := t.TempDir(), t.TempDir()
	mkBSLFile(t, dir, first, firstBody)
	mkBSLFile(t, dir, second, secondBody)

	cases := map[string]string{
		first:  firstKey,
		second: secondKey,
		"custom/Настрои\u0306ки/Forms/Ё.Ф/Ext/Form/Module.bsl":         "Форма.custom/Настройки/Forms/Ё%2EФ.МодульФормы",
		"custom/Настройки/Forms/Ё%2EФ/Ext/Form/Module.bsl":             "Форма.custom/Настройки/Forms/Ё%252EФ.МодульФормы",
		"other/oc-exchange/Настройки/Forms/Форма/Ext/Form/Module.bsl":  "Форма.other/oc-exchange/Настройки/Forms/Форма.МодульФормы",
		"custom/Documents/Док/Forms/Форма/Ext/Form/Module.bsl":         "Документ.Док.Форма.Форма.МодульФормы",
		"Расширения/Мое/Documents/Док/Forms/Форма/Ext/Form/Module.bsl": "ext.Мое.Документ.Док.Форма.Форма.МодульФормы",
		"custom/Настройки/Forms//Ext/Form/Module.bsl":                  "custom.Настройки.Форма..МодульФормы",
		"custom/Настройки/Ext/ObjectModule.bsl":                        "custom.Настройки.МодульОбъекта",
	}
	for path, want := range cases {
		if got := bslPathToModuleName(path); got != want {
			t.Errorf("module key for %q = %q, want %q", path, got, want)
		}
	}
	// Normalization can collapse the display name of two physical paths;
	// the raw-byte qualifiers keep the two assignments separate.
	normalized := []string{first, "custom/oc-exchange/Настрои\u0306ки/Forms/Форма/Ext/Form/Module.bsl"}
	ids := assignBSLDocIDs(normalized, extensionLayout{})
	if ids[normalized[0]] == ids[normalized[1]] || ids[normalized[0]] == firstKey || ids[normalized[1]] == firstKey {
		t.Errorf("normalization collision left an ambiguous key: %v", ids)
	}

	sig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cache, sig); err != nil {
		t.Fatal(err)
	}
	requireForms := func(idx *Index, want map[string]string) {
		t.Helper()
		requireCollisionBodies(t, idx, want)
		requireModuleHits(t, idx, "commonmarker", "Форма", "МодульФормы", want)
		if _, found := idx.GetContent("custom.oc-exchange.МодульФормы"); found || idx.GetPathIndex().Contains("custom.oc-exchange.МодульФормы") {
			t.Error("old ambiguous key still resolves")
		}
	}
	pair := map[string]string{firstKey: firstBody, secondKey: secondBody}
	idx := openSameNameGeneration(t, dir, cache, sig)
	requireForms(idx, pair)
	idx.Close()
	idx = openSameNameGeneration(t, dir, cache, sig)
	defer idx.Close()
	requireForms(idx, pair)

	const changedBody = "// commonmarker updatedmarker\n"
	mkBSLFile(t, dir, first, changedBody)
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(second))); err != nil {
		t.Fatal(err)
	}
	mustReload(t, idx)
	current := map[string]string{firstKey: changedBody}
	requireForms(idx, current)
	reopened := openSameNameGeneration(t, dir, cache, mustGenSig(t, dir))
	defer reopened.Close()
	requireForms(reopened, current)
}

func TestReadGenerationNamesRejectsDuplicatePersistedIDs(t *testing.T) {
	dir := t.TempDir()
	m := Manifest{Version: manifestVersion, SchemaVersion: dumpIndexSchemaVersion, ZapVersion: zapSegmentVersion, Files: map[string]FileEntry{
		"a.bsl": {DocID: "Й.Модуль"},
		"b.bsl": {DocID: "И\u0306.Модуль"},
	}}
	if err := m.Save(dir); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readGenerationNames(dir, dir); err == nil {
		t.Fatal("duplicate NFC document IDs were accepted")
	}
}

func requireModuleHits(t *testing.T, idx *Index, query, category, module string, want map[string]string) {
	t.Helper()
	for _, mode := range []SearchMode{SearchModeSmart, SearchModeExact, SearchModeRegex} {
		matches, total, err := idx.Search(SearchParams{Query: query, Category: category, Module: module, Mode: mode})
		if err != nil || total != len(want) || len(matches) != len(want) {
			t.Errorf("%s: got %d matches, total %d, error %v, want %d", mode, len(matches), total, err, len(want))
		}
		seen := make(map[string]bool, len(want))
		for _, hit := range matches {
			if body, exists := want[hit.Module]; !exists || seen[hit.Module] || hit.Line != 1 || hit.Context != body {
				t.Errorf("%s: wrong source for %q: %+v", mode, hit.Module, hit)
			}
			seen[hit.Module] = true
		}
	}
}

func TestFlatUnreadableChangeDoesNotCertifyOrDelete(t *testing.T) {
	root, flat, liveCache := t.TempDir(), t.TempDir(), t.TempDir()
	const modified = "Catalogs/Changed/Ext/ObjectModule.bsl"
	const deleted = "Catalogs/Deleted/Ext/ObjectModule.bsl"
	const id = "Справочник.Changed.МодульОбъекта"
	const newBody = "// replacementmarker new body\n"
	mkBSLFile(t, root, modified, "// originalmarker\n")
	mkBSLFile(t, root, deleted, "// deletemarker\n")
	if err := BuildCache(root, flat, false); err != nil {
		t.Fatal(err)
	}
	live := openReloadableIndex(t, root, liveCache)
	oldSig := mustGenSig(t, root)
	cpath := migCachePath(t, root, flat)
	before, err := os.ReadFile(manifestPath(cpath))
	if err != nil {
		t.Fatal(err)
	}
	shards := cacheShardDirs(cpath)
	oldShards := snapshotShardTree(t, shards)

	mkBSLFile(t, root, modified, newBody)
	changedPath := filepath.Join(root, filepath.FromSlash(modified))
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(deleted))); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(changedPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(changedPath, 0o600) })
	if _, err := os.ReadFile(changedPath); !errors.Is(err, os.ErrPermission) {
		t.Skipf("fixture needs real source read denial, got %v", err)
	}
	if err := BuildCache(root, flat, false); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("prewarm did not report actual read failure: %v", err)
	}
	idx, err := NewIndex(root, flat, false)
	if err != nil {
		t.Fatal(err)
	}
	<-idx.Done()
	if idx.Ready() || !errors.Is(idx.BuildError(), os.ErrPermission) {
		t.Errorf("warm writable open published a failed update: ready=%v error=%v", idx.Ready(), idx.BuildError())
	}
	idx.Close()
	after, err := os.ReadFile(manifestPath(cpath))
	if err != nil || !bytes.Equal(after, before) || !sameSnapshot(snapshotShardTree(t, shards), oldShards) {
		t.Fatal("an unreadable change mutated the admitted old flat cache")
	}

	newSig := mustGenSig(t, root)
	if gen, err := live.Reload(); !errors.Is(err, os.ErrPermission) || gen.Changed ||
		live.gensig != oldSig || !GenerationReady(root, liveCache, oldSig) || GenerationReady(root, liveCache, newSig) {
		t.Fatalf("failed rebuild replaced an active generation: %+v, %v", gen, err)
	}
	if err := os.Chmod(changedPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := BuildCache(root, flat, false); err != nil {
		t.Fatal(err)
	}
	warm, err := OpenForServe(root, flat)
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	<-warm.Done()
	if err := warm.BuildError(); err != nil {
		t.Fatal(err)
	}
	requireModuleHits(t, warm, "replacementmarker", "Справочник", "МодульОбъекта", map[string]string{id: newBody})
	requireModuleHits(t, warm, "originalmarker", "Справочник", "МодульОбъекта", nil)
}

func TestFlatManifestSaveFailureCannotCertifyMixedShards(t *testing.T) {
	root, flat := t.TempDir(), t.TempDir()
	const rel = "Catalogs/Changed/Ext/ObjectModule.bsl"
	const id = "Справочник.Changed.МодульОбъекта"
	const original = "// originalmarker\n"
	mkBSLFile(t, root, rel, original)
	source := filepath.Join(root, filepath.FromSlash(rel))
	before, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := BuildCache(root, flat, false); err != nil {
		t.Fatal(err)
	}
	cpath := migCachePath(t, root, flat)
	old, err := LoadManifest(cpath)
	if err != nil || old == nil {
		t.Fatalf("fixture manifest: %+v, %v", old, err)
	}
	mkBSLFile(t, root, rel, "// replacementmarker changed content\n")
	// The shard write succeeds but the atomic manifest save cannot start.
	if err := os.Mkdir(manifestPath(cpath)+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := BuildCache(root, flat, false); err == nil || !strings.Contains(err.Error(), "writing manifest tmp") {
		t.Fatalf("expected the post-write manifest error, got %v", err)
	}
	if m, err := LoadManifest(cpath); err != nil || m != nil {
		t.Fatalf("partial flat update left a valid stamp: %+v, %v", m, err)
	}

	mkBSLFile(t, root, rel, original)
	if err := os.Chtimes(source, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if diff, err := old.Diff(root); err != nil || !diff.Empty() {
		t.Fatalf("fixture must restore the old tuple exactly: %+v, %v", diff, err)
	}
	warm, err := OpenForServe(root, flat)
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	<-warm.Done()
	if err := warm.BuildError(); err != nil {
		t.Fatal(err)
	}
	requireModuleHits(t, warm, "originalmarker", "Справочник", "МодульОбъекта", map[string]string{id: original})
	requireModuleHits(t, warm, "replacementmarker", "Справочник", "МодульОбъекта", nil)
}
