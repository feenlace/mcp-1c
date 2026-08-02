package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two implementations of one rule, and they disagree in opposite orders.
// NewQueryHandler trims and then truncates; the extension truncates and then
// trims. ВЫБРАТЬ is seven characters and the extension's window is ten, so
// leading whitespace eats the keyword and the extension answers 400 for a query
// that visibly starts with ВЫБРАТЬ. These tests pin the wire text the extension
// actually receives, not the handler's return value.

// extensionWouldAccept transcribes the extension's own SELECT gate. Verbatim
// from extension/src/HTTPServices/MCPService/Ext/Module.bsl, ЗапросPOST:
//
//	Префикс = ВРег(СокрЛП(Лев(ТекстЗапроса, 10)));
//	Если Лев(Префикс, 7) <> "ВЫБРАТЬ" И Лев(Префикс, 6) <> "SELECT" Тогда
//		Возврат ОтветОшибка(400, "Only SELECT queries allowed");
//
// Лев is transcribed as CHARACTERS. A byte reading is excluded by what it would
// imply rather than by an assumption: under it Лев(q, 10) of a query beginning
// with ВЫБРАТЬ yields five Cyrillic characters, never the seven the comparison
// needs, so the extension would reject EVERY Russian query it has ever been
// shipped to answer. TestExtensionWouldAccept_TranscriptionTruthTable states
// that consequence as an assertion so the reading is falsifiable here rather
// than assumed.
//
// This helper is a transcription, so it is pinned by its own truth table before
// anything uses it as an oracle. A wrong transcription is then visible instead
// of load-bearing.
func extensionWouldAccept(q string) bool {
	return extensionWouldAcceptTrimmedBy(q, strings.TrimSpace)
}

// extensionWouldAcceptTrimmedBy is extensionWouldAccept with СокрЛП supplied by
// the caller. 1C documents СокрЛП as removing insignificant characters, and
// whether a newline counts as one is NOT VERIFIED here, so the ambiguity is made
// explicit and every input used below is asserted to give the same verdict under
// both readings. That is what keeps the unverified part from becoming
// load-bearing.
func extensionWouldAcceptTrimmedBy(q string, trim func(string) string) bool {
	rs := []rune(q)
	if len(rs) > 10 {
		rs = rs[:10]
	}
	prefix := []rune(strings.ToUpper(trim(string(rs))))
	head := func(n int) string {
		if len(prefix) > n {
			return string(prefix[:n])
		}
		return string(prefix)
	}
	return head(7) == "ВЫБРАТЬ" || head(6) == "SELECT"
}

func trimSpacesOnly(s string) string {
	return strings.Trim(s, " ")
}

func trimUnicodeSpace(s string) string {
	return strings.TrimFunc(s, unicode.IsSpace)
}

// prefixRow is one query and everything asserted about it.
type prefixRow struct {
	name string
	// query is what the caller sends to execute_query.
	query string
	// rawAccepted is what the extension would answer about the query text as the
	// caller typed it. false on an indented query is the divergence this phase
	// closes.
	rawAccepted bool
	// reachesServer is whether the Go-side gate lets the call out at all.
	reachesServer bool
}

func prefixRows() []prefixRow {
	return []prefixRow{
		{
			name:          "clean-cyrillic",
			query:         "ВЫБРАТЬ Товары.Ссылка ИЗ Справочник.Товары",
			rawAccepted:   true,
			reachesServer: true,
		},
		{
			name:          "clean-latin",
			query:         "SELECT Товары.Ссылка ИЗ Справочник.Товары",
			rawAccepted:   true,
			reachesServer: true,
		},
		{
			// Four spaces. The ten character window holds four spaces and six
			// letters, so ВЫБРАТЬ is never whole inside it.
			name:          "indent-4-cyrillic",
			query:         "    ВЫБРАТЬ Товары.Ссылка ИЗ Справочник.Товары",
			rawAccepted:   false,
			reachesServer: true,
		},
		{
			name:          "newline-indent-cyrillic",
			query:         "\n    ВЫБРАТЬ\n        Товары.Ссылка\n    ИЗ Справочник.Товары КАК Товары",
			rawAccepted:   false,
			reachesServer: true,
		},
		{
			// Latin, and the divergence is the same. Four spaces plus SELECT is
			// exactly ten characters and still passes; the fifth space is what
			// pushes the keyword out of the window. This row is the one whose
			// verdict does not depend on how Лев counts, because SELECT is ASCII.
			name:          "indent-5-latin",
			query:         "     SELECT Товары.Ссылка ИЗ Справочник.Товары",
			rawAccepted:   false,
			reachesServer: true,
		},
		{
			// The control that the SELECT gate still bites. Trimming must not
			// turn a refused query into an accepted one.
			name:          "indent-nonselect",
			query:         "   УДАЛИТЬ Справочник.Товары",
			rawAccepted:   false,
			reachesServer: false,
		},
	}
}

// TestExtensionWouldAccept_TranscriptionTruthTable pins the transcription on the
// exact inputs the parity test uses, before the parity test leans on it.
func TestExtensionWouldAccept_TranscriptionTruthTable(t *testing.T) {
	for _, r := range prefixRows() {
		if got := extensionWouldAccept(r.query); got != r.rawAccepted {
			t.Errorf("[%s] extensionWouldAccept(raw)=%v, table says %v; query=%q",
				r.name, got, r.rawAccepted, r.query)
		}
		// The СокрЛП ambiguity must not decide any row, on the raw text or on
		// the trimmed text the handler would send.
		for _, in := range []string{r.query, strings.TrimSpace(r.query)} {
			a := extensionWouldAcceptTrimmedBy(in, trimSpacesOnly)
			b := extensionWouldAcceptTrimmedBy(in, trimUnicodeSpace)
			if a != b {
				t.Errorf("[%s] the СокрЛП reading decides this row: spaces-only=%v unicode-space=%v on %q",
					r.name, a, b, in)
			}
		}
	}

	// The consequence that excludes a byte reading of Лев. Under it the first
	// ten BYTES of a Cyrillic query are five characters, so this must be true or
	// the transcription is claiming the extension refuses every Russian query.
	if !extensionWouldAccept("ВЫБРАТЬ Товары.Ссылка ИЗ Справочник.Товары") {
		t.Error("the transcription refuses a plain Cyrillic ВЫБРАТЬ query, which no shipped extension does")
	}

	// Negative controls: the transcription must be able to say no, and must say
	// no for the stated reason rather than for any reason.
	for _, q := range []string{
		"УДАЛИТЬ Справочник.Товары",
		"DELETE FROM Справочник.Товары",
		"ВЫБРА",
		"",
		"           ВЫБРАТЬ Товары.Ссылка",
	} {
		if extensionWouldAccept(q) {
			t.Errorf("NEGATIVE CONTROL FAILED: the transcription accepted %q", q)
		}
	}
	// Four spaces plus SELECT fits the window exactly, so this one is accepted
	// and proves the window boundary is where it is claimed to be.
	if !extensionWouldAccept("    SELECT Товары.Ссылка") {
		t.Error("the transcription refuses four spaces plus SELECT, which fits the ten character window exactly")
	}
}

// TestQueryPrefixParity_LeadingWhitespaceReachesTheExtension drives the real
// handler and asserts on the query text 1C receives, not on what the handler
// returns. The captured wire body is the oracle.
func TestQueryPrefixParity_LeadingWhitespaceReachesTheExtension(t *testing.T) {
	for _, r := range prefixRows() {
		t.Run(r.name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				called   bool
				captured string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					http.Error(w, "read error", http.StatusInternalServerError)
					return
				}
				var got onec.QueryRequest
				if err := json.Unmarshal(body, &got); err != nil {
					http.Error(w, "unmarshal error", http.StatusBadRequest)
					return
				}
				mu.Lock()
				called, captured = true, got.Query
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"columns":["Ссылка"],"rows":[["x"]],"total":1,"truncated":false}`))
			}))
			defer srv.Close()

			handler := NewQueryHandler(onec.NewClient(srv.URL, "", ""))
			args, err := json.Marshal(map[string]any{"query": r.query})
			if err != nil {
				t.Fatalf("marshalling arguments: %v", err)
			}
			res, handlerErr := handler(context.Background(), &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{Name: "execute_query", Arguments: args},
			})

			mu.Lock()
			gotCalled, gotQuery := called, captured
			mu.Unlock()

			if gotCalled != r.reachesServer {
				t.Fatalf("server called=%v, table says %v (handler err=%v)", gotCalled, r.reachesServer, handlerErr)
			}
			if !r.reachesServer {
				// The refusal is a tool result with IsError now, not an error, so
				// the control has to read the result. It must still refuse:
				// trimming may not turn a rejected query into an accepted one.
				text := failureText(t, res, handlerErr)
				if !strings.Contains(text, "ВЫБРАТЬ") {
					t.Errorf("the Go side refused, but not with the SELECT gate's own sentence:\n%s", text)
				}
				return
			}
			if handlerErr != nil {
				t.Fatalf("unexpected handler error: %v", handlerErr)
			}
			// An accepted query must not come back as a failure either: without
			// this, "reached the server" plus a rendered failure would pass.
			if res != nil && res.IsError {
				t.Fatalf("an accepted query answered with IsError:\n%s", resultText(t, res))
			}
			if !extensionWouldAccept(gotQuery) {
				t.Errorf("the extension would answer 400 for the text it received: %q\n"+
					"the caller sent: %q", gotQuery, r.query)
			}
		})
	}
}

// TestQueryPrefixParity_RuneSafeTruncation pins that the thirty byte window the
// Go side cuts for its own check can never split a rune in a way that changes
// the verdict. The window is a BYTE slice and Cyrillic is two bytes per rune, so
// the cut lands mid rune routinely; what matters is that it lands past the
// keyword every time.
func TestQueryPrefixParity_RuneSafeTruncation(t *testing.T) {
	const cut = 30 // the window in NewQueryHandler

	// The keyword must fit inside the window with room to spare, or the cut
	// could reach it. This is the property, and shrinking the window below the
	// keyword is what falsifies it.
	for _, kw := range []string{"ВЫБРАТЬ", "SELECT"} {
		if len(kw) >= cut {
			t.Fatalf("the keyword %q is %d bytes and the window is %d, so the cut can reach it", kw, len(kw), cut)
		}
	}

	rows := []struct {
		name     string
		query    string
		accepted bool
	}{
		{"cyrillic-select-40-runes", "ВЫБРАТЬ " + strings.Repeat("Я", 32), true},
		{"cyrillic-nonselect-40-runes", "УДАЛИТЬ " + strings.Repeat("Я", 32), false},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if n := utf8.RuneCountInString(r.query); n != 40 {
				t.Fatalf("fixture is %d runes, not 40", n)
			}
			// CONTROL: the cut really does land mid rune. Without it a green
			// result could mean the window never split anything.
			if utf8.ValidString(r.query[:cut]) {
				t.Fatalf("the %d byte cut of %q is valid UTF-8, so this fixture does not exercise a split rune", cut, r.query)
			}

			// The verdict computed on the split window equals the verdict
			// computed on the whole string.
			whole := strings.ToUpper(r.query)
			window := strings.ToUpper(r.query[:cut])
			wholeVerdict := strings.HasPrefix(whole, "ВЫБРАТЬ") || strings.HasPrefix(whole, "SELECT")
			windowVerdict := strings.HasPrefix(window, "ВЫБРАТЬ") || strings.HasPrefix(window, "SELECT")
			if wholeVerdict != windowVerdict {
				t.Errorf("the split rune changed the verdict: whole=%v window=%v", wholeVerdict, windowVerdict)
			}
			if windowVerdict != r.accepted {
				t.Errorf("verdict=%v, table says %v", windowVerdict, r.accepted)
			}

			// And the handler agrees with that verdict on the wire.
			var (
				mu     sync.Mutex
				called bool
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				called = true
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"columns":["Ссылка"],"rows":[],"total":0,"truncated":false}`))
			}))
			defer srv.Close()

			handler := NewQueryHandler(onec.NewClient(srv.URL, "", ""))
			args, err := json.Marshal(map[string]any{"query": r.query})
			if err != nil {
				t.Fatalf("marshalling arguments: %v", err)
			}
			_, handlerErr := handler(context.Background(), &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{Name: "execute_query", Arguments: args},
			})

			mu.Lock()
			gotCalled := called
			mu.Unlock()
			if gotCalled != r.accepted {
				t.Errorf("server called=%v, verdict says %v (handler err=%v)", gotCalled, r.accepted, handlerErr)
			}
		})
	}
}
