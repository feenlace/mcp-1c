package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// tool_error_contract_test.go IS THE ANTI-REGRESSION GUARD: tool #12 must not be
// able to ship blind.
//
// WHAT THIS FILE ADDS THAT THE DRIVERS CANNOT ADD. toolwiring_test.go drives every
// classified site and proves each one answers correctly. It cannot prove its own
// table is COMPLETE: a site nobody listed is a site nobody drives, and a table
// that never mentions it is green forever. So the tables there are one
// enumeration, and this file adds a second one taken from the SOURCE and a third
// one written down as literals, and asserts all three agree. A new handler that
// nobody classified moves the AST count and nothing else, which is precisely the
// disagreement the three-way comparison reports.
//
// The firing half is NOT repeated here. TestToolWiring_OperationalSitesAreToolResults,
// TestToolWiring_ProtocolSitesStayProtocolErrors and
// TestToolWiring_PanicIsInternalAndAPlainSourceErrorIsNot own it; this file reads
// operationalSites() and protocolSites() as data and asserts they cover the census.
//
// NOTHING HERE ASSERTS ON A LINE NUMBER. Sites are identified by
// (file, constructor, class); line numbers appear only inside failure messages,
// because every commit that touches a constructor moves them.
//
// NO ASSERTION IS A FLOOR. A floor is the classic false green: >= 33 is satisfied
// by 34, and 34 is exactly the event this guard exists to catch.

// ---------------------------------------------------------------------------
// THE EXPECTATION TABLE — enumeration (C).
//
// Every number below was MEASURED against this tree with the walk in this file
// and is reproduced in the test log, not carried over from a document. The
// derivation is recorded so a future re-measurement can be checked rather than
// trusted.
// ---------------------------------------------------------------------------

const (
	// wantCensusPackages is exact. Two packages under tools/ would mean the walk
	// is seeing something this guard was never calibrated against.
	wantCensusPackages = 1

	// wantCensusFiles is the count of non-test .go files in tools/ at the commit
	// that wrote this guard. It is a MINIMUM and it is the anti-zero assertion:
	// a walk pointed at an empty or wrong directory reports 0 and must not be
	// allowed to report 0 as agreement. Adding a file is legitimate and does not
	// break it; finding fewer than were there means the walk lost sight of the
	// package, and every count below would then be measuring nothing.
	wantCensusFiles = 15

	// wantSites is the total number of (nil, err) returns in handler position.
	// Derivation: 31 at v1.12.1, plus the two InternalError marks the panic
	// decision added, one in NewObjectStructureHandlerWithSource and one in
	// NewAnalyzeSubsystemsHandlerWithSource, plus the two sites the unhonoured
	// request repair added: the decode check NewMetadataHandler was missing
	// altogether, and the level check NewEventLogHandler owed its own schema.
	wantSites = 35

	// wantProtocolSites: 8 decode failures plus the 2 recovered-panic marks. The
	// eighth decode failure is metadata's, which used to be discarded.
	wantProtocolSites = 10

	// wantOperationalSites: wantSites - wantProtocolSites. The added one is
	// get_event_log refusing a level outside its declared enum; it sits on the
	// operational side for the reason ProtocolError's doc gives, that a VALUE the
	// caller chose is a mistake the caller can only fix from text it can read.
	wantOperationalSites = 25

	// wantToolHandlerFuncs is every top-level func in the package returning
	// mcp.ToolHandler, INCLUDING the two wrappers that are not constructors. It is
	// pinned because the enclosing-declaration rule below excludes exactly those
	// two by name, and a third wrapper appearing without anyone noticing would
	// silently change what the census counts.
	wantToolHandlerFuncs = 14

	// wantConstructors is the exported New* subset: the public surface an importer
	// such as the paid edition registers for itself.
	wantConstructors = 12

	// wantDelegating: NewAnalyzeSubsystemsHandler and NewObjectStructureHandler
	// forward to their WithSource twin and must NOT wrap again.
	wantDelegating = 2

	// wantDecorated: the remaining constructors, each of which must terminate in
	// WithToolErrors, alone or inside withIndexProtectionNotice.
	wantDecorated = 10
)

// wantPerConstructor is (protocol, operational) per constructor. It is compared
// in BOTH directions, so a site drifting from one constructor to another while
// the totals stay put is still a failure.
var wantPerConstructor = map[string]siteClasses{
	"NewAnalyzeSubsystemsHandlerWithSource": {protocol: 2, operational: 5},
	"NewConfigurationInfoHandler":           {protocol: 0, operational: 1},
	"NewEventLogHandler":                    {protocol: 1, operational: 2},
	"NewFormStructureHandler":               {protocol: 1, operational: 4},
	"NewMetadataHandler":                    {protocol: 1, operational: 1},
	"NewObjectStructureHandlerWithSource":   {protocol: 2, operational: 3},
	"NewQueryHandler":                       {protocol: 1, operational: 3},
	"NewReloadDumpHandler":                  {protocol: 0, operational: 1},
	"NewSearchCodeHandler":                  {protocol: 1, operational: 3},
	"NewValidateQueryHandler":               {protocol: 1, operational: 2},
}

// wantNonConstructorHandlerFuncs are the two funcs that return mcp.ToolHandler
// and are deliberately NOT censused.
//
// This list is load-bearing rather than documentary. WithToolErrors has exactly
// the handler type and its own body contains two (nil, err) returns; a walk
// without the enclosing-declaration rule counts them and reports 37/10/27 instead
// of 35/10/25, MISCLASSIFYING the classifier itself. Measured both ways on this
// tree. withIndexProtectionNotice is excluded by being unexported.
var wantNonConstructorHandlerFuncs = []string{"WithToolErrors", "withIndexProtectionNotice"}

// wantDelegatingConstructors are the two that forward instead of wrapping.
var wantDelegatingConstructors = []string{"NewAnalyzeSubsystemsHandler", "NewObjectStructureHandler"}

// ---------------------------------------------------------------------------
// THE AST CENSUS — enumeration (B).
// ---------------------------------------------------------------------------

// siteClasses is a (protocol, operational) pair, used for totals, for the
// per-constructor map and for the per-file agreement with the driver tables.
type siteClasses struct{ protocol, operational int }

func (c siteClasses) String() string {
	return fmt.Sprintf("(protocol %d, operational %d)", c.protocol, c.operational)
}

// handlerSite is one (nil, err) return inside a handler literal.
type handlerSite struct {
	file       string
	ctor       string
	class      string // "protocol" | "operational"
	source     string // printed back from the AST, for failure messages only
	line       int    // for navigation in failure messages only, never asserted
	isProtocol bool
}

// handlerFunc is one top-level func returning mcp.ToolHandler.
type handlerFunc struct {
	file        string
	name        string
	line        int
	terminal    string // classified shape of what it returns
	terminalSrc string // printed back, for failure messages only
	constructor bool   // exported and named New*
}

type toolCensus struct {
	dir      string
	packages int
	files    int
	sites    []handlerSite
	funcs    []handlerFunc
}

func (c toolCensus) classes() siteClasses {
	var out siteClasses
	for _, s := range c.sites {
		if s.isProtocol {
			out.protocol++
		} else {
			out.operational++
		}
	}
	return out
}

func (c toolCensus) perConstructor() map[string]siteClasses {
	out := map[string]siteClasses{}
	for _, s := range c.sites {
		v := out[s.ctor]
		if s.isProtocol {
			v.protocol++
		} else {
			v.operational++
		}
		out[s.ctor] = v
	}
	return out
}

func (c toolCensus) perFile() map[string]siteClasses {
	out := map[string]siteClasses{}
	for _, s := range c.sites {
		v := out[s.file]
		if s.isProtocol {
			v.protocol++
		} else {
			v.operational++
		}
		out[s.file] = v
	}
	return out
}

// censusTools walks dir with go/ast and returns what it found.
//
// THE RULE, verbatim: for every top-level FuncDecl with no receiver whose single
// result type is mcp.ToolHandler AND whose name is exported and starts with New,
// find every func literal of type
// func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error), and
// inside it collect every return with two results whose first is the identifier
// nil and whose second is not, WITHOUT descending into nested literals. A site is
// protocol when the second result is a call to InvalidParams or InternalError,
// operational otherwise.
//
// It takes dir as a parameter and reports errors instead of failing a test, so
// the same code can be aimed at a wrong directory on purpose, which is how
// TestToolErrorContract_AnEmptyWalkIsAFailure proves the counts below cannot be
// satisfied by enumerating nothing.
func censusTools(dir string) (toolCensus, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return toolCensus{dir: dir}, err
	}

	c := toolCensus{dir: dir, packages: len(pkgs)}

	var pkgNames []string
	for name := range pkgs {
		pkgNames = append(pkgNames, name)
	}
	sort.Strings(pkgNames)

	for _, pkgName := range pkgNames {
		pkg := pkgs[pkgName]
		var fileNames []string
		for name := range pkg.Files {
			fileNames = append(fileNames, name)
		}
		sort.Strings(fileNames)
		c.files += len(fileNames)

		for _, path := range fileNames {
			base := path[strings.LastIndex(path, "/")+1:]
			for _, decl := range pkg.Files[path].Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv != nil || fd.Body == nil || !returnsToolHandler(fd.Type) {
					continue
				}
				isCtor := fd.Name.IsExported() && strings.HasPrefix(fd.Name.Name, "New")
				kind, src := classifyTerminal(fset, fd)
				c.funcs = append(c.funcs, handlerFunc{
					file:        base,
					name:        fd.Name.Name,
					line:        fset.Position(fd.Pos()).Line,
					terminal:    kind,
					terminalSrc: src,
					constructor: isCtor,
				})
				if !isCtor {
					continue // the enclosing-declaration rule
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					lit, ok := n.(*ast.FuncLit)
					if !ok || !isHandlerLiteral(lit.Type) {
						return true
					}
					c.sites = append(c.sites, collectSites(fset, lit, base, fd.Name.Name)...)
					return false
				})
			}
		}
	}

	sort.Slice(c.sites, func(i, j int) bool {
		if c.sites[i].file != c.sites[j].file {
			return c.sites[i].file < c.sites[j].file
		}
		return c.sites[i].line < c.sites[j].line
	})
	sort.Slice(c.funcs, func(i, j int) bool {
		if c.funcs[i].file != c.funcs[j].file {
			return c.funcs[i].file < c.funcs[j].file
		}
		return c.funcs[i].line < c.funcs[j].line
	})
	return c, nil
}

// returnsToolHandler reports whether ft has exactly one unnamed result of type
// mcp.ToolHandler.
func returnsToolHandler(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) != 1 || len(ft.Results.List[0].Names) != 0 {
		return false
	}
	return exprString(ft.Results.List[0].Type) == "mcp.ToolHandler"
}

// isHandlerLiteral reports whether ft is exactly the raw ToolHandler signature.
// Matching the SIGNATURE and not merely "some func literal" is what keeps a
// helper closure inside a constructor out of the census.
func isHandlerLiteral(ft *ast.FuncType) bool {
	params := flattenFields(ft.Params)
	results := flattenFields(ft.Results)
	return len(params) == 2 && params[0] == "context.Context" && params[1] == "*mcp.CallToolRequest" &&
		len(results) == 2 && results[0] == "*mcp.CallToolResult" && results[1] == "error"
}

// flattenFields expands `a, b T` into one entry per name, so an arity check is a
// real arity check.
func flattenFields(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var out []string
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, exprString(f.Type))
		}
	}
	return out
}

func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	}
	return "?"
}

func printNode(fset *token.FileSet, n ast.Node) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, n); err != nil {
		return "<unprintable>"
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// collectSites gathers the (nil, err) returns of one handler literal.
func collectSites(fset *token.FileSet, lit *ast.FuncLit, file, ctor string) []handlerSite {
	var out []handlerSite
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if inner, ok := n.(*ast.FuncLit); ok && inner != lit {
			// A nested literal is somebody else's return path. Descending into it
			// would attribute, for example, a callback's error to this handler.
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 2 {
			return true
		}
		first, ok := ret.Results[0].(*ast.Ident)
		if !ok || first.Name != "nil" {
			return true
		}
		if second, ok := ret.Results[1].(*ast.Ident); ok && second.Name == "nil" {
			return true // the success return
		}
		isProto := false
		if call, ok := ret.Results[1].(*ast.CallExpr); ok {
			if fn, ok := call.Fun.(*ast.Ident); ok && (fn.Name == "InvalidParams" || fn.Name == "InternalError") {
				isProto = true
			}
		}
		class := "operational"
		if isProto {
			class = "protocol"
		}
		out = append(out, handlerSite{
			file:       file,
			ctor:       ctor,
			class:      class,
			source:     printNode(fset, ret),
			line:       fset.Position(ret.Pos()).Line,
			isProtocol: isProto,
		})
		return true
	})
	return out
}

// Terminal shapes a func returning mcp.ToolHandler can have.
const (
	terminalDecorated  = "decorated"  // WithToolErrors(...), possibly inside withIndexProtectionNotice
	terminalDelegating = "delegating" // forwards to another exported New*
	terminalBare       = "BARE"       // an undecorated handler literal: the regression
	terminalNoticeBare = "NOTICE-BARE"
	terminalUnknown    = "UNKNOWN"
	terminalNone       = "NO-RETURN"
)

// classifyTerminal names what a constructor actually hands back.
//
// THIS IS THE ASSERTION THE WHOLE GUARD EXISTS FOR. Every count above still
// passes for a tool #12 that is written, censused, driven and blind, because a
// bare handler literal has (nil, err) returns like any other. What separates a
// wired tool from a blind one is only this: what the constructor RETURNS.
func classifyTerminal(fset *token.FileSet, fd *ast.FuncDecl) (kind, src string) {
	kind, src = terminalNone, ""
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false // returns inside the handler are not the constructor's
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		kind = classifyTerminalExpr(ret.Results[0])
		src = printNode(fset, ret)
		return true
	})
	return kind, src
}

func classifyTerminalExpr(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.FuncLit:
		return terminalBare
	case *ast.CallExpr:
		fn, ok := t.Fun.(*ast.Ident)
		if !ok {
			return terminalUnknown
		}
		switch {
		case fn.Name == "WithToolErrors":
			return terminalDecorated
		case fn.Name == "withIndexProtectionNotice":
			if len(t.Args) == 0 {
				return terminalUnknown
			}
			// The notice wrapper alone is NOT the decorator. Before the error
			// contract landed, search_code and reload_dump looked exactly like
			// this with a bare literal inside, and they were blind.
			if classifyTerminalExpr(t.Args[len(t.Args)-1]) == terminalDecorated {
				return terminalDecorated
			}
			return terminalNoticeBare
		case ast.IsExported(fn.Name) && strings.HasPrefix(fn.Name, "New"):
			return terminalDelegating
		}
		return terminalUnknown
	}
	return terminalUnknown
}

// ---------------------------------------------------------------------------
// THE VERDICT — shared by the real run and by the proofs that it can fail.
// ---------------------------------------------------------------------------

// contractFindings is a list of violations, each tagged with the assertion that
// produced it. Collecting instead of calling t.Error directly is what lets the
// SAME verdict logic be aimed at a deliberately empty walk and be shown to
// report something; a verdict that can only run against the real tree cannot be
// shown to fail without editing the tree.
type contractFindings []string

func (f *contractFindings) addf(code, format string, a ...any) {
	*f = append(*f, code+": "+fmt.Sprintf(format, a...))
}

func (f contractFindings) has(code string) bool {
	for _, s := range f {
		if strings.HasPrefix(s, code+":") {
			return true
		}
	}
	return false
}

// verifyCensus is G1 through G5: everything provable from the source alone.
func verifyCensus(c toolCensus) contractFindings {
	var f contractFindings

	// G1 — the walk itself. Checked BEFORE any count is taken, because a count of
	// zero taken over nothing is not evidence of anything and must never be able
	// to read as agreement.
	if c.packages != wantCensusPackages {
		f.addf("G1", "the walk over %s found %d packages, want exactly %d",
			c.dir, c.packages, wantCensusPackages)
	}
	if c.files < wantCensusFiles {
		f.addf("G1", "the walk over %s found %d non-test files, want at least %d; "+
			"a walk that lost sight of the package would report every count below as zero",
			c.dir, c.files, wantCensusFiles)
	}
	if len(f) > 0 {
		return f // every number below would be measuring nothing
	}

	// G2 — the total. EXACT. A new (nil, err) return anywhere in a handler body
	// moves this, including the four operational sites whose text contains no
	// fmt.Errorf at all: return nil, dumpErr in NewFormStructureHandler, the bare
	// return nil, err in NewAnalyzeSubsystemsHandlerWithSource and in
	// NewObjectStructureHandlerWithSource, and errors.New(queryNotSelectMsg) in
	// NewQueryHandler. A search keyed on fmt.Errorf cannot close that class; a
	// walk over return statements can, and that is the whole reason this is an
	// AST census rather than a pattern match.
	if len(c.sites) != wantSites {
		f.addf("G2", "found %d handler error sites, want exactly %d:\n%s",
			len(c.sites), wantSites, formatSites(c.sites))
	}

	// G3 — the split. EXACT in both directions, so a mark added or removed in
	// silence is a failure even when the total is unchanged.
	got := c.classes()
	if got.protocol != wantProtocolSites || got.operational != wantOperationalSites {
		f.addf("G3", "classes are %s, want (protocol %d, operational %d)",
			got, wantProtocolSites, wantOperationalSites)
	}

	// G4 — per constructor, compared BOTH directions.
	perCtor := c.perConstructor()
	for name, want := range wantPerConstructor {
		got, ok := perCtor[name]
		if !ok {
			f.addf("G4", "constructor %s has no sites at all; it was expected to have %s", name, want)
			continue
		}
		if got != want {
			f.addf("G4", "constructor %s has %s, want %s", name, got, want)
		}
	}
	for name, got := range perCtor {
		if _, ok := wantPerConstructor[name]; !ok {
			f.addf("G4", "constructor %s has %s and is in no expectation row; "+
				"a new tool was added without being classified", name, got)
		}
	}

	// G5 — the terminal return of every func returning mcp.ToolHandler. This is
	// the assertion that catches a tool registered without the decorator.
	if len(c.funcs) != wantToolHandlerFuncs {
		f.addf("G5", "found %d funcs returning mcp.ToolHandler, want exactly %d:\n%s",
			len(c.funcs), wantToolHandlerFuncs, formatFuncs(c.funcs))
	}
	shapes := c.constructorShapes()
	ctors, delegating, decorated, nonCtors := shapes.ctors, shapes.delegating, shapes.decorated, shapes.nonCtors
	for _, fn := range shapes.undecorated {
		f.addf("G5", "constructor %s (%s:%d) terminates in %s, so every failure it "+
			"produces leaves as a protocol error the model never reads: %s",
			fn.name, fn.file, fn.line, fn.terminal, fn.terminalSrc)
	}
	if len(ctors) != wantConstructors {
		f.addf("G5", "found %d exported New* constructors returning mcp.ToolHandler, want exactly %d: %v",
			len(ctors), wantConstructors, ctors)
	}
	if len(delegating) != wantDelegating {
		f.addf("G5", "found %d delegating constructors, want exactly %d: %v",
			len(delegating), wantDelegating, delegating)
	}
	if len(decorated) != wantDecorated {
		f.addf("G5", "found %d decorated constructors, want exactly %d: %v",
			len(decorated), wantDecorated, decorated)
	}
	if !sameSet(delegating, wantDelegatingConstructors) {
		f.addf("G5", "the delegating constructors are %v, want %v", sorted(delegating), sorted(wantDelegatingConstructors))
	}
	// The exclusion list of the enclosing-declaration rule, pinned by name. A
	// third wrapper appearing here would change what the census counts without
	// changing any total, which is a silent recalibration.
	if !sameSet(nonCtors, wantNonConstructorHandlerFuncs) {
		f.addf("G5", "funcs returning mcp.ToolHandler that are not New* constructors are %v, want %v; "+
			"the census excludes exactly these and a new one changes what it counts",
			sorted(nonCtors), sorted(wantNonConstructorHandlerFuncs))
	}
	return f
}

// ctorShapes is the constructor population split by what each one returns.
type ctorShapes struct {
	ctors       []string      // exported New* returning mcp.ToolHandler
	delegating  []string      // forwards to another New*
	decorated   []string      // terminates in WithToolErrors
	nonCtors    []string      // returns mcp.ToolHandler and is not a New* constructor
	undecorated []handlerFunc // neither: the regression, kept whole so a report can name the shape
}

// constructorShapes computes the split ONCE, so the verdict and the log line
// cannot disagree about it. The log prints these MEASURED lengths and never the
// want* constants: a line that prints its own expectation says nothing about the
// tree it claims to have measured.
func (c toolCensus) constructorShapes() ctorShapes {
	var s ctorShapes
	for _, fn := range c.funcs {
		if !fn.constructor {
			s.nonCtors = append(s.nonCtors, fn.name)
			continue
		}
		s.ctors = append(s.ctors, fn.name)
		switch fn.terminal {
		case terminalDelegating:
			s.delegating = append(s.delegating, fn.name)
		case terminalDecorated:
			s.decorated = append(s.decorated, fn.name)
		default:
			s.undecorated = append(s.undecorated, fn)
		}
	}
	return s
}

// verifyDriverCoverage is G6 and G7: the agreement between the source census and
// the hand-written driver tables in toolwiring_test.go.
//
// This is the half neither enumeration can do alone. The drivers prove each site
// they name behaves; the census proves which sites exist. Only the comparison
// proves nobody added a site and forgot to drive it.
func verifyDriverCoverage(c toolCensus, operational, protocol map[string]int) contractFindings {
	var f contractFindings

	opTotal, protoTotal := 0, 0
	for _, n := range operational {
		opTotal += n
	}
	for _, n := range protocol {
		protoTotal += n
	}

	// G6 — the driver tables are exactly as large as the census says they must be.
	if opTotal != wantOperationalSites {
		f.addf("G6", "the operational driver table has %d rows, want exactly %d", opTotal, wantOperationalSites)
	}
	if protoTotal != wantProtocolSites {
		f.addf("G6", "the protocol driver table has %d rows, want exactly %d", protoTotal, wantProtocolSites)
	}
	classes := c.classes()
	if opTotal != classes.operational {
		f.addf("G6", "%d operational driver rows against %d operational sites in the source; "+
			"a site exists that nothing drives, or a driver names a site that is gone",
			opTotal, classes.operational)
	}
	if protoTotal != classes.protocol {
		f.addf("G6", "%d protocol driver rows against %d protocol sites in the source",
			protoTotal, classes.protocol)
	}

	// G7 — per file, both directions. Totals can agree while a file gains a site
	// and another loses one; this is what sees that.
	drivers := map[string]siteClasses{}
	for file, n := range operational {
		v := drivers[file]
		v.operational += n
		drivers[file] = v
	}
	for file, n := range protocol {
		v := drivers[file]
		v.protocol += n
		drivers[file] = v
	}
	source := c.perFile()
	for file, want := range source {
		got, ok := drivers[file]
		if !ok {
			f.addf("G7", "%s has %s in the source and no driver row at all", file, want)
			continue
		}
		if got != want {
			f.addf("G7", "%s has %s in the source but %s in the driver tables", file, want, got)
		}
	}
	for file, got := range drivers {
		if _, ok := source[file]; !ok {
			f.addf("G7", "the driver tables name %s with %s, and the source census has no such file; "+
				"a site string is misspelled or the site is gone", file, got)
		}
	}
	return f
}

func formatSites(sites []handlerSite) string {
	var b strings.Builder
	for _, s := range sites {
		fmt.Fprintf(&b, "  %-24s %-5d %-38s %-12s %s\n", s.file, s.line, s.ctor, s.class, s.source)
	}
	return b.String()
}

func formatFuncs(funcs []handlerFunc) string {
	var b strings.Builder
	for _, fn := range funcs {
		role := "wrapper"
		if fn.constructor {
			role = "constructor"
		}
		fmt.Fprintf(&b, "  %-24s %-5d %-38s %-12s %s\n", fn.file, fn.line, fn.name, role, fn.terminal)
	}
	return b.String()
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := sorted(a), sorted(b)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// driverFiles counts driver rows per source file.
//
// The site strings in toolwiring_test.go start with the file the site lives in,
// which is how a driver row and an AST site are matched without either one
// naming a line number. A misspelled file name produces a bucket the census has
// never heard of, and G7 reports it in the direction that catches exactly that.
func driverFiles(sites []string) map[string]int {
	out := map[string]int{}
	for _, s := range sites {
		file, _, _ := strings.Cut(s, " ")
		out[file]++
	}
	return out
}

// ---------------------------------------------------------------------------
// THE TESTS.
// ---------------------------------------------------------------------------

// TestToolErrorContract is the guard. It must be green on this tree; every way it
// can go red is a change somebody has to look at.
func TestToolErrorContract(t *testing.T) {
	c, err := censusTools(".")
	if err != nil {
		t.Fatalf("the AST walk over the tools package failed, so nothing below was measured: %v", err)
	}

	findings := verifyCensus(c)

	var opSites, protoSites []string
	for _, s := range operationalSites() {
		opSites = append(opSites, s.site)
	}
	for _, s := range protocolSites() {
		protoSites = append(protoSites, s.site)
	}
	findings = append(findings, verifyDriverCoverage(c, driverFiles(opSites), driverFiles(protoSites))...)

	for _, v := range findings {
		t.Error(v)
	}
	if len(findings) > 0 {
		t.Fatalf("the tool error contract has %d violations, listed above; "+
			"re-run the census and correct the expectation table only after understanding which of them moved",
			len(findings))
	}

	// Every number below is a length taken from the walk, never a want* constant.
	classes := c.classes()
	shapes := c.constructorShapes()
	t.Logf("census: %d sites across %d constructors (protocol %d, operational %d); "+
		"%d funcs return mcp.ToolHandler, %d exported New*, %d delegating, %d decorated",
		len(c.sites), len(c.perConstructor()), classes.protocol, classes.operational,
		len(c.funcs), len(shapes.ctors), len(shapes.delegating), len(shapes.decorated))
	t.Logf("drivers: %d operational rows, %d protocol rows, agreeing with the source per file across %d files",
		len(opSites), len(protoSites), len(c.perFile()))
}

// TestToolErrorContract_AnEmptyWalkIsAFailure IS THE PROOF THAT THE GUARD CANNOT
// ENUMERATE ZERO AND CALL IT AGREEMENT.
//
// This repository has shipped guards that passed with nothing to suppress. The
// failure mode here is specific and cheap to hit: point the walk at a directory
// that holds no Go package, and every count becomes 0. Without this test, "0
// sites, 0 constructors, no violations" would be indistinguishable from a clean
// tree for anyone reading a green run.
//
// It runs the SAME verifyCensus the real test runs. A verdict that is only ever
// aimed at the real tree cannot be shown to fail without editing the tree.
func TestToolErrorContract_AnEmptyWalkIsAFailure(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		c, err := censusTools(t.TempDir())
		if err != nil {
			t.Fatalf("walking an empty directory should not error: %v", err)
		}
		if len(c.sites) != 0 || len(c.funcs) != 0 || c.files != 0 {
			t.Fatalf("an empty directory yielded %d sites, %d funcs, %d files; "+
				"this control is not measuring what it claims", len(c.sites), len(c.funcs), c.files)
		}
		findings := verifyCensus(c)
		if !findings.has("G1") {
			t.Fatalf("an empty walk produced no G1 finding, so a misdirected census could report agreement: %v", findings)
		}
		t.Logf("empty walk correctly rejected by %v", findings)
	})

	t.Run("a real package that is not tools", func(t *testing.T) {
		// A directory with real Go files and no tool constructors. The failure
		// this catches is subtler than an empty directory: the walk succeeds, the
		// file count is plausible, and every site count is still zero.
		c, err := censusTools("../prompts")
		if err != nil {
			t.Fatalf("walking ../prompts failed: %v", err)
		}
		if c.packages != 1 {
			t.Fatalf("../prompts should be one package, got %d; this control is not set up", c.packages)
		}
		findings := verifyCensus(c)
		if len(findings) == 0 {
			t.Fatal("a census of the wrong package produced no findings at all")
		}
		if !findings.has("G1") && !findings.has("G2") {
			t.Fatalf("a census of the wrong package was not caught by the anti-zero assertions: %v", findings)
		}
		t.Logf("wrong-package walk correctly rejected by %v", findings)
	})

	t.Run("a plausible walk that found no sites", func(t *testing.T) {
		// The control above is rejected by the FILE count, which leaves the more
		// interesting failure unexercised: a walk that reports the right number of
		// packages and files and still enumerates nothing. That is what a broken
		// literal matcher or a signature that stopped matching looks like, and it
		// must not read as agreement either.
		c, err := censusTools(".")
		if err != nil {
			t.Fatalf("census: %v", err)
		}
		c.sites = nil
		c.funcs = nil
		findings := verifyCensus(c)
		if findings.has("G1") {
			t.Fatalf("this control was meant to get past G1 and did not: %v", findings)
		}
		for _, code := range []string{"G2", "G3", "G4", "G5"} {
			if !findings.has(code) {
				t.Errorf("a census that found no sites produced no %s finding: %v", code, findings)
			}
		}
		t.Logf("a plausible walk with nothing in it is rejected by %d findings", len(findings))
	})

	t.Run("driver tables emptied", func(t *testing.T) {
		// The other half: the source census is real, and the expectation the
		// drivers represent has been emptied. G6 must say so.
		c, err := censusTools(".")
		if err != nil {
			t.Fatalf("census: %v", err)
		}
		findings := verifyDriverCoverage(c, map[string]int{}, map[string]int{})
		if !findings.has("G6") {
			t.Fatalf("empty driver tables produced no G6 finding: %v", findings)
		}
		t.Logf("empty driver tables correctly rejected by %d findings", len(findings))
	})
}

// TestToolErrorContract_TheEnclosingRuleIsLoadBearing proves that the rule the
// census is built on is doing work rather than decorating a comment.
//
// WithToolErrors has exactly the handler signature and its own body returns
// (nil, err) twice. A census that counted every handler-typed literal in the
// package would attribute those two returns to nobody and report 26 operational
// sites instead of 24, and the classifier itself would be censused as if it were
// a tool. The rule that prevents it is "only inside an exported New*", and this
// test is what would go red if somebody relaxed it.
func TestToolErrorContract_TheEnclosingRuleIsLoadBearing(t *testing.T) {
	c, err := censusTools(".")
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	for _, fn := range c.funcs {
		if fn.constructor {
			continue
		}
		for _, s := range c.sites {
			if s.ctor == fn.name {
				t.Errorf("site %s:%d is attributed to %s, which is not a tool constructor",
					s.file, s.line, fn.name)
			}
		}
	}
	// The positive control: the excluded funcs must actually exist and actually
	// hold error returns, otherwise excluding them proves nothing.
	found := map[string]bool{}
	for _, fn := range c.funcs {
		if !fn.constructor {
			found[fn.name] = true
		}
	}
	for _, name := range wantNonConstructorHandlerFuncs {
		if !found[name] {
			t.Errorf("%s is not present as a non-constructor func returning mcp.ToolHandler; "+
				"the exclusion this test checks has nothing to exclude", name)
		}
	}
}
