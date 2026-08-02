package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// errshape_test.go PINS THE ERROR WIRE SHAPE THIS SERVER SHIPS TODAY, before any
// of it is changed.
//
// WHY A BASELINE AND NOT AN ASPIRATION. The change these tests exist to guard is
// an inversion: an operational failure that today leaves the server as a JSON-RPC
// error frame is to leave it as a tool result with IsError set. A test written
// only in the new direction would pass on the day it is written for the wrong
// reason if the conversion were incomplete somewhere else, and nothing would say
// so. Proving the OLD shape green first is what makes the new one a measurement
// of a change rather than a statement of intent.
//
// THE ORACLE IS THE RAW FRAME. Every assertion below reads the bytes
// LoggingTransport captured, which are real jsonrpc2.EncodeMessage output, and
// then PARSES them. Nothing here matches a substring of a frame: `"code": 0` and
// `"code":0` are the same frame, and a substring test would call them different.

// frameLog collects what LoggingTransport writes. It is mutex-protected because
// the transport writes from the client's read loop while the test reads from the
// test goroutine.
type frameLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *frameLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *frameLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Reset()
}

// reads returns the payload of every "read: " line, i.e. every frame the SERVER
// sent to the client. LoggingTransport writes "read: <json>\n" (mcp/transport.go
// loggingConn.Read).
func (l *frameLog) reads() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.buf.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, "read: "); ok {
			out = append(out, rest)
		}
	}
	return out
}

// connectLoggedSession wires srv to an in-memory client session whose client-side
// transport is wrapped in a LoggingTransport, so the test can read the frames the
// server actually put on the wire.
func connectLoggedSession(t *testing.T, srv *mcp.Server, extraCleanup func()) (*mcp.ClientSession, *frameLog, func()) {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		extraCleanup()
		t.Fatalf("server connect: %v", err)
	}
	log := &frameLog{}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "errshape-client", Version: "1.0"}, nil)
	session, err := mcpClient.Connect(ctx, &mcp.LoggingTransport{Transport: ct, Writer: log}, nil)
	if err != nil {
		extraCleanup()
		t.Fatalf("client connect: %v", err)
	}
	return session, log, func() { session.Close(); extraCleanup() }
}

// wireResponse is one parsed JSON-RPC response frame. The members are kept as
// RawMessage so that "the key is absent" and "the key is null" stay
// distinguishable, which is exactly the distinction the IsError assertions in a
// later phase turn on.
type wireResponse struct {
	raw    string
	object map[string]json.RawMessage
}

func (r wireResponse) has(key string) bool {
	_, ok := r.object[key]
	return ok
}

// wireError decodes the "error" member. Only call it after has("error").
func (r wireResponse) wireError(t *testing.T) *jsonrpc.Error {
	t.Helper()
	var we jsonrpc.Error
	if err := json.Unmarshal(r.object["error"], &we); err != nil {
		t.Fatalf("decoding the error member of %s: %v", r.raw, err)
	}
	return &we
}

// soleResponse returns the one response frame the server sent since the log was
// reset. It FAILS when there is not exactly one, so a frame belonging to another
// call can never be read as this call's answer.
func soleResponse(t *testing.T, log *frameLog) wireResponse {
	t.Helper()
	var found []wireResponse
	for _, raw := range log.reads() {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			t.Fatalf("captured frame is not JSON: %s (%v)", raw, err)
		}
		if _, isResponse := obj["id"]; !isResponse {
			continue // a notification, not an answer to a call
		}
		_, hasResult := obj["result"]
		_, hasError := obj["error"]
		if !hasResult && !hasError {
			continue
		}
		found = append(found, wireResponse{raw: raw, object: obj})
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one response frame, captured %d: %v", len(found), log.reads())
	}
	return found[0]
}

// call runs one tool call with a clean frame log and returns the raw frame that
// answered it, together with what the Go client made of it.
func call(t *testing.T, session *mcp.ClientSession, log *frameLog, name string, args map[string]any) (*mcp.CallToolResult, error, wireResponse) {
	t.Helper()
	log.reset()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	return res, err, soleResponse(t, log)
}

// failingQueryServer answers /query with the status and body given, and 404s
// everything else, so a test that reaches the wrong endpoint fails loudly instead
// of quietly getting a success.
func failingQueryServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

// TestWireShape_OperationalErrorIsAToolResult IS THE INVERSION, and it is the
// same test as before with its verdict turned round.
//
// WHAT IT USED TO PIN, kept here rather than deleted with the assertions. Under
// the name TestWireShape_OperationalErrorIsAProtocolErrorToday, and green on the
// unmodified tree, it asserted the exact opposite of every line below: CallTool
// returned err != nil and res == nil, the frame carried an "error" member and no
// "result" member, and the code was 0, because a plain handler error reaches
// internal/jsonrpc2 toWireError with no *WireError in its chain. The message was
// the raw Go chain, "executing query in 1C: 1C returned status 400...".
//
// That is the defect: a client had nothing to render into the conversation as
// tool output, so the model never saw 1C's diagnostic at all. Deleting the old
// test would have left the new one unable to say what changed; inverting it keeps
// the measurement and moves the verdict. The old direction was PROVEN green
// first, which is what stops this direction from passing vacuously.
func TestWireShape_OperationalErrorIsAToolResult(t *testing.T) {
	mock := failingQueryServer(t, http.StatusBadRequest, `{"error":"Поле не найдено \"Номенклатура\""}`)
	client := onec.NewClient(mock.URL, "", "")
	session, log, cleanup := connectLoggedSession(t, New("test", client, nil), mock.Close)
	defer cleanup()

	res, err, frame := call(t, session, log, "execute_query",
		map[string]any{"query": "ВЫБРАТЬ Номенклатура.Ссылка ИЗ Справочник.Номенклатура КАК Номенклатура"})

	if err != nil {
		t.Fatalf("CallTool returned an error; a failed 1C call must now be a tool result: %v", err)
	}
	if res == nil {
		t.Fatal("CallTool returned neither a result nor an error")
	}
	if frame.has("error") {
		t.Errorf("frame carries an error member; it must carry only a result: %s", frame.raw)
	}
	if !frame.has("result") {
		t.Fatalf("frame carries no result member: %s", frame.raw)
	}
	assertFrameIsError(t, frame)
	text := frameText(t, frame)
	if !strings.Contains(text, "Поле не найдено") {
		t.Errorf("the 1C diagnostic did not reach the tool content:\n%s", text)
	}
	if !strings.Contains(text, "## Запрос не выполнен") {
		t.Errorf("the content is not under the execute_query heading:\n%s", text)
	}
	// The frame itself, so the evidence for this release's central claim is
	// reproducible from the suite rather than retyped from a session somewhere.
	t.Logf("OPERATIONAL FRAME: %s", frame.raw)
}

// assertFrameIsError checks isError THE ONLY CORRECT WAY.
//
// CallToolResult.IsError is `json:"isError,omitempty"`, so a false value does not
// serialise as false, it VANISHES. "the key is not false" is therefore satisfied
// by a frame that never set it, and the only honest assertion is "the key is
// present and it is true".
func assertFrameIsError(t *testing.T, frame wireResponse) {
	t.Helper()
	var result map[string]json.RawMessage
	if err := json.Unmarshal(frame.object["result"], &result); err != nil {
		t.Fatalf("result member is not an object: %s (%v)", frame.raw, err)
	}
	raw, ok := result["isError"]
	if !ok {
		t.Fatalf("result carries no isError key at all, and omitempty makes that indistinguishable "+
			"from false: %s", frame.raw)
	}
	var isError bool
	if err := json.Unmarshal(raw, &isError); err != nil {
		t.Fatalf("isError is not a boolean: %s (%v)", frame.raw, err)
	}
	if !isError {
		t.Fatalf("isError is false: %s", frame.raw)
	}
}

// frameText returns the first text content block of a result frame.
func frameText(t *testing.T, frame wireResponse) string {
	t.Helper()
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(frame.object["result"], &result); err != nil {
		t.Fatalf("result member is not a tool result: %s (%v)", frame.raw, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("result carries no content: %s", frame.raw)
	}
	if result.Content[0].Type != "text" {
		t.Fatalf("first content block is %q, not text: %s", result.Content[0].Type, frame.raw)
	}
	return result.Content[0].Text
}

// TestWireShape_GenericToolRejectsMissingRequired pins the contrast that makes
// the raw-registration measurement below meaningful. bsl_syntax_help is the one
// tool registered through the generic mcp.AddTool (tools/bsl_help.go), and the
// generic path DOES validate the declared schema before the handler runs.
func TestWireShape_GenericToolRejectsMissingRequired(t *testing.T) {
	mock := failingQueryServer(t, http.StatusBadRequest, `{}`)
	client := onec.NewClient(mock.URL, "", "")
	session, log, cleanup := connectLoggedSession(t, New("test", client, nil), mock.Close)
	defer cleanup()

	_, err, frame := call(t, session, log, "bsl_syntax_help", map[string]any{})
	if err == nil {
		t.Fatalf("bsl_syntax_help accepted a call with no arguments; the schema declares query as required")
	}
	var we *jsonrpc.Error
	if !errors.As(err, &we) {
		t.Fatalf("client error is not a *jsonrpc.Error: %T %v", err, err)
	}
	if we.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("code = %d, want %d (frame %s)", we.Code, jsonrpc.CodeInvalidParams, frame.raw)
	}
	if !strings.Contains(we.Message, "required") {
		t.Errorf("message = %q, want it to name the missing required property", we.Message)
	}
	// The frame itself must agree with what the Go client reported.
	if !frame.has("error") || frame.has("result") {
		t.Errorf("frame shape disagrees with the client error: %s", frame.raw)
	}
	if code := frame.wireError(t).Code; code != jsonrpc.CodeInvalidParams {
		t.Errorf("frame code = %d, want %d: %s", code, jsonrpc.CodeInvalidParams, frame.raw)
	}
}

// TestWireShape_RawRegistrationEnforcesNoSchema pins the fact the whole
// classification decision rests on: for a tool added with Server.AddTool the
// published InputSchema is decoration. Nothing rejects a call before the handler,
// so the handler's own check is the entire gate, and treating that check's verdict
// as something the caller must be able to read is a choice about a check no other
// layer duplicates.
//
// WHAT «ENTERED» LOOKS LIKE AT THIS TREE. Today a handler that returns an error
// leaves the server as an error frame with code 0 carrying the handler's OWN text
// (see the test above). So the evidence that the handler ran is: the frame is not
// a -32602 schema rejection, and the message is the sentence the handler wrote.
// After Phase 4b the same evidence is a result frame with isError; the property
// being pinned is the same one either way, and it is the SDK's, not ours.
//
// The positive control is in this same test: the generic tool, given the same
// mistake, is rejected at -32602 before its handler runs. Without it a green
// result here could mean "the session was not talking to a server at all".
func TestWireShape_RawRegistrationEnforcesNoSchema(t *testing.T) {
	mock := failingQueryServer(t, http.StatusBadRequest, `{}`)
	client := onec.NewClient(mock.URL, "", "")
	// A placeholder index is enough: search_code's mode check runs before the
	// index is touched, and server.New registers search_code for any non-nil index.
	//
	// Deliberately NOT closed, for the reason tools/reload_dump_test.go records:
	// Index.Close waits on Done(), which only FinishServeOpen ever closes, and this
	// placeholder holds no shards and starts no goroutine, so there is nothing to
	// release. Closing it here hangs the test binary instead of failing it.
	index := dump.NewServePlaceholder(t.TempDir())
	session, log, cleanup := connectLoggedSession(t, New("test", client, index), mock.Close)
	defer cleanup()

	t.Run("required_is_not_enforced", func(t *testing.T) {
		_, _, frame := call(t, session, log, "execute_query", map[string]any{})
		if frame.has("error") {
			t.Fatalf("the SDK rejected the call before the handler: raw registration now enforces the "+
				"declared schema, and the classification rationale that rests on it is void. frame %s", frame.raw)
		}
		assertFrameIsError(t, frame)
		if text := frameText(t, frame); !strings.Contains(text, "query is required") {
			t.Errorf("content does not carry the handler's own sentence %q: %s", "query is required", text)
		}
	})

	t.Run("enum_is_not_enforced", func(t *testing.T) {
		_, _, frame := call(t, session, log, "search_code",
			map[string]any{"query": "x", "mode": "NOT_IN_ENUM"})
		if frame.has("error") {
			t.Fatalf("the SDK rejected the call before the handler: raw registration now enforces the "+
				"declared enum. frame %s", frame.raw)
		}
		assertFrameIsError(t, frame)
		// Contains, not equals: search_code is wrapped by withIndexProtectionNotice,
		// which prepends a notice whenever the served generation is unprotected.
		// The handler's sentence is what proves it ran either way.
		if text := frameText(t, frame); !strings.Contains(text, `unknown mode: "NOT_IN_ENUM"`) {
			t.Errorf("content does not carry the handler's own unknown-mode sentence: %s", text)
		}
	})

	t.Run("positive_control_generic_tool_is_enforced", func(t *testing.T) {
		_, err, frame := call(t, session, log, "bsl_syntax_help", map[string]any{})
		if err == nil {
			t.Fatalf("control failed: the generic tool accepted a call with no arguments")
		}
		if code := frame.wireError(t).Code; code != jsonrpc.CodeInvalidParams {
			t.Fatalf("control failed: the generic tool answered %d rather than %d, so the two raw "+
				"results above prove nothing about schema enforcement: %s", code, jsonrpc.CodeInvalidParams, frame.raw)
		}
	})
}

// ---------------------------------------------------------------------------
// The wire contract this release ships, measured over a real session.
// ---------------------------------------------------------------------------

// TestWireShape_ProtocolErrorsSurvive is the positive control for the inversion
// above. Without it, a blanket conversion of every handler error into a tool
// result would satisfy every other assertion in this file and nothing would say
// so.
//
// Three shapes, three different reasons a request never became a valid tool
// invocation: a body that is not a JSON object, a tool that does not exist, and a
// generic tool whose declared schema really is enforced.
func TestWireShape_ProtocolErrorsSurvive(t *testing.T) {
	mock := failingQueryServer(t, http.StatusBadRequest, `{"error":"неважно"}`)
	client := onec.NewClient(mock.URL, "", "")
	session, log, cleanup := connectLoggedSession(t, New("test", client, nil), mock.Close)
	defer cleanup()

	t.Run("arguments are not a JSON object", func(t *testing.T) {
		// The SDK does not validate a raw-registered schema (pinned above), so
		// this reaches the handler and fails in its own json.Unmarshal, which is
		// the site the InvalidParams mark is on.
		log.reset()
		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "execute_query", Arguments: json.RawMessage(`"not an object"`),
		})
		frame := soleResponse(t, log)
		if err == nil {
			t.Fatal("a body that is not a JSON object was accepted")
		}
		if frame.has("result") || !frame.has("error") {
			t.Fatalf("a decode failure did not stay a protocol error: %s", frame.raw)
		}
		if code := frame.wireError(t).Code; code != jsonrpc.CodeInvalidParams {
			t.Errorf("code = %d, want %d: %s", code, jsonrpc.CodeInvalidParams, frame.raw)
		}
		t.Logf("PROTOCOL FRAME: %s", frame.raw)
	})

	t.Run("unknown tool", func(t *testing.T) {
		_, err, frame := call(t, session, log, "no_such_tool", map[string]any{})
		if err == nil {
			t.Fatal("an unknown tool name was accepted")
		}
		if frame.has("result") || !frame.has("error") {
			t.Fatalf("an unknown tool did not stay a protocol error: %s", frame.raw)
		}
		we := frame.wireError(t)
		if we.Code != jsonrpc.CodeInvalidParams {
			t.Errorf("code = %d, want %d: %s", we.Code, jsonrpc.CodeInvalidParams, frame.raw)
		}
		if !strings.Contains(we.Message, "unknown tool") {
			t.Errorf("message = %q, want it to name the missing tool", we.Message)
		}
	})

	t.Run("generic tool schema", func(t *testing.T) {
		_, err, frame := call(t, session, log, "bsl_syntax_help", map[string]any{})
		if err == nil {
			t.Fatal("the generic tool accepted a call with no arguments")
		}
		if frame.has("result") || !frame.has("error") {
			t.Fatalf("the generic tool's schema rejection did not stay a protocol error: %s", frame.raw)
		}
		if code := frame.wireError(t).Code; code != jsonrpc.CodeInvalidParams {
			t.Errorf("code = %d, want %d: %s", code, jsonrpc.CodeInvalidParams, frame.raw)
		}
	})
}

// TestWireShape_RequiredCheckIsNowOperational pins the asymmetry this release
// SHIPS, so that it is a deliberate, visible state rather than an accident.
//
// The same mistake, a missing required argument, gets two different answers:
// execute_query answers with a readable tool result, bsl_syntax_help answers
// -32602. That difference is created by the REGISTRATION MECHANISM and not by
// this build: raw registration validates nothing, so execute_query's own check is
// the whole gate and its verdict is one the caller can act on. Both halves are
// asserted in one session, so a silent revert of either goes red.
func TestWireShape_RequiredCheckIsNowOperational(t *testing.T) {
	mock := failingQueryServer(t, http.StatusBadRequest, `{}`)
	client := onec.NewClient(mock.URL, "", "")
	session, log, cleanup := connectLoggedSession(t, New("test", client, nil), mock.Close)
	defer cleanup()

	_, err, frame := call(t, session, log, "execute_query", map[string]any{})
	if err != nil {
		t.Fatalf("execute_query with no arguments returned a protocol error: %v", err)
	}
	if frame.has("error") {
		t.Fatalf("execute_query answered with an error member: %s", frame.raw)
	}
	assertFrameIsError(t, frame)
	if text := frameText(t, frame); !strings.Contains(text, "## Запрос не выполнен") {
		t.Errorf("content is not under the execute_query heading:\n%s", text)
	}

	_, err, frame = call(t, session, log, "bsl_syntax_help", map[string]any{})
	if err == nil {
		t.Fatal("the generic tool no longer rejects a missing required argument, so the pair is gone")
	}
	if code := frame.wireError(t).Code; code != jsonrpc.CodeInvalidParams {
		t.Errorf("the generic half answered %d, want %d: %s", code, jsonrpc.CodeInvalidParams, frame.raw)
	}
}

// unprotectedIndexServer builds a server whose dump index is served out of a cache
// nothing could claim, which is the state the index-protection notice describes.
func unprotectedIndexServer(t *testing.T, freeze bool) *mcp.Server {
	t.Helper()
	dumpDir := t.TempDir()
	for i := range 3 {
		mkBSL(t, dumpDir, fmt.Sprintf("CommonModules/Модуль%02d/Ext/Module.bsl", i),
			fmt.Sprintf("Процедура Тест%02d()\n    Сообщить(\"МаркерПоиска %02d\");\nКонецПроцедуры\n", i, i))
	}
	cacheDir := t.TempDir()

	gen, err := dump.PrepareServeGeneration(context.Background(), dumpDir, cacheDir, false)
	if err != nil {
		t.Fatalf("warming the cache: %v", err)
	}
	gensig := gen.Gensig()
	gen.Release()

	if freeze {
		freezeTree(t, cacheDir)
	}
	idx, err := dump.OpenGenerationReadOnly(dumpDir, cacheDir, gensig)
	if err != nil {
		t.Fatalf("opening the generation for serving: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	<-idx.Done()

	// The state under test, asserted as a precondition. A test of "the notice is
	// first" that ran against a protected index would pass by never firing.
	if freeze && idx.UnprotectedReason() == "" {
		t.Fatal("the frozen cache produced a protected index, so nothing below tests the notice")
	}
	if !freeze && idx.UnprotectedReason() != "" {
		t.Fatalf("the writable cache produced an unprotected index: %q", idx.UnprotectedReason())
	}

	mock := failingQueryServer(t, http.StatusBadRequest, `{}`)
	t.Cleanup(mock.Close)
	return New("test", onec.NewClient(mock.URL, "", ""), idx)
}

// freezeTree clears every write bit under root, restores them afterwards, and
// PROVES the freeze took: a chmod that silently failed would let every assertion
// pass while exercising the ordinary protected path.
func freezeTree(t *testing.T, root string) {
	t.Helper()
	type saved struct {
		path string
		mode fs.FileMode
	}
	var modes []saved
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, iErr := d.Info()
		if iErr != nil {
			return iErr
		}
		modes = append(modes, saved{p, info.Mode().Perm()})
		return nil
	}); err != nil {
		t.Fatalf("walking %s before freezing it: %v", root, err)
	}
	t.Cleanup(func() {
		for _, s := range modes {
			_ = os.Chmod(s.path, s.mode)
		}
	})
	for i := len(modes) - 1; i >= 0; i-- {
		if err := os.Chmod(modes[i].path, modes[i].mode&^0o222); err != nil {
			t.Fatalf("clearing write bits on %s: %v", modes[i].path, err)
		}
	}
	if f, err := os.CreateTemp(root, ".control-"); err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		t.Fatalf("the control failed: %s is still writable after clearing its write bits", root)
	}
}

// TestSearchCodeFailureIsAToolResultNotAProtocolError IS THE SESSION-LEVEL GUARD
// ON THE DECORATOR IN NewSearchCodeHandler.
//
// The direct unit test of the notice wrapper, tools
// TestIndexNotice_AFailingCallIsDecoratedToo, calls withIndexProtectionNotice with
// a raw stub handler and asserts only on the Go error it returns, so it passes
// whether or not the real constructor is decorated. MEASURED by removing
// WithToolErrors from NewSearchCodeHandler: that test stayed green, while this
// one and four rows of the per-site table in tools went red. This is the half
// that runs over a real SESSION on the real registry, which is where a decorator
// applied to something other than the handler that was actually registered would
// still show up.
func TestSearchCodeFailureIsAToolResultNotAProtocolError(t *testing.T) {
	session, log, cleanup := connectLoggedSession(t, unprotectedIndexServer(t, true), func() {})
	defer cleanup()

	res, err, frame := call(t, session, log, "search_code",
		map[string]any{"query": "МаркерПоиска", "mode": "НетТакогоРежима"})
	if err != nil {
		t.Fatalf("a failed search is still a protocol error, so WithToolErrors is not wired "+
			"into NewSearchCodeHandler: %v", err)
	}
	if res == nil {
		t.Fatal("neither a result nor an error came back")
	}
	assertFrameIsError(t, frame)
	text := frameText(t, frame)
	if !strings.HasPrefix(text, "> ВНИМАНИЕ") {
		t.Errorf("the index-protection notice is not the first thing in the failure:\n%s", text)
	}
	if !strings.Contains(text, "## Поиск по выгрузке не выполнен") {
		t.Errorf("the failure is not under the search_code heading:\n%s", text)
	}
}

// TestSearchCode_NoticeStaysFirstOnAFailedCall pins the decorator NESTING ORDER.
//
// Both orders build and only one is correct. With the notice wrapper on the
// OUTSIDE an operational failure reaches prependNotice as a RESULT and takes the
// same path a success takes, so the notice lands first on both. With the wrapper
// inside, the failure is still an error when the notice is applied, the notice
// ends up BELOW the heading on failures while staying on top for successes, and
// the two halves of one statement about one answer drift apart depending on how
// the call went. That is exactly the defect the notice wrapper was written to
// prevent.
//
// The negative control is in this same test: on a HEALTHY index the first line is
// the heading and no notice appears, so a green result cannot mean "the notice is
// always first because it is always there".
func TestSearchCode_NoticeStaysFirstOnAFailedCall(t *testing.T) {
	const noticeMarker = "индекс выгрузки отдаётся без защиты"

	t.Run("unprotected index", func(t *testing.T) {
		session, log, cleanup := connectLoggedSession(t, unprotectedIndexServer(t, true), func() {})
		defer cleanup()

		_, _, frame := call(t, session, log, "search_code",
			map[string]any{"query": "МаркерПоиска", "mode": "НетТакогоРежима"})
		text := frameText(t, frame)
		first, _, _ := strings.Cut(text, "\n")
		if !strings.Contains(first, noticeMarker) {
			t.Errorf("the first line is %q, want the protection notice", first)
		}
		notice := strings.Index(text, noticeMarker)
		heading := strings.Index(text, "## Поиск по выгрузке не выполнен")
		if heading < 0 {
			t.Fatalf("no heading in:\n%s", text)
		}
		if notice > heading {
			t.Errorf("the notice sits BELOW the heading, which is the inverted nesting order:\n%s", text)
		}
	})

	t.Run("healthy index negative control", func(t *testing.T) {
		session, log, cleanup := connectLoggedSession(t, unprotectedIndexServer(t, false), func() {})
		defer cleanup()

		_, _, frame := call(t, session, log, "search_code",
			map[string]any{"query": "МаркерПоиска", "mode": "НетТакогоРежима"})
		text := frameText(t, frame)
		if strings.Contains(text, noticeMarker) {
			t.Errorf("a healthy index emitted the protection notice:\n%s", text)
		}
		if !strings.HasPrefix(text, "## Поиск по выгрузке не выполнен") {
			t.Errorf("the failure does not open with the heading:\n%s", text)
		}
	})
}
