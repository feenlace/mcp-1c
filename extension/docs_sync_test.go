package extension

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// ---------------------------------------------------------------------------
// docs/bsl is an INSTALL PATH, not a description of one.
//
// docs/bsl/README.md step 4 says «Скопируйте BSL-код из соответствующего файла»,
// so whatever those files contain is what runs on the installations that follow
// the documentation. They are therefore a second copy of the shipped handlers,
// and a second copy drifts.
//
// IT ALREADY DID, and that is why this guard exists rather than a note in a
// review checklist. The commit that taught ЗапросPOST and ПроверкаЗапросаPOST to
// answer 400 for a body without query changed the shipped module only. The two
// files under docs/bsl kept reading Параметры.query straight, so a documented
// install still answered 500 text/plain with a module name and a line number in
// it. The same change set also raised the version the extension reports, so a
// documented install passed the startup version probe and 500-ed anyway: the one
// check that could have caught it was made to agree.
//
// WHAT IS COMPARED is the executable statements: comments and blank lines are
// dropped and runs of whitespace are collapsed OUTSIDE STRING LITERALS. The doc
// files carry install notes the module has no reason to carry, so comparing them
// verbatim would fail on prose and teach the next reader to weaken the guard.
// What must not differ is what the code DOES.
//
// «Outside string literals» is a repair, not a nicety. The reducer used to
// collapse whitespace everywhere, so a documented install shipping
// ОтветОшибка(400, "query      is      required") compared equal to the shipped
// "query is required" and this guard passed. The literal is not decoration: it
// is the diagnostic on the wire, and tools.isQueryBodyFault matches it by prefix
// to pick which remedy the caller is given.
//
// OMISSION COUNTS AS DRIFT, which is why MCPService.xml is read here and not
// only the two BSL sources. A comparison of the files that exist cannot see a
// handler that has no file at all, and that is not a hypothetical: /subsystems
// shipped in extension 0.4.3, tools/analyze_subsystems.go calls it, and it had
// no entry under docs/bsl. The version endpoint reports the same number either
// way, so the probe at startup calls such an install verified.
// ---------------------------------------------------------------------------

const (
	docsBSLDir    = "../docs/bsl"
	embeddedModul = "src/HTTPServices/MCPService/Ext/Module.bsl"
)

// bslDeclRE matches the opening line of a BSL procedure or function.
var bslDeclRE = regexp.MustCompile(`^\s*(?:Функция|Процедура)\s+([\p{L}_][\p{L}\p{Nd}_]*)\s*\(`)

// bslEndRE matches the line that closes one.
var bslEndRE = regexp.MustCompile(`^\s*(?:КонецФункции|КонецПроцедуры)\s*$`)

// bslRoutines splits BSL source into routines by name, keeping only the
// executable statements of each. Nested routines do not exist in BSL, so a flat
// scan is exact.
func bslRoutines(src string) map[string][]string {
	out := map[string][]string{}
	var name string
	var body []string
	for _, line := range strings.Split(src, "\n") {
		if m := bslDeclRE.FindStringSubmatch(line); m != nil {
			name, body = m[1], nil
			continue
		}
		if name == "" {
			continue
		}
		if bslEndRE.MatchString(line) {
			out[name] = body
			name, body = "", nil
			continue
		}
		if stmt := bslStatement(line); stmt != "" {
			body = append(body, stmt)
		}
	}
	return out
}

// bslStatement reduces one source line to the statement on it, or "" when the
// line carries none.
//
// STRING LITERALS ARE COPIED, NOT REDUCED, and that is the whole of the
// difference from what this used to be. It was strings.Fields over the text
// before the first "//", which collapses whitespace EVERYWHERE, including inside
// a literal. Measured: a docs/bsl copy shipping
// ОтветОшибка(400, "query      is      required") reduced to the same statement
// as the shipped "query is required" and TestDocsBSLMatchesShippedModule passed.
// That is not a cosmetic difference on the wire: tools.isQueryBodyFault matches
// that answer by PREFIX, so the documented install would have shipped a
// diagnostic the Go side classifies as a query rejection and answers with the
// wrong remedy, and the one guard that compares the two copies could not see it.
//
// The comment marker is now found outside literals only, which is the same rule
// applied to the same problem: a "//" inside a literal is data.
func bslStatement(line string) string {
	var b strings.Builder
	rs := []rune(line)
	inString, pendingSpace := false, false
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if inString {
			b.WriteRune(r)
			if r == '"' {
				// Two quotes in a row are an escaped quote, not the end.
				if i+1 < len(rs) && rs[i+1] == '"' {
					b.WriteRune('"')
					i++
					continue
				}
				inString = false
			}
			continue
		}
		if r == '/' && i+1 < len(rs) && rs[i+1] == '/' {
			break
		}
		if unicode.IsSpace(r) {
			pendingSpace = true
			continue
		}
		if pendingSpace && b.Len() > 0 {
			b.WriteRune(' ')
		}
		pendingSpace = false
		b.WriteRune(r)
		if r == '"' {
			inString = true
		}
	}
	return b.String()
}

// TestDocsBSLMatchesShippedModule fails when a handler documented under
// docs/bsl stops doing what the shipped module does.
func TestDocsBSLMatchesShippedModule(t *testing.T) {
	raw, err := Source.ReadFile(embeddedModul)
	if err != nil {
		t.Fatalf("read embedded %s: %v", embeddedModul, err)
	}
	module := bslRoutines(string(raw))
	if len(module) < 20 {
		t.Fatalf("the shipped module parsed into %d routines; the reducer is broken and a "+
			"comparison against it would pass by finding nothing", len(module))
	}

	entries, err := os.ReadDir(docsBSLDir)
	if err != nil {
		t.Fatalf("read %s: %v", docsBSLDir, err)
	}

	files, compared := 0, 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".bsl" {
			continue
		}
		files++
		path := filepath.Join(docsBSLDir, e.Name())
		doc, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		routines := bslRoutines(string(doc))
		if len(routines) == 0 {
			t.Errorf("%s declares no routine at all; either the file stopped being an install "+
				"snippet or the reducer no longer reads it", path)
			continue
		}
		for name, docBody := range routines {
			modBody, ok := module[name]
			if !ok {
				t.Errorf("%s documents %s, which the shipped module does not define; an "+
					"installer pasting it gets a handler this binary never talks to", path, name)
				continue
			}
			compared++
			if d := firstDifference(docBody, modBody); d != "" {
				t.Errorf("%s is out of sync with the shipped %s.\n%s\n"+
					"docs/bsl is what README.md step 4 tells installers to paste, so this "+
					"difference ships to every documented install.", path, name, d)
			}
		}
	}

	// Every handler the service declares must HAVE a file. Without this the
	// walk above is blind to the one drift it cannot see: an endpoint that was
	// never documented at all.
	documented := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".bsl" {
			continue
		}
		doc, err := os.ReadFile(filepath.Join(docsBSLDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for name := range bslRoutines(string(doc)) {
			documented[name] = true
		}
	}
	handlers := declaredHandlers(t)
	for _, h := range handlers {
		if !documented[h] {
			t.Errorf("MCPService.xml declares handler %s, but no file under %s documents it; "+
				"an install built from the documentation is missing that endpoint while "+
				"/version still reports the same number", h, docsBSLDir)
		}
	}

	// The counts are asserted because every check above is an "if they differ"
	// check, and a walk that finds no file and no routine satisfies all of them.
	if files < 10 {
		t.Errorf("walked %d .bsl files under %s, expected at least 10", files, docsBSLDir)
	}
	if compared < 10 {
		t.Errorf("compared %d routines, expected at least 10", compared)
	}
	if len(handlers) < 10 {
		t.Errorf("read %d handlers from MCPService.xml, expected at least 10", len(handlers))
	}
	t.Logf("compared %d routines across %d files under %s against %d declared handlers",
		compared, files, docsBSLDir, len(handlers))
}

// declaredHandlers returns every Handler name in MCPService.xml. The element
// carries the routine name the platform dispatches to, so it is the list of
// routines an install has to have.
func declaredHandlers(t *testing.T) []string {
	t.Helper()
	raw, err := Source.ReadFile(mcpServicePath)
	if err != nil {
		t.Fatalf("read embedded %s: %v", mcpServicePath, err)
	}
	var doc struct {
		Handlers []string `xml:"HTTPService>ChildObjects>URLTemplate>ChildObjects>Method>Properties>Handler"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", mcpServicePath, err)
	}
	return doc.Handlers
}

// firstDifference reports the first statement that differs, with its neighbours,
// or "" when the two bodies are the same.
func firstDifference(doc, module []string) string {
	for i := 0; i < len(doc) || i < len(module); i++ {
		a, b := "<конец>", "<конец>"
		if i < len(doc) {
			a = doc[i]
		}
		if i < len(module) {
			b = module[i]
		}
		if a == b {
			continue
		}
		var ctx strings.Builder
		if i > 0 {
			ctx.WriteString("  ...после: " + doc[i-1] + "\n")
		}
		ctx.WriteString("  docs:   " + a + "\n")
		ctx.WriteString("  module: " + b)
		return ctx.String()
	}
	return ""
}

// TestBSLStatementReducerWorks is the positive control for the reducer the guard
// above rests on: a check that only ever asks "are these equal" passes when the
// reducer returns nothing for everything.
func TestBSLStatementReducerWorks(t *testing.T) {
	src := "// заголовок\n" +
		"Функция Пример(А)\n" +
		"    Если А > 0 Тогда // хвостовой комментарий\n" +
		"\n" +
		"        Возврат А;\n" +
		"    КонецЕсли;\n" +
		"КонецФункции\n"
	got := bslRoutines(src)
	want := []string{"Если А > 0 Тогда", "Возврат А;", "КонецЕсли;"}
	body, ok := got["Пример"]
	if !ok {
		t.Fatalf("the reducer did not find Пример: %v", got)
	}
	if len(body) != len(want) {
		t.Fatalf("reduced to %v, want %v", body, want)
	}
	for i := range want {
		if body[i] != want[i] {
			t.Errorf("statement %d = %q, want %q", i, body[i], want[i])
		}
	}
	if d := firstDifference(want, body); d != "" {
		t.Errorf("firstDifference reports a difference between equal bodies:\n%s", d)
	}
	if d := firstDifference(want, want[:2]); d == "" {
		t.Error("firstDifference found nothing between bodies of different length, so the " +
			"comparison the guard uses cannot fail")
	}

	// A comment marker must not swallow a statement that has none.
	if s := bslStatement("    Возврат ОтветJSON(Результат);"); s != "Возврат ОтветJSON(Результат);" {
		t.Errorf("bslStatement dropped a statement with no comment on it: %q", s)
	}

	// WHAT IS INSIDE A LITERAL IS DATA. This block replaces a check that the
	// shipped module contains no "//" inside a string literal, which existed
	// because the reducer would have cut there. The reducer no longer cuts there,
	// so the constraint on the module is gone and what is pinned instead is the
	// reducer's own behaviour.
	for _, c := range []struct{ name, in, want string }{
		{"double slash inside a literal is not a comment",
			`    Возврат ОтветОшибка(400, "see http://example/hs"); // хвост`,
			`Возврат ОтветОшибка(400, "see http://example/hs");`},
		{"runs of spaces inside a literal survive",
			`    Возврат ОтветОшибка(400, "query      is      required");`,
			`Возврат ОтветОшибка(400, "query      is      required");`},
		{"runs of spaces outside a literal are collapsed",
			`    Если    А   >   0   Тогда`, `Если А > 0 Тогда`},
		{"an escaped quote does not end the literal",
			`    Т = "он сказал ""да""   и ушёл";`, `Т = "он сказал ""да""   и ушёл";`},
	} {
		if got := bslStatement(c.in); got != c.want {
			t.Errorf("%s: bslStatement(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}

	// AND THE REDUCER MUST STILL DISCRIMINATE. Two lines that differ only inside
	// a literal have to reduce to different statements. Without this the property
	// above is satisfied by a reducer that returns every line unchanged, which
	// would pass the four cases and stop collapsing anything.
	tight := bslStatement(`    Возврат ОтветОшибка(400, "query is required");`)
	loose := bslStatement(`    Возврат ОтветОшибка(400, "query      is      required");`)
	if tight == loose {
		t.Errorf("two literals with different spacing reduce to the same statement %q, which is "+
			"the drift TestDocsBSLMatchesShippedModule was measured to miss", tight)
	}
	if d := firstDifference([]string{tight}, []string{loose}); d == "" {
		t.Error("firstDifference finds nothing between two statements that differ inside a literal")
	}
}

// ---------------------------------------------------------------------------
// THE PER-FILE HEADER IS PART OF THE INSTALL PATH, NOT A COURTESY.
//
// docs/bsl/README.md step 5 does not list the helpers itself. It says «Скопируйте
// вспомогательные функции, которые названы в шапке каждого файла», so the header
// note of each file IS the list an installer works from. A helper the handler
// calls and the header does not name is a routine that never gets copied, and the
// documented install fails at the first call to it.
//
// Nothing compared the two before. TestDocsBSLMatchesShippedModule reads every
// file in this directory, but it compares ROUTINE BODIES against the shipped
// module and never looks above the first declaration, which is exactly where the
// note lives.
// ---------------------------------------------------------------------------

// bslCallRE matches an identifier in call position.
var bslCallRE = regexp.MustCompile(`([\p{L}_][\p{L}\p{Nd}_]*)\s*\(`)

// bslWithoutLiterals removes every string literal from an already-reduced
// statement, so a routine name printed inside a diagnostic is not read as a call
// to that routine.
func bslWithoutLiterals(stmt string) string {
	var b strings.Builder
	rs := []rune(stmt)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '"' {
			b.WriteRune(rs[i])
			continue
		}
		for i++; i < len(rs); i++ {
			if rs[i] == '"' {
				if i+1 < len(rs) && rs[i+1] == '"' {
					i++
					continue
				}
				break
			}
		}
	}
	return b.String()
}

// docsBSLHeader returns everything a file carries above its first routine, which
// is the note README.md step 5 sends the installer to.
func docsBSLHeader(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if bslDeclRE.MatchString(line) {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// bslHelpersCalled returns the routines of universe that src calls and does not
// itself define.
func bslHelpersCalled(src string, universe map[string]bool) []string {
	local := bslRoutines(src)
	seen := map[string]bool{}
	var out []string
	for _, body := range local {
		for _, stmt := range body {
			for _, m := range bslCallRE.FindAllStringSubmatch(bslWithoutLiterals(stmt), -1) {
				name := m[1]
				// PRESENCE, not a non-nil body: a routine with no statements in it
				// reduces to a nil slice, so a value comparison would report a
				// routine the file defines itself as one it has to be told to copy.
				_, isLocal := local[name]
				if !universe[name] || isLocal || seen[name] {
					continue
				}
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestDocsBSLNotesNameEveryHelperTheyCall fails when a documented handler calls a
// routine of the shipped module that its own header does not name.
func TestDocsBSLNotesNameEveryHelperTheyCall(t *testing.T) {
	raw, err := Source.ReadFile(embeddedModul)
	if err != nil {
		t.Fatalf("read embedded %s: %v", embeddedModul, err)
	}
	universe := map[string]bool{}
	for name := range bslRoutines(string(raw)) {
		universe[name] = true
	}
	// CONTROL: the module parsed. Every assertion below is satisfied by an empty
	// universe, because nothing would then be required of any header.
	if len(universe) < 20 {
		t.Fatalf("the shipped module parsed into %d routines; with a universe that small "+
			"nothing is required of any header and this walk measures nothing", len(universe))
	}
	if !universe["ОтветJSON"] {
		t.Fatal("CONTROL: ОтветJSON is not among the routines the shipped module defines, so " +
			"the universe is not the module's own helpers")
	}

	entries, err := os.ReadDir(docsBSLDir)
	if err != nil {
		t.Fatalf("read %s: %v", docsBSLDir, err)
	}

	files, required, widest := 0, 0, 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".bsl" {
			continue
		}
		files++
		path := filepath.Join(docsBSLDir, e.Name())
		doc, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		header := docsBSLHeader(string(doc))
		need := bslHelpersCalled(string(doc), universe)
		if len(need) > widest {
			widest = len(need)
		}
		for _, name := range need {
			required++
			if !strings.Contains(header, name) {
				t.Errorf("%s calls %s and its header does not name it. README.md step 5 sends "+
					"an installer to that header for the helpers to copy, so a documented "+
					"install is missing the routine and fails at the first call to it",
					path, name)
			}
		}
	}

	// The counts are asserted because every check above is an "if it is missing"
	// check, and a walk that finds no file and no call satisfies all of them.
	if files < 10 {
		t.Errorf("walked %d .bsl files under %s, expected at least 10", files, docsBSLDir)
	}
	if required < 20 {
		t.Errorf("found %d helper calls to check across %d files, expected at least 20; the "+
			"call extractor has stopped finding calls", required, files)
	}
	// AND IT MUST DISCRIMINATE. An extractor that reported every routine of the
	// module as called by every file would satisfy both counts above while saying
	// nothing about any file.
	if widest >= len(universe) {
		t.Errorf("the widest file requires %d of the %d routines the module defines; the "+
			"extractor is returning the universe rather than the calls", widest, len(universe))
	}
	t.Logf("checked %d helper calls across %d files against %d module routines; widest file "+
		"requires %d", required, files, len(universe), widest)
}

// TestBSLHelpersCalledReadsCallsNotText is the positive control for the extractor
// the guard above rests on.
func TestBSLHelpersCalledReadsCallsNotText(t *testing.T) {
	universe := map[string]bool{"ОтветJSON": true, "ОтветОшибка": true, "ЕстьКоллекция": true}
	src := "// шапка называет ЕстьКоллекция\n" +
		"Функция ПримерGET(Запрос)\n" +
		"    Если Ложь Тогда\n" +
		"        Возврат ОтветОшибка(400, \"ЕстьКоллекция недоступна\");\n" +
		"    КонецЕсли;\n" +
		"    Возврат ОтветJSON(Результат); // ЕстьКоллекция\n" +
		"КонецФункции\n"

	got := bslHelpersCalled(src, universe)
	want := []string{"ОтветJSON", "ОтветОшибка"}
	if len(got) != len(want) {
		t.Fatalf("extracted %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}

	// A routine the file DEFINES is not a helper it has to be told to copy.
	self := "Функция ОтветJSON(Значение)\nКонецФункции\n" +
		"Функция ПримерGET(Запрос)\n    Возврат ОтветJSON(Результат);\nКонецФункции\n"
	if n := len(bslHelpersCalled(self, universe)); n != 0 {
		t.Errorf("a file defining the routine it calls still owes %d helpers", n)
	}

	// The header reader stops at the first declaration, or the note would be
	// satisfied by any mention anywhere in the file.
	head := docsBSLHeader(src)
	if !strings.Contains(head, "ЕстьКоллекция") {
		t.Errorf("the header reader lost the note text:\n%s", head)
	}
	if strings.Contains(head, "ОтветJSON") {
		t.Errorf("the header reader read past the first declaration, so a name used in the "+
			"body would satisfy the note:\n%s", head)
	}
}
