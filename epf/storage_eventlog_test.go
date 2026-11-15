package epf

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/onec"
)

func TestStorageEventLogRoutes(t *testing.T) {
	if capability("GET", "/storage/Catalog/Items") != "community.storage" {
		t.Fatal("storage route unavailable")
	}
	if capability("POST", "/eventlog") != "community.eventlog" {
		t.Fatal("eventlog route unavailable")
	}
	for _, tc := range []struct{ method, path string }{{"GET", "/storage/Subsystem/A"}, {"GET", "/storage/Catalog/A.B"}, {"GET", "/storage/Catalog/A%2fB"}, {"GET", "/storage/Unknown/A"}, {"POST", "/storage/Catalog/A"}, {"GET", "/eventlog"}, {"POST", "/eventlog/other"}} {
		if capability(tc.method, tc.path) != "" {
			t.Fatal("unsupported storage/log route admitted")
		}
	}
}

func TestStorageEventLogExistingClient(t *testing.T) {
	for _, storage := range []bool{true, false} {
		t.Run(map[bool]string{true: "storage", false: "eventlog"}[storage], func(t *testing.T) {
			b, _ := New(token, nil)
			defer b.Close()
			session := pair(t, b)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				c := onec.NewClient("http://epf.invalid", "", "")
				c.HTTPClient.Transport = b
				if storage {
					out, err := c.GetStorageStructure(ctx, "Catalog", "Items")
					if err == nil && (len(out.Tables) != 1 || len(out.Tables[0].Fields) != 1 || len(out.Tables[0].Indexes) != 1) {
						t.Error("physical fields/indexes lost")
					}
					done <- err
				} else {
					var out onec.EventLogResult
					err := c.Post(ctx, "/eventlog", onec.EventLogRequest{StartDate: "2026-11-08T00:00:00", EndDate: "2026-11-08T23:59:59", Level: "Ошибка", Event: []string{"MCP.Probe"}, User: "synthetic", Limit: 1}, &out)
					if err == nil && (out.Total != 1 || len(out.Events) != 1 || out.Events[0].EventPresentation != "Probe") {
						t.Error("event result lost")
					}
					done <- err
				}
			}()
			var p *pending
			select {
			case p = <-b.queue:
				b.queue <- p
			case err := <-done:
				t.Fatalf("route failed: %v", err)
			case <-ctx.Done():
				t.Fatal("task missing")
			}
			w := request(b, "POST", "/poll", session, `{"identity":"native-owner"}`)
			var task Task
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &task) != nil {
				t.Fatal("handout failed")
			}
			body := `{"events":[{"date":"2026-11-08T12:00:00","level":"Ошибка","event":"MCP.Probe","event_presentation":"Probe","user":"synthetic"}],"total":1}`
			if storage {
				if task.Capability != "community.storage" || task.ObjectType != "Catalog" || task.ObjectName != "Items" {
					t.Fatal("storage identity lost")
				}
				body = `{"name":"Items","full_name":"Справочник.Items","tables":[{"storage_name":"_Reference1","fields":[{"storage_name":"_Fld2"}],"indexes":[{"fields":[{"storage_name":"_Fld2"}]}]}]}`
			} else {
				if task.Capability != "community.eventlog" {
					t.Fatal("wrong log capability")
				}
				var got onec.EventLogRequest
				if json.Unmarshal(task.Body, &got) != nil || len(got.Event) != 1 || got.Event[0] != "MCP.Probe" || got.Level != "Ошибка" || got.User != "synthetic" || got.Limit != 1 || got.StartDate == "" || got.EndDate == "" {
					t.Fatal("event filters dropped")
				}
			}
			if request(b, "POST", "/result/"+task.ID, session, `{"identity":"native-owner","status":200,"body":`+body+`}`).Code != 204 {
				t.Fatal("result refused")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStorageEventLogNativeContract(t *testing.T) {
	data, err := os.ReadFile("source/MCPConnector/Forms/Main/Ext/Form/Module.bsl")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "// Fixed native storage map and bounded Administration-only event log.")
	if start < 0 {
		t.Fatal("native storage/log helpers missing")
	}
	text = text[start:]
	for _, bad := range []string{"MCP_", "УстановитьПривилегированныйРежим", "УстановитьБезопасныйРежим", "ОписаниеОшибки()", "ЗаписьЖурналаРегистрации(", "Новый ХР", "Новый ЖЛ", "ЖЛСтрока("} {
		if strings.Contains(text, bad) {
			t.Fatalf("unsafe storage/log implementation: %s", bad)
		}
	}
	event := text[strings.Index(text, "Функция ЖурналРегистрацииPOST"):]
	right := strings.Index(event, `ПравоДоступа("Администрирование", Метаданные)`)
	parse := strings.Index(event, "ЖЛПараметры =")
	export := strings.Index(event, "ВыгрузитьЖурналРегистрации(")
	if right < 0 || parse < right || export < parse {
		t.Fatal("event read does not check Administration first")
	}
	if !strings.Contains(event, "ВыгрузитьЖурналРегистрации(ЖЛТаблицаЖурнала, ЖЛОтбор, , , ЖЛЛимит)") || !strings.Contains(event, `"total",  ЖЛМассивСобытий.Количество()`) {
		t.Fatal("event read/output bounds changed")
	}
	if !strings.Contains(text, "ПолучитьСтруктуруХраненияБазыДанных(ХРОбъектыМД, Истина)") || !strings.Contains(text, "native platform returned no storage map for this stored object") {
		t.Fatal("storage became a fabricated fallback")
	}
}
