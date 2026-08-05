package dump

import (
	"os"
	"path/filepath"
	"testing"
)

// The wrap report, from the two directions a mutation found it unguarded in.

// TestWrappedPaths_AnAllExtensionsContainerIsNotWrapped.
//
// A segment the LAYOUT consumes is not a wrap. Under the -AllExtensions shape the
// first segment of every path is a recognised extension directory and the
// namespace accounts for it, so the question is only about what is left. Counting
// it would put a warning in front of every operator whose container is pointed at
// correctly, which is exactly the shape this whole feature exists to support.
func TestWrappedPaths_AnAllExtensionsContainerIsNotWrapped(t *testing.T) {
	root := t.TempDir()
	for dir, name := range map[string]string{"dirA": "РасширениеА", "dirB": "РасширениеБ"} {
		mkExtensionDump(t, filepath.Join(root, dir), extManifestClassic, name, "Catalogs")
		p := filepath.Join(root, dir, "Catalogs", "Ном", "Ext")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "ObjectModule.bsl"),
			[]byte("Процедура П() КонецПроцедуры\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	<-idx.Done()

	// PREMISE: the layout really did recognise both, or "not wrapped" would be true
	// for an uninteresting reason.
	if l := idx.ExtensionLayout(); l.Extensions != 2 {
		t.Fatalf("ExtensionLayout = %+v, want both extensions recognised", l)
	}
	wp := idx.WrappedPaths()
	if wp.Total != 2 {
		t.Fatalf("WrappedPaths = %+v, want both files counted", wp)
	}
	if wp.Files != 0 {
		t.Errorf("WrappedPaths = %+v: the extension directory the layout consumed was counted "+
			"as a wrap, so a correctly pointed -AllExtensions container warns about itself", wp)
	}

	// POSITIVE CONTROL: the same tree with the manifests removed IS wrapped, so the
	// zero above is the layout accounting for the segment and not the counter being
	// dead.
	for _, dir := range []string{"dirA", "dirB"} {
		if err := os.Remove(filepath.Join(root, dir, extManifestClassic)); err != nil {
			t.Fatal(err)
		}
	}
	plain, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer plain.Close()
	<-plain.Done()
	if wp := plain.WrappedPaths(); wp.Files != 2 {
		t.Fatalf("positive control failed: with no manifests the same tree reports %+v, "+
			"want both files wrapped", wp)
	}
}

// TestWrappedPaths_AReloadReportsTheGenerationItPublished.
//
// The report is published inside the critical section that swaps a generation in,
// and it is derived from pathToDocID. Taken one line before that map is assigned it
// measures the generation being RETIRED, and the number then describes a dump
// nobody is serving. The dump below changes shape across the reload precisely so
// the two answers differ.
func TestWrappedPaths_AReloadReportsTheGenerationItPublished(t *testing.T) {
	root := t.TempDir()
	mkBSLFile(t, root, "Catalogs/Номенклатура/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\nКонецПроцедуры\n")

	idx, err := NewIndex(root, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()
	<-idx.Done()

	before := idx.WrappedPaths()
	if before.Files != 0 || before.Total != 1 {
		t.Fatalf("before the reload WrappedPaths = %+v, want {Files:0 Total:1}", before)
	}

	// A second module, two levels below the root this index was opened on.
	mkBSLFile(t, root, "обёртка/Внутри/Catalogs/Другой/Ext/ObjectModule.bsl",
		"Процедура ПередЗаписью(Отказ)\nКонецПроцедуры\n")
	mustReload(t, idx)

	after := idx.WrappedPaths()
	if after.Total != 2 {
		t.Fatalf("after the reload WrappedPaths = %+v, want both files counted", after)
	}
	if after.Files != 1 {
		t.Errorf("after the reload WrappedPaths = %+v, want one wrapped file: the report "+
			"describes the generation that was retired rather than the one now served", after)
	}
}
