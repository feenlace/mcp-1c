package tools

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// dashRunes is the ONE set both the scan and its control read.
//
// They used to be two spellings of the same intention, a rune slice in the scan and
// a string literal in the control, and two spellings is how a control keeps passing
// after the thing it guards is emptied: delete every rune from the scan and the
// control still found its dash in its own literal. Sharing the set makes the
// control's own premise depend on the set being populated, and doubtCounterFields'
// neighbours below assert that it is.
var dashRunes = []rune{'‒', '–', '—', '―', '−'}

// The two notices added beside the collapse one, and the case that had no channel
// at all.
//
// A --dump pointed TWO levels above a dump root holding one extension collides with
// nothing: every path anchors on the metadata kind, derives a perfectly ordinary
// base-configuration key, and the collapse counter has nothing to count. The
// startup check cannot see it either, because one ReadDir cannot tell that tree
// from a hand-made one holding a single kind directory. So the extension namespace
// vanished and every channel stayed quiet. What can see it is a measurement taken
// AFTER the keys are derived: how many files were keyed from a path the anchor scan
// had to move.

const wrappedMarker = "имена модулей выведены не от корня выгрузки"
const doubtMarker = "не смог отнести к расширениям"

func TestWrappedNotice_TheSentenceMatchesTheState(t *testing.T) {
	if got := indexWrappedNotice(dump.WrappedPathState{}); got != "" {
		t.Errorf("a clean state produced a notice: %q", got)
	}
	if got := indexWrappedNotice(dump.WrappedPathState{Total: 100}); got != "" {
		t.Errorf("a state with no wrapped files produced a notice: %q", got)
	}
	got := indexWrappedNotice(dump.WrappedPathState{Files: 13575, Total: 13575})
	if !strings.Contains(got, wrappedMarker) {
		t.Fatalf("the notice is missing its marker: %q", got)
	}
	// THE PROPORTION, not just the count. It is what tells a reader whether they
	// have a handful of odd files or the whole dump, and those call for different
	// actions.
	if !strings.Contains(got, "13575 из 13575") {
		t.Errorf("the notice does not carry the proportion:\n%s", got)
	}
	if !strings.Contains(got, "reload_dump") || !strings.Contains(got, "--dump") {
		t.Errorf("the notice offers no remedy:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("the notice does not end in a newline: %q", got)
	}
	// A different state produces a different sentence, so the numbers above are
	// read from the state and not printed from constants.
	if other := indexWrappedNotice(dump.WrappedPathState{Files: 3, Total: 13575}); other == got {
		t.Error("two different states produced the same sentence")
	}
}

func TestDoubtNotice_TheSentenceMatchesTheState(t *testing.T) {
	if got := indexLayoutDoubtNotice(dump.ExtensionLayoutSummary{Extensions: 4}); got != "" {
		t.Errorf("a layout with nothing undecided produced a notice: %q", got)
	}
	for _, tc := range []struct {
		layout dump.ExtensionLayoutSummary
		want   string
	}{
		{dump.ExtensionLayoutSummary{NotRegular: 2}, "не обычным файлом, каталогов: 2."},
		{dump.ExtensionLayoutSummary{Unreadable: 1}, "Не удалось прочитать Configuration.xml, каталогов: 1."},
		{dump.ExtensionLayoutSummary{ReadTruncated: 5}, "не поместился в окно чтения, каталогов: 5."},
		{dump.ExtensionLayoutSummary{NameRejected: 3}, "как часть ключа, каталогов: 3."},
		{dump.ExtensionLayoutSummary{Malformed: 4}, "не закрыт комментарий, блок CDATA или инструкция обработки, каталогов: 4."},
		{dump.ExtensionLayoutSummary{Unscannable: 6}, "объявление DOCTYPE или другое объявление разметки, границы которого сервер не определяет, каталогов: 6."},
		{dump.ExtensionLayoutSummary{ScanTruncated: true}, "не все подкаталоги"},
	} {
		got := indexLayoutDoubtNotice(tc.layout)
		if !strings.Contains(got, doubtMarker) {
			t.Errorf("layout %+v produced no notice at all", tc.layout)
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("layout %+v produced %q, want it to carry %q", tc.layout, got, tc.want)
		}
	}
}

// TestBothNewNoticesCarryNoDashAndNoDiskContent. Neither of these two ever echoes
// anything read off disk, and this is the scan that says so over every branch.
func TestBothNewNoticesCarryNoDashAndNoDiskContent(t *testing.T) {
	// PREMISE: the set the scan reads is populated. Emptied, every ContainsRune below
	// is vacuously false and a notice made entirely of тире passes.
	if len(dashRunes) < 5 {
		t.Fatalf("dashRunes holds %d runes; the scan is only as wide as this set",
			len(dashRunes))
	}
	hostile := []string{"Доработки — копия", "ext–2", "A−B"}
	// POSITIVE CONTROL FIRST, over the SAME set the scan uses: the samples carry what
	// is looked for, and the looking is done by the code under test's own rule.
	if !strings.ContainsAny(strings.Join(hostile, ""), string(dashRunes)) {
		t.Fatal("control failed: the hostile names carry no dash character")
	}

	layouts := []dump.ExtensionLayoutSummary{
		{NotRegular: 1}, {Unreadable: 1}, {ReadTruncated: 1}, {NameRejected: 1},
		{Malformed: 1}, {Unscannable: 1}, {ScanTruncated: true},
		{NotRegular: 1, Unreadable: 2, ReadTruncated: 3, NameRejected: 4,
			Malformed: 5, Unscannable: 6, ScanTruncated: true},
	}
	produced, doubts, scanned := 0, 0, 0
	for _, layout := range layouts {
		layout.Dirs = hostile
		layout.SelfNamed = true
		layout.Extensions = len(hostile)
		doubt := indexLayoutDoubtNotice(layout)
		if doubt != "" {
			doubts++
		}
		msgs := []string{
			doubt,
			indexWrappedNotice(dump.WrappedPathState{Files: 7, Total: 9}),
		}
		for _, m := range msgs {
			if m == "" {
				continue
			}
			produced++
			// PER CODEPOINT, not per substring: a dash is a rune and the answer has to
			// be «no codepoint of this sentence is one of them», which is also the only
			// form that stays true if a rune is added to dashRunes.
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
					t.Errorf("a directory name read off disk was spliced into the notice:\n%s", m)
				}
			}
			// Every line of a notice stays inside the blockquote it opened.
			for _, line := range strings.Split(strings.TrimSpace(m), "\n") {
				if !strings.HasPrefix(line, "> ") {
					t.Errorf("a line escaped the notice structure: %q", line)
				}
			}
		}
	}
	// THE COUNT THAT SAYS THE SCAN REACHED THE BRANCHES IT NAMES. The wrapped notice
	// does not vary with the layout: it is the same non-empty sentence on every turn
	// of the loop, so counting it into `produced` let the total reach len(layouts)
	// with every doubt branch silent. A scan «over every branch» that reports a full
	// count while checking none of the branches is the shape this file exists to
	// refuse, so the doubt branches are counted apart and all of them must speak.
	//
	// The number is taken from len(layouts) and not written out, because the two
	// disagreed: the prose said six while the literal below holds the six doubt
	// kinds plus a truncation row plus a combined row.
	if doubts != len(layouts) {
		t.Fatalf("scanned %d doubt notices, want %d: a branch that produced nothing is a "+
			"branch this scan did not check", doubts, len(layouts))
	}
	if produced == 0 {
		t.Fatal("no branch produced a sentence, so the scan measured nothing")
	}
	// THE CODEPOINTS THE SCAN ACTUALLY VISITED. «No dash found» over zero codepoints
	// is the same green as «no dash found» over all of them.
	if scanned == 0 {
		t.Fatal("the per-codepoint scan visited no codepoint at all")
	}
	t.Logf("scanned %d codepoints across %d non-empty notices, %d of them doubt branches",
		scanned, produced, doubts)
}

// doubtCounterFields names every counter of ExtensionLayoutSummary that Undecided()
// actually adds up, discovered BY VALUE FLOW and not by a list written out here.
//
// The discriminator is the value, not the spelling: set one int field to n and ask
// Undecided(). Extensions is an int too and does not move it, so it is not a doubt
// counter and is not returned. A counter ADDED to the type is discovered on the next
// run without anybody remembering to extend a table, which is the property a
// hand-written list cannot have and the reason Malformed sat uncovered.
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

// TestDoubtNotice_EveryCounterUndecidedAddsUpHasItsOwnSentence is the guard the
// hand-written table above could not be.
//
// Malformed had been a counter, a doubt reason and a row in indexLayoutDoubtNotice
// for a whole branch, and DELETING THAT ROW LEFT ./tools GREEN, because the table in
// TestDoubtNotice_TheSentenceMatchesTheState listed the other four and nobody added
// the fifth. A table that has to be extended by hand is a table that documents which
// rows somebody remembered.
//
// So the rows are not listed. Every counter Undecided() adds up must produce a
// non-empty notice CARRYING ITS COUNT, and the notices must be pairwise DISTINCT:
// the count is what proves the sentence read the state, and the distinctness is what
// stops one generic sentence from covering every reason at once. Delete a row and
// its counter's notice loses the number; write two rows the same and the pair
// collides.
func TestDoubtNotice_EveryCounterUndecidedAddsUpHasItsOwnSentence(t *testing.T) {
	const n = 7
	fields := doubtCounterFields(t, n)

	// PREMISE: the discovery found the counters. Zero of them means the walk failed,
	// and a loop over nothing is green for every possible implementation.
	if len(fields) < 6 {
		t.Fatalf("discovered %v as the counters Undecided() adds up; there are six such "+
			"counters (the seventh reason, doubtScanTruncated, sets a bool and is not "+
			"one), so this walk is not seeing the type", fields)
	}
	// PREMISE: the discriminator discriminates. Extensions is an int and must NOT be
	// picked up, or «every counter has a sentence» would demand one for a count that
	// is not a doubt at all.
	if slices.Contains(fields, "Extensions") {
		t.Fatalf("discovered %v: Extensions is not a doubt and Undecided() must not "+
			"add it up", fields)
	}

	seen := map[string]string{}
	for _, f := range fields {
		got := indexLayoutDoubtNotice(summaryWith(t, f, n))
		if got == "" {
			t.Errorf("%s = %d produced no notice at all: a doubt that both delivery "+
				"channels stay silent about", f, n)
			continue
		}
		if !strings.Contains(got, strconv.Itoa(n)) {
			t.Errorf("%s = %d produced a notice that does not carry the count, so no "+
				"sentence in it read that counter:\n%s", f, n, got)
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

// TestWrappedNotice_ADumpTwoLevelsUpIsReported is the end-to-end case: a real tree,
// a real index, a real search_code answer.
//
// THE POINT IS THE SILENCE OF EVERYTHING ELSE. The assertions below deliberately
// check that this dump collides with nothing, so the notice under test is the only
// thing that could have reported it.
func TestWrappedNotice_ADumpTwoLevelsUpIsReported(t *testing.T) {
	parent := t.TempDir()
	body := func(n string) string {
		return "Процедура " + n + "()\n    Сообщить(\"" + collapseTerm + "\");\nКонецПроцедуры\n"
	}
	// One extension, two levels below the path the operator typed.
	mkBSL(t, parent, "wrap/МоёРасш/Catalogs/Ном/Ext/ObjectModule.bsl", body("Первый"))
	mkBSL(t, parent, "wrap/МоёРасш/CommonModules/Общий/Ext/Module.bsl", body("Второй"))
	mkBSL(t, parent, "wrap/МоёРасш/Configuration.xml", "")

	idx := collapseIndex(t, parent, false)

	// PREMISE, asserted rather than assumed: nothing collides, so the collapse
	// notice cannot be what reports this.
	if st := idx.CollapsedKeys(); st.Files != 0 {
		t.Fatalf("premise broken: this tree collides (%+v), so the wrapped notice is not the "+
			"only channel that could fire", st)
	}
	// PREMISE: the extension namespace really is gone, which is the harm.
	for _, n := range idx.ModuleNames() {
		if strings.HasPrefix(n, "ext.") {
			t.Fatalf("premise broken: %q kept its namespace, so there is nothing to report", n)
		}
	}
	wp := idx.WrappedPaths()
	if wp.Files != wp.Total || wp.Total == 0 {
		t.Fatalf("WrappedPaths = %+v, want every file counted as wrapped", wp)
	}

	text := callSearchCollapse(t, idx)
	if !strings.Contains(text, wrappedMarker) {
		t.Fatalf("a --dump two levels above the root produced no notice at all:\n%s", text)
	}
	if strings.Contains(text, collapseMarker) {
		t.Errorf("the collapse notice fired for a dump that lost nothing to a collision:\n%s", text)
	}
	if !strings.HasPrefix(text, "> ") {
		t.Errorf("the notice is not the first thing in the answer:\n%s", text)
	}
	if searchRenderedMatches(text) == 0 {
		t.Errorf("the answer carries no matches at all:\n%s", text)
	}
}

// TestWrappedNotice_ACorrectlyPointedDumpIsSilent is the control that stops the
// above from passing on a build that always warns.
func TestWrappedNotice_ACorrectlyPointedDumpIsSilent(t *testing.T) {
	root := t.TempDir()
	body := func(n string) string {
		return "Процедура " + n + "()\n    Сообщить(\"" + collapseTerm + "\");\nКонецПроцедуры\n"
	}
	mkBSL(t, root, "Catalogs/Ном/Ext/ObjectModule.bsl", body("Первый"))
	mkBSL(t, root, "CommonModules/Общий/Ext/Module.bsl", body("Второй"))
	mkBSL(t, root, "Configuration.xml", "")

	idx := collapseIndex(t, root, false)
	if wp := idx.WrappedPaths(); wp.Files != 0 {
		t.Fatalf("WrappedPaths = %+v at a correctly pointed root, want none", wp)
	}
	text := callSearchCollapse(t, idx)
	for _, marker := range []string{wrappedMarker, collapseMarker, doubtMarker} {
		if strings.Contains(text, marker) {
			t.Errorf("a correctly pointed dump carried %q:\n%s", marker, text)
		}
	}
	if searchRenderedMatches(text) == 0 {
		t.Fatalf("the control answered with no matches, so the silences above are about an "+
			"empty answer:\n%s", text)
	}
}

// TestDoubtNotice_AnUndecidableManifestReachesTheAnswer drives the third detection
// answer all the way to a tool response.
func TestDoubtNotice_AnUndecidableManifestReachesTheAnswer(t *testing.T) {
	root := t.TempDir()
	body := func(n string) string {
		return "Процедура " + n + "()\n    Сообщить(\"" + collapseTerm + "\");\nКонецПроцедуры\n"
	}
	mkBSL(t, root, "Расш/Catalogs/Ном/Ext/ObjectModule.bsl", body("Первый"))
	mkBSL(t, root, "CommonModules/Общий/Ext/Module.bsl", body("Второй"))
	// A manifest declaring a name that cannot be part of a key: the extension is
	// refused a namespace, which is the safe direction, and it is only safe if it
	// is said.
	mkBSL(t, root, "Расш/Configuration.xml",
		"\ufeff<MetaDataObject><Configuration><Properties>"+
			"<ObjectBelonging>Adopted</ObjectBelonging><Name>Доработки — копия</Name>"+
			"</Properties></Configuration></MetaDataObject>")

	idx := collapseIndex(t, root, false)
	layout := idx.ExtensionLayout()
	if layout.NameRejected != 1 {
		t.Fatalf("ExtensionLayout = %+v, want one rejected name", layout)
	}
	text := callSearchCollapse(t, idx)
	if !strings.Contains(text, doubtMarker) {
		t.Fatalf("a refused extension name produced no notice:\n%s", text)
	}
	if strings.Contains(text, "Доработки — копия") {
		t.Errorf("the rejected name was echoed into the answer:\n%s", text)
	}
}

// falseNamespaceClaims are the phrases the wrapped notice must not carry: each
// asserts, as fact, that the extension namespace was lost. The counter behind the
// notice does not measure that, and the tree below is one where it is false.
var falseNamespaceClaims = []string{
	"пространство имён расширения при этом теряется",
	"модули расширения попадают туда же",
	"теряется",
}

// TestWrappedNotice_SaysOnlyWhatItCounted is FIX 3: a notice that was numerically
// right and causally wrong.
//
// THE TREE IS ORDINARY, not hostile. One --dump holding a recognised extension and
// a base-configuration dump side by side, «ext» and «main». The two files under main
// are wrapped, so the notice fires and «2 из 4» is exactly right; the two files
// under ext keep their namespace and ext.FeenlaceMCPService.* is served IN THE SAME
// ANSWER. The old clause «пространство имён расширения при этом теряется» was
// therefore refuted by the very answer it was printed on.
func TestWrappedNotice_SaysOnlyWhatItCounted(t *testing.T) {
	root := t.TempDir()
	body := func(n string) string {
		return "Процедура " + n + "()\n    Сообщить(\"" + collapseTerm + "\");\nКонецПроцедуры\n"
	}
	mkBSL(t, root, "ext/Configuration.xml",
		"\ufeff<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<MetaDataObject><Configuration>"+
			"<Properties><ObjectBelonging>Adopted</ObjectBelonging>"+
			"<Name>FeenlaceMCPService</Name></Properties></Configuration></MetaDataObject>")
	mkBSL(t, root, "ext/CommonModules/Расш1/Ext/Module.bsl", body("РасшПервый"))
	mkBSL(t, root, "ext/CommonModules/Расш2/Ext/Module.bsl", body("РасшВторой"))
	mkBSL(t, root, "main/CommonModules/Общий/Ext/Module.bsl", body("Основной"))
	mkBSL(t, root, "main/Catalogs/Ном/Ext/ObjectModule.bsl", body("Второй"))

	idx := collapseIndex(t, root, false)

	// PREMISE ONE: the notice really does fire on this tree, or there is no sentence
	// to be wrong about.
	if wp := idx.WrappedPaths(); wp.Files == 0 {
		t.Fatalf("premise broken: WrappedPaths = %+v, the notice does not fire here", wp)
	}
	// PREMISE TWO: the namespace really is intact, which is what makes the old clause
	// false rather than merely unproven.
	kept := 0
	for _, n := range idx.ModuleNames() {
		if strings.HasPrefix(n, "ext.FeenlaceMCPService.") {
			kept++
		}
	}
	if kept == 0 {
		t.Fatalf("premise broken: no ext.FeenlaceMCPService.* key survived, so the clause "+
			"under test would have been true here; names = %v", idx.ModuleNames())
	}

	text := callSearchCollapse(t, idx)
	if !strings.Contains(text, wrappedMarker) {
		t.Fatalf("the wrapped notice did not reach the answer:\n%s", text)
	}
	// AND THE NAMESPACE IS IN THE SAME ANSWER, measured on the rendered bytes rather
	// than inferred from the index: that is what refutes the clause.
	if !strings.Contains(text, "ext.FeenlaceMCPService.") {
		t.Fatalf("the answer carries no extension key, so this tree cannot show the "+
			"clause being false:\n%s", text)
	}

	notice := indexWrappedNotice(idx.WrappedPaths())
	// POSITIVE CONTROL FIRST: the phrase list actually catches the sentence it was
	// written against. Without this the loop below passes for an empty list.
	const oldClause = "Имена таких модулей сервер восстанавливает, но пространство имён " +
		"расширения при этом теряется: модули расширения попадают туда же, куда модули " +
		"конфигурации."
	caught := 0
	for _, claim := range falseNamespaceClaims {
		if strings.Contains(oldClause, claim) {
			caught++
		}
	}
	if caught != len(falseNamespaceClaims) {
		t.Fatalf("control failed: the phrase list caught %d of its %d phrases in the "+
			"sentence it was written against, so it is not the check it claims to be",
			caught, len(falseNamespaceClaims))
	}

	for _, claim := range falseNamespaceClaims {
		if strings.Contains(notice, claim) {
			t.Errorf("the notice asserts %q, which this very answer refutes: %d extension "+
				"keys are served beside it. The counter measures that anchorIndex moved "+
				"and nothing else.\n%s", claim, kept, notice)
		}
	}

	// AND IT STILL SAYS THE MECHANISM AND THE REMEDY, or «no false claim» would be
	// satisfied by deleting the explanation altogether.
	for _, want := range []string{"подкаталогах первого уровня", "Чего это стоило, счётчик не измеряет"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the notice no longer carries %q, so the clause was removed rather "+
				"than corrected:\n%s", want, notice)
		}
	}
}
