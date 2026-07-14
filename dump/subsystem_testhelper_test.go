package dump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secWrite writes body to path, creating parent directories. Shared across the
// subsystem reader / security tests.
func secWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// secSubBody builds a minimal subsystem XML body with a Name and optional Content
// <Item> members. It has no <ChildObjects>, so it is valid for both the Ext and
// Hierarchical layouts (Hierarchical ignores ChildObjects; Ext without children is
// a leaf subsystem).
func secSubBody(name string, content ...string) string {
	items := ""
	for _, c := range content {
		items += "      <Item>" + c + "</Item>\n"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
  <Subsystem>
    <Properties>
      <Name>` + name + `</Name>
      <Content>
` + items + `      </Content>
    </Properties>
  </Subsystem>
</MetaDataObject>
`
}

// flattenNames returns every subsystem name in the forest, depth-first.
func flattenNames(subs []Subsystem) []string {
	var out []string
	var walk func([]Subsystem)
	walk = func(ss []Subsystem) {
		for _, s := range ss {
			out = append(out, s.Name)
			walk(s.Children)
		}
	}
	walk(subs)
	return out
}

// containsStr reports whether ss contains want.
func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// warningsContain reports whether any warning contains needle.
func warningsContain(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}
