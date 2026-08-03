package onec

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// urlcred_message_truth_test.go holds the refusal messages to the code that
// produces them.
//
// A refusal message is prose about code, and prose about code goes stale in a
// direction no other test can see: every assertion elsewhere in this package is
// about WHICH address is refused, and all of them stay green while the sentence
// printed to the administrator describes a different program. Three of them did.
//
//  1. ErrBaseURLAmbiguousAt told the reader the colon sits in the PATH. For
//     http://localhost:8080/base/hs/mcp-1c@v2 the colon that fired the refusal is
//     the PORT colon and the path holds no colon at all, so the reader inspected
//     a part of the address that was never the cause.
//  2. ErrURLCredentialUnstrippable told the reader to move the credentials into
//     --user and --password. For an address that also carries a "?" that is half
//     a remedy: the result is refused again, by ErrBaseURLHasQueryOrFragment,
//     with no warning that a second wall was coming.
//  3. ErrBaseURLUnparsable told the reader to check the address has no spaces.
//     An address with a space and none of @ ? # never reaches url.Parse at all
//     (step 1 returns it byte for byte), so a space alone cannot produce this
//     message. The named cause could not be the cause.
//
// Each test below measures the behaviour first and derives what the message must
// say from that measurement, so it fails when either side moves.

// ambiguousAtArms reports, for one address, which half of the step 4 condition
// fired: an explicit port, a colon inside the path before the '@', or both.
//
// It re-derives them from net/url rather than reading them out of the refusal, so
// the message is judged against the address and not against itself.
func ambiguousAtArms(t *testing.T, raw string) (port bool, pathColon bool) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("premise broken: %q does not parse (%v), so it cannot reach the ambiguity check", raw, err)
	}
	rp := u.RawPath
	if rp == "" {
		rp = u.EscapedPath()
	}
	at := strings.IndexRune(rp, '@')
	if at < 0 {
		t.Fatalf("premise broken: %q has no '@' in its path, so it cannot reach the ambiguity check", raw)
	}
	return u.Port() != "", strings.ContainsRune(rp[:at], ':')
}

// TestAmbiguousAtNamesTheColonThatFired is defect 1.
//
// The refusal fires on "port is set OR a colon sits in the path before the '@'".
// Both halves occur among real addresses, and for the half the documentation
// leads with there is no colon in the path whatsoever. A message that names only
// one of the two places sends the reader of the other one hunting for a character
// that is not there.
func TestAmbiguousAtNamesTheColonThatFired(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"explicit port, service name carries the @", `http://localhost:8080/base/hs/mcp-1c@v2`},
		{"explicit port, corpus row at-in-path-explicit-port", `http://1c.corp.local:8080/hs/mcp-1c@v2`},
		{"colon inside the path, corpus row L2-slashinuser", `http://us/er:passZZ@host/hs`},
		{"colon swallowed into the host as a port, corpus row L2-hostport", `http://user:1234/passZ@host/hs`},
	}

	msg := ErrBaseURLAmbiguousAt.Error()
	sawPortOnly, sawPathColon := false, false

	for _, c := range cases {
		err := CheckURLCredentialResidue(c.addr)
		if !errors.Is(err, ErrBaseURLAmbiguousAt) {
			t.Errorf("[%s] PREMISE BROKEN: %q is refused with %v, not ErrBaseURLAmbiguousAt, so this row "+
				"says nothing about that message", c.name, c.addr, err)
			continue
		}
		port, pathColon := ambiguousAtArms(t, c.addr)
		if !port && !pathColon {
			t.Errorf("[%s] PREMISE BROKEN: neither half of the condition holds for %q", c.name, c.addr)
			continue
		}
		if port && !pathColon {
			sawPortOnly = true
			t.Logf("[%s] %q: the colon that fired is the PORT colon; the path holds none", c.name, c.addr)
		}
		if pathColon {
			sawPathColon = true
		}
	}

	// Without both halves exercised the requirement below would be satisfiable by
	// naming whichever single place happened to be left.
	if !sawPortOnly {
		t.Fatal("PREMISE BROKEN: no address reached ErrBaseURLAmbiguousAt through the port alone, so " +
			"this test cannot tell a message that names only the path from one that names both")
	}
	if !sawPathColon {
		t.Fatal("PREMISE BROKEN: no address reached ErrBaseURLAmbiguousAt through a colon in the path")
	}

	// The message must account for BOTH places the colon can sit. Naming only the
	// path is the shipped defect; naming only the port would be the same defect
	// pointed the other way.
	if !strings.Contains(msg, "порт") {
		t.Errorf("an address is refused here because of its PORT colon while its path holds no colon "+
			"at all, yet the message never mentions the port: %q", msg)
	}
	if !strings.Contains(msg, "пут") {
		t.Errorf("an address is refused here because of a colon inside its PATH, yet the message never "+
			"mentions the path: %q", msg)
	}

	// The measurement the first requirement rests on, asserted rather than
	// assumed: for the address the documentation leads with, the path really is
	// colon free, and dropping the port really does make it acceptable.
	const docLead = `http://localhost:8080/base/hs/mcp-1c@v2`
	if _, pathColon := ambiguousAtArms(t, docLead); pathColon {
		t.Errorf("premise broken: %q now has a colon in its path, so the port is no longer the only "+
			"thing the message has to explain", docLead)
	}
	if err := CheckURLCredentialResidue(`http://localhost/base/hs/mcp-1c@v2`); err != nil {
		t.Errorf("premise broken: the same address without the port is refused (%v), so the port is not "+
			"what distinguishes them", err)
	}
}

// unstrippableRemedyRows pairs an address refused by ErrURLCredentialUnstrippable
// with the address a reader ends up holding after doing what its FIRST remedy
// says: taking the login and the password out and passing them as flags.
var unstrippableRemedyRows = []struct {
	name       string
	refused    string
	afterFlags string
}{
	{
		name:       "credentials only",
		refused:    `http://Admin:pass/word@localhost:8080/hs/mcp-1c`,
		afterFlags: `http://localhost:8080/hs/mcp-1c`,
	},
	{
		name:       "credentials and a query string",
		refused:    `http://Admin:secret@localhost:8080/hs/mcp-1c?debug=1`,
		afterFlags: `http://localhost:8080/hs/mcp-1c?debug=1`,
	},
	{
		name:       "credentials and a fragment",
		refused:    `http://admin:p@ss#w0rd@1c.corp.local/hs`,
		afterFlags: `http://1c.corp.local/hs`,
	},
}

// TestUnstrippableAdviceDoesNotEndInASecondRefusal is defect 2.
//
// The message leads with "take the login and the password out of the address and
// pass them as --user and --password". For an address that also carries a "?" or
// a "#", doing exactly that produces an address which is refused AGAIN, by a
// different sentinel. The first message is the only one that reader ever sees
// before acting, so it is the one that has to say so.
//
// The requirement is derived, not assumed: the second refusal is measured, and
// only when it really happens is the message required to warn about it.
func TestUnstrippableAdviceDoesNotEndInASecondRefusal(t *testing.T) {
	msg := ErrURLCredentialUnstrippable.Error()
	secondWall := false

	for _, r := range unstrippableRemedyRows {
		if err := CheckURLCredentialResidue(r.refused); !errors.Is(err, ErrURLCredentialUnstrippable) {
			t.Errorf("[%s] PREMISE BROKEN: %q is refused with %v, not ErrURLCredentialUnstrippable",
				r.name, r.refused, err)
			continue
		}
		err := CheckURLCredentialResidue(r.afterFlags)
		if err == nil {
			t.Logf("[%s] following the message once is enough: %q is accepted", r.name, r.afterFlags)
			continue
		}
		if errors.Is(err, ErrURLCredentialUnstrippable) {
			t.Errorf("[%s] the remedy left the credentials in place; %q is a bad model of it",
				r.name, r.afterFlags)
			continue
		}
		secondWall = true
		t.Logf("[%s] following the message once leaves %q, still refused by a different sentinel: %v",
			r.name, r.afterFlags, err)
	}

	if !secondWall {
		t.Fatal("PREMISE BROKEN: no row reaches a second refusal, so this test cannot tell a message " +
			"that warns about one from a message that does not")
	}

	// The warning has to live OUTSIDE the percent-encoding list. The shipped
	// message named "?" and "#" only as characters that can be written %3F and
	// %23 INSIDE a password, which is not a warning that the address itself may
	// not carry them, and a plain substring check would have been satisfied by it.
	const encodingMarker = "закодируйте"
	cut := strings.Index(msg, encodingMarker)
	if cut < 0 {
		t.Fatalf("PREMISE BROKEN: the message no longer contains %q, so this test cannot separate the "+
			"warning from the encoding list: %q", encodingMarker, msg)
	}
	head := msg[:cut]
	for _, ch := range []string{"?", "#"} {
		if !strings.Contains(head, ch) {
			t.Errorf("following this message once leaves an address that is refused again for its %s, "+
				"and the message names %s nowhere except inside the percent-encoding list, which is "+
				"advice about the PASSWORD rather than about the address: %q", ch, ch, msg)
		}
	}

	// POSITIVE CONTROL for the split above: a message shaped like the shipped one,
	// naming the characters only in the encoding list, must be caught. Without it
	// the loop could pass because "head" happened to contain everything.
	const shipped = "адрес отклонён. Уберите логин и пароль. Если они обязаны остаться, " +
		"закодируйте служебные символы: @ как %40, / как %2F, ? как %3F, # как %23"
	shippedHead := shipped[:strings.Index(shipped, encodingMarker)]
	if strings.Contains(shippedHead, "?") || strings.Contains(shippedHead, "#") {
		t.Error("POSITIVE CONTROL FAILED: the split accepts the shipped shape, so it cannot tell an " +
			"encoding list from a warning")
	}

	// The encoding remedy must survive the rewrite, and it must still WORK: each
	// escape the message offers has to produce an accepted address.
	for _, ok := range []string{
		`http://Admin:pass%40word@localhost:8080/hs/mcp-1c`,
		`http://Admin:pass%2Fword@localhost:8080/hs/mcp-1c`,
		`http://Admin:pass%3Fword@localhost:8080/hs/mcp-1c`,
		`http://Admin:pass%23word@localhost:8080/hs/mcp-1c`,
	} {
		if err := CheckURLCredentialResidue(ok); err != nil {
			t.Errorf("the message offers percent-encoding inside the credentials, but %q is refused: %v", ok, err)
		}
	}
}

// TestUnparsableNamesACauseThatCanProduceIt is defect 3.
//
// The message advised checking the address for spaces. Step 1 of
// SplitURLCredentials returns any address without '@', '?' or '#' byte for byte
// and never parses it, so an address whose only problem is a space is ACCEPTED.
// The advice named a cause that cannot produce the message it was printed with.
//
// WHY NOTHING HERE PINS A url.Parse VERDICT. Which of two sentinels refuses a
// malformed address turns on a single bit: whether url.Parse rejects it. That
// bit is net/url's own judgement and it MOVES. Measured on one input,
// http://[::1x]/hs?x=1: go1.25.0 PARSES it, so step 5 refuses it with
// ErrBaseURLHasQueryOrFragment; go1.26.0 REJECTS it, so the branch after step 1
// refuses it with ErrBaseURLUnparsable, because go1.26 validates a bracketed
// IPv6 literal through netip.ParseAddr. The product is right under both and both
// messages are true of that address, yet a test naming the sentinel was really
// asserting the Go version, and it failed CI (which pins the version in go.mod,
// 1.25.0) while passing on a developer machine with a newer toolchain.
//
// So every row below has its sentinel DERIVED from url.Parse as the test runs,
// the same way ambiguousAtArms re-derives the ambiguity arms instead of reading
// them out of the refusal. What is asserted is what survives the drift: the
// address is refused, and the message it is refused with names a cause the
// address really has.
func TestUnparsableNamesACauseThatCanProduceIt(t *testing.T) {
	msg := ErrBaseURLUnparsable.Error()

	// MEASUREMENT 1: a space alone does not produce this refusal. Step 1 hands
	// these back byte for byte, so they are accepted even when net/url would
	// refuse them. Not all of them are refused by net/url, and that is the point
	// of measuring instead of declaring: http://localhost:8080/hs/mcp -1c parses
	// cleanly on both toolchains above, so a claim that all three fail url.Parse
	// would be false prose sitting on top of a passing test.
	unparsedYetAccepted := false
	for _, spaceOnly := range []string{
		`http://localhost:80 80/hs/mcp-1c`,
		`http:// localhost:8080/hs/mcp-1c`,
		`http://localhost:8080/hs/mcp -1c`,
	} {
		if err := CheckURLCredentialResidue(spaceOnly); err != nil {
			t.Fatalf("premise broken: %q is now refused (%v); the false cause this test exists for "+
				"would have become a real one", spaceOnly, err)
		}
		if _, err := url.Parse(spaceOnly); err != nil {
			unparsedYetAccepted = true
			t.Logf("%q is refused by net/url (%v) and accepted anyway: step 1 never parsed it",
				spaceOnly, err)
		}
	}
	// WHICH of the rows above net/url refuses is net/url's business; that at
	// least one of them is refused and accepted anyway is the property. Naming
	// the row would pin the very verdict this test exists to stop pinning.
	if !unparsedYetAccepted {
		t.Fatal("premise broken: net/url now parses every address above, so 'accepted without being " +
			"parsed' is no longer what is being measured; this test needs an address it still refuses")
	}

	// MEASUREMENT 2: every address here is malformed, carries a '?' or a '#' and
	// carries no '@'. Each one is refused, and the refusal it gets is the one the
	// code structurally owes it:
	//
	//   url.Parse REFUSES it -> the branch after step 1 sees no '@' and returns
	//                           ErrBaseURLUnparsable;
	//   url.Parse ACCEPTS it -> nothing between there and step 5 can fire (no '@'
	//                           means no userinfo and no '@' in the path), so
	//                           step 5 returns ErrBaseURLHasQueryOrFragment.
	//
	// Both are derivations from the two properties asserted per row, not guesses,
	// and BOTH messages are then held to the same standard of truth: the first
	// may name '?' and '#' because every row carries one, and the second says the
	// address contains a '?' or a '#', which is likewise true of every row.
	reaching := []string{
		`http://localhost:80 80/hs/mcp-1c?x=1`,
		`http://localhost:80 80/hs/mcp-1c#x`,
		`http://1c.corp.local:8o80/hs?x=1`,
		`http://[::1x]/hs?x=1`,
		`http://%zz.example/hs?x=1`,
		`http://localhost:8080/hs/%zz?x=1`,
		"http://local\x7fhost/hs?x=1",
	}
	sawUnparsable := false
	for _, in := range reaching {
		// The two properties every derivation below rests on, checked rather than
		// assumed: a row that lost its '?' or grew an '@' would be evidence about
		// some other sentinel entirely.
		if !strings.ContainsAny(in, "?#") {
			t.Errorf("%q carries neither '?' nor '#', so it says nothing about a message that names "+
				"them as the cause", in)
			continue
		}
		if strings.ContainsRune(in, '@') {
			t.Errorf("%q carries an '@', so the credential branch takes it and it cannot reach this "+
				"refusal at all", in)
			continue
		}

		err := CheckURLCredentialResidue(in)
		if err == nil {
			t.Errorf("%q is malformed and carries a '?' or a '#', yet it is accepted", in)
			continue
		}

		if _, parseErr := url.Parse(in); parseErr != nil {
			if !errors.Is(err, ErrBaseURLUnparsable) {
				t.Errorf("net/url refuses %q (%v) and it carries no '@', so ErrBaseURLUnparsable is the "+
					"only refusal that can own it, yet it is refused with %v", in, parseErr, err)
				continue
			}
			sawUnparsable = true
			continue
		}
		if !errors.Is(err, ErrBaseURLHasQueryOrFragment) {
			t.Errorf("net/url parses %q, so step 5 owns it and ErrBaseURLHasQueryOrFragment is the only "+
				"refusal that can fire, yet it is refused with %v", in, err)
			continue
		}
		t.Logf("this toolchain parses %q, so it is refused for its '?' or '#' rather than for being "+
			"unparsable; that message is true of it too", in)
	}

	// Without one row reaching it, every requirement below would describe a
	// message this test never saw produced, and would pass for that reason.
	if !sawUnparsable {
		t.Fatal("PREMISE BROKEN: no address reached ErrBaseURLUnparsable, so the requirements below are " +
			"about a refusal nothing here produces; the catalogue needs an address net/url still refuses")
	}

	// THE VERDICT. The message may not name spaces, because a space alone cannot
	// produce it, and it must name what every address reaching it really has.
	if strings.Contains(msg, "пробел") {
		t.Errorf("the message tells the reader to check for spaces, but an address whose only problem "+
			"is a space is accepted without ever being parsed, so a space alone cannot produce this "+
			"message: %q", msg)
	}
	for _, ch := range []string{"?", "#"} {
		if !strings.Contains(msg, ch) {
			t.Errorf("every address that reaches this refusal carries a %s, and the message never "+
				"names it: %q", ch, msg)
		}
	}

	// Credential advice would be false here: this sentinel is unreachable for an
	// address containing '@', because the branch above it catches those.
	if strings.Contains(msg, "--user") || strings.Contains(msg, "--password") {
		t.Errorf("the message offers the credential flags, but no address containing an '@' can reach "+
			"this refusal, so there is never a credential in the address to move: %q", msg)
	}
}
