package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
)

const serveDumpInfoTimeout = 30 * time.Second

type serveDumpInfoFixture struct {
	Schema       int     `json:"schema"`
	DumpPath     string  `json:"dump_path"`
	Modules      int     `json:"modules"`
	BuildSeconds float64 `json:"build_seconds"`
	BuiltAt      string  `json:"built_at"`
	Version      string  `json:"mcp_1c_version"`
	Platform     string  `json:"platform"`
}

func openServeIndexReady(t *testing.T, dumpDir, cacheDir string) *dump.Index {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	idx, err := openServeIndexLocal(ctx, dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("openServeIndexLocal: %v", err)
	}
	select {
	case <-idx.Done():
	case <-time.After(serveDumpInfoTimeout):
		t.Fatal("serve index did not finish within 30s")
	}
	if err := idx.BuildError(); err != nil {
		t.Fatalf("serve index build: %v", err)
	}
	if !idx.Ready() {
		t.Fatal("serve index did not become ready")
	}
	return idx
}

func writeServeDumpInfoFixture(t *testing.T) (string, string) {
	t.Helper()
	dumpDir := t.TempDir()
	module := filepath.Join(dumpDir, "CommonModules", "Issue53", "Ext", "Module.bsl")
	if err := os.MkdirAll(filepath.Dir(module), 0o755); err != nil {
		t.Fatalf("create fixture module directory: %v", err)
	}
	if err := os.WriteFile(module, []byte("Процедура И53Проверка() Экспорт\nКонецПроцедуры\n"), 0o644); err != nil {
		t.Fatalf("write fixture module: %v", err)
	}
	return dumpDir, t.TempDir()
}

func useServeDumpInfoVersion(t *testing.T) {
	t.Helper()
	previous := dump.BuildVersion
	dump.BuildVersion = version
	t.Cleanup(func() { dump.BuildVersion = previous })
}

func assertServeDumpInfo(t *testing.T, dumpDir, cacheDir string, modules int) string {
	t.Helper()
	cpath, err := dump.CacheDir(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("resolve cache path: %v", err)
	}
	path := filepath.Join(cpath, "dump.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache mapping %s: %v", path, err)
	}
	var info serveDumpInfoFixture
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("decode cache mapping %s: %v", path, err)
	}
	wantDump, err := filepath.Abs(dumpDir)
	if err != nil {
		t.Fatalf("absolute dump path: %v", err)
	}
	if info.Schema != 1 || info.DumpPath != wantDump || info.Modules != modules || info.Version != version || info.Platform != runtime.GOOS {
		t.Errorf("dump.json metadata = %+v; want schema=1 dump_path=%q modules=%d version=%q platform=%q", info, wantDump, modules, version, runtime.GOOS)
	}
	if info.BuildSeconds < 0 {
		t.Errorf("build_seconds = %v, want non-negative", info.BuildSeconds)
	}
	if _, err := time.Parse(time.RFC3339, info.BuiltAt); err != nil {
		t.Errorf("built_at %q is not RFC3339: %v", info.BuiltAt, err)
	}
	if _, err := os.Stat(filepath.Join(dumpDir, "dump.json")); !os.IsNotExist(err) {
		t.Errorf("dump.json was written in the source dump root; stat err=%v", err)
	}
	return cpath
}

func TestOpenServeIndexWritesDumpInfoOnFreshServe(t *testing.T) {
	useServeDumpInfoVersion(t)
	dumpDir, cacheDir := writeServeDumpInfoFixture(t)
	idx := openServeIndexReady(t, dumpDir, cacheDir)
	t.Cleanup(func() { _ = idx.Close() })
	if idx.ModuleCount() != 1 {
		t.Fatalf("module count = %d, want 1", idx.ModuleCount())
	}
	cpath := assertServeDumpInfo(t, dumpDir, cacheDir, idx.ModuleCount())
	if _, err := os.Stat(filepath.Join(cpath, "g", "dump.json")); !os.IsNotExist(err) {
		t.Errorf("dump.json was written in generation directory; stat err=%v", err)
	}
}

func TestOpenServeIndexRestoresDumpInfoOnWarmServe(t *testing.T) {
	useServeDumpInfoVersion(t)
	dumpDir, cacheDir := writeServeDumpInfoFixture(t)
	first := openServeIndexReady(t, dumpDir, cacheDir)
	if err := first.Close(); err != nil {
		t.Fatalf("close initial serve index: %v", err)
	}
	cpath, err := dump.CacheDir(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("resolve cache path: %v", err)
	}
	if err := os.Remove(filepath.Join(cpath, "dump.json")); err != nil {
		t.Fatalf("remove initial mapping to prove warm path restores it: %v", err)
	}
	second := openServeIndexReady(t, dumpDir, cacheDir)
	t.Cleanup(func() { _ = second.Close() })
	if second.ModuleCount() != 1 {
		t.Fatalf("warm module count = %d, want 1", second.ModuleCount())
	}
	assertServeDumpInfo(t, dumpDir, cacheDir, second.ModuleCount())
}

func TestOpenServeIndexDumpInfoWriteFailureDoesNotFailServe(t *testing.T) {
	useServeDumpInfoVersion(t)
	dumpDir, cacheDir := writeServeDumpInfoFixture(t)
	cpath, err := dump.CacheDir(dumpDir, cacheDir)
	if err != nil {
		t.Fatalf("resolve cache path: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cpath, "dump.json"), 0o755); err != nil {
		t.Fatalf("make dump.json path unwritable as a file: %v", err)
	}
	idx := openServeIndexReady(t, dumpDir, cacheDir)
	t.Cleanup(func() { _ = idx.Close() })
	if idx.ModuleCount() != 1 {
		t.Fatalf("module count after mapping write failure = %d, want 1", idx.ModuleCount())
	}
}

func TestServeDumpInfoFollowsSuccessfulReloadOnly(t *testing.T) {
	useServeDumpInfoVersion(t)
	dumpDir, cacheDir := writeServeDumpInfoFixture(t)
	idx := openServeIndexReady(t, dumpDir, cacheDir)
	t.Cleanup(func() { _ = idx.Close() })
	cpath := assertServeDumpInfo(t, dumpDir, cacheDir, 1)
	path := filepath.Join(cpath, "dump.json")
	// An old timestamp makes freshness observable without a clock-dependent sleep.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var old serveDumpInfoFixture
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	old.BuiltAt = "2000-01-01T00:00:00Z"
	data, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(dumpDir, "CommonModules", "Added", "Ext", "Module.bsl")
	if err := os.MkdirAll(filepath.Dir(module), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(module, []byte("Процедура И53Новая() Экспорт\nКонецПроцедуры\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := idx.Reload()
	if err != nil || !rep.Changed || rep.ModulesAfter != 2 {
		t.Fatalf("Reload = %+v, %v", rep, err)
	}
	assertServeDumpInfo(t, dumpDir, cacheDir, 2)
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var current serveDumpInfoFixture
	if err := json.Unmarshal(data, &current); err != nil {
		t.Fatal(err)
	}
	if current.BuiltAt == old.BuiltAt {
		t.Fatal("successful reload did not refresh built_at")
	}
	if err := os.RemoveAll(dumpDir); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Reload(); err == nil {
		t.Fatal("reload of missing dump unexpectedly succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(data) {
		t.Fatal("failed reload changed successful metadata")
	}
}

func TestServeReloadDumpInfoWriteFailureDoesNotFailReload(t *testing.T) {
	useServeDumpInfoVersion(t)
	dumpDir, cacheDir := writeServeDumpInfoFixture(t)
	idx := openServeIndexReady(t, dumpDir, cacheDir)
	t.Cleanup(func() { _ = idx.Close() })
	cpath := assertServeDumpInfo(t, dumpDir, cacheDir, 1)
	path := filepath.Join(cpath, "dump.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(dumpDir, "CommonModules", "Added", "Ext", "Module.bsl")
	if err := os.MkdirAll(filepath.Dir(module), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(module, []byte("Процедура И53Новая() Экспорт\nКонецПроцедуры\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := idx.Reload()
	if err != nil || !rep.Changed || idx.ModuleCount() != 2 {
		t.Fatalf("Reload after metadata failure = %+v, %v", rep, err)
	}
	_, total, err := idx.Search(dump.SearchParams{Query: "И53Новая", Mode: dump.SearchModeExact, Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("search after metadata failure = %d, %v", total, err)
	}
}

func TestCancelledServeDoesNotWriteDumpInfo(t *testing.T) {
	dumpDir, cacheDir := writeServeDumpInfoFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	idx, err := openServeIndexLocal(ctx, dumpDir, cacheDir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	select {
	case <-idx.Done():
	case <-time.After(serveDumpInfoTimeout):
		t.Fatal("cancelled serve did not finish")
	}
	if idx.Ready() || idx.BuildError() == nil {
		t.Fatal("cancelled serve reported successful readiness")
	}
	cpath, err := dump.CacheDir(dumpDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cpath, "dump.json")); !os.IsNotExist(err) {
		t.Fatalf("cancelled serve wrote mapping: %v", err)
	}
}

func TestServeUnchangedReloadRestoresDumpInfo(t *testing.T) {
	useServeDumpInfoVersion(t)
	dumpDir, cacheDir := writeServeDumpInfoFixture(t)
	idx := openServeIndexReady(t, dumpDir, cacheDir)
	t.Cleanup(func() { _ = idx.Close() })
	cpath := assertServeDumpInfo(t, dumpDir, cacheDir, 1)
	if err := os.Remove(filepath.Join(cpath, "dump.json")); err != nil {
		t.Fatal(err)
	}
	rep, err := idx.Reload()
	if err != nil || rep.Changed {
		t.Fatalf("unchanged Reload = %+v, %v", rep, err)
	}
	assertServeDumpInfo(t, dumpDir, cacheDir, 1)
}
