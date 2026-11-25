package dump

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const (
	collisionObjectRel = "Documents/ПеремещениеЗапасов/Ext/ObjectModule.bsl"
	collisionBaseKey   = "Документ.ПеремещениеЗапасов.МодульОбъекта"
)

// buildCollisionIndex builds the real index over root and waits for it.
func buildCollisionIndex(t *testing.T, root string) *Index {
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
	return idx
}

func requireCollisionBodies(t *testing.T, idx *Index, want map[string]string) {
	t.Helper()
	names := make([]string, 0, len(want))
	for key, body := range want {
		names = append(names, key)
		if got, ok := idx.GetContent(key); !ok || got != body {
			t.Errorf("GetContent(%q) = %q, %v, want %q", key, got, ok, body)
		}
	}
	slices.Sort(names)
	if got := idx.ModuleNames(); !slices.Equal(got, names) {
		t.Errorf("module names = %v, want %v", got, names)
	}
	if got := idx.CollapsedKeys(); got.Files != 0 {
		t.Errorf("collapsed = %+v, want zero", got)
	}
}

func TestSameNameRootsAcrossDepthsAndDiscoveryOrder(t *testing.T) {
	const name = "Честное"
	const direct = "Настоящее"
	for _, wrapper := range []string{"Ааа", "Яяя"} {
		for _, nestedName := range []string{name, "Иное"} {
			t.Run(wrapper+"/"+nestedName, func(t *testing.T) {
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, extManifestClassic), []byte(baseConfigManifest()), 0o644); err != nil {
					t.Fatal(err)
				}
				prefix := wrapper + "/внутри"
				mkExtensionDump(t, filepath.Join(root, direct), extManifestClassic, name)
				mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(prefix)), extManifestClassic, nestedName)
				mkBSLFile(t, root, direct+"/"+collisionObjectRel, "// directmarker\n")
				mkBSLFile(t, root, prefix+"/"+collisionObjectRel, "// nestedmarker\n")
				mkBSLFile(t, root, collisionObjectRel, "// basemarker\n")
				idx := buildCollisionIndex(t, root)
				directKey, nestedKey := "ext."+name, "ext."+nestedName
				if nestedName == name {
					directKey += "@/" + direct
					nestedKey += "@/" + prefix
				}
				requireCollisionBodies(t, idx, map[string]string{
					collisionBaseKey:                   "// basemarker\n",
					directKey + "." + collisionBaseKey: "// directmarker\n",
					nestedKey + "." + collisionBaseKey: "// nestedmarker\n",
				})
				if got := idx.WrappedPaths(); got != (WrappedPathState{Total: 3}) {
					t.Errorf("wraps = %+v, want all three paths accounted for", got)
				}
			})
		}
	}
}

func TestSameNameSiblingsUseTheWholeEscapedRoot(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		"a.b":           "a%2Eb",
		"a%2Eb":         "a%252Eb",
		"a@b":           "a%40b",
		"W/a":           "W/a",
		"W/b":           "W/b",
		"one/Копии/src": "one/Копии/src",
		"two/Копии/src": "two/Копии/src",
	}
	want := make(map[string]string, len(fixtures)+1)
	for path, escaped := range fixtures {
		mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(path)), extManifestClassic, "Одно")
		body := "// " + escaped + "\n"
		mkBSLFile(t, root, path+"/"+collisionObjectRel, body)
		want["ext.Одно@/"+escaped+"."+collisionBaseKey] = body
	}
	mkBSLFile(t, root, "one/Копии/src-copy/"+collisionObjectRel, "// wrapped base\n")
	want[collisionBaseKey] = "// wrapped base\n"
	idx := buildCollisionIndex(t, root)
	requireCollisionBodies(t, idx, want)
	if got := idx.WrappedPaths(); got != (WrappedPathState{Files: 1, Total: len(want)}) {
		t.Errorf("wraps = %+v, want only the wrapped base", got)
	}
	dirs := make([]string, 0, len(fixtures))
	for path := range fixtures {
		dirs = append(dirs, path)
	}
	slices.Sort(dirs)
	if got := idx.ExtensionLayout().Dirs; !slices.Equal(got, dirs) {
		t.Errorf("extension roots = %v, want %v", got, dirs)
	}
}

func TestSameNameSelfAndNestedExtension(t *testing.T) {
	const name, configModuleRel = "Своё", collisionObjectRel
	for _, prefix := range []string{"nested", "Обёртка/внутри"} {
		t.Run(prefix, func(t *testing.T) {
			root := t.TempDir()
			mkExtensionDump(t, root, extManifestClassic, name)
			mkBSLFile(t, root, configModuleRel, "// rootmarker\n")
			mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(prefix)), extManifestClassic, name)
			mkBSLFile(t, root, prefix+"/"+configModuleRel, "// nestedmarker\n")
			requireCollisionBodies(t, buildCollisionIndex(t, root), map[string]string{
				"ext.Своё@/." + collisionBaseKey:               "// rootmarker\n",
				"ext.Своё@/" + prefix + "." + collisionBaseKey: "// nestedmarker\n",
			})
		})
	}
}

func TestSameNameRootEscapingPreservesBytes(t *testing.T) {
	for path, want := range map[string]string{
		"":          "Name@/",
		"Копии/a b": "Name@/Копии/a%20b",
		"a\x60b":    "Name@/a%60b",
		"a\\b":      "Name@/a%5Cb",
		"Й":         "Name@/Й",
		"И\u0306":   "Name@/%D0%98%CC%86",
		"é":         "Name@/é",
		"e\u0301":   "Name@/e%CC%81",
		"\u115F":    "Name@/%E1%85%9F",
		"a\xff":     "Name@/a%FF",
	} {
		if got := qualifiedExtensionName("Name", path); got != want {
			t.Errorf("qualifying %q = %q, want %q", path, got, want)
		}
	}
}

func requireSameNameSearch(t *testing.T, idx *Index, want map[string]string) {
	t.Helper()
	requireCollisionBodies(t, idx, want)
	wantExt := make([]string, 0, len(want))
	for key := range want {
		if key != collisionBaseKey {
			wantExt = append(wantExt, key)
		}
	}
	slices.Sort(wantExt)
	for _, mode := range []SearchMode{SearchModeSmart, SearchModeExact, SearchModeRegex} {
		matches, _, err := idx.Search(SearchParams{
			Query: "commonmarker", Mode: mode, Namespace: "ext", Category: "Документ", Module: "МодульОбъекта",
		})
		got := make([]string, 0, len(matches))
		for _, match := range matches {
			got = append(got, match.Module)
		}
		slices.Sort(got)
		if err != nil || !slices.Equal(got, wantExt) {
			t.Errorf("%s filtered search = %v, %v, want %v", mode, got, err, wantExt)
		}
	}
}

func TestSameNameDirectFlatRekeysBeforeMutatingShards(t *testing.T) {
	const firstBody, secondBody = "// commonmarker firstmarker\n", "// commonmarker secondmarker\n"
	for _, lastManifest := range []bool{true, false} {
		testName := "one root remains"
		if !lastManifest {
			testName = "no manifest root remains"
		}
		t.Run(testName, func(t *testing.T) {
			root, cache := t.TempDir(), t.TempDir()
			mkExtensionDump(t, filepath.Join(root, "a"), extManifestClassic, "Legacy")
			mkBSLFile(t, root, "a/"+collisionObjectRel, firstBody)
			single := map[string]string{"ext.Legacy." + collisionBaseKey: firstBody}
			double := map[string]string{"ext.Legacy@/a." + collisionBaseKey: firstBody, "ext.Legacy@/b." + collisionBaseKey: secondBody}
			open := func(cacheDir string, want map[string]string) {
				idx := migOpen(t, root, cacheDir, false)
				defer idx.Close()
				requireSameNameSearch(t, idx, want)
			}
			open(cache, single)
			cpath := migCachePath(t, root, cache)
			mkExtensionDump(t, filepath.Join(root, "b"), extManifestClassic, "Legacy")
			mkBSLFile(t, root, "b/"+collisionObjectRel, secondBody)
			open(cache, double)
			stored, err := LoadManifest(cpath)
			if err != nil || stored == nil || !slices.Equal(docIDsFromManifest(stored), []string{"ext.Legacy." + collisionBaseKey}) {
				t.Fatalf("adding a root changed the original flat manifest: %+v, %v", stored, err)
			}

			doubleCache := t.TempDir()
			open(doubleCache, double)
			if err := os.RemoveAll(filepath.Join(root, "b")); err != nil {
				t.Fatal(err)
			}
			if !lastManifest {
				if err := os.Remove(filepath.Join(root, "a", extManifestClassic)); err != nil {
					t.Fatal(err)
				}
				single = map[string]string{collisionBaseKey: firstBody}
			}
			open(doubleCache, single)
			doubleStored, err := LoadManifest(migCachePath(t, root, doubleCache))
			if err != nil || doubleStored == nil || len(doubleStored.Files) != 2 ||
				doubleStored.Files["a/"+collisionObjectRel].DocID != "ext.Legacy@/a."+collisionBaseKey ||
				doubleStored.Files["b/"+collisionObjectRel].DocID != "ext.Legacy@/b."+collisionBaseKey {
				t.Fatalf("removing a root changed the original flat manifest: %+v, %v", doubleStored, err)
			}
		})
	}
}

func openSameNameGeneration(t *testing.T, root, cache, sig string) *Index {
	t.Helper()
	idx, err := OpenGenerationReadOnly(root, cache, sig)
	if err != nil {
		t.Fatal(err)
	}
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		idx.Close()
		t.Fatal(err)
	}
	return idx
}

func TestSameNameFlatWithForeignOrMissingStamp(t *testing.T) {
	const first, second = "wrap/exports/a", "wrap/exports/b"
	const baseBody, firstBody, secondBody = "// commonmarker basemarker\n", "// commonmarker firstmarker\n", "// commonmarker secondmarker\n"
	want := map[string]string{
		collisionBaseKey: baseBody,
		"ext.Legacy@/" + first + "." + collisionBaseKey:  firstBody,
		"ext.Legacy@/" + second + "." + collisionBaseKey: secondBody,
	}
	const baseStem = "Документ.ПеремещениеЗапасов@/"
	const encodedRel = "Documents/ПеремещениеЗапасов/Ext/ObjectModule%2Ebsl.МодульОбъекта"
	oldIDs := map[string]string{
		collisionObjectRel:                baseStem + encodedRel,
		first + "/" + collisionObjectRel:  baseStem + first + "/" + encodedRel,
		second + "/" + collisionObjectRel: baseStem + second + "/" + encodedRel,
	}
	for _, stamp := range []string{"v9", "missing"} {
		t.Run(stamp, func(t *testing.T) {
			root, cache := t.TempDir(), t.TempDir()
			mkBSLFile(t, root, collisionObjectRel, baseBody)
			mkBSLFile(t, root, first+"/"+collisionObjectRel, firstBody)
			mkBSLFile(t, root, second+"/"+collisionObjectRel, secondBody)
			// Build real shards with the base namespace IDs, then give the
			// cache a foreign schema stamp or remove the manifest.
			if err := BuildCache(root, cache, false); err != nil {
				t.Fatal(err)
			}
			cpath := migCachePath(t, root, cache)
			stored, err := LoadManifest(cpath)
			if err != nil || stored == nil || len(stored.Files) != len(oldIDs) {
				t.Fatalf("initial flat manifest = %+v, %v", stored, err)
			}
			for path, wantID := range oldIDs {
				if got := stored.Files[path].DocID; got != wantID {
					t.Fatalf("initial document ID for %q = %q, want %q", path, got, wantID)
				}
			}
			if stamp == "missing" {
				if err := os.Remove(manifestPath(cpath)); err != nil {
					t.Fatal(err)
				}
			} else {
				stored.SchemaVersion = 9
				if err := stored.Save(cpath); err != nil {
					t.Fatal(err)
				}
			}
			mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(first)), extManifestClassic, "Legacy")
			mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(second)), extManifestClassic, "Legacy")
			shardDirs := cacheShardDirs(cpath)
			before := snapshotShardTree(t, shardDirs)

			idx := migOpen(t, root, cache, false)
			requireSameNameSearch(t, idx, want)
			idx.Close()

			if err := BuildCache(root, cache, false); err != nil {
				t.Fatal(err)
			}
			ready := openSameNameGeneration(t, root, cache, mustGenSig(t, root))
			defer ready.Close()
			requireSameNameSearch(t, ready, want)
			after, err := LoadManifest(cpath)
			if stamp == "missing" {
				if err != nil || after != nil {
					t.Errorf("manifest written over unstamped shards: %+v, %v", after, err)
				}
			} else if err != nil || after == nil || after.schemaVersion() != 9 ||
				!maps.Equal(after.Files, stored.Files) {
				t.Errorf("original v9 manifest changed: %+v, %v", after, err)
			}
			if got := snapshotShardTree(t, cacheShardDirs(cpath)); !sameSnapshot(got, before) {
				t.Errorf("old shard files changed: %v, want %v", got, before)
			}
			shards, err := openCachedShards(shardDirs, true, "1s")
			if err != nil {
				t.Fatal(err)
			}
			for _, shard := range shards {
				defer shard.Close()
			}
			for _, id := range oldIDs {
				shard := shards[shardForID(id, len(shards))]
				if doc, err := shard.Document(id); err != nil || doc == nil {
					t.Errorf("old shard cannot read its original docID %q: %v", id, err)
				}
			}
		})
	}
}

func TestSameNameXMLOnlyEditRefreshesDeepOwnership(t *testing.T) {
	const first, second = "wrap/exports/a", "wrap/exports/b"
	const firstBody, secondBody = "// commonmarker firstmarker\n", "// commonmarker secondmarker\n"
	root, cache := t.TempDir(), t.TempDir()
	mkExtensionDump(t, filepath.Join(root, filepath.FromSlash(first)), extManifestClassic, "Legacy")
	mkBSLFile(t, root, first+"/"+collisionObjectRel, firstBody)
	mkBSLFile(t, root, second+"/"+collisionObjectRel, secondBody)
	oldSig := mustGenSig(t, root)
	idx := openReloadableIndex(t, root, cache)
	single := map[string]string{"ext.Legacy." + collisionBaseKey: firstBody, collisionBaseKey: secondBody}
	double := map[string]string{"ext.Legacy@/" + first + "." + collisionBaseKey: firstBody, "ext.Legacy@/" + second + "." + collisionBaseKey: secondBody}
	check := func(idx *Index, bodies map[string]string, wraps int, dirs ...string) {
		t.Helper()
		requireSameNameSearch(t, idx, bodies)
		if got := idx.WrappedPaths(); got != (WrappedPathState{Files: wraps, Total: len(bodies)}) {
			t.Errorf("wrapped paths = %+v, expected %d wraps", got, wraps)
		}
		if got := idx.ExtensionLayout().Dirs; !slices.Equal(got, dirs) {
			t.Errorf("recognized roots = %v, want %v", got, dirs)
		}
	}
	check(idx, single, 1, first)
	secondRoot := filepath.Join(root, filepath.FromSlash(second))
	mkExtensionDump(t, secondRoot, extManifestClassic, "Legacy")
	changedSig := mustGenSig(t, root)
	if rep := mustReload(t, idx); !rep.Changed || rep.SigBefore != oldSig || rep.SigAfter != changedSig || changedSig == oldSig {
		t.Fatalf("adding only the extension manifest did not change the generation: %+v", rep)
	}
	check(idx, double, 0, first, second)
	warm := openSameNameGeneration(t, root, cache, changedSig)
	check(warm, double, 0, first, second)
	warm.Close()
	if err := os.Remove(filepath.Join(secondRoot, extManifestClassic)); err != nil {
		t.Fatal(err)
	}
	if rep := mustReload(t, idx); !rep.Changed || rep.SigAfter != oldSig {
		t.Fatalf("removing only the extension manifest did not restore its original identity: %+v", rep)
	}
	check(idx, single, 1, first)
}

func TestSameNameQualifiedRootCanContainAWrappedPath(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	for _, dir := range []string{"a.b", "b"} {
		mkExtensionDump(t, filepath.Join(root, dir), extManifestClassic, "Legacy")
	}
	mkBSLFile(t, root, "a.b/extra/"+collisionObjectRel, "// wrapped body\n")
	mkBSLFile(t, root, "b/"+collisionObjectRel, "// regular body\n")
	sig := mustGenSig(t, root)
	if err := BuildGeneration(root, cache, sig); err != nil {
		t.Fatal(err)
	}

	// Current discovery would consume "extra"; the persisted owner does not.
	if err := os.Remove(filepath.Join(root, "a.b", extManifestClassic)); err != nil {
		t.Fatal(err)
	}
	mkExtensionDump(t, filepath.Join(root, "a.b", "extra"), extManifestClassic, "Legacy")
	idx := openSameNameGeneration(t, root, cache, sig)
	defer idx.Close()
	requireCollisionBodies(t, idx, map[string]string{
		"ext.Legacy@/a%2Eb." + collisionBaseKey: "// wrapped body\n",
		"ext.Legacy@/b." + collisionBaseKey:     "// regular body\n",
	})
	if got := idx.WrappedPaths(); got != (WrappedPathState{Files: 1, Total: 2}) {
		t.Errorf("wrapped paths = %+v, want one wrap below a qualified root", got)
	}
}

func TestSameNameMissingStampCanHoldQualifiedKeys(t *testing.T) {
	const first, second = "wrap/exports/a", "wrap/exports/b"
	const firstBody, secondBody = "// commonmarker firstmarker\n", "// commonmarker secondmarker\n"
	root, cache := t.TempDir(), t.TempDir()
	firstRoot := filepath.Join(root, filepath.FromSlash(first))
	secondRoot := filepath.Join(root, filepath.FromSlash(second))
	mkExtensionDump(t, firstRoot, extManifestClassic, "Legacy")
	mkExtensionDump(t, secondRoot, extManifestClassic, "Legacy")
	mkBSLFile(t, root, first+"/"+collisionObjectRel, firstBody)
	mkBSLFile(t, root, second+"/"+collisionObjectRel, secondBody)

	flat := migOpen(t, root, cache, false)
	requireSameNameSearch(t, flat, map[string]string{
		"ext.Legacy@/" + first + "." + collisionBaseKey:  firstBody,
		"ext.Legacy@/" + second + "." + collisionBaseKey: secondBody,
	})
	flat.Close()
	cpath := migCachePath(t, root, cache)
	shardDirs := cacheShardDirs(cpath)
	before := snapshotShardTree(t, shardDirs)
	if err := os.Remove(manifestPath(cpath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(secondRoot, extManifestClassic)); err != nil {
		t.Fatal(err)
	}

	idx := migOpen(t, root, cache, false)
	defer idx.Close()
	requireSameNameSearch(t, idx, map[string]string{
		"ext.Legacy." + collisionBaseKey: firstBody,
		collisionBaseKey:                 secondBody,
	})
	for _, mode := range []SearchMode{SearchModeSmart, SearchModeExact} {
		matches, _, err := idx.Search(SearchParams{Query: "secondmarker", Mode: mode})
		if err != nil || len(matches) != 1 || matches[0].Module != collisionBaseKey {
			t.Errorf("%s search for restored base module = %+v, %v, want %q", mode, matches, err, collisionBaseKey)
		}
	}
	if manifest, err := LoadManifest(cpath); err != nil || manifest != nil {
		t.Errorf("manifest written over the old qualified shards: %+v, %v", manifest, err)
	}
	if after := snapshotShardTree(t, cacheShardDirs(cpath)); !sameSnapshot(after, before) {
		t.Errorf("original qualified shard files changed: %v, want %v", after, before)
	}
}

func TestXMLOnlyRenameRekeysWarmFlatAndReady(t *testing.T) {
	root, flat, adopt, cache := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	direct := mkExtensionDump(t, filepath.Join(root, "direct"), extManifestClassic, "Direct")
	const body = "// commonmarker realmodule\n"
	mkBSLFile(t, direct, collisionObjectRel, body)
	bslPath := filepath.Join(direct, filepath.FromSlash(collisionObjectRel))
	xmlPath := filepath.Join(direct, extManifestClassic)
	fixed := time.Unix(1700000000, 0)
	if err := os.Chtimes(xmlPath, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	bslBefore, err := os.Stat(bslPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{flat, adopt} {
		if err := BuildCache(root, path, false); err != nil {
			t.Fatal(err)
		}
	}
	idx := openReloadableIndex(t, root, cache)
	oldSig := mustGenSig(t, root)
	oldBodies := map[string]string{"ext.Direct." + collisionBaseKey: body}
	newBodies := map[string]string{"ext.NextEx." + collisionBaseKey: body}
	requireSameNameSearch(t, idx, oldBodies)

	oldXML, err := os.Stat(xmlPath)
	if err != nil {
		t.Fatal(err)
	}
	mkExtensionDump(t, direct, extManifestClassic, "NextEx")
	if err := os.Chtimes(xmlPath, oldXML.ModTime(), oldXML.ModTime()); err != nil {
		t.Fatal(err)
	}
	newXML, err := os.Stat(xmlPath)
	if err != nil || newXML.Size() != oldXML.Size() || !newXML.ModTime().Equal(oldXML.ModTime()) {
		t.Fatalf("XML rename must preserve size and mtime: %+v, %v", newXML, err)
	}
	bslAfter, err := os.Stat(bslPath)
	if err != nil || stampOf(bslAfter) != stampOf(bslBefore) {
		t.Fatalf("BSL tuple changed: %+v, %v", bslAfter, err)
	}
	content, err := os.ReadFile(bslPath)
	if err != nil || string(content) != body {
		t.Fatalf("BSL bytes changed: %q, %v", content, err)
	}
	newSig := mustGenSig(t, root)
	if newSig == oldSig {
		t.Fatal("same-size XML rename with unchanged timestamps did not change the signature")
	}
	if err := BuildGeneration(root, t.TempDir(), oldSig); err == nil {
		t.Fatal("public builder accepted an obsolete caller-supplied signature")
	}

	warmFlat := migOpen(t, root, flat, false)
	requireSameNameSearch(t, warmFlat, newBodies)
	warmFlat.Close()
	oldFlat, err := LoadManifest(migCachePath(t, root, flat))
	if err != nil || oldFlat == nil || oldFlat.Files["direct/"+collisionObjectRel].DocID != "ext.Direct."+collisionBaseKey {
		t.Fatalf("writable warm rekey overwrote the previous flat manifest: %+v, %v", oldFlat, err)
	}

	migrated, err := OpenForServe(root, adopt)
	if err != nil {
		t.Fatal(err)
	}
	<-migrated.Done()
	if err := migrated.BuildError(); err != nil {
		t.Fatal(err)
	}
	requireSameNameSearch(t, migrated, newBodies)
	migrated.Close()
	stale, err := LoadManifest(migCachePath(t, root, adopt))
	if err != nil || stale == nil || !stale.sameRevision(oldFlat) {
		t.Fatalf("adoption moved the XML-stale flat manifest: %+v, %v", stale, err)
	}

	if rep := mustReload(t, idx); !rep.Changed || rep.SigBefore != oldSig || rep.SigAfter != newSig {
		t.Fatalf("XML-only reload did not publish the new ownership: %+v", rep)
	}
	requireSameNameSearch(t, idx, newBodies)
	warm, err := OpenForServe(root, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer warm.Close()
	<-warm.Done()
	if err := warm.BuildError(); err != nil {
		t.Fatal(err)
	}
	requireSameNameSearch(t, warm, newBodies)
	cold := buildCollisionIndex(t, root)
	requireSameNameSearch(t, cold, newBodies)

	// Metadata XML unrelated to ownership cannot invalidate the cache.
	writeFileOrFatal(t, filepath.Join(direct, "ConfigDumpInfo.xml"), "<dump/>")
	if got := mustGenSig(t, root); got != newSig {
		t.Fatalf("unrelated XML changed ownership signature: %s -> %s", newSig, got)
	}
	sameOwnerXML, err := os.ReadFile(xmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xmlPath, append(sameOwnerXML, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustGenSig(t, root); got != newSig {
		t.Fatal("irrelevant manifest formatting changed the ownership signature")
	}
	if err := os.Remove(xmlPath); err != nil {
		t.Fatal(err)
	}
	noOwner := mustGenSig(t, root)
	if err := os.Mkdir(xmlPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := mustGenSig(t, root); got != noOwner || got == newSig {
		t.Fatal("a nonregular manifest was treated as an extension owner")
	}
}
