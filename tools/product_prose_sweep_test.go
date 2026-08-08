package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
)

// ===========================================================================
// WHICH LINES OF A SEARCH ANSWER ARE THIS SERVER'S OWN WORDS.
//
// The denylist sweeps in this package check that no product note names a cause
// the search did not establish. A sweep like that is only as good as its BOUND:
// too wide and it reddens on the customer's source, too narrow and the sentence
// it exists to keep out escapes into a line it does not look at.
// ===========================================================================

// productProseLines returns the lines of a rendered search answer that are the
// product's own notes.
//
// THE BLOCKQUOTE PREFIX ALONE IS NOT A BOUND. This helper used to be one line -
// keep what starts with «> », drop the rest - and the comment above it said that
// bound «can never redden on a module body» because «product notes are
// blockquotes and the customer's own code is not». That is false, and it is the
// renderer in this same package that falsifies it:
// tools/search.go:FormatSearchResultWithStats writes each match body through
// tools/toolerror.go:fencedAs, which returns
// fence + "\n" + text + "\n" + fence, so every line of the customer's source
// lands at column 0 with no prefix and no indent. A .bsl line that opens «> » is
// then byte-for-byte a blockquote, and one is trivially producible inside a 1C
// multi-line string literal, where «|> ...» is not even needed - a plain line of
// text may start with the character. TestTheProseSweepSkipsTheCustomersBody
// drives exactly that through the real renderer.
//
// THE BOUND THAT HOLDS IS THE FENCE. What a search answer puts between fences is
// payload and nothing else; what it writes outside them it wrote itself. The
// blockquote prefix is KEPT as a second, narrowing bound - it selects notes from
// the product's other outside-the-fence lines, chiefly the «### » heading, which
// carries the customer's module NAME and would be a false-positive source of its
// own.
//
// The scanner mirrors what fencedAs emits rather than all of CommonMark: an
// opening fence is a run of at least three backticks at column 0 followed by an
// info string carrying no backtick, and only a line that is nothing but at least
// that many backticks closes it. fenceLen makes the delimiter longer than the
// longest run in the payload, so no body line can close the block that wraps it,
// which is what lets a scanner this simple stay in step with the renderer.
//
// It returns the open fence's run length as well, and it is not decoration: a
// caller that gets a non-zero one has swept an answer whose tail was never
// examined at all, and a sweep that stopped looking is not a sweep.
func productProseLines(answer string) (prose []string, unclosedFence int) {
	fence := 0
	for _, line := range strings.Split(answer, "\n") {
		if fence == 0 {
			if n := openingFenceRun(line); n > 0 {
				fence = n
				continue
			}
			if strings.HasPrefix(line, "> ") {
				prose = append(prose, line)
			}
			continue
		}
		if n := closingFenceRun(line); n >= fence {
			fence = 0
		}
	}
	return prose, fence
}

// openingFenceRun is the length of the backtick run that opens a fenced block on
// this line, or 0 when the line does not open one. The info string may not carry
// a backtick, which is the same rule fencedAs is built on.
func openingFenceRun(line string) int {
	n := 0
	for n < len(line) && line[n] == '`' {
		n++
	}
	if n < 3 {
		return 0
	}
	if strings.ContainsRune(line[n:], '`') {
		return 0
	}
	return n
}

// closingFenceRun is the length of the backtick run on a line that is nothing
// but backticks, or 0 for any other line.
func closingFenceRun(line string) int {
	trimmed := strings.TrimRight(line, " \t")
	if len(trimmed) < 3 {
		return 0
	}
	if strings.Trim(trimmed, "`") != "" {
		return 0
	}
	return len(trimmed)
}

// TestTheProseSweepSkipsTheCustomersBody drives the REAL renderer over a module
// body that opens a line with «> », and checks that the sweep reads it as the
// customer's source rather than as one of this server's notes.
//
// IT CHECKS BOTH DIRECTIONS IN ONE ANSWER, because either alone passes for the
// wrong reason: a sweep that returns nothing at all satisfies «the body is not
// swept», and a sweep that returns every line satisfies «a note is caught». The
// answer below carries one of each and the assertion is the exact set.
//
// The body is a comparison continued onto its own line, which 1C parses as one
// expression because a newline inside one is whitespace. Nothing here rests on
// that being idiomatic: FormatSearchResultWithStats copies dump.Match.Context
// into the answer byte for byte and makes no claim about what a .bsl file may
// hold.
func TestTheProseSweepSkipsTheCustomersBody(t *testing.T) {
	const bodyLine = "> 0 Тогда // старые файлы удалены"
	body := "Если Остаток\n" + bodyLine + "\n    Сообщить(Остаток);\nКонецЕсли;"

	answer := FormatSearchResultWithStats(
		[]dump.Match{{Module: "ОбщийМодуль.Проба", Line: 2, Context: body}},
		dump.SearchStats{Total: 1}, "Остаток", dump.SearchModeExact, nil)

	// CONTROL: the body really did land at column 0, so the line below is a
	// blockquote by prefix and the sweep has something to get wrong.
	if !strings.Contains(answer, "\n"+bodyLine+"\n") {
		t.Fatalf("control failed: the renderer did not put the body line at column 0, so this "+
			"test measures nothing.\n%s", answer)
	}

	// A REAL PRODUCT-PROSE VIOLATION in the same answer: a note, outside every
	// fence, naming a cause the search did not establish. This is the shape the
	// sweep exists to catch and it must survive the new bound.
	const note = "> Часть модулей не показана: их файлы удалены."
	answer += note + "\n"

	prose, unclosed := productProseLines(answer)
	if unclosed != 0 {
		t.Fatalf("the sweep ended inside an unclosed fence of %d backticks, so the tail of the "+
			"answer was never examined.\n%s", unclosed, answer)
	}
	for _, line := range prose {
		if line == bodyLine {
			t.Errorf("the sweep reads the customer's own source as one of this server's notes. "+
				"Every line of dump.Match.Context lands at column 0, so «> » alone does not "+
				"separate them.\nline: %q", line)
		}
	}
	if !slices.Contains(prose, note) {
		t.Errorf("the sweep no longer sees a note that names a cause: bounding it away from the "+
			"body must not bound it away from the prose.\ngot: %q\nwant to contain: %q",
			prose, note)
	}

	// THE SECOND BOUND, pinned separately. The «### » heading is outside every
	// fence and is NOT prose: it carries the customer's module name, run through
	// inlineCode, and sweeping it would put a denylist over a string the customer
	// chose. Dropping the blockquote test would leave the fence test passing and
	// this heading in the swept set.
	for _, line := range prose {
		if strings.HasPrefix(line, "### ") {
			t.Errorf("the sweep returned a match heading, which carries the customer's module "+
				"name rather than this server's words.\nline: %q", line)
		}
	}
}

// TestTheProseSweepReportsAnUnclosedFence pins the control that the sweep carries
// for its own benefit.
//
// A scanner that tracks fences can be walked off the end of the answer by an
// unbalanced one, and it fails SILENTLY when that happens: everything after the
// stray opener is read as payload and never examined. The tests that sweep for a
// forbidden claim would then pass because they stopped looking, which is the one
// way this bound is worse than the prefix test it replaces. So the sweep reports
// it and every caller treats it as a failed control.
func TestTheProseSweepReportsAnUnclosedFence(t *testing.T) {
	// CONTROL: the same answer with the fence closed is swept to the end.
	closed := "> первая сноска\n```bsl\nЕсли Истина Тогда\n```\n> вторая сноска\n"
	prose, unclosed := productProseLines(closed)
	if unclosed != 0 {
		t.Fatalf("control failed: a balanced answer reports an unclosed fence of %d.", unclosed)
	}
	if len(prose) != 2 {
		t.Fatalf("control failed: a balanced answer yields %d notes, want 2: %q", len(prose), prose)
	}

	// The same text with the closing fence gone. The trailing note is now inside
	// the block as far as any scanner can tell, and the sweep must say so rather
	// than report a clean answer.
	orphaned := "> первая сноска\n```bsl\nЕсли Истина Тогда\n> вторая сноска\n"
	prose, unclosed = productProseLines(orphaned)
	if unclosed != 3 {
		t.Errorf("an answer whose fence is never closed is reported as unclosed=%d; a sweep that "+
			"stopped at a stray opener must not look clean.", unclosed)
	}
	if slices.Contains(prose, "> вторая сноска") {
		t.Errorf("a line after an unclosed opener was returned as prose, so the fence state is "+
			"not being tracked at all.\ngot: %q", prose)
	}
}
