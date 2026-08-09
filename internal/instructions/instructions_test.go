package instructions

import (
	"strings"
	"testing"
)

// instructions_test.go guards properties of the TEXT ITSELF. What the text says
// about the server is guarded from the two places that can call the server:
// server/instructions_contract_test.go (a live session, the live registry) and
// tools/instructions_contract_test.go (the renderers and the corpus).
//
// EVERY TEST HERE IS OF THE FORM "the text does not contain X", and that family
// is vacuously true of an empty string. So each one asserts the constant is
// non-empty first, and the anchors that make an emptied constant fail outright
// live in the other two files: the six tool names must be substrings, and the two
// corpus numbers must be substrings.

// dashRunes is the ONE set both the scan and its control read, for the reason the
// cmd/mcp-1c copy gives: two spellings of one intention is how a control keeps
// passing after the set it guards is emptied.
//
// Wider than the cmd copy by the two ASCII-adjacent hyphens, because this text is
// authored rather than assembled from disk content, and a pasted U+2010 is the
// shape an author introduces.
var dashRunes = []rune{'‐', '‑', '‒', '–', '—', '―', '−'}

// bannedProhibitions is the negative-imperative ban.
//
// WHY IT IS A BAN AND NOT A REVIEW NOTE. An over-broad "do not call X" fires on
// every session, so it suppresses the calls the user needed at the rate the text
// is read, and the product cannot report it: a call that was never made leaves no
// log line, and there is no telemetry in this tree. The failure is therefore
// invisible in exactly the way that makes it expensive, and the only affordable
// defence is to forbid the shape.
//
// It catches a CLASS OF PHRASINGS, not the class of intentions. A prohibition
// spelled some way not listed here passes, and nothing in this file can change
// that. Written down so the green is read for what it is.
//
// Matching is case-folded and by substring, so "не вызывай" also catches
// "Не вызывайте".
var bannedProhibitions = []string{
	"не вызывай",
	"не запрашивай",
	"не используй",
	"не пытайся",
	"не повторяй",
	"не проси",
	"не обращайся",
	"незачем",
	"бессмысленно",
	"бесполезно",
	"нет смысла",
	"не имеет смысла",
	"не стоит",
	"не надо",
	"не нужно",
	"избегай",
	"запрещено",
	"никогда не",
}

// findProhibition is the detector, written as a function over its input so the
// control below can aim it at text that is known to carry one.
func findProhibition(s string) string {
	lower := strings.ToLower(s)
	for _, p := range bannedProhibitions {
		if strings.Contains(lower, p) {
			return p
		}
	}
	return ""
}

// findDash is the same shape for the dash scan.
func findDash(s string) (rune, bool) {
	for _, r := range dashRunes {
		if strings.ContainsRune(s, r) {
			return r, true
		}
	}
	return 0, false
}

func requireNonEmptyText(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(Text) == "" {
		t.Fatal("premise failed: Text is empty, and every \"does not contain\" assertion in this file is " +
			"vacuously true of an empty string")
	}
}

// TestTextCarriesNoDash is the house rule for customer-facing Russian.
//
// The ASCII hyphen is NOT in the scanned set and must not be: the text names the
// binary (mcp-1c), a flag (--dump) and several tool identifiers, and every ASCII
// hyphen it contains is inside one of those. TestTextHyphensAreOnlyInIdentifiers
// is the half that pins that claim.
func TestTextCarriesNoDash(t *testing.T) {
	requireNonEmptyText(t)

	// PREMISE: the scanned set is populated. Emptied, the scan reports clean for a
	// sentence made entirely of тире.
	if len(dashRunes) < 5 {
		t.Fatalf("dashRunes holds %d runes; the scan is only as wide as this set", len(dashRunes))
	}

	// Positive control, ONE PER RUNE, over the same set and the same detector: a
	// rune that fell out of the set is caught here rather than being silently not
	// looked for.
	for _, r := range dashRunes {
		if _, found := findDash(Text + string(r)); !found {
			t.Errorf("control failed: the scan does not see U+%04X even when it is appended to the text", r)
		}
	}

	if r, found := findDash(Text); found {
		t.Errorf("customer-facing RU carries U+%04X", r)
	}
}

// TestTextHyphensAreOnlyInIdentifiers is the other half of the dash rule: the
// ASCII hyphen is allowed, but only inside a machine identifier the model has to
// type back verbatim.
func TestTextHyphensAreOnlyInIdentifiers(t *testing.T) {
	requireNonEmptyText(t)

	// The identifiers this text is allowed to spell with a hyphen. Anything else
	// carrying one is prose, and prose uses no hyphen.
	allowed := []string{"mcp-1c", "--dump"}

	// PREMISE: every allowed form really does contain a hyphen, so blanking the
	// list cannot turn this into a check of nothing.
	for _, a := range allowed {
		if !strings.Contains(a, "-") {
			t.Fatalf("control failed: allowed form %q carries no hyphen, so it excuses nothing", a)
		}
	}

	stripped := Text
	for _, a := range allowed {
		stripped = strings.ReplaceAll(stripped, a, "")
	}

	// Positive control: the stripping removes the allowed forms and nothing else,
	// so a hyphen introduced anywhere else survives it.
	if !strings.Contains(stripped+"тест-строка", "-") {
		t.Fatal("control failed: the residue scan cannot see a hyphen at all")
	}

	if i := strings.Index(stripped, "-"); i >= 0 {
		from := max(0, i-40)
		to := min(len(stripped), i+40)
		t.Errorf("an ASCII hyphen appears outside an identifier, near: %q", stripped[from:to])
	}
}

// TestTextIsSafeInARawStringLiteral pins the reason instructions.go may use a raw
// string literal at all. A backtick in the text cannot be escaped inside one, so
// the day one arrives the constant has to be rewritten rather than quietly broken.
func TestTextIsSafeInARawStringLiteral(t *testing.T) {
	requireNonEmptyText(t)

	// Positive control on the same operator the assertion uses.
	if !strings.Contains(Text+"`", "`") {
		t.Fatal("control failed: the scan cannot see a backtick even when one is appended")
	}

	if strings.Contains(Text, "`") {
		t.Error("the text contains a backtick, so it can no longer live in a Go raw string literal")
	}
}

// TestTextCarriesNoBareProhibition is the negative-imperative ban.
func TestTextCarriesNoBareProhibition(t *testing.T) {
	requireNonEmptyText(t)

	// PREMISE: the ban list is populated. Emptied, the loop below runs zero
	// controls and findProhibition returns "" for a text made entirely of
	// prohibitions, which is a green that means nothing was looked for.
	if len(bannedProhibitions) < 10 {
		t.Fatalf("bannedProhibitions holds %d entries; the scan is only as wide as this list",
			len(bannedProhibitions))
	}

	// PREMISE plus positive control, one per entry: the detector really does fire
	// on every phrase the list claims to catch. An entry misspelled into something
	// that can never match is exactly the way this guard would stop guarding, and
	// it would otherwise look identical to a clean text.
	for _, p := range bannedProhibitions {
		if got := findProhibition(Text + " " + p); got == "" {
			t.Errorf("control failed: the detector does not fire on %q", p)
		}
		// And it is case-folded, which is the whole reason the detector lowers its
		// input rather than comparing raw. ToUpper is applied to the whole token so
		// it stays rune-correct on Cyrillic.
		if got := findProhibition(Text + " " + strings.ToUpper(p)); got == "" {
			t.Errorf("control failed: the detector does not fire on %q in upper case", p)
		}
	}

	if got := findProhibition(Text); got != "" {
		i := strings.Index(strings.ToLower(Text), got)
		from := max(0, i-60)
		to := min(len(Text), i+60)
		t.Errorf("the text carries the bare prohibition %q, near: %q\n"+
			"Recast it as a fact plus the condition it holds under, or delete it: a prohibition that "+
			"fires on every session suppresses calls the user needed, and nothing here can report that.",
			got, Text[from:to])
	}
}
