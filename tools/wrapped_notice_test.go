package tools

import (
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

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
	hostile := []string{"Доработки — копия", "ext–2", "A−B"}
	// POSITIVE CONTROL FIRST: the samples carry what is looked for.
	if !strings.ContainsAny(strings.Join(hostile, ""), "—–‒―−") {
		t.Fatal("control failed: the hostile names carry no dash character")
	}

	layouts := []dump.ExtensionLayoutSummary{
		{NotRegular: 1}, {Unreadable: 1}, {ReadTruncated: 1}, {NameRejected: 1},
		{ScanTruncated: true},
		{NotRegular: 1, Unreadable: 2, ReadTruncated: 3, NameRejected: 4, ScanTruncated: true},
	}
	produced, doubts := 0, 0
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
			for _, r := range []rune{'—', '–', '‒', '―', '−'} {
				if strings.ContainsRune(m, r) {
					t.Errorf("customer-facing RU carries U+%04X:\n%s", r, m)
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
	// of the loop, so counting it into `produced` made the total reach six with every
	// doubt branch silent. A scan «over every branch» that reports six while checking
	// none of the six is the shape this file exists to refuse, so the doubt branches
	// are counted apart and all of them must speak.
	if doubts != len(layouts) {
		t.Fatalf("scanned %d doubt notices, want %d: a branch that produced nothing is a "+
			"branch this scan did not check", doubts, len(layouts))
	}
	if produced == 0 {
		t.Fatal("no branch produced a sentence, so the scan measured nothing")
	}
	t.Logf("scanned %d non-empty notices, %d of them doubt branches", produced, doubts)
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
