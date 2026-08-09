package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/bsl"
	"github.com/feenlace/mcp-1c/internal/instructions"
	"github.com/feenlace/mcp-1c/onec"
)

// instructions_contract_test.go holds the half of the instruction-text guard that
// has to CALL something.
//
// THE RULE THESE ENFORCE. Every sentence in internal/instructions must be
// decidable from Go compiled into this binary, because the 1C extension is
// installed and versioned separately and cmd/mcp-1c treats its version as a floor,
// so a claim whose truth lives in Module.bsl is false for part of the installed
// base. The consequence for a guard is direct: an assertion here may only ever
// call a Go renderer or read a Go corpus. There is no fixture in this file that
// stands in for the extension, because there is no sentence left that needs one.
//
// AND THEY CALL THE RENDERER, NEVER GREP THE SOURCE. A grep for «Всего» in
// eventlog.go proves a literal exists somewhere in a file. It does not prove the
// literal is reached, that it is reached on the path the text describes, or that
// nothing downstream rewrites it. Only the rendered string is the thing the model
// reads, so only the rendered string is asserted on.

// ---------------------------------------------------------------------------
// The refusal vocabulary (paragraph 2).
// ---------------------------------------------------------------------------

// instrRefusalHeadingPattern is the shape the text promises the model: the first
// line of a refusal says that the thing asked for was not done, not obtained or
// not read.
var instrRefusalHeadingPattern = regexp.MustCompile(
	`не (выполнен|выполнена|выполнено|выполнены|получен|получена|получено|получены|прочитан|прочитана|прочитано|прочитаны)$`)

// instrHeadingConsts reads the heading constants out of the package SOURCE rather
// than repeating them, so an eleventh tool shipping a heading like «Ошибка
// анализа» is caught by a guard nobody had to remember to update.
//
// It takes dir as a parameter for the same reason censusTools does: so the walk
// can be aimed somewhere empty and shown to report nothing rather than agreement.
func instrHeadingConsts(dir string) (map[string]string, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if !strings.HasPrefix(name.Name, "heading") || i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						v, err := strconv.Unquote(lit.Value)
						if err != nil {
							continue
						}
						out[name.Name] = v
					}
				}
			}
		}
	}
	return out, nil
}

// TestInstructionsRefusalVocabularyIsClosed is paragraph 2's guard.
func TestInstructionsRefusalVocabularyIsClosed(t *testing.T) {
	headings, err := instrHeadingConsts(".")
	if err != nil {
		t.Fatalf("parsing the package source: %v", err)
	}

	// PREMISE: the walk found the constants. Zero would agree with everything.
	if len(headings) < 10 {
		t.Fatalf("the heading census found %d constants; the package had ten refusal headings when "+
			"this guard was written, so the walk is measuring the wrong thing", len(headings))
	}

	// Positive control on the pattern itself: it must reject the shape the text
	// would not describe. Without this, a pattern loosened to `.*` passes silently.
	if instrRefusalHeadingPattern.MatchString("Ошибка анализа") {
		t.Fatal("control failed: the refusal pattern accepts «Ошибка анализа», which the text's " +
			"sentence does not describe, so it accepts anything")
	}

	names := make([]string, 0, len(headings))
	for name := range headings {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !instrRefusalHeadingPattern.MatchString(headings[name]) {
			t.Errorf("%s = %q does not end the way the instruction text tells the model a refusal reads; "+
				"either the heading or the sentence has to change", name, headings[name])
		}
	}

	// AND THE HEADING IS LITERALLY THE FIRST LINE, which is the part of the
	// sentence that says «первая строка». Rendered, not grepped.
	rendered := renderFailure(headings["headingQuery"], fmt.Errorf("boom"))
	firstLine := strings.SplitN(rendered, "\n", 2)[0]
	if want := "## " + headings["headingQuery"]; firstLine != want {
		t.Errorf("the first line of a rendered refusal is %q, want %q", firstLine, want)
	}
}

// ---------------------------------------------------------------------------
// execute_query rendering (paragraph 4).
// ---------------------------------------------------------------------------

// TestInstructionsQueryRendererKeepsEveryColumnAndCell is paragraph 4's guard:
// «сервер печатает каждую колонку каждой строки целиком и ничего в ячейках не
// сокращает».
func TestInstructionsQueryRendererKeepsEveryColumnAndCell(t *testing.T) {
	long := strings.Repeat("Ы", 5000)
	res := &onec.QueryResult{
		Columns: []string{"КолонкаA", "КолонкаB", "КолонкаC"},
		Rows: [][]any{
			{"r1c1", "r1c2", long},
			{"r2c1", "r2c2", "r2c3"},
			{"r3c1", "r3c2", "r3c3"},
		},
		Total: 3,
	}
	out := formatQueryResult(res)

	for _, col := range res.Columns {
		if !strings.Contains(out, col) {
			t.Errorf("the rendered table drops the column %q", col)
		}
	}
	for i, row := range res.Rows {
		for j, cell := range row {
			s := cell.(string)
			if !strings.Contains(out, s) {
				t.Errorf("row %d column %d is missing from the rendered table (%d runes)", i, j, len([]rune(s)))
			}
		}
	}

	// The long cell is present WHOLE. Containing a prefix of it is what a
	// truncating renderer produces, and the assertion above would pass on the
	// short cells regardless, so this is the one that carries the claim.
	if !strings.Contains(out, long) {
		t.Errorf("the 5000 rune cell was not rendered whole; the text tells the model nothing shortens a cell")
	}

	// Control: the same containment test fails on a cell that really was cut, so a
	// pass above is the renderer keeping the value and not the test being blind.
	cut := strings.Repeat("Ы", 5000) + "ХВОСТ"
	if strings.Contains(out, cut) {
		t.Fatal("control failed: the containment test passes for a value the output does not hold")
	}
}

// ---------------------------------------------------------------------------
// get_event_log rendering (paragraph 5).
// ---------------------------------------------------------------------------

// TestInstructionsEventLogRendererHasNoTruncationNote is paragraph 5's guard. The
// sentence tells the model that this answer carries no truncation note and that
// the «Всего» line is at the end, so both halves are rendered and read here.
//
// THE EMPTY CASE IS ASSERTED TOO, because the sentence opens with «Когда записи
// есть»: without records the renderer returns early and prints no «Всего» at all,
// and a sentence that did not say so would be wrong about that answer.
func TestInstructionsEventLogRendererHasNoTruncationNote(t *testing.T) {
	full := formatEventLog(&onec.EventLogResult{
		Events: []onec.EventLogEntry{
			{Date: "2026-03-01T10:00:00", Level: "Ошибка", Event: "Событие1", User: "Иванов"},
			{Date: "2026-03-01T11:00:00", Level: "Ошибка", Event: "Событие2", User: "Петров"},
		},
		Total: 900,
	})

	if !strings.Contains(full, "\nВсего: 900\n") {
		t.Errorf("the «Всего» line the text tells the model to compare against is not in the answer:\n%s", full)
	}
	if !strings.HasSuffix(strings.TrimRight(full, "\n"), "Всего: 900") {
		t.Errorf("«Всего» is no longer the last line, so «в конце отдельную строку» is stale:\n%s", full)
	}
	if strings.Contains(strings.ToLower(full), "усеч") {
		t.Errorf("the answer now carries a truncation note, and the text says it does not:\n%s", full)
	}

	// Control: the scan for the note can see one. Without it a renderer that
	// started emitting «Результат усечён» could pass on a broken comparison.
	if !strings.Contains(strings.ToLower(full+"Результат усечён."), "усеч") {
		t.Fatal("control failed: the truncation-note scan cannot see a note even when one is appended")
	}

	empty := formatEventLog(&onec.EventLogResult{Total: 900})
	if strings.Contains(empty, "Всего") {
		t.Errorf("an answer with no records now prints «Всего», so the sentence's «Когда записи есть» "+
			"qualifier is wrong in the opposite direction:\n%s", empty)
	}
}

// ---------------------------------------------------------------------------
// get_metadata_tree rendering (paragraph 3).
// ---------------------------------------------------------------------------

// TestInstructionsMetadataSummaryIsOneLinePerCategory is paragraph 3's guard for
// «короткая сводка, по строке на категорию» and for «напечатано как filter="..."».
func TestInstructionsMetadataSummaryIsOneLinePerCategory(t *testing.T) {
	// PREMISE: the category table this is measured against is populated.
	if len(metadataCategories) < 3 {
		t.Fatalf("metadataCategories holds %d entries; the fixture below needs three", len(metadataCategories))
	}
	filled := metadataCategories[:3]
	emptyCat := metadataCategories[3]

	tree := map[string][]string{
		emptyCat.key: {},
	}
	for i, cat := range filled {
		tree[cat.key] = []string{fmt.Sprintf("Объект%dА", i), fmt.Sprintf("Объект%dБ", i)}
	}

	out := formatMetadataSummary(tree, nil)

	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- **") {
			rows++
		}
	}
	if rows != len(filled) {
		t.Errorf("the summary printed %d rows for %d non-empty categories; the text calls it "+
			"«по строке на категорию»:\n%s", rows, len(filled), out)
	}

	for _, cat := range filled {
		// The VALUE the text tells the model to lift is the key, printed quoted.
		if want := `filter="` + cat.key + `"`; !strings.Contains(out, want) {
			t.Errorf("the summary does not print %s, and the text tells the model to read filter from "+
				"there:\n%s", want, out)
		}
		// The LABEL is the title, and for most categories the two differ, which is
		// the whole reason the sentence points at the printed value.
		if !strings.Contains(out, "- **"+cat.title+"**") {
			t.Errorf("the summary row for %q no longer carries the title %q:\n%s", cat.key, cat.title, out)
		}
	}

	// The empty category produced no row, which is what makes the count above a
	// count of non-empty categories rather than of the table.
	if strings.Contains(out, `filter="`+emptyCat.key+`"`) {
		t.Errorf("a category with no objects produced a summary row:\n%s", out)
	}

	// AND THE FILTERED CALL RETURNS THE WHOLE CATEGORY, which is the other half of
	// the same sentence.
	cat := filled[0]
	whole := make([]string, 40)
	for i := range whole {
		whole[i] = fmt.Sprintf("Объект%02d", i)
	}
	filteredOut := formatMetadataTree(map[string][]string{cat.key: whole}, nil, cat.key)
	for _, name := range whole {
		if !strings.Contains(filteredOut, name) {
			t.Errorf("the filtered answer dropped %q, and the text says the category comes back "+
				"«целиком»:\n%s", name, filteredOut)
		}
	}
}

// ---------------------------------------------------------------------------
// bsl_syntax_help numbers (paragraph 6).
// ---------------------------------------------------------------------------

// TestInstructionsBSLNumbersAreDerived is paragraph 6's guard.
//
// NOT ONE NUMBER IS TYPED HERE. Both expectations are built by replaying the
// predicate the tool itself uses over the corpus the tool itself reads, so adding
// a single entry to bsl/functions.go turns this red, which is the rot the text
// would otherwise carry silently.
func TestInstructionsBSLNumbersAreDerived(t *testing.T) {
	all := len(bsl.Search(""))
	str := len(bsl.Search("Стр"))

	// PREMISES, all three from the corpus rather than from a document.
	if all != len(bsl.BuiltinFunctions) {
		t.Fatalf("an empty query returns %d of %d entries, so it is no longer «весь справочник»",
			all, len(bsl.BuiltinFunctions))
	}
	if all == 0 {
		t.Fatal("premise failed: the corpus is empty, so every count below is zero and agrees with nothing")
	}
	if str <= 1 {
		t.Fatalf("«Стр» matches %d entries; the sentence exists to say it matches many", str)
	}
	if n := len(bsl.Search("ZZZQQQ")); n != 0 {
		t.Fatalf("premise failed: a nonsense query matched %d entries, so Search returns everything "+
			"regardless and the counts measure nothing", n)
	}

	wantStr := fmt.Sprintf("«Стр» возвращает %d полных статей", str)
	if !strings.Contains(instructions.Text, wantStr) {
		t.Errorf("the text does not say %q; the corpus now answers «Стр» with %d entries", wantStr, str)
	}
	wantAll := fmt.Sprintf("все %d", all)
	if !strings.Contains(instructions.Text, wantAll) {
		t.Errorf("the text does not say %q; the corpus now holds %d entries", wantAll, all)
	}

	// CONTROL: the checks above are sensitive to the number and not merely to the
	// wording around it. If they were not, a corpus that grew by one would keep
	// them green, which is exactly the failure they exist to catch.
	if strings.Contains(instructions.Text, fmt.Sprintf("«Стр» возвращает %d полных статей", str+1)) {
		t.Fatal("control failed: the text matches the «Стр» count off by one, so the assertion is not " +
			"reading the number")
	}
	if strings.Contains(instructions.Text, fmt.Sprintf("все %d", all+1)) {
		t.Fatal("control failed: the text matches the corpus size off by one, so the assertion is not " +
			"reading the number")
	}

	// AND THE TOOL'S OWN DESCRIPTION AGREES. Two model-facing surfaces quoting one
	// corpus is two places to go stale, and they must not be able to go stale
	// apart.
	desc := BSLHelpTool().Description
	sizeInDesc := regexp.MustCompile(`(^|\D)` + strconv.Itoa(all) + `(\D|$)`)
	if !sizeInDesc.MatchString(desc) {
		t.Errorf("bsl_syntax_help's description does not carry the corpus size %d, so it and the "+
			"instruction text disagree about the same corpus:\n%s", all, desc)
	}
}
