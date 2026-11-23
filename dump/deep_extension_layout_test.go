package dump

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
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
