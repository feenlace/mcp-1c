package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// The model is told about the request and the answer, never about this
// program's insides.
//
// Measured before the repair, driving real handlers:
//
//	search_code       {"query":123}   parsing input: json: cannot unmarshal number
//	                                  into Go struct field searchCodeInput.query of
//	                                  type string
//	get_metadata_tree 200 ["a","b"]   decoding 1C response: json: cannot unmarshal
//	                                  array into Go value of type map[string][]string
//	get_metadata_tree 200 {"Спр":{}}  … into Go value of type []string
//
// searchCodeInput is unexported. map[string][]string is a decision about how
// this program stores an answer. Neither is something a caller can act on, and
// both change under refactors that change no contract at all.
// ---------------------------------------------------------------------------

// goInternals matches the shapes a Go program leaks when it prints its own
// errors. Each alternative is present because something in this tree produced
// it, not because it seemed plausible.
var goInternals = regexp.MustCompile(
	`Go value of type|Go struct field|map\[[a-z]|\[\]string|\btools\.[A-Za-z]|\bonec\.[A-Za-z]|` +
		`\bdump\.[A-Za-z]|\*json\.|[A-Za-z]+Input\b|[A-Za-z]+Request\b`)

// The last two alternatives were added AFTER the first repair shipped, because
// the built binary answered execute_query {"query":[1,2]} with the field path
// "queryLimitInput.query": encoding/json puts the name of an EMBEDDED Go type
// into json.UnmarshalTypeError.Field, and the guard as first written matched
// nothing in it. A regexp guard sees only what somebody thought to name, which
// is why the real-binary drive is not optional.

func TestNoGoInternalsReachTheModel(t *testing.T) {
	// A body that decodes into something other than the tool's target shape, for
	// every tool that decodes a 1C answer, plus a mistyped argument for every
	// tool that takes one. Both halves matter: the two leaks measured came from
	// opposite directions.
	badBodies := map[string]string{
		"array where an object was expected": `["a","b"]`,
		"object where an array was expected": `{"Справочники":{"a":1}}`,
		"number where an array was expected": `{"Справочники":5}`,
		"string at the root":                 `"кто там"`,
	}

	for name, body := range badBodies {
		b := body
		t.Run("1C answer: "+name, func(t *testing.T) {
			h := NewMetadataHandler(metadataReplay(t, b))
			text, isErr, err := callTool(t, h, `{}`)
			assertNoGoInternals(t, "get_metadata_tree", text, isErr, err)
		})
	}

	badArgs := []struct {
		tool string
		args string
	}{
		{"search_code", `{"query":123}`},
		{"get_metadata_tree", `{"filter":123}`},
		{"execute_query", `{"query":[1,2]}`},
		{"get_event_log", `{"limit":"пятьдесят"}`},
		{"get_object_structure", `{"object_type":{}}`},
		{"get_form_structure", `{"object_type":true}`},
		{"validate_query", `{"query":0}`},
		{"analyze_subsystems", `{"action":42}`},
	}
	for _, c := range badArgs {
		c := c
		t.Run("argument: "+c.tool, func(t *testing.T) {
			h := handlerByName(t, c.tool)
			text, isErr, err := callTool(t, h, c.args)
			assertNoGoInternals(t, c.tool, text, isErr, err)
			// The refusal must still name the argument, or the repair traded a
			// useless fact for no fact at all.
			blob := text
			if err != nil {
				blob = err.Error()
			}
			if !strings.Contains(blob, "аргумент") && !strings.Contains(blob, "аргументы") {
				t.Errorf("%s: the refusal does not say it is about an argument: %q", c.tool, blob)
			}
		})
	}

	// THE CONTROL. The matcher must fire on the text that used to be shipped, or
	// every pass above means only that the regexp matched nothing anywhere.
	for _, was := range []string{
		"parsing input: json: cannot unmarshal number into Go struct field searchCodeInput.query of type string",
		"decoding 1C response: json: cannot unmarshal array into Go value of type map[string][]string",
		"decoding 1C response: json: cannot unmarshal object into Go value of type []string",
		`аргумент "queryLimitInput.query" должен быть: строка, а получено: массив`,
	} {
		if !goInternals.MatchString(was) {
			t.Errorf("the matcher does not fire on a string this repo actually shipped, "+
				"so it cannot fail: %q", was)
		}
	}
	// And it must NOT fire on the replacements, or it is matching Russian prose.
	for _, now := range []string{
		`аргумент "query" должен быть: строка, а получено: число`,
		"ответ 1С разобрать не удалось: пришёл массив, а ожидался объект",
	} {
		if goInternals.MatchString(now) {
			t.Errorf("the matcher fires on the repaired text, so it is measuring the wrong thing: %q", now)
		}
	}
}

// TestReloadDumpNamesThePathOnce covers the other half of the same rule.
func TestReloadDumpNamesThePathOnce(t *testing.T) {
	dumpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dumpDir, "M.bsl"),
		[]byte("Процедура Пример() КонецПроцедуры"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := dump.NewIndex(dumpDir, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()
	waitReady(t, idx, 30*time.Second)

	// Make the walk fail the way the gate's capture did.
	if err := os.Chmod(dumpDir, 0o000); err != nil {
		t.Skipf("cannot make the dump unreadable on this platform: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dumpDir, 0o755) })

	text, isErr, err := callTool(t, NewReloadDumpHandler(idx), `{}`)
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if !isErr {
		t.Skipf("this platform let the unreadable dump be read, so there is no failure to "+
			"measure:\n%s", text)
	}
	if n := strings.Count(text, dumpDir); n != 1 {
		t.Errorf("the dump path appears %d times, want exactly 1; the repetition reads as two "+
			"different failures:\n%s", n, text)
	}
	// CONTROL: it appears at all. Removing it entirely would take away the one
	// fact the operator needs, which directory failed.
	if !strings.Contains(text, dumpDir) {
		t.Errorf("the failure no longer says which directory could not be read:\n%s", text)
	}
}

func assertNoGoInternals(t *testing.T, tool, text string, isErr bool, err error) {
	t.Helper()
	blob := text
	if err != nil {
		blob = err.Error()
	}
	if blob == "" {
		t.Fatalf("%s produced neither text nor an error, so nothing was measured (isError=%v)", tool, isErr)
	}
	if m := goInternals.FindString(blob); m != "" {
		t.Errorf("%s leaks %q to the model:\n%s", tool, m, blob)
	}
}

// handlerByName builds each tool against a far side that answers, so a failure
// can only come from the argument under test.
func handlerByName(t *testing.T, name string) mcp.ToolHandler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	c := onec.NewClient(srv.URL, "", "")

	switch name {
	case "search_code":
		return NewSearchCodeHandler(oneModuleIndex(t))
	case "get_metadata_tree":
		return NewMetadataHandler(c)
	case "execute_query":
		return NewQueryHandler(c)
	case "get_event_log":
		return NewEventLogHandler(c)
	case "get_object_structure":
		return NewObjectStructureHandler(c)
	case "get_form_structure":
		return NewFormStructureHandler(c, "")
	case "validate_query":
		return NewValidateQueryHandler(c)
	case "analyze_subsystems":
		return NewAnalyzeSubsystemsHandler(c)
	}
	t.Fatalf("no builder for %s", name)
	return nil
}

// TestEveryDotComesFromEmbedding pins the condition jsonshape.callerFieldName
// relies on: in this module a dot in a decoder field path can only be an
// embedded Go type, never a nested JSON object, so taking the last segment
// yields the key the caller wrote.
//
// If somebody gives a tool a genuinely nested input, the last segment stops
// being the whole truth and this fails rather than quietly shortening a path the
// caller does need.
func TestEveryDotComesFromEmbedding(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(metadataInput{}),
		reflect.TypeOf(searchCodeInput{}),
		reflect.TypeOf(objectInput{}),
		reflect.TypeOf(queryLimitInput{}),
		reflect.TypeOf(queryInput{}),
		reflect.TypeOf(formInput{}),
		reflect.TypeOf(analyzeSubsystemsInput{}),
		reflect.TypeOf(onec.EventLogRequest{}),
	}
	if len(types) == 0 {
		t.Fatal("no input types listed, so the walk proves nothing")
	}
	checked := 0
	for _, tp := range types {
		for i := 0; i < tp.NumField(); i++ {
			f := tp.Field(i)
			checked++
			if f.Anonymous {
				continue // embedded: the case the strip handles
			}
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				t.Errorf("%s.%s is a NAMED struct field, so a decode failure inside it would "+
					"produce a dotted path whose first segment the caller did write; "+
					"jsonshape.callerFieldName would shorten it wrongly", tp.Name(), f.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("the walk inspected no fields at all")
	}
	t.Logf("input types: %d, fields inspected: %d", len(types), checked)
}
