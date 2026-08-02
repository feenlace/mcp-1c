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
	// Credentials AND a query string. The docs say following the message once is
	// not enough, because this refusal is the credential one and the address is
	// refused again, by a different sentinel, after the credentials come out.
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
	// Reading a file that is not there must fail loudly rather than compare "".
	if _, err := os.ReadFile("../../docs/there-is-no-such-file.md"); err == nil {
		t.Error("reading a missing documentation file succeeded, so the guard could compare " +
			"against an empty string and pass")
	}
}
