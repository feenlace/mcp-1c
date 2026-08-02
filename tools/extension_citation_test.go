package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A citation that names ABSOLUTE LINE NUMBERS of a file this repository also
// edits goes stale on the next commit that touches that file, and it goes stale
// SILENTLY: the command still runs, still prints something plausible, and the
// reader has no way to notice that the window slid off the thing it was meant to
// show.
//
// That is not hypothetical. tools/toolerror.go carried an awk command addressing
// lines 873 through 959 of the extension module as the evidence for a claim about
// ЖурналРегистрацииPOST. The very next commit to the extension grew that function
// to 873..1017, so the window stopped short of the ВыгрузитьЖурналРегистрации
// call it existed to demonstrate: the command then printed the Попытка blocks and
// NOT the call, i.e. it printed a clean-looking result while showing none of the
// evidence.
//
// This block deliberately does NOT reproduce that command verbatim. The guard
// below scans every .go file including this one, and a test that had to exempt
// itself would have a hole exactly the size of the thing it is checking.
//
// The repair is not a corrected range, which would be correct only until the next
// edit. The repair is that a claim about the shipped module is grounded by
// something that PARSES it and FAILS when it stops being true —
// TestEventLogRightsFailureRaisesInsideTheExtension in remedy_truth_test.go —
// and the prose points at that instead of restating it by line offset.
//
// SCOPE, stated so the negative result is not read as more than it is: this
// guard rejects LINE-ADDRESSED extraction (awk NR, sed line ranges, path:line)
// inside comment blocks that talk about the shipped extension. It does not and
// cannot reject every possible way of writing a stale citation, and it
// deliberately does not touch citations into the Go standard library, whose line
// numbers this repository does not move.
// ---------------------------------------------------------------------------

// lineAddressedCitation matches ways of pointing INTO a file by absolute line
// number. Each alternative is a form actually used in this repository's comments.
var lineAddressedCitation = regexp.MustCompile(
	`NR\s*[<>=!]=?\s*\d+` + // awk line addressing: NR>=873
		`|sed\s+-n\s*'?\s*\d+\s*,\s*\d+\s*p` + // sed -n '873,959p'
		`|Module\.bsl:\d+`) // path:line into the shipped module

// bslHandlerNames returns every Функция/Процедура name defined in the shipped
// module. A comment naming one of them is talking about that module even when it
// does not spell the path out, which is exactly how the defect above hid: the
// broken citation said only "<the module>".
func bslHandlerNames(t *testing.T, src string) []string {
	t.Helper()
	// NOT \w: Go's \w is ASCII-only and every handler in this module is Cyrillic,
	// so \w+ matches nothing here. The first draft of this test used it and the
	// control below caught it.
	def := regexp.MustCompile(`(?m)^(?:Функция|Процедура)\s+([^\s(]+)`)
	var names []string
	for _, m := range def.FindAllStringSubmatch(src, -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatal("no Функция/Процедура found in the shipped module; the sieve below would " +
			"then recognise nothing and the whole scan would pass vacuously")
	}
	return names
}

func mentionsShippedExtension(block string, handlers []string) bool {
	if strings.Contains(block, "extension/src") || strings.Contains(block, "<the module>") {
		return true
	}
	for _, h := range handlers {
		if strings.Contains(block, h) {
			return true
		}
	}
	return false
}

// commentBlocks returns the maximal runs of consecutive // lines, with the line
// number the run starts on. Blocks rather than lines because the citation and the
// thing it cites are usually on different lines of the same paragraph.
func commentBlocks(src string) map[int]string {
	lines := strings.Split(src, "\n")
	out := map[int]string{}
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "//") {
			i++
			continue
		}
		j := i
		for j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "//") {
			j++
		}
		out[i+1] = strings.Join(lines[i:j], "\n")
		i = j
	}
	return out
}

func TestNoLineAddressedCitationsIntoTheShippedExtension(t *testing.T) {
	modSrc, err := os.ReadFile(bslModule)
	if err != nil {
		t.Fatalf("reading the shipped extension module: %v", err)
	}
	handlers := bslHandlerNames(t, string(modSrc))

	// CONTROL 1: the detector fires on the exact text that was shipped broken.
	// Without this a regex that matches nothing would make every file below pass.
	broken := "// ЖурналРегистрацииPOST calls it outside every Попытка. Verified with\n" +
		"//\t/usr/bin/awk 'NR>=873 && NR<=959' <the module>"
	if !mentionsShippedExtension(broken, handlers) {
		t.Fatal("CONTROL: the sieve does not recognise the very block this test was written for")
	}
	if !lineAddressedCitation.MatchString(broken) {
		t.Fatal("CONTROL: the detector does not match the citation this test was written for")
	}
	// CONTROL 2: a stdlib citation is NOT flagged, so a pass is not just an
	// everything-is-fine regex.
	stdlib := "// shouldCopyHeaderOnRedirect (/usr/local/go/src/net/http/client.go:1005-1022) сравнивает"
	if lineAddressedCitation.MatchString(stdlib) {
		t.Fatal("CONTROL: the detector flags a Go stdlib citation, which is out of scope")
	}

	root := ".."
	scanned, aboutExtension := 0, 0
	var bad []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "vendor" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for start, block := range commentBlocks(string(src)) {
			if !mentionsShippedExtension(block, handlers) {
				continue
			}
			aboutExtension++
			if m := lineAddressedCitation.FindString(block); m != "" {
				bad = append(bad, path+": comment block at line "+itoa(start)+
					" cites the shipped module by absolute line number ("+m+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	// CONTROL 3: the walk actually reached Go files and actually found blocks
	// about the extension. A zero here would make an empty `bad` meaningless.
	if scanned < 10 {
		t.Fatalf("CONTROL: only %d .go files scanned; the walk did not reach the repository", scanned)
	}
	if aboutExtension == 0 {
		t.Fatal("CONTROL: no comment block about the shipped extension was found at all, so " +
			"finding no bad citation proves nothing")
	}
	t.Logf("scanned %d .go files, %d comment blocks reference the shipped extension",
		scanned, aboutExtension)

	for _, b := range bad {
		t.Errorf("%s\n\tground the claim in a test that parses the module (see "+
			"TestEventLogRightsFailureRaisesInsideTheExtension) rather than in a line range "+
			"that the next edit to the module silently invalidates", b)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
