package dump

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

func TestDeepExtensionModulesKeepTheirOwnKeys(t *testing.T) {
	root := t.TempDir()
	const prefix = "custom/production-scale/src/cfe/РМП_РабочееМестоПроизводства"
	const module = "Catalogs/Номенклатура/Ext/ObjectModule.bsl"
	const baseKey = "Справочник.Номенклатура.МодульОбъекта"
	const extKey = "ext.ДругоеИмяРасширения." + baseKey
	writeFileOrFatal(t, filepath.Join(root, extManifestClassic), baseConfigManifest())
	mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(prefix)), extManifestClassic, "ДругоеИмяРасширения")
	writeBSL(t, root, module, "// COMMONMARKER BASEMARKER\n")
	writeBSL(t, root, prefix+"/"+module, "// COMMONMARKER EXTENSIONMARKER\n")

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatal(err)
	}
	if got := idx.ModuleNames(); len(got) != 2 || !slices.Contains(got, baseKey) || !slices.Contains(got, extKey) {
		t.Errorf("module names = %v, want %q and %q", got, baseKey, extKey)
	}
	for key, want := range map[string]string{
		baseKey: "// COMMONMARKER BASEMARKER\n",
		extKey:  "// COMMONMARKER EXTENSIONMARKER\n",
	} {
		if got, ok := idx.GetContent(key); !ok || got != want {
			t.Errorf("GetContent(%q) = %q, %v, want %q", key, got, ok, want)
		}
	}
	if got := idx.CollapsedKeys().Files; got != 0 {
		t.Errorf("collapsed files = %d, want 0", got)
	}
	if got := idx.WrappedPaths().Files; got != 0 {
		t.Errorf("wrapped paths = %d, want 0", got)
	}
	layout := detectExtensionLayout(root)
	if got := layout.moduleKey(filepath.FromSlash(prefix + "-copy/" + module)); got != baseKey {
		t.Errorf("module beside the extension = %q, want %q", got, baseKey)
	}
}

func TestDeepExtensionDiscoveryPreservesRecognizedRoots(t *testing.T) {
	root := t.TempDir()
	for path, name := range map[string]string{
		"shallow/wrapper":              "Й",
		"outer/branch/deep":            "И\u0306",
		"outer/branch/duplicate-a":     "Duplicate",
		"outer/branch/duplicate-b":     "Duplicate",
		"outer/branch/known":           "Parent",
		"outer/branch/known/child":     "HiddenByParent",
		"outer/branch/Catalogs/object": "Metadata",
		"outer/branch/Расширения/ext":  "LegacyContainer",
	} {
		mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(path)), extManifestClassic, name)
	}
	outside := mkExtensionDump(t, t.TempDir(), extManifestClassic, "Outside")
	symlinkOrSkip(t, outside, filepath.Join(root, "outer", "branch", "linked"))
	layout := detectExtensionLayout(root)
	if got := layout.summary().Dirs; !slices.Equal(got, []string{"outer/branch/known", "shallow/wrapper"}) {
		t.Fatalf("recognized prefixes = %v", got)
	}
	const module = "/Catalogs/Items/Ext/ObjectModule.bsl"
	if got := layout.moduleKey("shallow/wrapper" + module); got != "ext.Й.Справочник.Items.МодульОбъекта" {
		t.Errorf("the deep NFC duplicate took the shallow key: %q", got)
	}
}

func TestDeepExtensionBudgetLeavesShallowSourcesUsable(t *testing.T) {
	root := t.TempDir()
	direct := mkExtensionDump(t, filepath.Join(root, "z-direct"), extManifestClassic, "Direct")
	shallow := mkExtensionDump(t, filepath.Join(root, "z-wrapper", "extension"), extManifestClassic, "Shallow")
	const module = "Catalogs/Items/Ext/ObjectModule.bsl"
	writeBSL(t, direct, module, "// direct\n")
	writeBSL(t, shallow, module, "// shallow\n")
	form := seedExtensionForm(t, shallow, "Items", "ItemForm", formXMLWith("shallow"))
	writeFileOrFatal(t, filepath.Join(root, extManifestClassic), baseConfigManifest())
	selection := MetadataSelection{Kind: "configuration"}
	checkStrictSources := func(wantSource string) {
		t.Helper()
		if got, err := FindFormFiles(root, "DataProcessor", "Items"); err != nil || got["ItemForm"] != form {
			t.Errorf("FindFormFiles = %v, %v, want %q", got, err, form)
		}
		if got, err := ReadMetadataSummary(context.Background(), root, selection); err != nil || len(got.Sources) != 1 || got.Sources[0].Path != wantSource {
			t.Errorf("ReadMetadataSummary = %+v, %v, want %q", got, err, wantSource)
		}
	}
	checkStrictSources("Configuration.xml")

	// Three 24-entry branches exceed the range of the independent deep budget.
	for i := 0; i < 3; i++ {
		for j := 0; j < 24; j++ {
			path := filepath.Join(root, "a-wide", fmt.Sprintf("branch-%d", i), fmt.Sprintf("candidate-%02d", j))
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	mkExtensionDump(t, filepath.Join(root, "a-wide", "branch-0", "candidate-00"), extManifestClassic, "Early")
	mkExtensionDump(t, filepath.Join(root, "a-wide", "branch-2", "candidate-23"), extManifestClassic, "Unreached")
	mkExtensionDump(t, filepath.Join(root, "a-wide", "branch-0", "candidate-01", "extension"), extManifestClassic, "LaterLevel")
	// A real metadata directory above many objects stays out of the descent.
	for i := 0; i < 40; i++ {
		writeBSL(t, root, fmt.Sprintf("Catalogs/Base%02d/Ext/ObjectModule.bsl", i), "// base\n")
	}

	layout := detectExtensionLayout(root)
	if got := layout.summary(); !got.ScanTruncated || !slices.Equal(got.Dirs, []string{"a-wide/branch-0/candidate-00", "z-direct", "z-wrapper/extension"}) {
		t.Errorf("layout summary = %+v, want the first deep candidate, both shallow roots and truncation", got)
	}
	// Root + 4 immediate candidates + 4 second-level candidates + 64 deep ones.
	if got := layout.cost.Lstats; got != 9+maxDeepProbeChildren {
		t.Errorf("manifest candidates = %d, want %d", got, 9+maxDeepProbeChildren)
	}
	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"ext.Direct.Справочник.Items.МодульОбъекта":  "// direct\n",
		"ext.Shallow.Справочник.Items.МодульОбъекта": "// shallow\n",
	} {
		if got, ok := idx.GetContent(key); !ok || got != want {
			t.Errorf("GetContent(%q) = %q, %v, want %q", key, got, ok, want)
		}
	}
	checkStrictSources("Configuration.xml")
	if err := os.Remove(filepath.Join(root, extManifestClassic)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(direct, extManifestClassic)); err != nil {
		t.Fatal(err)
	}
	checkStrictSources("z-wrapper/extension/Configuration.xml")
}

func TestDeepExtensionRebuildsSchemaEightFlatCache(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	const prefix = "custom/production-scale/src/cfe/extension"
	const module = "Catalogs/Items/Ext/ObjectModule.bsl"
	const oldKey = "Справочник.Items.МодульОбъекта"
	const newKey = "ext.ManifestName." + oldKey
	const body = "// extension\n"
	writeBSL(t, root, prefix+"/"+module, body)

	// Reproduce the v8 docID by indexing before the manifest is visible.
	if err := BuildCache(root, cache, false); err != nil {
		t.Fatal(err)
	}
	cacheRoot, err := cachePath(root, cache)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(cacheRoot)
	if err != nil || manifest == nil {
		t.Fatalf("LoadManifest: %v, %v", manifest, err)
	}
	if got := docIDsFromManifest(manifest); !slices.Equal(got, []string{oldKey}) {
		t.Fatalf("stale docIDs = %v, want %q", got, oldKey)
	}
	manifest.SchemaVersion = 8
	if err := manifest.Save(cacheRoot); err != nil {
		t.Fatal(err)
	}
	mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(prefix)), extManifestClassic, "ManifestName")

	idx, err := NewIndex(root, cache, false)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatal(err)
	}
	if got := idx.ModuleNames(); !slices.Equal(got, []string{newKey}) {
		t.Errorf("rebuilt docIDs = %v, want %q", got, newKey)
	}
	if got, ok := idx.GetContent(newKey); !ok || got != body {
		t.Errorf("GetContent(%q) = %q, %v, want %q", newKey, got, ok, body)
	}
}

func TestDeepExtensionKeepsLegacyModuleIdentity(t *testing.T) {
	for _, legacyPrefix := range []string{
		"Расширения/И\u0306",
		"a-wrapper/Расширения/И\u0306",
		"custom/production/src/Расширения/И\u0306",
	} {
		t.Run(legacyPrefix, func(t *testing.T) {
			root, cache := t.TempDir(), t.TempDir()
			const deep = "custom/production/src"
			const module = "Catalogs/Items/Ext/ObjectModule.bsl"
			const oldKey = "Справочник.Items.МодульОбъекта"
			const extKey = "ext.Й." + oldKey
			mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(deep)), extManifestClassic, "Й")
			writeBSL(t, root, deep+"/"+module, "// deepmarker\n")

			check := func(idx *Index, want map[string]string) {
				t.Helper()
				defer idx.Close()
				<-idx.Done()
				if err := idx.BuildError(); err != nil {
					t.Fatal(err)
				}
				if names := idx.ModuleNames(); len(names) != len(want) || idx.CollapsedKeys().Files != 0 {
					t.Errorf("names = %v, collapsed = %d, want distinct keys %v", names, idx.CollapsedKeys().Files, want)
				}
				for key, body := range want {
					if got, ok := idx.GetContent(key); !ok || got != body {
						t.Errorf("GetContent(%q) = %q, %v, want %q", key, got, ok, body)
					}
					marker := strings.TrimSpace(strings.TrimPrefix(body, "// "))
					matches, _, err := idx.Search(SearchParams{Query: marker})
					if err != nil || len(matches) != 1 || matches[0].Module != key {
						t.Errorf("smart search for %q = %+v, %v, want %q", marker, matches, err, key)
					}
				}
			}
			openFlat := func(cache string) *Index {
				t.Helper()
				idx, err := NewIndex(root, cache, false)
				if err != nil {
					t.Fatal(err)
				}
				return idx
			}

			deepOnly := map[string]string{extKey: "// deepmarker\n"}
			mixed := map[string]string{extKey: "// legacymarker\n", oldKey: "// deepmarker\n"}
			flatSnapshot := func(cache string, want map[string]string) func() {
				t.Helper()
				cpath, err := cachePath(root, cache)
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(manifestPath(cpath))
				if err != nil {
					t.Fatal(err)
				}
				return func() {
					t.Helper()
					after, err := os.ReadFile(manifestPath(cpath))
					if err != nil || !bytes.Equal(after, before) {
						t.Errorf("flat manifest changed: %v", err)
					}
					shards, err := openCachedShards(cacheShardDirs(cpath), true, "1s")
					if err != nil {
						t.Fatal(err)
					}
					for _, shard := range shards {
						defer shard.Close()
					}
					alias := bleve.NewIndexAlias(shards...)
					if count, err := alias.DocCount(); err != nil || count != uint64(len(want)) {
						t.Errorf("old flat shard document count = %d, %v, want %d", count, err, len(want))
					}
					for key, body := range want {
						marker := strings.TrimSpace(strings.TrimPrefix(body, "// "))
						query := bleve.NewMatchQuery(marker)
						query.SetField("content")
						hits, err := alias.Search(bleve.NewSearchRequest(query))
						if err != nil || len(hits.Hits) != 1 || hits.Hits[0].ID != key {
							t.Errorf("old flat shard search for %q = %+v, %v, want %q", marker, hits, err, key)
						}
					}
				}
			}

			check(openFlat(cache), deepOnly)
			checkOriginalFlat := flatSnapshot(cache, deepOnly)

			// A new legacy module takes precedence over an already persisted deep key.
			writeBSL(t, root, legacyPrefix+"/"+module, "// legacymarker\n")
			check(openFlat(cache), mixed)
			checkOriginalFlat()

			// Explicit prewarm leaves the flat cache intact and publishes a usable READY.
			if err := BuildCache(root, cache, false); err != nil {
				t.Fatal(err)
			}
			sig := mustGenSig(t, root)
			if !GenerationReady(root, cache, sig) {
				t.Fatal("prewarm did not publish a READY generation")
			}
			generation, err := OpenGenerationReadOnly(root, cache, sig)
			if err != nil {
				t.Fatal(err)
			}
			check(generation, mixed)
			checkOriginalFlat()

			mixedCache := t.TempDir()
			check(openFlat(mixedCache), mixed)
			checkMixedFlat := flatSnapshot(mixedCache, mixed)
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(legacyPrefix+"/"+module))); err != nil {
				t.Fatal(err)
			}
			check(openFlat(mixedCache), deepOnly)
			checkMixedFlat()
		})
	}
}

func TestDeepExtensionKeepsAnEnclosedLegacyModule(t *testing.T) {
	root := t.TempDir()
	const deep = "custom/production/src"
	const module = "Catalogs/Items/Ext/ObjectModule.bsl"
	const suffix = "Справочник.Items.МодульОбъекта"
	mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(deep)), extManifestClassic, "Modern")
	writeBSL(t, root, deep+"/"+module, "// modern\n")
	writeBSL(t, root, deep+"/Расширения/Legacy/"+module, "// legacy\n")

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatal(err)
	}
	if got := idx.ModuleNames(); !slices.Equal(got, []string{"ext.Legacy." + suffix, "ext.Modern." + suffix}) {
		t.Errorf("module names = %v", got)
	}
	if got := idx.ExtensionLayout().Dirs; !slices.Equal(got, []string{deep}) {
		t.Errorf("indexed extension prefixes = %v, want %q", got, deep)
	}
	wantWrap := WrappedPathState{Files: 1, Total: 2}
	if got := idx.WrappedPaths(); got != wantWrap {
		t.Errorf("wrapped paths = %+v, want %+v", got, wantWrap)
	}

	const moved = "custom/production/renamed"
	if err := os.Rename(filepath.Join(root, filepath.FromSlash(deep)), filepath.Join(root, filepath.FromSlash(moved))); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := idx.ExtensionLayout().Dirs; !slices.Equal(got, []string{moved}) {
		t.Errorf("updated layout prefixes = %v, want %q", got, moved)
	}
	if got := idx.WrappedPaths(); got != wantWrap {
		t.Errorf("updated wrapped paths = %+v, want %+v", got, wantWrap)
	}
}
