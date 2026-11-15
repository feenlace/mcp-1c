package epf

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDocumentOwnerObservationIsIdentityOnlyAndSessionPinned(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprint("advanced=", advanced), func(t *testing.T) {
			b, _ := New(token, nil)
			defer b.Close()
			if advanced {
				b.EnableQueryPackage(func(context.Context) (time.Time, error) { return time.Time{}, ErrUnavailable })
				b.EnableCodeExecute()
			} else {
				b.EnableDocumentRead(func(context.Context) (time.Time, error) { return time.Time{}, ErrUnavailable })
				b.EnableDocumentDateWrite()
			}
			core := map[string]any{"epf_contract": "document-read/1", "identity_contract": 1, "principal_uuid": graphID, "config_name": "Fixture", "config_version": "1", "core_owner": "MCP_HTTPService", "core_install": "22222222-2222-4222-8222-222222222222", "core_version": "1", "base": strings.Repeat("a", 64), "native_session": "7"}
			raw, _ := json.Marshal(core)
			pair, _ := json.Marshal(map[string]any{"protocol": 1, "identity": "native", "core": string(raw)})
			w := request(b, "POST", "/pair", pairCode(b), string(pair))
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			var connected struct{ Session string }
			json.Unmarshal(w.Body.Bytes(), &connected)
			ctx, err := b.PinOwnerObservation(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			first, err := b.DocumentOwnerBinding(ctx, graphID, "Fixture", "1")
			if err != nil || len(first) != 64 {
				t.Fatal(err)
			}
			for _, body := range []string{graphWire("get", `{"action":"get","document_type":"Документ.Test","uuid":"`+graphID+`"}`), graphWire("describe", `{"action":"describe"}`), graphWire("identity", `{"action":"describe","uuid":"`+graphID+`"}`), graphWire("identity", `{"action":"describe","action":"get"}`)} {
				r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/document-read", strings.NewReader(body))
				if _, err = b.RoundTrip(r); err == nil {
					t.Fatal("observation admitted business/paid work")
				}
			}
			if len(b.queue) != 0 {
				t.Fatal("rejected request enqueued")
			}
			body := graphWire("identity", `{"action":"describe"}`)
			r, _ := http.NewRequest("POST", "http://epf.invalid/document-read", strings.NewReader(body))
			if _, err = b.RoundTrip(r); err == nil {
				t.Fatal("ordinary identity lost paid gate")
			}
			done := make(chan error, 1)
			go func() {
				r, _ := http.NewRequestWithContext(ctx, "POST", "http://epf.invalid/document-read", strings.NewReader(body))
				res, e := b.RoundTrip(r)
				if res != nil {
					res.Body.Close()
				}
				done <- e
			}()
			select {
			case p := <-b.queue:
				if p.task.Capability != "pro.document-receipt" || p.task.Core != string(raw) || p.task.Session != connected.Session {
					t.Fatal("missing native core pin")
				}
				b.queue <- p
			case <-time.After(time.Second):
				t.Fatal("identity not queued")
			}
			poll := request(b, "POST", "/poll", connected.Session, `{"identity":"native"}`)
			var task Task
			json.Unmarshal(poll.Body.Bytes(), &task)
			if request(b, "POST", "/result/"+task.ID, connected.Session, `{"identity":"native","status":200,"body":{"contract_version":1,"identity":"Fixture:1:`+graphID+`","data":{"document_contract":1,"identity_contract":1,"principal_uuid":"`+graphID+`","config_name":"Fixture","config_version":"1"}}}`).Code != 204 {
				t.Fatal("result refused")
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			// A fresh native session alone does not change the durable owner tuple.
			core["native_session"] = "8"
			raw, _ = json.Marshal(core)
			b.mu.Lock()
			b.core = string(raw)
			b.mu.Unlock()
			if _, err = b.DocumentOwnerBinding(ctx, graphID, "Fixture", "1"); err == nil {
				t.Fatal("old request pin accepted changed core")
			}
			renewed, _ := b.PinOwnerObservation(context.Background())
			same, err := b.DocumentOwnerBinding(renewed, graphID, "Fixture", "1")
			if err != nil || same != first {
				t.Fatal("native session became durable ownership")
			}
			for _, field := range []string{"principal_uuid", "base", "core_install", "core_version"} {
				old := core[field]
				if field == "principal_uuid" || field == "core_install" {
					core[field] = "33333333-3333-4333-8333-333333333333"
				} else if field == "base" {
					core[field] = strings.Repeat("b", 64)
				} else {
					core[field] = "2"
				}
				raw, _ = json.Marshal(core)
				b.mu.Lock()
				b.core = string(raw)
				b.mu.Unlock()
				pin, _ := b.PinOwnerObservation(context.Background())
				changed, e := b.DocumentOwnerBinding(pin, graphID, "Fixture", "1")
				if e == nil && changed == first {
					t.Fatal("changed authority accepted", field)
				}
				core[field] = old
			}
			if _, err = b.DocumentOwnerBinding(context.Background(), graphID, "Fixture", "1"); err == nil {
				t.Fatal("unbound pair metadata accepted")
			}
		})
	}
}
