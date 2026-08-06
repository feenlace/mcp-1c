package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// nested_dump_root_test.go covers the delivery half of the nested-root check. The
// detection itself is measured in dump/dumproot_test.go; what is asserted here is
// that the answer REACHES the person who typed the flag, and that it does so at a
// level this binary actually prints.

// customerShapedTree builds the tree the customer had: a parent holding a proper
// dump root and a second root beside it.
func customerShapedTree(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	for _, d := range []string{"main/Catalogs", "main/Documents", "main/Ext", "ext/Catalogs", "ext/CommonModules"} {
		if err := os.MkdirAll(filepath.Join(parent, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(parent, "main", "ConfigDumpInfo.xml"), []byte("<x/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return parent
}

// captureAtErrorLevel installs a default slog handler that keeps NOTHING below
// Error, mirroring what main installs, and returns the buffer it writes to.
func captureAtErrorLevel(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var out bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelError})))
	return &out
}

// TestNestedDumpRootNoticeIsDeliveredAtErrorLevel is the level pin.
//
// The handler below discards everything under Error. A message published at Warn
// would therefore produce an EMPTY buffer here while still passing any test that
// merely called the formatter, which is precisely how a warning ships that
// "exists, has a passing test, and never appears in a normal run".
func TestNestedDumpRootNoticeIsDeliveredAtErrorLevel(t *testing.T) {
	out := captureAtErrorLevel(t)
	parent := customerShapedTree(t)

	reportDumpRootAndLayout(parent)

	got := out.String()
	if got == "" {
		t.Fatal("nothing was published through a handler that keeps only Error and above. " +
			"The message is being sent below Error, so it cannot appear in a normal run of " +
			"this binary, whose default handler is installed at LevelError.")
	}
	if !strings.Contains(got, "level=ERROR") {
		t.Errorf("published record is not at ERROR:\n%s", got)
	}
	// THE SENTENCE, not the record. Matching the whole record let the "roots=ext,main"
	// ATTRIBUTE satisfy an assertion that claimed to be about the message, so a
	// regression that emptied the sentence stayed green. The message field is
	// extracted and checked on its own.
	if !strings.Contains(recordMessage(t, got), "--dump") {
		t.Errorf("the sentence does not mention the flag to change:\n%s", got)
	}
	// The names of the roots ride in the attributes and MUST NOT be in the sentence:
	// they are directory names read off disk, and RU prose here carries no тире.
	if msg := recordMessage(t, got); strings.Contains(msg, "main") || strings.Contains(msg, "ext,") {
		t.Errorf("a directory name read off disk was spliced into customer-facing RU:\n%s", msg)
	}
	if !strings.Contains(got, "roots=") {
		t.Errorf("the root names are not carried in the attributes either, so the operator "+
			"cannot learn them at all:\n%s", got)
	}

	// Positive control for the buffer itself: the handler really does drop a Warn,
	// so the non-empty buffer above is evidence about the level and not about the
	// handler being permissive.
	out.Reset()
	slog.Warn("control")
	if out.Len() != 0 {
		t.Fatalf("positive control failed: the handler published a Warn (%q), so passing the "+
			"assertion above proves nothing about the level", out.String())
	}
}

// TestNestedDumpRootNoticeIsSilentOnAProperRoot pins the other half: the shipped
// behaviour is silence, and the check must not become a new line every operator
// with a correct path has to learn to ignore.
func TestNestedDumpRootNoticeIsSilentOnAProperRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dump")
	for _, d := range []string{"Catalogs", "Documents", "Ext"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out := captureAtErrorLevel(t)
	reportDumpRootAndLayout(root)
	if out.Len() != 0 {
		t.Errorf("a correctly pointed --dump produced output:\n%s", out.String())
	}

	// A path with nothing dump-like anywhere stays silent too: this channel reports
	// what it FOUND, never that it looked. The case it cannot see, a --dump two
	// levels above a root, is measured on the other channel; see
	// dump.WrappedPathState and TestWrappedNotice_ADumpTwoLevelsUpIsReported.
	plain := t.TempDir()
	if err := os.MkdirAll(filepath.Join(plain, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	reportDumpRootAndLayout(plain)
	if out.Len() != 0 {
		t.Errorf("an ordinary directory produced output:\n%s", out.String())
	}

	// Positive control for the capture itself.
	out.Reset()
	reportDumpRootAndLayout(customerShapedTree(t))
	if out.Len() == 0 {
		t.Fatal("positive control failed: the customer's shape published nothing either, " +
			"so the assertions above say nothing about the check")
	}
}

// TestRealDumpRootsOnThisMachineStaySilent is the noise check that matters: the
// rule above is only tolerable if every tree a customer would actually point at is
// quiet. These are the real artefacts on this machine, not fixtures.
func TestRealDumpRootsOnThisMachineStaySilent(t *testing.T) {
	roots := []string{
		"/Users/igoroot/GolandProjects/mcp/dumps/dump_bsl",
		"/Users/igoroot/GolandProjects/mcp/dumps/dump_2",
		"/Users/igoroot/Downloads/canon_vm",
		"/Users/igoroot/Downloads/mcp-modified",
		"/Users/igoroot/Downloads/extdump_vm/FeenlaceMCPService",
		"/Users/igoroot/Downloads/extdump_vm/mcp_service",
	}
	checked := 0
	out := captureAtErrorLevel(t)
	for _, r := range roots {
		if _, err := os.Stat(r); err != nil {
			continue
		}
		checked++
		out.Reset()
		reportDumpRootAndLayout(r)
		if out.Len() != 0 {
			t.Errorf("a real dump root produced output:\n%s\n%s", r, out.String())
		}
	}
	if checked == 0 {
		t.Skip("no real dump artefacts on this machine")
	}
	t.Logf("checked %d real dump roots, all silent", checked)

	// And the container of two real extension dumps is NOT silent, and says the
	// thing that is true of it.
	const container = "/Users/igoroot/Downloads/extdump_vm"
	if _, err := os.Stat(container); err != nil {
		return
	}
	out.Reset()
	reportDumpRootAndLayout(container)
	got := out.String()
	if got == "" {
		t.Fatal("the container of two extension dumps produced nothing")
	}
	if strings.Contains(recordMessage(t, got), "затира") {
		t.Errorf("the message claims an overwrite for two real extension dumps that this "+
			"same process keys under separate names:\n%s", got)
	}
}

// recordMessage pulls the msg= field out of a slog TextHandler line, so an
// assertion about the SENTENCE cannot be satisfied by an attribute.
func recordMessage(t *testing.T, record string) string {
	t.Helper()
	i := strings.Index(record, "msg=")
	if i < 0 {
		t.Fatalf("no msg= field in the record:\n%s", record)
	}
	rest := record[i+len("msg="):]
	if strings.HasPrefix(rest, "\"") {
		if q, err := strconv.Unquote(rest[:strings.Index(rest[1:], "\"")+2]); err == nil {
			return q
		}
	}
	if j := strings.IndexAny(rest, " \n"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// TestNestedDumpRootMessageSaysOnlyWhatWasMeasured.
//
// The sentence this replaces asserted that the roots below the path «затирают друг
// друга», and on the tree that motivated the whole feature that is FALSE: two
// extension dumps side by side are recognised from their manifests and keyed under
// their own names, and the same process measures the overwrite as zero. It then
// told the operator to point --dump at ONE of them, which for that tree throws the
// other extension away. Both halves are pinned here.
func TestNestedDumpRootMessageSaysOnlyWhatWasMeasured(t *testing.T) {
	twoRoots := dump.DumpRootInspection{NestedRoots: []string{"ext", "main"}}

	// Every root below the path is a recognised extension: no overwrite is claimed
	// and no discard is prescribed.
	allExt := nestedDumpRootMessage(twoRoots, dump.ExtensionLayoutSummary{Extensions: 2})
	if allExt == "" {
		t.Fatal("the message is empty for an inspection that found roots")
	}
	if strings.Contains(allExt, "затира") {
		t.Errorf("the message asserts an overwrite that the same process measures as zero:\n%s", allExt)
	}
	if !strings.Contains(allExt, "не потеряется") {
		t.Errorf("the message does not say the recognised extensions keep their content:\n%s", allExt)
	}

	// None of them is an extension: they really do share a keyspace, and the
	// sentence says so. Without this the assertion above would also pass on a
	// message that never mentions overwriting at all.
	noneExt := nestedDumpRootMessage(twoRoots, dump.ExtensionLayoutSummary{})
	if !strings.Contains(noneExt, "затирают друг друга") {
		t.Errorf("two plain dump roots under one path really do collide, and the message "+
			"does not say so:\n%s", noneExt)
	}
	if allExt == noneExt {
		t.Error("the message is the same whether the roots are extensions or not, so it is " +
			"not reporting a measurement")
	}

	// It must not tell the operator the server will handle it.
	for _, forbidden := range []string{"автоматически", "сам перейдёт", "будет использован"} {
		if strings.Contains(allExt, forbidden) || strings.Contains(noneExt, forbidden) {
			t.Errorf("the message promises an action the server does not take (%q)", forbidden)
		}
	}

	// A root says nothing.
	if got := nestedDumpRootMessage(dump.DumpRootInspection{IsRoot: true, NestedRoots: []string{"main"}},
		dump.ExtensionLayoutSummary{}); got != "" {
		t.Errorf("a path that IS a root produced %q", got)
	}

	// NOT A ROOT AND NO ROOT BELOW IT EITHER: still silent, deliberately. One
	// ReadDir cannot tell a path two levels above a dump from a hand-made tree
	// holding one kind directory, and the second one keys perfectly well. That case
	// is reported by MEASUREMENT on the other channel, by the wrapped-path count,
	// which is zero for the valid tree and every file for the wrong one. Guessing
	// here would put a warning in front of every operator with a partial tree.
	if got := nestedDumpRootMessage(dump.DumpRootInspection{}, dump.ExtensionLayoutSummary{}); got != "" {
		t.Errorf("an inspection that found nothing produced %q", got)
	}

	// Truncation is carried rather than dropped.
	cut := nestedDumpRootMessage(dump.DumpRootInspection{NestedRoots: []string{"main"}, Truncated: true},
		dump.ExtensionLayoutSummary{Extensions: 1})
	if cut == nestedDumpRootMessage(dump.DumpRootInspection{NestedRoots: []string{"main"}},
		dump.ExtensionLayoutSummary{Extensions: 1}) {
		t.Error("a truncated scan produces the same sentence as a complete one, so the " +
			"operator cannot tell a full answer from a partial one")
	}
}

// TestStartupMessagesCarryNoDashAndNoDiskContent drives every branch of both
// sentences past a byte scan for the dash characters, and past a HOSTILE directory
// name rather than a polite one.
//
// A previous guard on RU prose fed itself benign literals and stayed green while a
// тире reached rendered RU through an ordinary directory name like «Доработки —
// копия». The samples below are that name and worse.
func TestStartupMessagesCarryNoDashAndNoDiskContent(t *testing.T) {
	hostile := []string{"Доработки — копия", "ext—2", "A\u2212B", "«тире–внутри»"}
	var msgs []string
	for _, layout := range []dump.ExtensionLayoutSummary{
		{}, {Extensions: 1}, {Extensions: 2}, {Extensions: 1, ScanTruncated: true},
		{NotRegular: 1}, {Unreadable: 2}, {ReadTruncated: 3}, {NameRejected: 4},
		{Malformed: 5}, {Unscannable: 6},
		{NotRegular: 1, Unreadable: 1, ReadTruncated: 1, NameRejected: 1,
			Malformed: 1, Unscannable: 1, ScanTruncated: true},
	} {
		layout.Dirs = hostile
		for _, insp := range []dump.DumpRootInspection{
			{}, {NestedRoots: hostile}, {NestedRoots: hostile, Truncated: true},
			{NestedRoots: hostile[:1]},
		} {
			msgs = append(msgs, nestedDumpRootMessage(insp, layout))
		}
		msgs = append(msgs, extensionLayoutDoubtMessage(layout))
	}

	// PREMISE: the set the scan reads is populated. Emptied, every comparison below is
	// vacuously false and a sentence made entirely of тире passes.
	if len(dashRunes) < 5 {
		t.Fatalf("dashRunes holds %d runes; the scan is only as wide as this set",
			len(dashRunes))
	}
	// Positive control FIRST, over the SAME set the scan uses: the samples really do
	// carry what is being looked for, so a clean scan below is the messages being
	// clean and not the scan being blind. Two spellings of one set is how a control
	// keeps passing after the set it guards is emptied.
	joined := strings.Join(hostile, "")
	if !strings.ContainsAny(joined, string(dashRunes)) {
		t.Fatal("control failed: the hostile samples carry none of the dash characters")
	}

	produced, scanned := 0, 0
	for _, m := range msgs {
		if m == "" {
			continue
		}
		produced++
		// PER CODEPOINT, not per substring, so the answer stays «no codepoint of this
		// sentence is one of them» when a rune is added to dashRunes.
		for _, got := range m {
			scanned++
			for _, bad := range dashRunes {
				if got == bad {
					t.Errorf("customer-facing RU carries U+%04X:\n%s", bad, m)
				}
			}
		}
		for _, name := range hostile {
			if strings.Contains(m, name) {
				t.Errorf("a directory name read off disk was spliced into the sentence:\n%s", m)
			}
		}
	}
	if produced == 0 {
		t.Fatal("no branch produced a sentence, so the scan measured nothing")
	}
	if scanned == 0 {
		t.Fatal("the per-codepoint scan visited no codepoint at all")
	}
	t.Logf("scanned %d codepoints across %d non-empty sentences", scanned, produced)
}

// TestTheDoubtMessageCountsAndNamesNothing pins the third answer reaching the
// operator at all. A namespace that quietly failed to appear looks exactly like a
// dump that never had one.
func TestTheDoubtMessageCountsAndNamesNothing(t *testing.T) {
	if got := extensionLayoutDoubtMessage(dump.ExtensionLayoutSummary{Extensions: 3}); got != "" {
		t.Errorf("a layout with nothing undecided produced %q", got)
	}
	for _, tc := range []struct {
		layout dump.ExtensionLayoutSummary
		want   string
	}{
		{dump.ExtensionLayoutSummary{NotRegular: 2}, "не обычным файлом"},
		{dump.ExtensionLayoutSummary{Unreadable: 1}, "Не удалось прочитать"},
		{dump.ExtensionLayoutSummary{ReadTruncated: 5}, "окно чтения"},
		{dump.ExtensionLayoutSummary{NameRejected: 1}, "нельзя использовать как часть ключа"},
		{dump.ExtensionLayoutSummary{Malformed: 1}, "не закрыт комментарий, блок CDATA или инструкция обработки"},
		{dump.ExtensionLayoutSummary{Unscannable: 1}, "объявление DOCTYPE или другое объявление разметки"},
		{dump.ExtensionLayoutSummary{ScanTruncated: true}, "не все подкаталоги"},
	} {
		got := extensionLayoutDoubtMessage(tc.layout)
		if !strings.Contains(got, tc.want) {
			t.Errorf("layout %+v produced %q, want it to mention %q", tc.layout, got, tc.want)
		}
	}
	// The counts are in it, and they are the real ones.
	if got := extensionLayoutDoubtMessage(dump.ExtensionLayoutSummary{ReadTruncated: 7}); !strings.Contains(got, ": 7.") {
		t.Errorf("the count is missing from %q", got)
	}
}

// TestMainReportsNestedDumpRootsBeforeServing is structural, and it is the test
// that stops all of the above from being dead code.
//
// Every behavioural test here calls reportDumpRootAndLayout directly. That proves
// the function works and proves nothing about whether the binary ever runs it, so
// what is asserted here is that main calls it, that it is handed the resolved
// --dump value, and that the call sits BEFORE the index is opened. Positions are
// compared against other nodes, never against line numbers.
func TestMainReportsNestedDumpRootsBeforeServing(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}
	var mainFn *ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "main" {
			mainFn = fn
		}
	}
	if mainFn == nil {
		t.Fatal("premise broken: main.go declares no func main")
	}

	callsTo := func(name string) []*ast.CallExpr {
		var out []*ast.CallExpr
		ast.Inspect(mainFn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if fn.Name == name {
					out = append(out, call)
				}
			case *ast.SelectorExpr:
				if fn.Sel.Name == name {
					out = append(out, call)
				}
			}
			return true
		})
		return out
	}

	report := callsTo("reportDumpRootAndLayout")
	if len(report) != 1 {
		t.Fatalf("main calls reportDumpRootAndLayout %d times, want exactly 1; every "+
			"behavioural test in this file would still pass with the call removed", len(report))
	}
	// PREMISE: the anchor it is ordered against really exists, or the ordering
	// assertion below would hold vacuously.
	open := callsTo("openServeIndexLocal")
	if len(open) != 1 {
		t.Fatalf("premise broken: main calls openServeIndexLocal %d times, want 1", len(open))
	}
	if report[0].Pos() > open[0].Pos() {
		t.Error("reportDumpRootAndLayout runs AFTER the index is opened, so the operator is " +
			"told about the path only once the wrong one has been used")
	}

	// It must receive the resolved flag value, not something else that happens to
	// be a string.
	if len(report[0].Args) != 1 {
		t.Fatalf("reportDumpRootAndLayout is called with %d arguments, want 1", len(report[0].Args))
	}
	star, ok := report[0].Args[0].(*ast.StarExpr)
	if !ok {
		t.Fatalf("reportDumpRootAndLayout is not handed a dereferenced flag: %T", report[0].Args[0])
	}
	if id, ok := star.X.(*ast.Ident); !ok || id.Name != "dumpDir" {
		t.Errorf("reportDumpRootAndLayout is handed %v, want *dumpDir", star.X)
	}
}

// TestASymlinkedDumpPathIsReportedRatherThanApproved is the delivery half of R17.
//
// The inspection follows the link and may report a perfectly good dump root; the
// indexer does not follow it and indexes nothing. The sentence for that path must
// be about the link, not about the target, because every other sentence this
// function can produce is true of the target and useless about the path.
func TestASymlinkedDumpPathIsReportedRatherThanApproved(t *testing.T) {
	symlinked := dump.DumpRootInspection{IsRoot: true, RootIsSymlink: true}
	got := nestedDumpRootMessage(symlinked, dump.ExtensionLayoutSummary{})
	if got == "" {
		t.Fatal("a symlinked --dump that inspects AS A ROOT produced nothing, so a path " +
			"that indexes zero modules is declared correct")
	}
	if !strings.Contains(got, "символьная ссылка") {
		t.Errorf("the sentence does not say what is wrong with the path: %q", got)
	}
	if !strings.Contains(got, "ни один модуль") {
		t.Errorf("the sentence does not say what the consequence is: %q", got)
	}

	// It overrides the nested-root sentence too: a symlink whose target holds roots
	// still indexes nothing, so naming them would be advice about a path that
	// cannot work.
	withRoots := dump.DumpRootInspection{RootIsSymlink: true, NestedRoots: []string{"a", "b"}}
	if nestedDumpRootMessage(withRoots, dump.ExtensionLayoutSummary{Extensions: 2}) != got {
		t.Error("a symlinked path with roots under it got the nested-root sentence, which " +
			"describes the target rather than the path")
	}

	// A real root with symlinked children says something narrower: what is lost is
	// those subtrees, not everything.
	children := nestedDumpRootMessage(
		dump.DumpRootInspection{IsRoot: true, SymlinkedChildren: 3}, dump.ExtensionLayoutSummary{})
	if children == "" {
		t.Error("a root with symlinked children produced nothing; the walk skips them silently")
	}
	if children == got {
		t.Error("a root with symlinked children got the whole-path sentence")
	}
	if !strings.Contains(children, "3") {
		t.Errorf("the count is missing: %q", children)
	}

	// AND A PLAIN ROOT IS STILL SILENT.
	if s := nestedDumpRootMessage(dump.DumpRootInspection{IsRoot: true}, dump.ExtensionLayoutSummary{}); s != "" {
		t.Errorf("an ordinary correct root produced %q", s)
	}

	// No тире in any of them.
	for _, m := range []string{got, children} {
		for _, r := range []rune{'—', '–', '‒', '―', '−'} {
			if strings.ContainsRune(m, r) {
				t.Errorf("customer-facing RU carries U+%04X: %s", r, m)
			}
		}
	}
}

// dashRunes is the ONE set both the scan and its control read, for the reason the
// tools package gives at its own copy: two spellings of one intention is how a
// control keeps passing after the set it guards is emptied.
var dashRunes = []rune{'‒', '–', '—', '―', '−'}

// doubtCounterFields names every counter of ExtensionLayoutSummary that Undecided()
// adds up, discovered BY VALUE FLOW: set one int field and ask Undecided(). A
// counter added to the type is discovered on the next run, which is the property the
// hand-written table below cannot have.
func doubtCounterFields(t *testing.T, n int) []string {
	t.Helper()
	typ := reflect.TypeOf(dump.ExtensionLayoutSummary{})
	var names []string
	for i := range typ.NumField() {
		if typ.Field(i).Type.Kind() != reflect.Int {
			continue
		}
		v := reflect.New(typ).Elem()
		v.Field(i).SetInt(int64(n))
		if v.Interface().(dump.ExtensionLayoutSummary).Undecided() == n {
			names = append(names, typ.Field(i).Name)
		}
	}
	return names
}

// summaryWith builds a summary with one named int counter set to n.
func summaryWith(t *testing.T, field string, n int) dump.ExtensionLayoutSummary {
	t.Helper()
	v := reflect.New(reflect.TypeOf(dump.ExtensionLayoutSummary{})).Elem()
	f := v.FieldByName(field)
	if !f.IsValid() {
		t.Fatalf("ExtensionLayoutSummary has no field %q", field)
	}
	f.SetInt(int64(n))
	return v.Interface().(dump.ExtensionLayoutSummary)
}

// TestTheDoubtMessageHasASentencePerCounter is the operator-log half of the guard
// the tools package carries for the MCP half.
//
// The two channels drifted apart exactly once and it was not noticed: Malformed had
// a counter, a reason and a branch in extensionLayoutDoubtMessage, and the table in
// TestTheDoubtMessageCountsAndNamesNothing listed the other four. Deleting the
// branch left ./cmd/mcp-1c green. So the branches are not listed here: every counter
// Undecided() adds up must produce a non-empty sentence CARRYING ITS COUNT, and the
// sentences must be pairwise distinct.
func TestTheDoubtMessageHasASentencePerCounter(t *testing.T) {
	const n = 7
	fields := doubtCounterFields(t, n)

	// PREMISE: the discovery found the counters. A loop over nothing is green for
	// every possible implementation.
	if len(fields) < 6 {
		t.Fatalf("discovered %v as the counters Undecided() adds up; there are six such "+
			"counters (the seventh reason, doubtScanTruncated, sets a bool and is not "+
			"one), so this walk is not seeing the type", fields)
	}
	// PREMISE: the discriminator discriminates. Extensions is an int and is not a
	// doubt, so it must not be picked up.
	if slices.Contains(fields, "Extensions") {
		t.Fatalf("discovered %v: Extensions is not a doubt and Undecided() must not "+
			"add it up", fields)
	}

	seen := map[string]string{}
	for _, f := range fields {
		got := extensionLayoutDoubtMessage(summaryWith(t, f, n))
		if got == "" {
			t.Errorf("%s = %d produced no sentence at all, so this doubt never reaches "+
				"the operator", f, n)
			continue
		}
		if !strings.Contains(got, strconv.Itoa(n)) {
			t.Errorf("%s = %d produced a sentence that does not carry the count, so no "+
				"clause in it read that counter:\n%s", f, n, got)
			continue
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%s and %s produce the SAME sentence, so one of them has no "+
				"sentence of its own:\n%s", f, other, got)
			continue
		}
		seen[got] = f
	}
	t.Logf("counters Undecided() adds up: %v", fields)
}
