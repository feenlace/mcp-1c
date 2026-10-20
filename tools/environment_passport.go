package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net"
	"regexp"
	"strings"
	"unicode"
)

type PassportMode string

const (
	ModeLive    PassportMode = "live"
	ModeOffline PassportMode = "offline"
)

type PassportContext struct {
	Mode                                           PassportMode
	ProductVersion, Edition, Transport, SourceRoot string
}
type PassportFieldStatus struct {
	Source     string `json:"source"`
	Status     string `json:"status"`
	ActionCode string `json:"action_code,omitempty"`
}
type EnvironmentPassport struct {
	ProductVersion       string              `json:"product_version,omitempty"`
	Edition              string              `json:"edition,omitempty"`
	Transport            string              `json:"transport,omitempty"`
	ConfigurationName    string              `json:"configuration_name,omitempty"`
	ConfigurationVersion string              `json:"configuration_version,omitempty"`
	Vendor               string              `json:"vendor,omitempty"`
	PlatformVersion      string              `json:"platform_version,omitempty"`
	DatabaseMode         string              `json:"database_mode,omitempty"`
	ExtensionVersion     string              `json:"extension_version,omitempty"`
	ConfigurationStatus  PassportFieldStatus `json:"configuration_status"`
	VersionStatus        PassportFieldStatus `json:"version_status"`
}

var passportVersion = regexp.MustCompile(`^[vV]?([0-9]+(\.[0-9]+){1,3})([+-][a-zA-Z0-9.-]+)?$`)

func safePassportVersion(s string) string {
	if len(s) > 80 {
		return ""
	}
	match := passportVersion.FindStringSubmatch(s)
	if match == nil {
		return ""
	}
	// Opaque version suffixes can contain host names or tokens; publish only numeric components.
	return match[1]
}
func passportText(s string) string {
	if len(s) > 256 || strings.ContainsAny(s, "\r\n\t\\/@:=") {
		return ""
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return s
}
func passportStatus(source, status string) PassportFieldStatus {
	action := ""
	switch status {
	case "denied":
		action = "check_current_read_rights"
	case "unsupported":
		action = "update_extension"
	case "timeout":
		action = "retry_with_connection_available"
	case "cancelled":
		action = "retry_if_needed"
	case "unavailable":
		action = "check_declared_source_or_connection"
	}
	return PassportFieldStatus{source, status, action}
}
func classifyPassportError(source string, err error) PassportFieldStatus {
	status := "unavailable"
	switch {
	case errors.Is(err, context.Canceled):
		status = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		status = "timeout"
	default:
		var se *onec.StatusError
		var ne net.Error
		if errors.As(err, &se) {
			switch se.StatusCode {
			case 401, 403:
				status = "denied"
			case 404, 405, 501:
				status = "unsupported"
			case 408, 504:
				status = "timeout"
			}
		} else if errors.As(err, &ne) && ne.Timeout() {
			status = "timeout"
		}
	}
	return passportStatus(source, status)
}

// ReadEnvironmentPassport discards upstream errors before returning. Its error chain never retains a URL, body or credentials.
// Offline mode performs zero client calls, even when a client is supplied.
func ReadEnvironmentPassport(ctx context.Context, client *onec.Client, pc PassportContext) (EnvironmentPassport, error) {
	p := EnvironmentPassport{ProductVersion: safePassportVersion(pc.ProductVersion), ConfigurationStatus: passportStatus("live_configuration", "unavailable"), VersionStatus: passportStatus("live_extension", "unavailable")}
	switch pc.Edition {
	case "Community", "Advanced", "Professional", "Pro":
		p.Edition = pc.Edition
	}
	switch pc.Transport {
	case "http", "polling", "proxy", "offline":
		p.Transport = pc.Transport
	}
	if pc.Mode == ModeOffline {
		p.Transport = "offline"
		p.ConfigurationStatus = passportStatus("dump_xml", "unavailable")
		p.VersionStatus = passportStatus("offline", "unavailable")
		summary, err := dump.ReadMetadataSummary(ctx, pc.SourceRoot, dump.MetadataSelection{Kind: "configuration"})
		if err != nil {
			p.ConfigurationStatus = classifyPassportError("dump_xml", err)
			return p, nil
		}
		p.ConfigurationName = passportText(summary.Name)
		p.ConfigurationVersion = safePassportVersion(summary.Version)
		p.Vendor = passportText(summary.Vendor)
		p.ConfigurationStatus = passportStatus("dump_xml", "available")
		return p, nil
	}
	if pc.Mode != ModeLive {
		return p, errors.New("passport mode must be live or offline")
	}
	if client == nil {
		return p, nil
	}
	var conf onec.ConfigurationInfo
	if err := client.Get(ctx, "/configuration", &conf); err != nil {
		p.ConfigurationStatus = classifyPassportError("live_configuration", err)
	} else {
		p.ConfigurationName = passportText(conf.Name)
		p.ConfigurationVersion = safePassportVersion(conf.Version)
		p.Vendor = passportText(conf.Vendor)
		p.PlatformVersion = safePassportVersion(conf.PlatformVersion)
		if conf.Mode == "file" || conf.Mode == "server" {
			p.DatabaseMode = conf.Mode
		}
		if p.ConfigurationName != "" {
			p.ConfigurationStatus = passportStatus("live_configuration", "available")
		}
	}
	var ver onec.VersionInfo
	if err := client.Get(ctx, "/version", &ver); err != nil {
		p.VersionStatus = classifyPassportError("live_extension", err)
	} else {
		p.ExtensionVersion = safePassportVersion(ver.Version)
		if p.ExtensionVersion != "" {
			p.VersionStatus = passportStatus("live_extension", "available")
		}
	}
	return p, nil
}
func EnvironmentPassportTool() *mcp.Tool {
	return &mcp.Tool{Name: "environment_passport", Title: "Безопасный паспорт среды", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, Description: "Краткий паспорт выбранной среды: конфигурация, версия платформы и расширения, редакция и явно объявленный транспорт. Без адресов, учётных данных, лицензий и сведений о машине. Каждый источник имеет отдельный статус; offline читает только XML.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`)}
}
func NewEnvironmentPassportHandler(client *onec.Client, pc PassportContext) mcp.ToolHandler {
	return WithToolErrors("Паспорт среды не получен", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, err := ReadEnvironmentPassport(ctx, client, pc)
		if err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(p, "", "  ")
		return textResult("# Паспорт среды\n\nНезаполненные значения недоступны. Статус относится к указанному источнику.\n\n```json\n" + string(data) + "\n```"), nil
	})
}
