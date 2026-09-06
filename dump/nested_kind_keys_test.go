package dump

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Nested metadata kinds: one path, one key.
//
// A nested kind has no top-level dump directory, so its module path carries an
// extra "<КаталогВыгрузки>/<Имя>" pair inside the parent's directory.
// subdirSegmentNames is what turns such a pair into a ".<Вид>.<Имя>." infix, and a
// pair the table does not know contributes NOTHING to the key.
//
// The names live in testdata/nested_kind_dirs.txt, which cites them; a literal here
// would be a second copy free to drift away from the first, and a "fix" that edited
// only the copy would make these tests pass without moving a single key.
// ---------------------------------------------------------------------------

// dumpDirForRussianKind inverts dumpDirNames. It fails rather than guesses when two
// dump directories claim one Russian name, because then the inverse is not a
// function and every path built through it would be built on a coin toss.
func dumpDirForRussianKind(t *testing.T) map[string]string {
	t.Helper()
	inv := make(map[string]string, len(dumpDirNames))
	for dir, ru := range dumpDirNames {
		if prev, dup := inv[ru]; dup {
			t.Fatalf("dumpDirNames maps both %q and %q to %q, so its inverse is not a "+
				"function and no path can be built through it", prev, dir, ru)
		}
		inv[ru] = dir
	}
	return inv
}

// nestedKindChain walks a nested kind up to the top-level dump directory that
// ultimately contains it, and returns that directory together with the chain of
// nested-kind directories from the outermost one down to dir inclusive.
//
// The walk is the reason a doubly nested kind needs no special case anywhere in this
// file: ТаблицаИзмерения names Куб as its parent, Куб is itself nested and names
// ВнешнийИсточникДанных, and only that one has a directory of its own.
func nestedKindChain(t *testing.T, rows []nestedKindRow, dir string) (top string, chain []string) {
	t.Helper()
	byRu := map[string]nestedKindRow{}
	byDir := map[string]nestedKindRow{}
	for _, r := range rows {
		byRu[r.singularRu] = r
		byDir[r.dir] = r
	}
	inv := dumpDirForRussianKind(t)

	r, ok := byDir[dir]
	if !ok {
		t.Fatalf("no fixture row for %q", dir)
	}
	chain = []string{r.dir}
	cur := r.parentRu
	for range rows { // bounded: a chain cannot be longer than the fixture
		if parent, nested := byRu[cur]; nested {
			chain = append([]string{parent.dir}, chain...)
			cur = parent.parentRu
			continue
		}
		break
	}
	top, ok = inv[cur]
	if !ok {
		t.Fatalf("row %q ends at parent «%s», which has no dump directory in dumpDirNames "+
			"and is not a nested kind either, so the chain has no top", dir, cur)
	}
	return top, chain
}

// nestedProbe is one nested kind's family: the module of the object that OWNS the
// kind, and the modules of two of its children under that kind.
type nestedProbe struct {
	dir      string
	owner    string
	children []string
}

// nestedProbeFor builds that family for one fixture row, with a distinct object name
// at every level so that two paths of the family can never be equal as strings.
func nestedProbeFor(t *testing.T, rows []nestedKindRow, dir, tail string) nestedProbe {
	t.Helper()
	top, chain := nestedKindChain(t, rows, dir)

	// The owner's own path: the top-level object, then one object per nested level
	// ABOVE dir.
	prefix := top + "/Об0"
	for i, c := range chain[:len(chain)-1] {
		prefix += "/" + c + "/Об" + fmt.Sprint(i+1)
	}
	p := nestedProbe{dir: dir, owner: prefix + "/" + tail}
	for _, child := range []string{"Дитя1", "Дитя2"} {
		p.children = append(p.children, prefix+"/"+dir+"/"+child+"/"+tail)
	}
	return p
}

// collidingPaths groups paths by the key they mint and returns the groups holding
// more than one path, rendered for a failure message. An empty result means every
// path in the set minted a key of its own.
func collidingPaths(paths []string) []string {
	byKey := map[string][]string{}
	for _, p := range paths {
		k := bslPathToModuleName(p)
		byKey[k] = append(byKey[k], p)
	}
	var out []string
	for k, ps := range byKey {
		if len(ps) > 1 {
			out = append(out, fmt.Sprintf("%s <- %s", k, strings.Join(ps, " , ")))
		}
	}
	sort.Strings(out)
	return out
}

// TestNestedKindPathsDoNotCollideWithTheirParent is the (1 parent + N children) -> 1
// collapse, for every nested kind the fixture declares.
func TestNestedKindPathsDoNotCollideWithTheirParent(t *testing.T) {
	rows := readNestedKindDirs(t)

	// Positive control FIRST, on the checker itself. A pair that is distinct at the
	// tip must come back clean, or the checker reports everything and its verdicts
	// below carry no information.
	clean := []string{
		"Catalogs/Ном/Forms/Ф1/Ext/Form/Module.bsl",
		"Catalogs/Ном/Forms/Ф2/Ext/Form/Module.bsl",
	}
	if bad := collidingPaths(clean); len(bad) > 0 {
		t.Fatalf("control failed: the checker calls two forms of one catalog a collision: %v", bad)
	}
	// And the other direction: it must actually see one.
	if bad := collidingPaths([]string{clean[0], clean[0]}); len(bad) != 1 {
		t.Fatalf("control failed: the checker did not see a path colliding with itself, "+
			"reporting %v", bad)
	}

	checked := 0
	for _, r := range rows {
		p := nestedProbeFor(t, rows, r.dir, "Ext/ManagerModule.bsl")
		all := append([]string{p.owner}, p.children...)
		if bad := collidingPaths(all); len(bad) > 0 {
			t.Errorf("nested kind %q: the owner and its children share a key: %v", r.dir, bad)
		}
		checked++
	}
	if checked != len(rows) {
		t.Fatalf("checked %d of %d fixture rows", checked, len(rows))
	}
}

// TestEveryFormOfANestedObjectKeysDistinctly is what forbids widening
// subdirSegmentNames on its own. Every ordered pair of nested kinds is swept, so the test cannot be satisfied
// by teaching the derivation about one pair.
func TestEveryFormOfANestedObjectKeysDistinctly(t *testing.T) {
	rows := readNestedKindDirs(t)

	pairs := 0
	for _, outer := range rows {
		top, chain := nestedKindChain(t, rows, outer.dir)
		prefix := top + "/Об0"
		for i, c := range chain[:len(chain)-1] {
			prefix += "/" + c + "/Об" + fmt.Sprint(i+1)
		}
		for _, inner := range rows {
			for _, tail := range []string{"Ext/ManagerModule.bsl", "Ext/Form/Module.bsl"} {
				paths := []string{
					prefix + "/" + outer.dir + "/O1/" + inner.dir + "/I1/" + tail,
					prefix + "/" + outer.dir + "/O1/" + inner.dir + "/I2/" + tail,
					prefix + "/" + outer.dir + "/O2/" + inner.dir + "/I1/" + tail,
				}
				if bad := collidingPaths(paths); len(bad) > 0 {
					t.Errorf("%s below %s, tail %s: %v", inner.dir, outer.dir, tail, bad)
				}
				pairs++
			}
		}
	}
	if want := 2 * len(rows) * len(rows); pairs != want {
		t.Fatalf("swept %d combinations, want %d; the sweep is not covering the fixture", pairs, want)
	}
}

// TestDoublyNestedKindsKeyDistinctlyFromTheirParent is the second nesting level: a
// cube's own module, both of its dimension tables', and the external data source
// above all three.
func TestDoublyNestedKindsKeyDistinctlyFromTheirParent(t *testing.T) {
	rows := readNestedKindDirs(t)

	deep := 0
	for _, r := range rows {
		top, chain := nestedKindChain(t, rows, r.dir)
		if len(chain) < 2 {
			continue
		}
		deep++
		p := nestedProbeFor(t, rows, r.dir, "Ext/ManagerModule.bsl")
		// Everything above the owner too: the top-level object and each intermediate.
		all := []string{top + "/Об0/Ext/ManagerModule.bsl", p.owner}
		all = append(all, p.children...)
		if bad := collidingPaths(all); len(bad) > 0 {
			t.Errorf("doubly nested %q: %v", r.dir, bad)
		}
	}
	if deep == 0 {
		t.Fatalf("no fixture row is nested two levels deep, so this test measured nothing")
	}
}

// TestAWrappedNestedKindPathStillAnchors is the anchor scan over the same class.
func TestAWrappedNestedKindPathStillAnchors(t *testing.T) {
	rows := readNestedKindDirs(t)

	// Control: a shape the scan already handles must anchor at 1, so a failure below
	// is the nested path and not the scan being off altogether.
	const known = "wrapper/Catalogs/Ном/Ext/ObjectModule.bsl"
	if got := anchorIndex(strings.Split(known, "/")); got != 1 {
		t.Fatalf("control failed: anchorIndex(%q) = %d, want 1", known, got)
	}

	for _, r := range rows {
		p := nestedProbeFor(t, rows, r.dir, "Ext/ManagerModule.bsl")
		for _, bare := range append([]string{p.owner}, p.children...) {
			wrapped := "wrapper/" + bare
			if got := anchorIndex(strings.Split(wrapped, "/")); got != 1 {
				t.Errorf("anchorIndex(%q) = %d, want 1", wrapped, got)
			}
			if got, want := bslPathToModuleName(wrapped), bslPathToModuleName(bare); got != want {
				t.Errorf("wrapped %q keys as %q, the same path unwrapped keys as %q",
					wrapped, got, want)
			}
		}
	}
}
