package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/nativecheck"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HelpCheckConfig is explicit operator authority. Model input cannot select a database or installed executable.
type HelpCheckConfig struct {
	PlatformExecutable string
	TargetFileDatabase string
	OutputRoot         string
	User, Password     string
}
type HelpAddRequest struct {
	dump.HelpPatchRequest
	Mode         string `json:"mode"`
	ArtifactPath string `json:"artifact_path,omitempty"`
}
type HelpAddResult struct {
	Patch dump.HelpPatchResult            `json:"patch"`
	Check nativecheck.ExternalCheckResult `json:"check"`
}

func HelpAddTool() *mcp.Tool {
	return &mcp.Tool{Name: "help_add", Title: "Справка внешнего отчёта или обработки", Description: "Добавить безопасную справку ru к выбранному ExternalReport/ExternalDataProcessor в известном hierarchical2.20 XML-формате. Основной XML не меняется. manifest_path, expected_source_sha256 и language=ru обязательны; ровно одно content (экранируемый текст) или html (корректный XML-фрагмент из форматирующих тегов без атрибутов). До256KiB. overwrite явно разрешает замену существующей справки с backup. mode=patch работает без1С; patch_and_check требует явно настроенных оператором платформы, synthetic file DB и output root. Проверка только собирает доверенный source snapshot; не исполняет BSL/объект и не проверяет GUI. UIOpenStatus всегда NOT_RUN.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"manifest_path":{"type":"string"},"expected_source_sha256":{"type":"string"},"language":{"type":"string","enum":["ru"]},"content":{"type":"string"},"html":{"type":"string"},"overwrite":{"type":"boolean"},"mode":{"type":"string","enum":["patch","patch_and_check"]},"artifact_path":{"type":"string"}},"required":["manifest_path","expected_source_sha256","language","mode"]}`)}
}
func NewHelpAddHandler(root string, config *HelpCheckConfig) mcp.ToolHandler {
	return WithToolErrors("Добавление справки не выполнено", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input HelpAddRequest
		if err := localDecode(req.Params.Arguments, &input); err != nil {
			return nil, InvalidParams(argumentDecodeError(err))
		}
		if input.Mode != "patch" && input.Mode != "patch_and_check" {
			return nil, errors.New("mode must be patch or patch_and_check")
		}
		if input.Mode == "patch_and_check" && input.ArtifactPath == "" {
			return nil, errors.New("patch_and_check requires a selected new artifact_path")
		}
		patch, err := dump.PatchHelp(ctx, root, input.HelpPatchRequest)
		result := HelpAddResult{Patch: patch, Check: nativecheck.ExternalCheckResult{NativeCheckStatus: "NOT_RUN", UIOpenStatus: "NOT_RUN", Stage: "source_patch", ReasonCode: "source_only"}}
		failed := err != nil
		if err != nil && patch.Status != "partial" {
			return nil, err
		}
		if !failed && input.Mode == "patch_and_check" {
			if config == nil {
				result.Check.NativeCheckStatus = "unavailable"
				result.Check.ReasonCode = "configure_explicit_platform_file_fixture_output_root"
				result.Check.Warnings = []string{"Set all operator options --native-check-platform, --native-check-file-db and --native-check-output-root, then retry using the current trusted source digest and a new artifact_path."}
				failed = true
			} else {
				result.Check, err = nativecheck.CheckExternal(ctx, nativecheck.ExternalCheckRequest{PlatformExecutable: config.PlatformExecutable, SourceRoot: root, ManifestPath: input.ManifestPath, SourceTreeSHA256: patch.Source.SourceTreeSHA256, Kind: patch.Source.Kind, TargetFileDatabase: config.TargetFileDatabase, OutputRoot: config.OutputRoot, ArtifactPath: input.ArtifactPath, User: config.User, Password: config.Password})
				failed = err != nil
			}
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		text := string(data)
		if failed {
			text = "Добавление справки не выполнено\n\nРезультат изменения исходников и отдельной проверки:\n\n" + text
		}
		out := textResult(text)
		out.IsError = failed
		return out, nil
	})
}
