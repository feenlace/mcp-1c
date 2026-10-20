package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RecordCountTool() *mcp.Tool {
	return &mcp.Tool{Name: "count_records", Title: "Количество доступных записей", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, Description: "Посчитать записи выбранного Catalog или Document, доступные текущему пользователю. Не выгружает данные. Период [start,end) задаётся точными датами YYYY-MM-DDTHH:MM:SS в гражданском времени базы; организация задаётся типизированной ссылкой справочника. Произвольные запросы, поля-выражения и расширенная платная статистика не поддерживаются.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"object_type":{"type":"string","enum":["Catalog","Document"]},"object_name":{"type":"string"},"period":{"type":"object","additionalProperties":false,"properties":{"field":{"type":"string"},"start":{"type":"string"},"end":{"type":"string"}},"required":["field","start","end"]},"organization":{"type":"object","additionalProperties":false,"properties":{"field":{"type":"string"},"reference":{"type":"object","additionalProperties":false,"properties":{"catalog_name":{"type":"string"},"uuid":{"type":"string"}},"required":["catalog_name","uuid"]}},"required":["field","reference"]}},"required":["object_type","object_name"]}`)}
}
func NewRecordCountHandler(client *onec.Client) mcp.ToolHandler {
	return WithToolErrors("Количество доступных записей не получено", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input onec.RecordCountRequest
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, InvalidParams(argumentDecodeError(err))
		}
		result, err := client.CountRecords(ctx, input)
		if err != nil {
			var status *onec.StatusError
			if errors.As(err, &status) && (status.StatusCode == 404 || status.StatusCode == 405 || status.StatusCode == 501) {
				err = fmt.Errorf("POST /count недоступен (HTTP %d). Проверьте публикацию MCPService и наличие обработчика /count. Обновите расширение командой mcp-1c --install с путём своей базы и учётными данными установки; затем повторите вызов", status.StatusCode)
			}
			return nil, err
		}
		return textResult(FormatRecordCount(result)), nil
	})
}
func FormatRecordCount(result *onec.RecordCountResult) string {
	data, _ := json.MarshalIndent(result, "", "  ")
	return fmt.Sprintf("# Количество доступных записей\n\nРезультат текущей сессии 1С. Период полуоткрытый; лимит возвращаемых строк не ограничивает сканирование COUNT.\n\n```json\n%s\n```", data)
}
