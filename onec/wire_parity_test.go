package onec

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// wire_parity_test.go is the gate that says the credential split changed WHAT IS
// STORED and not WHAT IS SENT.
//
// The contract: for every address the design accepts, the bytes that reach an
// HTTP server must be IDENTICAL to what v1.12.1 (6d96384) put there, Authorization
// and RequestURI compared byte for byte. Divergence is allowed only where the
// table declares it, and the SET of divergent rows is itself asserted from what
// the two sides OBSERVABLY did, so an unintended change to it fails.
//
// WHY THE SET AND THE COUNT ARE COMPUTED FROM OBSERVATION. An earlier draft of
// this gate derived both from the table. That made two of its six assertions
// restatements of the table and unable to fail: a mutation that reached the server
// on a row the table calls dead still reported the table's own divergence set.
// They are now computed from today.saw and design.saw.

// wireExpectedDivergentIDs is the complete, closed set of rows where the design is
// allowed to behave differently from v1.12.1. Adding a row to it is a decision;
// arriving at it by accident is a failure.
var wireExpectedDivergentIDs = []string{"L3-query-token"}

// wireRow is one address plus the MEASURED behaviour of both sides.
type wireRow struct {
	ID       string
	Base     string // %H% and %V6% are replaced with live loopback authorities
	FlagUser string
	FlagPass string
	Note     string

	// TodayReaches records whether v1.12.1's request reaches the server. It is
	// ASSERTED, not assumed: if that premise is gone, the row says so.
	TodayReaches bool
	// DesignReaches is the same statement about the implementation under test.
	DesignReaches bool

	// Secrets are fragments of Base that must not appear in the design's refusal
	// text. Each is checked only if it really occurs in Base.
	Secrets []string
	// TodayLeaks names a secret v1.12.1's own error text DOES carry. It is the
	// positive control for the no-leak assertion: without it a green no-leak
	// check could mean "the checker is looking at the wrong string".
	TodayLeaks string
}

func wireTable(host, v6host string) []wireRow {
	rows := []wireRow{
		{ID: "no-userinfo", Base: "http://%H%/hs", TodayReaches: true, DesignReaches: true},
		{ID: "flags-only", Base: "http://%H%/hs", FlagUser: "flaguser", FlagPass: "flagpass",
			TodayReaches: true, DesignReaches: true},
		{ID: "userinfo-only", Base: "http://urluser:urlpass@%H%/hs",
			TodayReaches: true, DesignReaches: true, Secrets: []string{"urlpass"}},
		{ID: "userinfo-and-flags", Base: "http://urluser:urlpass@%H%/hs", FlagUser: "flaguser", FlagPass: "flagpass",
			Note: "flags must win, exactly as today", TodayReaches: true, DesignReaches: true},
		{ID: "at-in-password", Base: "http://admin:p@ssw0rd@%H%/hs",
			TodayReaches: true, DesignReaches: true, Secrets: []string{"p@ssw0rd"}},
		{ID: "pct-40", Base: "http://admin:p%40ss@%H%/hs", TodayReaches: true, DesignReaches: true},
		{ID: "pct-2F", Base: "http://admin:pa%2Fss@%H%/hs", TodayReaches: true, DesignReaches: true},
		{ID: "pct-cyrillic", Base: "http://%D0%90%D0%B4%D0%BC%D0%B8%D0%BD:%D0%9F%D0%B0%D1%80%D0%BE%D0%BB%D1%8C123@%H%/hs",
			TodayReaches: true, DesignReaches: true},
		{ID: "user-only", Base: "http://admin@%H%/hs", TodayReaches: true, DesignReaches: true},
		{ID: "empty-password", Base: "http://user:@%H%/hs", TodayReaches: true, DesignReaches: true},
		{ID: "L4-empty-username", Base: "http://:pw@%H%/hs",
			Note: "THE decision row: net/http sends Basic OnB3 today", TodayReaches: true, DesignReaches: true},
		{ID: "L4-both-empty", Base: "http://:@%H%/hs", TodayReaches: true, DesignReaches: true},
		{ID: "password-only-flag", Base: "http://%H%/hs", FlagPass: "flagpass",
			Note: "--password without --user must send NOTHING, as today", TodayReaches: true, DesignReaches: true},
		{ID: "raw-cyrillic", Base: "http://Админ:Пароль123@%H%/hs",
			Note:         "DEAD today: url.Parse fails and its error quotes the password verbatim",
			TodayReaches: false, DesignReaches: false,
			Secrets: []string{"Пароль123", "Админ"}, TodayLeaks: "Пароль123"},
		{ID: "cyrillic-path-cred", Base: "http://admin:secret@%H%/база/hs",
			Note:         "Client.BaseURL changes bytes (url.String escapes the path); the WIRE must not",
			TodayReaches: true, DesignReaches: true, Secrets: []string{"secret"}},
		{ID: "cyrillic-path-plain", Base: "http://%H%/база/hs", TodayReaches: true, DesignReaches: true},
		{ID: "mixed-case-scheme", Base: "HTTP://admin:secret@%H%/hs",
			Note:         "url.String lowercases the scheme; the wire must not care",
			TodayReaches: true, DesignReaches: true, Secrets: []string{"secret"}},
		{ID: "L1-slash", Base: "http://admin:p@ss/w0rd@%H%/hs",
			Note:         `today: DNS lookup of host "ss", and the error prints the residue w0rd@ verbatim`,
			TodayReaches: false, DesignReaches: false,
			Secrets: []string{"w0rd", "p@ss"}, TodayLeaks: "w0rd"},
		{ID: "L2-hostport", Base: "http://user:1234/passZ@%H%/hs",
			TodayReaches: false, DesignReaches: false,
			Secrets: []string{"passZ"}, TodayLeaks: "passZ"},
		{ID: "L3-query-token", Base: "http://admin:secret@%H%/hs?token=t0psecret",
			Note:         "THE ONE INTENDED DIVERGENCE: today sends it, and the method name lands INSIDE the query string",
			TodayReaches: true, DesignReaches: false,
			Secrets: []string{"t0psecret", "secret"}},
	}
	if v6host != "" {
		rows = append(rows, wireRow{ID: "ipv6-cred", Base: "http://admin:secret@%V6%/hs",
			TodayReaches: true, DesignReaches: true, Secrets: []string{"secret"}})
	}
	for i := range rows {
		rows[i].Base = strings.ReplaceAll(rows[i].Base, "%H%", host)
		rows[i].Base = strings.ReplaceAll(rows[i].Base, "%V6%", v6host)
	}
	return rows
}

type wireCapture struct {
	auth string
	uri  string
	saw  bool
}

// todayClientModel reproduces mcp-1c-go v1.12.1 (6d96384) exactly, because that
// tree is not in the working copy to be driven directly:
//
//	onec/client.go NewClient  stored baseURL verbatim
//	onec/client.go Get        http.NewRequestWithContext(..., c.BaseURL+endpoint, nil)
//	onec/client.go do         if c.User != "" { req.SetBasicAuth(c.User, c.Password) }
type todayClientModel struct{ BaseURL, User, Password string }

func (c *todayClientModel) get(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+endpoint, nil)
	if err != nil {
		return err
	}
	if c.User != "" { // the v1.12.1 gate
		req.SetBasicAuth(c.User, c.Password)
	}
	req.Close = true
	resp, err := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}

// designClientModel is the client shape under test, parameterised by the splitter
// so the same gate can score a mutated splitter.
type designClientModel struct {
	BaseURL, User, Password string
	sendAuth                bool
	baseErr                 error
}

// wireSplit is the seam. It returns the base to store, the credential lifted off
// it, whether there was one, and a refusal.
type wireSplit func(raw string) (base, user, password string, had bool, err error)

func newDesignClientModel(split wireSplit, baseURL, user, password string) *designClientModel {
	c := &designClientModel{}
	base, u, p, had, err := split(baseURL)
	c.baseErr, c.BaseURL = err, base
	switch {
	case user != "":
		c.User, c.Password, c.sendAuth = user, password, true
	case had:
		c.User, c.Password, c.sendAuth = u, p, true
	}
	return c
}

func (c *designClientModel) get(ctx context.Context, endpoint string) error {
	if c.baseErr != nil {
		return c.baseErr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+endpoint, nil)
	if err != nil {
		return err
	}
	// sendAuth covers the empty-username case; `c.User != ""` keeps the v1.12.1
	// behaviour for a caller that assigns the exported fields after construction.
	// Neither term alone is sufficient.
	if c.sendAuth || c.User != "" {
		req.SetBasicAuth(c.User, c.Password)
	}
	req.Close = true
	resp, err := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}

// wireServers starts a loopback server on IPv4 and, when the platform has one, on
// IPv6, both recording the last request they saw into *last.
func wireServers(t *testing.T, last *wireCapture) (host, v6host string) {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last = wireCapture{auth: r.Header.Get("Authorization"), uri: r.RequestURI, saw: true}
		fmt.Fprint(w, `{}`)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	host = strings.TrimPrefix(srv.URL, "http://")

	if ln, err := net.Listen("tcp", "[::1]:0"); err == nil {
		s6 := &httptest.Server{Listener: ln, Config: &http.Server{Handler: handler}}
		s6.Start()
		t.Cleanup(s6.Close)
		v6host = strings.TrimPrefix(s6.URL, "http://")
	} else {
		t.Logf("IPv6 loopback unavailable, the ipv6 row is not run: %v", err)
	}
	return host, v6host
}

// runWireParityGate is the gate. Every branch below can fail; nothing here only
// prints.
func runWireParityGate(t *testing.T, name string, split wireSplit) {
	t.Helper()
	var last wireCapture
	host, v6host := wireServers(t, &last)
	rows := wireTable(host, v6host)

	// TABLE SANITY. A gate whose table degenerated to nothing must fail, not pass.
	minRows := 20
	if v6host != "" {
		minRows = 21
	}
	if len(rows) != minRows {
		t.Fatalf("table has %d rows, want %d: the table itself changed", len(rows), minRows)
	}
	for _, r := range rows {
		for _, s := range r.Secrets {
			if !strings.Contains(r.Base, s) {
				t.Errorf("[%s] table lists secret %q which does not occur in the base %q; "+
					"the no-leak check for this row would be vacuous", r.ID, s, r.Base)
			}
		}
		if r.TodayLeaks != "" && !strings.Contains(r.Base, r.TodayLeaks) {
			t.Errorf("[%s] TodayLeaks %q does not occur in the base %q", r.ID, r.TodayLeaks, r.Base)
		}
	}

	var gotDivergent []string
	parityRows, authRows := 0, 0

	for _, r := range rows {
		last = wireCapture{}
		errToday := (&todayClientModel{BaseURL: r.Base, User: r.FlagUser, Password: r.FlagPass}).
			get(context.Background(), "/query")
		today := last

		last = wireCapture{}
		errNew := newDesignClientModel(split, r.Base, r.FlagUser, r.FlagPass).
			get(context.Background(), "/query")
		design := last

		// 1. The PREMISE. v1.12.1's behaviour is measured, not remembered.
		if today.saw != r.TodayReaches {
			t.Errorf("[%s] v1.12.1 reached the server = %v, table says %v (err=%v). "+
				"The premise of this row is gone.", r.ID, today.saw, r.TodayReaches, errToday)
		}
		// 2. The IMPLEMENTATION.
		if design.saw != r.DesignReaches {
			t.Errorf("[%s] %s reached the server = %v, table says %v (err=%v)",
				r.ID, name, design.saw, r.DesignReaches, errNew)
		}
		// Divergence is counted from what the two sides OBSERVABLY did, never from
		// what the table says they do.
		if today.saw != design.saw {
			gotDivergent = append(gotDivergent, r.ID)
		}

		// 3. BYTE PARITY on every row both sides observably send.
		if today.saw && design.saw {
			parityRows++
			if design.auth != today.auth {
				t.Errorf("[%s] Authorization: %s sent %q, v1.12.1 sent %q", r.ID, name, design.auth, today.auth)
			}
			if design.uri != today.uri {
				t.Errorf("[%s] RequestURI: %s sent %q, v1.12.1 sent %q", r.ID, name, design.uri, today.uri)
			}
			if today.auth != "" {
				authRows++
			}
		}

		// 4. A REFUSAL MUST NOT QUOTE THE VALUE.
		if !r.DesignReaches {
			if errNew == nil {
				t.Errorf("[%s] the table says this address must be refused, yet %s returned no error", r.ID, name)
			} else {
				for _, s := range r.Secrets {
					if strings.Contains(errNew.Error(), s) {
						t.Errorf("[%s] the refusal text carries the secret %q: %v", r.ID, s, errNew)
					}
				}
			}
			// POSITIVE CONTROL for the line above: on these rows v1.12.1's own error
			// DOES carry the secret, so a green no-leak check cannot mean "the checker
			// was reading an empty string".
			if r.TodayLeaks != "" {
				if errToday == nil {
					t.Errorf("[%s] control failed: v1.12.1 returned no error at all", r.ID)
				} else if !strings.Contains(errToday.Error(), r.TodayLeaks) {
					t.Errorf("[%s] control failed: v1.12.1's error does not carry %q, so the "+
						"no-leak assertion proves nothing here. today=%v", r.ID, r.TodayLeaks, errToday)
				}
			}
		}
	}

	// 5. THE DIVERGENCE SET IS CLOSED.
	sort.Strings(gotDivergent)
	want := append([]string(nil), wireExpectedDivergentIDs...)
	sort.Strings(want)
	if strings.Join(gotDivergent, ",") != strings.Join(want, ",") {
		t.Errorf("divergent rows = %v, want exactly %v", gotDivergent, want)
	}

	// 6. THE GATE MUST HAVE COMPARED SOMETHING.
	wantParity := minRows - 4 // 3 dead-today rows + the 1 declared divergence
	if parityRows != wantParity {
		t.Errorf("compared byte parity on %d rows, want %d: the comparison is not covering the table",
			parityRows, wantParity)
	}
	if authRows < 12 {
		t.Errorf("only %d rows produced a non-empty Authorization on v1.12.1; the header "+
			"comparison is mostly comparing \"\" with \"\"", authRows)
	}
	t.Logf("%s: rows=%d parity-compared=%d with-auth=%d divergent=%v", name, len(rows), parityRows, authRows, gotDivergent)
}

func TestWireParityGate(t *testing.T) {
	runWireParityGate(t, "design", func(raw string) (string, string, string, bool, error) {
		r, err := SplitURLCredentials(raw)
		return r.Base, r.User, r.Password, r.HadUserinfo, err
	})
}

// TestShippedClientMatchesTheParityModel closes the hole the gate above leaves
// open. runWireParityGate drives a MODEL of the client, and a model that has
// drifted from onec.Client would keep the gate green while the shipped binary
// sent something else. This test drives the SHIPPED Client over the same table
// and asserts it observably does what the model does.
func TestShippedClientMatchesTheParityModel(t *testing.T) {
	var last wireCapture
	host, v6host := wireServers(t, &last)
	rows := wireTable(host, v6host)

	compared, refused := 0, 0
	for _, r := range rows {
		last = wireCapture{}
		modelErr := newDesignClientModel(func(raw string) (string, string, string, bool, error) {
			res, err := SplitURLCredentials(raw)
			return res.Base, res.User, res.Password, res.HadUserinfo, err
		}, r.Base, r.FlagUser, r.FlagPass).get(context.Background(), "/query")
		model := last

		last = wireCapture{}
		var out map[string]any
		shippedErr := NewClient(r.Base, r.FlagUser, r.FlagPass).Get(context.Background(), "/query", &out)
		shipped := last

		if shipped.saw != model.saw {
			t.Errorf("[%s] the shipped client reached the server = %v, the model = %v "+
				"(shipped err=%v, model err=%v)", r.ID, shipped.saw, model.saw, shippedErr, modelErr)
			continue
		}
		if shipped.saw {
			compared++
			if shipped.auth != model.auth {
				t.Errorf("[%s] Authorization: shipped sent %q, the model sent %q", r.ID, shipped.auth, model.auth)
			}
			if shipped.uri != model.uri {
				t.Errorf("[%s] RequestURI: shipped sent %q, the model sent %q", r.ID, shipped.uri, model.uri)
			}
			continue
		}
		refused++
		if shippedErr == nil {
			t.Errorf("[%s] the shipped client sent nothing and returned no error either", r.ID)
			continue
		}
		// The text is the shipped one, so this is the assertion that matters most:
		// whatever the shipped client says about a refused address carries no secret.
		for _, s := range r.Secrets {
			if strings.Contains(shippedErr.Error(), s) {
				t.Errorf("[%s] the shipped client's error carries the secret %q: %v", r.ID, s, shippedErr)
			}
		}
	}
	if compared == 0 || refused == 0 {
		t.Fatalf("the cross-check exercised %d sending rows and %d refused rows; "+
			"a zero on either side means it proved nothing", compared, refused)
	}
	t.Logf("shipped-vs-model: sending-rows=%d refused-rows=%d", compared, refused)
}

// TestL4DecisionIsDeliberate pins the empty-username decision with its own test,
// so it is a decision and not an accident of an `if user != ""` gate.
//
// http://:pw@host has an EMPTY username and net/http authenticates it today by
// promoting req.URL.User to an Authorization header. Keeping the v1.12.1 gate
// `if c.User != ""` after the split would silently unauthenticate that base.
func TestL4DecisionIsDeliberate(t *testing.T) {
	var last wireCapture
	host, _ := wireServers(t, &last)
	base := "http://:pw@" + host + "/hs"
	const want = "Basic OnB3" // base64(":pw")

	last = wireCapture{}
	if err := (&todayClientModel{BaseURL: base}).get(context.Background(), "/query"); err != nil {
		t.Fatalf("today: %v", err)
	}
	if last.auth != want {
		t.Fatalf("v1.12.1 sent %q, expected %q; the premise of this decision is wrong", last.auth, want)
	}

	last = wireCapture{}
	var out map[string]any
	if err := NewClient(base, "", "").Get(context.Background(), "/query", &out); err != nil {
		t.Fatalf("shipped client: %v", err)
	}
	if last.auth != want {
		t.Errorf("the shipped client sent %q, want %q: http://:pw@host silently lost its authentication",
			last.auth, want)
	}

	// THE CONTROL that proves the old gate would have broken it. Without it this
	// test could pass on a design that never had anything to preserve.
	res, err := SplitURLCredentials(base)
	if err != nil {
		t.Fatalf("control failed: the split refused %q: %v", base, err)
	}
	if res.User != "" {
		t.Errorf("control failed: the v1.12.1 gate `if c.User != \"\"` would NOT have skipped this "+
			"shape (User=%q), so there is nothing here to fix", res.User)
	}
	if !res.HadUserinfo {
		t.Errorf("control failed: the split reports no userinfo for %q, so sendAuth is not what "+
			"carries this row", base)
	}
}
