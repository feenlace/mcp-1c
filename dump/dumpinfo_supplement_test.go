package dump

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func readSupplementDoc(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestDumpInfoSupplementPreserveExisting(t *testing.T) {
	cpath := t.TempDir()
	writeDumpInfo(cpath, cpath, 1, time.Second)
	path := filepath.Join(cpath, "dump.json")
	raw, _ := os.ReadFile(path)
	raw = append(raw[:len(raw)-1], []byte(",\n  \"legacy_cache_dir\": \"old\",\n  \"legacy_cache_bytes\": 123\n}")...)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	writeDumpInfo(cpath, cpath, 2, time.Second)
	doc := readSupplementDoc(t, path)
	if string(doc["legacy_cache_dir"]) != `"old"` || string(doc["legacy_cache_bytes"]) != "123" {
		t.Fatalf("legacy fields lost: %s", doc)
	}
}

func TestDumpInfoSupplementBlockingOpenRestoresMapping(t *testing.T) {
	dir, cache := fixtureDump(t), t.TempDir()
	sig := mustGenSig(t, dir)
	if err := BuildGeneration(dir, cache, sig); err != nil {
		t.Fatal(err)
	}
	idx, err := OpenGenerationReadOnly(dir, cache, sig)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	<-idx.Done()
	if !idx.Ready() {
		t.Fatal(idx.BuildError())
	}
	cpath, _ := cachePath(dir, cache)
	doc := readSupplementDoc(t, filepath.Join(cpath, "dump.json"))
	if string(doc["modules"]) != "2" {
		t.Fatalf("count: %s", doc["modules"])
	}
}

func TestDumpInfoSupplementLifecycle(t *testing.T) {
	for _, mode := range []string{"blocking", "placeholder", "warm"} {
		t.Run(mode, func(t *testing.T) {
			dir, cache := fixtureDump(t), t.TempDir()
			cpath, _ := cachePath(dir, cache)
			path := filepath.Join(cpath, "dump.json")
			if err := BuildCache(dir, cache, false); err != nil {
				t.Fatal(err)
			}
			m, err := OpenDumpInfoMetadata(dir, cache)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			value := json.RawMessage(`"old-cache"`)
			if err := m.Update(DumpInfoField{"legacy_cache_dir", value}, DumpInfoField{"legacy_cache_bytes", json.RawMessage("123")}); err != nil {
				t.Fatal(err)
			}
			copy(value, `"mutated!!"`)
			var idx *Index
			if mode == "placeholder" {
				gen, err := PrepareServeGeneration(context.Background(), dir, cache, false)
				if err != nil {
					t.Fatal(err)
				}
				idx = NewServePlaceholder(dir)
				idx.FinishServeOpen(cache, gen, nil)
			} else if mode == "blocking" {
				idx, err = OpenForServe(dir, cache)
			} else {
				idx, err = NewIndex(dir, cache, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer idx.Close()
			<-idx.Done()
			if !idx.Ready() {
				t.Fatal(idx.BuildError())
			}
			assertExtras := func(modules int) {
				t.Helper()
				doc := readSupplementDoc(t, path)
				if string(doc["modules"]) != fmt.Sprint(modules) || string(doc["legacy_cache_dir"]) != `"old-cache"` || string(doc["legacy_cache_bytes"]) != "123" {
					t.Fatalf("metadata: %s", doc)
				}
				raw, _ := os.ReadFile(path)
				if strings.Index(string(raw), `"legacy_cache_dir"`) > strings.Index(string(raw), `"legacy_cache_bytes"`) {
					t.Fatal("extra ordering changed")
				}
			}
			assertExtras(2)
			// Index retention alone must be sufficient after the consumer closes.
			m.Close()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			mkBSLFile(t, dir, "CommonModules/Added/Ext/Module.bsl", "Процедура Added()\nКонецПроцедуры\n")
			rep := mustReload(t, idx)
			if !rep.Changed || rep.ModulesAfter != 3 {
				t.Fatalf("reload: %+v", rep)
			}
			assertExtras(3)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if mustReload(t, idx).Changed {
				t.Fatal("no-op rebuilt")
			}
			assertExtras(3)
			// A replacement serving Index joins the SAME cache state, not genDir.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			replacement, err := OpenForServe(dir, cache)
			if err != nil {
				t.Fatal(err)
			}
			<-replacement.Done()
			if !replacement.Ready() {
				t.Fatal(replacement.BuildError())
			}
			assertExtras(3)
			replacement.Close()
			prior, _ := os.ReadFile(path)
			if err := os.Rename(dir, dir+"-gone"); err != nil {
				t.Fatal(err)
			}
			_, reloadErr := idx.Reload()
			if err := os.Rename(dir+"-gone", dir); err != nil {
				t.Fatal(err)
			}
			if reloadErr == nil {
				t.Fatal("missing dump reload succeeded")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(prior, after) {
				t.Fatal("failed reload modified prior bytes")
			}
		})
	}
}

func TestDumpInfoSupplementDelayedUpdateAndReaders(t *testing.T) {
	prev := BuildVersion
	BuildVersion = "dev-supplement-consumer"
	t.Cleanup(func() { BuildVersion = prev })
	dir, cache := fixtureDump(t), t.TempDir()
	idx := openReloadableIndex(t, dir, cache)
	m, err := OpenDumpInfoMetadata(dir, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	cpath, _ := cachePath(dir, cache)
	path := filepath.Join(cpath, "dump.json")
	// Startup scans are outside Core locks and finish after a later Reload.
	scanned, release, updated := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		close(scanned)
		<-release
		updated <- m.Update(DumpInfoField{"legacy_cache_dir", json.RawMessage(`"delayed"`)})
	}()
	<-scanned
	mkBSLFile(t, dir, "CommonModules/Newer/Ext/Module.bsl", "Процедура Newer()\nКонецПроцедуры\n")
	mustReload(t, idx)
	current := readSupplementDoc(t, path)
	if string(current["mcp_1c_version"]) != `"dev-supplement-consumer"` {
		t.Fatal("consumer version missing")
	}
	close(release)
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	after := readSupplementDoc(t, path)
	for name, value := range current {
		if reservedDumpInfoName(name) && !bytes.Equal(value, after[name]) {
			t.Fatalf("delayed update overwrote %s", name)
		}
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 500; i++ {
			raw, err := os.ReadFile(path)
			if err != nil {
				done <- err
				return
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(raw, &doc); err != nil {
				done <- err
				return
			}
			if string(doc["modules"]) != "3" || string(doc["schema"]) != "1" {
				done <- fmt.Errorf("partial/stale metadata: %s", raw)
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 30; i++ {
		if err := m.Update(DumpInfoField{"legacy_cache_bytes", json.RawMessage(fmt.Sprint(i))}); err != nil {
			t.Fatal(err)
		}
		if mustReload(t, idx).Changed {
			t.Fatal("unexpected change")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDumpInfoSupplementValidationAndFailure(t *testing.T) {
	dir, cache := t.TempDir(), t.TempDir()
	m, err := OpenDumpInfoMetadata(dir, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	cpath, _ := cachePath(dir, cache)
	path := filepath.Join(cpath, "dump.json")
	if err := m.Update(DumpInfoField{"extra", json.RawMessage("1")}); !errors.Is(err, ErrDumpInfoUnavailable) {
		t.Fatalf("missing standard: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("extra-only document created")
	}
	if err := os.MkdirAll(cpath, 0755); err != nil {
		t.Fatal(err)
	}
	writeDumpInfo(cpath, dir, 1, time.Second)
	prior, _ := os.ReadFile(path)
	for _, fields := range [][]DumpInfoField{
		{{"modules", json.RawMessage("99")}}, {{"mcp_1c_version", json.RawMessage(`"old"`)}},
		{{"x", json.RawMessage("{")}}, {{"x", json.RawMessage("1")}, {"x", json.RawMessage("2")}},
		{{"", json.RawMessage("1")}},
		{{string([]byte{0xff}), json.RawMessage("1")}}, {{"x", json.RawMessage(`"` + strings.Repeat("a", maxDumpInfoExtrasBytes) + `"`)}},
	} {
		if err := m.Update(fields...); err == nil {
			t.Fatalf("accepted invalid fields: %v", fields)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(prior, after) {
			t.Fatal("invalid update changed bytes")
		}
	}
	for _, bad := range [][]byte{[]byte(`{"schema":1}`), []byte(`{"schema":1,"schema":1}`), []byte("null"), []byte(strings.Repeat(" ", maxDumpInfoBytes+1))} {
		if err := os.WriteFile(path, bad, 0644); err != nil {
			t.Fatal(err)
		}
		if err := m.Update(DumpInfoField{"extra", json.RawMessage("1")}); !errors.Is(err, ErrDumpInfoUnavailable) {
			t.Fatalf("malformed mapping: %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(bad, after) {
			t.Fatal("malformed prior bytes replaced by Update")
		}
	}
	if err := os.WriteFile(path, prior, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cpath, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(cpath, 0755)
	// Only claim write-failure preservation if this process actually lacks write.
	probe, err := os.CreateTemp(cpath, "probe")
	if err == nil {
		probe.Close()
		os.Remove(probe.Name())
		t.Skip("process can write mode-0555 directory")
	}
	if err := m.Update(DumpInfoField{"extra", json.RawMessage("1")}); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	writeDumpInfo(cpath, dir, 99, time.Second)
	after, _ := os.ReadFile(path)
	if !bytes.Equal(prior, after) {
		t.Fatal("failed publication changed prior bytes")
	}
	if err := os.Chmod(cpath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeDumpInfo(cpath, dir, 1, time.Second)
	if _, ok := readSupplementDoc(t, path)["extra"]; ok {
		t.Fatal("failed update retained unpublished value")
	}
}

func TestDumpInfoSupplementAliasesIndependenceAndRetention(t *testing.T) {
	dir, cache, other := t.TempDir(), t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "cache-alias")
	if err := os.Symlink(cache, alias); err != nil {
		t.Fatal(err)
	}
	m, err := OpenDumpInfoMetadata(dir, cache)
	if err != nil {
		t.Fatal(err)
	}
	key := m.state.path
	second, err := OpenDumpInfoMetadata(dir, alias)
	if err != nil {
		t.Fatal(err)
	}
	if m.state != second.state {
		t.Fatal("alias uses different state")
	}
	separate, err := OpenDumpInfoMetadata(dir, other)
	if err != nil {
		t.Fatal(err)
	}
	defer separate.Close()
	if m.state == separate.state {
		t.Fatal("different base shares state")
	}
	cpath, _ := cachePath(dir, cache)
	os.MkdirAll(cpath, 0755)
	writeDumpInfo(cpath, dir, 1, time.Second)
	// A locked path must not hold the registry lock or impede a different base.
	m.state.mu.Lock()
	complete := make(chan error, 1)
	go func() {
		p, _ := cachePath(dir, other)
		err := os.MkdirAll(p, 0755)
		if err == nil {
			writeDumpInfo(p, dir, 2, time.Second)
			err = separate.Update(DumpInfoField{"independent", json.RawMessage("true")})
		}
		complete <- err
	}()
	select {
	case err := <-complete:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		m.state.mu.Unlock()
		t.Fatal("different cache blocked")
	}
	m.state.mu.Unlock()
	if err := second.Update(DumpInfoField{"alias", json.RawMessage("true")}); err != nil {
		t.Fatal(err)
	}
	if string(readSupplementDoc(t, filepath.Join(cpath, "dump.json"))["alias"]) != "true" {
		t.Fatal("alias update lost")
	}
	m.Close()
	second.Close()
	second.Close()
	if err := second.Update(DumpInfoField{"closed", json.RawMessage("true")}); err == nil {
		t.Fatal("closed handle accepted update")
	}
	dumpInfoRegistry.Lock()
	_, retained := dumpInfoRegistry.paths[key]
	dumpInfoRegistry.Unlock()
	if retained {
		t.Fatal("last Close retained state")
	}
}

func TestDumpInfoSupplementRemoveContract(t *testing.T) {
	dir, cache := fixtureDump(t), t.TempDir()
	idx := openReloadableIndex(t, dir, cache)
	m, err := OpenDumpInfoMetadata(dir, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// A runtime interface keeps the red test executable before the API exists.
	remover, ok := any(m).(interface{ Remove(...string) error })
	if !ok {
		t.Fatal("supported supplementary removal API is missing; absent legacy tree leaves stale fields")
	}
	cpath, _ := cachePath(dir, cache)
	path := filepath.Join(cpath, "dump.json")
	if err := m.Update(DumpInfoField{"legacy_cache_dir", json.RawMessage(`"old"`)}, DumpInfoField{"legacy_cache_bytes", json.RawMessage("0")}, DumpInfoField{"unrelated", json.RawMessage("true")}); err != nil {
		t.Fatal(err)
	}
	prior := readSupplementDoc(t, path)
	mkBSLFile(t, dir, "CommonModules/NewCount/Ext/Module.bsl", "Процедура NewCount()\nКонецПроцедуры\n")
	mustReload(t, idx)
	latest := readSupplementDoc(t, path)
	if string(prior["modules"]) == string(latest["modules"]) {
		t.Fatal("no newer standard snapshot")
	}
	if err := remover.Remove("legacy_cache_dir", "legacy_cache_bytes"); err != nil {
		t.Fatal(err)
	}
	after := readSupplementDoc(t, path)
	for name, value := range latest {
		if reservedDumpInfoName(name) && !bytes.Equal(value, after[name]) {
			t.Fatalf("Remove overwrote current %s", name)
		}
	}
	for _, name := range []string{"legacy_cache_dir", "legacy_cache_bytes"} {
		if _, ok := after[name]; ok {
			t.Fatalf("removed field retained: %s", name)
		}
	}
	if string(after["unrelated"]) != "true" {
		t.Fatal("Remove discarded unrelated extra")
	}
	priorBytes, _ := os.ReadFile(path)
	for _, names := range [][]string{{"modules"}, {"mcp_1c_version"}, {""}, {"x", "x"}, {string([]byte{0xff})}, {strings.Repeat("a", 257)}} {
		if err := remover.Remove(names...); err == nil {
			t.Fatalf("invalid removal accepted: %v", names)
		}
		raw, _ := os.ReadFile(path)
		if !bytes.Equal(raw, priorBytes) {
			t.Fatal("invalid removal changed bytes")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := remover.Remove("unrelated"); !errors.Is(err, ErrDumpInfoUnavailable) {
		t.Fatalf("missing standard: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Remove created extra-only JSON")
	}
	mustReload(t, idx)
	restored := readSupplementDoc(t, path)
	for _, name := range []string{"legacy_cache_dir", "legacy_cache_bytes"} {
		if _, ok := restored[name]; ok {
			t.Fatalf("deleted mapping Reload resurrected %s", name)
		}
	}
	if string(restored["unrelated"]) != "true" {
		t.Fatal("failed missing-file Remove altered retained state")
	}
	mkBSLFile(t, dir, "CommonModules/AfterRemove/Ext/Module.bsl", "Процедура AfterRemove()\nКонецПроцедуры\n")
	mustReload(t, idx)
	if _, ok := readSupplementDoc(t, path)["legacy_cache_dir"]; ok {
		t.Fatal("changed Reload resurrected removed legacy metadata")
	}
	if err := m.Update(DumpInfoField{"legacy_cache_dir", json.RawMessage(`"empty-present"`)}, DumpInfoField{"legacy_cache_bytes", json.RawMessage("0")}); err != nil {
		t.Fatal(err)
	}
	if string(readSupplementDoc(t, path)["legacy_cache_bytes"]) != "0" {
		t.Fatal("present empty legacy tree must retain explicit zero")
	}
}

func TestDumpInfoSupplementRemoveWriteFailure(t *testing.T) {
	dir, cache := t.TempDir(), t.TempDir()
	m, err := OpenDumpInfoMetadata(dir, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	cpath, _ := cachePath(dir, cache)
	path := filepath.Join(cpath, "dump.json")
	if err := os.MkdirAll(cpath, 0755); err != nil {
		t.Fatal(err)
	}
	writeDumpInfo(cpath, dir, 1, time.Second)
	if err := m.Update(DumpInfoField{"keep", json.RawMessage(`"prior"`)}); err != nil {
		t.Fatal(err)
	}
	prior, _ := os.ReadFile(path)
	if err := os.Chmod(cpath, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(cpath, 0755)
	probe, err := os.CreateTemp(cpath, "probe")
	if err == nil {
		probe.Close()
		os.Remove(probe.Name())
		t.Skip("process can write mode-0555 directory")
	}
	if err := m.Remove("keep"); err == nil {
		t.Fatal("removal write unexpectedly succeeded")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(prior, after) {
		t.Fatal("failed removal changed prior bytes")
	}
	if err := os.Chmod(cpath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeDumpInfo(cpath, dir, 2, time.Second)
	if string(readSupplementDoc(t, path)["keep"]) != `"prior"` {
		t.Fatal("failed removal changed retained state")
	}
	m.Close()
	if err := m.Remove("keep"); err == nil {
		t.Fatal("closed handle accepted removal")
	}
}

func TestDumpInfoSupplementCaseAliasesBeforeHashExists(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	base, alias := filepath.Join(root, "CaseCache"), filepath.Join(root, "casecache")
	if err := os.Mkdir(base, 0755); err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(alias)
	if os.IsNotExist(err) {
		t.Skip("host cache fixture filesystem distinguishes case")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(firstInfo, aliasInfo) {
		t.Skip("case names are different directories on this filesystem")
	}
	one, err := OpenDumpInfoMetadata(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := OpenDumpInfoMetadata(dir, alias)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	cpath, _ := cachePath(dir, base)
	if _, err := os.Stat(cpath); !os.IsNotExist(err) {
		t.Fatalf("hash should still be missing: %v", err)
	}
	if err := os.Mkdir(cpath, 0755); err != nil {
		t.Fatal(err)
	}
	writeDumpInfo(cpath, dir, 1, time.Second)
	if err := one.Update(DumpInfoField{"owned", json.RawMessage("1")}); err != nil {
		t.Fatal(err)
	}
	if err := two.Remove("owned"); err != nil {
		t.Fatal(err)
	}
	if err := one.Update(DumpInfoField{"unrelated", json.RawMessage("2")}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cpath, "dump.json")
	if _, ok := readSupplementDoc(t, path)["owned"]; ok {
		t.Fatal("case alias split resurrected removed field")
	}
	if one.state != two.state {
		t.Fatal("same physical cache has different retained states")
	}
	done := make(chan error, 2)
	for _, handle := range []*DumpInfoMetadata{one, two} {
		go func(m *DumpInfoMetadata) {
			for i := 0; i < 40; i++ {
				if err := m.Update(DumpInfoField{"transient", json.RawMessage("true")}); err != nil {
					done <- err
					return
				}
				if err := m.Remove("transient"); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(handle)
	}
	for i := 0; i < 300; i++ {
		doc := readSupplementDoc(t, path)
		if string(doc["schema"]) != "1" || string(doc["modules"]) != "1" || string(doc["unrelated"]) != "2" {
			t.Fatalf("incomplete concurrent alias doc: %s", doc)
		}
		if _, ok := doc["owned"]; ok {
			t.Fatal("concurrent aliases resurrected removed field")
		}
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if err := one.Update(DumpInfoField{"after_concurrency", json.RawMessage("true")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSupplementDoc(t, path)["transient"]; ok {
		t.Fatal("concurrent removal was not retained")
	}
}

func TestDumpInfoSupplementForeignMapping(t *testing.T) {
	dir, foreign, cache := fixtureDump(t), t.TempDir(), t.TempDir()
	cpath, _ := cachePath(dir, cache)
	if err := os.MkdirAll(cpath, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cpath, "dump.json")
	// A valid foreign schema must not be treated as owner metadata.
	writeDumpInfo(cpath, dir, 2, time.Second)
	doc := readSupplementDoc(t, path)
	foreignJSON, _ := json.Marshal(foreign)
	doc["dump_path"] = foreignJSON
	doc["foreign_extra"] = json.RawMessage("true")
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	m, err := OpenDumpInfoMetadata(dir, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Update(DumpInfoField{"own", json.RawMessage("1")}); !errors.Is(err, ErrDumpInfoUnavailable) {
		t.Fatalf("foreign standard mapping accepted: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(raw, after) {
		t.Fatal("foreign mapping Update changed prior bytes")
	}
	if err := m.Remove("foreign_extra"); !errors.Is(err, ErrDumpInfoUnavailable) {
		t.Fatalf("foreign standard removal accepted: %v", err)
	}
	if err := BuildCache(dir, cache, false); err != nil {
		t.Fatal(err)
	}
	own := readSupplementDoc(t, path)
	if _, ok := own["foreign_extra"]; ok {
		t.Fatal("real owner publication imported foreign extras")
	}
	if !bytes.Equal(own["dump_path"], json.RawMessage(strconv.Quote(dir))) {
		t.Fatalf("wrong owner: %s", own["dump_path"])
	}
	if err := m.Update(DumpInfoField{"owned", json.RawMessage("7")}); err != nil {
		t.Fatal(err)
	}
	doc = readSupplementDoc(t, path)
	doc["dump_path"] = foreignJSON
	doc["foreign_extra"] = json.RawMessage("true")
	raw, _ = json.Marshal(doc)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := BuildCache(dir, cache, false); err != nil {
		t.Fatal(err)
	}
	own = readSupplementDoc(t, path)
	if _, ok := own["foreign_extra"]; ok {
		t.Fatal("foreign replacement polluted retained owner extras")
	}
	if string(own["owned"]) != "7" {
		t.Fatal("owner-retained extras lost")
	}
}

func TestDumpInfoSupplementAbsentBaseAndCreatedBetweenRegistrations(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	base, alias := filepath.Join(root, "InitiallyMissing"), filepath.Join(root, "initiallymissing")
	one, err := OpenDumpInfoMetadata(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	cpath, _ := cachePath(dir, base)
	if _, err := os.Stat(cpath); !os.IsNotExist(err) {
		t.Fatalf("registration created hash directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cpath, "dump.json")); !os.IsNotExist(err) {
		t.Fatalf("registration created mapping: %v", err)
	}
	// Base creation happens during registration, so later registrations observe a
	// physical directory rather than guessing how missing case names resolve.
	baseInfo, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	two, err := OpenDumpInfoMetadata(dir, alias)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	aliasInfo, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	same := os.SameFile(baseInfo, aliasInfo)
	if (one.state == two.state) != same {
		t.Fatalf("state sharing differs from real SameFile: same=%v", same)
	}
	if err := os.Mkdir(cpath, 0755); err != nil {
		t.Fatal(err)
	}
	writeDumpInfo(cpath, dir, 1, time.Second)
	if err := one.Update(DumpInfoField{"owned", json.RawMessage("1")}); err != nil {
		t.Fatal(err)
	}
	if same {
		if err := two.Remove("owned"); err != nil {
			t.Fatal(err)
		}
		if err := one.Update(DumpInfoField{"unrelated", json.RawMessage("2")}); err != nil {
			t.Fatal(err)
		}
		if _, ok := readSupplementDoc(t, filepath.Join(cpath, "dump.json"))["owned"]; ok {
			t.Fatal("newly created base alias resurrected removed field")
		}
	} else {
		otherPath, _ := cachePath(dir, alias)
		if err := os.Mkdir(otherPath, 0755); err != nil {
			t.Fatal(err)
		}
		writeDumpInfo(otherPath, dir, 2, time.Second)
		if err := two.Update(DumpInfoField{"separate", json.RawMessage("2")}); err != nil {
			t.Fatal(err)
		}
		if _, ok := readSupplementDoc(t, filepath.Join(cpath, "dump.json"))["separate"]; ok {
			t.Fatal("distinct created bases share retained values")
		}
	}
}

func TestDumpInfoSupplementCaseSensitiveBaseIndependence(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	base, other := filepath.Join(root, "DistinctCase"), filepath.Join(root, "distinctcase")
	if err := os.Mkdir(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(a, b) {
		t.Skip("host fixture filesystem aliases case; cannot prove separate case-sensitive directories")
	}
	one, err := OpenDumpInfoMetadata(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := OpenDumpInfoMetadata(dir, other)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	if one.state == two.state {
		t.Fatal("distinct case-sensitive directories share state")
	}
	cp1, _ := cachePath(dir, base)
	cp2, _ := cachePath(dir, other)
	if err := os.Mkdir(cp1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cp2, 0755); err != nil {
		t.Fatal(err)
	}
	writeDumpInfo(cp1, dir, 1, time.Second)
	writeDumpInfo(cp2, dir, 2, time.Second)
	if err := one.Update(DumpInfoField{"own", json.RawMessage("1")}); err != nil {
		t.Fatal(err)
	}
	if err := two.Remove("own"); err != nil {
		t.Fatal(err)
	}
	first, second := readSupplementDoc(t, filepath.Join(cp1, "dump.json")), readSupplementDoc(t, filepath.Join(cp2, "dump.json"))
	if string(first["own"]) != "1" || string(first["modules"]) != "1" {
		t.Fatal("other case-sensitive base changed owner")
	}
	if _, ok := second["own"]; ok {
		t.Fatal("case-sensitive base copied extra")
	}
	if string(second["modules"]) != "2" {
		t.Fatal("case-sensitive base copied standard")
	}
}

func TestDumpInfoSupplementOwnerAliasAndConflict(t *testing.T) {
	dir, cache, foreign := t.TempDir(), t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "dump-alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	cpath, _ := cachePath(dir, cache)
	if err := os.Mkdir(cpath, 0755); err != nil {
		t.Fatal(err)
	}
	m, err := OpenDumpInfoMetadata(dir, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	aliasHandle, err := openDumpInfoMetadata(cpath, alias)
	if err != nil {
		t.Fatal(err)
	}
	defer aliasHandle.Close()
	if m.state != aliasHandle.state {
		t.Fatal("same physical dump owner conflicted")
	}
	writeDumpInfo(cpath, alias, 1, time.Second)
	if err := m.Update(DumpInfoField{"legit_alias", json.RawMessage("true")}); err != nil {
		t.Fatal(err)
	}
	if string(readSupplementDoc(t, filepath.Join(cpath, "dump.json"))["legit_alias"]) != "true" {
		t.Fatal("legitimate owner alias rejected")
	}
	if conflict, err := openDumpInfoMetadata(cpath, foreign); err == nil {
		conflict.Close()
		t.Fatal("shared cache accepted conflicting dump owner")
	}
	prior, _ := os.ReadFile(filepath.Join(cpath, "dump.json"))
	writeDumpInfo(cpath, foreign, 999, time.Second)
	after, _ := os.ReadFile(filepath.Join(cpath, "dump.json"))
	if !bytes.Equal(prior, after) {
		t.Fatal("conflicting full writer replaced registered owner metadata")
	}
}
