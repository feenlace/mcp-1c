package onec

// ObjectStructure represents the structure of a 1C metadata object.
type ObjectStructure struct {
	Name         string        `json:"Имя"`
	Synonym      string        `json:"Синоним"`
	Attributes   []Attribute   `json:"Реквизиты"`
	TabularParts []TabularPart `json:"ТабличныеЧасти,omitempty"`
	Dimensions   []Attribute   `json:"Измерения,omitempty"`
	Resources    []Attribute   `json:"Ресурсы,omitempty"`
}

// Attribute represents a metadata object attribute.
type Attribute struct {
	Name    string `json:"Имя"`
	Synonym string `json:"Синоним"`
	Type    string `json:"Тип"`
}

// TabularPart represents a tabular part of a metadata object.
type TabularPart struct {
	Name       string      `json:"Имя"`
	Attributes []Attribute `json:"Реквизиты"`
}

// ModuleCode represents the source code of a 1C module.
type ModuleCode struct {
	Name       string `json:"Имя"`
	ModuleKind string `json:"ВидМодуля"`
	Code       string `json:"Код"`
}

// QueryRequest is the request body for the query endpoint.
type QueryRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// QueryResult is the response from the query endpoint.
type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated"`
}

// VersionInfo represents the extension version response.
type VersionInfo struct {
	Version string `json:"version"`
}

// SearchRequest is the request body for the search endpoint.
type SearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// SearchResult is the response from the search endpoint.
type SearchResult struct {
	Matches []SearchMatch `json:"matches"`
	Total   int           `json:"total"`
}

// SearchMatch represents a single search result.
type SearchMatch struct {
	Module  string `json:"module"`
	Line    int    `json:"line"`
	Context string `json:"context"`
}

// FormStructure represents the structure of a 1C form.
type FormStructure struct {
	Name     string        `json:"Имя"`
	Title    string        `json:"Заголовок"`
	Elements []FormElement `json:"Элементы"`
	Commands []FormCommand `json:"Команды,omitempty"`
	Handlers []FormHandler `json:"Обработчики,omitempty"`
}

// FormElement represents an element on a 1C form.
type FormElement struct {
	Name     string `json:"Имя"`
	Type     string `json:"Тип"`
	Title    string `json:"Заголовок,omitempty"`
	DataPath string `json:"ПутьКДанным,omitempty"`
}

// FormCommand represents a form command.
type FormCommand struct {
	Name   string `json:"Имя"`
	Action string `json:"Действие"`
}

// FormHandler represents an event handler on a form.
type FormHandler struct {
	Event   string `json:"Событие"`
	Handler string `json:"Обработчик"`
}

// ValidateQueryRequest is the request body for the validate-query endpoint.
type ValidateQueryRequest struct {
	Query string `json:"query"`
}

// ValidateQueryResult is the response from the validate-query endpoint.
type ValidateQueryResult struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
}
