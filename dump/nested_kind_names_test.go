package dump

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Nested metadata kinds: their Russian names, and the parent that owns them.
//
// A nested kind is one that never gets a top-level dump directory: it lives
// inside its parent's directory, so a path through it carries an extra
// "<КаталогВыгрузки>/<Имя>" pair. subdirSegmentNames is the table that turns such
// a pair into the ".<Вид>.<Имя>." infix of a module key, and today it knows two:
// Forms and Commands.
//
// EVERY OTHER NESTED KIND IS CURRENTLY INVISIBLE TO THAT TABLE, and a path
// through one keys as though the pair were not there at all. That is how a
// calculation register's own record-set module and the record-set module of its
// nested recalculation arrive at ONE key.
//
// This file does not fix that. It fixes the thing that has to come first: a
// Russian name written without a source is an invention, and the names the fix
// needs are not in this tree yet. So the names are CITED here, from the platform
// type reference, and checked against what the tree already believes, BEFORE any
// of them is used to derive a key.
// ---------------------------------------------------------------------------

// nestedKindDirsFixture pairs each nested kind with the parent property that owns
// it. Both halves are measured; see the header of the file itself for provenance.
const nestedKindDirsFixture = "testdata/nested_kind_dirs.txt"

// nestedKindRow is one line of that fixture.
type nestedKindRow struct {
	dir        string // dump subdirectory, e.g. "Forms"
	singularEn string // English singular, e.g. "Form"
	singularRu string // Russian singular, e.g. "Форма"
	parentProp string // the parent's collection property, e.g. "Формы"
	parentRu   string // the parent kind's Russian singular, e.g. "Справочник"
}

// readNestedKindDirs parses the fixture, dropping blank lines and comments.
func readNestedKindDirs(t *testing.T) []nestedKindRow {
	t.Helper()
	f, err := os.Open(filepath.FromSlash(nestedKindDirsFixture))
	if err != nil {
		t.Fatalf("open fixture %s: %v", nestedKindDirsFixture, err)
	}
	defer f.Close()

	var rows []nestedKindRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 5 {
			t.Fatalf("fixture %s: line %q has %d fields, want <КаталогВыгрузки> <АнглИмяЕд> "+
				"<РусИмяЕд> <СвойствоКоллекцияРодителя> <РусИмяРодителя>",
				nestedKindDirsFixture, line, len(fields))
		}
		rows = append(rows, nestedKindRow{fields[0], fields[1], fields[2], fields[3], fields[4]})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("fixture %s parsed to zero rows; the parser or the file is broken",
			nestedKindDirsFixture)
	}
	return rows
}

// nestedKindRussianNames is the set of Russian singulars the fixture declares
// nested. It is what tells the plainModuleDirs rule that these kinds are not
// missing from dumpDirNames but are categorically outside it: a nested kind has no
// top-level dump directory to be named by.
func nestedKindRussianNames(t *testing.T) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	for _, r := range readNestedKindDirs(t) {
		set[r.singularRu] = true
	}
	return set
}

// segmentNameMismatches returns the rows whose Russian singular disagrees with the
// segment dictionary, considering only rows the dictionary actually knows. The
// dictionary is a parameter so the test can run the same checker against a
// deliberately damaged copy as a positive control.
func segmentNameMismatches(dict map[string]string, rows []nestedKindRow) []string {
	var bad []string
	for _, r := range rows {
		ru, ok := dict[r.dir]
		if !ok {
			continue
		}
		if ru != r.singularRu {
			bad = append(bad, r.dir)
		}
	}
	sort.Strings(bad)
	return bad
}

// unownedRows returns the rows whose named parent property is not in the parent's
// property table. props is a parameter for the same positive-control reason.
func unownedRows(props map[string][]string, rows []nestedKindRow) []string {
	var bad []string
	for _, r := range rows {
		p, ok := props[r.parentRu]
		if !ok {
			continue // reported separately: a parent with no snapshot at all
		}
		if !slices.Contains(p, r.parentProp) {
			bad = append(bad, r.dir)
		}
	}
	sort.Strings(bad)
	return bad
}

// nestedKindsNotYetInSegmentDict pins the nested kinds whose Russian name is cited
// here but which subdirSegmentNames does not yet carry.
//
// THE PIN IS WHAT LETS THIS FILE END GREEN WITHOUT PRETENDING THE WORK IS DONE.
// Checking the dictionary for a kind it does not contain would be red for a reason
// that is not a defect: the name is cited, and wiring it into key derivation is a
// separate change with its own key movements to account for. Skipping those rows
// silently would be worse, because the file would then shrink to a test of Forms
// and Commands and nobody would see it happen. So the rows are skipped BY NAME,
// and the set of skipped names is asserted in BOTH directions: a kind wired into
// the dictionary without being taken off this list fails, and a kind added to the
// fixture without either a dictionary entry or a line here fails too.
//
// IT IS EMPTY NOW, and it is kept rather than deleted because the both-directions
// assertion it feeds is what makes a NEW fixture row visible: add one without also
// wiring it into the dictionary and the test names it here.
var nestedKindsNotYetInSegmentDict = []string{}

// TestNestedKindNamesAreCitedAndOwnedByTheirParent is the whole of this cluster's
// claim: every Russian name the nested-kind work will need is a citation, and the
// parent each one is attached to really does own it.
func TestNestedKindNamesAreCitedAndOwnedByTheirParent(t *testing.T) {
	rows := readNestedKindDirs(t)
	props := kindProperties(t)

	// 1. Every parent must have a property snapshot, or the ownership half of this
	// test is measuring nothing for that row.
	for _, r := range rows {
		if _, ok := props[r.parentRu]; !ok {
			t.Errorf("row %q names parent «%s», which %s does not cover, so the claim that "+
				"«%s» owns this kind rests on nothing this tree can check",
				r.dir, r.parentRu, kindPropertiesFixture, r.parentRu)
		}
	}

	// 2. The parent really owns the kind, through the named collection property.
	if bad := unownedRows(props, rows); len(bad) > 0 {
		t.Errorf("rows %v name a collection property their parent does not have in %s. "+
			"A parent that does not own the kind cannot be where a path through it comes from.",
			bad, kindPropertiesFixture)
	}

	// 3. Where the segment dictionary already knows the directory, the cited name
	// must be exactly what the dictionary emits. This is the half that catches a
	// name typed from knowledge: it has to agree with a table that is already
	// shipping keys.
	if bad := segmentNameMismatches(subdirSegmentNames, rows); len(bad) > 0 {
		t.Errorf("rows %v cite a Russian singular that disagrees with subdirSegmentNames. "+
			"That table is what a shipped key already carries, so a disagreement is a name "+
			"somebody typed rather than read.", bad)
	}

	// 4. The English singular is cited too, and identifies exactly one kind. Without
	// this the column would be carried but never read, which is how a copied line
	// keeps a neighbour's name without anything noticing.
	seenEn := map[string]string{}
	for _, r := range rows {
		if prev, dup := seenEn[r.singularEn]; dup {
			t.Errorf("rows %q and %q both cite the English singular %q; one of them is a copied "+
				"line that kept its neighbour's name", prev, r.dir, r.singularEn)
		}
		seenEn[r.singularEn] = r.dir
	}

	// 5. Counters. A checker that examined nothing must not report success, and the
	// dictionary half must have examined at least one row of its own.
	checkedOwnership, checkedDict := 0, 0
	for _, r := range rows {
		if _, ok := props[r.parentRu]; ok {
			checkedOwnership++
		}
		if _, ok := subdirSegmentNames[r.dir]; ok {
			checkedDict++
		}
	}
	if checkedOwnership == 0 {
		t.Fatalf("no row had a parent in %s, so the ownership check measured nothing",
			kindPropertiesFixture)
	}
	if checkedDict == 0 {
		t.Fatalf("no row was in subdirSegmentNames, so the name check measured nothing; " +
			"Forms and Commands are in the fixture precisely so this cannot happen")
	}

	// 6. The pin, both directions.
	var absent []string
	for _, r := range rows {
		if _, ok := subdirSegmentNames[r.dir]; !ok {
			absent = append(absent, r.dir)
		}
	}
	sort.Strings(absent)
	want := slices.Clone(nestedKindsNotYetInSegmentDict)
	sort.Strings(want)
	if !slices.Equal(absent, want) {
		t.Errorf("nested kinds missing from subdirSegmentNames = %v, pinned as %v. "+
			"Either a kind was wired in without being taken off the pin, or one was added "+
			"to the fixture without a line either way.", absent, want)
	}
}

// TestNestedKindCheckersRejectAPluralAndAWrongOwner is the negative control for
// both checkers above. A green verdict from a checker that has not been shown to go
// red proves nothing, and the specific error each one exists to catch is a PLURAL
// standing where a singular belongs: «Формы» is the parent's property, «Форма» is
// the kind, and the two are one letter apart.
func TestNestedKindCheckersRejectAPluralAndAWrongOwner(t *testing.T) {
	rows := readNestedKindDirs(t)
	props := kindProperties(t)

	// Control A: the segment-name checker must reject the plural. The dictionary is
	// damaged rather than the fixture, so the shipped fixture is untouched.
	const control = "Forms"
	if _, ok := subdirSegmentNames[control]; !ok {
		t.Fatalf("positive control is broken: %q is not in subdirSegmentNames to begin with", control)
	}
	damagedDict := make(map[string]string, len(subdirSegmentNames))
	for k, v := range subdirSegmentNames {
		damagedDict[k] = v
	}
	damagedDict[control] = "Формы" // the collection plural, where the singular belongs
	if got := segmentNameMismatches(damagedDict, rows); !slices.Equal(got, []string{control}) {
		t.Fatalf("positive control failed: with %q set to the plural the checker reported %v, "+
			"want exactly [%s]. It cannot tell a plural from a singular, so its green verdict "+
			"above is worthless.", control, got, control)
	}

	// Control B: the ownership checker must reject a row whose parent property is
	// the SINGULAR. Справочник has «Формы» and does not have «Форма».
	damagedRows := slices.Clone(rows)
	hit := -1
	for i, r := range damagedRows {
		if r.dir == control {
			hit = i
			break
		}
	}
	if hit < 0 {
		t.Fatalf("positive control is broken: no %q row in %s", control, nestedKindDirsFixture)
	}
	damagedRows[hit].parentProp = "Форма" // the singular, where the collection belongs
	if got := unownedRows(props, damagedRows); !slices.Equal(got, []string{control}) {
		t.Fatalf("positive control failed: with the %q row pointed at a property its parent "+
			"does not have the checker reported %v, want exactly [%s]", control, got, control)
	}
}
