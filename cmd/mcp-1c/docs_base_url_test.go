package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// ---------------------------------------------------------------------------
// README.md and docs/ describe what --base accepts, what it refuses and which
// extension version this build needs. That is prose ABOUT code, and prose about
// code goes stale silently: nothing fails when SplitURLCredentials starts
// accepting an address the documentation still calls refused, or when
// expectedExtensionVersion moves and three files keep quoting the old number.
//
// So the documentation is not trusted to describe itself here. Every address the
// docs present in the "какие адреса отклоняются" table is put through the SAME
// function the binary calls, and the verdict the docs claim is compared with the
// verdict the code gives. Every version the docs quote is compared with
// expectedExtensionVersion.
//
// The precedent is extension/docs_sync_test.go, which guards docs/bsl against
// the shipped module for the same reason and in the same shape, counts included:
// a walk that finds nothing satisfies every "if they differ" check in it.
//
// WHAT THIS DOES NOT DO. It does not read the tables out of the markdown. A
// parser for prose would itself be a thing that can quietly match nothing, and
// the table wording is Russian sentences, not data. The addresses are listed
// here and the test asserts each one is PRESENT in the file that documents it,
// so deleting a row from the docs fails just as loudly as changing the code
// under it.
// ---------------------------------------------------------------------------

const (
	docReadme         = "../../README.md"
	docGettingStarted = "../../docs/getting-started.md"
	doc1CSetup        = "../../docs/1c-setup.md"
)

// unparsableQuote is the fragment of ErrBaseURLUnparsable that getting-started.md
// quotes verbatim. It is spelled out ONCE and compared against both sides, so the
// documentation cannot quote one sentence while the binary prints another.
const unparsableQuote = "адрес 1С отклонён: в нём есть ? или #, и разобрать его как адрес не получается"

// baseURLDocClaim is one address the documentation shows, and the verdict it
// tells the reader to expect.
type baseURLDocClaim struct {
	addr string
	// refusedBy is nil when the docs present the address as accepted.
	refusedBy error
	// in is the documentation file the address appears in.
	in string
}

// baseURLDocClaims mirrors the table under "Какие адреса отклоняются при
// запуске" in docs/getting-started.md, plus the two addresses README.md and the
// authentication section show as working.
var baseURLDocClaims = []baseURLDocClaim{
	{addr: `http://localhost:8080/hs/mcp-1c`, in: docReadme},
	{addr: `http://Admin:p@ssw0rd@localhost:8080/hs/mcp-1c`, in: docGettingStarted},
	{addr: `http://localhost/base/hs/mcp-1c@v2`, in: docGettingStarted},
	{addr: `http://localhost:8080/base/hs/mcp-1c@v2`, refusedBy: onec.ErrBaseURLAmbiguousAt, in: docGettingStarted},
	{addr: `http://localhost:8080/hs/mcp-1c?debug=1`, refusedBy: onec.ErrBaseURLHasQueryOrFragment, in: docGettingStarted},
	{addr: `http://Admin:pass/word@localhost:8080/hs/mcp-1c`, refusedBy: onec.ErrURLCredentialUnstrippable, in: docGettingStarted},
	{addr: `http://Админ:пароль@localhost:8080/hs/mcp-1c`, refusedBy: onec.ErrURLCredentialUnstrippable, in: docGettingStarted},
	// The remedy the shipped message names, shown in the docs as the way through.
	{addr: `http://Admin:pass%2Fword@localhost:8080/hs/mcp-1c`, in: docGettingStarted},
	// The address the recommendation shows as working. It is ASCII on purpose:
	// the first draft of that sentence used a Russian password and this guard is
	// what caught it, because a Russian password inside the address is REFUSED.
	{addr: `http://Admin:secret@localhost:8080/hs/mcp-1c`, in: docGettingStarted},
	{addr: `http://Admin:secret@сервер/база/hs/mcp-1c`, in: docReadme},
	// Russian letters in the userinfo are refused whichever half they sit in.
	{addr: `http://Админ:пароль@localhost:8080/hs/mcp-1c`, refusedBy: onec.ErrURLCredentialUnstrippable, in: docGettingStarted},
	// Credentials AND a query string. Two problems, one refusal, and it is the
	// credential one. The docs say both are named in that single message, and the
	// wording checks further down are what hold the message to it.
	{addr: `http://Admin:secret@localhost:8080/hs/mcp-1c?debug=1`, refusedBy: onec.ErrURLCredentialUnstrippable, in: docGettingStarted},
	// The fourth sentinel. Reaching it takes an address that BOTH carries a "?"
	// or "#" and fails url.Parse, because an address with none of @ ? # is
	// returned byte for byte without ever being parsed.
	{addr: `http://localhost:80 80/hs/mcp-1c?x=1`, refusedBy: onec.ErrBaseURLUnparsable, in: docGettingStarted},
	// A typo with no @ ? # is NOT refused: it is passed through as typed, which
	// is why the docs tell the reader a bad address surfaces as a connection
	// failure rather than as a startup refusal.
	{addr: `http://localhost:8080/hs/mcp -1c`, in: docGettingStarted},
}

// TestDocsMatchBaseURLBehaviour fails when the documentation describes an
// address differently from what the binary does with it.
func TestDocsMatchBaseURLBehaviour(t *testing.T) {
	sources := map[string]string{}
	for _, path := range []string{docReadme, docGettingStarted, doc1CSetup} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		sources[path] = string(raw)
	}

	for _, c := range baseURLDocClaims {
		_, err := onec.SplitURLCredentials(c.addr)
		switch {
		case c.refusedBy == nil && err != nil:
			t.Errorf("%s presents %s as a working address, but SplitURLCredentials refuses it: %v",
				c.in, c.addr, err)
		case c.refusedBy != nil && err == nil:
			t.Errorf("%s tells the reader %s is refused, but SplitURLCredentials accepts it",
				c.in, c.addr)
		case c.refusedBy != nil && !errors.Is(err, c.refusedBy):
			t.Errorf("%s describes the refusal of %s as %v, but the code refuses it with %v; "+
				"the remedy printed to the user is the other message's",
				c.in, c.addr, c.refusedBy, err)
		}
		if !strings.Contains(sources[c.in], c.addr) {
			t.Errorf("%s no longer shows %s, so this guard is holding the code to an example "+
				"the reader cannot see", c.in, c.addr)
		}
	}

	// The docs quote the percent-encoding remedy. It must be the remedy the
	// shipped message actually gives, not one the documentation invented.
	for _, remedy := range []string{"%40", "%2F", "%3F", "%23"} {
		if !strings.Contains(onec.ErrURLCredentialUnstrippable.Error(), remedy) {
			t.Errorf("the docs tell the reader to encode with %s, but "+
				"ErrURLCredentialUnstrippable no longer names it: %q",
				remedy, onec.ErrURLCredentialUnstrippable.Error())
		}
		if !strings.Contains(sources[docGettingStarted], remedy) {
			t.Errorf("%s no longer names the %s encoding the error message gives",
				docGettingStarted, remedy)
		}
	}
	if !strings.Contains(onec.ErrBaseURLAmbiguousAt.Error(), "%40") {
		t.Errorf("the docs give %%40 as the way out of an ambiguous @, but "+
			"ErrBaseURLAmbiguousAt no longer names it: %q", onec.ErrBaseURLAmbiguousAt.Error())
	}

	// -----------------------------------------------------------------------
	// Three paragraphs of getting-started.md are about the WORDING of a refusal
	// rather than about which address is refused. Each rests on a property of ONE
	// named message, and if that message loses the property the paragraph becomes
	// a description of a program that is no longer shipped. Pinned per sentinel
	// and by name, for the same reason the coverage loop at the bottom is: a
	// check that lumped them together would let one message go unwatched behind
	// another one's pass.
	// -----------------------------------------------------------------------

	// "Про двоеточие перед @". The docs say the message names TWO places the
	// colon can sit. It has to name both, because both really occur: for
	// http://localhost:8080/base/hs/mcp-1c@v2 the colon that fires is the port
	// colon and the path holds none, while http://us/er:passZZ@host/hs has no
	// port and a colon in the path. That is measured in
	// urlcred_message_truth_test.go:TestAmbiguousAtNamesTheColonThatFired; this
	// holds the paragraph to the same message.
	ambiguous := onec.ErrBaseURLAmbiguousAt.Error()
	for _, must := range []string{"порт", "пут"} {
		if !strings.Contains(ambiguous, must) {
			t.Errorf("%s explains that the ambiguity message names both the port colon and the path "+
				"colon, but the message no longer contains %q: %q", docGettingStarted, must, ambiguous)
		}
	}
	if !strings.Contains(sources[docGettingStarted], "перед номером порта или в самом пути") {
		t.Errorf("%s no longer tells the reader where the colon the message names actually sits",
			docGettingStarted)
	}

	// "Если в адресе есть и учётные данные, и ?". The docs now tell the reader
	// that one pass is enough BECAUSE both problems are named in one message.
	// That is only true while the warning sits OUTSIDE the percent-encoding list:
	// "? как %3F" is advice about the password, not about the address, and a bare
	// substring check for "?" would have been satisfied by it.
	unstrippable := onec.ErrURLCredentialUnstrippable.Error()
	const encodingMarker = "закодируйте"
	if cut := strings.Index(unstrippable, encodingMarker); cut < 0 {
		t.Errorf("ErrURLCredentialUnstrippable no longer offers percent-encoding, so %s promises a "+
			"remedy the binary does not give: %q", docGettingStarted, unstrippable)
	} else {
		for _, ch := range []string{"?", "#"} {
			if !strings.Contains(unstrippable[:cut], ch) {
				t.Errorf("%s tells the reader both problems are named in one message, but "+
					"ErrURLCredentialUnstrippable names %s nowhere except inside the percent-encoding "+
					"list, which is advice about the password: %q", docGettingStarted, ch, unstrippable)
			}
		}
	}

	// "Опечатка в адресе при запуске не ловится". The docs quote the fourth
	// message verbatim and then state that it says nothing about spaces, because
	// an address whose only problem is a space never reaches url.Parse. Both
	// halves are pinned: the quote against the shipped text, and the claim
	// against the shipped text.
	unparsable := onec.ErrBaseURLUnparsable.Error()
	if !strings.Contains(unparsable, unparsableQuote) {
		t.Errorf("%s quotes %q as the fourth refusal, but the binary prints %q",
			docGettingStarted, unparsableQuote, unparsable)
	}
	if !strings.Contains(sources[docGettingStarted], unparsableQuote) {
		t.Errorf("%s no longer quotes the fourth refusal message verbatim", docGettingStarted)
	}
	if strings.Contains(unparsable, "пробел") {
		t.Errorf("%s states that the fourth message says nothing about spaces, but it does: %q",
			docGettingStarted, unparsable)
	}

	// Every file that states the required extension version must state THIS one.
	for path, src := range sources {
		if !strings.Contains(src, expectedExtensionVersion) {
			continue
		}
		for _, stale := range []string{"0.4.3", "0.4.4", "0.4.5", "0.4.6", "0.4.8", "0.4.9"} {
			if stale == expectedExtensionVersion {
				continue
			}
			if strings.Contains(src, `"version":"`+stale+`"`) {
				t.Errorf("%s quotes /version answering %s while this build requires %s",
					path, stale, expectedExtensionVersion)
			}
		}
	}
	// getting-started and 1c-setup both walk the reader through /version, so both
	// must carry the number. A silent disappearance is the drift this catches.
	for _, path := range []string{docGettingStarted, doc1CSetup, docReadme} {
		if !strings.Contains(sources[path], expectedExtensionVersion) {
			t.Errorf("%s does not mention the required extension version %s",
				path, expectedExtensionVersion)
		}
	}

	// The version floor is a LOG LINE, not a refusal to start, and the docs say
	// so by quoting the line. Quoting a line the binary does not emit would be
	// the same defect one level down.
	const olderLine = "Extension is OLDER than this build requires"
	for _, path := range []string{docGettingStarted, doc1CSetup, docReadme} {
		if !strings.Contains(sources[path], olderLine) {
			t.Errorf("%s no longer quotes %q, the line an operator searches for", path, olderLine)
		}
	}

	// Counts, because every check above is an "if they differ" check and an empty
	// claim list satisfies all of them. The floor is stated as a floor and does
	// not publish a figure about the table: the table's row count is not what
	// this list holds, and a message that named one would go stale on its own.
	if len(baseURLDocClaims) < 10 {
		t.Errorf("only %d documented addresses are guarded; the list has shrunk, and a walk "+
			"over nothing satisfies every check above", len(baseURLDocClaims))
	}

	// EVERY sentinel the shipped code can put in front of a user must be reached
	// by some documented address. Counting refusal CLAIMS instead would let all
	// of them land on one message while another shipped refusal stayed
	// undocumented, which is exactly what this list looked like at first: three
	// sentinels covered, ErrBaseURLUnparsable covered by nothing.
	sentinels := []error{
		onec.ErrURLCredentialUnstrippable,
		onec.ErrBaseURLAmbiguousAt,
		onec.ErrBaseURLHasQueryOrFragment,
		onec.ErrBaseURLUnparsable,
	}
	for _, want := range sentinels {
		covered := false
		for _, c := range baseURLDocClaims {
			if c.refusedBy != nil && errors.Is(c.refusedBy, want) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("no documented address reaches %v, so the documentation never tells a "+
				"reader what to do about it", want)
		}
	}
	refused := 0
	for _, c := range baseURLDocClaims {
		if c.refusedBy != nil {
			refused++
		}
	}
	t.Logf("checked %d documented addresses (%d refusals) covering %d sentinels",
		len(baseURLDocClaims), refused, len(sentinels))
}

// TestDocsBaseURLGuardCanFail is the positive control. Every assertion above is
// of the form "if these disagree", and such a test passes when the comparison it
// rests on cannot distinguish anything. These are the same comparisons, run
// against inputs where they MUST report a difference.
func TestDocsBaseURLGuardCanFail(t *testing.T) {
	// An address the docs call accepted that the code refuses.
	if _, err := onec.SplitURLCredentials(`http://localhost:8080/hs/mcp-1c?x=1`); err == nil {
		t.Error("SplitURLCredentials accepted an address with a query string, so the refusal " +
			"claims in the guard above cannot fail")
	}
	// An address the docs call refused that the code accepts.
	if _, err := onec.SplitURLCredentials(`http://localhost/base/hs/mcp-1c@v2`); err != nil {
		t.Errorf("SplitURLCredentials refused an @ in the path without a port (%v), so the "+
			"acceptance claims in the guard above cannot fail", err)
	}
	// errors.Is must actually discriminate between the sentinels, otherwise the
	// "wrong message" branch is unreachable.
	_, err := onec.SplitURLCredentials(`http://localhost:8080/hs/mcp-1c?x=1`)
	if errors.Is(err, onec.ErrBaseURLAmbiguousAt) {
		t.Error("a query-string refusal matches ErrBaseURLAmbiguousAt, so the guard cannot " +
			"tell one sentinel from another")
	}
	if !errors.Is(err, onec.ErrBaseURLHasQueryOrFragment) {
		t.Errorf("a query-string refusal is %v, not ErrBaseURLHasQueryOrFragment", err)
	}
	// The substring check must be able to miss.
	if strings.Contains(onec.ErrBaseURLUnparsable.Error(), "%40") {
		t.Error("ErrBaseURLUnparsable names %40, so the substring checks above prove nothing " +
			"about which message carries the remedy")
	}

	// The "outside the percent-encoding list" split must be able to fail. This is
	// the message as it shipped before the rewrite: it names "?" and "#" ONLY as
	// characters that can be written %3F and %23 inside a password. A guard that
	// accepted this shape would not have caught the defect it exists for.
	const shippedBeforeRewrite = "адрес 1С содержит логин и пароль в форме, которую невозможно " +
		"разобрать однозначно, поэтому он отклонён. Уберите логин и пароль из адреса и задайте их " +
		"флагами --user и --password. Если они обязаны остаться в адресе, закодируйте служебные " +
		"символы: @ как %40, / как %2F, ? как %3F, # как %23"
	cut := strings.Index(shippedBeforeRewrite, "закодируйте")
	if cut < 0 {
		t.Error("the control message does not contain the encoding marker, so the split above is not " +
			"being exercised")
	} else if strings.Contains(shippedBeforeRewrite[:cut], "?") ||
		strings.Contains(shippedBeforeRewrite[:cut], "#") {
		t.Error("the split accepts the pre-rewrite message, whose only mention of ? and # is inside " +
			"the encoding list, so it cannot tell a warning from an encoding offer")
	}

	// The "says nothing about spaces" check must be able to fire.
	if !strings.Contains("Проверьте, что адрес записан целиком и без пробелов", "пробел") {
		t.Error("the space check does not match the wording it exists to forbid, so it would pass " +
			"against a message that blames spaces")
	}

	// The verbatim quote check must be able to miss: a fragment that is NOT in the
	// shipped message has to be reported as absent.
	if strings.Contains(onec.ErrBaseURLUnparsable.Error(), unparsableQuote+" никогда") {
		t.Error("the quote comparison matches a fragment the message does not contain, so quoting a " +
			"sentence the binary never prints would pass")
	}
	// Reading a file that is not there must fail loudly rather than compare "".
	if _, err := os.ReadFile("../../docs/there-is-no-such-file.md"); err == nil {
		t.Error("reading a missing documentation file succeeded, so the guard could compare " +
			"against an empty string and pass")
	}
}
