package dump

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
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

// mkAllExtensionsContainer writes an -AllExtensions container: two child
// directories, each carrying its own manifest and one module. It is the ONE shape
// in which a warm start can disagree with a cold one, because it is the shape whose
// keys depend on a layout read from disk rather than on the path alone.
func mkAllExtensionsContainer(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for dir, name := range map[string]string{"dirA": "РасширениеА", "dirB": "РасширениеБ"} {
		mkExtensionDump(t, filepath.Join(root, dir), extManifestClassic, name, "Catalogs")
		mkBSLFile(t, root, dir+"/Catalogs/Ном/Ext/ObjectModule.bsl",
			"Процедура ПередЗаписью(Отказ)\nКонецПроцедуры\n")
	}
	return root
}

// wrappedAfterColdBuild cold-builds root into cacheDir and returns the report, only
// after the manifest exists, so the caller's warm reopen really is warm.
func wrappedAfterColdBuild(t *testing.T, root, cacheDir string) (WrappedPathState, ExtensionLayoutSummary) {
	t.Helper()
	idx, err := NewIndex(root, cacheDir, false)
	if err != nil {
		t.Fatalf("cold NewIndex: %v", err)
	}
	defer idx.Close()
	<-idx.Done()
	if err := idx.BuildError(); err != nil {
		t.Fatalf("cold build error: %v", err)
	}
	cpath, err := cachePath(root, cacheDir)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	waitManifest(t, cpath, 60*time.Second)
	return idx.WrappedPaths(), idx.ExtensionLayout()
}

// TestWrappedPaths_WarmAndReadOnlyStartsAgreeWithTheColdOne is the parity this
// file's own doc comment asserted and nothing measured.
//
// noteWrappedPaths read idx.extLayout as a FIELD, and the only thing that ever
// filled that field was moduleKeyFor's sync.Once. A cold build derives every key
// through moduleKeyFor and so runs it; the warm manifest path and the read-only
// generation open take their DocIDs out of the manifest and never call it, so the
// same index measured the same paths against a ZERO layout. Every segment the
// layout would have accounted for then counted as a wrap.
//
// WHY IT HAD TO BE A REGRESSION TEST AND NOT AN ARGUMENT. The branch bumps
// dumpIndexSchemaVersion, so every installation cold-builds exactly once and is
// warm from then on. The notice this feeds tells the operator that the extension
// namespace is being lost and to restart against the dump root, and the restart it
// prescribes reproduces it. The cold-only test above cannot see any of that.
//
// The container is the ONLY shape where the two can disagree: a plain dump has an
// empty layout on every path, so a bug here is invisible on dumps/dump_bsl.
func TestWrappedPaths_WarmAndReadOnlyStartsAgreeWithTheColdOne(t *testing.T) {
	root := mkAllExtensionsContainer(t)
	cacheDir := t.TempDir()

	coldWP, coldLayout := wrappedAfterColdBuild(t, root, cacheDir)

	// PREMISE: the cold build really did recognise both extensions, or the parity
	// below would be parity over an uninteresting zero.
	if coldLayout.Extensions != 2 {
		t.Fatalf("cold ExtensionLayout = %+v, want both extensions recognised", coldLayout)
	}
	if coldWP != (WrappedPathState{Files: 0, Total: 2}) {
		t.Fatalf("cold WrappedPaths = %+v, want {Files:0 Total:2}", coldWP)
	}

	// WARM MANIFEST REOPEN, the start every installation makes after the first.
	warm, err := NewIndex(root, cacheDir, false)
	if err != nil {
		t.Fatalf("warm NewIndex: %v", err)
	}
	defer warm.Close()
	<-warm.Done()
	if err := warm.BuildError(); err != nil {
		t.Fatalf("warm build error: %v", err)
	}
	if l := warm.ExtensionLayout(); l.Extensions != 2 {
		t.Fatalf("warm ExtensionLayout = %+v, want both extensions recognised", l)
	}
	if got := warm.WrappedPaths(); got != coldWP {
		t.Errorf("warm WrappedPaths = %+v, cold reported %+v: the warm start measures the "+
			"same bytes against a different layout, so the operator is told on every answer "+
			"that the extension namespace is being lost and to restart, and the restart "+
			"reproduces it", got, coldWP)
	}

	// READ-ONLY GENERATION OPEN, the other warm entry: its names come out of the
	// generation manifest and it never derives a key either.
	gensig := mustGenSig(t, root)
	if err := BuildGeneration(root, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	ro, err := OpenGenerationReadOnly(root, cacheDir, gensig)
	if err != nil {
		t.Fatalf("OpenGenerationReadOnly: %v", err)
	}
	defer ro.Close()
	waitReady(t, ro, 60*time.Second)
	if err := ro.BuildError(); err != nil {
		t.Fatalf("read-only build error: %v", err)
	}
	if l := ro.ExtensionLayout(); l.Extensions != 2 {
		t.Fatalf("read-only ExtensionLayout = %+v, want both extensions recognised", l)
	}
	if got := ro.WrappedPaths(); got != coldWP {
		t.Errorf("read-only WrappedPaths = %+v, cold reported %+v: same disagreement on the "+
			"generation path", got, coldWP)
	}
}

// TestWrappedPaths_TheWarmParityControlCanStillCountAWrap is the positive control
// for the test above, and it is a SEPARATE test because it has to be able to fail
// on its own.
//
// Parity is satisfied by a counter that has died as thoroughly as by one that
// works: "warm equals cold" is true when both are 0 for the wrong reason. So the
// same three starts are made over the same tree with the manifests taken away,
// where every path IS wrapped, and all three must say so.
func TestWrappedPaths_TheWarmParityControlCanStillCountAWrap(t *testing.T) {
	root := mkAllExtensionsContainer(t)
	for _, dir := range []string{"dirA", "dirB"} {
		if err := os.Remove(filepath.Join(root, dir, extManifestClassic)); err != nil {
			t.Fatal(err)
		}
	}
	cacheDir := t.TempDir()

	want := WrappedPathState{Files: 2, Total: 2}
	coldWP, coldLayout := wrappedAfterColdBuild(t, root, cacheDir)
	if coldLayout.Extensions != 0 {
		t.Fatalf("control cold ExtensionLayout = %+v, want no extension recognised", coldLayout)
	}
	if coldWP != want {
		t.Fatalf("control cold WrappedPaths = %+v, want %+v", coldWP, want)
	}

	warm, err := NewIndex(root, cacheDir, false)
	if err != nil {
		t.Fatalf("warm NewIndex: %v", err)
	}
	defer warm.Close()
	<-warm.Done()
	if got := warm.WrappedPaths(); got != want {
		t.Errorf("control warm WrappedPaths = %+v, want %+v: the warm counter is not "+
			"counting, so parity elsewhere proves nothing", got, want)
	}

	gensig := mustGenSig(t, root)
	if err := BuildGeneration(root, cacheDir, gensig); err != nil {
		t.Fatalf("BuildGeneration: %v", err)
	}
	ro, err := OpenGenerationReadOnly(root, cacheDir, gensig)
	if err != nil {
		t.Fatalf("OpenGenerationReadOnly: %v", err)
	}
	defer ro.Close()
	waitReady(t, ro, 60*time.Second)
	if got := ro.WrappedPaths(); got != want {
		t.Errorf("control read-only WrappedPaths = %+v, want %+v", got, want)
	}
}

// TestExtensionLayoutIsNeverReadAroundItsOnce is the census that keeps the defect
// above from coming back in a different function.
//
// The parity test one file up proves the layout is resolved on the paths that exist
// TODAY. It cannot say anything about a loader or a report added next month, and
// the bug it pins is not a wrong expression: it is a RIGHT expression in a place
// that has no ordering guarantee. The field is only the layout after the Once has
// run, so every read outside Index.layout is an assumption about who ran first, and
// reading it is silent when the assumption is wrong: the zero value is a perfectly
// well-formed layout that simply changes no key.
//
// It walks the package's own AST rather than grepping. A grep over source text
// counts the word in this comment, in the doc comment on Index.layout and in the
// string of any message that names the field, and a census that has to be taught
// which of its own hits are prose is a census that will be taught to ignore a real
// one. Selector expressions are the thing being asked about, so selector
// expressions are what it collects.
//
// WRITING THE FIELD NAME INTO A COMMENT CANNOT MOVE EITHER NUMBER THIS PUBLISHES.
// ParseDir is called with mode 0, so comments are not attached to the AST at all,
// and the traversal below accepts only *ast.SelectorExpr. Both counts are asserted
// while the non-test files it parses spell the field out in prose, which is the
// standing proof rather than an argument.
//
// IT FOLLOWS THE FIELD, IT DOES NOT WALK THE FUNCTIONS. The first version collected
// *ast.FuncDecl out of f.Decls and inspected those, so it asked «is this read inside
// a function other than the accessor» when the question is «is this read anywhere
// other than the accessor». A package-level «var _ = someIndex.extLayout» is a
// GenDecl, it is legal Go, it runs at init before any Once, and it walked straight
// past a census whose whole job was to catch it. Every declaration is inspected now,
// and a read that belongs to no function is reported as one at package level.
// collectExtLayoutReads is the shared collector, and the control below runs it over
// a source file written to contain exactly that shape.
func TestExtensionLayoutIsNeverReadAroundItsOnce(t *testing.T) {
	const field, accessor = "extLayout", "layout"

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing the dump package: %v", err)
	}
	pkg, ok := pkgs["dump"]
	if !ok {
		t.Fatalf("package dump not found among %v", pkgs)
	}

	// PREMISE: the census actually reached the source. A ParseDir that matched
	// nothing would report zero offenders and look exactly like a clean tree.
	if len(pkg.Files) < 10 {
		t.Fatalf("parsed only %d non-test files of package dump; the census is not "+
			"reading the package it claims to police", len(pkg.Files))
	}

	var offenders, inAccessor []string
	for name, f := range pkg.Files {
		o, a := collectExtLayoutReads(fset, filepath.Base(name), f, field, accessor)
		offenders = append(offenders, o...)
		inAccessor = append(inAccessor, a...)
	}

	// PREMISE: the accessor itself is still there and still reads the field. If it
	// were renamed or rewritten, "no offenders" would be true because nothing reads
	// the field at all, and the census would be guarding an empty room.
	if len(inAccessor) != 2 {
		t.Fatalf("Index.%s reads .%s %d times (%v), want 2 (the Once's assignment and "+
			"the return); the census no longer describes the accessor it exempts",
			accessor, field, len(inAccessor), inAccessor)
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf(".%s is read outside Index.%s at %v. The field is not the layout until "+
			"extLayoutOnce has run, and only key derivation runs it: a warm manifest start "+
			"and a read-only generation open derive no keys, so a read there silently "+
			"measures against an empty layout instead of the one the index keyed with. "+
			"Call Index.%s.", field, accessor, offenders, accessor)
	}
}

// collectExtLayoutReads returns every read of .field in f, split into the ones
// inside the accessor and the ones anywhere else.
//
// EVERY DECLARATION, not every function. A selector expression can sit in a var, a
// const or a type declaration as well as in a function body, and the ones outside a
// function are the dangerous ones here: they run at init, before any Once.
func collectExtLayoutReads(fset *token.FileSet, file string, f *ast.File,
	field, accessor string) (offenders, inAccessor []string) {
	for _, decl := range f.Decls {
		owner := "(package level)"
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil {
			owner = fn.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != field {
				return true
			}
			where := file + ":" + itoa(fset.Position(sel.Sel.Pos()).Line) + " in " + owner
			if owner == accessor {
				inAccessor = append(inAccessor, where)
			} else {
				offenders = append(offenders, where)
			}
			return true
		})
	}
	return offenders, inAccessor
}

// TestTheExtLayoutCensusSeesAReadThatBelongsToNoFunction is the control that says
// the census above is following the FIELD and not merely walking the functions.
//
// It is not a restatement of the collector in a second form. The source below is
// compiled by go/parser exactly as the real package is, it carries one read at
// package level and one inside an ordinary function, and both have to be reported.
// Restore the old «only *ast.FuncDecl» traversal and the package-level row
// disappears while the real census stays green, because no production file happens
// to contain that shape today. That is precisely how the hole survived: the guard
// was checking a place rather than a value.
func TestTheExtLayoutCensusSeesAReadThatBelongsToNoFunction(t *testing.T) {
	const field, accessor = "extLayout", "layout"
	const src = `package dump

var packageLevelRead = shared.extLayout

func layout() int { return shared.extLayout }

func somethingElse() int { return shared.extLayout }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "control.go", src, 0)
	if err != nil {
		t.Fatalf("parsing the control source: %v", err)
	}

	offenders, inAccessor := collectExtLayoutReads(fset, "control.go", f, field, accessor)

	// The accessor's own read is exempted, so the control also proves the exemption
	// is not exempting everything.
	if len(inAccessor) != 1 {
		t.Errorf("inAccessor = %v, want exactly the read inside %s", inAccessor, accessor)
	}
	if len(offenders) != 2 {
		t.Fatalf("offenders = %v, want two: the package-level read and the one in "+
			"somethingElse", offenders)
	}
	var atPackageLevel bool
	for _, o := range offenders {
		if strings.HasSuffix(o, "in (package level)") {
			atPackageLevel = true
		}
	}
	if !atPackageLevel {
		t.Errorf("offenders = %v: none of them is the package-level read, so the census "+
			"is still asking which FUNCTION a read is in rather than whether the field "+
			"is read at all", offenders)
	}
}
