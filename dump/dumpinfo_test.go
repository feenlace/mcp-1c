package dump

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestBuildCache_WritesDumpInfo covers issue #26 case (a): after a plain build the
// top-level <hash>/dump.json is valid JSON with an absolute dump_path, the correct
// module count, a positive build_seconds, and the platform/schema/built_at facts.
func TestBuildCache_WritesDumpInfo(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\n\t// проверка\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Documents/Реализация/Ext/ObjectModule.bsl",
		"Процедура ОбработкаПроведения(Отказ)\n\t// проведение\nКонецПроцедуры\n")

	if err := BuildCache(dir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}

	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cpath, "dump.json"))
	if err != nil {
		t.Fatalf("read dump.json: %v", err)
	}

	var info dumpInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("dump.json is not valid JSON: %v", err)
	}

	absDir, _ := filepath.Abs(dir)
	if info.DumpPath != absDir {
		t.Errorf("dump_path = %q, want absolute %q", info.DumpPath, absDir)
	}
	if !filepath.IsAbs(info.DumpPath) {
		t.Errorf("dump_path is not absolute: %q", info.DumpPath)
	}
	if info.Modules != 2 {
		t.Errorf("modules = %d, want 2", info.Modules)
	}
	if info.BuildSeconds <= 0 {
		t.Errorf("build_seconds = %v, want > 0", info.BuildSeconds)
	}
	if info.Schema != 1 {
		t.Errorf("schema = %d, want 1", info.Schema)
	}
	if info.Platform != runtime.GOOS {
		t.Errorf("platform = %q, want %q", info.Platform, runtime.GOOS)
	}
	if info.BuiltAt == "" {
		t.Error("built_at is empty")
	} else if _, perr := time.Parse(time.RFC3339, info.BuiltAt); perr != nil {
		t.Errorf("built_at is not RFC3339: %q (%v)", info.BuiltAt, perr)
	}
}

// TestBuildCache_Reindex_DumpInfoTopLevelAndSurvivesGC covers case (b): --reindex
// (the generation build path) rewrites the top-level dump.json with fresh data,
// the file is never written under the per-generation g/ subtree, and a subsequent
// GCGenerations pass does not remove it.
func TestBuildCache_Reindex_DumpInfoTopLevelAndSurvivesGC(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Один/Ext/ObjectModule.bsl",
		"Процедура Один()\n\t// контент1\nКонецПроцедуры\n")

	if err := BuildCache(dir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache initial: %v", err)
	}
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	dumpJSON := filepath.Join(cpath, "dump.json")

	// Drift the dump (add a module), then reindex. The reindex builds a fresh
	// generation under g/<newsig>/, but dump.json must be rewritten at the TOP
	// level with the new module count.
	mkBSLFile(t, dir, "Catalogs/Два/Ext/ObjectModule.bsl",
		"Процедура Два()\n\t// контент2\nКонецПроцедуры\n")
	if err := BuildCache(dir, cacheDir, true); err != nil {
		t.Fatalf("BuildCache reindex: %v", err)
	}

	data, err := os.ReadFile(dumpJSON)
	if err != nil {
		t.Fatalf("dump.json not at top level after reindex: %v", err)
	}
	var info dumpInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("dump.json invalid after reindex: %v", err)
	}
	if info.Modules != 2 {
		t.Errorf("modules after reindex = %d, want 2 (rewritten with fresh data)", info.Modules)
	}

	// dump.json must NOT be written under the generations subtree g/.
	if entries, derr := os.ReadDir(filepath.Join(cpath, "g")); derr == nil {
		for _, e := range entries {
			if _, serr := os.Stat(filepath.Join(cpath, "g", e.Name(), "dump.json")); serr == nil {
				t.Errorf("dump.json must not be written under g/%s", e.Name())
			}
		}
	}

	// GC of old generations must not touch the top-level dump.json.
	gensig := mustGenSig(t, dir)
	if _, err := GCGenerations(dir, cacheDir, gensig); err != nil {
		t.Fatalf("GCGenerations: %v", err)
	}
	if _, err := os.Stat(dumpJSON); err != nil {
		t.Fatalf("dump.json was removed by GCGenerations (must stay top-level): %v", err)
	}
}

// TestBuildCache_DumpInfoDoesNotBreakServe covers case (c): the extra dump.json
// file must not perturb the serve path. cacheShardDirs only picks shard_* dirs, and
// a warm-start open still searches successfully with dump.json present.
func TestBuildCache_DumpInfoDoesNotBreakServe(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Поиск/Ext/ObjectModule.bsl",
		"Процедура Поиск()\n\t// уникальныймаркерпоиска\nКонецПроцедуры\n")

	if err := BuildCache(dir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cpath, "dump.json")); err != nil {
		t.Fatalf("precondition: dump.json should exist: %v", err)
	}

	// cacheShardDirs must ignore the extra dump.json file.
	for _, d := range cacheShardDirs(cpath) {
		if filepath.Base(d) == "dump.json" {
			t.Fatalf("cacheShardDirs incorrectly picked up dump.json: %v", d)
		}
	}

	// Warm-start open (the serve path) must succeed and return search results.
	idx, err := NewIndex(dir, cacheDir, false)
	if err != nil {
		t.Fatalf("NewIndex warm-start: %v", err)
	}
	defer idx.Close()
	waitReady(t, idx, 30*time.Second)
	if err := idx.BuildError(); err != nil {
		t.Fatalf("warm-start build error: %v", err)
	}
	m, total, err := idx.Search(SearchParams{Query: "уникальныймаркерпоиска", Mode: SearchModeSmart, Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total == 0 || len(m) == 0 {
		t.Fatalf("search returned no results with dump.json present: total=%d", total)
	}
}

// TestBuildCache_DumpInfoWriteFailureDoesNotFailBuild covers case (d): a dump.json
// write failure must not fail an otherwise-successful build. The write is forced to
// fail by pre-creating <cpath>/dump.json as a directory (os.WriteFile then returns
// EISDIR), which is portable across platforms and works even when the test runs as
// root (unlike a read-only directory).
func TestBuildCache_DumpInfoWriteFailureDoesNotFailBuild(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Сбой/Ext/ObjectModule.bsl",
		"Процедура Сбой()\n\t// маркер\nКонецПроцедуры\n")

	cpath, err := cachePath(dir, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cpath, "dump.json"), 0o755); err != nil {
		t.Fatalf("pre-create dump.json as a directory: %v", err)
	}

	if err := BuildCache(dir, cacheDir, false); err != nil {
		t.Fatalf("BuildCache must not fail when the dump.json write fails: %v", err)
	}

	// The build itself must have produced shards.
	if len(cacheShardDirs(cpath)) == 0 {
		t.Fatal("expected shards to be built despite the dump.json write failure")
	}
	// dump.json is still the directory we created (the write was correctly skipped).
	fi, err := os.Stat(filepath.Join(cpath, "dump.json"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("expected dump.json to remain the pre-created directory: err=%v", err)
	}
}

// TestBuildCache_StderrPathLine covers case (e): the build prints the index folder
// path to stderr, and the new output line contains no em dash or en dash.
func TestBuildCache_StderrPathLine(t *testing.T) {
	dir := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Тире/Ext/ObjectModule.bsl",
		"Процедура Тире()\n\t// маркер\nКонецПроцедуры\n")

	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	outCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		outCh <- string(b)
	}()

	buildErr := BuildCache(dir, cacheDir, false)
	os.Stderr = orig
	w.Close()
	out := <-outCh
	if buildErr != nil {
		t.Fatalf("BuildCache: %v", buildErr)
	}

	cpath, _ := cachePath(dir, cacheDir)
	wantLine := "Папка индексов: " + cpath
	if !strings.Contains(out, wantLine) {
		t.Errorf("stderr missing index-folder line %q; got: %q", wantLine, out)
	}
	// The added output must not contain an em dash (U+2014) or en dash (U+2013).
	// The needles are written as \u escapes so no raw em/en dash byte appears in
	// this source file.
	for _, bad := range []string{"\u2014", "\u2013"} {
		if strings.Contains(out, bad) {
			t.Errorf("added stderr output contains forbidden dash %q: %q", bad, out)
		}
	}
}

// TestWriteDumpInfo_VersionField documents the mcp_1c_version decision: the field
// is omitted when BuildVersion is unset (tests / non-main callers) and present when
// main has injected the binary version.
func TestWriteDumpInfo_VersionField(t *testing.T) {
	cpath := t.TempDir()

	// Unset: the field is omitted.
	writeDumpInfo(cpath, cpath, 3, 5*time.Second)
	raw, err := os.ReadFile(filepath.Join(cpath, "dump.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "mcp_1c_version") {
		t.Errorf("mcp_1c_version should be omitted when BuildVersion is empty: %s", raw)
	}

	// Set: the field is present with the injected version.
	prev := BuildVersion
	t.Cleanup(func() { BuildVersion = prev })
	BuildVersion = "v1.8.0-test"
	writeDumpInfo(cpath, cpath, 3, 5*time.Second)
	raw, err = os.ReadFile(filepath.Join(cpath, "dump.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var info dumpInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if info.Version != "v1.8.0-test" {
		t.Errorf("mcp_1c_version = %q, want %q", info.Version, "v1.8.0-test")
	}
}
