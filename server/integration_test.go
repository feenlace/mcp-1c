package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mock1CHandler simulates the 1C HTTP service endpoints.
func mock1CHandler() http.Handler {
	metadata := map[string][]string{
		"Справочники":            {"Контрагенты", "Номенклатура"},
		"Документы":              {"РеализацияТоваровУслуг"},
		"Перечисления":           {"СтавкиНДС", "ВидыНоменклатуры"},
		"Обработки":              {"ЗагрузкаДанныхИзФайла"},
		"Отчеты":                 {"ОборотноСальдоваяВедомость"},
		"РегистрыСведений":       {"КурсыВалют"},
		"РегистрыНакопления":     {"ТоварыНаСкладах"},
		"РегистрыБухгалтерии":    {},
		"ПланыСчетов":            {"Хозрасчетный"},
		"ПланыВидовХарактеристик": {"ВидыСубконтоХозрасчетные"},
		"ПланыОбмена":            {"ОбменБухгалтерия"},
		"ЖурналыДокументов":      {"ЖурналОпераций"},
		"Константы":              {"ОсновнаяОрганизация"},
		"ОбщиеМодули":            {"ОбщийМодуль1"},
		"Роли":                   {"Администратор", "Бухгалтер"},
		"Подсистемы":             {"Бухгалтерия"},
		"HTTPСервисы":            {"MCPService"},
	}

	objects := map[string]onec.ObjectStructure{
		"Document/РеализацияТоваровУслуг": {
			Name:    "РеализацияТоваровУслуг",
			Synonym: "Реализация (акты, накладные, УПД)",
			Attributes: []onec.Attribute{
				{Name: "Контрагент", Synonym: "Контрагент", Type: "СправочникСсылка.Контрагенты"},
				{Name: "СуммаДокумента", Synonym: "Сумма", Type: "Число"},
			},
			TabularParts: []onec.TabularPart{
				{
					Name: "Товары",
					Attributes: []onec.Attribute{
						{Name: "Номенклатура", Synonym: "Номенклатура", Type: "СправочникСсылка.Номенклатура"},
						{Name: "Количество", Synonym: "Количество", Type: "Число"},
					},
				},
			},
		},
		"AccumulationRegister/ТоварыНаСкладах": {
			Name:    "ТоварыНаСкладах",
			Synonym: "Товары на складах",
			Dimensions: []onec.Attribute{
				{Name: "Номенклатура", Synonym: "Номенклатура", Type: "СправочникСсылка.Номенклатура"},
				{Name: "Склад", Synonym: "Склад", Type: "СправочникСсылка.Склады"},
			},
			Resources: []onec.Attribute{
				{Name: "Количество", Synonym: "Количество", Type: "Число"},
			},
		},
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(metadata)
	})

	mux.HandleFunc("/object/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/object/")
		obj, ok := objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"Object not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(obj)
	})

	mux.HandleFunc("/query", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{
			"columns":   []string{"Наименование"},
			"rows":      [][]any{{"ООО Ромашка"}, {"ИП Иванов"}},
			"total":     2,
			"truncated": false,
		})
	})

	mux.HandleFunc("/form/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{
			"name":  "ФормаДокумента",
			"title": "Реализация товаров и услуг",
			"elements": []map[string]any{
				{"name": "Контрагент", "type": "ПолеВвода", "title": "Контрагент", "dataPath": "Объект.Контрагент"},
				{"name": "СуммаДокумента", "type": "ПолеВвода", "title": "Сумма", "dataPath": "Объект.СуммаДокумента"},
			},
			"commands": []map[string]any{
				{"name": "Провести", "action": "ПровестиИЗакрыть"},
			},
			"handlers": []map[string]any{
				{"event": "ПриОткрытии", "handler": "ПриОткрытии"},
			},
		})
	})

	mux.HandleFunc("/eventlog", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{
				{
					"date":     "2026-03-07T10:00:00",
					"level":    "Ошибка",
					"event":    "Данные.Запись",
					"user":     "Администратор",
					"metadata": "Документ.РеализацияТоваровУслуг",
					"comment":  "Ошибка при записи документа",
				},
				{
					"date":  "2026-03-07T09:30:00",
					"level": "Информация",
					"event": "Сеанс.Начало",
					"user":  "Бухгалтер",
				},
			},
			"total": 2,
		})
	})

	mux.HandleFunc("/configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{
			"name":             "БухгалтерияПредприятия",
			"version":          "3.0.150.1",
			"vendor":           "Фирма \"1С\"",
			"platform_version": "8.3.25.1000",
			"mode":             "file",
		})
	})

	mux.HandleFunc("/validate-query", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		upper := strings.ToUpper(strings.TrimSpace(req.Query))
		if strings.HasPrefix(upper, "ВЫБРАТЬ") || strings.HasPrefix(upper, "SELECT") {
			json.NewEncoder(w).Encode(map[string]any{"valid": true})
		} else {
			json.NewEncoder(w).Encode(map[string]any{
				"valid":  false,
				"errors": []string{"Ожидается ключевое слово ВЫБРАТЬ"},
			})
		}
	})

	return mux
}

// setupIntegration creates a mock 1C server and connected MCP client session.
func setupIntegration(t *testing.T) (*mcp.ClientSession, func()) {
	t.Helper()

	mock := httptest.NewServer(mock1CHandler())
	client := onec.NewClient(mock.URL, "", "")

	// Create a temp dump directory for search_code tests.
	dumpDir := t.TempDir()
	mkBSL(t, dumpDir, "Documents/РеализацияТоваровУслуг/Ext/ObjectModule.bsl",
		"Процедура ОбработкаПроведения(Отказ, РежимПроведения)\n\t// Код проведения\nКонецПроцедуры\n")
	mkBSL(t, dumpDir, "Documents/ПоступлениеТоваровУслуг/Ext/ObjectModule.bsl",
		"Процедура ОбработкаПроведения(Отказ)\n\t// Проведение поступления\nКонецПроцедуры\n")

	dumpIndex, err := dump.NewIndex(dumpDir, false)
	if err != nil {
		mock.Close()
		t.Fatalf("NewIndex: %v", err)
	}

	deadline := time.After(30 * time.Second)
	for !dumpIndex.Ready() {
		select {
		case <-deadline:
			dumpIndex.Close()
			mock.Close()
			t.Fatal("timed out waiting for dump index to become ready")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	srv := New("test", client, dumpIndex)

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()

	_, err = srv.Connect(ctx, st, nil)
	if err != nil {
		mock.Close()
		t.Fatalf("server connect: %v", err)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		mock.Close()
		t.Fatalf("client connect: %v", err)
	}

	cleanup := func() {
		session.Close()
		dumpIndex.Close()
		mock.Close()
	}
	return session, cleanup
}

func TestIntegration_ListTools(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}

	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Name] = true
	}

	expected := []string{
		"get_metadata_tree", "get_object_structure", "execute_query",
		"search_code", "get_form_structure", "validate_query",
		"get_event_log", "get_configuration_info", "bsl_syntax_help",
	}
	for _, want := range expected {
		if !toolNames[want] {
			t.Errorf("expected tool %q in list, got: %v", want, toolNames)
		}
	}

	if len(result.Tools) != len(expected) {
		t.Errorf("expected %d tools, got %d: %v", len(expected), len(result.Tools), toolNames)
	}
}

func TestIntegration_MetadataTree(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	// Without filter -- summary with category names and counts.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_metadata_tree",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"Справочники", "Документы", "Регистры сведений",
		"Регистры накопления", "Общие модули",
		"Перечисления", "Планы счетов", "Роли",
		"Подсистемы", "HTTP-сервисы", "Планы обмена", "Константы",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in summary, got:\n%s", want, text)
		}
	}

	// With filter -- detailed list of objects in category.
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_metadata_tree",
		Arguments: map[string]any{"filter": "Справочники"},
	})
	if err != nil {
		t.Fatalf("CallTool with filter error: %v", err)
	}
	text = result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"Контрагенты", "Номенклатура"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in filtered response, got:\n%s", want, text)
		}
	}
}

func TestIntegration_ObjectStructure(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_object_structure",
		Arguments: map[string]any{
			"object_type": "Document",
			"object_name": "РеализацияТоваровУслуг",
		},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"РеализацияТоваровУслуг", "Контрагент", "СуммаДокумента", "Товары", "Номенклатура"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in response, got:\n%s", want, text)
		}
	}
}

func TestIntegration_ObjectStructure_Register(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_object_structure",
		Arguments: map[string]any{
			"object_type": "AccumulationRegister",
			"object_name": "ТоварыНаСкладах",
		},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"ТоварыНаСкладах", "Измерения", "Номенклатура", "Склад", "Ресурсы", "Количество"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in response, got:\n%s", want, text)
		}
	}
}

func TestIntegration_ObjectStructure_NotFound(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_object_structure",
		Arguments: map[string]any{
			"object_type": "Document",
			"object_name": "НесуществующийДокумент",
		},
	})
	if err == nil {
		t.Fatal("expected error for non-existent object")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 in error, got: %v", err)
	}
}

func TestIntegration_FormStructure(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_form_structure",
		Arguments: map[string]any{
			"object_type": "Document",
			"object_name": "РеализацияТоваровУслуг",
		},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"ФормаДокумента", "Контрагент", "ПолеВвода", "Провести", "ПриОткрытии"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in response, got:\n%s", want, text)
		}
	}
}

func TestIntegration_ConfigInfo(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_configuration_info",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"БухгалтерияПредприятия",
		"3.0.150.1",
		"8.3.25.1000",
		"Файловый",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in response, got:\n%s", want, text)
		}
	}
}

func TestIntegration_SearchCode(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search_code",
		Arguments: map[string]any{
			"query": "ОбработкаПроведения",
		},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"ОбработкаПроведения",
		"Документ.РеализацияТоваровУслуг.МодульОбъекта",
		"Документ.ПоступлениеТоваровУслуг.МодульОбъекта",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in response, got:\n%s", want, text)
		}
	}
}

func TestIntegration_BSLSyntaxHelp(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bsl_syntax_help",
		Arguments: map[string]any{
			"query": "СтрНайти",
		},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "СтрНайти") {
		t.Errorf("expected СтрНайти in response, got:\n%s", text)
	}
	if !strings.Contains(text, "StrFind") {
		t.Errorf("expected StrFind in response, got:\n%s", text)
	}
}

func TestIntegration_ExecuteQuery(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "execute_query",
		Arguments: map[string]any{
			"query": "ВЫБРАТЬ Наименование ИЗ Справочник.Контрагенты",
		},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"Наименование", "ООО Ромашка", "ИП Иванов"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in response, got:\n%s", want, text)
		}
	}
}

func TestIntegration_ValidateQuery_Valid(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "validate_query",
		Arguments: map[string]any{
			"query": "ВЫБРАТЬ Наименование ИЗ Справочник.Контрагенты",
		},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "корректен") {
		t.Errorf("expected 'корректен' in response for valid query, got:\n%s", text)
	}
}

func TestIntegration_ValidateQuery_Invalid(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "validate_query",
		Arguments: map[string]any{
			"query": "ОБНОВИТЬ Справочник.Контрагенты",
		},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "ошибки") {
		t.Errorf("expected 'ошибки' in response for invalid query, got:\n%s", text)
	}
}

func TestIntegration_EventLog(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_event_log",
		Arguments: map[string]any{
			"level": "Ошибка",
			"limit": 10,
		},
	})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"Журнал регистрации",
		"Ошибка",
		"Администратор",
		"РеализацияТоваровУслуг",
		"Информация",
		"Бухгалтер",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in response, got:\n%s", want, text)
		}
	}
}

func TestIntegration_ListPrompts(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListPrompts error: %v", err)
	}

	expected := map[string]bool{
		"review_module":            false,
		"write_posting":            false,
		"optimize_query":           false,
		"explain_config":           false,
		"analyze_error":            false,
		"find_duplicates":          false,
		"write_report":             false,
		"explain_object":           false,
		"1c_query_syntax":          false,
		"1c_metadata_navigation":   false,
		"1c_development_workflow":  false,
	}

	for _, p := range result.Prompts {
		if _, ok := expected[p.Name]; ok {
			expected[p.Name] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected prompt %q not found in ListPrompts result", name)
		}
	}

	if len(result.Prompts) != len(expected) {
		t.Errorf("expected %d prompts, got %d", len(expected), len(result.Prompts))
	}
}

func TestIntegration_GetPrompt_ReviewModule(t *testing.T) {
	session, cleanup := setupIntegration(t)
	defer cleanup()

	result, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name: "review_module",
		Arguments: map[string]string{
			"object_type": "Document",
			"object_name": "РеализацияТоваровУслуг",
		},
	})
	if err != nil {
		t.Fatalf("GetPrompt error: %v", err)
	}

	if result.Description == "" {
		t.Error("expected non-empty description")
	}

	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}

	msg := result.Messages[0]
	if msg.Role != "user" {
		t.Errorf("expected role \"user\", got %q", msg.Role)
	}

	tc, ok := msg.Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected *mcp.TextContent, got %T", msg.Content)
	}

	for _, keyword := range []string{
		"Document",
		"РеализацияТоваровУслуг",
		"get_object_structure",
		"search_code",
	} {
		if !strings.Contains(tc.Text, keyword) {
			t.Errorf("expected %q in prompt text, got:\n%s", keyword, tc.Text)
		}
	}
}

func mkBSL(t *testing.T, base, relPath, content string) {
	t.Helper()
	full := filepath.Join(base, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
