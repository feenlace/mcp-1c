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

func TestParserCountFixedRoutes(t *testing.T) {
	for _, tc := range []struct{ path, cap string }{{"/validate-query", "community.validate-query"}, {"/count", "community.count"}} {
		if capability("POST", tc.path) != tc.cap {
			t.Fatalf("missing fixed capability: %s", tc.path)
		}
		for _, method := range []string{"GET", "PUT", "DELETE"} {
			if capability(method, tc.path) != "" {
				t.Fatal("non-POST admitted")
			}
		}
		if capability("POST", tc.path+"/other") != "" {
			t.Fatal("dynamic parser/count route admitted")
		}
	}
}

func TestParserCountExistingClientAndExactFilters(t *testing.T) {
	for _, count := range []bool{false, true} {
		t.Run(map[bool]string{false: "validate", true: "count"}[count], func(t *testing.T) {
			b, _ := New(token, nil)
			defer b.Close()
			session := pair(t, b)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req := onec.RecordCountRequest{ObjectType: "Document", ObjectName: "CountDocs", Period: &onec.CountPeriod{Field: "Дата", Start: "2026-01-01T00:00:00", End: "2026-01-02T00:00:00"}, Organization: &onec.CountOrganization{Field: "Organization", Reference: onec.CountReference{CatalogName: "Organizations", UUID: "550e8400-e29b-41d4-a716-446655440000"}}}
			done := make(chan error, 1)
			go func() {
				c := onec.NewClient("http://epf.invalid", "", "")
				c.HTTPClient.Transport = b
				if count {
					out, err := c.CountRecords(ctx, req)
					if err == nil && (out.Count != 9007199254740993 || out.Period == nil || out.Organization == nil) {
						t.Error("exact count/filter binding lost")
					}
					done <- err
				} else {
					var out onec.ValidateQueryResult
					err := c.Post(ctx, "/validate-query", onec.ValidateQueryRequest{Query: "ВЫБРАТЬ ИЗ"}, &out)
					if err == nil && (out.Valid || len(out.Errors) != 1) {
						t.Error("invalid syntax result lost")
					}
					done <- err
				}
			}()
			var p *pending
			select {
			case p = <-b.queue:
				b.queue <- p
			case err := <-done:
				t.Fatalf("existing client refused: %v", err)
			case <-ctx.Done():
				t.Fatal("not queued")
			}
			w := request(b, "POST", "/poll", session, `{"identity":"native-owner"}`)
			var task Task
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &task) != nil {
				t.Fatal("task refused")
			}
			cap := "community.validate-query"
			body := `{"valid":false,"errors":["{(1,1)}: query syntax error"]}`
			if count {
				cap = "community.count"
				var got onec.RecordCountRequest
				if json.Unmarshal(task.Body, &got) != nil || got.Period == nil || got.Organization == nil {
					t.Fatal("typed input filters lost")
				}
				bytes, _ := json.Marshal(map[string]any{"object_type": req.ObjectType, "object_name": req.ObjectName, "count": json.Number("9007199254740993"), "period": req.Period, "organization": req.Organization})
				body = string(bytes)
			}
			if task.Capability != cap || strings.Contains(string(task.Body), "native_code") {
				t.Fatal("wrong capability")
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

func TestParserCountNativeSourceContract(t *testing.T) {
	source, err := os.ReadFile("source/MCPConnector/Forms/Main/Ext/Form/Module.bsl")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "// Fixed query parsing and permitted record count")
	if start < 0 {
		t.Fatal("missing native handlers")
	}
	text = text[start:]
	if end := strings.Index(text, "// Fixed native storage map and bounded Administration-only event log."); end >= 0 {
		text = text[:end]
	}
	for _, bad := range []string{"MCP_", "УстановитьПривилегированныйРежим", "Записать(", "ОписаниеОшибки()", "Новый СЧ", "Новый ПР"} {
		if strings.Contains(text, bad) {
			t.Fatalf("unsafe native parser/count helper: %s", bad)
		}
	}
	parser := text[:strings.Index(text, "Функция КоличествоPOST")]
	if strings.Contains(parser, ".Выполнить(") || !strings.Contains(parser, "Новый СхемаЗапроса") {
		t.Fatal("validation is not parser-only")
	}
	for _, must := range []string{"ПривилегированныйРежим()", "ПравоДоступа(\"Чтение\"", "ВЫБРАТЬ РАЗРЕШЕННЫЕ КОЛИЧЕСТВО(*)", "УстановитьПараметр(\"Начало\"", "УстановитьПараметр(\"Конец\"", "УстановитьПараметр(\"Организация\"", "selected count object not found", "selected count table is denied"} {
		if !strings.Contains(text, must) {
			t.Fatalf("native boundary lost: %s", must)
		}
	}
}
