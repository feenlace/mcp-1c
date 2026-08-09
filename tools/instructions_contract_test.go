package tools

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/bsl"
	"github.com/feenlace/mcp-1c/internal/instructions"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// instrStringConsts reads EVERY string constant out of the package source, keyed
// by name. Not only the ones named heading*: the census below resolves a heading
// passed as an identifier, and an identifier that happens to be called something
// else still reaches the model as a first line.
func instrStringConsts(dir string) (map[string]string, error) {
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
						if i >= len(vs.Values) {
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

// refusalSite is one call to WithToolErrors, with the heading it really passes.
type refusalSite struct {
	where   string // file:line, so a failure names the call and not a constant
	spelled string // how the argument was written: a literal, or the const's name
	heading string
}

// instrRefusalHeadings reads the heading census FROM THE CALL SITES, not from the
// constant names.
//
// WHY IT CHANGED. The census used to collect constants whose NAME began with
// «heading», which made two shapes invisible: a heading passed as a bare string
// literal, and a heading held in a const whose name starts with anything else.
// Both reach renderFailure and both become the first line the instruction text
// teaches the model to read as a refusal, and the premise that ten constants were
// found stayed satisfied by the ten survivors. Both shapes are measured: turning
// one WithToolErrors argument into the literal «Ошибка анализа», and into a const
// declared under a name the prefix filter misses, each left the shipped census
// green and each reddens this one.
//
// It takes dir as a parameter for the same reason censusTools does: so the walk
// can be aimed somewhere empty and shown to report nothing rather than agreement.
func instrRefusalHeadings(dir string) ([]refusalSite, []string, error) {
	consts, err := instrStringConsts(dir)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, nil, err
	}

	var sites []refusalSite
	var unresolved []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				fn, ok := call.Fun.(*ast.Ident)
				if !ok || fn.Name != "WithToolErrors" || len(call.Args) == 0 {
					return true
				}
				where := fset.Position(call.Pos()).String()
				switch arg := call.Args[0].(type) {
				case *ast.BasicLit:
					if arg.Kind != token.STRING {
						unresolved = append(unresolved, fmt.Sprintf("%s: heading argument is a %s literal", where, arg.Kind))
						return true
					}
					v, err := strconv.Unquote(arg.Value)
					if err != nil {
						unresolved = append(unresolved, fmt.Sprintf("%s: heading literal does not unquote: %v", where, err))
						return true
					}
					sites = append(sites, refusalSite{where: where, spelled: arg.Value, heading: v})
				case *ast.Ident:
					v, ok := consts[arg.Name]
					if !ok {
						unresolved = append(unresolved, fmt.Sprintf("%s: heading %s is not a string constant of this package", where, arg.Name))
						return true
					}
					sites = append(sites, refusalSite{where: where, spelled: arg.Name, heading: v})
				default:
					unresolved = append(unresolved, fmt.Sprintf("%s: heading argument is a %T, which this census cannot read", where, arg))
				}
				return true
			})
		}
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].where < sites[j].where })
	return sites, unresolved, nil
}

// TestInstructionsRefusalVocabularyIsClosed is paragraph 2's guard.
func TestInstructionsRefusalVocabularyIsClosed(t *testing.T) {
	sites, unresolved, err := instrRefusalHeadings(".")
	if err != nil {
		t.Fatalf("parsing the package source: %v", err)
	}

	// A heading this census cannot read is itself a failure: an unread heading is
	// an unchecked first line, and reporting agreement over the ones it could read
	// is how the previous census stayed green.
	for _, u := range unresolved {
		t.Errorf("the refusal-heading census cannot resolve a call site, so its first line is unchecked: %s", u)
	}

	// PREMISE: the walk found the call sites. Zero would agree with everything.
	if len(sites) < 10 {
		t.Fatalf("the heading census found %d WithToolErrors call sites; the package had ten when this "+
			"guard was written, so the walk is measuring the wrong thing", len(sites))
	}

	// Control: aimed somewhere with no Go source, the same walk must report nothing
	// rather than the ten it found above.
	if empty, _, err := instrRefusalHeadings(t.TempDir()); err != nil {
		t.Fatalf("control failed: the census errored on an empty directory: %v", err)
	} else if len(empty) != 0 {
		t.Fatalf("control failed: the census found %d call sites in an empty directory", len(empty))
	}

	// Positive control on the pattern itself: it must reject the shape the text
	// would not describe. Without this, a pattern loosened to `.*` passes silently.
	if instrRefusalHeadingPattern.MatchString("Ошибка анализа") {
		t.Fatal("control failed: the refusal pattern accepts «Ошибка анализа», which the text's " +
			"sentence does not describe, so it accepts anything")
	}

	for _, s := range sites {
		if s.heading == "" {
			t.Errorf("%s passes an empty heading, so the refusal has no first line at all", s.where)
			continue
		}
		if !instrRefusalHeadingPattern.MatchString(s.heading) {
			t.Errorf("%s passes %s = %q, which does not end the way the instruction text tells the model "+
				"a refusal reads; either the heading or the sentence has to change", s.where, s.spelled, s.heading)
		}

		// AND THE HEADING IS LITERALLY THE FIRST LINE, which is the part of the
		// sentence that says «первая строка». Rendered, not grepped, and rendered for
		// EVERY heading rather than for one looked up by constant name.
		//
		// The keyed form this replaced read headings["headingQuery"]. Renaming that
		// constant made the lookup return "", so the assertion compared "## " to
		// "## " and passed: an assertion that cannot fail, on the only claim in the
		// tree tying «первая строка» to a rendered artefact.
		rendered := renderFailure(s.heading, fmt.Errorf("boom"))
		firstLine := strings.SplitN(rendered, "\n", 2)[0]
		if want := "## " + s.heading; firstLine != want {
			t.Errorf("%s: the first line of a rendered refusal is %q, want %q", s.where, firstLine, want)
		}
	}
}

// ---------------------------------------------------------------------------
// execute_query rendering (paragraph 4).
// ---------------------------------------------------------------------------

// TestInstructionsQueryRendererKeepsEveryColumnAndCell is paragraph 4's guard:
// «сервер печатает каждую колонку каждой строки целиком и ничего в ячейках не
// сокращает».
//
// THE FIXTURE IS WIDE AND TALL ON PURPOSE. It used to be three columns by three
// rows, which is under any cap a renderer would plausibly grow: inserting
// «if len(res.Columns) > 20 { res.Columns = res.Columns[:20] }» or the same for
// rows left this green while the sentence became false. 40 × 200 is above both
// shapes, so the claim is guarded on the two axes a cap can live on and not only
// on the length of one cell.
func TestInstructionsQueryRendererKeepsEveryColumnAndCell(t *testing.T) {
	const cols, rows = 40, 200
	long := strings.Repeat("Ы", 5000)

	res := &onec.QueryResult{Total: rows}
	for c := 0; c < cols; c++ {
		res.Columns = append(res.Columns, fmt.Sprintf("Колонка%02d", c))
	}
	for r := 0; r < rows; r++ {
		row := make([]any, 0, cols)
		for c := 0; c < cols; c++ {
			row = append(row, fmt.Sprintf("r%03dc%02d", r, c))
		}
		res.Rows = append(res.Rows, row)
	}
	// The one oversized cell keeps the original claim («ничего в ячейках не
	// сокращает») on the axis the wide fixture does not test.
	res.Rows[rows-1][cols-1] = long

	// THE EXPECTATION IS SNAPSHOTTED BEFORE THE CALL. formatQueryResult takes a
	// POINTER, so a renderer that caps by reslicing (`r.Columns = r.Columns[:20]`)
	// shortens the very slice the assertions below would range over. Measured: with
	// the loops reading res.Columns after the call, a 20-column cap left this test
	// green while the header it rendered was half the table.
	wantColumns := append([]string(nil), res.Columns...)
	wantCells := make([][]string, len(res.Rows))
	for i, row := range res.Rows {
		wantCells[i] = make([]string, len(row))
		for j, cell := range row {
			wantCells[i][j] = cell.(string)
		}
	}

	out := formatQueryResult(res)

	for _, col := range wantColumns {
		if !strings.Contains(out, col) {
			t.Errorf("the rendered table drops the column %q", col)
		}
	}
	for i, row := range wantCells {
		for j, s := range row {
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

	// AND THE FILTERED CALL RETURNS THE OBJECT LIST, which is the other half of the
	// same sentence.
	//
	// NOT «вся категория целиком». That wording is retired and forbidden by
	// retiredClaims in internal/instructions, because NewMetadataHandler runs
	// filterNoise BEFORE the filter branch, so the answer is short by every name
	// ending in a noise suffix. What the sentence obliges THIS renderer to do is
	// drop nothing of what it is handed; the one removal the text accounts for
	// happens in the handler, and TestInstructionsFilteredCategoryDropsOnlyAttachedFiles
	// is what reads that.
	cat := filled[0]
	whole := make([]string, 40)
	for i := range whole {
		whole[i] = fmt.Sprintf("Объект%02d", i)
	}
	filteredOut := formatMetadataTree(map[string][]string{cat.key: whole}, nil, cat.key)
	for _, name := range whole {
		if !strings.Contains(filteredOut, name) {
			t.Errorf("the renderer dropped %q from the filtered answer, and the text tells the model a "+
				"filtered call returns «список объектов категории» with only the names ending in "+
				"ПрисоединенныеФайлы taken out; that removal is the handler's, so anything this renderer "+
				"drops is a name the text does not account for:\n%s", name, filteredOut)
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
	// DELIMITED, not a bare Contains. «все 180» is a prefix of «все 1800», so the
	// bare containment this replaced was satisfied by a text claiming ten times the
	// corpus, and by «все 18000» too. An extra trailing digit is exactly the typo a
	// hand edit produces, and the sentence exists to make the model size a call.
	wantAll := fmt.Sprintf("все %d", all)
	allDelimited := regexp.MustCompile(`все ` + strconv.Itoa(all) + `(\D|$)`)
	if !allDelimited.MatchString(instructions.Text) {
		t.Errorf("the text does not say %q as a whole number; the corpus now holds %d entries", wantAll, all)
	}
	if strings.Contains(instructions.Text, wantAll+"0") {
		t.Errorf("the text says %q, which shares a prefix with the true corpus size %d", wantAll+"0", all)
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

// ---------------------------------------------------------------------------
// get_metadata_tree filtering (paragraph 3, the «список объектов» half).
// ---------------------------------------------------------------------------

// TestInstructionsFilteredCategoryDropsOnlyAttachedFiles drives the HANDLER,
// which is the only place this defect is visible.
//
// The sentence used to read «а с ним вся категория целиком» and it was false at
// the tip that shipped it: NewMetadataHandler calls filterNoise(tree) BEFORE the
// «if input.Filter != ""» branch, so a *ПрисоединенныеФайлы object is gone before
// the renderer the other guard trusts is ever reached, and the summary prints a
// positive count that is short. The renderer-level guard could not see it:
// TestInstructionsMetadataSummaryIsOneLinePerCategory calls formatMetadataTree
// directly with a tree nothing filtered.
//
// «ПрисоединенныеФайлы» catalogs are ordinary БСП objects and this product's own
// mock is required to emit one: cmd/mock-1c/main_test.go:TestHandleSubsystems
// fails with «expected a noise object in allObjects» if it stops. So the shape
// the sentence has to be true about is not hypothetical.
func TestInstructionsFilteredCategoryDropsOnlyAttachedFiles(t *testing.T) {
	// PREMISE: the filter really removes something, so the sentence has a subject.
	if len(noiseSuffixes) == 0 {
		t.Fatal("premise failed: noiseSuffixes is empty, so nothing is removed and the sentence " +
			"describes a filter that does not exist")
	}

	// THE TEXT NAMES EVERY SUFFIX THE FILTER USES. Normalised on ё, because the list
	// carries both spellings of one word and the text can only print one of them.
	normalise := func(s string) string { return strings.ReplaceAll(strings.ToLower(s), "ё", "е") }
	textNorm := normalise(instructions.Text)
	for _, suffix := range noiseSuffixes {
		if !strings.Contains(textNorm, normalise(suffix)) {
			t.Errorf("the handler removes names ending in %q and the instruction text does not name it; "+
				"a model told the answer holds the category's objects reads the absence as «no such object»",
				suffix)
		}
	}
	// Control: a suffix the filter does not use must be reported absent, so the loop
	// above is reading the text and not agreeing with anything.
	if strings.Contains(textNorm, normalise("ВложенныеФайлыЖурнала")) {
		t.Fatal("control failed: the text contains a suffix nothing removes, so the containment test " +
			"is not discriminating")
	}

	const category = "Справочники"
	keep := []string{"Номенклатура", "Контрагенты"}
	drop := "ЗаказПокупателя" + noiseSuffixes[0]

	payload, err := json.Marshal(map[string][]string{category: append(append([]string{}, keep...), drop)})
	if err != nil {
		t.Fatalf("building the fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metadata" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	handler := NewMetadataHandler(onec.NewClient(srv.URL, "", ""))

	filtered, isErr, err := callTool(t, handler, `{"filter":"`+category+`"}`)
	if err != nil || isErr {
		t.Fatalf("the filtered call failed (isError=%v): %v\n%s", isErr, err, filtered)
	}
	for _, name := range keep {
		if !strings.Contains(filtered, name) {
			t.Errorf("the filtered answer dropped %q, which is an ordinary object of the category:\n%s",
				name, filtered)
		}
	}
	if strings.Contains(filtered, drop) {
		t.Errorf("the filtered answer carries %q, so the handler no longer removes names ending in %q "+
			"and the instruction text now says something the server does not do:\n%s",
			drop, noiseSuffixes[0], filtered)
	}

	// AND THE SUMMARY COUNTS WHAT THE FILTERED CALL RETURNS. A count of three beside
	// a list of two is the shape that makes a model report a missing object as a
	// missing object rather than as a filtered one.
	summary, isErr, err := callTool(t, handler, `{}`)
	if err != nil || isErr {
		t.Fatalf("the summary call failed (isError=%v): %v\n%s", isErr, err, summary)
	}
	row := ""
	for _, line := range strings.Split(summary, "\n") {
		if strings.Contains(line, `filter="`+category+`"`) {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("the summary has no row for %s, so the count below cannot be read:\n%s", category, summary)
	}
	if want := fmt.Sprintf("(%d)", len(keep)); !strings.Contains(row, want) {
		t.Errorf("the summary row is %q, which does not carry the count %s the filtered answer returns",
			row, want)
	}
}

// ---------------------------------------------------------------------------
// The limit parameter (paragraph 3, first sentence).
// ---------------------------------------------------------------------------

// instrLimitProperty returns a tool's declared «limit» property.
func instrLimitProperty(t *testing.T, tool *mcp.Tool) (typ, description string) {
	t.Helper()
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshalling the schema of %q: %v", tool.Name, err)
	}
	var shape struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("decoding the schema of %q: %v", tool.Name, err)
	}
	p, ok := shape.Properties["limit"]
	if !ok {
		t.Fatalf("%q declares no property named limit, and the instruction text tells the model to "+
			"pass one", tool.Name)
	}
	return p.Type, p.Description
}

// TestInstructionsLimitIsDeclaredAsACount is the anchor for «задаёт он число
// результатов, а не размер ответа».
//
// WHY THE SENTENCE CHANGED. It used to read «и считает он результаты, а не
// байты», which is a claim about WHO COUNTS AND HOW. For search_code that is Go;
// for execute_query and get_event_log the counting is Лимит in
// extension/src/HTTPServices/MCPService/Ext/Module.bsl, which is installed and
// versioned separately from this binary and whose version check is a floor that
// gates nothing. Three sentences were cut from this text for depending on BSL and
// that clause was not, so the exclusion rule had an exception nobody wrote down.
// The recast claim is about what the parameter DECLARES, and that is three input
// schemas and three constants in this binary.
//
// It also pins the six numbers the schemas quote to the six constants that decide
// behaviour. The instruction text made limit a first-class steer, which promotes
// those descriptions from decoration to the model's only source for the ceiling,
// and nothing in the module read any of the six.
func TestInstructionsLimitIsDeclaredAsACount(t *testing.T) {
	cases := []struct {
		tool               *mcp.Tool
		defaultVal, maxVal int
	}{
		{QueryTool(), defaultQueryLimit, maxQueryLimit},
		{EventLogTool(), defaultEventLogLimit, maxEventLogLimit},
		{SearchCodeTool(), defaultSearchLimit, maxSearchLimit},
	}

	// PREMISE: the table is populated. Emptied, the loop below runs zero assertions
	// and zero controls: three schemas go unread and the six numbers the model
	// reads the ceiling out of go unchecked, while this test still passes on the
	// two clampLimit assertions at the end. Measured: with the table emptied the
	// whole package stays green.
	//
	// SHRINK-ONLY. Dropping a row is how a tool stops being covered here, and the
	// sentence this table anchors is precisely about which tools have a limit.
	if len(cases) < 3 {
		t.Fatalf("the limit table holds %d rows and held 3 when this guard was written; a dropped row "+
			"is a tool that stopped being checked against the sentence that names it", len(cases))
	}

	// PREMISE: the sentence still names exactly these three tools. If it were
	// rewritten to name a fourth, this table would be measuring the wrong set and
	// would say so rather than agreeing.
	const anchor = "Параметр limit есть только у "
	start := strings.Index(instructions.Text, anchor)
	if start < 0 {
		t.Fatalf("the sentence beginning %q is gone from the text", anchor)
	}
	sentence := instructions.Text[start:]
	if end := strings.Index(sentence, "."); end >= 0 {
		sentence = sentence[:end]
	}
	if !strings.Contains(instructions.Text, "задаёт он число результатов, а не размер ответа") {
		t.Errorf("the clause this guard anchors is gone; the sentence now reads %q.\n"+
			"If it was rewritten, check first that the new claim is decidable in Go: what the far side "+
			"counts limit in is decided in Module.bsl, which is versioned separately from this binary.",
			sentence)
	}

	numbers := regexp.MustCompile(`\d+`)
	for _, c := range cases {
		if !strings.Contains(sentence, c.tool.Name) {
			t.Errorf("the limit sentence no longer names %q, which declares a limit", c.tool.Name)
		}

		typ, desc := instrLimitProperty(t, c.tool)

		// A COUNT, not a size. The declared JSON type is the machine half of the
		// claim: a byte budget would be declared and described in bytes.
		if typ != "integer" {
			t.Errorf("%s declares limit as %q; the text tells the model it is a number of results",
				c.tool.Name, typ)
		}
		if !strings.Contains(desc, "Максимальное количество") {
			t.Errorf("%s describes limit as %q, which does not tell the model it bounds a COUNT",
				c.tool.Name, desc)
		}

		// AND THE TWO NUMBERS IN THAT DESCRIPTION ARE THE TWO CONSTANTS. The model
		// reads the ceiling out of this sentence and out of nowhere else, and nothing
		// in this module used to read any of the six.
		got := numbers.FindAllString(desc, -1)
		if len(got) != 2 {
			t.Errorf("%s's limit description carries %d numbers, want the default and the maximum: %q",
				c.tool.Name, len(got), desc)
			continue
		}
		want := []string{strconv.Itoa(c.defaultVal), strconv.Itoa(c.maxVal)}
		if got[0] != want[0] || got[1] != want[1] {
			t.Errorf("%s tells the model limit defaults to %s and caps at %s; the constants this binary "+
				"clamps with are %s and %s", c.tool.Name, got[0], got[1], want[0], want[1])
		}
		// Control: the comparison is reading the numbers and not the wording around
		// them, so an off-by-one must disagree.
		if got[1] == strconv.Itoa(c.maxVal+1) {
			t.Fatalf("control failed: %s's ceiling matched %d as well as %d", c.tool.Name, c.maxVal+1, c.maxVal)
		}
	}

	// AND THE CLAMP IS A COUNT IN GO TOO. clampLimit is what every one of the three
	// handlers applies before the value leaves this binary.
	if got := clampLimit(maxQueryLimit*10, defaultQueryLimit, maxQueryLimit); got != maxQueryLimit {
		t.Errorf("clampLimit does not cap at the maximum: got %d, want %d", got, maxQueryLimit)
	}
	if got := clampLimit(0, defaultQueryLimit, maxQueryLimit); got != defaultQueryLimit {
		t.Errorf("clampLimit does not default: got %d, want %d", got, defaultQueryLimit)
	}
}

// ---------------------------------------------------------------------------
// get_event_log's wire type (paragraph 5).
// ---------------------------------------------------------------------------

// TestInstructionsEventLogResultCannotCarryTruncation is the static half of
// «Пометки об усечении в этом ответе нет».
//
// The renderer guard proves today's renderer prints no note. This one proves the
// wire type cannot grow the field a note would be rendered from: onec.QueryResult
// carries Truncated and tools/query.go renders «> Результат усечён…» off it, so
// the shape exists in this codebase and the day it is copied onto EventLogResult
// the sentence stops being true.
func TestInstructionsEventLogResultCannotCarryTruncation(t *testing.T) {
	fields := func(v any) map[string]bool {
		out := map[string]bool{}
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			out[rt.Field(i).Name] = true
		}
		return out
	}

	// Control: the same reflection over the type that DOES carry the flag must
	// report it, so a walk that found nothing cannot report agreement.
	if !fields(onec.QueryResult{})["Truncated"] {
		t.Fatal("control failed: the reflection does not see onec.QueryResult.Truncated, so a clean " +
			"report about EventLogResult means nothing")
	}

	got := fields(onec.EventLogResult{})
	want := map[string]bool{"Events": true, "Total": true}
	if len(got) != len(want) {
		t.Errorf("onec.EventLogResult now holds %d fields, want %d: %v", len(got), len(want), got)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("onec.EventLogResult no longer holds %s", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("onec.EventLogResult grew the field %s. If it can carry truncation, the sentence "+
				"«Пометки об усечении в этом ответе нет» is now a promise about a renderer rather than "+
				"about a wire type, and the text has to say so.", name)
		}
	}
}
