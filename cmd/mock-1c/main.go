package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/feenlace/mcp-1c/onec"
)

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
		"Перечисления": {
			"СтавкиНДС",
			"ВидыНоменклатуры",
			"ВидыОпераций",
			"ТипыКонтактнойИнформации",
		},
		"Обработки": {
			"ЗагрузкаДанныхИзФайла",
			"ГрупповоеИзменениеОбъектов",
		},
		"Отчеты": {
			"ОборотноСальдоваяВедомость",
			"КарточкаСчета",
			"АнализСубконто",
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
		"РегистрыРасчета":         {},
		"ПланыСчетов":             {"Хозрасчетный"},
		"ПланыВидовХарактеристик": {"ВидыСубконтоХозрасчетные"},
		"ПланыВидовРасчета":       {},
		"ПланыОбмена":             {"ОбменБухгалтерия"},
		"БизнесПроцессы":          {},
		"Задачи":                  {},
		"ОпределяемыеТипы":        {"ЗначениеДоступа", "ЛюбаяСсылкаИлиСтрока", "СоставнойЧерезОпределяемый"},
		"ЖурналыДокументов":       {"ЖурналОпераций"},
		"Константы":               {"ВалютаРегламентированногоУчета", "ОсновнаяОрганизация"},
		"ОбщиеМодули": {
			"ОбщегоНазначения",
			"ОбщегоНазначенияКлиентСервер",
			"УправлениеПечатью",
		},
		"ОбщиеФормы":        {"ФормаВопроса", "ФормаПредупреждения"},
		"ОбщиеКоманды":      {},
		"ОбщиеМакеты":       {"МакетПечати"},
		"Роли":              {"Администратор", "Бухгалтер", "ТолькоПросмотр"},
		"Подсистемы":        {"Бухгалтерия", "Зарплата", "Администрирование"},
		"РегулярныеЗадания": {"ОбновлениеКурсовВалют"},
		"ВебСервисы":        {},
		"HTTPСервисы":       {"MCPService"},
	}

	objects = map[objectKey]onec.ObjectStructure{
		{typ: "Document", name: "РеализацияТоваровУслуг"}: {
			Name:    "РеализацияТоваровУслуг",
			Synonym: "Реализация (акты, накладные, УПД)",
			Attributes: []onec.Attribute{
				{Name: "Контрагент", Synonym: "Контрагент", Type: "СправочникСсылка.Контрагенты"},
				{Name: "Организация", Synonym: "Организация", Type: "СправочникСсылка.Организации"},
				{Name: "Склад", Synonym: "Склад", Type: "СправочникСсылка.Склады"},
				{Name: "Валюта", Synonym: "Валюта расчётов", Type: "СправочникСсылка.Валюты"},
				{Name: "ДоговорКонтрагента", Synonym: "Договор", Type: "СправочникСсылка.ДоговорыКонтрагентов"},
				{Name: "СуммаДокумента", Synonym: "Сумма", Type: "Число"},
				{Name: "Комментарий", Synonym: "Комментарий", Type: "Строка"},
			},
			TabularParts: []onec.TabularPart{
				{
					Name: "Товары",
					Attributes: []onec.Attribute{
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
					Attributes: []onec.Attribute{
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
			Attributes: []onec.Attribute{
				{Name: "ИНН", Synonym: "ИНН", Type: "Строка"},
				{Name: "КПП", Synonym: "КПП", Type: "Строка"},
				{Name: "НаименованиеПолное", Synonym: "Полное наименование", Type: "Строка"},
				{Name: "ЮридическийАдрес", Synonym: "Юридический адрес", Type: "Строка"},
				{Name: "ОсновнойДоговор", Synonym: "Основной договор", Type: "СправочникСсылка.ДоговорыКонтрагентов"},
				{Name: "ОсновнойБанковскийСчет", Synonym: "Основной банковский счёт", Type: "СправочникСсылка.БанковскиеСчета"},
			},
			TabularParts: []onec.TabularPart{
				{
					Name: "КонтактнаяИнформация",
					Attributes: []onec.Attribute{
						{Name: "Тип", Synonym: "Тип", Type: "ПеречислениеСсылка.ТипыКонтактнойИнформации"},
						{Name: "Представление", Synonym: "Представление", Type: "Строка"},
					},
				},
			},
		},
		{typ: "Catalog", name: "Номенклатура"}: {
			Name:    "Номенклатура",
			Synonym: "Номенклатура",
			Attributes: []onec.Attribute{
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
			Dimensions: []onec.Attribute{
				{Name: "Номенклатура", Synonym: "Номенклатура", Type: "СправочникСсылка.Номенклатура"},
				{Name: "Склад", Synonym: "Склад", Type: "СправочникСсылка.Склады"},
			},
			Resources: []onec.Attribute{
				{Name: "Количество", Synonym: "Количество", Type: "Число"},
			},
			Attributes: []onec.Attribute{},
		},
		// Types are TECHNICAL metadata names as the extension now emits them for
		// reference members: Метаданные.НайтиПоТипу(Тип).ПолноеИмя() -> "Справочник.X"
		// (not the localized synonym Строка(Тип) used to yield). Primitives stay
		// as Строка(Тип), e.g. "Строка"/"Число".
		{typ: "DefinedType", name: "ЗначениеДоступа"}: {
			Name:    "ЗначениеДоступа",
			Synonym: "Значение доступа",
			Types:   []string{"Справочник.Пользователи", "Справочник.ВнешниеПользователи"},
		},
		// Composition mixing a reference type with a primitive (Строка): community
		// review coverage for issue #33 — primitives must render alongside refs.
		{typ: "DefinedType", name: "ЛюбаяСсылкаИлиСтрока"}: {
			Name:    "ЛюбаяСсылкаИлиСтрока",
			Synonym: "Любая ссылка или строка",
			Types:   []string{"Справочник.Номенклатура", "Строка"},
		},
		// Composition that references ANOTHER DefinedType. This offline fixture only
		// exercises Go handling of whatever the "types" array contains; the real
		// platform .Типы() nested-expansion behavior is out of scope (real-1C gate).
		{typ: "DefinedType", name: "СоставнойЧерезОпределяемый"}: {
			Name:    "СоставнойЧерезОпределяемый",
			Synonym: "Составной через определяемый тип",
			Types:   []string{"ОпределяемыйТип.ЗначениеДоступа", "Справочник.Организации"},
		},
		// Subsystem structure (issue #36 Phase 1). content = direct members
		// (full metadata names); subsystems = nested child tree (>=2 levels here
		// via Розница -> Касса) so the renderer's indentation path is exercised.
		{typ: "Subsystem", name: "Продажи"}: {
			Name:    "Продажи",
			Synonym: "Продажи",
			Content: []string{"Справочник.Контрагенты", "Документ.РеализацияТоваровУслуг"},
			Subsystems: []onec.SubsystemNode{
				{
					Name:     "Розница",
					FullName: "Подсистема.Продажи.Подсистема.Розница",
					Synonym:  "Розница",
					Content:  []string{"Справочник.Склады"},
					Subsystems: []onec.SubsystemNode{
						{
							Name:     "Касса",
							FullName: "Подсистема.Продажи.Подсистема.Розница.Подсистема.Касса",
							Synonym:  "Рабочее место кассира",
							Content:  []string{"Документ.КассовыйОрдер"},
						},
					},
				},
				{
					Name:     "Опт",
					FullName: "Подсистема.Продажи.Подсистема.Опт",
					Synonym:  "Оптовые продажи",
					Content:  []string{"Документ.СчетНаОплатуПокупателю"},
				},
			},
		},
		// Empty subsystem: no members, no children -> renders neither the
		// "## Состав" nor the "## Подсистемы" block (edge-case coverage).
		{typ: "Subsystem", name: "ПустаяПодсистема"}: {
			Name:    "ПустаяПодсистема",
			Synonym: "Пустая подсистема",
		},
	}

	// subsystemForest backs GET /subsystems (issue #36 Phase 2-4). It exercises
	// every analyze_subsystems computation and edge case with no live 1C:
	//   - Справочник.Контрагенты: in Продажи AND Продажи>Розница (same root)
	//   - Документ.РеализацияТоваровУслуг: in Продажи AND Финансы (different roots)
	//   - Справочник.Организации, Справочник.Валюты, РегистрСведений.КурсыВалют,
	//     РегистрНакопления.ТоварыНаСкладах: applied but in no subsystem (orphans)
	//   - Справочник.НоменклатураПрисоединенныеФайлы: orphaned noise (filtered out)
	subsystemForest = onec.SubsystemForest{
		Subsystems: []onec.SubsystemNode{
			{
				Name:     "Продажи",
				FullName: "Подсистема.Продажи",
				Synonym:  "Продажи",
				Content:  []string{"Справочник.Контрагенты", "Справочник.Номенклатура", "Документ.РеализацияТоваровУслуг"},
				Subsystems: []onec.SubsystemNode{
					{
						Name:     "Розница",
						FullName: "Подсистема.Продажи.Подсистема.Розница",
						Synonym:  "Розница",
						Content:  []string{"Справочник.Контрагенты", "Справочник.Склады"},
					},
					{
						Name:     "Опт",
						FullName: "Подсистема.Продажи.Подсистема.Опт",
						Synonym:  "Оптовые продажи",
						Content:  []string{"Документ.СчетНаОплатуПокупателю"},
					},
				},
			},
			{
				Name:     "Закупки",
				FullName: "Подсистема.Закупки",
				Synonym:  "Закупки",
				Content:  []string{"Документ.ПоступлениеТоваровУслуг"},
			},
			{
				Name:     "Финансы",
				FullName: "Подсистема.Финансы",
				Synonym:  "Финансы",
				Content:  []string{"Документ.РеализацияТоваровУслуг", "Документ.ПлатежноеПоручение"},
			},
		},
		AllObjects: []string{
			"Справочник.Контрагенты",
			"Справочник.Номенклатура",
			"Справочник.Организации",
			"Справочник.Склады",
			"Справочник.Валюты",
			"Документ.РеализацияТоваровУслуг",
			"Документ.ПоступлениеТоваровУслуг",
			"Документ.СчетНаОплатуПокупателю",
			"Документ.ПлатежноеПоручение",
			"РегистрСведений.КурсыВалют",
			"РегистрНакопления.ТоварыНаСкладах",
			"Справочник.НоменклатураПрисоединенныеФайлы",
		},
	}
)

// isSelectQuery checks if a query starts with SELECT/ВЫБРАТЬ keyword.
func isSelectQuery(query string) bool {
	upper := strings.ToUpper(strings.TrimSpace(query))
	return strings.HasPrefix(upper, "ВЫБРАТЬ") || strings.HasPrefix(upper, "SELECT")
}

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

func handleSubsystems(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	writeJSON(w, http.StatusOK, subsystemForest)
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

	if !isSelectQuery(req.Query) {
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

func handleForm(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)

	writeJSON(w, http.StatusOK, map[string]any{
		"name":  "ФормаДокумента",
		"title": "Реализация товаров и услуг",
		"elements": []map[string]any{
			{"name": "Контрагент", "type": "ПолеВвода", "title": "Контрагент", "dataPath": "Объект.Контрагент"},
			{"name": "Организация", "type": "ПолеВвода", "title": "Организация", "dataPath": "Объект.Организация"},
			{"name": "СуммаДокумента", "type": "ПолеВвода", "title": "Сумма", "dataPath": "Объект.СуммаДокумента"},
			{"name": "ТаблицаТоваров", "type": "ТаблицаФормы", "title": "Товары", "dataPath": "Объект.Товары"},
		},
		"commands": []map[string]any{
			{"name": "ПровестиИЗакрыть", "action": "ПровестиИЗакрыть"},
			{"name": "Записать", "action": "Записать"},
		},
		"handlers": []map[string]any{
			{"event": "ПриОткрытии", "handler": "ПриОткрытии"},
			{"event": "ПередЗаписью", "handler": "ПередЗаписью"},
		},
	})
}

func handleValidateQuery(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}

	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if isSelectQuery(req.Query) {
		writeJSON(w, http.StatusOK, map[string]any{"valid": true})
	} else {
		writeJSON(w, http.StatusOK, map[string]any{
			"valid":  false,
			"errors": []string{"Ожидается ключевое слово ВЫБРАТЬ или SELECT"},
		})
	}
}

func handleEventLog(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}

	var req struct {
		Level string `json:"level"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	events := []map[string]any{
		{
			"date":     "2026-03-07T14:30:00",
			"level":    "Ошибка",
			"event":    "Данные.Запись",
			"user":     "Администратор",
			"metadata": "Документ.РеализацияТоваровУслуг",
			"comment":  "Ошибка при записи: поле Контрагент не заполнено",
		},
		{
			"date":     "2026-03-07T14:25:00",
			"level":    "Предупреждение",
			"event":    "Данные.Проведение",
			"user":     "Бухгалтер",
			"metadata": "Документ.ПоступлениеТоваровУслуг",
			"comment":  "Отрицательный остаток по регистру ТоварыНаСкладах",
		},
		{
			"date":  "2026-03-07T14:00:00",
			"level": "Информация",
			"event": "Сеанс.Начало",
			"user":  "Администратор",
		},
	}

	if req.Level != "" {
		var filtered []map[string]any
		for _, e := range events {
			if e["level"] == req.Level {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	total := len(events)
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit < total {
		events = events[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"total":  total,
	})
}

func handleConfiguration(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":             "БухгалтерияПредприятия",
		"version":          "3.0.150.28",
		"vendor":           "Фирма \"1С\"",
		"platform_version": "8.3.25.1394",
		"mode":             "file",
	})
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	writeJSON(w, http.StatusOK, map[string]string{"version": "0.3.0"})
}

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags)
	log.SetOutput(os.Stderr)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/metadata", handleMetadata)
	mux.HandleFunc("/mcp/subsystems", handleSubsystems)
	mux.HandleFunc("/mcp/object/", handleObject)
	mux.HandleFunc("/mcp/query", handleQuery)

	mux.HandleFunc("/mcp/form/", handleForm)
	mux.HandleFunc("/mcp/validate-query", handleValidateQuery)
	mux.HandleFunc("/mcp/eventlog", handleEventLog)
	mux.HandleFunc("/mcp/configuration", handleConfiguration)
	mux.HandleFunc("/mcp/version", handleVersion)

	addr := fmt.Sprintf(":%d", *port)
	logger.Printf("Mock 1C server listening on %s", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Fatalf("Server error: %v", err)
	}
}
