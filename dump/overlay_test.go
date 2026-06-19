package dump

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// snapshotFingerprints walks root and records each regular file's content
// fingerprint (relative slash-path -> "size:sha256"). It deliberately excludes
// mtime/atime: the reader registry heartbeats entry mtimes (os.Chtimes), so an
// mtime-sensitive snapshot would flake; the immutable base content never changes.
func snapshotFingerprints(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = fmt.Sprintf("%d:%s", len(b), hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

// assertSameFingerprints fails if the two snapshots differ (added/removed file or
// changed content), reporting the offending paths.
func assertSameFingerprints(t *testing.T, before, after map[string]string) {
	t.Helper()
	for path, want := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("immutable base file disappeared after overlay ingest: %s", path)
			continue
		}
		if got != want {
			t.Errorf("immutable base file content changed after overlay ingest: %s (before=%s after=%s)", path, want, got)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("new file appeared under the base cache after overlay ingest: %s", path)
		}
	}
}

// newReadOnlyGenIndex builds an immutable generation from srcDir into cacheDir and
// opens it READ-ONLY (the serve path). The returned index is ready, read-only, and
// auto-closed on test cleanup.
func newReadOnlyGenIndex(t *testing.T, srcDir, cacheDir string) *Index {
	t.Helper()
	gensig := mustGenSig(t, srcDir)
	if err := BuildGeneration(srcDir, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	idx, err := OpenGenerationReadOnly(srcDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("OpenGenerationReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("read-only build error: %v", err)
	}
	if !idx.readOnly {
		t.Fatal("OpenGenerationReadOnly did not produce a read-only index")
	}
	return idx
}

// TestOverlay_ReadOnly_IndexDocWithMeta_MergedSearchBaseUntouched proves that on a
// read-only (immutable generation) index, IndexDocWithMeta routes the doc to a
// per-process in-memory overlay that is merged into the search alias (smart search
// finds it; maps expose it), while the on-disk immutable base stays byte-identical.
func TestOverlay_ReadOnly_IndexDocWithMeta_MergedSearchBaseUntouched(t *testing.T) {
	src := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, src, "Catalogs/База/Ext/ObjectModule.bsl",
		"Процедура БазоваяПроцедура()\n\t// маркерБазовыйОригинал\nКонецПроцедуры\n")

	idx := newReadOnlyGenIndex(t, src, cacheDir)

	// Snapshot the immutable base AFTER open+ready, BEFORE the overlay write, so the
	// comparison isolates the effect of IndexDocWithMeta alone.
	before := snapshotFingerprints(t, cacheDir)

	const extID = "ext.МоёРасш.Документы.ЗаказКлиента.МодульОбъекта"
	const extMarker = "маркерРасширенияОверлей"
	if err := idx.IndexDocWithMeta(extID,
		"Функция РасшФ()\n\t// "+extMarker+"\n\tВозврат 1;\nКонецФункции\n",
		"Расширение", "МоёРасш"); err != nil {
		t.Fatalf("IndexDocWithMeta on read-only index: %v", err)
	}

	// (a) smart search (alias merge of base + overlay) returns the overlay doc.
	matches, total, err := idx.Search(SearchParams{Query: extMarker, Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatalf("smart Search: %v", err)
	}
	if total == 0 || len(matches) == 0 {
		t.Fatal("smart search did not find the overlay doc — alias merge broken")
	}

	// Base content still searchable via the same alias.
	if _, baseTotal, err := idx.Search(SearchParams{Query: "маркерБазовыйОригинал", Mode: SearchModeSmart, Limit: 50}); err != nil || baseTotal == 0 {
		t.Fatalf("base doc no longer searchable after overlay add: total=%d err=%v", baseTotal, err)
	}

	// (b) maps (used by GetContent / ModuleNames / regex+exact) expose the overlay doc.
	content, ok := idx.GetContent(extID)
	if !ok || !strings.Contains(content, extMarker) {
		t.Fatalf("GetContent missing overlay doc: ok=%v content=%q", ok, content)
	}
	if !slices.Contains(idx.ModuleNames(), extID) {
		t.Fatal("ModuleNames does not include the overlay doc")
	}

	// The overlay was actually created (read-only ingest path taken).
	idx.mu.RLock()
	overlayCreated := idx.overlay != nil
	idx.mu.RUnlock()
	if !overlayCreated {
		t.Fatal("expected the in-memory overlay to be created on read-only ingest")
	}

	// (c) the immutable base on disk is untouched.
	after := snapshotFingerprints(t, cacheDir)
	assertSameFingerprints(t, before, after)
}

// TestOverlay_ReadOnly_DeleteDoc_RemovesFromOverlay proves DeleteDoc removes an
// overlay doc from a read-only index: gone from smart search, names and content.
func TestOverlay_ReadOnly_DeleteDoc_RemovesFromOverlay(t *testing.T) {
	src := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, src, "Catalogs/База/Ext/ObjectModule.bsl",
		"Процедура БазоваяПроцедура()\n\t// маркерБазаУдаление\nКонецПроцедуры\n")

	idx := newReadOnlyGenIndex(t, src, cacheDir)

	const extID = "ext.УдаляемоеРасш.Документы.Накладная.МодульОбъекта"
	const extMarker = "маркерУдаляемыйОверлей"
	if err := idx.IndexDocWithMeta(extID,
		"Функция УдалФ()\n\t// "+extMarker+"\n\tВозврат 0;\nКонецФункции\n",
		"Расширение", "УдаляемоеРасш"); err != nil {
		t.Fatalf("IndexDocWithMeta: %v", err)
	}

	// Confirm it is searchable first.
	if _, total, err := idx.Search(SearchParams{Query: extMarker, Mode: SearchModeSmart, Limit: 50}); err != nil || total == 0 {
		t.Fatalf("overlay doc not searchable before delete: total=%d err=%v", total, err)
	}

	if err := idx.DeleteDoc(extID); err != nil {
		t.Fatalf("DeleteDoc on read-only index: %v", err)
	}

	// Gone from smart search.
	matches, total, err := idx.Search(SearchParams{Query: extMarker, Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatalf("smart Search after delete: %v", err)
	}
	if total != 0 || len(matches) != 0 {
		t.Errorf("expected overlay doc gone from smart search after delete, got total=%d len=%d", total, len(matches))
	}

	// Gone from names and content.
	if slices.Contains(idx.ModuleNames(), extID) {
		t.Error("ModuleNames still lists the deleted overlay doc")
	}
	if _, ok := idx.GetContent(extID); ok {
		t.Error("GetContent still returns the deleted overlay doc")
	}
}

// TestOverlay_RWPath_OverlayNeverCreated proves the legacy read-WRITE path is
// unchanged: IndexDocWithMeta goes to the shards (searchable) and the overlay is
// never created.
func TestOverlay_RWPath_OverlayNeverCreated(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Раб/Ext/ObjectModule.bsl",
		"Процедура Рабочая()\n\t// маркерРабочийБаза\nКонецПроцедуры\n")

	idx, err := NewIndex(dir, "", false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	waitReady(t, idx, 30*time.Second)

	if idx.readOnly {
		t.Fatal("NewIndex(reindex=false) unexpectedly produced a read-only index")
	}

	const docID = "ext.РабРасш.Документы.Заказ.МодульОбъекта"
	const marker = "маркерРабочийРежим"
	if err := idx.IndexDocWithMeta(docID,
		"Функция РабФ()\n\t// "+marker+"\n\tВозврат 7;\nКонецФункции\n",
		"Расширение", "РабРасш"); err != nil {
		t.Fatalf("IndexDocWithMeta (RW): %v", err)
	}

	if _, total, err := idx.Search(SearchParams{Query: marker, Mode: SearchModeSmart, Limit: 50}); err != nil || total == 0 {
		t.Fatalf("RW doc not searchable: total=%d err=%v", total, err)
	}

	// The overlay must never be created in RW mode (writes go to shards).
	idx.mu.RLock()
	overlay := idx.overlay
	idx.mu.RUnlock()
	if overlay != nil {
		t.Fatal("overlay must be nil in read-write mode (writes go to shards)")
	}
}

// TestOverlay_PerProcess_Independent opens TWO read-only indices over the SAME
// on-disk immutable generation. An overlay ingest into one is invisible to the
// other — proving overlays are per-process (in-memory) and never written to the
// shared base.
func TestOverlay_PerProcess_Independent(t *testing.T) {
	src := t.TempDir()
	cacheDir := t.TempDir()
	mkBSLFile(t, src, "Catalogs/Общий/Ext/ObjectModule.bsl",
		"Процедура Общая()\n\t// маркерОбщейБазы\nКонецПроцедуры\n")

	gensig := mustGenSig(t, src)
	if err := BuildGeneration(src, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}

	open := func() *Index {
		idx, err := OpenGenerationReadOnly(src, cacheDir, gensig)
		if err != nil {
			t.Fatalf("OpenGenerationReadOnly: %v", err)
		}
		t.Cleanup(func() { _ = idx.Close() })
		<-idx.Done()
		if err := idx.BuildError(); err != nil {
			t.Fatalf("build error: %v", err)
		}
		if !idx.readOnly {
			t.Fatal("expected read-only index")
		}
		return idx
	}

	idx1 := open()
	idx2 := open()

	// Both readers serve the shared base content.
	for i, idx := range []*Index{idx1, idx2} {
		if _, total, err := idx.Search(SearchParams{Query: "маркерОбщейБазы", Mode: SearchModeSmart, Limit: 50}); err != nil || total == 0 {
			t.Fatalf("reader %d cannot see shared base: total=%d err=%v", i+1, total, err)
		}
	}

	const extID = "ext.Изолят.Документы.Д.МодульОбъекта"
	const extMarker = "маркерТолькоВПервом"
	if err := idx1.IndexDocWithMeta(extID,
		"Функция ИзоФ()\n\t// "+extMarker+"\n\tВозврат 1;\nКонецФункции\n",
		"Расширение", "Изолят"); err != nil {
		t.Fatalf("IndexDocWithMeta into idx1: %v", err)
	}

	// idx1 sees its own overlay doc.
	if _, total, err := idx1.Search(SearchParams{Query: extMarker, Mode: SearchModeSmart, Limit: 50}); err != nil || total == 0 {
		t.Fatalf("idx1 does not see its own overlay doc: total=%d err=%v", total, err)
	}
	if !slices.Contains(idx1.ModuleNames(), extID) {
		t.Fatal("idx1 ModuleNames missing its own overlay doc")
	}

	// idx2 (independent process, shared base) must NOT see it.
	matches2, total2, err := idx2.Search(SearchParams{Query: extMarker, Mode: SearchModeSmart, Limit: 50})
	if err != nil {
		t.Fatalf("idx2 Search: %v", err)
	}
	if total2 != 0 || len(matches2) != 0 {
		t.Errorf("overlay leaked across processes: idx2 sees the doc (total=%d len=%d)", total2, len(matches2))
	}
	if slices.Contains(idx2.ModuleNames(), extID) {
		t.Error("overlay leaked across processes: idx2 ModuleNames lists the doc")
	}
}
