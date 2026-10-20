package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ownXSD = `<?xml version="1.0"?><xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"><xs:element name="OwnFixture" type="xs:string"/></xs:schema>`

func schemaFixture(t *testing.T) (string, SchemaRegistrationRequest) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "own.xsd"), []byte(ownXSD), 0600); err != nil {
		t.Fatal(err)
	}
	return root, SchemaRegistrationRequest{XSDPath: "own.xsd", Version: "declared-1", SourceURL: "https://source.invalid/schema?unverified=true", ExpectedSHA256: localHash([]byte(ownXSD)), ManifestPath: "schemas.json"}
}

func TestRegisterSchemaProvenanceAndUpdate(t *testing.T) {
	root, input := schemaFixture(t)
	r, err := RegisterSchema(context.Background(), root, input)
	if err != nil || r.Status != "registered" || r.Schema.SHA256 != input.ExpectedSHA256 || r.Schema.SourceURL != input.SourceURL {
		t.Fatalf("register: %+v %v", r, err)
	}
	originalManifest, err := os.ReadFile(filepath.Join(root, input.ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	r, err = RegisterSchema(context.Background(), root, input)
	if err != nil || r.Status != "unchanged" || r.BackupPath != "" {
		t.Fatalf("noop: %+v %v", r, err)
	}
	unchanged, _ := os.ReadFile(filepath.Join(root, input.ManifestPath))
	if !bytes.Equal(originalManifest, unchanged) {
		t.Fatal("no-op rewrote manifest")
	}
	// Another supplied schema must survive updating the first one.
	if err := os.WriteFile(filepath.Join(root, "other.xsd"), []byte(ownXSD), 0600); err != nil {
		t.Fatal(err)
	}
	other := input
	other.XSDPath = "other.xsd"
	if _, err := RegisterSchema(context.Background(), root, other); err != nil {
		t.Fatal(err)
	}
	beforeUpdate, _ := os.ReadFile(filepath.Join(root, input.ManifestPath))
	if err := os.Chmod(filepath.Join(root, input.ManifestPath), 0640); err != nil {
		t.Fatal(err)
	}
	input.Version = "declared-2"
	input.SourceURL = "javascript:opaque-provenance-only"
	r, err = RegisterSchema(context.Background(), root, input)
	if err != nil || r.Status != "updated" || r.BackupPath == "" {
		t.Fatalf("update: %+v %v", r, err)
	}
	backup, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.BackupPath)))
	if !bytes.Equal(backup, beforeUpdate) {
		t.Fatal("backup lost original manifest")
	}
	var manifest schemaManifest
	after, _ := os.ReadFile(filepath.Join(root, input.ManifestPath))
	if err := json.Unmarshal(after, &manifest); err != nil {
		t.Fatal(err)
	}
	records := map[string]SchemaRegistration{}
	for _, entry := range manifest.Schemas {
		records[entry.XSDPath] = entry
	}
	if len(records) != 2 || records["own.xsd"].Version != "declared-2" || records["other.xsd"].Version != "declared-1" {
		t.Fatalf("records lost: %+v", manifest)
	}
	st, _ := os.Stat(filepath.Join(root, input.ManifestPath))
	if st.Mode().Perm() != 0640 {
		t.Fatal("manifest permissions changed")
	}
	xsd, _ := os.ReadFile(filepath.Join(root, input.XSDPath))
	if string(xsd) != ownXSD {
		t.Fatal("XSD changed")
	}
}

func TestRegisterSchemaRefusalsPreserveFiles(t *testing.T) {
	for name, change := range map[string]func(*SchemaRegistrationRequest){
		"missing version": func(r *SchemaRegistrationRequest) { r.Version = "" },
		"missing file":    func(r *SchemaRegistrationRequest) { r.XSDPath = "missing.xsd" },
		"wrong hash":      func(r *SchemaRegistrationRequest) { r.ExpectedSHA256 = strings.Repeat("0", 64) },
		"invalid hash":    func(r *SchemaRegistrationRequest) { r.ExpectedSHA256 = "not-a-hash" },
		"escape":          func(r *SchemaRegistrationRequest) { r.ManifestPath = "../escape.json" },
		"same path":       func(r *SchemaRegistrationRequest) { r.ManifestPath = r.XSDPath },
	} {
		t.Run(name, func(t *testing.T) {
			root, r := schemaFixture(t)
			change(&r)
			if _, err := RegisterSchema(context.Background(), root, r); err == nil {
				t.Fatal("accepted")
			}
			b, _ := os.ReadFile(filepath.Join(root, "own.xsd"))
			if string(b) != ownXSD {
				t.Fatal("source changed")
			}
		})
	}
	for _, data := range []string{`<not-schema/>`, `<!DOCTYPE xs:schema SYSTEM "https://must-not-fetch.invalid"><xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"/>`, `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"/><second/>`, `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">`} {
		root, r := schemaFixture(t)
		r.ExpectedSHA256 = ""
		_ = os.WriteFile(filepath.Join(root, "own.xsd"), []byte(data), 0600)
		if _, err := RegisterSchema(context.Background(), root, r); err == nil {
			t.Fatalf("invalid XSD accepted: %s", data)
		}
	}
	root, r := schemaFixture(t)
	for _, bad := range []string{`{"format":"unknown","schemas":[]}`, `{"format":"mcp-1c-schema-registration-v1","schemas":[],"preserve_me":true}`, `{"format":"mcp-1c-schema-registration-v1","schemas":[],"schemas":[]}`, `broken`} {
		_ = os.WriteFile(filepath.Join(root, r.ManifestPath), []byte(bad), 0600)
		if _, err := RegisterSchema(context.Background(), root, r); err == nil {
			t.Fatal("bad manifest accepted")
		}
		b, _ := os.ReadFile(filepath.Join(root, r.ManifestPath))
		if string(b) != bad {
			t.Fatal("bad manifest was erased")
		}
	}
	_ = os.Remove(filepath.Join(root, r.ManifestPath))
	_ = os.WriteFile(filepath.Join(root, r.ManifestPath+".lock"), []byte("other writer"), 0600)
	if _, err := RegisterSchema(context.Background(), root, r); err == nil {
		t.Fatal("lock ignored")
	}
	if _, err := os.Stat(filepath.Join(root, r.ManifestPath+".lock")); err != nil {
		t.Fatal("another writer lock removed")
	}
}

func TestSchemaConflictingCommitAndCancelled(t *testing.T) {
	root, input := schemaFixture(t)
	_ = os.WriteFile(filepath.Join(root, input.ManifestPath), []byte("independent newer data"), 0600)
	r, err := openLocalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := replaceSchemaManifest(context.Background(), r, input.ManifestPath, []byte("stale"), []byte("replacement"), 0600); err == nil {
		t.Fatal("stale replacement accepted")
	}
	b, _ := os.ReadFile(filepath.Join(root, input.ManifestPath))
	if string(b) != "independent newer data" {
		t.Fatal("conflict overwritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RegisterSchema(ctx, root, input); err == nil {
		t.Fatal("cancellation ignored")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err == nil {
		input.ManifestPath = "escape/manifest.json"
		if _, err := RegisterSchema(context.Background(), root, input); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
}

func TestRegisterSchemaUTF8BOMHashesExactBytes(t *testing.T) {
	root, input := schemaFixture(t)
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte(ownXSD)...)
	if err := os.WriteFile(filepath.Join(root, input.XSDPath), data, 0600); err != nil {
		t.Fatal(err)
	}
	input.ExpectedSHA256 = localHash(data)
	r, err := RegisterSchema(context.Background(), root, input)
	if err != nil || r.Schema.SHA256 != input.ExpectedSHA256 || r.Schema.Size != int64(len(data)) {
		t.Fatalf("BOM provenance: %+v %v", r, err)
	}
}
