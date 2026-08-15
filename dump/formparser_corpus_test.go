package dump

import (
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
		"  NoFormRoot                %d",
		root, files, carriers, lists, cfFiles, cfLists,
		trueWithText, trueNoText, falseWithText, falseNoText,
		trueWithText+trueNoText+falseWithText+falseNoText, mainTableAbsent,
		parseErrors, incomplete, noFormRoot)

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
}
