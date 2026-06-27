package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ConfigurationInfoTool returns the MCP tool definition for get_configuration_info.
func ConfigurationInfoTool() *mcp.Tool {
	return &mcp.Tool{
		Name:  "get_configuration_info",
		Title: "Информация о конфигурации",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Получить общую информацию о базе 1С: название конфигурации, версия, поставщик, платформа, режим работы. " +
			"Используй первым делом чтобы понять с какой конфигурацией работаешь.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

// NewConfigurationInfoHandler returns a ToolHandler that fetches configuration info from 1C.
func NewConfigurationInfoHandler(client *onec.Client) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var info onec.ConfigurationInfo
		if err := client.Get(ctx, "/configuration", &info); err != nil {
			return nil, fmt.Errorf("fetching configuration info from 1C: %w", err)
		}

		return textResult(formatConfigurationInfo(&info)), nil
	}
}

func formatConfigurationInfo(info *onec.ConfigurationInfo) string {
	var b strings.Builder
	b.WriteString("# Информация о конфигурации 1С\n\n")

	b.WriteString("| Параметр | Значение |\n")
	b.WriteString("|----------|----------|\n")

	writeRow := func(key, value string) {
		if value != "" {
			fmt.Fprintf(&b, "| %s | %s |\n", key, value)
		}
	}

	writeRow("Конфигурация", info.Name)
	writeRow("Версия", info.Version)
	writeRow("Поставщик", info.Vendor)
	writeRow("Платформа", info.PlatformVersion)

	mode := info.Mode
	switch mode {
	case "file":
		mode = "Файловый"
	case "server":
		mode = "Клиент-серверный"
	}
	writeRow("Режим работы", mode)

	return b.String()
}
