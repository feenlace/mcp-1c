package dump

import (
	"bytes"
	"context"
	"encoding/xml"
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
	if err := externalXMLDocument(updated); err != nil {
		t.Fatalf("updated descriptor is not a whole XML document: %v", err)
	}
	var descriptor struct {
		Pages []string `xml:"http://v8.1c.ru/8.3/xcf/extrnprops Page"`
	}
	if err := xml.Unmarshal(updated, &descriptor); err != nil || strings.Join(descriptor.Pages, ",") != "en,ru" {
		t.Fatalf("native-namespace pages: %+v %v", descriptor, err)
	}
	again, err := helpDescriptor(updated)
	if err != nil || !bytes.Equal(again, updated) {
		t.Fatal("descriptor repeat rewrote unknown bytes")
	}
	if _, err := helpDescriptor([]byte(strings.Replace(string(data), "2.20", "2.10", 1))); err == nil {
		t.Fatal("unknown descriptor profile accepted")
	}
}

func TestPatchHelpDescriptorRefusalPreservesSourceAndRetry(t *testing.T) {
	const ns = "http://v8.1c.ru/8.3/xcf/extrnprops"
	for _, descriptor := range []string{
		`<Help xmlns="` + ns + `" version="2.20"/>`,
		"\xef\xbb\xbf<?xml version=\"1.0\"?><Help xmlns=\"" + ns + "\" version=\"2.20\" />\r\n",
		`<h:Help xmlns:h="` + ns + `" version="2.20"><h:Page>en</h:Page></h:Help>`,
	} {
		root, manifest := helpFixture(t, "HelpProbeReport")
		dir := filepath.Join(root, "HelpProbeReport", "Ext", "Help")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		descriptorPath := filepath.Join(root, "HelpProbeReport", "Ext", "Help.xml")
		htmlPath := filepath.Join(dir, "ru.html")
		if err := os.WriteFile(descriptorPath, []byte(descriptor), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(htmlPath, []byte("owned original HTML"), 0600); err != nil {
			t.Fatal(err)
		}
		source, err := InspectExternalSource(context.Background(), root, "HelpProbeReport.xml")
		if err != nil {
			t.Fatal(err)
		}
		request := HelpPatchRequest{ManifestPath: "HelpProbeReport.xml", ExpectedSourceSHA256: source.SourceTreeSHA256, Language: "ru", Content: "replacement", Overwrite: true}
		if _, err := PatchHelp(context.Background(), root, request); err == nil {
			t.Fatalf("unsupported descriptor patched: %s", descriptor)
		}
		for path, want := range map[string][]byte{descriptorPath: []byte(descriptor), htmlPath: []byte("owned original HTML"), filepath.Join(root, "HelpProbeReport.xml"): manifest} {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("refusal mutated %s: %v", path, err)
			}
		}
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".mcp-help-backup-") || strings.HasSuffix(entry.Name(), ".help.lock") {
				t.Fatalf("refusal created staging/lock: %s", entry.Name())
			}
		}
		// An explicit caller repair to the native profile can retry with a fresh digest.
		if err := os.WriteFile(descriptorPath, []byte(nativeHelpDescriptor), 0600); err != nil {
			t.Fatal(err)
		}
		source, err = InspectExternalSource(context.Background(), root, request.ManifestPath)
		if err != nil {
			t.Fatal(err)
		}
		request.ExpectedSourceSHA256 = source.SourceTreeSHA256
		result, err := PatchHelp(context.Background(), root, request)
		if err != nil || result.Status != "patched" {
			t.Fatalf("supported repaired retry: %+v %v", result, err)
		}
	}
}

func TestInspectExternalSourceRefusesXMLFragments(t *testing.T) {
	for _, fragment := range []string{"<Help/><Page>ru</Page>", "<Help/>trailing", "<Help/>\u00a0", "leading<Help/>", " \n"} {
		root, _ := helpFixture(t, "HelpProbeProcessor")
		dir := filepath.Join(root, "HelpProbeProcessor", "Ext")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "Help.xml")
		if err := os.WriteFile(path, []byte(fragment), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectExternalSource(context.Background(), root, "HelpProbeProcessor.xml"); err == nil {
			t.Fatalf("XML fragment accepted: %q", fragment)
		}
		if _, err := PatchHelp(context.Background(), root, HelpPatchRequest{ManifestPath: "HelpProbeProcessor.xml", ExpectedSourceSHA256: strings.Repeat("0", 64), Language: "ru", Content: "owned", Overwrite: true}); err == nil {
			t.Fatal("malformed source patched")
		}
		after, _ := os.ReadFile(path)
		if string(after) != fragment {
			t.Fatal("malformed source refusal mutated descriptor")
		}
	}
}

// The existing cancellation boundary observes the first published descriptor,
// without a product hook, concurrent writer or timing-dependent filesystem race.
type helpCancelAfterDescriptor struct {
	context.Context
	cancel    context.CancelFunc
	path      string
	onPublish func()
	observed  bool
}

func (c *helpCancelAfterDescriptor) Err() error {
	if !c.observed {
		data, err := os.ReadFile(c.path)
		if err == nil && bytes.Contains(data, []byte("<Page>ru</Page>")) {
			c.observed = true
			if c.onPublish != nil {
				c.onPublish()
			}
			c.cancel()
		}
	}
	return c.Context.Err()
}

func TestPatchHelpCancellationAfterFirstPublishRollbackAndConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "restore-original"
		if conflict {
			name = "preserve-independent-edit"
		}
		t.Run(name, func(t *testing.T) {
			root, manifest := helpFixture(t, "HelpProbeProcessor")
			dir := filepath.Join(root, "HelpProbeProcessor", "Ext", "Help")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "HelpProbeProcessor", "Ext", "Help.xml")
			htmlPath := filepath.Join(dir, "ru.html")
			original := []byte(`<Help xmlns="http://v8.1c.ru/8.3/xcf/extrnprops" version="2.20"><Unknown>original</Unknown></Help>`)
			independent := []byte(`<Help xmlns="http://v8.1c.ru/8.3/xcf/extrnprops" version="2.20"><Owner>independent</Owner></Help>`)
			for path, data := range map[string][]byte{path: original, htmlPath: []byte("original HTML")} {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			source, err := InspectExternalSource(context.Background(), root, "HelpProbeProcessor.xml")
			if err != nil {
				t.Fatal(err)
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &helpCancelAfterDescriptor{Context: base, cancel: cancel, path: path}
			if conflict {
				ctx.onPublish = func() {
					if err := os.WriteFile(path, independent, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			request := HelpPatchRequest{ManifestPath: "HelpProbeProcessor.xml", ExpectedSourceSHA256: source.SourceTreeSHA256, Language: "ru", Content: "replacement HTML", Overwrite: true}
			result, err := PatchHelp(ctx, root, request)
			if err == nil || !ctx.observed {
				t.Fatalf("did not fail after first publish: %+v %v", result, err)
			}
			want := original
			if conflict {
				want = independent
				if result.Status != "partial" || len(result.Changes) == 0 || len(result.Warnings) == 0 {
					t.Fatalf("missing explicit recovery result: %+v", result)
				}
				backup, err := os.ReadFile(filepath.Join(root, result.Changes[0].BackupPath))
				if err != nil || !bytes.Equal(backup, original) {
					t.Fatalf("original backup unavailable: %v", err)
				}
			} else if result.Status != "failed" || len(result.Changes) != 0 {
				t.Fatalf("rollback not reported: %+v", result)
			}
			for path, expected := range map[string][]byte{path: want, htmlPath: []byte("original HTML"), filepath.Join(root, request.ManifestPath): manifest} {
				actual, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatalf("recovery changed %s: %v", path, err)
				}
			}
			if !conflict {
				backups, _ := filepath.Glob(filepath.Join(root, ".mcp-help-backup-*"))
				if len(backups) != 0 {
					t.Fatal("successful rollback left staging/backups")
				}
			}
			source, err = InspectExternalSource(context.Background(), root, request.ManifestPath)
			if err != nil {
				t.Fatal(err)
			}
			request.ExpectedSourceSHA256 = source.SourceTreeSHA256
			retry, err := PatchHelp(context.Background(), root, request)
			if err != nil || retry.Status != "patched" {
				t.Fatalf("recovery retry failed: %+v %v", retry, err)
			}
			if conflict {
				after, _ := os.ReadFile(path)
				if !bytes.Contains(after, []byte("<Owner>independent</Owner>")) {
					t.Fatal("retry discarded independent content")
				}
			}
		})
	}
}
