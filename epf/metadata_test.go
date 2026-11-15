package epf

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/feenlace/mcp-1c/onec"
)

func TestMetadataRouteAdmission(t *testing.T) {
	for _, tc := range []struct{ path, cap, kind, name string }{
		{"/object/Catalog/Товары", "community.object", "Catalog", "Товары"},
		{"/form/Document/Заказ", "community.form", "Document", "Заказ"},
		{"/object/Subsystem/Подсистема.Продажи.Подсистема.Общее", "community.object", "Subsystem", "Подсистема.Продажи.Подсистема.Общее"},
	} {
		path := (&url.URL{Path: tc.path}).EscapedPath()
		if got := capability("GET", path); got != tc.cap {
			t.Fatalf("capability %q", got)
		}
		_, kind, name := metadataRoute(path)
		if kind != tc.kind || name != tc.name {
			t.Fatalf("lost typed metadata identity: %q %q", kind, name)
		}
		if capability("POST", path) != "" {
			t.Fatal("mutation admitted")
		}
	}
	for _, path := range []string{
		"/object/Unknown/Name", "/object/%43atalog/Name", "/object/Catalog/", "/object/Catalog/1Name",
		"/object/Catalog/A/B", "/object/Catalog/A%2fB", "/object/Catalog/A%5cB", "/object/Catalog/%252f",
		"/object/Catalog/..", "/object/Catalog/A.B", "/object/Catalog/A%0aB", "/object/Catalog/%ff",
		"/object/Subsystem/A..B", "/object/Subsystem/.A", "/object/Subsystem/A.", "/object/Catalog/" + strings.Repeat("A", 129),
	} {
		if capability("GET", path) != "" {
			t.Fatalf("unsafe route admitted: %q", path)
		}
	}
	b, _ := New(token, nil)
	for _, path := range []string{"/object/Catalog/A?object_name=Other", "/subsystems?code=execute", "/execute"} {
		r, _ := http.NewRequest("GET", "http://epf.invalid"+path, nil)
		if _, err := b.RoundTrip(r); !errors.Is(err, ErrCapabilityUnavailable) {
			t.Fatalf("unsupported source not classified: %v", err)
		}
	}
	if len(b.queue) != 0 {
		t.Fatal("refused route queued")
	}
}

// Exercise the real onec request decoder, task envelope and result binding for each added route.
func TestMetadataExistingClientRoutes(t *testing.T) {
	for _, tc := range []struct{ path, cap, kind, name, body string }{
		{"/object/Catalog/Товары", "community.object", "Catalog", "Товары", `{"name":"Товары","attributes":[{"name":"Артикул","type":"Строка"}]}`},
		{"/object/Subsystem/Общее", "community.object", "Subsystem", "Общее", `{"ambiguous":["Подсистема.А.Подсистема.Общее","Подсистема.Б.Подсистема.Общее"]}`},
		{"/subsystems", "community.subsystems", "", "", `{"subsystems":[],"allObjects":["Справочник.Товары"],"warnings":["partial metadata"]}`},
		{"/form/Catalog/Товары", "community.form", "Catalog", "Товары", `{"name":"ФормаОбъекта","title":"Товары","elements":[],"warnings":["static metadata only; configure --dump"]}`},
	} {
		t.Run(tc.cap+tc.name, func(t *testing.T) {
			b, _ := New(token, nil)
			defer b.Close()
			session := pair(t, b)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got := make(chan map[string]json.RawMessage, 1)
			done := make(chan error, 1)
			go func() {
				client := onec.NewClient("http://epf.invalid", "", "")
				client.HTTPClient.Transport = b
				var out map[string]json.RawMessage
				err := client.Get(ctx, tc.path, &out)
				got <- out
				done <- err
			}()
			var pendingTask *pending
			select {
			case pendingTask = <-b.queue:
				b.queue <- pendingTask
			case <-ctx.Done():
				t.Fatal("no task")
			}
			w := request(b, "POST", "/poll", session, `{"identity":"native-owner"}`)
			var task Task
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &task) != nil {
				t.Fatal("invalid task handout")
			}
			if task.Capability != tc.cap || task.ObjectType != tc.kind || task.ObjectName != tc.name {
				t.Fatal("typed classification lost")
			}
			body := `{"identity":"native-owner","status":200,"body":` + tc.body + `}`
			if request(b, "POST", "/result/"+task.ID, session, body).Code != 204 {
				t.Fatal("result refused")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var want map[string]json.RawMessage
			_ = json.Unmarshal([]byte(tc.body), &want)
			out := <-got
			for k, v := range want {
				if string(out[k]) != string(v) {
					t.Fatalf("response field lost: %s", k)
				}
			}
		})
	}
}

func TestMetadataNativeSourceContract(t *testing.T) {
	source, err := os.ReadFile("source/MCPConnector/Forms/Main/Ext/Form/Module.bsl")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	// The EPF collection universe must remain synchronized with the owned HTTP helper.
	httpSource, err := os.ReadFile("../extension/src/HTTPServices/MCPService/Ext/Module.bsl")
	if err != nil {
		t.Fatal(err)
	}
	collections := func(s string) []string {
		re := regexp.MustCompile(`(?:МД)?ПрикладныеКоллекции\.Добавить\("([^"\n]+)"\)`)
		var out []string
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return out
	}
	if got, want := collections(text), collections(string(httpSource)); len(got) == 0 || !slices.Equal(got, want) {
		t.Fatal("metadata universe drift")
	}
	added := text[strings.Index(text, "// Static metadata reads"):]
	if end := strings.Index(added, "// Fixed query parsing and permitted record count"); end >= 0 {
		added = added[:end]
	}
	for _, forbidden := range []string{"ПолучитьФорму(", "ОткрытьФорму(", "Выполнить(", "ОписаниеОшибки()", "MCP_", "Новый МД"} {
		if strings.Contains(added, forbidden) {
			t.Fatalf("unsafe static metadata helper: %s", forbidden)
		}
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*([А-Яа-яЁёA-Za-z_][\wА-Яа-яЁё]*)\s*=`).FindAllStringSubmatch(added, -1) {
		if !strings.HasPrefix(m[1], "МД") {
			t.Fatalf("unscoped new form local: %s", m[1])
		}
	}
}
