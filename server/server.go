package server

import (
	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/feenlace/mcp-1c/prompts"
	"github.com/feenlace/mcp-1c/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New creates an MCP server with basic configuration and registers tools.
// If dumpIndex is provided, the search_code tool will be registered.
func New(version string, onecClient *onec.Client, dumpIndex *dump.Index) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{
			Name:    "mcp-1c",
			Version: version,
		},
		nil,
	)
	// dumpDir is the offline configuration dump directory, when one is present.
	// Several tools can answer from the dump instead of the live 1C extension; an
	// empty dumpDir selects the live path (the offline source constructors return
	// nil, making the WithSource handlers byte-identical to the live-only ones).
	var dumpDir string
	if dumpIndex != nil {
		dumpDir = dumpIndex.Dir()
	}

	s.AddTool(tools.MetadataTool(), tools.NewMetadataHandler(onecClient))
	// object_structure serves object_type=Subsystem from the dump when present and
	// falls through to live HTTP for every other type (nil source -> fully live).
	s.AddTool(tools.ObjectStructureTool(), tools.NewObjectStructureHandlerWithSource(onecClient, tools.DumpObjectStructFunc(dumpDir)))
	s.AddTool(tools.QueryTool(), tools.NewQueryHandler(onecClient))
	if dumpIndex != nil {
		s.AddTool(tools.SearchCodeTool(), tools.NewSearchCodeHandler(dumpIndex))
	}

	// Pass dump directory to form handler so it can enrich the HTTP response
	// with data from Form.xml files parsed from the dump.
	s.AddTool(tools.FormStructureTool(), tools.NewFormStructureHandler(onecClient, dumpDir))

	s.AddTool(tools.ValidateQueryTool(), tools.NewValidateQueryHandler(onecClient))
	s.AddTool(tools.EventLogTool(), tools.NewEventLogHandler(onecClient))
	s.AddTool(tools.ConfigurationInfoTool(), tools.NewConfigurationInfoHandler(onecClient))
	// analyze_subsystems answers from the dump when present (nil source -> live).
	s.AddTool(tools.AnalyzeSubsystemsTool(), tools.NewAnalyzeSubsystemsHandlerWithSource(onecClient, tools.DumpSubsystemForestFunc(dumpDir)))
	tools.RegisterBSLHelp(s)
	prompts.RegisterAll(s)
	return s
}
