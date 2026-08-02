package onec

import (
	"net/url"
	"strings"
	"testing"
	"unicode"
)

// urlcred_test.go is the assertion half of the credential split. The table it
// scores lives in urlcred_corpus_test.go, so a mutation of urlcred.go is judged
// by an unchanged judge.

func splitCorpusImpl() corpusImpl {
	return corpusImpl{
		Name: "onec.SplitURLCredentials",
		Split: func(raw string) (corpusResult, error) {
			r, err := SplitURLCredentials(raw)
			return corpusResult{
				Base: r.Base, User: r.User, Password: r.Password,
				HadUserinfo: r.HadUserinfo, Display: r.Display,
			}, err
		},
		Check: CheckURLCredentialResidue,
	}
}

// TestCorpusSecretsAreReal is the premise of every no-leak assertion below: a
// fragment the table calls a secret must really occur in that row's own input.
// Without it INVARIANT B could pass by testing nothing.
func TestCorpusSecretsAreReal(t *testing.T) {
	if f := corpusSecretsSanity(); len(f) > 0 {
		for _, x := range f {
			t.Errorf("%v", x)
		}
	}
	// The counter-check: a table that degenerated to nothing must fail here too.
	if n := len(corpusTable()); n != 47 {
		t.Fatalf("the corpus has %d rows, want 47: the table itself changed", n)
	}
}

func TestSplitURLCredentials_Corpus(t *testing.T) {
	findings := corpusRun(splitCorpusImpl())
	for _, f := range findings {
		t.Errorf("%v", f)
	}
	t.Logf("rows=%d findings=%d", len(corpusTable()), len(findings))
}

// TestGuardHasNoShortCircuit pins the structural property that made the original
// defect possible: CheckURLCredentialResidue must return exactly the error
// SplitURLCredentials returns, for every row, with no branch of its own.
//
// The comparison is on the error VALUE, not on its text. That is what makes the
// property structural: a guard that re-derived an equal-looking error would still
// fail here, and a guard that short-circuits on "a credential was found" fails on
// exactly the rows it exists for.
func TestGuardHasNoShortCircuit(t *testing.T) {
	for _, r := range corpusTable() {
		_, splitErr := SplitURLCredentials(r.In)
		guardErr := CheckURLCredentialResidue(r.In)
		if splitErr != guardErr {
			t.Errorf("[%s] in=%q: split err=%v but guard err=%v", r.ID, r.In, splitErr, guardErr)
		}
	}
}

// TestRefusalMessagesHaveNoDashes checks every character of every shipped refusal
// message. Intra-word hyphens (mcp-1c, HTTP-сервис) and CLI flag tokens (--user)
// are house style; an em dash, an en dash, a minus sign or a hyphen used as a
// clause separator is not.
//
// The check runs IN GO rather than in a shell: a shell dash check cannot fire
// reliably under zsh, which is how a previous one passed while the text carried
// the character it was looking for.
func TestRefusalMessagesHaveNoDashes(t *testing.T) {
	msgs := map[string]string{
		"ErrURLCredentialUnstrippable": ErrURLCredentialUnstrippable.Error(),
		"ErrBaseURLHasQueryOrFragment": ErrBaseURLHasQueryOrFragment.Error(),
		"ErrBaseURLUnparsable":         ErrBaseURLUnparsable.Error(),
	}
	for name, m := range msgs {
		if bad := dashViolations(m); len(bad) > 0 {
			t.Errorf("%s: %v", name, bad)
		}
	}

	// NEGATIVE CONTROLS. Without them the loop above could pass by doing
	// nothing. Each of these MUST be caught; a clean control is a failed test.
	controls := map[string]string{
		"em dash U+2014":                "адрес отклонён — уберите пароль",
		"en dash U+2013":                "адрес отклонён – уберите пароль",
		"minus sign U+2212":             "адрес отклонён − уберите пароль",
		"horizontal bar U+2015":         "адрес отклонён ― уберите пароль",
		"figure dash U+2012":            "адрес ‒ отклонён",
		"hyphen U+2010":                 "адрес от‐клонён",
		"non breaking hyphen U+2011":    "адрес от‑клонён",
		"ascii hyphen as a clause dash": "адрес отклонён - уберите пароль",
		"trailing hyphen":               "адрес отклонён, уберите пароль-",
		"leading hyphen":                "-адрес отклонён",
		"hyphen before a space":         "запрос- не выполнен",
	}
	for name, m := range controls {
		if bad := dashViolations(m); len(bad) == 0 {
			t.Errorf("NEGATIVE CONTROL FAILED: the checker accepted %q (%s)", m, name)
		}
	}

	// FALSE-POSITIVE CONTROLS. The allowed shapes must NOT be flagged, or the
	// checker would be passing by rejecting everything.
	for _, ok := range []string{
		"HTTP-сервис", "mcp-1c", "read-only", "Content-Type: text/html",
		"флагами --user и --password", "значение `--base`", "http://сервер/база/hs/mcp-1c",
		"(--cache-dir)", `"--dump"`, "веб-сервер", "BM25-ранжирование",
	} {
		if bad := dashViolations(ok); len(bad) > 0 {
			t.Errorf("FALSE POSITIVE: the checker rejected the allowed shape %q: %v", ok, bad)
		}
	}
}

// dashViolations returns every dash in s that house style forbids. Intra-word
// hyphens and the "--flag" token are allowed; every Unicode dash and every hyphen
// used as a clause separator is not.
//
// House style writes flags in backticks (see tools/index_notice.go), which is why
// a backtick is in the boundary set alongside a space.
func dashViolations(s string) []string {
	forbidden := map[rune]bool{'—': true, '–': true, '‒': true, '―': true, '−': true, '‐': true, '‑': true}
	var out []string
	rs := []rune(s)
	window := func(i int) string { return string(rs[max(0, i-12):min(len(rs), i+12)]) }
	isBoundary := func(r rune) bool {
		switch r {
		case ' ', '`', '(', '"', '\t', '\n':
			return true
		}
		return false
	}
	for i, r := range rs {
		if forbidden[r] {
			out = append(out, string(r)+" at "+itoa(i)+" in "+window(i))
			continue
		}
		if r != '-' {
			continue
		}
		prev, next := ' ', ' '
		if i > 0 {
			prev = rs[i-1]
		}
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		// allowed 1: intra-word hyphen, letter or digit on both sides
		if isWordRune(prev) && isWordRune(next) {
			continue
		}
		// allowed 2: the first hyphen of a "--flag" token
		if isBoundary(prev) && next == '-' && i+2 < len(rs) && isWordRune(rs[i+2]) {
			continue
		}
		// allowed 3: the second hyphen of a "--flag" token
		if prev == '-' && isWordRune(next) && i >= 2 && isBoundary(rs[i-2]) {
			continue
		}
		out = append(out, "bare hyphen at "+itoa(i)+" in "+window(i))
	}
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestScrubbedURLError pins what an error carrying a URL looks like after the
// scrub. It is needed even after the split at the boundary, because http.Client
// builds its OWN *url.Error from the request URL and masks only the password.
func TestScrubbedURLError(t *testing.T) {
	boom := errTestBoom{}
	cases := []struct {
		name    string
		in      error
		wantOut string
		absent  []string
	}{
		{
			name:    "credential is removed, the address survives",
			in:      &url.Error{Op: "Get", URL: "http://admin:sup3rsecret@host/hs/query", Err: boom},
			wantOut: `Get "http://host/hs/query": boom`,
			absent:  []string{"sup3rsecret", "admin"},
		},
		{
			name:    "an address that cannot be cleaned loses the address entirely",
			in:      &url.Error{Op: "Get", URL: "http://admin:p@ss/w0rd@host/hs/query", Err: boom},
			wantOut: `Get: boom`,
			absent:  []string{"w0rd", "admin", "host"},
		},
		{
			name:    "an address with no credential is left alone",
			in:      &url.Error{Op: "Get", URL: "http://host/hs/query", Err: boom},
			wantOut: `Get "http://host/hs/query": boom`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ScrubbedURLError(c.in).Error()
			if got != c.wantOut {
				t.Errorf("ScrubbedURLError = %q, want %q", got, c.wantOut)
			}
			for _, a := range c.absent {
				if strings.Contains(got, a) {
					t.Errorf("ScrubbedURLError %q still contains %q", got, a)
				}
			}
		})
	}

	// An error that carries no *url.Error at all comes back unchanged, so the
	// scrub cannot quietly swallow a diagnostic it has nothing to do with.
	if got := ScrubbedURLError(boom); got != error(boom) {
		t.Errorf("ScrubbedURLError changed a non-URL error: %v", got)
	}
}

type errTestBoom struct{}

func (errTestBoom) Error() string { return "boom" }

// TestDisplayBaseNeverRendersCredentials proves the property a model-visible
// address has to have. For http://user:1234/passZ@host/hs the PARSED host is
// literally "user:1234", so a display string derived from a kept base would
// render a credential at the model.
//
// The design closes it structurally, in two independent ways:
//  1. Display exists only for an ACCEPTED base, and that shape is REFUSED;
//  2. Display is assembled from Scheme and Host alone, and net/url never puts
//     userinfo in Host, so even an accepted base cannot carry one there.
func TestDisplayBaseNeverRendersCredentials(t *testing.T) {
	for _, r := range corpusTable() {
		res, err := SplitURLCredentials(r.In)
		if err != nil && res.Display != "" {
			t.Errorf("[%s] refused but Display = %q", r.ID, res.Display)
		}
		if strings.ContainsAny(res.Display, "?#") {
			t.Errorf("[%s] Display %q carries a query or a fragment", r.ID, res.Display)
		}
		if i := strings.Index(res.Display, "://"); i >= 0 && strings.Contains(res.Display[i+3:], "/") {
			t.Errorf("[%s] Display %q carries a path", r.ID, res.Display)
		}
		for _, s := range corpusPresentSecrets(r) {
			if strings.Contains(res.Display, s) {
				t.Errorf("[%s] Display %q contains the secret %q", r.ID, res.Display, s)
			}
		}
		if res.Display != "" && DisplayBase(r.In) != res.Display {
			t.Errorf("[%s] DisplayBase() disagrees with the split result", r.ID)
		}
	}

	// THE COUNTER-EXAMPLE, measured rather than asserted: what a display built as
	// "scheme://host of the kept base" would have produced for L2.
	const l2 = `http://user:1234/passZ@host/hs`
	u, err := url.Parse(l2)
	if err != nil {
		t.Fatalf("premise broken: %v", err)
	}
	if naive := u.Scheme + "://" + u.Host; naive != "http://user:1234" {
		t.Fatalf("premise broken: the naive display for %q is %q, not the credential-shaped "+
			"string this test exists to rule out", l2, naive)
	}
	if res, _ := SplitURLCredentials(l2); res.Display != "" {
		t.Errorf("the design produced a display %q for the refused shape %q", res.Display, l2)
	}
}
