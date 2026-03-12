package dump

import (
	"strings"

	"github.com/blevesearch/bleve/v2/analysis"
	_ "github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	_ "github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	_ "github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/registry"

	"github.com/feenlace/mcp-1c/bsl"
)

const (
	analyzerBSL           = "bsl"
	tokenFilterBSLSynonym = "bsl_synonym"
)

// bslSynonymFilter expands tokens with BSL bilingual synonyms.
// For each token, if a synonym exists (e.g., "процедура" <-> "procedure"),
// both the original and the synonym are emitted at the same position.
type bslSynonymFilter struct {
	synonyms map[string]string
}

func newBSLSynonymFilter() *bslSynonymFilter {
	return &bslSynonymFilter{synonyms: buildSynonymMap()}
}

func (f *bslSynonymFilter) Filter(input analysis.TokenStream) analysis.TokenStream {
	output := make(analysis.TokenStream, 0, len(input)*2)
	for _, token := range input {
		output = append(output, token)
		term := string(token.Term)
		if syn, ok := f.synonyms[term]; ok {
			output = append(output, &analysis.Token{
				Term:     []byte(syn),
				Position: token.Position,
				Start:    token.Start,
				End:      token.End,
				Type:     token.Type,
			})
		}
	}
	return output
}

func init() {
	// Register the BSL synonym token filter in the global Bleve registry.
	// This allows referencing it by name in custom analyzer configs.
	err := registry.RegisterTokenFilter(tokenFilterBSLSynonym,
		func(config map[string]any, cache *registry.Cache) (analysis.TokenFilter, error) {
			return newBSLSynonymFilter(), nil
		},
	)
	if err != nil {
		panic("failed to register BSL synonym token filter: " + err.Error())
	}
}

// buildSynonymMap returns a bidirectional map of BSL synonyms (lowercase).
// Sources: ~40 hardcoded language keyword pairs + built-in function pairs.
func buildSynonymMap() map[string]string {
	// BSL language keywords (Russian <-> English).
	keywords := map[string]string{
		"процедура":            "procedure",
		"конецпроцедуры":       "endprocedure",
		"функция":              "function",
		"конецфункции":         "endfunction",
		"если":                 "if",
		"тогда":                "then",
		"иначе":                "else",
		"иначеесли":            "elsif",
		"конецесли":            "endif",
		"для":                  "for",
		"каждого":              "each",
		"из":                   "in",
		"по":                   "to",
		"цикл":                 "do",
		"пока":                 "while",
		"конеццикла":           "enddo",
		"возврат":              "return",
		"попытка":              "try",
		"исключение":           "except",
		"конецпопытки":         "endtry",
		"новый":                "new",
		"перем":                "var",
		"экспорт":              "export",
		"знач":                 "val",
		"не":                   "not",
		"и":                    "and",
		"или":                  "or",
		"истина":               "true",
		"ложь":                 "false",
		"неопределено":         "undefined",
		"выбрать":              "select",
		"где":                  "where",
		"как":                  "as",
		"левое":                "left",
		"внутреннее":           "inner",
		"соединение":           "join",
		"сгруппировать":        "group",
		"упорядочить":          "order",
		"имеющие":              "having",
		"различные":            "distinct",
		"объединить":           "union",
		"выразить":             "cast",
		"количество":           "count",
		"сумма":                "sum",
		"максимум":             "max",
		"минимум":              "min",
		"среднее":              "avg",
		"добавитьобработчик":   "addhandler",
		"вызватьисключение":    "raise",
		"выполнить":            "execute",
		"перейти":              "goto",
		"продолжить":           "continue",
		"прервать":             "break",
	}

	m := make(map[string]string, len(keywords)*2+len(bsl.BuiltinFunctions)*2)

	for ru, en := range keywords {
		if ru == en {
			continue
		}
		m[ru] = en
		m[en] = ru
	}

	// Built-in platform functions from bsl.BuiltinFunctions.
	// Skip the pair entirely if either key already exists (avoids broken
	// bidirectional chains when a keyword and a built-in share an English name).
	for _, fn := range bsl.BuiltinFunctions {
		ru := strings.ToLower(fn.Name)
		en := strings.ToLower(fn.NameEn)
		if ru == "" || en == "" || ru == en {
			continue
		}
		_, ruExists := m[ru]
		_, enExists := m[en]
		if ruExists || enExists {
			continue
		}
		m[ru] = en
		m[en] = ru
	}

	return m
}

// buildBSLMapping creates the Bleve index mapping for BSL module documents.
// Fields: name (keyword), category (keyword), module (keyword), content (full-text with bsl analyzer).
// Default mapping is disabled; only "module" document type is indexed.
func buildBSLMapping() *mapping.IndexMappingImpl {
	im := mapping.NewIndexMapping()

	// Register custom BSL analyzer: unicode tokenizer -> lowercase -> bsl_synonym.
	err := im.AddCustomAnalyzer(analyzerBSL, map[string]any{
		"type":          "custom",
		"tokenizer":     "unicode",
		"token_filters": []any{"to_lower", tokenFilterBSLSynonym},
	})
	if err != nil {
		// Programming error — analyzer config is static, should never fail.
		panic("failed to register BSL analyzer: " + err.Error())
	}

	// Keyword fields (exact match, no analysis).
	// Store=false: we never read these back from Bleve (content served from contentByName).
	nameField := mapping.NewKeywordFieldMapping()
	nameField.Store = false
	nameField.DocValues = false
	categoryField := mapping.NewKeywordFieldMapping()
	categoryField.Store = false
	categoryField.DocValues = false
	moduleField := mapping.NewKeywordFieldMapping()
	moduleField.Store = false
	moduleField.DocValues = false

	// Full-text content field with BSL analyzer.
	// Store=false: full text served from contentByName, not from Bleve stored fields.
	contentField := mapping.NewTextFieldMapping()
	contentField.Analyzer = analyzerBSL
	contentField.Store = false
	contentField.IncludeInAll = false
	contentField.IncludeTermVectors = false

	// Document mapping for BSL modules.
	docMapping := mapping.NewDocumentMapping()
	docMapping.AddFieldMappingsAt("name", nameField)
	docMapping.AddFieldMappingsAt("category", categoryField)
	docMapping.AddFieldMappingsAt("module", moduleField)
	docMapping.AddFieldMappingsAt("content", contentField)

	im.AddDocumentMapping("module", docMapping)
	im.DefaultMapping.Enabled = false

	return im
}
