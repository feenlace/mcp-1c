package dump

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestColocateBuildScratch asserts the offline-builder scratch directory is placed
// beside its destination (same filesystem → intra-device final rename, no EXDEV),
// is not mistakable for a shard, and is cleaned up.
func TestColocateBuildScratch(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "shard_7")

	prefix, cleanup, err := coLocateBuildScratch(dst)
	if err != nil {
		t.Fatalf("coLocateBuildScratch: %v", err)
	}

	// Co-located: same parent directory as the destination, hence same device, so
	// bleve's final os.Rename(scratch_segment -> shard/store) is intra-device.
	if filepath.Dir(prefix) != filepath.Dir(dst) {
		t.Errorf("scratch %q is not co-located with destination %q", prefix, dst)
	}
	// Must not be picked up by cacheShardDirs (which matches the "shard_" prefix).
	if strings.HasPrefix(filepath.Base(prefix), "shard_") {
		t.Errorf("scratch base %q starts with shard_ and would be mistaken for a shard", filepath.Base(prefix))
	}
	if dirs := cacheShardDirs(base); len(dirs) != 0 {
		t.Errorf("cacheShardDirs mistook the scratch dir for a shard: %v", dirs)
	}
	// Created on disk, then removed by cleanup (no leak).
	if fi, statErr := os.Stat(prefix); statErr != nil || !fi.IsDir() {
		t.Fatalf("scratch dir not created: err=%v", statErr)
	}
	cleanup()
	if _, statErr := os.Stat(prefix); !os.IsNotExist(statErr) {
		t.Errorf("cleanup did not remove scratch %q (err=%v)", prefix, statErr)
	}
}

// TestBuildShardOffline_NoTempDirDependency is the EXDEV regression test. The cold
// build previously aborted with "invalid cross-device link" because bleve's offline
// builder wrote its scratch under os.TempDir() and then renamed the final segment
// into a cache on a different device (the Docker named-volume / k8s-PVC layout). The
// fix co-locates the scratch in the destination arena, so the build must no longer
// depend on os.TempDir() at all — proven here by pointing TMPDIR at a non-existent
// directory and requiring the build to still succeed without ever touching it.
func TestBuildShardOffline_NoTempDirDependency(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "shard_0")
	bogusTmp := filepath.Join(t.TempDir(), "nonexistent-tmpdir")
	t.Setenv("TMPDIR", bogusTmp)
	if os.TempDir() != bogusTmp {
		t.Skipf("os.TempDir() is not driven by TMPDIR on this platform (got %q); the co-located scratch still applies, but this behavioral probe is Unix-specific", os.TempDir())
	}

	content := map[string]string{
		"Документ.A.МодульОбъекта": "Процедура А() Экспорт\n\tСообщить(\"привет\");\nКонецПроцедуры\n",
		"Документ.Б.МодульОбъекта": "Функция Б()\n\tВозврат 1;\nКонецФункции\n",
	}
	names := []string{"Документ.A.МодульОбъекта", "Документ.Б.МодульОбъекта"}

	idx, err := buildShardOffline(dst, names, func(n string) string { return content[n] }, 0, 1, buildBSLMapping(), &atomic.Int64{})
	if err != nil {
		t.Fatalf("buildShardOffline with an unusable os.TempDir() failed: %v "+
			"(the scratch must be co-located with the destination, not under os.TempDir())", err)
	}
	defer idx.Close()

	if dc, dcErr := idx.DocCount(); dcErr != nil || dc != 2 {
		t.Errorf("expected 2 docs, got %d (err=%v)", dc, dcErr)
	}
	// The build must not have created or used os.TempDir().
	if _, statErr := os.Stat(bogusTmp); !os.IsNotExist(statErr) {
		t.Errorf("build created/used os.TempDir() %q (stat err=%v); scratch should be co-located", bogusTmp, statErr)
	}
	// No scratch dir left behind in the destination arena.
	entries, _ := os.ReadDir(filepath.Dir(dst))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".bleve-scratch-") {
			t.Errorf("leaked scratch dir %q in the cache arena", e.Name())
		}
	}
}
