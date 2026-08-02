package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

// TestWireShape_OperationalErrorIsAProtocolErrorToday records the defect this
// build exists to fix: a 1C-side failure that the model could act on leaves the
// server as a JSON-RPC ERROR frame, not as a tool result, so an MCP client has
// nothing to render into the conversation as tool output.
//
// The code is 0 rather than one of the JSON-RPC codes because the handler returns
// a plain error: internal/jsonrpc2/messages.go toWireError builds a WireError with
// only a Message when errors.As finds no *WireError in the chain.
//
// THIS TEST IS INVERTED IN PHASE 4B. Proving it green in this direction now is
// what stops the inverted version from passing vacuously.
func TestWireShape_OperationalErrorIsAProtocolErrorToday(t *testing.T) {
	mock := failingQueryServer(t, http.StatusBadRequest, `{"error":"Поле не найдено \"Номенклатура\""}`)
	client := onec.NewClient(mock.URL, "", "")
	session, log, cleanup := connectLoggedSession(t, New("test", client, nil), mock.Close)
	defer cleanup()

	res, err, frame := call(t, session, log, "execute_query",
		map[string]any{"query": "ВЫБРАТЬ Номенклатура.Ссылка ИЗ Справочник.Номенклатура КАК Номенклатура"})

	if err == nil {
		t.Fatalf("CallTool returned no error; today a failed 1C call is a protocol error")
	}
	if res != nil {
		t.Errorf("CallTool returned a non-nil result alongside the error: %+v", res)
	}
	if frame.has("result") {
		t.Errorf("frame carries a result member; today it must carry only an error: %s", frame.raw)
	}
	if !frame.has("error") {
		t.Fatalf("frame carries no error member: %s", frame.raw)
	}
	we := frame.wireError(t)
	if we.Code != 0 {
		t.Errorf("error code = %d, want 0: a plain handler error carries no JSON-RPC code today (frame %s)", we.Code, frame.raw)
	}
	const wantPrefix = "executing query in 1C: 1C returned status 400"
	if !strings.HasPrefix(we.Message, wantPrefix) {
		t.Errorf("error message = %q, want it to start with %q", we.Message, wantPrefix)
	}
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
		_, err, frame := call(t, session, log, "execute_query", map[string]any{})
		if err == nil {
			t.Fatalf("execute_query with no arguments returned no error at all")
		}
		we := frame.wireError(t)
		if we.Code == jsonrpc.CodeInvalidParams {
			t.Fatalf("the SDK rejected the call at -32602: raw registration now enforces the declared schema, "+
				"and the classification rationale that rests on it is void. frame %s", frame.raw)
		}
		if we.Message != "query is required" {
			t.Errorf("message = %q, want the handler's own %q: %s", we.Message, "query is required", frame.raw)
		}
	})

	t.Run("enum_is_not_enforced", func(t *testing.T) {
		_, err, frame := call(t, session, log, "search_code",
			map[string]any{"query": "x", "mode": "NOT_IN_ENUM"})
		if err == nil {
			t.Fatalf("search_code with a value outside the declared enum returned no error at all")
		}
		we := frame.wireError(t)
		if we.Code == jsonrpc.CodeInvalidParams {
			t.Fatalf("the SDK rejected the call at -32602: raw registration now enforces the declared enum. frame %s", frame.raw)
		}
		// Contains, not equals: search_code is wrapped by withIndexProtectionNotice,
		// which prepends a notice to the error text whenever the served generation
		// is unprotected. The handler's sentence is what proves it ran either way.
		if !strings.Contains(we.Message, `unknown mode: "NOT_IN_ENUM"`) {
			t.Errorf("message = %q, want it to carry the handler's own unknown-mode sentence: %s", we.Message, frame.raw)
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
