package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FormPatternsVersion identifies this static, authored layout reference.
const FormPatternsVersion = "1"

var formPatternOrder = []string{"document", "catalog", "list", "processing", "wizard"}
var formPatternText = map[string]string{
	"document":   "## Документ\n\nВерхняя зона: номер, дата и основные реквизиты. Основная область: табличные части, при необходимости на отдельных страницах. Нижняя зона: итоговые значения и комментарий. Размещайте связанные поля рядом; сохраняйте порядок чтения сверху вниз.\n",
	"catalog":    "## Элемент справочника\n\nНачните с наименования и кода, затем сгруппируйте основные реквизиты по смыслу. Редко используемые сведения можно вынести на отдельную страницу. Для короткой карточки достаточно одной вертикальной группы.\n",
	"list":       "## Список\n\nРасположите отборы над таблицей, действия — рядом с областью, к которой они относятся. Показывайте выбранные значения отборов явно. Не смешивайте итог по всем данным и итог по отфильтрованным строкам.\n",
	"processing": "## Обработка или отчёт\n\nОтделите параметры от результата. Рядом с основной командой поясните, что она делает; после выполнения покажите состояние и результат. Для отчёта оставьте результату основную площадь формы.\n",
	"wizard":     "## Последовательные шаги\n\nКаждый шаг решает одну задачу. Покажите текущий шаг и доступные переходы. До завершающего действия дайте проверить введённые значения; при возврате сохраняйте ввод.\n",
}

// FormPatternsText retrieves bounded reference text. It neither generates nor validates native forms.
func FormPatternsText(selection string) (string, error) {
	if selection == "" {
		selection = "all"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Паттерны компоновки управляемых форм · версия %s\n\nСтатические рекомендации по компоновке. Это не генератор, редактор или проверка свойств платформы 1С.\n\n", FormPatternsVersion)
	if selection == "all" {
		for _, name := range formPatternOrder {
			out.WriteString(formPatternText[name])
			out.WriteString("\n")
		}
	} else if value, ok := formPatternText[selection]; ok {
		out.WriteString(value)
	} else {
		return "", errors.New("selection must be all, document, catalog, list, processing or wizard")
	}
	return out.String(), nil
}

type FormPatternsRequest struct {
	Selection string `json:"selection,omitempty"`
}

func FormPatternsTool() *mcp.Tool {
	return &mcp.Tool{Name: "form_patterns", Title: "Паттерны компоновки форм", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, Description: "Статический справочник компоновки управляемых форм: document, catalog, list, processing, wizard или all. Не генерирует и не проверяет форму; работает без базы, платформы, сети и платной лицензии.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"selection":{"type":"string","enum":["all","document","catalog","list","processing","wizard"]}}}`)}
}

func NewFormPatternsHandler() mcp.ToolHandler {
	return WithToolErrors("Паттерны компоновки форм не получены", func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input FormPatternsRequest
		if err := localDecode(req.Params.Arguments, &input); err != nil {
			return nil, InvalidParams(argumentDecodeError(err))
		}
		text, err := FormPatternsText(input.Selection)
		if err != nil {
			return nil, err
		}
		return textResult(text), nil
	})
}
