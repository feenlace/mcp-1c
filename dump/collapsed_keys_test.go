package dump

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Collapsed-key detection.
//
// The keyspace collapse this package can suffer is SILENT by construction: the
// loaders write plain maps (contentByName[name], pathByName[name]), so the second
// file deriving a name overwrites the first and its content is gone, while
// ModuleCount() keeps returning the number of files walked. Nothing in the index
// says the two numbers disagree.
//
// WHY THE DETECTION IS EXACT AND NOT A RATIO. An earlier proposal was to flag a
// dump when distinct*2 < total. That is refuted by arithmetic, not by taste: a
// tree half of which is shifted by a wrapper produces roughly total+small
// distinct keys — 13586 against 13575 files in the measured case — so the ratio
// never trips while 6782 modules are already lost. Any threshold has the same
// shape of hole. The detector below counts the overwrites themselves.
// ---------------------------------------------------------------------------

// TestCollapsedKeysOfCountsExactly pins the arithmetic of the detector on
// hand-checkable inputs, including the case that refuted the ratio heuristic.
func TestCollapsedKeysOfCountsExactly(t *testing.T) {
	tests := []struct {
		name      string
		names     []string
		wantFiles int
		wantKeys  int
		wantNames []string
	}{
		{"no collision", []string{"a", "b", "c"}, 0, 0, nil},
		{"empty", nil, 0, 0, nil},
		{"one name twice", []string{"a", "a", "b"}, 1, 1, []string{"a"}},
		{"one name three times", []string{"a", "a", "a"}, 2, 1, []string{"a"}},
		{"two names twice each", []string{"a", "a", "b", "b", "c"}, 2, 2, []string{"a", "b"}},
		// The refutation, in miniature. 5 files, 4 distinct keys: the ratio
		// distinct*2 < total is 8 < 5, FALSE, so a ratio detector says "fine"
		// while one file's content is already unreachable.
		{"a ratio detector would call this clean",
			[]string{"a", "a", "b", "c", "d"}, 1, 1, []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collapsedKeysOf(tt.names)
			if got.Files != tt.wantFiles {
				t.Errorf("Files = %d, want %d", got.Files, tt.wantFiles)
			}
			if got.Keys != tt.wantKeys {
				t.Errorf("Keys = %d, want %d", got.Keys, tt.wantKeys)
			}
			if !slices.Equal(got.Sample, tt.wantNames) {
				t.Errorf("Sample = %v, want %v", got.Sample, tt.wantNames)
			}
		})
	}
}

// TestCollapsedKeyRatioHeuristicIsRefutedAtScale reproduces, at the size it was
// measured at, the corpus that refuted the ratio detector, so the reason this
// code counts overwrites instead of comparing two totals survives in the tree
// rather than only in a commit message.
//
// The shape is a half-shifted tree: 6782 keys carrying two files each, plus 11
// files that stayed unique. 13575 files, 6793 distinct keys. The proposed rule
// was "flag when distinct*2 < total", and 6793*2 = 13586, which is NOT less than
// 13575. It misses by 11 while 6782 files have already lost their content.
func TestCollapsedKeyRatioHeuristicIsRefutedAtScale(t *testing.T) {
	var names []string
	for i := 0; i < 6782; i++ {
		k := "dup" + itoa(i)
		names = append(names, k, k)
	}
	for i := 0; i < 11; i++ {
		names = append(names, "uniq"+itoa(i))
	}
	const total = 13575
	if len(names) != total {
		t.Fatalf("fixture built %d names, want %d", len(names), total)
	}

	got := collapsedKeysOf(names)
	if got.Files != 6782 {
		t.Fatalf("Files = %d, want 6782: the exact detector must see every overwrite", got.Files)
	}
	distinct := total - got.Files
	if distinct != 6793 {
		t.Fatalf("distinct = %d, want 6793", distinct)
	}

	// The refuted rule, evaluated on this very corpus. It must NOT fire — that is
	// the refutation, and asserting it here is what keeps the exact detector from
	// ever being "simplified" back into a threshold.
	if distinct*2 < total {
		t.Fatalf("the ratio rule fired (%d*2 = %d < %d), so this corpus no longer "+
			"demonstrates the hole it was built to demonstrate",
			distinct, distinct*2, total)
	}
	t.Logf("ratio rule says clean: distinct*2 = %d is not < %d, missing by %d, "+
		"while %d files have lost their content",
		distinct*2, total, distinct*2-total, got.Files)
}

// TestCollapsedKeySampleIsBoundedButCountIsExact pins the two halves of the
// display contract: the list of names shown is capped, the numbers are not.
func TestCollapsedKeySampleIsBoundedButCountIsExact(t *testing.T) {
	var names []string
	const colliding = collapsedKeySampleLimit + 7
	for i := 0; i < colliding; i++ {
		k := "к" + itoa(i)
		names = append(names, k, k)
	}

	got := collapsedKeysOf(names)
	if got.Files != colliding {
		t.Errorf("Files = %d, want %d", got.Files, colliding)
	}
	if got.Keys != colliding {
		t.Errorf("Keys = %d, want %d", got.Keys, colliding)
	}
	if len(got.Sample) != collapsedKeySampleLimit {
		t.Errorf("len(Sample) = %d, want the cap %d", len(got.Sample), collapsedKeySampleLimit)
	}
	if !slices.IsSorted(got.Sample) {
		t.Errorf("Sample = %v is not sorted; the sample must be stable across runs, and "+
			"Go map iteration order is randomised", got.Sample)
	}

	// Positive control: below the cap the sample is complete, so the assertion
	// above is measuring a cap and not a constant.
	small := collapsedKeysOf([]string{"a", "a", "b", "b"})
	if len(small.Sample) != 2 {
		t.Fatalf("positive control failed: 2 colliding names produced a sample of %d",
			len(small.Sample))
	}
}

// bareIndex builds the minimum Index the bulk loaders touch: they populate
// names/contentByName/pathByName/pathToDocID and nothing else, so no bleve shard
// and no cache directory is needed to exercise them.
func bareIndex(t *testing.T, dir string) *Index {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Index{
		dir:           dir,
		contentByName: make(map[string]cachedModule),
		pathByName:    make(map[string]string),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
}

// collapsingDump lays out a dump in which two files genuinely derive one key,
// and two others do not. The colliding pair is the customer's own defect shape
// restricted to what SURVIVES the anchor scan: a wrapper over a top-level
// directory that is not a dumpDirNames kind, so no anchor exists and both files
// key on the wrapper. Returns the dump root and the key the pair collapses onto.
func collapsingDump(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	mkBSLFile(t, dir, "w/Прочее/A/Ext/ObjectModule.bsl", "Процедура А()\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "w/Прочее/B/Ext/ObjectModule.bsl", "Процедура Б()\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl", "Процедура В()\nКонецПроцедуры\n")
	mkBSLFile(t, dir, "Catalogs/Контрагенты/Ext/ObjectModule.bsl", "Процедура Г()\nКонецПроцедуры\n")

	a := bslPathToModuleName("w/Прочее/A/Ext/ObjectModule.bsl")
	b := bslPathToModuleName("w/Прочее/B/Ext/ObjectModule.bsl")
	if a != b {
		t.Fatalf("fixture is not a collision: %q vs %q. The two paths must derive ONE key "+
			"or this test measures nothing", a, b)
	}
	// And the control pair must NOT collide, or every count below would be
	// satisfied by a detector that flags everything.
	c := bslPathToModuleName("Catalogs/Номенклатура/Ext/ObjectModule.bsl")
	d := bslPathToModuleName("Catalogs/Контрагенты/Ext/ObjectModule.bsl")
	if c == d {
		t.Fatalf("fixture control is broken: the two Catalogs paths also collide (%q)", c)
	}
	return dir, a
}

// TestLoadPathsReportCollapsedKeys runs the two filesystem loaders over the same
// colliding dump and requires both to report the same exact numbers. They are
// separate code paths (cold build with content, fast startup without) and a fix
// applied to one of them only would leave the other lying.
func TestLoadPathsReportCollapsedKeys(t *testing.T) {
	dir, key := collapsingDump(t)

	loaders := map[string]func(*Index) error{
		"loadBSLFiles": func(idx *Index) error { return idx.loadBSLFiles(dir) },
		"loadBSLPaths": func(idx *Index) error { return idx.loadBSLPaths(dir) },
	}
	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			idx := bareIndex(t, dir)
			if err := load(idx); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if n := idx.ModuleCount(); n != 4 {
				t.Fatalf("ModuleCount = %d, want 4: the fixture wrote four .bsl files", n)
			}
			if n := idx.CollapsedKeyCount(); n != 1 {
				t.Errorf("CollapsedKeyCount = %d, want 1", n)
			}
			st := idx.CollapsedKeys()
			if st.Keys != 1 {
				t.Errorf("Keys = %d, want 1", st.Keys)
			}
			if !slices.Contains(st.Sample, key) {
				t.Errorf("Sample = %v, want it to name the collapsed key %q", st.Sample, key)
			}
			// The number that lies, stated next to the one that does not: four
			// files were counted, one of them can no longer be read back.
			if len(idx.pathByName) != idx.ModuleCount()-idx.CollapsedKeyCount() {
				t.Errorf("pathByName holds %d entries but ModuleCount-CollapsedKeyCount = %d; "+
					"the collapse count does not describe the map it is about",
					len(idx.pathByName), idx.ModuleCount()-idx.CollapsedKeyCount())
			}
		})
	}
}

// TestCleanDumpReportsNoCollapse is the negative control for the test above: a
// dump with no collision must report zero, or a detector that always fires would
// satisfy every assertion there.
func TestCleanDumpReportsNoCollapse(t *testing.T) {
	dir := t.TempDir()
	mkBSLFile(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl", "А\n")
	mkBSLFile(t, dir, "Catalogs/Контрагенты/Ext/ManagerModule.bsl", "Б\n")
	mkBSLFile(t, dir, "Ext/SessionModule.bsl", "В\n")

	idx := bareIndex(t, dir)
	if err := idx.loadBSLFiles(dir); err != nil {
		t.Fatal(err)
	}
	if n := idx.ModuleCount(); n != 3 {
		t.Fatalf("ModuleCount = %d, want 3", n)
	}
	if n := idx.CollapsedKeyCount(); n != 0 {
		t.Errorf("CollapsedKeyCount = %d, want 0 on a clean dump; sample %v",
			n, idx.CollapsedKeys().Sample)
	}
}

// TestCollapsedKeysZeroValueIsSafe pins that an Index which has loaded nothing
// answers zero rather than panicking on a nil atomic. Every tool response reads
// this, including responses produced while a background build is still running.
func TestCollapsedKeysZeroValueIsSafe(t *testing.T) {
	idx := &Index{}
	if n := idx.CollapsedKeyCount(); n != 0 {
		t.Errorf("CollapsedKeyCount on a fresh Index = %d, want 0", n)
	}
	if st := idx.CollapsedKeys(); st.Files != 0 || st.Keys != 0 || len(st.Sample) != 0 {
		t.Errorf("CollapsedKeys on a fresh Index = %+v, want the zero value", st)
	}
	var nilIdx *Index
	if n := nilIdx.CollapsedKeyCount(); n != 0 {
		t.Errorf("CollapsedKeyCount on a nil Index = %d, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// Census: every writer of idx.names must be accounted for.
//
// The count is computed once, where a name slice is INSTALLED, rather than on
// every read, because deriving it per call would put an O(modules) map build on
// every tool response. That choice has one failure mode: a loader added later
// that installs names and does not record the collapse, leaving CollapsedKeyCount
// describing the PREVIOUS load. This test makes that a red build instead of a
// silent wrong number.
// ---------------------------------------------------------------------------

// namesWriters lists every function in the package that writes idx.names, and
// says why each one does or does not have to record collapsed keys.
//
// RECORDING: the bulk loaders. Each installs a whole name slice derived from a
// dump or a manifest, which is the only way a duplicate can enter idx.names.
//
// EXEMPT: the runtime single-document paths. IndexDoc and IndexDocWithMeta append
// only when the id is absent from BOTH contentByName and pathByName, and
// applyIncrementalUpdate does the same, so none of them can add a duplicate. They
// also ingest documents that are not dump files, which is not what this number is
// about. DeleteDoc only removes.
var namesWriters = map[string]bool{ // func name -> must record collapsed keys
	"loadBSLFiles":            true,
	"loadBSLPaths":            true,
	"loadFromManifestAndDiff": true,
	"loadNamesReadOnly":       true,
	"swapGeneration":          true,
	"IndexDoc":                false,
	"IndexDocWithMeta":        false,
	"applyIncrementalUpdate":  false,
	"DeleteDoc":               false,
}

// funcsWritingNames parses the package's non-test sources and returns the name of
// every function containing an assignment whose left-hand side is a selector
// ending in `names`.
//
// It matches the SELECTOR and not the receiver's type, so a `.names` field on
// some other struct would be reported too. That is deliberate over-reach: the
// consequence is a build that asks a human to declare one more function, which is
// the safe direction. A type-checked version would need go/types and a full
// package load to answer a question whose wrong answer is a silent wrong number.
func funcsWritingNames(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	found := map[string]bool{}
	files := 0
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", n), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		files++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(node ast.Node) bool {
				as, ok := node.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range as.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if ok && sel.Sel.Name == "names" {
						found[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	if files == 0 {
		t.Fatalf("parsed zero non-test .go files; the census measured nothing")
	}
	if len(found) == 0 {
		t.Fatalf("the census found no writer of .names at all across %d files, which "+
			"contradicts the loaders this package is built from", files)
	}
	return found
}

// TestEveryNamesWriterIsAccountedFor fails when a function starts writing
// idx.names without being listed in namesWriters, so nobody can add a loader that
// silently leaves the collapsed-key report describing an older load.
func TestEveryNamesWriterIsAccountedFor(t *testing.T) {
	found := funcsWritingNames(t)

	var undeclared []string
	for fn := range found {
		if _, ok := namesWriters[fn]; !ok {
			undeclared = append(undeclared, fn)
		}
	}
	slices.Sort(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("these functions write idx.names but are not declared in namesWriters: %v.\n"+
			"Add each one with true if it installs a name slice (then call noteCollapsedKeys "+
			"from it) or false with the reason it cannot introduce a duplicate.", undeclared)
	}

	var vanished []string
	for fn := range namesWriters {
		if !found[fn] {
			vanished = append(vanished, fn)
		}
	}
	slices.Sort(vanished)
	if len(vanished) > 0 {
		t.Errorf("namesWriters lists %v, which no longer write idx.names. A stale census "+
			"hides a real one: remove them.", vanished)
	}
}

// TestEveryRecordingWriterCallsNoteCollapsedKeys checks the other half: a
// function declared as recording must actually contain the call. Without this,
// namesWriters is a list of intentions.
func TestEveryRecordingWriterCallsNoteCollapsedKeys(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	calls := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", n), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "noteCollapsedKeys" {
					calls[fn.Name.Name] = true
				}
				return true
			})
		}
	}

	var missing []string
	for fn, mustRecord := range namesWriters {
		if mustRecord && !calls[fn] {
			missing = append(missing, fn)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("declared as recording but containing no noteCollapsedKeys call: %v", missing)
	}

	var unexpected []string
	for fn := range calls {
		if mustRecord, declared := namesWriters[fn]; declared && !mustRecord {
			unexpected = append(unexpected, fn)
		}
	}
	slices.Sort(unexpected)
	if len(unexpected) > 0 {
		t.Errorf("declared as exempt but calling noteCollapsedKeys: %v. Either the "+
			"exemption reason is wrong or the call is", unexpected)
	}

	if len(calls) == 0 {
		t.Fatalf("no function calls noteCollapsedKeys anywhere; the check above cannot fail")
	}
}

// itoa avoids pulling strconv into the test file's import list for one use.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
