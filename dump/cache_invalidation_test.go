package dump

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// cache_invalidation_test.go answers two questions that had contradictory answers
// in the notes leading up to this branch: WHICH constant governs whether a warm
// generation is reused, and does bumping it actually reach a user who already has
// one.
//
// THE INVENTORY, resolved from the source rather than from memory. Package dump
// declares SIX version constants and no others:
//
//	genSigVersion          generation.go   the gensig DERIVATION: how GenSig walks
//	                                       and hashes a dump. Folded into gensig.
//	dumpIndexSchemaVersion generation.go   the LOGICAL index schema, including the
//	                                       docID derivation. Folded into gensig AND
//	                                       stamped into the manifest as "sv". THIS
//	                                       is the one that governs reuse.
//	zapSegmentVersion      generation.go   the on-disk scorch segment format.
//	                                       Folded into gensig, stamped as "zv".
//	baselineSchemaVersion  generation.go   FROZEN history, for a manifest written
//	baselineZapVersion     generation.go   before those two were stamped. Never bumped.
//	manifestVersion        manifest.go     the manifest FILE format ("v"). NOT part
//	                                       of gensig; a mismatch makes LoadManifest
//	                                       return nil and the caller re-walk.
//
// currentCacheVersion and cacheSchemaVersion, which earlier analyses named as the
// governing constants, DO NOT EXIST in this repository. That is why the advice
// disagreed with itself.
//
// WRITING THEM DOWN HERE CHANGED THE ANSWER TO THE QUESTION THAT ESTABLISHES IT,
// which is worth saying because it is the ordinary way such a claim rots. A
// repository-wide search for either name now hits this file, and the commit
// message that introduced it. The search that still means something excludes both:
// the .git directory and the file naming the ghosts. Run that way it returns
// nothing, and the structural half of the claim is enforced by
// TestVersionConstantsAreExactlyTheseAndOnlyThese below, which reads const
// declarations out of the non-test sources of this package and so cannot be
// satisfied by a mention in prose.
//
// TWO INDEPENDENT PATHS ENFORCE dumpIndexSchemaVersion, which is why a bump is
// sufficient and not merely likely to work:
//
//  1. genSig writes "schema-v%d" into the hash, so a bumped version yields a
//     different gensig, which is a different generation DIRECTORY, so the old one
//     is not adopted; it is not even looked for.
//  2. the legacy flat cache is gated separately by flatCacheSchemaStale, which
//     compares the manifest's stamped schema against the current constant.

// writeBSL creates a .bsl under dir at rel.
func writeBSL(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSchemaBumpForcesAColdRebuildAndTheSameVersionReuses is the invalidation
// proof, taken by building a generation UNDER THE PREVIOUS SCHEMA VERSION and then
// asking the current binary for one.
//
// genSig is parameterised precisely so this can be done without a second binary,
// and its own doc says so. The generation built below is byte for byte what a v3
// binary would have adopted: same walk, same content hash, same directory naming,
// only the schema number differs.
func TestSchemaBumpForcesAColdRebuildAndTheSameVersionReuses(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	writeBSL(t, dir, "Catalogs/Номенклатура/Ext/ObjectModule.bsl", "// x\n")

	const previousSchemaVersion = 3
	if dumpIndexSchemaVersion == previousSchemaVersion {
		t.Fatalf("premise broken: dumpIndexSchemaVersion is still %d, so this test compares "+
			"a version against itself and cannot fail", previousSchemaVersion)
	}

	oldSig, err := genSig(dir, previousSchemaVersion, zapSegmentVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := BuildGeneration(dir, cache, oldSig); err != nil {
		t.Fatalf("building the v%d generation: %v", previousSchemaVersion, err)
	}
	// PREMISE: the old generation really is on disk and adoptable ON ITS OWN TERMS.
	// Without this the assertion below would pass because nothing was ever built.
	if !GenerationReady(dir, cache, oldSig) {
		t.Fatalf("premise broken: the v%d generation is not READY, so there is nothing "+
			"for the bump to invalidate", previousSchemaVersion)
	}

	newSig, err := GenSig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if newSig == oldSig {
		t.Fatalf("the schema version is not folded into the gensig: v%d and v%d both give %s. "+
			"A warm generation would be adopted across the bump and the whole fix would be "+
			"inert for every existing user.", previousSchemaVersion, dumpIndexSchemaVersion, newSig)
	}
	if GenerationReady(dir, cache, newSig) {
		t.Errorf("a generation for the CURRENT gensig %s already exists after building only "+
			"the v%d one; the bump did not force a rebuild", newSig, previousSchemaVersion)
	}

	// NEGATIVE CONTROL: the same version reuses. Build under the current gensig and
	// it is ready, and a second BuildGeneration is the documented content-addressed
	// no-op rather than a second build.
	if err := BuildGeneration(dir, cache, newSig); err != nil {
		t.Fatal(err)
	}
	if !GenerationReady(dir, cache, newSig) {
		t.Fatal("negative control failed: a generation built under the current version is not ready")
	}
	before := generationDirNames(t, cache, dir)
	if err := BuildGeneration(dir, cache, newSig); err != nil {
		t.Fatal(err)
	}
	if after := generationDirNames(t, cache, dir); !slices.Equal(before, after) {
		t.Errorf("rebuilding the SAME gensig produced a different set of generations: %v -> %v; "+
			"reuse is not happening and every open would pay a cold build", before, after)
	}
	// And both generations coexist, which is what makes the bump a rebuild rather
	// than a destructive wipe of a cache another process may still be serving.
	if len(before) != 2 {
		t.Errorf("generations on disk = %v, want the old and the new one side by side", before)
	}
}

// generationDirNames lists the generation directories in the arena, sorted.
func generationDirNames(t *testing.T, cacheDir, dumpDir string) []string {
	t.Helper()
	cpath, err := cachePath(dumpDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(generationsDir(cpath))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), buildTmpPrefix) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// TestAWarmGenerationReplaysItsPersistedDocIDs is the MECHANISM behind the bump,
// and it is the part that was argued rather than shown.
//
// The claim is that a wrongly rooted user's collapsed keys survive a code fix,
// because a generation manifest carries one DocID per file and the read-only open
// takes them verbatim. If that were false, the bump would be an unnecessary cold
// rebuild for every installation on earth. So it is measured: the manifest is
// rewritten with a key the current derivation would never produce, and the index
// serves exactly that key.
func TestAWarmGenerationReplaysItsPersistedDocIDs(t *testing.T) {
	dir := t.TempDir()
	cache := t.TempDir()
	const rel = "Catalogs/Номенклатура/Ext/ObjectModule.bsl"
	writeBSL(t, dir, rel, "// x\n")

	sig, err := GenSig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := BuildGeneration(dir, cache, sig); err != nil {
		t.Fatal(err)
	}
	cpath, err := cachePath(dir, cache)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(generationDir(cpath, sig), "manifest.json")

	// The key a wrongly rooted v3 binary would have written for this file. The
	// current derivation cannot produce it from this path, which is what makes the
	// assertion below meaningful.
	const stale = "Documents.dumps.МодульОбъекта"
	if live := bslPathToModuleName(rel); live == stale {
		t.Fatalf("premise broken: the current derivation produces %q by itself", stale)
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	entry, ok := m.Files[rel]
	if !ok {
		t.Fatalf("premise broken: the manifest has no entry for %q (entries: %v)", rel, m.Files)
	}
	if entry.DocID == stale {
		t.Fatal("premise broken: the manifest already carries the stale key")
	}
	entry.DocID = stale
	m.Files[rel] = entry
	patched, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, patched, 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := OpenGenerationReadOnly(dir, cache, sig)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	<-idx.Done()

	names := idx.ModuleNames()
	if !slices.Contains(names, stale) {
		t.Errorf("ModuleNames() = %v, want it to contain the manifest's persisted DocID %q. "+
			"If a warm generation did NOT replay its stored keys, bumping the schema version "+
			"would be an unnecessary cold rebuild for every installation.", names, stale)
	}
	if slices.Contains(names, bslPathToModuleName(rel)) {
		t.Errorf("ModuleNames() = %v contains the freshly derived key, so the open re-derived "+
			"instead of reading the manifest and this test is not measuring what it claims", names)
	}
}

// TestVersionConstantsAreExactlyTheseAndOnlyThese keeps the inventory in the tree.
//
// It exists because three separate analyses of this cache pointed at three
// differently named constants, two of which are not in the repository at all. A
// sentence in a commit message cannot stop that recurring; a test can.
func TestVersionConstantsAreExactlyTheseAndOnlyThese(t *testing.T) {
	want := []string{
		"baselineSchemaVersion",
		"baselineZapVersion",
		"dumpIndexSchemaVersion",
		"genSigVersion",
		"manifestVersion",
		"zapSegmentVersion",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, id := range vs.Names {
					if strings.HasSuffix(id.Name, "Version") {
						got = append(got, id.Name)
					}
				}
			}
		}
	}
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("the version constants of package dump are %v, want %v.\n"+
			"If one was added, say in this file which of the two questions it answers: "+
			"whether a generation may be REUSED (fold it into genSig), or how to read a "+
			"file already written (leave it out).", got, want)
	}

	// The names earlier analyses attributed the behaviour to. Asserting their
	// absence is the point: it is the finding, not an aside.
	for _, ghost := range []string{"currentCacheVersion", "cacheSchemaVersion"} {
		if slices.Contains(got, ghost) {
			t.Errorf("%q now exists; the mapping recorded at the top of this file is stale "+
				"and the reconciliation has to be redone", ghost)
		}
	}
}

// TestOnlyTheGensigConstantsDecideReuse pins WHICH of the six actually gate a
// generation, behaviourally rather than by reading the code.
func TestOnlyTheGensigConstantsDecideReuse(t *testing.T) {
	dir := t.TempDir()
	writeBSL(t, dir, "Catalogs/Ном/Ext/ObjectModule.bsl", "// x\n")

	base, err := genSig(dir, dumpIndexSchemaVersion, zapSegmentVersion)
	if err != nil {
		t.Fatal(err)
	}
	otherSchema, err := genSig(dir, dumpIndexSchemaVersion+1, zapSegmentVersion)
	if err != nil {
		t.Fatal(err)
	}
	otherZap, err := genSig(dir, dumpIndexSchemaVersion, zapSegmentVersion+1)
	if err != nil {
		t.Fatal(err)
	}
	if base == otherSchema {
		t.Error("the schema version is not folded into the gensig")
	}
	if base == otherZap {
		t.Error("the zap version is not folded into the gensig")
	}
	if otherSchema == otherZap {
		t.Error("the schema and zap versions collide in the gensig, so one can mask the other")
	}
	// And GenSig really passes the CURRENT constants, or the parameterised core
	// above would be measuring something the binary never computes.
	live, err := GenSig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if live != base {
		t.Errorf("GenSig = %s but genSig(current constants) = %s; the exported entry point "+
			"does not use the constants this test varies", live, base)
	}

	// manifestVersion is the counter-example and is asserted as one: it is stamped
	// into the file and is NOT part of the signature, so bumping it could never
	// invalidate a generation.
	if strings.Contains(gensigInput(t), "manifest-v") {
		t.Error("manifestVersion appears in the gensig header; the mapping at the top of " +
			"this file says it does not")
	}
}

// gensigInput returns the literal format string genSig hashes its version header
// with, read from the source so the assertion above is about the code and not
// about a copy of it.
func gensigInput(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("generation.go")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "gensig-v%d"
	i := strings.Index(string(src), marker)
	if i < 0 {
		t.Fatal("the gensig version header no longer starts with the marker this test looks " +
			"for, so it can no longer inspect what it claims to inspect")
	}
	line := string(src)[i:]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	return line
}
