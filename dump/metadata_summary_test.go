package dump

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func summaryFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"Configuration.xml":                    `<MetaDataObject><Configuration><Properties><Name>Fixture</Name><Version>1.2.3</Version><Vendor>A &amp; B</Vendor></Properties><ChildObjects><Catalog>Items</Catalog><Subsystem>Main</Subsystem></ChildObjects></Configuration></MetaDataObject>`,
		"Catalogs/Items.xml":                   `<MetaDataObject><Catalog><Properties><Name>Items</Name><CodeLength>9</CodeLength></Properties><ChildObjects><Attribute><Properties><Name>Organization</Name><Type><Type>cfg:CatalogRef.Organizations</Type></Type></Properties></Attribute><TabularSection><Properties><Name>Lines</Name></Properties><ChildObjects><Attribute><Properties><Name>Quantity</Name><Type><Type>xs:decimal</Type></Type></Properties></Attribute></ChildObjects></TabularSection><FutureNode/></ChildObjects></Catalog></MetaDataObject>`,
		"Subsystems/Main.xml":                  `<MetaDataObject><Subsystem><Properties><Name>Main</Name><Content><Item>Catalog.Items</Item></Content></Properties><ChildObjects><Subsystem>Child</Subsystem></ChildObjects></Subsystem></MetaDataObject>`,
		"Subsystems/Main/Subsystems/Child.xml": `<MetaDataObject><Subsystem><Properties><Name>Child</Name></Properties><ChildObjects/></Subsystem></MetaDataObject>`,
	}
	for rel, data := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func TestMetadataSummaryConfigurationObjectSubsystem(t *testing.T) {
	dir := summaryFixture(t)
	ctx := context.Background()
	conf, err := ReadMetadataSummary(ctx, dir, MetadataSelection{Kind: "configuration"})
	if err != nil || conf.Name != "Fixture" || conf.Vendor != "A & B" || conf.ObjectCounts["Catalog"] != 1 {
		t.Fatalf("config %+v %v", conf, err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "Catalogs/Items.xml"))
	hash := sha256.Sum256(before)
	obj, err := ReadMetadataSummary(ctx, dir, MetadataSelection{Kind: "object", ObjectType: "Catalog", Name: "Items"})
	if err != nil || len(obj.Attributes) != 1 || len(obj.TableParts) != 1 || obj.Attributes[0].Types[0] != "cfg:CatalogRef.Organizations" || len(obj.Warnings) == 0 {
		t.Fatalf("object %+v %v", obj, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "Catalogs/Items.xml"))
	if hash != sha256.Sum256(after) {
		t.Fatal("source changed")
	}
	sub, err := ReadMetadataSummary(ctx, dir, MetadataSelection{Kind: "subsystem", Name: "Main"})
	if err != nil || len(sub.Content) != 1 || len(sub.Children) != 1 {
		t.Fatalf("subsystem %+v %v", sub, err)
	}
	sub, err = ReadMetadataSummary(ctx, dir, MetadataSelection{Kind: "subsystem", Name: "Подсистема.Main.Child"})
	if err != nil || sub.Name != "Child" {
		t.Fatalf("nested %+v %v", sub, err)
	}
	wrapped := t.TempDir()
	if err := os.Rename(dir, filepath.Join(wrapped, "Wrapped")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMetadataSummary(ctx, wrapped, MetadataSelection{Kind: "configuration"})
	if err != nil || got.Sources[0].Path != "Wrapped/Configuration.xml" {
		t.Fatalf("wrapped %+v %v", got, err)
	}
}
func TestMetadataSummaryRefusalsAndRecovery(t *testing.T) {
	dir := summaryFixture(t)
	ctx := context.Background()
	selection := MetadataSelection{Kind: "object", ObjectType: "Catalog", Name: "Items"}
	path := filepath.Join(dir, "Catalogs/Items.xml")
	good, _ := os.ReadFile(path)
	for _, bad := range []string{`<MetaDataObject><Catalog>`, `<MetaDataObject><Catalog><Properties><Name>Items</Name><Name>Other</Name></Properties></Catalog></MetaDataObject>`, strings.Repeat("<x>", 66) + strings.Repeat("</x>", 66), strings.Repeat("x", summaryMaxXML+1)} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadMetadataSummary(ctx, dir, selection); err == nil {
			t.Fatalf("accepted malformed/oversize/duplicate source")
		}
	}
	_ = os.WriteFile(path, good, 0600)
	if _, err := ReadMetadataSummary(ctx, dir, selection); err != nil {
		t.Fatal("recovery", err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "Catalogs/Items/Ext"), 0700)
	_ = os.WriteFile(filepath.Join(dir, "Catalogs/Items/Ext/Items.xml"), good, 0600)
	if _, err := ReadMetadataSummary(ctx, dir, selection); err == nil {
		t.Fatal("accepted duplicate source layout")
	}
	_ = os.Remove(filepath.Join(dir, "Catalogs/Items/Ext/Items.xml"))
	_ = os.Remove(path)
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret.xml"), good, 0600)
	_ = os.Symlink(filepath.Join(outside, "secret.xml"), path)
	if _, err := ReadMetadataSummary(ctx, dir, selection); err == nil {
		t.Fatal("accepted escaping symlink")
	}
	if _, err := ReadMetadataSummary(ctx, dir, MetadataSelection{Kind: "object", ObjectType: "Catalog", Name: "../Items"}); err == nil {
		t.Fatal("accepted traversal")
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ReadMetadataSummary(cancelCtx, dir, selection); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := ReadMetadataSummary(ctx, "", MetadataSelection{}); err == nil {
		t.Fatal("missing root became valid empty config")
	}
}

func TestMetadataSummaryLegacySubsystemAndNestedDuplicates(t *testing.T) {
	root := summaryFixture(t)
	if err := os.RemoveAll(filepath.Join(root, "Subsystems")); err != nil {
		t.Fatal(err)
	}
	rel := "Subsystems/Main/Ext/Subsystem.xml"
	path := filepath.Join(root, rel)
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(`<MetaDataObject><Subsystem><Properties><Name>Main</Name><Content><Item>Catalog.Items</Item></Content></Properties><ChildObjects><Subsystem><Properties><Name>Child</Name><Content><Item>Catalog.Items</Item></Content></Properties><ChildObjects/></Subsystem></ChildObjects></Subsystem></MetaDataObject>`), 0600)
	got, err := ReadMetadataSummary(context.Background(), root, MetadataSelection{Kind: "subsystem", Name: "Child"})
	if err != nil || got.Name != "Child" || len(got.Content) != 1 || got.Sources[0].Path != rel {
		t.Fatalf("legacy nested %+v %v", got, err)
	}
	objPath := filepath.Join(root, "Catalogs/Items.xml")
	data, _ := os.ReadFile(objPath)
	bad := strings.Replace(string(data), `<Name>Quantity</Name>`, `<Name>Quantity</Name><Name>Duplicate</Name>`, 1)
	_ = os.WriteFile(objPath, []byte(bad), 0600)
	if _, err := ReadMetadataSummary(context.Background(), root, MetadataSelection{Kind: "object", ObjectType: "Catalog", Name: "Items"}); err == nil {
		t.Fatal("duplicate nested identity accepted")
	}
}
