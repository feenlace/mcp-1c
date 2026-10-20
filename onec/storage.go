package onec

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// GetStorageStructure fetches the live physical storage map for one metadata
// object using this client's transport, cancellation and response limits.
// Names are metadata identifiers, not physical table names or qualified paths.
func (c *Client) GetStorageStructure(ctx context.Context, objectType, objectName string) (*StorageStructure, error) {
	if objectType == "" || objectName == "" {
		return nil, fmt.Errorf("object_type and object_name are required")
	}
	prefix := storageMetadataPrefix(objectType)
	if prefix == "" {
		return nil, fmt.Errorf("unsupported storage object_type: %q", objectType)
	}
	for i, r := range objectName {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			return nil, fmt.Errorf("object_name must be a metadata identifier: letters, digits and underscore; the first character cannot be a digit")
		}
	}

	endpoint := "/storage/" + url.PathEscape(objectType) + "/" + url.PathEscape(objectName)
	var st StorageStructure
	if err := c.Get(ctx, endpoint, &st); err != nil {
		return nil, err
	}
	if !strings.EqualFold(st.Name, objectName) || !strings.EqualFold(st.FullName, prefix+"."+st.Name) || st.Tables == nil {
		return nil, fmt.Errorf("invalid storage structure response: expected the selected object's name, full_name and tables array")
	}
	for _, table := range st.Tables {
		if table.StorageName == "" || table.Fields == nil || table.Indexes == nil {
			return nil, fmt.Errorf("invalid storage structure response: each table requires storage_name, fields and indexes arrays")
		}
		for _, field := range table.Fields {
			if field.StorageName == "" {
				return nil, fmt.Errorf("invalid storage structure response: a table field has no storage_name")
			}
		}
		for _, index := range table.Indexes {
			if index.Fields == nil {
				return nil, fmt.Errorf("invalid storage structure response: an index has no fields array")
			}
		}
	}
	return &st, nil
}

func storageMetadataPrefix(objectType string) string {
	switch objectType {
	case "Catalog":
		return "Справочник"
	case "Document":
		return "Документ"
	case "Enum":
		return "Перечисление"
	case "InformationRegister":
		return "РегистрСведений"
	case "AccumulationRegister":
		return "РегистрНакопления"
	case "AccountingRegister":
		return "РегистрБухгалтерии"
	case "CalculationRegister":
		return "РегистрРасчета"
	case "ChartOfAccounts":
		return "ПланСчетов"
	case "ChartOfCharacteristicTypes":
		return "ПланВидовХарактеристик"
	case "ChartOfCalculationTypes":
		return "ПланВидовРасчета"
	case "ExchangePlan":
		return "ПланОбмена"
	case "BusinessProcess":
		return "БизнесПроцесс"
	case "Task":
		return "Задача"
	case "DataProcessor":
		return "Обработка"
	case "Report":
		return "Отчет"
	case "DefinedType":
		return "ОпределяемыйТип"
	default:
		return ""
	}
}
