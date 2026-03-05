package onec

// MetadataTree represents the metadata tree of a 1C configuration.
type MetadataTree struct {
	Catalogs  []string `json:"Справочники"`
	Documents []string `json:"Документы"`
	Registers []string `json:"Регистры"`
}

// ObjectStructure represents the structure of a 1C metadata object.
type ObjectStructure struct {
	Name         string        `json:"Имя"`
	Synonym      string        `json:"Синоним"`
	Attributes   []Attribute   `json:"Реквизиты"`
	TabularParts []TabularPart `json:"ТабличныеЧасти"`
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
