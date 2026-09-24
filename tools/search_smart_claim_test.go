package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestSearchCodeDescription_SmartModeClaimsHold pins what the search_code
// description says about the smart mode against what the published tool does.
//
// None of those clauses is decided where it is written. The tokenizer, the lower
// casing, the synonym filter, the absence of a stemmer and the OR between the
// words of a query live in dump (analyzer.go and Index.searchSmart), and the
// choice of mode lives in NewSearchCodeHandler, so a change in either could make
// the published sentence false without touching a line of it. The test therefore
// calls the handler the way a client does, with the argument names and the mode
// values the schema declares, and it first checks that the description still says
// every clause measured below, so the text and the test cannot drift apart either.
//
// NO MODULE HERE HOLDS A CHAIN WORD STANDING ALONE. A module that did would give
// smart a hit of its own for that query, and a smart that fell back to a substring
// scan whenever it found nothing would then never be exercised.
func TestSearchCodeDescription_SmartModeClaimsHold(t *testing.T) {
	// otherForm has the length of splitWord and a different last letter, so a
	// stemmer that drops one final letter maps both to one term. truncated is the
	// start of splitWord, which a prefix match would accept.
	const (
		chainWord  = "Номенклатура"
		chain      = "Справочники.Номенклатура.НайтиПоКоду"
		latinWord  = "Items"
		latinChain = "Catalogs.Items.FindByCode"
		splitWord  = "Организация"
		otherForm  = "Организации"
		truncated  = "Организац"
	)

	desc := SearchCodeTool().Description
	for _, quoted := range []string{
		"хотя бы одно слово запроса или его BSL-синоним",
		"Регистр не важен",
		"словоформа должна совпасть",
		"смысл фразы он не ищет",
		"Точка между двумя буквами русского или латинского алфавита слово не делит",
		"модуль, где " + chainWord + " стоит только внутри имён вроде " + chain,
		"по запросу " + chainWord + " этот режим не находит",
		"такие вхождения находит режим exact",
	} {
		if !strings.Contains(desc, quoted) {
			t.Fatalf("the description no longer says %q, so this test no longer checks what it "+
				"publishes:\n%s", quoted, desc)
		}
	}

	// The argument names and the mode values are the schema's, not this test's.
	raw, ok := SearchCodeTool().InputSchema.(json.RawMessage)
	if !ok {
		t.Fatalf("search_code input schema is %T, want json.RawMessage", SearchCodeTool().InputSchema)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("search_code input schema does not parse: %v", err)
	}
	if _, ok := schema.Properties["query"]; !ok {
		t.Fatalf("the schema declares no query argument: %s", raw)
	}
	if m, ok := schema.Properties["mode"]; !ok || !slices.Contains(m.Enum, "smart") || !slices.Contains(m.Enum, "exact") {
		t.Fatalf("the schema does not declare mode with smart and exact: %s", raw)
	}

	// Module name -> body. The names occur in no body and in no query below, and no
	// name is part of another, so finding a name in an answer means that module.
	modules := map[string]string{
		"Кириллица":   "Х = " + chain + "(1);\n",
		"НачалоИмени": "Х = " + chainWord + ".Код;\n",
		"Латиница":    "X = " + latinChain + "(1);\n",
		"Цифра":       "Х = Т1." + splitWord + ";\n",
		"Подчерк":     "Х = Мой_." + splitWord + ";\n",
		"Синоним":     "Процедура Проверить()\nКонецПроцедуры\n",
	}
	dir := t.TempDir()
	for name, body := range modules {
		mkBSL(t, dir, "CommonModules/"+name+"/Ext/Module.bsl", body)
	}
	index, err := dump.NewIndex(dir, t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer index.Close()
	waitReady(t, index, 30*time.Second)
	handler := NewSearchCodeHandler(index)

	call := func(args map[string]any) []string {
		t.Helper()
		body, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		res, err := handler(context.Background(), &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{Name: "search_code", Arguments: body},
		})
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(res.Content) == 0 {
			t.Fatalf("%v: the answer has no content", args)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if res.IsError {
			t.Fatalf("%v was refused, so nothing below is a search result:\n%s", args, text)
		}
		var got []string
		for name := range modules {
			if strings.Contains(text, "ОбщийМодуль."+name+".Модуль") {
				got = append(got, name)
			}
		}
		slices.Sort(got)
		return got
	}
	smart := func(query string) []string {
		t.Helper()
		return call(map[string]any{"query": query, "mode": "smart"})
	}

	// CONTROL FIRST: exact reads every chain, so the module names are recognised in
	// an answer and the words really are in those modules. «такие вхождения находит
	// режим exact» is this same set of calls.
	if got := call(map[string]any{"query": chainWord, "mode": "exact"}); !slices.Contains(got, "Кириллица") || !slices.Contains(got, "НачалоИмени") {
		t.Fatalf("control failed: exact does not find %q inside %s and at the start of %s.Код, "+
			"so an empty smart answer below would mean nothing. Found: %v", chainWord, chain, chainWord, got)
	}
	if got := call(map[string]any{"query": latinWord, "mode": "exact"}); !slices.Contains(got, "Латиница") {
		t.Fatalf("control failed: exact does not find %q inside %s. Found: %v", latinWord, latinChain, got)
	}

	// A dot between two letters does not split the name, wherever the word sits in
	// it, in smart and in the default mode, for the Russian and the Latin alphabet.
	for _, args := range []map[string]any{
		{"query": chainWord, "mode": "smart"},
		{"query": chainWord},
		{"query": latinWord, "mode": "smart"},
		{"query": latinWord},
	} {
		if got := call(args); len(got) != 0 {
			t.Errorf("%v found %v, yet the description says a dot between two letters does not "+
				"split the name", args, got)
		}
	}

	// A digit or an underscore before the dot does split it, which is why the
	// description names letters rather than every dot.
	if got := smart(splitWord); !slices.Contains(got, "Цифра") || !slices.Contains(got, "Подчерк") {
		t.Errorf("smart does not find %q after a digit or an underscore before the dot, so the "+
			"letters in the description's rule no longer mark its edge. Found: %v", splitWord, got)
	}
	if got := smart(strings.ToUpper(splitWord)); !slices.Contains(got, "Цифра") {
		t.Errorf("smart misses the upper-case query %q, so «Регистр не важен» is false. Found: %v",
			strings.ToUpper(splitWord), got)
	}
	if got := smart(otherForm); len(got) != 0 {
		t.Errorf("smart finds %v through %q, so «словоформа должна совпасть» is false",
			got, otherForm)
	}
	if got := smart(truncated); len(got) != 0 {
		t.Errorf("smart finds %v through the truncated %q, so «словоформа должна совпасть» is false",
			got, truncated)
	}
	if got := smart(splitWord + " Отсутствующее"); !slices.Contains(got, "Цифра") {
		t.Errorf("smart misses a module that holds one of two query words, so «хотя бы одно "+
			"слово запроса» is false. Found: %v", got)
	}
	if got := smart("Procedure"); !slices.Contains(got, "Синоним") {
		t.Errorf("smart does not find Процедура through Procedure, so «или его BSL-синоним» is "+
			"false. Found: %v", got)
	}
	// Words that describe the chain module without occurring in it. None of them is
	// a BSL keyword, so no synonym can bring a module in.
	if got := smart("перечень товаров"); len(got) != 0 {
		t.Errorf("smart finds %v for a phrase none of whose words occurs in the code, so «смысл "+
			"фразы он не ищет» is false", got)
	}
}
