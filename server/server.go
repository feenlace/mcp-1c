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
	s.AddTool(tools.MetadataTool(), tools.NewMetadataHandler(onecClient))
	s.AddTool(tools.ObjectStructureTool(), tools.NewObjectStructureHandler(onecClient))
	s.AddTool(tools.QueryTool(), tools.NewQueryHandler(onecClient))
	if dumpIndex != nil {
		s.AddTool(tools.SearchCodeTool(), tools.NewSearchCodeHandler(dumpIndex))
	}

	// Pass dump directory to form handler so it can enrich the HTTP response
	// with data from Form.xml files parsed from the dump.
	var dumpDir string
	if dumpIndex != nil {
		dumpDir = dumpIndex.Dir()
	}
	s.AddTool(tools.FormStructureTool(), tools.NewFormStructureHandler(onecClient, dumpDir))

	s.AddTool(tools.ValidateQueryTool(), tools.NewValidateQueryHandler(onecClient))
	s.AddTool(tools.EventLogTool(), tools.NewEventLogHandler(onecClient))
	s.AddTool(tools.ConfigurationInfoTool(), tools.NewConfigurationInfoHandler(onecClient))
	tools.RegisterBSLHelp(s)
	prompts.RegisterAll(s)
	return s
}
