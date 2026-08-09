// Package instructions holds the server-level prose the MCP server hands the
// model once per session, in InitializeResult.Instructions.
//
// WHY IT IS ITS OWN PACKAGE. The text is wired up in package server
// (server.New passes it to mcp.NewServer), but the guards that keep it honest
// have to call the renderers it describes, and those live in package tools.
// server imports tools, so tools cannot import server without an import cycle.
// A leaf package both of them can import is the only home from which every
// guard can reach the constant.
//
// WHAT MAY BE SAID HERE. Only what is decidable from Go compiled into this same
// binary. The 1C extension, extension/src/HTTPServices/MCPService/Ext/Module.bsl,
// is installed and versioned separately from this binary, and
// cmd/mcp-1c/main.go treats expectedExtensionVersion as a FLOOR rather than an
// exact requirement: a NEWER extension is logged as "a supported combination"
// (main.go:checkExtensionVersion) and an OLDER one is logged and then served
// anyway, because main.go:main starts checkExtensionVersion in a goroutine whose
// verdict gates nothing.
// So a sentence whose truth is decided in BSL is simply false for part of the
// installed base. That rule is why nothing below states the seven-day event log
// window, what the truncated flag means, or that a parameter string becomes a
// Дата: all three are decided in Module.bsl, none of them in this binary.
//
// NO BARE PROHIBITIONS. An over-broad "do not call X" fires on every session and
// suppresses calls the user needed, and nothing in this product can report a call
// that was never made: there is no telemetry anywhere in the tree. Every steer
// below is a fact plus the condition it holds under, or a positive instruction.
//
// The guards are in three places, each next to what it reads:
// internal/instructions/instructions_test.go for properties of the text itself,
// server/instructions_contract_test.go for what a live session receives and what
// the live registry offers, tools/instructions_contract_test.go for the rendered
// output and the corpus the text quotes numbers from.
package instructions

// Text is the instruction string. It is sent once, inside the initialize result,
// and the SDK snapshot-copies ServerOptions in NewServer, so it cannot be changed
// after the server is constructed.
//
// A Go raw string literal, which is safe because the text holds no backtick;
// TestTextIsSafeInARawStringLiteral fails if one ever appears.
//
// Customer-facing RU: no тире.
const Text = `Сервер mcp-1c читает конфигурацию и данные одной информационной базы 1С:Предприятие. Читай ответы буквально: чего в ответе нет, того сервер не утверждал.

Ответ, первая строка которого говорит, что запрошенное не выполнено, не получено или не прочитано, это отказ инструмента, а не пустой результат. Он говорит о вызове, а не о содержимом конфигурации.

Параметр limit есть только у execute_query, search_code и get_event_log, и считает он результаты, а не байты. У остальных инструментов ограничить размер ответа нечем, поэтому сужай вызов аргументами заранее. У get_metadata_tree для этого есть filter: без него приходит короткая сводка, по строке на категорию, а с ним вся категория целиком. Значение filter бери из сводки, где оно напечатано как filter="...".

В execute_query перечисляй нужные поля вместо ВЫБРАТЬ *: сервер печатает каждую колонку каждой строки целиком и ничего в ячейках не сокращает, так что ширину строки не ограничивает ничто.

Когда записи есть, get_event_log печатает их, а в конце отдельную строку «Всего». Пометки об усечении в этом ответе нет, поэтому сравнивай число показанных записей с «Всего» сам.

В bsl_syntax_help поиск идёт подстрокой по имени функции: «Стр» возвращает 28 полных статей вместо одной, а пустой query возвращает весь справочник, все 180. Знаешь имя целиком, передавай его целиком.

Если в списке инструментов нет search_code и reload_dump, сервер запущен без флага --dump, и поиска по коду в этой сессии нет. О самой конфигурации это не говорит ничего.`
