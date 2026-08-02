package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// The operator can see what the model was told, and it is the same text.
//
// Measured before the repair: a get_event_log call against a 500 produced a
// 389-byte answer for the model and 0 bytes of log, with an explicit slog.Warn
// on the same capture writing 69 bytes as the control that the capture worked.
// ---------------------------------------------------------------------------

// capturedRecord is one slog record, kept as structure rather than as formatted
// text so an assertion about an attribute cannot be satisfied by the attribute's
// name appearing somewhere in a rendered line.
type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

type capturingHandler struct {
	mu   sync.Mutex
	recs *[]capturedRecord
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler            { return h }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.recs = append(*h.recs, rec)
	return nil
}

// captureSlog redirects the default logger for the duration of one test.
func captureSlog(t *testing.T) *[]capturedRecord {
	t.Helper()
	recs := &[]capturedRecord{}
	old := slog.Default()
	slog.SetDefault(slog.New(&capturingHandler{recs: recs}))
	t.Cleanup(func() { slog.SetDefault(old) })
	return recs
}

func TestLoggedFailureIsTheModelFacingText(t *testing.T) {
	recs := captureSlog(t)

	const detail = "нет права на чтение журнала регистрации"
	h := NewEventLogHandler(envelope1C(t, 500, detail))
	res, err := h(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "get_event_log", Arguments: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("an operational failure must not become a protocol error: %v", err)
	}
	shown := failureText(t, res, err)

	var failures []capturedRecord
	for _, r := range *recs {
		if r.msg == logMsgToolFailed {
			failures = append(failures, r)
		}
	}
	if len(failures) != 1 {
		t.Fatalf("one operational failure produced %d log records, want exactly 1: %+v",
			len(failures), *recs)
	}
	rec := failures[0]

	// THE anti-divergence assertion. Byte identity, not containment: two texts
	// that merely overlap are exactly the state the repair exists to prevent,
	// where the operator cannot tell which one the model acted on.
	if got := rec.attrs[logAttrAnswer]; got != shown {
		t.Errorf("the logged answer and the model-facing answer differ.\nlogged: %q\nmodel:  %q", got, shown)
	}
	if rec.attrs["tool"] != "get_event_log" {
		t.Errorf("the record does not name the tool: %q", rec.attrs["tool"])
	}
	if rec.level != slog.LevelError {
		t.Errorf("level is %v; only Error reaches the operator's file on the pipe path "+
			"cmd/mcp-1c/main.go installs", rec.level)
	}

	// The failure really did carry the far side's diagnostic, so the log the
	// operator reads is worth reading.
	if !strings.Contains(rec.attrs[logAttrAnswer], detail) {
		t.Errorf("the logged answer does not carry the 1C diagnostic: %q", rec.attrs[logAttrAnswer])
	}
}

// TestLogCaptureCanFail is the positive control for the capture itself.
//
// Without it every assertion above is satisfiable by a capture that records
// nothing and a handler that logs nothing, which is precisely the pair of
// conditions that made the shipped behaviour look fine.
func TestLogCaptureCanFail(t *testing.T) {
	recs := captureSlog(t)
	if len(*recs) != 0 {
		t.Fatalf("the capture started non-empty: %+v", *recs)
	}
	slog.Error("GATEC-CONTROL-LINE", "k", "v")
	if len(*recs) != 1 || (*recs)[0].msg != "GATEC-CONTROL-LINE" || (*recs)[0].attrs["k"] != "v" {
		t.Fatalf("the capture did not record an explicit log call, so a zero from it means nothing: %+v", *recs)
	}
}

// TestSuccessAndProtocolErrorsAreNotLoggedAsFailures keeps the new log site from
// becoming noise, and keeps its silence meaningful.
//
// A record on every call would make the operator's file useless for finding the
// call that failed, and a record on a protocol error would double-count the one
// class that still travels as a wire frame the client logs for itself.
func TestSuccessAndProtocolErrorsAreNotLoggedAsFailures(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		recs := captureSlog(t)
		h := NewMetadataHandler(metadataReplay(t, `{"Справочники":["Контрагенты"]}`))
		if _, isErr, err := callTool(t, h, `{}`); isErr || err != nil {
			t.Fatalf("the control call was supposed to succeed: isError=%v err=%v", isErr, err)
		}
		if n := countFailureRecords(*recs); n != 0 {
			t.Errorf("a successful call wrote %d failure records", n)
		}
	})

	t.Run("protocol error", func(t *testing.T) {
		recs := captureSlog(t)
		h := NewMetadataHandler(metadataReplay(t, `{"Справочники":["Контрагенты"]}`))
		if _, _, err := callTool(t, h, `not json`); err == nil {
			t.Fatal("the control call was supposed to fail as a protocol error")
		}
		if n := countFailureRecords(*recs); n != 0 {
			t.Errorf("a protocol error wrote %d failure records; it already travels as a wire "+
				"frame the client logs", n)
		}
	})

	// CONTROL: the same capture, in the same test, DOES see an operational
	// failure. Two silences above prove nothing without it.
	t.Run("operational failure still logs", func(t *testing.T) {
		recs := captureSlog(t)
		h := NewMetadataHandler(onec.NewClient("http://127.0.0.1:1", "", ""))
		if _, isErr, _ := callTool(t, h, `{}`); !isErr {
			t.Fatal("the control call was supposed to fail operationally")
		}
		if n := countFailureRecords(*recs); n != 1 {
			t.Errorf("an operational failure wrote %d failure records, want 1", n)
		}
	})
}

func countFailureRecords(recs []capturedRecord) int {
	n := 0
	for _, r := range recs {
		if r.msg == logMsgToolFailed {
			n++
		}
	}
	return n
}
