// Package prompts registers MCP prompts that help LLMs use the server's tools
// for common 1C:Enterprise development tasks.
package prompts

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptDef bundles a prompt definition with its handler.
type promptDef struct {
	prompt  *mcp.Prompt
	handler mcp.PromptHandler
}

// allPrompts is the complete list of prompts exposed by the server.
var allPrompts = []promptDef{
	{
		prompt: &mcp.Prompt{
			Name:        "review_module",
			Description: "Ревью кода модуля 1С",
			Arguments: []*mcp.PromptArgument{
				{Name: "object_type", Description: "Тип объекта метаданных (например Document, Catalog)", Required: true},
				{Name: "object_name", Description: "Имя объекта метаданных", Required: true},
			},
		},
		handler: handleReviewModule,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "write_posting",
			Description: "Написание обработки проведения документа",
			Arguments: []*mcp.PromptArgument{
				{Name: "document_name", Description: "Имя документа", Required: true},
			},
		},
		handler: handleWritePosting,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "optimize_query",
			Description: "Оптимизация запроса 1С",
			Arguments: []*mcp.PromptArgument{
				{Name: "query", Description: "Текст запроса на языке 1С", Required: true},
			},
		},
		handler: handleOptimizeQuery,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "explain_config",
			Description: "Объяснение структуры конфигурации",
		},
		handler: handleExplainConfig,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "analyze_error",
			Description: "Анализ ошибки 1С",
			Arguments: []*mcp.PromptArgument{
				{Name: "error_text", Description: "Текст ошибки из 1С", Required: true},
			},
		},
		handler: handleAnalyzeError,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "find_duplicates",
			Description: "Поиск дублей в модуле",
			Arguments: []*mcp.PromptArgument{
				{Name: "object_type", Description: "Тип объекта метаданных (например Document, Catalog)", Required: true},
				{Name: "object_name", Description: "Имя объекта метаданных", Required: true},
			},
		},
		handler: handleFindDuplicates,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "write_report",
			Description: "Помощь с написанием отчёта",
			Arguments: []*mcp.PromptArgument{
				{Name: "description", Description: "Описание требуемого отчёта", Required: true},
			},
		},
		handler: handleWriteReport,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "explain_object",
			Description: "Объяснение назначения объекта",
			Arguments: []*mcp.PromptArgument{
				{Name: "object_type", Description: "Тип объекта метаданных (например Document, Catalog)", Required: true},
				{Name: "object_name", Description: "Имя объекта метаданных", Required: true},
			},
		},
		handler: handleExplainObject,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "1c_query_syntax",
			Description: "Синтаксис запросов 1С: таблицы, виртуальные таблицы, перечисления, типичные ошибки",
		},
		handler: handle1CQuerySyntax,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "1c_metadata_navigation",
			Description: "Навигация по метаданным конфигурации 1С: порядок исследования, маппинг категорий",
		},
		handler: handle1CMetadataNavigation,
	},
	{
		prompt: &mcp.Prompt{
			Name:        "1c_development_workflow",
			Description: "Рабочий процесс разработки на 1С: пошаговый workflow и чеклист",
			Arguments: []*mcp.PromptArgument{
				{Name: "task", Description: "Описание задачи разработки", Required: true},
			},
		},
		handler: handle1CDevelopmentWorkflow,
	},
}

// RegisterAll registers all prompts on the given MCP server.
func RegisterAll(s *mcp.Server) {
	for _, p := range allPrompts {
		s.AddPrompt(p.prompt, p.handler)
	}
}

// requiredArg extracts a required argument from the prompt request,
// returning an error if it is missing or empty.
func requiredArg(req *mcp.GetPromptRequest, name string) (string, error) {
	if req.Params == nil || req.Params.Arguments == nil {
		return "", fmt.Errorf("missing required argument %q", name)
	}
	v := req.Params.Arguments[name]
	if v == "" {
		return "", fmt.Errorf("missing required argument %q", name)
	}
	return v, nil
}

// requiredObjectArgs extracts the object_type and object_name arguments common to
// several prompts (review_module, find_duplicates, explain_object).
func requiredObjectArgs(req *mcp.GetPromptRequest) (objectType, objectName string, err error) {
	objectType, err = requiredArg(req, "object_type")
	if err != nil {
		return "", "", err
	}
	objectName, err = requiredArg(req, "object_name")
	if err != nil {
		return "", "", err
	}
	return objectType, objectName, nil
}

// promptResult constructs a standard prompt result with a single user message.
func promptResult(description, text string) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{
		Description: description,
		Messages: []*mcp.PromptMessage{
			{
				Role:    "user",
				Content: &mcp.TextContent{Text: text},
			},
		},
	}, nil
}

//garble:ignore
func handleReviewModule(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	objectType, objectName, err := requiredObjectArgs(req)
	if err != nil {
		return nil, err
	}

	return promptResult(
		fmt.Sprintf("Ревью модуля %s.%s", objectType, objectName),
		fmt.Sprintf(`Проведи ревью кода модуля объекта %s "%s".

Шаги:
1. Используй инструмент get_object_structure чтобы посмотреть структуру объекта (object_type: %s, object_name: %s)
2. Используй инструмент search_code чтобы найти код модулей этого объекта
3. Проанализируй код на:
   - Ошибки и потенциальные баги
   - Нарушения стандартов разработки 1С
   - Производительность (лишние запросы к базе данных, неоптимальные циклы, запросы в цикле)
   - Читаемость и именование переменных/процедур (убедись, что зарезервированные слова И, Или, Не, Для, Если и др. не используются как имена переменных)
   - Корректность работы с транзакциями и блокировками
4. Если нужна справка по встроенным функциям -- используй инструмент bsl_syntax_help
5. Предложи конкретные улучшения с примерами кода на языке 1С`, objectType, objectName, objectType, objectName),
	)
}

//garble:ignore
func handleWritePosting(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	documentName, err := requiredArg(req, "document_name")
	if err != nil {
		return nil, err
	}

	return promptResult(
		fmt.Sprintf("Обработка проведения документа %s", documentName),
		fmt.Sprintf(`Помоги написать обработку проведения для документа "%s".

Шаги:
1. Используй инструмент get_object_structure чтобы посмотреть структуру документа (object_type: Document, object_name: %s) -- реквизиты и табличные части
2. Используй инструмент search_code чтобы найти текущий код модуля объекта
3. Используй инструмент get_metadata_tree чтобы увидеть доступные регистры накопления, сведений и бухгалтерии
4. Для каждого регистра, в который должен записывать документ, используй get_object_structure чтобы узнать его измерения, ресурсы и реквизиты
5. Напиши процедуру ОбработкаПроведения(Отказ, РежимПроведения) которая:
   - Формирует движения по нужным регистрам
   - Использует запрос для получения данных табличной части с соединениями
   - Контролирует остатки при необходимости (РежимПроведения = РежимПроведенияДокумента.Оперативный)
   - Очищает движения перед формированием новых
6. Если нужна справка по синтаксису -- используй инструмент bsl_syntax_help`, documentName, documentName),
	)
}

//garble:ignore
func handleOptimizeQuery(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	query, err := requiredArg(req, "query")
	if err != nil {
		return nil, err
	}

	return promptResult(
		"Оптимизация запроса 1С",
		fmt.Sprintf(`Проанализируй и оптимизируй следующий запрос 1С:

%s

Шаги:
1. Используй инструмент execute_query чтобы выполнить запрос и оценить объём возвращаемых данных
2. Используй инструмент get_metadata_tree чтобы увидеть доступные объекты метаданных
3. При необходимости используй get_object_structure для проверки структуры таблиц, участвующих в запросе
4. Проанализируй запрос на:
   - Использование соединений (LEFT JOIN vs INNER JOIN)
   - Наличие условий, не покрытых индексами
   - Использование виртуальных таблиц с параметрами вместо вложенных запросов
   - Избыточные подзапросы и временные таблицы
   - Корректность использования РАЗЛИЧНЫЕ, ПЕРВЫЕ, СГРУППИРОВАТЬ
   - Возможность использования пакетных запросов
5. Предложи оптимизированную версию запроса с пояснениями`, query),
	)
}

//garble:ignore
func handleExplainConfig(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return promptResult(
		"Объяснение структуры конфигурации 1С",
		`Объясни структуру текущей конфигурации 1С.

Шаги:
1. Используй инструмент get_metadata_tree чтобы получить полное дерево метаданных конфигурации
2. Проанализируй состав конфигурации:
   - Какие подсистемы есть и за что они отвечают
   - Основные справочники и их назначение
   - Документы и бизнес-процессы которые они автоматизируют
   - Регистры накопления и сведений -- какие данные хранят
   - Регистры бухгалтерии и планы счетов (если есть)
   - Отчёты и обработки
   - Общие модули и их вероятная роль
   - Роли и разграничение доступа
3. Опиши общую архитектуру конфигурации: какую предметную область она автоматизирует, как связаны основные объекты между собой
4. Укажи на особенности и возможные проблемы архитектуры`,
	)
}

//garble:ignore
func handleAnalyzeError(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	errorText, err := requiredArg(req, "error_text")
	if err != nil {
		return nil, err
	}

	return promptResult(
		"Анализ ошибки 1С",
		fmt.Sprintf(`Проанализируй следующую ошибку 1С и помоги её исправить:

%s

Шаги:
1. Определи тип ошибки (синтаксическая, ошибка времени выполнения, ошибка запроса, ошибка блокировки, ошибка прав доступа)
2. Если ошибка указывает на конкретный объект метаданных -- используй get_object_structure для просмотра его структуры
3. Если ошибка связана с кодом модуля -- используй search_code для поиска исходного кода
4. Если ошибка связана с запросом -- используй execute_query для проверки запроса
5. Если нужна справка по функциям -- используй bsl_syntax_help
6. Объясни:
   - Причину ошибки
   - В каких условиях она возникает
   - Как её исправить (с примером кода)
   - Как предотвратить подобные ошибки в будущем`, errorText),
	)
}

//garble:ignore
func handleFindDuplicates(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	objectType, objectName, err := requiredObjectArgs(req)
	if err != nil {
		return nil, err
	}

	return promptResult(
		fmt.Sprintf("Поиск дублей в модуле %s.%s", objectType, objectName),
		fmt.Sprintf(`Найди дублирующийся и избыточный код в модулях объекта %s "%s".

Шаги:
1. Используй инструмент get_object_structure чтобы посмотреть структуру объекта (object_type: %s, object_name: %s)
2. Используй инструмент search_code чтобы найти код модулей объекта
3. Проанализируй код на:
   - Дублирующиеся фрагменты кода (copy-paste)
   - Процедуры и функции с похожей логикой, которые можно объединить
   - Повторяющиеся запросы к базе данных
   - Одинаковые проверки условий в разных местах
   - Код, который можно вынести в общий модуль
4. Для каждого найденного дубля предложи рефакторинг:
   - Выделение общей процедуры/функции
   - Параметризация отличающихся частей
   - Примеры кода после рефакторинга`, objectType, objectName, objectType, objectName),
	)
}

//garble:ignore
func handleWriteReport(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	description, err := requiredArg(req, "description")
	if err != nil {
		return nil, err
	}

	return promptResult(
		"Помощь с написанием отчёта 1С",
		fmt.Sprintf(`Помоги написать отчёт 1С по следующему описанию:

%s

Шаги:
1. Используй инструмент get_metadata_tree чтобы увидеть доступные объекты метаданных (справочники, документы, регистры)
2. Определи источники данных для отчёта и используй get_object_structure для каждого из них, чтобы узнать структуру полей
3. Используй execute_query чтобы проверить пробный запрос и убедиться что данные доступны
4. Если нужна справка по синтаксису запросов или функций -- используй bsl_syntax_help
5. Напиши:
   - Текст запроса для СКД (системы компоновки данных) или прямого вывода
   - Описание структуры настроек СКД (группировки, поля, отборы, условное оформление)
   - Код модуля отчёта если нужна программная обработка данных
   - Рекомендации по оптимизации при больших объёмах данных`, description),
	)
}

//garble:ignore
func handleExplainObject(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	objectType, objectName, err := requiredObjectArgs(req)
	if err != nil {
		return nil, err
	}

	return promptResult(
		fmt.Sprintf("Объяснение объекта %s.%s", objectType, objectName),
		fmt.Sprintf(`Объясни назначение и устройство объекта %s "%s".

Шаги:
1. Используй инструмент get_object_structure чтобы получить полную структуру объекта (object_type: %s, object_name: %s) -- реквизиты, табличные части, измерения, ресурсы
2. Используй инструмент search_code чтобы найти код модулей объекта
3. Используй инструмент get_metadata_tree чтобы увидеть другие объекты конфигурации и понять связи
4. Объясни:
   - Для чего предназначен этот объект в конфигурации
   - Какие данные он хранит (описание каждого реквизита и табличной части)
   - С какими другими объектами связан (ссылочные типы реквизитов)
   - Какую бизнес-логику содержат его модули
   - Как он используется в бизнес-процессах предприятия`, objectType, objectName, objectType, objectName),
	)
}

//garble:ignore
func handle1CQuerySyntax(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return promptResult(
		"Синтаксис запросов 1С",
		`# Синтаксис запросов 1С

## Именование таблиц по типам

Имена таблиц в запросах используют ЕДИНСТВЕННОЕ число:
- Справочник.X (НЕ Справочники.X)
- Документ.X (НЕ Документы.X)
- РегистрНакопления.X (НЕ РегистрыНакопления.X)
- РегистрСведений.X (НЕ РегистрыСведений.X)
- РегистрБухгалтерии.X
- ПланСчетов.X
- ПланВидовХарактеристик.X

Перечисления НЕ являются таблицами! Нельзя делать ВЫБРАТЬ ИЗ Перечисление.X.

## Виртуальные таблицы регистров накопления

- РегистрНакопления.X.Остатки(&Период, Условия)
- РегистрНакопления.X.Обороты(&НачалоПериода, &КонецПериода, Периодичность, Условия)
- РегистрНакопления.X.ОстаткиИОбороты(&НачалоПериода, &КонецПериода, Периодичность, МетодДополнения, Условия)

## Виртуальные таблицы регистров сведений

- РегистрСведений.X.СрезПоследних(&Период, Условия)
- РегистрСведений.X.СрезПервых(&Период, Условия)

## Перечисления

Перечисления не являются таблицами. Используй функцию ЗНАЧЕНИЕ() в WHERE или CASE:
  ЗНАЧЕНИЕ(Перечисление.ИмяПеречисления.ИмяЗначения)

## Предопределённые элементы

Обращение к предопределённым элементам справочников:
  ЗНАЧЕНИЕ(Справочник.Валюты.USD)

## Параметры

Параметры указываются через &Имя и передаются через аргумент parameters:
  ГДЕ Дата > &ДатаНачала

## Типичные ошибки

1. НЕПРАВИЛЬНО: ВЫБРАТЬ Перечисления.ВидыОпераций.Наименование
   ПРАВИЛЬНО: используй ЗНАЧЕНИЕ(Перечисление.ВидыОпераций.Приход) в условии WHERE
   Причина: перечисления не являются таблицами

2. НЕПРАВИЛЬНО: ВЫБРАТЬ * ИЗ Справочники.Номенклатура
   ПРАВИЛЬНО: ВЫБРАТЬ * ИЗ Справочник.Номенклатура
   Причина: имена таблиц в единственном числе

3. НЕПРАВИЛЬНО: ИЗ РегистрНакопления.Остатки
   ПРАВИЛЬНО: ИЗ РегистрНакопления.ТоварыНаСкладах.Остатки(&Период)
   Причина: виртуальная таблица вызывается от конкретного регистра

4. НЕПРАВИЛЬНО: ГДЕ Склад = "Основной"
   ПРАВИЛЬНО: ГДЕ Склад = &Склад (передать ссылку через параметр)
   Причина: ссылочные поля нельзя сравнивать со строками

5. НЕПРАВИЛЬНО: Документы.Реализация
   ПРАВИЛЬНО: Документ.РеализацияТоваровУслуг (единственное число, полное имя)
   Причина: используется единственное число и полное имя объекта метаданных

## Рабочий процесс

1. Вызови get_object_structure для получения точных имён полей объекта
2. Вызови validate_query для проверки синтаксиса написанного запроса
3. Вызови execute_query для выполнения проверенного запроса`,
	)
}

//garble:ignore
func handle1CMetadataNavigation(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return promptResult(
		"Навигация по метаданным конфигурации 1С",
		`# Навигация по метаданным конфигурации 1С

## Порядок исследования незнакомой конфигурации

1. get_configuration_info: общая информация (имя, версия, режим совместимости)
2. get_metadata_tree без фильтра: сводка по категориям (сколько справочников, документов, регистров)
3. get_metadata_tree с filter по нужной категории: список объектов в категории
4. get_object_structure: структура конкретного объекта (реквизиты, табличные части, типы)

## Маппинг категорий метаданных на имена таблиц запросов

- Справочники -> Справочник.X
- Документы -> Документ.X
- РегистрыНакопления -> РегистрНакопления.X
- РегистрыСведений -> РегистрСведений.X
- РегистрыБухгалтерии -> РегистрБухгалтерии.X
- ПланыСчетов -> ПланСчетов.X
- ПланыВидовХарактеристик -> ПланВидовХарактеристик.X
- Перечисления -> НЕ являются таблицами (используй ЗНАЧЕНИЕ())

## Элементы структуры объекта

- Реквизиты: поля объекта (для справочников, документов). В запросах доступны напрямую.
- Табличные части: вложенные таблицы. В запросах доступны через точку от основной таблицы.
- Измерения: ключевые поля регистров (определяют разрезы учёта).
- Ресурсы: значения регистров (суммы, количества, то что хранится в разрезе измерений).
- Реквизиты регистров: дополнительные поля регистров (не влияют на разрезы учёта).

## Как найти нужный объект по бизнес-задаче

- search_code: поиск по ключевым словам в коде модулей (найти логику обработки, вычисления)
- get_metadata_tree: обзор категорий для понимания структуры конфигурации

## Когда использовать search_code vs metadata-инструменты

- search_code: для поиска логики в коде (как что-то вычисляется, где обрабатывается)
- get_metadata_tree / get_object_structure: для структуры данных (какие поля, типы, связи между объектами)`,
	)
}

//garble:ignore
func handle1CDevelopmentWorkflow(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	task, err := requiredArg(req, "task")
	if err != nil {
		return nil, err
	}

	return promptResult(
		"Рабочий процесс разработки на 1С",
		fmt.Sprintf(`# Рабочий процесс разработки на 1С

Задача: %s

## Пошаговый workflow

1. Изучи конфигурацию: вызови get_configuration_info для общей информации, затем get_metadata_tree для обзора объектов
2. Найди релевантные объекты: вызови get_metadata_tree с фильтром по нужной категории, search_code по ключевым словам задачи
3. Изучи структуру объектов: вызови get_object_structure для каждого найденного объекта, запомни точные имена реквизитов
4. Найди существующий код: вызови search_code по именам процедур и объектов, изучи текущую реализацию
5. Уточни синтаксис: вызови bsl_syntax_help для нужных встроенных функций платформы
6. Напиши код, используя точные имена из шага 3
7. Валидируй запросы: вызови validate_query перед execute_query для каждого написанного запроса

## Чеклист перед написанием кода

- Знаешь ли ты точные имена объектов метаданных? (если нет: get_metadata_tree)
- Проверил ли структуру объектов, с которыми работаешь? (если нет: get_object_structure)
- Знаешь ли сигнатуру BSL-функций, которые используешь? (если нет: bsl_syntax_help)
- Проверил ли существующий код в этих модулях? (если нет: search_code)

## Зарезервированные слова

Следующие слова являются ключевыми в языке 1С и НЕ МОГУТ использоваться как имена переменных, параметров или функций:

Если, Тогда, Иначе, ИначеЕсли, КонецЕсли, Для, Каждого, Из, По, Цикл, КонецЦикла, Пока, Процедура, КонецПроцедуры, Функция, КонецФункции, Перем, Возврат, Продолжить, Прервать, И, Или, Не, Попытка, Исключение, КонецПопытки, Истина, Ложь, Неопределено, NULL, Новый, Экспорт, Знач, Перейти, Асинх, Ждать

If, Then, Else, ElsIf, EndIf, For, Each, In, To, Do, EndDo, While, Procedure, EndProcedure, Function, EndFunction, Var, Return, Continue, Break, And, Or, Not, Try, Except, EndTry, True, False, Undefined, NULL, New, Export, Val, Goto, Async, Await

### Частая ошибка

НЕПРАВИЛЬНО: Для Каждого И Из Коллекция Цикл
ПРАВИЛЬНО:   Для Каждого Элемент Из Коллекция Цикл

"И" является логическим оператором (AND), использовать его как переменную нельзя.`, task),
	)
}
