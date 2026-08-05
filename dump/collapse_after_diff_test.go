package dump

import (
	"os"
	"path/filepath"
	"testing"
)

// The collapse report was published BEFORE the incremental warm-start diff, and
// the comment justifying that said the diff "can only ADD to the picture" and
// that "its additions are dedup-guarded". Both clauses were measured and both are
// false, in opposite directions, and each produces a wrong number a user acts on.
//
// MEASURED on a two-root fixture whose files collide on one key, warm-starting
// from the manifest each time:
//
//	cold build, 2 colliding files                 modules=2  collapsed files=1 keys=1
//	warm start, nothing changed                   modules=2  collapsed files=1 keys=1
//	warm start after DELETING one collided file   modules=1  collapsed files=1 keys=1   <- over-report
//	warm start after ADDING a new colliding file  modules=1  collapsed files=0 keys=0   <- under-report
//
// The deletion case says a file lost its content when no file did, and prints the
// name of a module that is now perfectly readable. The addition case is the one
// that matters: two files derive one key, one file's content is unreachable
// through every map the index reads, and the report says nothing at all. That is
// the exact silent loss the whole collapse counter exists to make countable, let
// back in through the incremental path.
//
// The dedup guard on the addition path is real; what is false is using it as a
// reason the COUNT stays right. It suppresses the append to idx.names precisely
// when a duplicate key arrives, so counting idx.names after it cannot see the
// duplicate. The multiset the report needs is one entry per FILE, which is what
// pathToDocID holds.

// cadWrite writes one .bsl under root.
func cadWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cadOpen opens the index over dir with the shared cache and returns the collapse
// report and the module count it publishes.
func cadOpen(t *testing.T, dir, cache string) (CollapsedKeyState, int) {
	t.Helper()
	idx, err := NewIndex(dir, cache, false)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("build: %v", err)
	}
	return idx.CollapsedKeys(), idx.ModuleCount()
}

// TestCollapseReportSurvivesTheIncrementalDiff drives the four states above.
func TestCollapseReportSurvivesTheIncrementalDiff(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	// Two dump roots under one parent: the anchor scan keys both off Catalogs, so
	// the two files derive one module name.
	a := "A/Catalogs/Товары/Ext/ObjectModule.bsl"
	b := "B/Catalogs/Товары/Ext/ObjectModule.bsl"
	c := "C/Catalogs/Товары/Ext/ObjectModule.bsl"
	cadWrite(t, dir, a, "Процедура Один()\nКонецПроцедуры\n")
	cadWrite(t, dir, b, "Процедура Два()\nКонецПроцедуры\n")

	// CONTROL: the cold build sees the collapse. Every assertion below is about
	// the warm path keeping up with it, so a fixture that never collided would
	// make all of them vacuous.
	if st, _ := cadOpen(t, dir, cache); st.Files != 1 || st.Keys != 1 {
		t.Fatalf("control failed: the cold build reports files=%d keys=%d, want 1 and 1",
			st.Files, st.Keys)
	}
	// And the warm start over an unchanged dump keeps it. This is the state the
	// pre-diff publication was written for and it must not regress.
	if st, _ := cadOpen(t, dir, cache); st.Files != 1 || st.Keys != 1 {
		t.Fatalf("a warm start over an UNCHANGED dump lost the collapse report: files=%d keys=%d",
			st.Files, st.Keys)
	}

	// CLAUSE 1: the diff DELETES. One of the two colliding files goes away, so
	// nothing collides any more.
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(b))); err != nil {
		t.Fatal(err)
	}
	st, count := cadOpen(t, dir, cache)
	if st.Files != 0 || st.Keys != 0 {
		t.Errorf("after one of the two colliding files was DELETED the report still claims "+
			"files=%d keys=%d sample=%v, over one module (%d indexed) that is now perfectly "+
			"readable. The diff does not only add.", st.Files, st.Keys, st.Sample, count)
	}

	// CLAUSE 2: an ADDED file collides with one already in the manifest. The
	// addition path refuses to append a name it already has, so the duplicate
	// never reaches idx.names and a report counted from idx.names cannot see it.
	cadWrite(t, dir, c, "Процедура Три()\nКонецПроцедуры\n")
	st, count = cadOpen(t, dir, cache)
	if st.Files != 1 || st.Keys != 1 {
		t.Errorf("after a NEW colliding file was added the report says files=%d keys=%d "+
			"(indexed modules: %d), so one file's content is unreachable and nothing says "+
			"so. That is the silent loss this counter exists to prevent.",
			st.Files, st.Keys, count)
	}
}
