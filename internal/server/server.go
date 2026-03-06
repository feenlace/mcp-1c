package server

import (
	"github.com/feenlace/mcp-1c/internal/onec"
	"github.com/feenlace/mcp-1c/internal/prompts"
	"github.com/feenlace/mcp-1c/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New creates an MCP server with basic configuration and registers tools.
func New(onecClient *onec.Client) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{
			Name:    "mcp-1c",
			Version: "0.3.0-beta",
		},
		nil,
	)
	s.AddTool(tools.MetadataTool(), tools.NewMetadataHandler(onecClient))
	s.AddTool(tools.ObjectStructureTool(), tools.NewObjectStructureHandler(onecClient))
	s.AddTool(tools.ModuleCodeTool(), tools.NewModuleCodeHandler(onecClient))
	s.AddTool(tools.QueryTool(), tools.NewQueryHandler(onecClient))
	tools.RegisterBSLHelp(s)
	prompts.RegisterAll(s)
	return s
}
