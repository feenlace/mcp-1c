package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feenlace/mcp-1c/onec"
)

// ---- dump fixture helpers (Hierarchical / Root shape) ----

func dumpWrite(t *testing.T, dir, body string, segs ...string) {
	t.Helper()
	p := filepath.Join(append([]string{dir}, segs...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func subBody(name string, content ...string) string {
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

func applObj(name string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
  <Properties><Name>` + name + `</Name></Properties>
</MetaDataObject>
`
}

// ---- nil source when there is no dump ----

func TestDumpSources_NilWhenNoDump(t *testing.T) {
	if DumpSubsystemForestFunc("") != nil {
		t.Errorf("DumpSubsystemForestFunc(\"\") must be nil (selects live path)")
	}
	if DumpObjectStructFunc("") != nil {
		t.Errorf("DumpObjectStructFunc(\"\") must be nil (selects live path)")
	}
}

// ---- forest func: tree shape + universe ----

func TestDumpSubsystemForestFunc_ForestShapeAndUniverse(t *testing.T) {
	dir := t.TempDir()
	dumpWrite(t, dir, subBody("Продажи", "Document.Реализация"), "Subsystems", "Продажи.xml")
	dumpWrite(t, dir, applObj("Реализация"), "Documents", "Реализация.xml")
	dumpWrite(t, dir, applObj("Возврат"), "Documents", "Возврат.xml")

	forest, err := DumpSubsystemForestFunc(dir)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(forest.Subsystems) != 1 {
		t.Fatalf("Subsystems = %d, want 1: %+v", len(forest.Subsystems), forest.Subsystems)
	}
	root := forest.Subsystems[0]
	if root.Name != "Продажи" || root.FullName != "Подсистема.Продажи" {
		t.Errorf("root = {Name:%q FullName:%q}, want {Продажи, Подсистема.Продажи}", root.Name, root.FullName)
	}
	if len(root.Content) != 1 || root.Content[0] != "Документ.Реализация" {
		t.Errorf("root.Content = %v, want [Документ.Реализация]", root.Content)
	}
	uni := map[string]bool{}
	for _, o := range forest.AllObjects {
		uni[o] = true
	}
	if !uni["Документ.Реализация"] || !uni["Документ.Возврат"] {
		t.Errorf("AllObjects = %v, want to include Документ.Реализация and Документ.Возврат", forest.AllObjects)
	}
}

// ---- forest func: end-to-end through the analyze_subsystems handler ----

func TestDumpSubsystemForestFunc_EndToEndOrphansContaining(t *testing.T) {
	dir := t.TempDir()
	dumpWrite(t, dir, subBody("Продажи", "Document.Реализация"), "Subsystems", "Продажи.xml")
	dumpWrite(t, dir, applObj("Реализация"), "Documents", "Реализация.xml")
	dumpWrite(t, dir, applObj("Возврат"), "Documents", "Возврат.xml")

	// nil client: a source is present, so HTTP is never contacted.
	h := NewAnalyzeSubsystemsHandlerWithSource(nil, DumpSubsystemForestFunc(dir))

	orphans, err := runHandlerText(t, h, "analyze_subsystems", map[string]any{"action": "orphans"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, orphans, "Документ.Возврат")       // orphan (in no subsystem)
	mustNotContain(t, orphans, "Документ.Реализация") // distributed

	containing, err := runHandlerText(t, h, "analyze_subsystems", map[string]any{"action": "containing", "object": "Реализация"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, containing, "Продажи")
}

// ---- object_structure func: the type-routing (fall-through) contract ----

func TestDumpObjectStructFunc_SubsystemHandled(t *testing.T) {
	dir := t.TempDir()
	dumpWrite(t, dir, subBody("Продажи", "Document.Реализация"), "Subsystems", "Продажи.xml")
	dumpWrite(t, dir, subBody("Розница", "Catalog.Склады"), "Subsystems", "Продажи", "Subsystems", "Розница.xml")

	obj, handled, err := DumpObjectStructFunc(dir)(context.Background(), "Subsystem", "Продажи")
	if !handled {
		t.Fatal("handled = false, want true (offline owns the Subsystem type)")
	}
	if err != nil {
		t.Fatal(err)
	}
	if obj.Name != "Продажи" {
		t.Errorf("obj.Name = %q, want Продажи", obj.Name)
	}
	if len(obj.Content) != 1 || obj.Content[0] != "Документ.Реализация" {
		t.Errorf("obj.Content = %v, want [Документ.Реализация]", obj.Content)
	}
	if len(obj.Subsystems) != 1 || obj.Subsystems[0].Name != "Розница" {
		t.Errorf("obj.Subsystems = %+v, want one child Розница", obj.Subsystems)
	}
}

func TestDumpObjectStructFunc_NonSubsystemFallsThrough(t *testing.T) {
	dir := t.TempDir()
	dumpWrite(t, dir, subBody("Продажи"), "Subsystems", "Продажи.xml")

	obj, handled, err := DumpObjectStructFunc(dir)(context.Background(), "Catalog", "Контрагенты")
	if handled {
		t.Errorf("handled = true for Catalog, want false (must fall through to live)")
	}
	if err != nil {
		t.Errorf("err = %v, want nil on fall-through", err)
	}
	if obj.Name != "" {
		t.Errorf("obj = %+v, want zero value on fall-through", obj)
	}
}

func TestDumpObjectStructFunc_GenuineNotFound(t *testing.T) {
	dir := t.TempDir()
	dumpWrite(t, dir, subBody("Продажи"), "Subsystems", "Продажи.xml")

	_, handled, err := DumpObjectStructFunc(dir)(context.Background(), "Subsystem", "НетТакой")
	if !handled {
		t.Fatal("handled = false, want true")
	}
	if err == nil || err.Error() != "подсистема НетТакой не найдена в дампе" {
		t.Fatalf("err = %v, want the plain not-found error", err)
	}
}

// R-23: object_structure on a dropped (malformed) subsystem must not masquerade as
// a clean 404; the drop is surfaced (named in warnings) instead.
func TestDumpObjectStructFunc_DroppedSubsystemCarriesWarnings(t *testing.T) {
	dir := t.TempDir()
	dumpWrite(t, dir,
		`<?xml version="1.0"?><MetaDataObject><Subsystem><Properties><Name>Продажи</Name>`,
		"Subsystems", "Продажи.xml")

	obj, handled, err := DumpObjectStructFunc(dir)(context.Background(), "Subsystem", "Продажи")
	if !handled {
		t.Fatal("handled = false, want true")
	}
	if err == nil && len(obj.Warnings) == 0 {
		t.Fatal("a dropped subsystem surfaced as a clean result with no diagnostic")
	}
	if len(obj.Warnings) > 0 && !strings.Contains(strings.Join(obj.Warnings, "; "), "Продажи") {
		t.Errorf("warnings must NAME the dropped subsystem Продажи; got %v", obj.Warnings)
	}
}

func TestDumpObjectStructFunc_Ambiguous(t *testing.T) {
	dir := t.TempDir()
	dumpWrite(t, dir, subBody("Продажи"), "Subsystems", "Продажи.xml")
	dumpWrite(t, dir, subBody("Отчеты"), "Subsystems", "Продажи", "Subsystems", "Отчеты.xml")
	dumpWrite(t, dir, subBody("Закупки"), "Subsystems", "Закупки.xml")
	dumpWrite(t, dir, subBody("Отчеты"), "Subsystems", "Закупки", "Subsystems", "Отчеты.xml")

	obj, handled, err := DumpObjectStructFunc(dir)(context.Background(), "Subsystem", "Отчеты")
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v, want handled=true err=nil", handled, err)
	}
	want := []string{"Подсистема.Закупки.Отчеты", "Подсистема.Продажи.Отчеты"}
	if len(obj.Ambiguous) != 2 || obj.Ambiguous[0] != want[0] || obj.Ambiguous[1] != want[1] {
		t.Errorf("Ambiguous = %v, want %v (sorted full paths)", obj.Ambiguous, want)
	}
}

func TestDumpObjectStructFunc_ByFullPath(t *testing.T) {
	dir := t.TempDir()
	dumpWrite(t, dir, subBody("Продажи"), "Subsystems", "Продажи.xml")
	dumpWrite(t, dir, subBody("Отчеты"), "Subsystems", "Продажи", "Subsystems", "Отчеты.xml")
	dumpWrite(t, dir, subBody("Закупки"), "Subsystems", "Закупки.xml")
	dumpWrite(t, dir, subBody("Отчеты"), "Subsystems", "Закупки", "Subsystems", "Отчеты.xml")

	obj, handled, err := DumpObjectStructFunc(dir)(context.Background(), "Subsystem", "Подсистема.Продажи.Отчеты")
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v, want handled=true err=nil", handled, err)
	}
	if obj.Name != "Отчеты" || len(obj.Ambiguous) != 0 {
		t.Errorf("by-full-path resolve = {Name:%q Ambiguous:%v}, want a single Отчеты", obj.Name, obj.Ambiguous)
	}
}

// ---- panic recovery: a panic becomes a bounded, path-free error ----

func TestRecoverToDumpError_ConvertsPanicPathFree(t *testing.T) {
	err := func() (err error) {
		defer recoverToDumpError(&err)
		panic("boom /var/lib/onec/secret/path")
	}()
	if err == nil {
		t.Fatal("want an error from a recovered panic")
	}
	if strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "secret") {
		t.Errorf("recovered error leaks panic detail: %v", err)
	}
}

func TestRecoverToStructError_ConvertsPanicHandledTruePathFree(t *testing.T) {
	obj := onec.ObjectStructure{Name: "before"} // non-zero, must be reset on panic
	var handled bool
	err := func() (err error) {
		defer recoverToStructError(&obj, &handled, &err)
		panic("boom /secret/path")
	}()
	if !handled {
		t.Error("handled = false, want true (offline owns the type even on panic)")
	}
	if err == nil {
		t.Fatal("want an error from a recovered panic")
	}
	if strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "secret") {
		t.Errorf("recovered struct error leaks panic detail: %v", err)
	}
	if obj.Name != "" {
		t.Errorf("obj must be reset to zero on recovered panic, got %+v", obj)
	}
}

// ---- end-to-end parity vectors through the handlers ----

// R-40: an NFD-authored Content reference cancels against the NFC universe key, so
// a distributed object is not falsely reported as an orphan.
func TestDumpForest_NFDContentCancels(t *testing.T) {
	nfcMoy := string([]rune{'М', 'о', 0x0439})      // "Мой" NFC
	nfdMoy := string([]rune{'М', 'о', 'и', 0x0306}) // "Мой" NFD

	dir := t.TempDir()
	dumpWrite(t, dir, applObj(nfcMoy), "Catalogs", nfcMoy+".xml")
	dumpWrite(t, dir, subBody("Учет", "Catalog."+nfdMoy), "Subsystems", "Учет.xml")

	h := NewAnalyzeSubsystemsHandlerWithSource(nil, DumpSubsystemForestFunc(dir))
	out, err := runHandlerText(t, h, "analyze_subsystems", map[string]any{"action": "orphans"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Все применимые объекты распределены по подсистемам.") {
		t.Errorf("R-40: NFD-referenced object should cancel to zero orphans, got:\n%s", out)
	}
}

// R-38: object_structure shows the real synonym parsed from the dump.
func TestDumpObjectStruct_SynonymRendered(t *testing.T) {
	dir := t.TempDir()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
  <Subsystem>
    <Properties>
      <Name>Продажи</Name>
      <Synonym>
        <v8:item xmlns:v8="http://v8.1c.ru/8.1/data/core">
          <v8:lang>ru</v8:lang>
          <v8:content>Управление продажами</v8:content>
        </v8:item>
      </Synonym>
    </Properties>
  </Subsystem>
</MetaDataObject>`
	dumpWrite(t, dir, body, "Subsystems", "Продажи.xml")

	h := NewObjectStructureHandlerWithSource(nil, DumpObjectStructFunc(dir))
	out, err := runHandlerText(t, h, "get_object_structure", map[string]any{"object_type": "Subsystem", "object_name": "Продажи"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# Продажи (Управление продажами)") {
		t.Errorf("R-38: header must show the real synonym from the dump:\n%s", out)
	}
}
