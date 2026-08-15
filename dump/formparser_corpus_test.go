package dump

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpusEnv names the environment variable that points this test at a real
// DumpConfigToFiles tree.
//
// OPT-IN, and deliberately so: the reference tree is 2.9 GB and lives on one
// machine, so a test that required it would fail everywhere else, and a test
// that silently passed without it would be worse still. The variable is the same
// shape as MCP_TOCTOU_STRESS in this package, which gates the other check that
// cannot run everywhere.
const corpusEnv = "MCP_DUMP_CORPUS"

// ---------------------------------------------------------------------------
// The <ListSettings> measurement, and the convention it is meaningless without.
// ---------------------------------------------------------------------------

// listSettingsConvention is the sentence every figure below is measured under,
// and it is interpolated into every failure message rather than kept in a
// comment. A byte count of an XML element is not one quantity: the same block
// measured between its tags, measured across them, and re-serialised through a
// writer gives three different numbers, and re-serialising gives a different
// number again for a different writer. A refusal printing a count without
// naming its convention tells the next reader nothing about which of them moved.
const listSettingsConvention = "convention: bytes of the file's own text, from the `<` of " +
	"<ListSettings> through the `>` of its closing tag, the tags included"

// listSettingsSpans measures every <ListSettings> element in one Form.xml
// against listSettingsConvention and reports how many of them were written as an
// empty-element tag.
//
// IT READS THE RAW BYTES AND NOT THE PARSE, deliberately: the parser under test
// drops this element entirely, so a measurement taken through it could only ever
// report zero. Working from the file's own text is also what makes the number
// reproducible, which re-serialising is not.
//
// THE EMPTY SPELLING IS NOT HYPOTHETICAL and skipping it would silently shrink
// every total here: 18 of the 1918 elements in the reference dump are written
// <ListSettings/> with no closing tag at all, so a scan that looked only for
// </ListSettings> would find 1900 and call it presence.
func listSettingsSpans(raw []byte) (spans []int, selfClosing int) {
	const open, closeTag = "<ListSettings", "</ListSettings>"
	// The name has to END here: <ListSettingsSomethingElse> is another element.
	isDelim := func(b byte) bool {
		return b == '>' || b == '/' || b == ' ' || b == '\t' || b == '\r' || b == '\n'
	}

	for i := 0; i < len(raw); {
		rel := bytes.Index(raw[i:], []byte(open))
		if rel < 0 {
			break
		}
		start := i + rel
		after := start + len(open)
		if after >= len(raw) || !isDelim(raw[after]) {
			i = after
			continue
		}
		gt := bytes.IndexByte(raw[after:], '>')
		if gt < 0 {
			break
		}
		gt += after
		if raw[gt-1] == '/' {
			spans = append(spans, gt+1-start)
			selfClosing++
			i = gt + 1
			continue
		}
		endRel := bytes.Index(raw[gt:], []byte(closeTag))
		if endRel < 0 {
			break
		}
		end := gt + endRel + len(closeTag)
		spans = append(spans, end-start)
		i = end
	}
	return spans, selfClosing
}

// TestParseFormXML_CorpusCensus runs THIS reader over a whole real configuration
// dump and counts what it finds.
//
// It exists because every other test in this package feeds the reader documents
// chosen to exercise a rule, and a rule can be right on eight fixtures and wrong
// on the ninth thousand. The expected figures below were produced by a SECOND
// reader, written separately in another language against the same tree, so a
// match here is agreement between two independent implementations rather than
// this one reproducing itself.
//
// The census is also the only check that can see a whole CLASS being missed. The
// discriminator this reader turns on is an expanded namespace comparison; if it
// were subtly wrong the fixtures would still pass, because they were written
// from the same understanding, and only the total would move.
func TestParseFormXML_CorpusCensus(t *testing.T) {
	root := os.Getenv(corpusEnv)
	if root == "" {
		// VISIBLE, because a non-verbose `go test` prints no skip line at all and
		// a silent skip is indistinguishable from a pass. Both the log and the
		// skip message name the variable, so a reader of either knows the census
		// did not run.
		t.Logf("SKIPPING the corpus census: %s is not set, so no dump was walked "+
			"and none of the figures below were checked", corpusEnv)
		t.Skipf("set %s to a DumpConfigToFiles root to run the corpus census", corpusEnv)
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		t.Fatalf("%s=%q is not a readable directory: %v", corpusEnv, root, err)
	}

	var (
		files           int
		carriers        int
		lists           int
		cfFiles         int
		cfLists         int
		parseErrors     int
		incomplete      int
		noFormRoot      int
		mainTableAbsent int

		// The ManualQuery split is counted in FOUR buckets and not two, because
		// two buckets hide the case that matters. A list with the flag OFF and a
		// query text stored anyway is a list whose text the platform does not
		// run, and it is the trap a reader of this data walks into; folded into
		// "false" it is invisible.
		//
		// Counting it also catches the mistake this very test was first written
		// with: the expectation said 991 for "ManualQuery true", which is the
		// size of a DIFFERENT population, the lists carrying any query text at
		// all. 991 = 986 + 5, and the 5 are false-with-text. A total without its
		// decomposition cannot tell those two apart.
		trueWithText  int
		trueNoText    int
		falseWithText int
		falseNoText   int

		// ENG-XML-15. <ListSettings> is the block this reader drops, and what
		// justifies dropping it is its REACH and its SIZE. Three quantities are
		// pinned and one is refused:
		//
		//   pinned   presence, which is what makes the channel universal;
		//   pinned   the maximum, under listSettingsConvention;
		//   pinned   the holder of the record, which is what makes the maximum
		//            traceable to a file somebody can open;
		//   refused  the corpus TOTAL. Four runs produced four sums for it
		//            (2082412 / 2081854 / 2130530 / 2026754) while the maximum
		//            and the holder came out identical every time, so the total
		//            measures the convention and the run, not the dump.
		listSettings      int
		listSettingsEmpty int
		// pairingMismatch counts files where the number of <ListSettings>
		// elements in the text is not the number of dynamic lists this reader
		// reported. It is what turns "1918 == 1918" from an equality between two
		// totals into presence per list: two totals can agree while every single
		// pairing behind them is wrong.
		pairingMismatch   int
		maxListSettings   int
		maxListSettingsAt string
	)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "Form.xml" {
			return nil
		}
		files++

		form, perr := ParseFormXML(path)
		if perr != nil {
			parseErrors++
			t.Errorf("parsing %s: %v", strings.TrimPrefix(path, root), perr)
			return nil
		}
		if form.ParseIncomplete {
			incomplete++
		}
		if form.NoFormRoot {
			noFormRoot++
		}

		// ENG-XML-15, measured on EVERY file and not only on the carriers: a
		// <ListSettings> in a file this reader reported no list for is a pairing
		// failure too, and skipping the non-carriers would make it invisible.
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Errorf("re-reading %s for the <ListSettings> measurement: %v",
				strings.TrimPrefix(path, root), rerr)
			return nil
		}
		spans, empties := listSettingsSpans(raw)
		listSettings += len(spans)
		listSettingsEmpty += empties
		if len(spans) != len(form.DynamicLists) {
			pairingMismatch++
			t.Errorf("%s carries %d <ListSettings> elements but this reader reported %d "+
				"dynamic lists, so presence is not one per list here",
				strings.TrimPrefix(path, root), len(spans), len(form.DynamicLists))
		}
		for _, span := range spans {
			if span > maxListSettings {
				maxListSettings = span
				maxListSettingsAt = filepath.ToSlash(
					strings.TrimPrefix(strings.TrimPrefix(path, root), string(os.PathSeparator)))
			}
		}

		if len(form.DynamicLists) == 0 {
			return nil
		}

		carriers++
		lists += len(form.DynamicLists)
		isCommon := strings.Contains(path, string(os.PathSeparator)+commonFormsDumpDir+string(os.PathSeparator))
		if isCommon {
			cfFiles++
			cfLists += len(form.DynamicLists)
		}
		for _, l := range form.DynamicLists {
			if l.MainTable == "" {
				mainTableAbsent++
			}
			hasText := strings.TrimSpace(l.QueryText) != ""
			switch {
			case l.ManualQuery && hasText:
				trueWithText++
			case l.ManualQuery && !hasText:
				trueNoText++
			case !l.ManualQuery && hasText:
				falseWithText++
			default:
				falseNoText++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", corpusEnv, err)
	}

	// Every figure is printed with its breakdown, because a total on its own is
	// green under the wrong split: 1918 is satisfied by 986+932+0 just as well
	// as by the true division.
	t.Logf("census over %s:\n"+
		"  Form.xml files            %d\n"+
		"  carriers / lists          %d / %d\n"+
		"  under CommonForms         %d files / %d lists\n"+
		"  ManualQuery true+text     %d\n"+
		"  ManualQuery true no text  %d\n"+
		"  ManualQuery false+text    %d\n"+
		"  ManualQuery false no text %d\n"+
		"  ManualQuery sum           %d\n"+
		"  MainTable absent          %d\n"+
		"  parse errors              %d\n"+
		"  ParseIncomplete           %d\n"+
		"  NoFormRoot                %d\n"+
		"  ListSettings elements     %d (of them written <ListSettings/>: %d)\n"+
		"  ListSettings pairing bad  %d files\n"+
		"  ListSettings maximum      %d bytes, %s\n"+
		"  ListSettings %s",
		root, files, carriers, lists, cfFiles, cfLists,
		trueWithText, trueNoText, falseWithText, falseNoText,
		trueWithText+trueNoText+falseWithText+falseNoText, mainTableAbsent,
		parseErrors, incomplete, noFormRoot,
		listSettings, listSettingsEmpty, pairingMismatch,
		maxListSettings, maxListSettingsAt, listSettingsConvention)

	// POSITIVE CONTROL: the walk really reached forms. Without this a wrong root
	// would report zeroes and every equality below would be a comparison against
	// an expectation nobody met.
	if files == 0 {
		t.Fatalf("control failed: no Form.xml was found under %q, so this census "+
			"measured nothing", root)
	}

	// The expected values hold for the reference tree only. Another dump has
	// other contents, and the census is then read from the log above rather than
	// asserted, which is why the equalities are guarded by the file count.
	const referenceFiles = 5665
	if files != referenceFiles {
		t.Logf("this is not the reference tree (%d Form.xml files, reference has %d), "+
			"so the figures above are reported and not asserted", files, referenceFiles)
		return
	}

	for _, c := range []struct {
		what      string
		got, want int
	}{
		{"dynamic lists", lists, 1918},
		{"files carrying a list", carriers, 1628},
		{"lists under CommonForms", cfLists, 42},
		{"files under CommonForms carrying a list", cfFiles, 22},
		{"ManualQuery true with a query text", trueWithText, 986},
		{"ManualQuery true with no query text", trueNoText, 0},
		{"ManualQuery false WITH a query text the platform does not run", falseWithText, 5},
		{"ManualQuery false with no query text", falseNoText, 927},
		{"lists with no MainTable", mainTableAbsent, 154},
		{"parse errors", parseErrors, 0},
		// ENG-XML-15, presence. One <ListSettings> per dynamic list, over the
		// whole tree: this is what makes the dropped block a channel that EVERY
		// list has rather than a curiosity of a few.
		{"<ListSettings> elements", listSettings, 1918},
		{"files where <ListSettings> does not pair one to one with the lists", pairingMismatch, 0},
		// Written as an empty-element tag. Pinned because it is the spelling a
		// scan for </ListSettings> alone does not see: drop this branch and the
		// presence figure above becomes 1900 while still looking like a finding.
		{"<ListSettings/> written empty", listSettingsEmpty, 18},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.what, c.got, c.want)
		}
	}
	if sum := trueWithText + trueNoText + falseWithText + falseNoText; sum != lists {
		t.Errorf("the ManualQuery split does not add up: %d + %d + %d + %d = %d, "+
			"but there are %d lists",
			trueWithText, trueNoText, falseWithText, falseNoText, sum, lists)
	}

	// ENG-XML-15, the maximum and its holder. THE CONVENTION IS PART OF THE
	// REFUSAL and not part of a comment: a size for an XML element is three
	// different numbers under three different conventions, and a failure that
	// prints only «got X, want Y» leaves the next reader unable to tell a real
	// change in the dump from a change in how somebody counted. The same block
	// measured between the tags rather than across them is 13652 bytes, and
	// re-serialised through an XML writer it is smaller again and moves with the
	// writer.
	const (
		wantMaxListSettings = 13681
		wantMaxHolder       = "DataProcessors/ДокументооборотСКонтролирующимиОрганами/Forms/" +
			"Документ_ЗаявлениеАбонентаСпецоператораСвязи_ФормаСписка/Ext/Form.xml"
	)
	if maxListSettings != wantMaxListSettings {
		t.Errorf("largest single <ListSettings>: got %d bytes, want %d (%s)",
			maxListSettings, wantMaxListSettings, listSettingsConvention)
	}
	// The holder is pinned SEPARATELY from the size, because the two fail for
	// different reasons: a size that moved with an unchanged holder is a change
	// in the counting, and a holder that moved is a change in the dump.
	if maxListSettingsAt != wantMaxHolder {
		t.Errorf("holder of the <ListSettings> record: got %q, want %q (%s)",
			maxListSettingsAt, wantMaxHolder, listSettingsConvention)
	}
	// The corpus TOTAL is deliberately absent from every assertion above. It is
	// the quantity that produced four values for one measurement, and it is
	// logged nowhere either, so nobody can quote it back as a fact.
}
