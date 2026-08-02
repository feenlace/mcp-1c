package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolwiring_test.go IS THE CONTRACT, driven one site at a time.
//
// After this commit no tool returns a bare operational error. Each of the
// operational sites answers with (result, nil) and IsError set, under its own
// heading; each protocol site answers with (nil, error) carrying a NON-ZERO
// JSON-RPC code. Both halves are asserted, because either one alone is satisfied
// by a blanket conversion in its own direction.
//
// SITES ARE NAMED BY (file, source text), never by line number: this commit
// itself moves every line in ten constructors, so a line number written here
// would be false the moment it was written.
//
// DRIVERS BUILD THEIR OWN CONSTRUCTOR INSTANCES rather than going through one
// server. Two sites in analyze_subsystems and two in form are reachable only in
// different instances (an offline source present versus absent), so a single
// server can reach at most one of each pair.

// envelope1C answers every path with status and an extension envelope, which is
// the shape a real 1C failure has.
func envelope1C(t *testing.T, status int, detail string) *onec.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		body, _ := json.Marshal(map[string]string{"error": detail})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return onec.NewClient(srv.URL, "", "")
}

// placeholderIndex is the fixture reload_dump and search_code fail on.
//
// Deliberately NOT closed: Index.Close waits on Done(), which only
// FinishServeOpen ever closes, and this placeholder holds no shards and starts no
// goroutine, so there is nothing to release. Closing it hangs the test binary
// instead of failing it.
func placeholderIndex(t *testing.T) *dump.Index {
	t.Helper()
	return dump.NewServePlaceholder(t.TempDir())
}

// drive runs one handler with raw arguments, exactly as Server.callTool does.
func drive(t *testing.T, h mcp.ToolHandler, args string) (*mcp.CallToolResult, error) {
	t.Helper()
	return h(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "wiring", Arguments: json.RawMessage(args)},
	})
}

// failureText asserts the contract for an operational failure and returns the
// rendered text, so every churned assertion in this package reads the failure the
// same way instead of each one deciding for itself what a failure looks like.
func failureText(t *testing.T, res *mcp.CallToolResult, err error) string {
	t.Helper()
	if err != nil {
		t.Fatalf("an operational failure came back as an error, so the model never reads it: %v", err)
	}
	if res == nil {
		t.Fatal("neither a result nor an error came back")
	}
	if !res.IsError {
		t.Fatalf("the result does not carry IsError, so this was not reported as a failure at all:\n%s",
			resultText(t, res))
	}
	return resultText(t, res)
}

// operationalSite is one row of the contract: a failure the caller can act on.
type operationalSite struct {
	name    string
	site    string
	build   func(t *testing.T) mcp.ToolHandler
	args    string
	heading string
	wants   []string
}

func operationalSites() []operationalSite {
	const oops = "Поле не найдено \"Номенклатура\""
	return []operationalSite{
		{
			name: "analyze_subsystems action is required", site: `analyze_subsystems.go "action is required"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewAnalyzeSubsystemsHandler(envelope1C(t, 500, oops)) },
			args:    `{}`,
			heading: headingSubsystems, wants: []string{"action is required"},
		},
		{
			name: "analyze_subsystems unknown action", site: `analyze_subsystems.go "unknown action"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewAnalyzeSubsystemsHandler(envelope1C(t, 500, oops)) },
			args:    `{"action":"bogus"}`,
			heading: headingSubsystems, wants: []string{`unknown action: "bogus"`},
		},
		{
			name: "analyze_subsystems containing requires object", site: `analyze_subsystems.go "action=containing requires"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewAnalyzeSubsystemsHandler(envelope1C(t, 500, oops)) },
			args:    `{"action":"containing"}`,
			heading: headingSubsystems, wants: []string{"object parameter"},
		},
		{
			name: "analyze_subsystems source error", site: `analyze_subsystems.go "return nil, err"`,
			build: func(t *testing.T) mcp.ToolHandler {
				src := func(context.Context) (onec.SubsystemForest, error) {
					return onec.SubsystemForest{}, errors.New("выгрузка подсистем не прочитана")
				}
				return NewAnalyzeSubsystemsHandlerWithSource(envelope1C(t, 500, oops), src)
			},
			args:    `{"action":"orphans"}`,
			heading: headingSubsystems, wants: []string{"выгрузка подсистем не прочитана"},
		},
		{
			name: "analyze_subsystems live fetch", site: `analyze_subsystems.go "fetching subsystems from 1C"`,
			build: func(t *testing.T) mcp.ToolHandler {
				return NewAnalyzeSubsystemsHandlerWithSource(envelope1C(t, 500, oops), nil)
			},
			args:    `{"action":"orphans"}`,
			heading: headingSubsystems, wants: []string{oops, untrustedTextNotice},
		},
		{
			name: "configuration_info fetch", site: `configuration_info.go "fetching configuration info from 1C"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewConfigurationInfoHandler(envelope1C(t, 500, oops)) },
			args:    `{}`,
			heading: headingConfigInfo, wants: []string{oops},
		},
		{
			name: "eventlog read", site: `eventlog.go "reading event log from 1C"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewEventLogHandler(envelope1C(t, 500, oops)) },
			args:    `{}`,
			heading: headingEventLog, wants: []string{oops},
		},
		{
			name: "form required args", site: `form.go "object_type and object_name are required"`,
			build: func(t *testing.T) mcp.ToolHandler {
				return NewFormStructureHandler(envelope1C(t, 500, oops), "")
			},
			args:    `{}`,
			heading: headingForm, wants: []string{"object_type and object_name are required"},
		},
		{
			name: "form name not in dump", site: `form.go "return nil, dumpErr"`,
			build: func(t *testing.T) mcp.ToolHandler {
				srv := formHTTPServer(t, "ФормаДокумента", "Реализация")
				dumpDir := t.TempDir()
				writeDumpForm(t, dumpDir, "Documents", "РеализацияТоваровУслуг", "ФормаВыбора",
					formXMLWithTitle("Выбор", "ПолеВыбора"))
				return NewFormStructureHandler(onec.NewClient(srv.URL, "", ""), dumpDir)
			},
			args:    `{"object_type":"Document","object_name":"РеализацияТоваровУслуг","form_name":"ФормаСписка"}`,
			heading: headingForm, wants: []string{"ФормаСписка", "ФормаВыбора"},
		},
		{
			// Distinguished from the row below BY CONSTRUCTION, which is also how
			// the two sites differ in the source: a dump directory is configured
			// here and is not configured there.
			name: "form both sources fail", site: `form.go "fetching form structure from 1C: %w (dump fallback: %v)"`,
			build: func(t *testing.T) mcp.ToolHandler {
				return NewFormStructureHandler(envelope1C(t, 500, oops), t.TempDir())
			},
			args:    `{"object_type":"Document","object_name":"НетТакого"}`,
			heading: headingForm, wants: []string{oops},
		},
		{
			name: "form http only", site: `form.go "fetching form structure from 1C: %w"`,
			build: func(t *testing.T) mcp.ToolHandler {
				return NewFormStructureHandler(envelope1C(t, 500, oops), "")
			},
			args:    `{"object_type":"Document","object_name":"НетТакого"}`,
			heading: headingForm, wants: []string{oops},
		},
		{
			name: "metadata fetch", site: `metadata.go "fetching metadata from 1C"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewMetadataHandler(envelope1C(t, 500, oops)) },
			args:    `{}`,
			heading: headingMetadata, wants: []string{oops},
		},
		{
			name: "object_structure required args", site: `object_structure.go "object_type and object_name are required"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewObjectStructureHandler(envelope1C(t, 500, oops)) },
			args:    `{}`,
			heading: headingObject, wants: []string{"object_type and object_name are required"},
		},
		{
			name: "object_structure source error", site: `object_structure.go "return nil, err"`,
			build: func(t *testing.T) mcp.ToolHandler {
				sub := func(context.Context, string, string) (onec.ObjectStructure, bool, error) {
					return onec.ObjectStructure{}, true, errors.New("подсистема из выгрузки не прочитана")
				}
				return NewObjectStructureHandlerWithSource(envelope1C(t, 500, oops), sub)
			},
			args:    `{"object_type":"Subsystem","object_name":"Продажи"}`,
			heading: headingObject, wants: []string{"подсистема из выгрузки не прочитана"},
		},
		{
			name: "object_structure live fetch", site: `object_structure.go "fetching object structure from 1C"`,
			build: func(t *testing.T) mcp.ToolHandler {
				sub := func(context.Context, string, string) (onec.ObjectStructure, bool, error) {
					return onec.ObjectStructure{}, false, nil // declines every type
				}
				return NewObjectStructureHandlerWithSource(envelope1C(t, 500, oops), sub)
			},
			args:    `{"object_type":"Catalog","object_name":"Номенклатура"}`,
			heading: headingObject, wants: []string{oops},
		},
		{
			name: "query required", site: `query.go "query is required"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewQueryHandler(envelope1C(t, 500, oops)) },
			args:    `{}`,
			heading: headingQuery, wants: []string{"query is required"},
		},
		{
			name: "query is not a SELECT", site: `query.go queryNotSelectMsg`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewQueryHandler(envelope1C(t, 500, oops)) },
			args:    `{"query":"УДАЛИТЬ ИЗ Справочник.Валюты"}`,
			heading: headingQuery, wants: []string{"ВЫБРАТЬ"},
		},
		{
			name: "query execution", site: `query.go "executing query in 1C"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewQueryHandler(envelope1C(t, 400, oops)) },
			args:    `{"query":"ВЫБРАТЬ Номенклатура.Ссылка ИЗ Справочник.Номенклатура КАК Номенклатура"}`,
			heading: headingQuery, wants: []string{oops, untrustedTextNotice, remedyQueryRejected},
		},
		{
			name: "reload_dump", site: `reload_dump.go "%w.%s"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewReloadDumpHandler(placeholderIndex(t)) },
			args:    `{}`,
			heading: headingReload, wants: []string{},
		},
		{
			name: "search query required", site: `search.go "query is required"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewSearchCodeHandler(placeholderIndex(t)) },
			args:    `{}`,
			heading: headingSearch, wants: []string{"query is required"},
		},
		{
			name: "search unknown mode", site: `search.go "unknown mode"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewSearchCodeHandler(placeholderIndex(t)) },
			args:    `{"query":"x","mode":"bogus"}`,
			heading: headingSearch, wants: []string{`unknown mode: "bogus"`},
		},
		{
			name: "search engine", site: `search.go "search: %w"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewSearchCodeHandler(placeholderIndex(t)) },
			args:    `{"query":"Функция","mode":"regex"}`,
			heading: headingSearch, wants: []string{},
		},
		{
			name: "validate_query required", site: `validate_query.go "query is required"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewValidateQueryHandler(envelope1C(t, 500, oops)) },
			args:    `{}`,
			heading: headingValidateQuery, wants: []string{"query is required"},
		},
		{
			name: "validate_query call", site: `validate_query.go "validating query in 1C"`,
			build:   func(t *testing.T) mcp.ToolHandler { return NewValidateQueryHandler(envelope1C(t, 500, oops)) },
			args:    `{"query":"ВЫБРАТЬ 1"}`,
			heading: headingValidateQuery, wants: []string{oops},
		},
	}
}

// TestToolWiring_OperationalSitesAreToolResults drives every operational site and
// asserts the same three things about each: no error escapes, IsError is set, and
// the text opens under that tool's own heading.
func TestToolWiring_OperationalSitesAreToolResults(t *testing.T) {
	sites := operationalSites()
	fired := 0
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			res, err := drive(t, s.build(t), s.args)
			if err != nil {
				t.Fatalf("site %s still returns a bare error, so the model never reads it: %v", s.site, err)
			}
			if res == nil {
				t.Fatalf("site %s returned neither a result nor an error", s.site)
			}
			if !res.IsError {
				t.Fatalf("site %s answered without IsError, so a client renders a failure as a normal answer:\n%s",
					s.site, resultText(t, res))
			}
			text := resultText(t, res)
			if !strings.Contains(text, "## "+s.heading) {
				t.Errorf("site %s is not under its own heading %q:\n%s", s.site, s.heading, text)
			}
			for _, w := range s.wants {
				if !strings.Contains(text, w) {
					t.Errorf("site %s does not name the cause %q:\n%s", s.site, w, text)
				}
			}
			fired++
		})
	}
	if fired != len(sites) {
		t.Errorf("%d of %d operational sites fired", fired, len(sites))
	}
	t.Logf("operational sites driven: %d", fired)
}

// protocolSite is one row of the other half: a request that never became a valid
// tool invocation, or a fault inside this server.
type protocolSite struct {
	name  string
	site  string
	build func(t *testing.T) mcp.ToolHandler
	args  string
	code  int64
}

func protocolSites() []protocolSite {
	const oops = "неважно"
	panicSrc := func(context.Context) (onec.SubsystemForest, error) {
		return onec.SubsystemForest{}, errDumpSubsystemPanic
	}
	panicSub := func(context.Context, string, string) (onec.ObjectStructure, bool, error) {
		return onec.ObjectStructure{}, true, errDumpSubsystemPanic
	}
	return []protocolSite{
		{"analyze_subsystems decode", `analyze_subsystems.go "parsing input"`,
			func(t *testing.T) mcp.ToolHandler { return NewAnalyzeSubsystemsHandler(envelope1C(t, 500, oops)) },
			`not json`, jsonrpc.CodeInvalidParams},
		{"eventlog decode", `eventlog.go "parsing input"`,
			func(t *testing.T) mcp.ToolHandler { return NewEventLogHandler(envelope1C(t, 500, oops)) },
			`not json`, jsonrpc.CodeInvalidParams},
		{"form decode", `form.go "parsing input"`,
			func(t *testing.T) mcp.ToolHandler { return NewFormStructureHandler(envelope1C(t, 500, oops), "") },
			`not json`, jsonrpc.CodeInvalidParams},
		{"object_structure decode", `object_structure.go "parsing input"`,
			func(t *testing.T) mcp.ToolHandler { return NewObjectStructureHandler(envelope1C(t, 500, oops)) },
			`not json`, jsonrpc.CodeInvalidParams},
		{"query decode", `query.go "parsing input"`,
			func(t *testing.T) mcp.ToolHandler { return NewQueryHandler(envelope1C(t, 500, oops)) },
			`not json`, jsonrpc.CodeInvalidParams},
		{"search decode", `search.go "parsing input"`,
			func(t *testing.T) mcp.ToolHandler { return NewSearchCodeHandler(placeholderIndex(t)) },
			`not json`, jsonrpc.CodeInvalidParams},
		{"validate_query decode", `validate_query.go "parsing input"`,
			func(t *testing.T) mcp.ToolHandler { return NewValidateQueryHandler(envelope1C(t, 500, oops)) },
			`not json`, jsonrpc.CodeInvalidParams},
		{"analyze_subsystems recovered panic", `analyze_subsystems.go InternalError(err)`,
			func(t *testing.T) mcp.ToolHandler {
				return NewAnalyzeSubsystemsHandlerWithSource(envelope1C(t, 500, oops), panicSrc)
			},
			`{"action":"orphans"}`, jsonrpc.CodeInternalError},
		{"object_structure recovered panic", `object_structure.go InternalError(err)`,
			func(t *testing.T) mcp.ToolHandler {
				return NewObjectStructureHandlerWithSource(envelope1C(t, 500, oops), panicSub)
			},
			`{"object_type":"Subsystem","object_name":"Продажи"}`, jsonrpc.CodeInternalError},
	}
}

// TestToolWiring_ProtocolSitesStayProtocolErrors is the positive control for the
// test above: without it, a blanket conversion of every error into a tool result
// would pass and nothing would say so.
//
// The code must be NON-ZERO as well as correct. Zero is what a plain handler
// error produces today, so "code is right" and "the mark was actually applied"
// are different statements and both are made.
func TestToolWiring_ProtocolSitesStayProtocolErrors(t *testing.T) {
	sites := protocolSites()
	fired, invalidParams, internal := 0, 0, 0
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			res, err := drive(t, s.build(t), s.args)
			if err == nil {
				t.Fatalf("site %s was converted into a tool result; the protocol channel is gone", s.site)
			}
			if res != nil {
				t.Errorf("site %s returned a result alongside the error: %+v", s.site, res)
			}
			var we *jsonrpc.Error
			if !errors.As(err, &we) {
				t.Fatalf("site %s returned %T, which carries no JSON-RPC code: %v", s.site, err, err)
			}
			if we.Code == 0 {
				t.Errorf("site %s carries code 0, which is what an UNMARKED handler error produces", s.site)
			}
			if we.Code != s.code {
				t.Errorf("site %s code = %d, want %d", s.site, we.Code, s.code)
			}
			fired++
			switch we.Code {
			case jsonrpc.CodeInvalidParams:
				invalidParams++
			case jsonrpc.CodeInternalError:
				internal++
			}
		})
	}
	t.Logf("protocol sites driven: %d (invalid-params %d, internal %d)", fired, invalidParams, internal)
}

// TestToolWiring_PanicIsInternalAndAPlainSourceErrorIsNot puts both halves of the
// panic decision in ONE test per tool, so a blanket conversion of the whole branch
// breaks it.
//
// The two errors leave the SAME return site. Marking that site unconditionally
// would ship an ordinary offline-source failure as an internal error the caller
// cannot read; marking neither would ship a recovered panic as something the
// caller is invited to retry.
func TestToolWiring_PanicIsInternalAndAPlainSourceErrorIsNot(t *testing.T) {
	t.Run("analyze_subsystems", func(t *testing.T) {
		plain := errors.New("выгрузка подсистем не прочитана")
		hPanic := NewAnalyzeSubsystemsHandlerWithSource(envelope1C(t, 500, "x"),
			func(context.Context) (onec.SubsystemForest, error) {
				return onec.SubsystemForest{}, errDumpSubsystemPanic
			})
		hPlain := NewAnalyzeSubsystemsHandlerWithSource(envelope1C(t, 500, "x"),
			func(context.Context) (onec.SubsystemForest, error) { return onec.SubsystemForest{}, plain })
		assertPanicSplit(t, hPanic, hPlain, `{"action":"orphans"}`, headingSubsystems, plain.Error())
	})
	t.Run("object_structure", func(t *testing.T) {
		plain := errors.New("подсистема из выгрузки не прочитана")
		hPanic := NewObjectStructureHandlerWithSource(envelope1C(t, 500, "x"),
			func(context.Context, string, string) (onec.ObjectStructure, bool, error) {
				return onec.ObjectStructure{}, true, errDumpSubsystemPanic
			})
		hPlain := NewObjectStructureHandlerWithSource(envelope1C(t, 500, "x"),
			func(context.Context, string, string) (onec.ObjectStructure, bool, error) {
				return onec.ObjectStructure{}, true, plain
			})
		assertPanicSplit(t, hPanic, hPlain, `{"object_type":"Subsystem","object_name":"Продажи"}`,
			headingObject, plain.Error())
	})
}

func assertPanicSplit(t *testing.T, hPanic, hPlain mcp.ToolHandler, args, heading, plainText string) {
	t.Helper()

	res, err := drive(t, hPanic, args)
	if err == nil {
		t.Fatalf("a recovered panic became a tool result the caller is invited to retry:\n%s", resultText(t, res))
	}
	var we *jsonrpc.Error
	if !errors.As(err, &we) || we.Code != jsonrpc.CodeInternalError {
		t.Fatalf("a recovered panic did not carry %d: %T %v", jsonrpc.CodeInternalError, err, err)
	}

	res, err = drive(t, hPlain, args)
	if err != nil {
		t.Fatalf("an ordinary source failure was marked as a protocol error, so the whole branch was converted: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("an ordinary source failure did not answer with IsError: %+v", res)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "## "+heading) || !strings.Contains(text, plainText) {
		t.Errorf("the ordinary failure is not rendered under %q with its own cause:\n%s", heading, text)
	}
}
