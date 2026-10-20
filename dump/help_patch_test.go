package dump

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func helpFixture(t *testing.T, name string) (string, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "help-native", name+".xml"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name+".xml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return root, data
}
func TestPatchHelpNativeProfilesPreserveRootAndBackups(t *testing.T) {
	if externalHash([]byte(nativeHelpDescriptor)) != "76aa9ea3f89de48211fc07ae7713bf627d61be458b41c955a87b05c3eb2bcd23" {
		t.Fatal("descriptor differs from pinned native output")
	}
	for _, name := range []string{"HelpProbeProcessor", "HelpProbeReport"} {
		t.Run(name, func(t *testing.T) {
			root, original := helpFixture(t, name)
			source, err := InspectExternalSource(context.Background(), root, name+".xml")
			if err != nil {
				t.Fatal(err)
			}
			r, err := PatchHelp(context.Background(), root, HelpPatchRequest{ManifestPath: name + ".xml", ExpectedSourceSHA256: source.SourceTreeSHA256, Language: "ru", Content: "Unicode Ёжик & <escaped>"})
			if err != nil || r.Status != "patched" || len(r.Changes) != 2 || r.Source.SourceTreeSHA256 == source.SourceTreeSHA256 {
				t.Fatalf("patch: %+v %v", r, err)
			}
			path := filepath.Join(root, name, "Ext", "Help", "ru.html")
			v1, _ := os.ReadFile(path)
			if !bytes.Contains(v1, []byte("&amp; &lt;escaped&gt;")) {
				t.Fatal("plain content not escaped")
			}
			after, _ := os.ReadFile(filepath.Join(root, name+".xml"))
			if !bytes.Equal(after, original) {
				t.Fatal("root metadata/UUID/format changed")
			}
			request := HelpPatchRequest{ManifestPath: name + ".xml", ExpectedSourceSHA256: r.Source.SourceTreeSHA256, Language: "ru", HTML: "<h1>V2</h1><p>Ёжик &amp; текст</p>"}
			if _, err := PatchHelp(context.Background(), root, request); err == nil {
				t.Fatal("existing help overwritten without explicit policy")
			}
			request.Overwrite = true
			r, err = PatchHelp(context.Background(), root, request)
			if err != nil || r.Status != "patched" {
				t.Fatalf("edit: %+v %v", r, err)
			}
			backup, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.Changes[1].BackupPath)))
			if !bytes.Equal(backup, v1) {
				t.Fatal("HTML backup differs")
			}
			after, _ = os.ReadFile(filepath.Join(root, name+".xml"))
			if !bytes.Equal(after, original) {
				t.Fatal("edit changed root")
			}
			request.ExpectedSourceSHA256 = r.Source.SourceTreeSHA256
			r, err = PatchHelp(context.Background(), root, request)
			if err != nil || r.Status != "unchanged" {
				t.Fatalf("noop: %+v %v", r, err)
			}
		})
	}
}

func TestHelpSourceDigestBindsBSLAndRefusesUnsafeInput(t *testing.T) {
	root, original := helpFixture(t, "HelpProbeProcessor")
	dir := filepath.Join(root, "HelpProbeProcessor", "Ext")
	_ = os.MkdirAll(dir, 0700)
	module := filepath.Join(dir, "ObjectModule.bsl")
	_ = os.WriteFile(module, []byte("// owned source A"), 0600)
	source, err := InspectExternalSource(context.Background(), root, "HelpProbeProcessor.xml")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(module, []byte("// changed source B"), 0600)
	r := HelpPatchRequest{ManifestPath: "HelpProbeProcessor.xml", ExpectedSourceSHA256: source.SourceTreeSHA256, Language: "ru", Content: "safe"}
	if _, err := PatchHelp(context.Background(), root, r); err == nil {
		t.Fatal("changed executable source accepted")
	}
	source, _ = InspectExternalSource(context.Background(), root, r.ManifestPath)
	r.ExpectedSourceSHA256 = source.SourceTreeSHA256
	for _, unsafe := range []string{`<script>alert(1)</script>`, `<img src="https://must-not-fetch.invalid/a"/>`, `<p onclick="run()">x</p>`, `<!-- [if IE]><script>x</script><![endif] -->`, `<fragment>unknown</fragment>`} {
		bad := r
		bad.Content = ""
		bad.HTML = unsafe
		if _, err := PatchHelp(context.Background(), root, bad); err == nil {
			t.Fatalf("unsafe HTML accepted: %s", unsafe)
		}
	}
	bad := r
	bad.ManifestPath = "../escape.xml"
	if _, err := PatchHelp(context.Background(), root, bad); err == nil {
		t.Fatal("escape accepted")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "Help")); err == nil {
		if _, err := PatchHelp(context.Background(), root, r); err == nil {
			t.Fatal("help symlink accepted")
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, r.ManifestPath))
	if !bytes.Equal(after, original) {
		t.Fatal("refusal changed root")
	}
}

func TestHelpDescriptorPreservesUnknownContent(t *testing.T) {
	data := []byte(`<?xml version="1.0"?><Help xmlns="http://v8.1c.ru/8.3/xcf/extrnprops" version="2.20"><Unknown attr="keep">custom</Unknown><Page>en</Page></Help>`)
	updated, err := helpDescriptor(data)
	if err != nil || !bytes.Contains(updated, []byte(`<Unknown attr="keep">custom</Unknown>`)) || !bytes.Contains(updated, []byte("<Page>ru</Page>")) {
		t.Fatalf("descriptor: %s %v", updated, err)
	}
	again, err := helpDescriptor(updated)
	if err != nil || !bytes.Equal(again, updated) {
		t.Fatal("descriptor repeat rewrote unknown bytes")
	}
	if _, err := helpDescriptor([]byte(strings.Replace(string(data), "2.20", "2.10", 1))); err == nil {
		t.Fatal("unknown descriptor profile accepted")
	}
}
