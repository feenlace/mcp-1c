package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolerror_test.go drives the conversion machinery through its own entry points.
//
// NOTHING IS WIRED YET at this phase, so every assertion here is about
// WithToolErrors and renderFailure and about nothing else. That is deliberate: a
// test that reached a real tool would also be measuring the wiring, and the
// wiring lands in a later commit whose own tests must be able to fail on their
// own.

// handlerReturning builds a ToolHandler that answers with exactly (res, err),
// which is the only input WithToolErrors has.
func handlerReturning(res *mcp.CallToolResult, err error) mcp.ToolHandler {
	return func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return res, err
	}
}

// runDecorated drives a decorated handler once.
func runDecorated(t *testing.T, h mcp.ToolHandler) (*mcp.CallToolResult, error) {
	t.Helper()
	return h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "unit"}})
}

// renderedText returns the sole text block of a failure result, failing the test
// when there is not exactly one, so a second block can never be read as the first.
func renderedText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("no result to read text from")
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected exactly one content block, got %d: %+v", len(res.Content), res.Content)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content block is %T, not *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

// TestWithToolErrors_SuccessIsUntouched pins that the decorator is invisible on
// the path it is not for. The pointer identity is the assertion, not the text:
// a decorator that rebuilt an equal result would still be a decorator that can
// drop a field nobody thought to compare.
func TestWithToolErrors_SuccessIsUntouched(t *testing.T) {
	want := textResult("ok")
	got, err := runDecorated(t, WithToolErrors(headingQuery, handlerReturning(want, nil)))
	if err != nil {
		t.Fatalf("success path returned an error: %v", err)
	}
	if got != want {
		t.Errorf("result pointer changed on the success path: got %p, want %p", got, want)
	}
	if got.IsError {
		t.Error("IsError is set on a success")
	}
}

// TestWithToolErrors_OperationalBecomesResult is the whole point of the release
// in one assertion: a failure the caller can act on leaves the handler as tool
// CONTENT, not as an error the model never sees.
func TestWithToolErrors_OperationalBecomesResult(t *testing.T) {
	const cause = "search index is building, please retry"
	res, err := runDecorated(t, WithToolErrors(headingSearch, handlerReturning(nil, errors.New(cause))))
	if err != nil {
		t.Fatalf("an operational failure was still returned as an error: %v", err)
	}
	if res == nil {
		t.Fatal("no result and no error: the failure vanished")
	}
	if !res.IsError {
		t.Error("IsError is not set, so a client renders the failure as a normal answer")
	}
	text := renderedText(t, res)
	if !strings.HasPrefix(text, "## "+headingSearch) {
		t.Errorf("text does not open with the heading:\n%s", text)
	}
	if !strings.Contains(text, cause) {
		t.Errorf("text does not carry the cause %q:\n%s", cause, text)
	}
}

// TestWithToolErrors_MarkSurvivesWrapping proves errors.As is load-bearing and
// not cosmetic, WITH the control that makes it a proof.
//
// The wrap under test is the exact shape index_notice.go uses,
// fmt.Errorf("%s\n%w", notice, err). A handler is free to wrap its own marked
// error the same way, and a bare type assertion would then classify a -32602 as
// an operational failure and ship it as readable content with the wrong contract.
func TestWithToolErrors_MarkSurvivesWrapping(t *testing.T) {
	const notice = "> ВНИМАНИЕ: индекс выгрузки отдаётся без защиты.\n"
	marked := InvalidParams(errors.New("parsing input: boom"))
	wrapped := fmt.Errorf("%s\n%w", notice, marked)

	// The control, in this test on purpose: if a bare assertion DID see through
	// the wrap, everything below would pass for a reason that is not the one the
	// implementation relies on.
	if _, ok := wrapped.(*ProtocolError); ok { //nolint:errorlint // this IS the control
		t.Fatal("control failed: a bare type assertion saw through the %w wrap, so errors.As proves nothing here")
	}
	var probe *ProtocolError
	if !errors.As(wrapped, &probe) {
		t.Fatal("errors.As did not find the mark through the wrap")
	}

	res, err := runDecorated(t, WithToolErrors(headingSearch, handlerReturning(nil, wrapped)))
	if res != nil {
		t.Errorf("a marked error also produced a result: %+v", res)
	}
	if err == nil {
		t.Fatal("a marked protocol error was converted into a tool result")
	}
	var we *jsonrpc.Error
	if !errors.As(err, &we) {
		t.Fatalf("returned error is %T, not a *jsonrpc.Error: %v", err, err)
	}
	if we.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("code = %d, want %d", we.Code, jsonrpc.CodeInvalidParams)
	}
	if !strings.HasPrefix(we.Message, notice) {
		t.Errorf("the notice is no longer first in the message: %q", we.Message)
	}
}

// TestWithToolErrors_InternalErrorMark pins the second mark. A recovered panic is
// a fault inside this server, and shipping it as readable content invites the
// caller to retry the same crash.
func TestWithToolErrors_InternalErrorMark(t *testing.T) {
	res, err := runDecorated(t, WithToolErrors(headingObject, handlerReturning(nil, InternalError(errDumpSubsystemPanic))))
	if res != nil {
		t.Errorf("a marked error also produced a result: %+v", res)
	}
	if err == nil {
		t.Fatal("a recovered panic was converted into a tool result")
	}
	var we *jsonrpc.Error
	if !errors.As(err, &we) {
		t.Fatalf("returned error is %T, not a *jsonrpc.Error: %v", err, err)
	}
	if we.Code != jsonrpc.CodeInternalError {
		t.Errorf("code = %d, want %d", we.Code, jsonrpc.CodeInternalError)
	}
	if we.Message != errDumpSubsystemPanic.Error() {
		t.Errorf("message = %q, want the sentinel's own text %q", we.Message, errDumpSubsystemPanic.Error())
	}
}

// TestWithToolErrors_NeverReturnsBothResultAndError walks every branch.
//
// It is load-bearing rather than pedantic: the jsonrpc2 layer logs an internal
// error and DISCARDS the result when a handler answers with both, so a branch
// that returned both would lose the answer at the transport with no test failing.
func TestWithToolErrors_NeverReturnsBothResultAndError(t *testing.T) {
	bare := &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "raw wire error"}
	cases := []struct {
		name       string
		inRes      *mcp.CallToolResult
		inErr      error
		wantResult bool
		wantErr    bool
	}{
		{"success", textResult("ok"), nil, true, false},
		{"protocol mark", nil, InvalidParams(errors.New("boom")), false, true},
		{"bare jsonrpc error", nil, bare, false, true},
		{"operational", nil, errors.New("boom"), true, false},
		// A handler that answers with BOTH must not have both propagated: the
		// stray result is dropped and the failure is what the caller reads.
		{"both from the inner handler", textResult("stray"), errors.New("boom"), true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := runDecorated(t, WithToolErrors(headingMetadata, handlerReturning(c.inRes, c.inErr)))
			if res != nil && err != nil {
				t.Fatalf("both a result and an error came back: %+v / %v", res, err)
			}
			if (res != nil) != c.wantResult {
				t.Errorf("result present = %v, want %v", res != nil, c.wantResult)
			}
			if (err != nil) != c.wantErr {
				t.Errorf("error present = %v (%v), want %v", err != nil, err, c.wantErr)
			}
		})
	}
	// The stray-result row above is only meaningful if the dropped text really
	// is gone, so assert it rather than assuming it.
	res, _ := runDecorated(t, WithToolErrors(headingMetadata,
		handlerReturning(textResult("stray"), errors.New("boom"))))
	if strings.Contains(renderedText(t, res), "stray") {
		t.Error("the inner handler's stray result survived into the rendered failure")
	}
}

// iisErrorPage is a body of the class an on-prem IIS really serves: it carries a
// physical path, the account the pool runs under and markup. None of it may
// reach the model.
const iisErrorPage = `<!DOCTYPE html><html><!-- x --><head><title>401.5</title></head>` +
	`<body>Модуль обработчика не смог обработать запрос. ` +
	`Физический путь: C:\inetpub\wwwroot\1c\ Учётная запись: DOMAIN\svc_1c</body></html>`

// TestRenderFailure_Classes walks every class the renderer distinguishes and
// asserts what each one shows and, for the foreign body, what it must not.
func TestRenderFailure_Classes(t *testing.T) {
	const onecDetail = `{(3, 5)}: Поле не найдено "Номенклатура"`

	cases := []struct {
		name    string
		heading string
		err     error
		present []string
		absent  []string
	}{
		{
			name:    "status extension envelope",
			heading: headingQuery,
			err: fmt.Errorf("executing query in 1C: %w", &onec.StatusError{
				StatusCode: 400, Endpoint: "/query", Base: "http://1c.local",
				BodyKind: onec.BodyKindExtension, Detail: onecDetail,
				RawBody: `{"error":"` + onecDetail + `"}`, ContentType: "application/json; charset=utf-8",
				BodyBytes: 60,
			}),
			present: []string{
				"## " + headingQuery,
				fmt.Sprintf(lineStatusExtension, 400),
				untrustedTextNotice,
				captionOnecError + ":",
				onecDetail,
			},
			absent: []string{lineGeneric, remedyForeignBody},
		},
		{
			name:    "status foreign body",
			heading: headingMetadata,
			err: fmt.Errorf("fetching metadata from 1C: %w", &onec.StatusError{
				StatusCode: 401, Endpoint: "/metadata", Base: "http://1c.local",
				BodyKind: onec.BodyKindForeign, RawBody: iisErrorPage,
				ContentType: "text/html", BodyBytes: len(iisErrorPage),
			}),
			present: []string{
				"## " + headingMetadata,
				fmt.Sprintf(lineStatusForeign, 401, len(iisErrorPage)),
				// The header IS shown on this branch, so it is framed and it
				// sits in code marks rather than inside the class sentence.
				untrustedHeaderNotice,
				fmt.Sprintf(lineForeignContentType, "text/html"),
				remedyForeignBody,
			},
			absent: []string{
				`C:\inetpub\wwwroot\1c\`,
				`DOMAIN\svc_1c`,
				"<!-- x -->",
				// untrustedTextNotice names 1С as the author. Here the author is
				// unknown, so the notice that ships is the one that says so;
				// asserting this one absent is what keeps the two apart.
				untrustedTextNotice,
				captionOnecError,
			},
		},
		{
			name:    "transport",
			heading: headingEventLog,
			err: fmt.Errorf("reading event log from 1C: %w", &onec.TransportError{
				Base: "http://1c.local", Endpoint: "/eventlog",
				Err: &url.Error{Op: "Post", URL: "http://1c.local/eventlog", Err: errors.New("connection refused")},
			}),
			present: []string{
				"## " + headingEventLog,
				fmt.Sprintf(lineTransport, "http://1c.local"),
				captionNetwork + ":",
				"connection refused",
				remedyUnreachable,
			},
			absent: []string{untrustedTextNotice, remedyForeignBody},
		},
		{
			name:    "request",
			heading: headingConfigInfo,
			err: fmt.Errorf("fetching configuration info from 1C: %w", &onec.RequestError{
				Base: "http://1c.local", Endpoint: "/configuration",
				Err: errors.New("net/url: invalid control character in URL"),
			}),
			present: []string{
				"## " + headingConfigInfo,
				lineRequest,
				captionNetwork + ":",
				"invalid control character",
				remedyUnreachable,
			},
			absent: []string{untrustedTextNotice, remedyForeignBody},
		},
		{
			name:    "plain error",
			heading: headingSearch,
			err:     errors.New("search index is building, please retry"),
			present: []string{
				"## " + headingSearch,
				lineGeneric,
				captionCause + ":",
				"search index is building, please retry",
			},
			absent: []string{untrustedTextNotice, remedyForeignBody, remedyUnreachable},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderFailure(c.heading, c.err)
			if n := strings.Count(got, "## "+c.heading); n != 1 {
				t.Errorf("heading appears %d times, want exactly 1:\n%s", n, got)
			}
			for _, want := range c.present {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range c.absent {
				if strings.Contains(got, unwanted) {
					t.Errorf("must not contain %q, but does:\n%s", unwanted, got)
				}
			}
		})
	}

	// POSITIVE CONTROL for the foreign-body row. A zero-hit search over text that
	// never held the strings is a filter selecting nothing, not a redaction.
	for _, secret := range []string{`C:\inetpub\wwwroot\1c\`, `DOMAIN\svc_1c`, "<!-- x -->"} {
		if !strings.Contains(iisErrorPage, secret) {
			t.Errorf("control failed: the fixture body does not contain %q, so its absence above proves nothing", secret)
		}
	}
}

// TestRenderFailure_FenceLengthComputedFromPayload pins that upstream text cannot
// break out of its fence. isError content is the channel a model is trained to
// act on, so a payload that closes the fence early would be free markdown.
func TestRenderFailure_FenceLengthComputedFromPayload(t *testing.T) {
	cases := []struct {
		name      string
		detail    string
		wantFence string
	}{
		{"no backticks", "просто текст", "```"},
		{"one backtick", "текст с ` внутри", "```"},
		{"three backticks", "текст с ``` внутри", "````"},
		{"four backticks", "текст с ```` внутри", "`````"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderFailure(headingQuery, &onec.StatusError{
				StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: c.detail,
			})
			lines := strings.Split(got, "\n")
			var fences []int
			for i, ln := range lines {
				if ln == c.wantFence {
					fences = append(fences, i)
				}
			}
			if len(fences) != 2 {
				t.Fatalf("expected exactly two %q fence lines, found %d:\n%s", c.wantFence, len(fences), got)
			}
			// The oracle: the payload sits between the fences and no line of it
			// equals the fence, so the block cannot be closed early.
			for _, ln := range lines[fences[0]+1 : fences[1]] {
				if strings.TrimSpace(ln) == c.wantFence {
					t.Errorf("the fence %q occurs inside its own payload:\n%s", c.wantFence, got)
				}
			}
			if !strings.Contains(strings.Join(lines[fences[0]+1:fences[1]], "\n"), c.detail) {
				t.Errorf("the payload is not inside the fence:\n%s", got)
			}
		})
	}
}

// TestRenderFailure_NoCauseIsAsserted is the mitigation for the failure mode where
// the remediation tells the model to rewrite a query whose problem is rights.
//
// The extension answers every one of these with the same 400 built from
// ОписаниеОшибки(), so the renderer cannot know the cause and must not pretend to.
// The assertion is byte equality of everything outside the quoted 1C text, which
// is what a cause-dependent remediation would break.
func TestRenderFailure_NoCauseIsAsserted(t *testing.T) {
	details := map[string]string{
		"syntax": "{(3, 5)}: Поле не найдено \"Номенклатура\"\n\tИЗ <<?>>Справочник.Номенклатура КАК Номенклатура",
		"rights": "Недостаточно прав для доступа к таблице Справочник.Контрагенты",
		"lock":   "Конфликт блокировок при выполнении транзакции. Превышено время ожидания.",
		"params": "Несоответствие типов параметра &ДатаНачала",
	}

	var first, firstName string
	for name, detail := range details {
		got := renderFailure(headingQuery, &onec.StatusError{
			StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: detail,
		})
		if !strings.Contains(got, remedyQueryRejected) {
			t.Errorf("%s: the shared remediation is missing:\n%s", name, got)
		}
		if !strings.Contains(got, "переписывать его") {
			t.Errorf("%s: the discriminator sentence is missing:\n%s", name, got)
		}
		// Normalise away the two things that legitimately vary with the payload:
		// the quoted 1C text itself, and the marker hint, which is emitted only
		// when the payload really carries the <<?>> insertion.
		norm := stripFencedBlock(t, got)
		norm = strings.ReplaceAll(norm, strings.TrimRight(queryMarkerHint, "\n")+"\n\n", "")
		if first == "" {
			first, firstName = norm, name
			continue
		}
		if norm != first {
			t.Errorf("the rendering outside the quoted 1C text differs between %s and %s:\n--- %s ---\n%s\n--- %s ---\n%s",
				firstName, name, firstName, first, name, norm)
		}
	}
}

// stripFencedBlock removes the fenced payload, leaving everything the renderer
// itself wrote.
func stripFencedBlock(t *testing.T, text string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start, end := -1, -1
	for i, ln := range lines {
		if !strings.HasPrefix(ln, "```") || strings.TrimLeft(ln, "`") != "" {
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		end = i
		break
	}
	if start < 0 || end < 0 {
		t.Fatalf("no fenced block found in:\n%s", text)
	}
	return strings.Join(append(append([]string{}, lines[:start]...), lines[end+1:]...), "\n")
}

// TestRenderFailure_PrintCapAndMarkerWindow pins the cap that bounds what the
// MODEL sees.
//
// After the read cap moved to 65536 bytes an extension envelope can be very
// large, and the only thing standing between that and a tool result is this cap.
// The window exists because the useful part of a 1C parse error is where it
// stopped, which is where <<?>> is, and a head-only cut would drop exactly that.
func TestRenderFailure_PrintCapAndMarkerWindow(t *testing.T) {
	head := strings.Repeat("а", 4000)
	tail := strings.Repeat("я", 1000)

	t.Run("marker window", func(t *testing.T) {
		detail := head + queryMarker + tail
		got := renderFailure(headingQuery, &onec.StatusError{
			StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: detail,
		})
		payload := fencedPayload(t, got)
		if !strings.Contains(payload, queryMarker) {
			t.Error("the window dropped the <<?>> marker, which is the one part worth keeping")
		}
		if n := utf8.RuneCountInString(payload); n != maxDetailRunes {
			t.Errorf("payload is %d runes, want the cap %d", n, maxDetailRunes)
		}
		wantNotice := fmt.Sprintf(detailWindowNotice, maxDetailRunes, utf8.RuneCountInString(detail))
		if !strings.Contains(got, strings.TrimRight(wantNotice, "\n")) {
			t.Errorf("the window notice is missing:\n%s", got)
		}
		// The two notices share a prefix, so the discriminator is the WHOLE
		// formatted head-truncation notice for this same detail, derived from the
		// constant rather than typed out, and never a byte slice of it.
		headNotice := fmt.Sprintf(detailTruncatedNotice, maxDetailRunes, utf8.RuneCountInString(detail))
		if strings.Contains(got, strings.TrimRight(headNotice, "\n")) {
			t.Error("a windowed payload also announced a head truncation")
		}
	})

	t.Run("head truncation without a marker", func(t *testing.T) {
		detail := head + tail
		got := renderFailure(headingQuery, &onec.StatusError{
			StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: detail,
		})
		payload := fencedPayload(t, got)
		if n := utf8.RuneCountInString(payload); n != maxDetailRunes {
			t.Errorf("payload is %d runes, want the cap %d", n, maxDetailRunes)
		}
		if !strings.HasPrefix(payload, "аа") {
			t.Error("the head window did not start at the head of the text")
		}
		if strings.Contains(payload, "я") {
			t.Error("the tail of an over-long text reached the payload")
		}
		wantNotice := fmt.Sprintf(detailTruncatedNotice, maxDetailRunes, utf8.RuneCountInString(detail))
		if !strings.Contains(got, strings.TrimRight(wantNotice, "\n")) {
			t.Errorf("the truncation notice is missing:\n%s", got)
		}
	})

	t.Run("a short text is not touched", func(t *testing.T) {
		const detail = "Поле не найдено"
		got := renderFailure(headingQuery, &onec.StatusError{
			StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: detail,
		})
		if p := fencedPayload(t, got); p != detail {
			t.Errorf("payload = %q, want the text unchanged %q", p, detail)
		}
		if strings.Contains(got, "> ВНИМАНИЕ") {
			t.Errorf("a text below the cap announced a truncation:\n%s", got)
		}
	})

	t.Run("the cap applies to a plain error too", func(t *testing.T) {
		got := renderFailure(headingSearch, errors.New(head+tail))
		if n := utf8.RuneCountInString(fencedPayload(t, got)); n != maxDetailRunes {
			t.Errorf("payload is %d runes, want the cap %d: the cap must not be specific to one class", n, maxDetailRunes)
		}
	})
}

// fencedPayload returns the text between the fence lines.
func fencedPayload(t *testing.T, text string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start, end := -1, -1
	for i, ln := range lines {
		if !strings.HasPrefix(ln, "```") || strings.TrimLeft(ln, "`") != "" {
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		end = i
		break
	}
	if start < 0 || end < 0 {
		t.Fatalf("no fenced block found in:\n%s", text)
	}
	return strings.Join(lines[start+1:end], "\n")
}

// TestRenderFailure_NoDashes runs the house dash rule over every string this file
// ships, IN GO. A shell check cannot fire reliably under zsh, and a check that
// cannot fire proves nothing, so the checker carries its own controls.
func TestRenderFailure_NoDashes(t *testing.T) {
	shipped := []string{
		headingQuery, headingValidateQuery, headingMetadata, headingObject, headingForm,
		headingEventLog, headingConfigInfo, headingSubsystems, headingSearch, headingReload,
		fmt.Sprintf(lineStatusExtension, 400),
		fmt.Sprintf(lineStatusForeign, 401, 1234),
		fmt.Sprintf(lineForeignContentType, "text/html"),
		lineForeignContentTypeUnusable,
		fmt.Sprintf(lineTransport, "http://server"),
		lineTransportNoBase,
		lineRequest,
		lineGeneric,
		untrustedTextNotice,
		untrustedHeaderNotice,
		captionOnecError, captionCause, captionNetwork,
		remedyQueryRejected, remedyForeignBody, remedyUnreachable,
		queryMarkerHint, queryReadOnlyReassurance,
		fmt.Sprintf(bodyTruncatedNotice, 65536),
		fmt.Sprintf(detailTruncatedNotice, 1200, 5000),
		fmt.Sprintf(detailWindowNotice, 1200, 5000),
		// Whole renderings, built from dash-free inputs so a violation can only
		// come from the renderer's own text.
		renderFailure(headingQuery, &onec.StatusError{StatusCode: 400, BodyKind: onec.BodyKindExtension, Detail: "Поле не найдено"}),
		renderFailure(headingMetadata, &onec.StatusError{StatusCode: 401, BodyKind: onec.BodyKindForeign, ContentType: "text/html", BodyBytes: 99}),
		renderFailure(headingEventLog, &onec.TransportError{Base: "http://server", Endpoint: "/eventlog", Err: errors.New("connection refused")}),
		renderFailure(headingConfigInfo, &onec.RequestError{Base: "http://server", Endpoint: "/configuration", Err: errors.New("bad url")}),
		renderFailure(headingSearch, errors.New("index is building")),
	}
	violations := 0
	for _, s := range shipped {
		for _, v := range dashViolations(s) {
			violations++
			t.Errorf("dash violation: %s", v)
		}
	}

	// NEGATIVE CONTROLS: each MUST be caught, otherwise the green above is a
	// checker that cannot fire.
	mustCatch := []struct{ name, in string }{
		{"em dash U+2014", "адрес отклонён — уберите пароль"},
		{"en dash U+2013", "адрес отклонён – уберите пароль"},
		{"minus U+2212", "адрес отклонён − уберите пароль"},
		{"hyphen U+2010", "адрес от‐клонён"},
		{"non breaking hyphen U+2011", "адрес от‑клонён"},
		{"horizontal bar U+2015", "адрес отклонён ― уберите пароль"},
		{"figure dash U+2012", "адрес ‒ отклонён"},
		{"ascii clause hyphen", "адрес отклонён - уберите пароль"},
		{"trailing hyphen", "уберите пароль-"},
		{"leading hyphen", "-адрес отклонён"},
		{"hyphen before space", "запрос- не выполнен"},
	}
	for _, c := range mustCatch {
		if len(dashViolations(c.in)) == 0 {
			t.Errorf("control miss: %s was not caught in %q", c.name, c.in)
		}
	}

	// FALSE-POSITIVE CONTROLS: house style must stay legal.
	mustPass := []string{
		"HTTP-сервис", "mcp-1c", "read-only", "Content-Type: text/html",
		"флагами --user и --password", "значение `--base`", "http://сервер/база/hs/mcp-1c",
		"(--cache-dir)", "\"--dump\"", "веб-сервер", "BM25-ранжирование",
	}
	for _, s := range mustPass {
		if v := dashViolations(s); len(v) != 0 {
			t.Errorf("false positive on %q: %v", s, v)
		}
	}

	// The three counts are MEASURED and printed, never typed into prose from
	// memory: anything that quotes them takes them from this line.
	t.Logf("shipped=%d violations=%d negative-controls=%d false-positive-controls=%d",
		len(shipped), violations, len(mustCatch), len(mustPass))
}

// dashViolations reports every dash the house rule forbids in customer-facing RU.
//
// Two allowances, and they are what the false-positive controls exercise: an
// intra-word hyphen (letter or digit on both sides) and the two hyphens of a
// "--flag" token whose first hyphen follows a boundary character. House style
// writes flags in backticks, so backtick is in the boundary set.
func dashViolations(s string) []string {
	forbidden := map[rune]bool{'—': true, '–': true, '‒': true, '―': true, '−': true, '‐': true, '‑': true}
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	isBoundary := func(r rune) bool {
		switch r {
		case ' ', '`', '(', '"', '\t', '\n':
			return true
		}
		return false
	}
	rs := []rune(s)
	window := func(i int) string { return string(rs[max(0, i-12):min(len(rs), i+12)]) }
	var out []string
	for i, r := range rs {
		if forbidden[r] {
			out = append(out, string(r)+" at "+strconv.Itoa(i)+" in "+window(i))
			continue
		}
		if r != '-' {
			continue
		}
		prev, next := ' ', ' '
		if i > 0 {
			prev = rs[i-1]
		}
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		if isWord(prev) && isWord(next) {
			continue
		}
		if isBoundary(prev) && next == '-' && i+2 < len(rs) && isWord(rs[i+2]) {
			continue
		}
		if prev == '-' && isWord(next) && i >= 2 && isBoundary(rs[i-2]) {
			continue
		}
		out = append(out, "bare hyphen at "+strconv.Itoa(i)+" in "+window(i))
	}
	return out
}
