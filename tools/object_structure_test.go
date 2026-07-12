package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestObjectStructureHandler(t *testing.T) {
	const mockResponse = `{
		"name": "РеализацияТоваровУслуг",
		"synonym": "Реализация товаров и услуг",
		"attributes": [
			{"name": "Контрагент", "synonym": "Контрагент", "type": "СправочникСсылка.Контрагенты"},
			{"name": "Сумма", "synonym": "Сумма документа", "type": "Число"}
		],
		"tabularParts": [
			{
				"name": "Товары",
				"attributes": [
					{"name": "Номенклатура", "synonym": "Номенклатура", "type": "СправочникСсылка.Номенклатура"},
					{"name": "Количество", "synonym": "Количество", "type": "Число"}
				]
			}
		]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/Document/РеализацияТоваровУслуг" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "Document",
		"object_name": "РеализацияТоваровУслуг",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}

	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if tc.Text == "" {
		t.Fatal("expected non-empty text")
	}

	for _, want := range []string{
		"РеализацияТоваровУслуг",
		"Реализация товаров и услуг",
		"Контрагент",
		"Сумма",
		"Товары",
		"Номенклатура",
		"Количество",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestObjectStructureHandler_Register(t *testing.T) {
	const mockResponse = `{
		"name": "ТоварыНаСкладах",
		"synonym": "Товары на складах",
		"dimensions": [
			{"name": "Номенклатура", "synonym": "Номенклатура", "type": "СправочникСсылка.Номенклатура"},
			{"name": "Склад", "synonym": "Склад", "type": "СправочникСсылка.Склады"}
		],
		"resources": [
			{"name": "Количество", "synonym": "Количество", "type": "Число"}
		],
		"attributes": []
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/AccumulationRegister/ТоварыНаСкладах" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "AccumulationRegister",
		"object_name": "ТоварыНаСкладах",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	for _, want := range []string{
		"ТоварыНаСкладах", "Измерения", "Номенклатура", "Склад",
		"Ресурсы", "Количество",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestObjectStructureHandler_Enum(t *testing.T) {
	const mockResponse = `{
		"name": "СтатусыЗаказов",
		"synonym": "Статусы заказов",
		"attributes": [],
		"values": [
			{"name": "Новый", "synonym": "Новый", "comment": "Заказ только что создан"},
			{"name": "ВРаботе", "synonym": "В работе", "comment": "Заказ обрабатывается"},
			{"name": "Закрыт", "synonym": "Закрыт", "comment": ""}
		]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/Enum/СтатусыЗаказов" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "Enum",
		"object_name": "СтатусыЗаказов",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	for _, want := range []string{
		"СтатусыЗаказов",
		"Статусы заказов",
		"Значения",
		"Новый",
		"ВРаботе",
		"В работе",
		"Закрыт",
		"Заказ только что создан",
		"Заказ обрабатывается",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestObjectStructureHandler_DefinedType(t *testing.T) {
	const mockResponse = `{
		"name": "ЗначениеДоступа",
		"synonym": "Значение доступа",
		"types": [
			"Справочник.Пользователи",
			"Справочник.ВнешниеПользователи"
		]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/DefinedType/ЗначениеДоступа" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "DefinedType",
		"object_name": "ЗначениеДоступа",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	for _, want := range []string{
		"ЗначениеДоступа",
		"Значение доступа",
		"## Состав типа",
		"Справочник.Пользователи",
		"Справочник.ВнешниеПользователи",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

func TestObjectStructureHandler_MissingArgs(t *testing.T) {
	client := onec.NewClient("http://localhost:0", "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing arguments")
	}
}

func TestObjectStructureTool(t *testing.T) {
	tool := ObjectStructureTool()
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name != "get_object_structure" {
		t.Errorf("expected tool name %q, got %q", "get_object_structure", tool.Name)
	}
	if tool.Description == "" {
		t.Error("expected non-empty description")
	}
}

// TestFormatObjectStructure_TypesGuard codifies that the "## Состав типа" block
// is emitted iff len(Types) > 0, so existing objects' output is unchanged.
func TestFormatObjectStructure_TypesGuard(t *testing.T) {
	cases := []struct {
		name  string
		types []string
		want  bool
	}{
		{"nil", nil, false},
		{"empty", []string{}, false},
		{"one", []string{"Справочник.Пользователи"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := formatObjectStructure(&onec.ObjectStructure{
				Name:    "ЗначениеДоступа",
				Synonym: "Значение доступа",
				Types:   tc.types,
			})
			got := strings.Contains(out, "## Состав типа")
			if got != tc.want {
				t.Errorf("block present = %v, want %v; output:\n%s", got, tc.want, out)
			}
		})
	}
}

// TestFormatObjectStructure_DefinedTypeNoDash guards the no-тире rule: the RU
// composition block must use ASCII list markers, never em-dash / en-dash.
func TestFormatObjectStructure_DefinedTypeNoDash(t *testing.T) {
	out := formatObjectStructure(&onec.ObjectStructure{
		Name:    "ЗначениеДоступа",
		Synonym: "Значение доступа",
		Types:   []string{"Справочник.Пользователи", "Справочник.ВнешниеПользователи"},
	})
	idx := strings.Index(out, "## Состав типа")
	if idx < 0 {
		t.Fatalf("expected composition block, got:\n%s", out)
	}
	block := out[idx:]
	if strings.ContainsRune(block, '—') || strings.ContainsRune(block, '–') {
		t.Errorf("composition block contains em/en-dash, violates no-тире rule:\n%s", block)
	}
}

// TestObjectStructureTool_DefinedTypeSchema proves the tool description advertises
// DefinedType and its types field, and that object_type stays free-text (no enum).
func TestObjectStructureTool_DefinedTypeSchema(t *testing.T) {
	raw, ok := ObjectStructureTool().InputSchema.(json.RawMessage)
	if !ok {
		t.Fatalf("InputSchema type = %T, want json.RawMessage", ObjectStructureTool().InputSchema)
	}
	schema := string(raw)
	for _, want := range []string{
		"DefinedType",
		"ОпределяемыеТипы->DefinedType",
		"Для DefinedType возвращается поле types",
	} {
		if !strings.Contains(schema, want) {
			t.Errorf("object_type schema missing %q", want)
		}
	}
	if strings.Contains(schema, `"enum"`) {
		t.Errorf("object_type must remain free-text (no enum), schema:\n%s", schema)
	}
}

// TestFormatObjectStructure_TypesSorted proves the DefinedType composition is
// rendered in deterministic (Go-canonical sorted) order regardless of the order
// the platform returns the types in, so tool output is stable across runs and
// platform iteration order. Written red against the unsorted renderer.
func TestFormatObjectStructure_TypesSorted(t *testing.T) {
	// Deliberately unsorted input; none is a substring of another.
	input := []string{"Число", "Булево", "Строка", "Дата"}

	want := append([]string(nil), input...)
	sort.Strings(want)

	out := formatObjectStructure(&onec.ObjectStructure{
		Name:    "ЗначениеДоступа",
		Synonym: "Значение доступа",
		Types:   input,
	})

	idx := strings.Index(out, "## Состав типа")
	if idx < 0 {
		t.Fatalf("expected composition block, got:\n%s", out)
	}
	block := out[idx:]

	positions := make([]int, len(want))
	for i, tp := range want {
		p := strings.Index(block, tp)
		if p < 0 {
			t.Fatalf("type %q missing from composition block:\n%s", tp, block)
		}
		positions[i] = p
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] >= positions[i] {
			t.Errorf("composition not in sorted order: %q (pos %d) must precede %q (pos %d)\nblock:\n%s",
				want[i-1], positions[i-1], want[i], positions[i], block)
		}
	}
}

// TestFormatObjectStructure_TypesStable proves rendering is idempotent: the same
// input yields byte-identical output on repeated calls (no map/iteration nondeterminism).
func TestFormatObjectStructure_TypesStable(t *testing.T) {
	mk := func() string {
		return formatObjectStructure(&onec.ObjectStructure{
			Name:    "ЗначениеДоступа",
			Synonym: "Значение доступа",
			Types:   []string{"Справочник.Пользователи", "Булево", "Справочник.ВнешниеПользователи"},
		})
	}
	if a, b := mk(), mk(); a != b {
		t.Errorf("output not stable across calls:\n--- first ---\n%s\n--- second ---\n%s", a, b)
	}
}

// TestObjectStructureHandler_DefinedType_Primitive proves a composition mixing a
// reference type with a primitive (Строка) renders both in the "## Состав типа"
// markdown block.
func TestObjectStructureHandler_DefinedType_Primitive(t *testing.T) {
	const mockResponse = `{
		"name": "ЛюбаяСсылкаИлиСтрока",
		"synonym": "Любая ссылка или строка",
		"types": ["Справочник.Номенклатура", "Строка"]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/DefinedType/ЛюбаяСсылкаИлиСтрока" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "DefinedType",
		"object_name": "ЛюбаяСсылкаИлиСтрока",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	idx := strings.Index(tc.Text, "## Состав типа")
	if idx < 0 {
		t.Fatalf("expected composition block, got:\n%s", tc.Text)
	}
	block := tc.Text[idx:]
	for _, want := range []string{"Справочник.Номенклатура", "Строка"} {
		if !strings.Contains(block, want) {
			t.Errorf("composition block missing %q, got:\n%s", want, block)
		}
	}
}

// TestObjectStructureHandler_DefinedType_Nested proves the Go rendering path
// handles a composition that references another DefinedType without error and
// renders the member string. Real platform .Типы() nested-expansion is not
// asserted here (needs the real-1C gate).
func TestObjectStructureHandler_DefinedType_Nested(t *testing.T) {
	const mockResponse = `{
		"name": "СоставнойЧерезОпределяемый",
		"synonym": "Составной через определяемый тип",
		"types": ["ОпределяемыйТип.ЗначениеДоступа", "Справочник.Организации"]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/DefinedType/СоставнойЧерезОпределяемый" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "DefinedType",
		"object_name": "СоставнойЧерезОпределяемый",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	for _, want := range []string{"ОпределяемыйТип.ЗначениеДоступа", "Справочник.Организации"} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}
}

// leadingSpaceCount returns the number of leading spaces on the first line of
// block that contains marker, failing the test if the marker is absent.
func leadingSpaceCount(t *testing.T, block, marker string) int {
	t.Helper()
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, marker) {
			return len(line) - len(strings.TrimLeft(line, " "))
		}
	}
	t.Fatalf("marker %q not found in:\n%s", marker, block)
	return -1
}

// TestObjectStructureHandler_Subsystem exercises the Subsystem structure path end
// to end: a subsystem with a flat Состав and nested child subsystems (>=2 levels)
// must render its members and an indented subsystem tree.
func TestObjectStructureHandler_Subsystem(t *testing.T) {
	// Top-level content is deliberately unsorted to prove the renderer sorts it.
	const mockResponse = `{
		"name": "Продажи",
		"synonym": "Продажи",
		"content": ["Справочник.Контрагенты", "Документ.РеализацияТоваровУслуг"],
		"subsystems": [
			{
				"name": "Розница",
				"fullName": "Подсистема.Продажи.Подсистема.Розница",
				"synonym": "Розница",
				"content": ["Справочник.Кассы"],
				"subsystems": [
					{
						"name": "Касса",
						"fullName": "Подсистема.Продажи.Подсистема.Розница.Подсистема.Касса",
						"synonym": "Рабочее место кассира",
						"content": ["Документ.ЧекККМ"]
					}
				]
			},
			{
				"name": "Опт",
				"fullName": "Подсистема.Продажи.Подсистема.Опт",
				"synonym": "Оптовые продажи",
				"content": ["Документ.ЗаказПокупателя"]
			}
		]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/Subsystem/Продажи" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "Subsystem",
		"object_name": "Продажи",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := result.Content[0].(*mcp.TextContent)
	for _, want := range []string{
		"Продажи",
		"## Состав",
		"Справочник.Контрагенты",
		"Документ.РеализацияТоваровУслуг",
		"## Подсистемы",
		"Розница",
		"Касса",
		"Документ.ЧекККМ",
		"Опт",
		"Документ.ЗаказПокупателя",
	} {
		if !strings.Contains(tc.Text, want) {
			t.Errorf("expected text to contain %q, got:\n%s", want, tc.Text)
		}
	}

	// Top-level Состав must be sorted: Документ.* precedes Справочник.* (Cyrillic Д < С).
	sostavIdx := strings.Index(tc.Text, "## Состав")
	podsystemIdx := strings.Index(tc.Text, "## Подсистемы")
	sostavBlock := tc.Text[sostavIdx:podsystemIdx]
	if strings.Index(sostavBlock, "Документ.РеализацияТоваровУслуг") >= strings.Index(sostavBlock, "Справочник.Контрагенты") {
		t.Errorf("Состав not sorted, got block:\n%s", sostavBlock)
	}

	// Nested child (Касса, depth 1) must be indented deeper than its parent (Розница, depth 0).
	tree := tc.Text[podsystemIdx:]
	if leadingSpaceCount(t, tree, "**Касса**") <= leadingSpaceCount(t, tree, "**Розница**") {
		t.Errorf("nested subsystem not indented deeper than parent:\n%s", tree)
	}
}

// TestFormatObjectStructure_SubsystemEmpty proves an empty subsystem (no Состав,
// no child subsystems) emits neither the "## Состав" nor "## Подсистемы" block.
func TestFormatObjectStructure_SubsystemEmpty(t *testing.T) {
	out := formatObjectStructure(&onec.ObjectStructure{
		Name:       "ПустаяПодсистема",
		Synonym:    "Пустая подсистема",
		Content:    []string{},
		Subsystems: []onec.SubsystemNode{},
	})
	if strings.Contains(out, "## Состав") {
		t.Errorf("empty Content must not emit '## Состав', got:\n%s", out)
	}
	if strings.Contains(out, "## Подсистемы") {
		t.Errorf("empty Subsystems must not emit '## Подсистемы', got:\n%s", out)
	}
}

// TestFormatObjectStructure_SubsystemNested proves a >=2-level subsystem tree
// renders with strictly increasing indentation per depth.
func TestFormatObjectStructure_SubsystemNested(t *testing.T) {
	out := formatObjectStructure(&onec.ObjectStructure{
		Name:    "Продажи",
		Synonym: "Продажи",
		Subsystems: []onec.SubsystemNode{
			{
				Name: "Розница", Synonym: "Розница",
				Subsystems: []onec.SubsystemNode{
					{
						Name: "Касса", Synonym: "Касса",
						Subsystems: []onec.SubsystemNode{
							{Name: "Смена", Synonym: "Смена"},
						},
					},
				},
			},
		},
	})
	d0 := leadingSpaceCount(t, out, "**Розница**")
	d1 := leadingSpaceCount(t, out, "**Касса**")
	d2 := leadingSpaceCount(t, out, "**Смена**")
	if !(d0 < d1 && d1 < d2) {
		t.Errorf("expected strictly increasing indent per depth, got %d < %d < %d:\n%s", d0, d1, d2, out)
	}
}

// TestFormatObjectStructure_SubsystemSortedNoDash proves the Состав list is sorted
// and neither the Состав nor the Подсистемы block contains an em/en-dash (no-тире rule).
func TestFormatObjectStructure_SubsystemSortedNoDash(t *testing.T) {
	out := formatObjectStructure(&onec.ObjectStructure{
		Name:    "Продажи",
		Synonym: "Продажи",
		Content: []string{"Справочник.Контрагенты", "Документ.РеализацияТоваровУслуг", "Отчет.Продажи"},
		Subsystems: []onec.SubsystemNode{
			{Name: "Розница", Synonym: "Розница", Content: []string{"Справочник.Кассы"}},
		},
	})

	idx := strings.Index(out, "## Состав")
	if idx < 0 {
		t.Fatalf("expected Состав block, got:\n%s", out)
	}
	block := out[idx:]
	if strings.ContainsRune(block, '—') || strings.ContainsRune(block, '–') {
		t.Errorf("Subsystem blocks contain em/en-dash, violates no-тире rule:\n%s", block)
	}

	// Sorted: Документ < Отчет < Справочник (Cyrillic order).
	want := []string{"Документ.РеализацияТоваровУслуг", "Отчет.Продажи", "Справочник.Контрагенты"}
	positions := make([]int, len(want))
	for i, w := range want {
		p := strings.Index(block, w)
		if p < 0 {
			t.Fatalf("member %q missing:\n%s", w, block)
		}
		positions[i] = p
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] >= positions[i] {
			t.Errorf("Состав not sorted: %q must precede %q\nblock:\n%s", want[i-1], want[i], block)
		}
	}
}

// TestFormatObjectStructure_SubsystemBackCompat proves an object WITHOUT
// Content/Subsystems renders byte-identically to the pre-feature output (the new
// blocks are strictly additive).
func TestFormatObjectStructure_SubsystemBackCompat(t *testing.T) {
	obj := &onec.ObjectStructure{
		Name:    "Контрагенты",
		Synonym: "Контрагенты",
		Attributes: []onec.Attribute{
			{Name: "ИНН", Synonym: "ИНН", Type: "Строка"},
		},
	}
	const want = "# Контрагенты (Контрагенты)\n\n## Реквизиты\n- **ИНН** (ИНН) — Строка\n\n"
	got := formatObjectStructure(obj)
	if got != want {
		t.Errorf("back-compat output changed.\n got: %q\nwant: %q", got, want)
	}
}

// TestFormatObjectStructure_SubsystemSurfacesFullName (bug #1) proves the
// subsystem tree render surfaces each node's full metadata name, the only unique
// key when two nested subsystems share a short name under different roots.
// Without it, an AI cannot tell the two "Касса" nodes apart.
func TestFormatObjectStructure_SubsystemSurfacesFullName(t *testing.T) {
	obj := &onec.ObjectStructure{
		Name:    "Продажи",
		Synonym: "Продажи",
		Subsystems: []onec.SubsystemNode{
			{
				Name: "Розница", Synonym: "Розница", FullName: "Подсистема.Продажи.Подсистема.Розница",
				Subsystems: []onec.SubsystemNode{
					{Name: "Касса", Synonym: "Рабочее место", FullName: "Подсистема.Продажи.Подсистема.Розница.Подсистема.Касса"},
				},
			},
			{
				Name: "Опт", Synonym: "Опт", FullName: "Подсистема.Продажи.Подсистема.Опт",
				Subsystems: []onec.SubsystemNode{
					{Name: "Касса", Synonym: "Касса опта", FullName: "Подсистема.Продажи.Подсистема.Опт.Подсистема.Касса"},
				},
			},
		},
	}
	out := formatObjectStructure(obj)
	// Both same-short-name nodes must expose their distinct full names.
	mustContain(t, out,
		"Подсистема.Продажи.Подсистема.Розница.Подсистема.Касса",
		"Подсистема.Продажи.Подсистема.Опт.Подсистема.Касса",
	)
	// The subsystem tree is customer-facing RU: no em/en dash.
	idx := strings.Index(out, "## Подсистемы")
	if idx < 0 {
		t.Fatalf("expected Подсистемы block, got:\n%s", out)
	}
	if strings.ContainsRune(out[idx:], '—') || strings.ContainsRune(out[idx:], '–') {
		t.Errorf("subsystem tree contains em/en-dash, violates no-тире rule:\n%s", out[idx:])
	}
}

// TestFormatObjectStructure_SubsystemAmbiguous (bug #1) proves that when the
// server cannot resolve a single subsystem from an ambiguous short name and
// returns the full-name candidates, the render surfaces them as a clear signal
// (no тире) instead of an empty structure.
func TestFormatObjectStructure_SubsystemAmbiguous(t *testing.T) {
	obj := &onec.ObjectStructure{
		Ambiguous: []string{
			"Подсистема.Продажи.Подсистема.Розница.Подсистема.Касса",
			"Подсистема.Продажи.Подсистема.Опт.Подсистема.Касса",
		},
	}
	out := formatObjectStructure(obj)
	mustContain(t, out,
		"Неоднозначное имя подсистемы",
		"Подсистема.Продажи.Подсистема.Розница.Подсистема.Касса",
		"Подсистема.Продажи.Подсистема.Опт.Подсистема.Касса",
	)
	// An ambiguity result must not fall through to an empty normal header.
	mustNotContain(t, out, "#  (")
	if strings.ContainsRune(out, '—') || strings.ContainsRune(out, '–') {
		t.Errorf("ambiguity message contains em/en-dash, violates no-тире rule:\n%s", out)
	}
}

// TestObjectStructureTool_SubsystemSchema proves the tool description advertises
// Subsystem as an object_type while keeping object_type free-text (no enum).
func TestObjectStructureTool_SubsystemSchema(t *testing.T) {
	raw, ok := ObjectStructureTool().InputSchema.(json.RawMessage)
	if !ok {
		t.Fatalf("InputSchema type = %T, want json.RawMessage", ObjectStructureTool().InputSchema)
	}
	schema := string(raw)
	if !strings.Contains(schema, "Subsystem") {
		t.Errorf("object_type schema missing Subsystem:\n%s", schema)
	}
	if strings.Contains(schema, `"enum"`) {
		t.Errorf("object_type must remain free-text (no enum), schema:\n%s", schema)
	}
}

// TestFormatObjectStructure_WarningsDiagnostics (BUG-3b) proves an object carrying
// non-fatal subsystem tree-builder warnings surfaces a diagnostics line right
// after the header so a truncated (partial) membership view is visible instead of
// being silently trusted as complete.
func TestFormatObjectStructure_WarningsDiagnostics(t *testing.T) {
	out := formatObjectStructure(&onec.ObjectStructure{
		Name:    "Продажи",
		Synonym: "Продажи",
		Content: []string{"Документ.РеализацияТоваровУслуг"},
		Warnings: []string{
			"Подсистема Розница: Ошибка доступа к составу",
			"Подсистема Опт: Недоступно",
		},
	})
	mustContain(t, out,
		"Диагностика",
		"предупреждений: 2",
		"Подсистема Розница: Ошибка доступа к составу",
		"Подсистема Опт: Недоступно",
	)
	// The diagnostics line is customer-facing RU: no em/en dash.
	if strings.ContainsRune(out, '—') || strings.ContainsRune(out, '–') {
		t.Errorf("warnings diagnostics contains em/en-dash, violates no-тире rule:\n%s", out)
	}
	// Surfaced prominently: the diagnostics line must precede the Состав block.
	di := strings.Index(out, "Диагностика")
	ci := strings.Index(out, "## Состав")
	if di < 0 || (ci >= 0 && di > ci) {
		t.Errorf("diagnostics must appear before the Состав block:\n%s", out)
	}
}

// TestFormatObjectStructure_WarningsBackCompat proves the warnings channel is
// strictly additive: an object with no warnings renders byte-identically whether
// the field is nil or an explicitly empty slice, and never emits a diagnostics line.
func TestFormatObjectStructure_WarningsBackCompat(t *testing.T) {
	base := onec.ObjectStructure{
		Name:    "Продажи",
		Synonym: "Продажи",
		Content: []string{"Документ.РеализацияТоваровУслуг"},
		Subsystems: []onec.SubsystemNode{
			{Name: "Розница", Synonym: "Розница", Content: []string{"Справочник.Кассы"}},
		},
	}
	nilW := base
	nilW.Warnings = nil
	emptyW := base
	emptyW.Warnings = []string{}

	a := formatObjectStructure(&nilW)
	b := formatObjectStructure(&emptyW)
	if a != b {
		t.Errorf("empty Warnings changed output.\n--- nil ---\n%q\n--- empty ---\n%q", a, b)
	}
	if strings.Contains(a, "Диагностика") {
		t.Errorf("no-warnings output must not contain a diagnostics line:\n%s", a)
	}
}

// TestObjectStructure_WarningsDecode proves the "warnings" JSON key round-trips
// into ObjectStructure.Warnings, and that its absence decodes to a nil slice
// (omitempty back-compat).
func TestObjectStructure_WarningsDecode(t *testing.T) {
	const withW = `{"name":"Продажи","synonym":"Продажи","attributes":[],"warnings":["Подсистема Розница: Недоступно"]}`
	var a onec.ObjectStructure
	if err := json.Unmarshal([]byte(withW), &a); err != nil {
		t.Fatalf("decode with warnings: %v", err)
	}
	if len(a.Warnings) != 1 || a.Warnings[0] != "Подсистема Розница: Недоступно" {
		t.Errorf("Warnings = %v, want one entry", a.Warnings)
	}

	const noW = `{"name":"Продажи","synonym":"Продажи","attributes":[]}`
	var b onec.ObjectStructure
	if err := json.Unmarshal([]byte(noW), &b); err != nil {
		t.Fatalf("decode without warnings: %v", err)
	}
	if b.Warnings != nil {
		t.Errorf("absent warnings must decode to nil, got %v", b.Warnings)
	}
}

// TestObjectStructureHandler_SubsystemWarnings (BUG-3b) exercises the warnings
// channel end to end: a Subsystem object whose JSON carries a "warnings" array
// must render both the diagnostics line and the normal membership blocks.
func TestObjectStructureHandler_SubsystemWarnings(t *testing.T) {
	const mockResponse = `{
		"name": "Продажи",
		"synonym": "Продажи",
		"content": ["Документ.РеализацияТоваровУслуг"],
		"subsystems": [
			{"name": "Розница", "fullName": "Подсистема.Продажи.Подсистема.Розница", "synonym": "Розница", "content": ["Справочник.Кассы"]}
		],
		"warnings": ["Подсистема Розница: Ошибка чтения состава"]
	}`

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object/Subsystem/Продажи" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockServer.Close()

	client := onec.NewClient(mockServer.URL, "", "")
	handler := NewObjectStructureHandler(client)

	args, _ := json.Marshal(map[string]string{
		"object_type": "Subsystem",
		"object_name": "Продажи",
	})
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_object_structure",
			Arguments: args,
		},
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := result.Content[0].(*mcp.TextContent)
	mustContain(t, tc.Text,
		"Диагностика",
		"Подсистема Розница: Ошибка чтения состава",
		"## Состав",
		"Документ.РеализацияТоваровУслуг",
	)
}
