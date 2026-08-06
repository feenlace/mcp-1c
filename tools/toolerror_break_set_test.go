package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The break set is published in three places and was counted by hand in two of
// them. This file makes the count DERIVED, so it cannot rot again.
//
// WHAT WENT WRONG. headingBreakReplacer's doc opened with a written count of seven
// and then enumerated EIGHT. The stale sentence is described rather than quoted, so
// a grep for it finds the defect and not this repudiation of it. Neither number was
// a typo: the replacer maps eight SPELLINGS and the set contains seven
// RUNES, because CRLF is a two-rune sequence and every other entry is one rune. A
// written numeral has to pick one of the two readings and cannot say which, so the
// numeral is gone from the prose and the relationship is asserted here instead.
//
// WHY IT IS A VALUE-FLOW GUARD AND NOT A SPELLING GUARD. It does not grep for the
// word «seven» anywhere. It reads the pairs back out of the shipped source, checks
// what each one DOES at runtime, and checks the two sets against each other. Adding
// a rune to breakRunes without teaching the replacer fails it; adding a pair to the
// replacer that the heading tests never drive fails it too.

// replacerPairs reads the (old, new) arguments of the headingBreakReplacer literal
// out of the source.
//
// It parses with flag 0, so COMMENTS ARE DROPPED. That is the exclusion that makes
// the count honest: the doc comment above the literal enumerates every one of these
// spellings, and a census that read comments would count each spelling twice and
// report a set twice its real size.
func replacerPairs(t *testing.T) [][2]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "toolerror.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing toolerror.go: %v", err)
	}
	var pairs [][2]string
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if name.Name != "headingBreakReplacer" || i >= len(vs.Values) {
				continue
			}
			call, ok := vs.Values[i].(*ast.CallExpr)
			if !ok {
				t.Fatalf("headingBreakReplacer is not built by a call: %T", vs.Values[i])
			}
			found = true
			var args []string
			for _, a := range call.Args {
				bl, ok := a.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					t.Fatalf("headingBreakReplacer takes a non-literal argument, so this "+
						"census cannot see the whole set: %T", a)
				}
				s, err := strconv.Unquote(bl.Value)
				if err != nil {
					t.Fatalf("unquoting %s: %v", bl.Value, err)
				}
				args = append(args, s)
			}
			if len(args)%2 != 0 {
				t.Fatalf("headingBreakReplacer got %d arguments, which is not pairs", len(args))
			}
			for j := 0; j < len(args); j += 2 {
				pairs = append(pairs, [2]string{args[j], args[j+1]})
			}
		}
		return true
	})
	if !found {
		t.Fatal("premise broken: no headingBreakReplacer declaration found in toolerror.go, " +
			"so every assertion below would hold over an empty set")
	}
	return pairs
}

// TestHeadingBreakReplacerIsExactlyTheBreakSet derives both counts and pins the
// relationship between them.
func TestHeadingBreakReplacerIsExactlyTheBreakSet(t *testing.T) {
	pairs := replacerPairs(t)
	if len(pairs) == 0 {
		t.Fatal("premise broken: the replacer literal parsed to zero pairs")
	}

	// Every spelling maps to the ONE marker. A pair that mapped to a space or to
	// nothing would be a rune-replacing repair, which is the thing this package has
	// refused for customer data.
	for _, p := range pairs {
		if p[1] != "�" {
			t.Errorf("spelling %q maps to %q, not to the U+FFFD marker", p[0], p[1])
		}
	}

	// Split the spellings by what they ARE. This is where the two counts come from.
	var seqs, runesInReplacer []string
	for _, p := range pairs {
		if len([]rune(p[0])) == 1 {
			runesInReplacer = append(runesInReplacer, p[0])
		} else {
			seqs = append(seqs, p[0])
		}
	}

	// The single-rune spellings must be EXACTLY the set the heading tests drive.
	var want []string
	for _, r := range breakRunes {
		want = append(want, string(r))
	}
	sort.Strings(want)
	sort.Strings(runesInReplacer)
	if strings.Join(want, "|") != strings.Join(runesInReplacer, "|") {
		t.Errorf("the replacer's single-rune spellings and breakRunes disagree.\n"+
			" breakRunes: %q\n   replacer: %q", want, runesInReplacer)
	}

	// The only multi-rune spelling is CRLF, and it is FIRST, so a Windows line
	// ending becomes one marker rather than two. strings.Replacer compares its old
	// strings in argument order and never overlaps a match, so the position is the
	// whole mechanism and not a style choice.
	if len(seqs) != 1 || seqs[0] != "\r\n" {
		t.Errorf("multi-rune spellings = %q, want exactly [\"\\r\\n\"]", seqs)
	}
	if pairs[0][0] != "\r\n" {
		t.Errorf("CRLF is not the first pair (first is %q), so a CRLF is matched as CR then "+
			"LF and becomes two markers", pairs[0][0])
	}

	// THE DERIVED COUNTS. Neither number is written down anywhere else.
	if len(pairs) != len(breakRunes)+1 {
		t.Errorf("the replacer maps %d spellings but the break set has %d runes; they differ "+
			"by more than the one CRLF sequence", len(pairs), len(breakRunes))
	}
	t.Logf("derived: %d spellings = %d runes + %d sequence(s)",
		len(pairs), len(breakRunes), len(seqs))
}

// TestHeadingBreakReplacerDoesWhatTheSetSays checks the RUNTIME behaviour of every
// spelling the census found, so the file could not pass by declaring a set it does
// not act on.
func TestHeadingBreakReplacerDoesWhatTheSetSays(t *testing.T) {
	pairs := replacerPairs(t)
	for _, p := range pairs {
		if got := headingBreakReplacer.Replace("А" + p[0] + "Б"); got != "А�Б" {
			t.Errorf("spelling %q: Replace gave %q, want one marker between the two letters",
				p[0], got)
		}
	}

	// A CRLF really does collapse to ONE marker rather than two.
	if got := headingBreakReplacer.Replace("А\r\nБ"); got != "А�Б" {
		t.Errorf("CRLF produced %q, want a single marker", got)
	}

	// CONTROL: a rune NOT in the set is untouched, so "everything became a marker"
	// cannot pass this test.
	for _, r := range []rune{'\t', '`', '#', '—', ' ', 'ё'} {
		s := "А" + string(r) + "Б"
		if got := headingBreakReplacer.Replace(s); got != s {
			t.Errorf("U+%04X is not a line break but was rewritten: %q -> %q", r, s, got)
		}
	}
}
