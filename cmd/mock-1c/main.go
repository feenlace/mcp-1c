package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

// Attribute represents a metadata attribute (requisite).
type Attribute struct {
	Name    string `json:"Имя"`
	Synonym string `json:"Синоним"`
	Type    string `json:"Тип"`
}

// TabularSection represents a tabular part of a metadata object.
type TabularSection struct {
	Name       string      `json:"Имя"`
	Attributes []Attribute `json:"Реквизиты"`
}

// ObjectMeta represents the full structure of a metadata object.
type ObjectMeta struct {
	Name            string           `json:"Имя"`
	Synonym         string           `json:"Синоним"`
	Attributes      []Attribute      `json:"Реквизиты"`
	TabularSections []TabularSection `json:"ТабличныеЧасти,omitempty"`
	Dimensions      []Attribute      `json:"Измерения,omitempty"`
	Resources       []Attribute      `json:"Ресурсы,omitempty"`
}

// objectKey combines type and name for map lookup.
type objectKey struct {
	typ  string
	name string
}

var (
	metadata = map[string][]string{
		"Справочники": {
			"Контрагенты",
			"Номенклатура",
			"Организации",
			"Сотрудники",
			"Валюты",
			"Склады",
			"БанковскиеСчета",
			"ДоговорыКонтрагентов",
			"ЕдиницыИзмерения",
		},
		"Документы": {
			"РеализацияТоваровУслуг",
			"ПоступлениеТоваровУслуг",
			"СчетНаОплатуПокупателю",
			"ПлатежноеПоручение",
			"КассовыйОрдер",
			"АвансовыйОтчет",
			"ОперацияБух",
		},
		"РегистрыСведений": {
			"КурсыВалют",
			"АдресныйКлассификатор",
			"НастройкиУчетнойПолитики",
		},
		"РегистрыНакопления": {
			"ТоварыНаСкладах",
			"ВзаиморасчетыСКонтрагентами",
		},
		"РегистрыБухгалтерии": {
			"Хозрасчетный",
		},
		"ОбщиеМодули": {
			"ОбщегоНазначения",
			"ОбщегоНазначенияКлиентСервер",
			"УправлениеПечатью",
		},
	}

	objects = map[objectKey]ObjectMeta{
		{typ: "Document", name: "РеализацияТоваровУслуг"}: {
			Name:    "РеализацияТоваровУслуг",
			Synonym: "Реализация (акты, накладные, УПД)",
			Attributes: []Attribute{
				{Name: "Контрагент", Synonym: "Контрагент", Type: "СправочникСсылка.Контрагенты"},
				{Name: "Организация", Synonym: "Организация", Type: "СправочникСсылка.Организации"},
				{Name: "Склад", Synonym: "Склад", Type: "СправочникСсылка.Склады"},
				{Name: "Валюта", Synonym: "Валюта расчётов", Type: "СправочникСсылка.Валюты"},
				{Name: "ДоговорКонтрагента", Synonym: "Договор", Type: "СправочникСсылка.ДоговорыКонтрагентов"},
				{Name: "СуммаДокумента", Synonym: "Сумма", Type: "Число"},
				{Name: "Комментарий", Synonym: "Комментарий", Type: "Строка"},
			},
			TabularSections: []TabularSection{
				{
					Name: "Товары",
					Attributes: []Attribute{
						{Name: "Номенклатура", Synonym: "Номенклатура", Type: "СправочникСсылка.Номенклатура"},
						{Name: "Количество", Synonym: "Количество", Type: "Число"},
						{Name: "Цена", Synonym: "Цена", Type: "Число"},
						{Name: "Сумма", Synonym: "Сумма", Type: "Число"},
						{Name: "СтавкаНДС", Synonym: "Ставка НДС", Type: "ПеречислениеСсылка.СтавкиНДС"},
						{Name: "СуммаНДС", Synonym: "Сумма НДС", Type: "Число"},
					},
				},
				{
					Name: "Услуги",
					Attributes: []Attribute{
						{Name: "Номенклатура", Synonym: "Номенклатура", Type: "СправочникСсылка.Номенклатура"},
						{Name: "Количество", Synonym: "Количество", Type: "Число"},
						{Name: "Цена", Synonym: "Цена", Type: "Число"},
						{Name: "Сумма", Synonym: "Сумма", Type: "Число"},
						{Name: "СодержаниеУслуги", Synonym: "Содержание", Type: "Строка"},
					},
				},
			},
		},
		{typ: "Catalog", name: "Контрагенты"}: {
			Name:    "Контрагенты",
			Synonym: "Контрагенты",
			Attributes: []Attribute{
				{Name: "ИНН", Synonym: "ИНН", Type: "Строка"},
				{Name: "КПП", Synonym: "КПП", Type: "Строка"},
				{Name: "НаименованиеПолное", Synonym: "Полное наименование", Type: "Строка"},
				{Name: "ЮридическийАдрес", Synonym: "Юридический адрес", Type: "Строка"},
				{Name: "ОсновнойДоговор", Synonym: "Основной договор", Type: "СправочникСсылка.ДоговорыКонтрагентов"},
				{Name: "ОсновнойБанковскийСчет", Synonym: "Основной банковский счёт", Type: "СправочникСсылка.БанковскиеСчета"},
			},
			TabularSections: []TabularSection{
				{
					Name: "КонтактнаяИнформация",
					Attributes: []Attribute{
						{Name: "Тип", Synonym: "Тип", Type: "ПеречислениеСсылка.ТипыКонтактнойИнформации"},
						{Name: "Представление", Synonym: "Представление", Type: "Строка"},
					},
				},
			},
		},
		{typ: "Catalog", name: "Номенклатура"}: {
			Name:    "Номенклатура",
			Synonym: "Номенклатура",
			Attributes: []Attribute{
				{Name: "Артикул", Synonym: "Артикул", Type: "Строка"},
				{Name: "ЕдиницаИзмерения", Synonym: "Единица измерения", Type: "СправочникСсылка.ЕдиницыИзмерения"},
				{Name: "ВидНоменклатуры", Synonym: "Вид номенклатуры", Type: "ПеречислениеСсылка.ВидыНоменклатуры"},
				{Name: "СтавкаНДС", Synonym: "Ставка НДС", Type: "ПеречислениеСсылка.СтавкиНДС"},
				{Name: "Описание", Synonym: "Описание", Type: "Строка"},
			},
		},
		{typ: "AccumulationRegister", name: "ТоварыНаСкладах"}: {
			Name:    "ТоварыНаСкладах",
			Synonym: "Товары на складах",
			Dimensions: []Attribute{
				{Name: "Номенклатура", Synonym: "Номенклатура", Type: "СправочникСсылка.Номенклатура"},
				{Name: "Склад", Synonym: "Склад", Type: "СправочникСсылка.Склады"},
			},
			Resources: []Attribute{
				{Name: "Количество", Synonym: "Количество", Type: "Число"},
			},
			Attributes: []Attribute{},
		},
	}

	modules = map[string]string{
		"Document/РеализацияТоваровУслуг/ObjectModule": "Процедура ОбработкаПроведения(Отказ, РежимПроведения)\n\tДвижения.ТоварыНаСкладах.Записывать = Истина;\n\tДля Каждого ТекСтрокаТовары Из Товары Цикл\n\t\tДвижение = Движения.ТоварыНаСкладах.Добавить();\n\t\tДвижение.ВидДвижения = ВидДвиженияНакопления.Расход;\n\t\tДвижение.Период = Дата;\n\t\tДвижение.Номенклатура = ТекСтрокаТовары.Номенклатура;\n\t\tДвижение.Склад = Склад;\n\t\tДвижение.Количество = ТекСтрокаТовары.Количество;\n\tКонецЦикла;\nКонецПроцедуры",
		"CommonModule/ОбщегоНазначения/CommonModule": "Функция ТекущаяДатаСеанса() Экспорт\n\tВозврат ТекущаяДата();\nКонецФункции",
	}
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func handleMetadata(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	writeJSON(w, http.StatusOK, metadata)
}

func handleObject(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)

	// Parse path: /mcp/object/{type}/{name}
	path := strings.TrimPrefix(r.URL.Path, "/mcp/object/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid path. Expected /mcp/object/{type}/{name}",
		})
		return
	}

	key := objectKey{typ: parts[0], name: parts[1]}
	obj, ok := objects[key]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Object not found",
		})
		return
	}

	writeJSON(w, http.StatusOK, obj)
}

func handleModule(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)

	// Parse path: /mcp/module/{type}/{name}/{moduleType}
	path := strings.TrimPrefix(r.URL.Path, "/mcp/module/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Invalid path. Expected /mcp/module/{type}/{name}/{moduleType}",
		})
		return
	}

	key := strings.Join(parts, "/")
	code, ok := modules[key]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "Module not found",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"Имя":       parts[1],
		"ВидМодуля": parts[2],
		"Код":       code,
	})
}

func handleQuery(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}

	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	upper := strings.ToUpper(strings.TrimSpace(req.Query))
	if !strings.HasPrefix(upper, "ВЫБРАТЬ") && !strings.HasPrefix(upper, "SELECT") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Only SELECT queries allowed"})
		return
	}

	result := map[string]any{
		"columns":   []string{"Наименование", "ИНН"},
		"rows":      [][]string{{"ООО Ромашка", "7701234567"}, {"ИП Петров", "772987654321"}},
		"total":     2,
		"truncated": false,
	}
	writeJSON(w, http.StatusOK, result)
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	writeJSON(w, http.StatusOK, map[string]string{"version": "0.2.0"})
}

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags)
	log.SetOutput(os.Stderr)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/metadata", handleMetadata)
	mux.HandleFunc("/mcp/object/", handleObject)
	mux.HandleFunc("/mcp/module/", handleModule)
	mux.HandleFunc("/mcp/query", handleQuery)
	mux.HandleFunc("/mcp/version", handleVersion)

	addr := fmt.Sprintf(":%d", *port)
	logger.Printf("Mock 1C server listening on %s", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Fatalf("Server error: %v", err)
	}
}
