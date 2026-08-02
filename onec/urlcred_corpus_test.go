package onec

import (
	"fmt"
	"strings"
)

// urlcred_corpus_test.go holds ONE adversarial table and ONE checker, so that the
// shipped splitter and every mutation of it are judged by literally the same code.
//
// It is deliberately data plus a scorer and contains no Test function of its own:
// the assertions live in urlcred_test.go. Keeping the table separate is what makes
// the mutation gate possible, because a mutation replaces urlcred.go and leaves
// the judge untouched.

// corpusResult mirrors BaseURLSplit as plain data, so a mutated implementation may
// have a different internal shape and still be scored by this table.
type corpusResult struct {
	Base        string
	User        string
	Password    string
	HadUserinfo bool
	Display     string
}

// corpusImpl is the pair of entry points under test.
//
//	Split is what NewClient calls; its Base is what lands in Client.BaseURL.
//	Check is what the flag boundary in cmd/mcp-1c calls.
type corpusImpl struct {
	Name  string
	Split func(string) (corpusResult, error)
	Check func(string) error
}

// corpusRow is one adversarial input plus everything expected of it.
type corpusRow struct {
	ID   string
	In   string
	Note string

	// Refuse says the address must be rejected.
	Refuse bool

	// Expected values when Refuse is false. Base is compared byte for byte.
	Base        string
	User        string
	Password    string
	HadUserinfo bool
	Display     string

	// Secrets are fragments of In that must never survive into Base, Display
	// or a refusal message. Each is checked only if it really occurs in In,
	// so a typo in the table cannot make the check vacuous.
	Secrets []string
}

// corpusTable is the adversarial corpus. Every leak shape measured on the
// hand-rolled splitter is here, alongside every shape that must keep working.
func corpusTable() []corpusRow {
	return []corpusRow{
		// ---- L1: '@' in the password, followed by / ? # ------------------
		{
			ID: "L1-slash", In: `http://admin:p@ss/w0rd@1c.corp.local/hs`,
			Note:   "RFC: userinfo=admin:p host=ss, tail w0rd@1c.corp.local stays in the path",
			Refuse: true, Secrets: []string{"w0rd", "p@ss"},
		},
		{
			ID: "L1-question", In: `http://admin:p@ss?w0rd@1c.corp.local/hs`,
			Refuse: true, Secrets: []string{"w0rd", "p@ss"},
		},
		{
			ID: "L1-hash", In: `http://admin:p@ss#w0rd@1c.corp.local/hs`,
			Refuse: true, Secrets: []string{"w0rd", "p@ss"},
		},

		// ---- L2: truncated authority parses as a dialable host -----------
		{
			ID: "L2-hostport", In: `http://user:1234/passZ@host/hs`,
			Note:   "authority user:1234 is a valid host:port, so net/url finds NO userinfo",
			Refuse: true, Secrets: []string{"passZ", "user:1234"},
		},
		{
			ID: "L2-slashinuser", In: `http://us/er:passZZ@host/hs`,
			Refuse: true, Secrets: []string{"passZZ"},
		},

		// ---- L3: a secret in the query string ----------------------------
		{
			ID: "L3-querytoken", In: `http://admin:secret@host/hs?token=t0psecret`,
			Note:   "userinfo strips cleanly, the token does not",
			Refuse: true, Secrets: []string{"t0psecret", "secret"},
		},

		// ---- L4: empty username ------------------------------------------
		{
			ID: "L4-emptyuser", In: `http://:pw@host`,
			Note: "net/http sends Basic OnB3 today; the split must keep sending it",
			Base: `http://host`, User: "", Password: "pw", HadUserinfo: true,
			Display: `http://host`, Secrets: []string{"pw"},
		},
		{
			ID: "L4-bothempty", In: `http://:@host/hs`,
			Base: `http://host/hs`, User: "", Password: "", HadUserinfo: true,
			Display: `http://host`,
		},

		// ---- '@' in the password with each of / ? # ----------------------
		{
			ID: "at-pw-slash", In: `http://user:pa/ss@host/hs`,
			Note:   "url.Parse fails: invalid port \":pa\" after host, and that error quotes the password",
			Refuse: true, Secrets: []string{"pa/ss", "ss@host"},
		},
		{
			ID: "at-pw-question", In: `http://user:pa?ss@host/hs`,
			Refuse: true, Secrets: []string{"pa?ss"},
		},
		{
			ID: "at-pw-hash", In: `http://user:pa#ss@host/hs`,
			Refuse: true, Secrets: []string{"pa#ss"},
		},

		// ---- scheme-less and opaque --------------------------------------
		{
			ID: "schemeless", In: `user:pass@host:8080`,
			Note:   "url.Parse reads scheme=user, opaque=pass@host:8080; there is no authority",
			Refuse: true, Secrets: []string{"pass@host", "user:pass"},
		},
		{
			ID: "opaque", In: `http:user:pass@host`,
			Refuse: true, Secrets: []string{"user:pass"},
		},

		// ---- the shapes that MUST keep working ---------------------------
		{
			ID: "at-in-password-plain", In: `http://admin:p@ssw0rd@host/hs`,
			Note: "net/url allows '@' inside userinfo and takes the LAST one",
			Base: `http://host/hs`, User: "admin", Password: "p@ssw0rd", HadUserinfo: true,
			Display: `http://host`, Secrets: []string{"p@ssw0rd", "ssw0rd"},
		},
		{
			ID: "plain-credential", In: `http://admin:sup3rsecret@1c.corp.local/hs/mcp-1c`,
			Base: `http://1c.corp.local/hs/mcp-1c`, User: "admin", Password: "sup3rsecret",
			HadUserinfo: true, Display: `http://1c.corp.local`, Secrets: []string{"sup3rsecret"},
		},
		{
			ID: "no-userinfo", In: `http://1c.corp.local/hs/mcp-1c`,
			Base: `http://1c.corp.local/hs/mcp-1c`, Display: `http://1c.corp.local`,
		},
		{
			ID: "user-only", In: `http://admin@host/hs`,
			Base: `http://host/hs`, User: "admin", HadUserinfo: true, Display: `http://host`,
		},
		{
			ID: "empty-password", In: `http://user:@host/hs`,
			Base: `http://host/hs`, User: "user", Password: "", HadUserinfo: true,
			Display: `http://host`,
		},
		{
			ID: "pct-40", In: `http://admin:p%40ss@host/hs`,
			Note: "%40 decodes to '@' in the password and is NOT a delimiter",
			Base: `http://host/hs`, User: "admin", Password: "p@ss", HadUserinfo: true,
			Display: `http://host`, Secrets: []string{"p%40ss"},
		},
		{
			ID: "pct-2F", In: `http://admin:pa%2Fss@host/hs`,
			Base: `http://host/hs`, User: "admin", Password: "pa/ss", HadUserinfo: true,
			Display: `http://host`, Secrets: []string{"pa%2Fss"},
		},
		{
			ID: "pct-cyrillic", In: `http://%D0%90%D0%B4%D0%BC%D0%B8%D0%BD:%D0%9F%D0%B0%D1%80%D0%BE%D0%BB%D1%8C123@host/hs`,
			Note: "percent-encoded Админ:Пароль123",
			Base: `http://host/hs`, User: "Админ", Password: "Пароль123", HadUserinfo: true,
			Display: `http://host`, Secrets: []string{"%D0%9F%D0%B0%D1%80%D0%BE%D0%BB%D1%8C123"},
		},
		{
			ID: "raw-cyrillic", In: `http://Админ:Пароль123@1c.corp.local/hs/mcp-1c`,
			Note:   "url.Parse fails with net/url: invalid userinfo; this address is DEAD today",
			Refuse: true, Secrets: []string{"Пароль123", "Админ"},
		},
		{
			ID: "bad-escape", In: `http://user:sec%zz@host/hs`,
			Note:   "url.Parse fails with invalid URL escape \"%zz\"",
			Refuse: true, Secrets: []string{"sec%zz"},
		},
		{
			ID: "space-in-password", In: `http://admin:p@ss w0rd@host/hs`,
			Refuse: true, Secrets: []string{"ss w0rd"},
		},

		// ---- hosts --------------------------------------------------------
		{
			ID: "ipv6-cred", In: `http://admin:secret@[2001:db8::1]:8080/hs`,
			Base: `http://[2001:db8::1]:8080/hs`, User: "admin", Password: "secret",
			HadUserinfo: true, Display: `http://[2001:db8::1]:8080`, Secrets: []string{"secret"},
		},
		{
			ID: "ipv6-plain", In: `http://[::1]:8080/hs`,
			Base: `http://[::1]:8080/hs`, Display: `http://[::1]:8080`,
		},
		{
			ID: "explicit-port", In: `http://1c.corp.local:8080/hs/mcp-1c`,
			Base: `http://1c.corp.local:8080/hs/mcp-1c`, Display: `http://1c.corp.local:8080`,
		},
		{
			ID: "default-port-cred", In: `http://admin:secret@1c.corp.local:80/hs`,
			Note: "control for url.String() normalisation: the :80 must survive",
			Base: `http://1c.corp.local:80/hs`, User: "admin", Password: "secret",
			HadUserinfo: true, Display: `http://1c.corp.local:80`, Secrets: []string{"secret"},
		},
		{
			ID: "mixed-case-cred", In: `HTTP://admin:secret@1C.Corp.Local/HS/mcp-1c`,
			Note: "control for url.String() normalisation: scheme case",
			Base: `http://1C.Corp.Local/HS/mcp-1c`, User: "admin", Password: "secret",
			HadUserinfo: true, Display: `http://1C.Corp.Local`, Secrets: []string{"secret"},
		},
		{
			ID: "pct-in-path-cred", In: `http://admin:secret@host/hs%2Fx/mcp-1c`,
			Note: "control for url.String() normalisation: %2F in the path must survive",
			Base: `http://host/hs%2Fx/mcp-1c`, User: "admin", Password: "secret",
			HadUserinfo: true, Display: `http://host`, Secrets: []string{"secret"},
		},
		{
			ID: "https-cred", In: `https://admin:secret@1c.corp.local/hs/mcp-1c`,
			Base: `https://1c.corp.local/hs/mcp-1c`, User: "admin", Password: "secret",
			HadUserinfo: true, Display: `https://1c.corp.local`, Secrets: []string{"secret"},
		},
		{
			ID: "no-path", In: `http://admin:secret@host`,
			Base: `http://host`, User: "admin", Password: "secret", HadUserinfo: true,
			Display: `http://host`, Secrets: []string{"secret"},
		},
		{
			ID: "trailing-slash", In: `http://admin:secret@host/`,
			Base: `http://host/`, User: "admin", Password: "secret", HadUserinfo: true,
			Display: `http://host`, Secrets: []string{"secret"},
		},

		// ---- '@' and '?' outside the authority ---------------------------
		{
			ID: "at-in-path", In: `http://1c.corp.local/hs/mcp-1c@v2`,
			Note:   "legal RFC path, refused BY POLICY; the cost of closing L2",
			Refuse: true,
		},
		{
			ID: "at-in-query", In: `http://1c.corp.local/odata/X?$filter=a@b`,
			Refuse: true,
		},
		{
			ID: "query-token", In: `http://1c.corp.local/hs?token=t0psecret`,
			Note:   "no userinfo at all, yet a secret would sit in Client.BaseURL",
			Refuse: true, Secrets: []string{"t0psecret"},
		},
		{
			ID: "fragment", In: `http://1c.corp.local/hs#frag`,
			Refuse: true,
		},
		{
			ID: "unparsable-no-at", In: `http://1c.corp.local:8o80/hs?x=1`,
			Note:   "parse fails and there is no '@': the message must NOT talk about credentials",
			Refuse: true,
		},

		// ---- must pass through byte for byte -----------------------------
		{ID: "internal-proxy", In: `proxy://SomeBase`, Base: `proxy://SomeBase`, Display: `proxy://SomeBase`},
		{ID: "internal-poll", In: `poll://local`, Base: `poll://local`, Display: `poll://local`},
		{ID: "internal-empty", In: ``, Base: ``, Display: ``},
		{
			ID: "internal-proxy-space", In: `proxy://Бухгалтерия предприятия`,
			Note: "url.Parse FAILS on this (invalid character \" \" in host name); it must still pass through",
			Base: `proxy://Бухгалтерия предприятия`, Display: ``,
		},
		{
			ID: "not-a-url", In: `not a url at all`,
			Base: `not a url at all`, Display: ``,
		},

		// ---- non-ASCII paths and infobase names -------------------------
		{
			ID: "cyrillic-path-plain", In: `http://1c.corp.local/база/hs/mcp-1c`,
			Note: "no credential: must come back byte for byte, NOT percent-encoded",
			Base: `http://1c.corp.local/база/hs/mcp-1c`, Display: `http://1c.corp.local`,
		},
		{
			ID: "cyrillic-path-cred", In: `http://admin:secret@1c.corp.local/база/hs/mcp-1c`,
			Note: "MEASURED normalisation: url.String() percent-encodes the path when the userinfo is removed",
			Base: `http://1c.corp.local/%D0%B1%D0%B0%D0%B7%D0%B0/hs/mcp-1c`,
			User: "admin", Password: "secret", HadUserinfo: true,
			Display: `http://1c.corp.local`, Secrets: []string{"secret"},
		},
		{
			ID: "internal-proxy-cyrillic", In: `proxy://Бухгалтерия`,
			Note: "url.Parse succeeds and url.String() WOULD percent-encode the host; the verbatim path must win",
			Base: `proxy://Бухгалтерия`, Display: `proxy://Бухгалтерия`,
		},

		// ---- the residual, stated rather than hidden ---------------------
		{
			ID: "residual-hostport", In: `http://user:1234/passZ`,
			Note: "no '@' anywhere, so nothing marks this as a credential; accepted, and Display shows user:1234",
			Base: `http://user:1234/passZ`, Display: `http://user:1234`,
		},
	}
}

// corpusFinding is one failed expectation.
type corpusFinding struct {
	RowID  string
	In     string
	Detail string
}

func (f corpusFinding) String() string {
	return fmt.Sprintf("[%s] in=%q: %s", f.RowID, f.In, f.Detail)
}

// corpusRun scores impl against the whole table and returns every violation.
func corpusRun(impl corpusImpl) []corpusFinding {
	var out []corpusFinding
	add := func(r corpusRow, format string, args ...any) {
		out = append(out, corpusFinding{RowID: r.ID, In: r.In, Detail: fmt.Sprintf(format, args...)})
	}

	for _, r := range corpusTable() {
		res, err := impl.Split(r.In)
		checkErr := impl.Check(r.In)

		// INVARIANT A: the guard runs always. Check and Split must agree, or
		// the flag boundary can accept something the client will still store.
		if (err == nil) != (checkErr == nil) {
			add(r, "guard disagrees with split: Split err=%v, Check err=%v", err, checkErr)
		}

		if r.Refuse {
			if err == nil {
				add(r, "expected REFUSE, got ACCEPT with Base=%q user=%q pass=%q", res.Base, res.User, res.Password)
			}
			if checkErr == nil {
				add(r, "expected the guard to REFUSE, it returned nil")
			}
			// INVARIANT C: a refusal message may not quote the value.
			if err != nil {
				for _, s := range corpusPresentSecrets(r) {
					if strings.Contains(err.Error(), s) {
						add(r, "refusal message contains the secret %q", s)
					}
				}
			}
			// INVARIANT E: a refused address leaves nothing behind.
			if res.Base != "" || res.User != "" || res.Password != "" || res.Display != "" || res.HadUserinfo {
				add(r, "refused but the result is not zero: %+v", res)
			}
			continue
		}

		if err != nil {
			add(r, "expected ACCEPT, got REFUSE: %v", err)
			continue
		}
		if res.Base != r.Base {
			add(r, "Base = %q, want %q", res.Base, r.Base)
		}
		if res.User != r.User {
			add(r, "User = %q, want %q", res.User, r.User)
		}
		if res.Password != r.Password {
			add(r, "Password = %q, want %q", res.Password, r.Password)
		}
		if res.HadUserinfo != r.HadUserinfo {
			add(r, "HadUserinfo = %v, want %v", res.HadUserinfo, r.HadUserinfo)
		}
		if res.Display != r.Display {
			add(r, "Display = %q, want %q", res.Display, r.Display)
		}
		// INVARIANT B: no credential byte survives into anything kept.
		for _, s := range corpusPresentSecrets(r) {
			if strings.Contains(res.Base, s) {
				add(r, "LEAK: Base %q still contains the secret %q", res.Base, s)
			}
			if strings.Contains(res.Display, s) {
				add(r, "LEAK: Display %q still contains the secret %q", res.Display, s)
			}
		}
		// INVARIANT D: an address with no credential is returned byte for byte.
		if !res.HadUserinfo && res.Base != r.In {
			add(r, "no credential was found, yet Base %q != input %q", res.Base, r.In)
		}
		// INVARIANT F: Display never carries a query string or a path.
		if strings.ContainsAny(res.Display, "?#") {
			add(r, "Display %q carries ? or #", res.Display)
		}
		if i := strings.Index(res.Display, "://"); i >= 0 && strings.Contains(res.Display[i+3:], "/") {
			add(r, "Display %q carries a path", res.Display)
		}
	}
	return out
}

// corpusPresentSecrets returns only the fragments that really occur in the input,
// so a mistyped table entry cannot silently make a check vacuous.
func corpusPresentSecrets(r corpusRow) []string {
	var out []string
	for _, s := range r.Secrets {
		if s != "" && strings.Contains(r.In, s) {
			out = append(out, s)
		}
	}
	return out
}

// corpusSecretsSanity fails if a table row lists a secret that does not occur in
// its own input. Without it, INVARIANT B could pass by testing nothing.
func corpusSecretsSanity() []corpusFinding {
	var out []corpusFinding
	for _, r := range corpusTable() {
		for _, s := range r.Secrets {
			if !strings.Contains(r.In, s) {
				out = append(out, corpusFinding{RowID: r.ID, In: r.In,
					Detail: fmt.Sprintf("table lists secret %q, which does not occur in the input", s)})
			}
		}
	}
	return out
}
