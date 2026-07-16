package dump

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// appliedUniverseCollections are the 15 applied-kind live BSL collection property names
// (Russian plural). They are pinned here because the applied membership tables store the
// Russian SINGULAR (metadataTypes.RussianName), not the collection-property plural the
// extension enumerates, so the cross-file sync check needs the plural spelling to assert
// the FULL ПрикладныеКоллекции set (applied 15 + the service kinds below).
var appliedUniverseCollections = []string{
	"Справочники", "Документы", "Перечисления", "Отчеты", "Обработки",
	"РегистрыСведений", "РегистрыНакопления", "РегистрыБухгалтерии", "РегистрыРасчета",
	"ПланыСчетов", "ПланыВидовХарактеристик", "ПланыВидовРасчета", "ПланыОбмена",
	"БизнесПроцессы", "Задачи",
}

// TestBSLUniverseInSyncWithGo reads the live extension's ПодсистемыGET universe list
// (ПрикладныеКоллекции) straight from Module.bsl and asserts it equals, exactly, the Go
// universe: the applied 15 plus every universeServiceKinds.bslCollection. This is the
// single anti-drift guard that keeps the LIVE path and the DUMP path enumerating the SAME
// set of kinds. If either side gains or loses a kind without the other, it fails, so the
// two implementations of the orphans universe cannot silently diverge.
func TestBSLUniverseInSyncWithGo(t *testing.T) {
	data, err := os.ReadFile(moduleBSLPath(t))
	if err != nil {
		t.Fatalf("cannot read Module.bsl (the live extension source must ship in this repo): %v", err)
	}
	got := parsePrikladnyeKollekcii(t, string(data))

	want := map[string]bool{}
	for _, c := range appliedUniverseCollections {
		want[c] = true
	}
	for _, k := range universeServiceKinds {
		want[k.bslCollection] = true
	}

	for c := range got {
		if !want[c] {
			t.Errorf("Module.bsl ПрикладныеКоллекции lists %q which is NOT in the Go universe (drift)", c)
		}
	}
	for c := range want {
		if !got[c] {
			t.Errorf("the Go universe expects collection %q which is MISSING from Module.bsl ПрикладныеКоллекции (drift)", c)
		}
	}
	if len(got) != len(want) {
		t.Errorf("collection count mismatch: Module.bsl=%d, Go=%d", len(got), len(want))
	}
}

// moduleBSLPath resolves the in-repo Module.bsl path relative to THIS test file, so the
// check is independent of the working directory the tests run from.
func moduleBSLPath(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate Module.bsl")
	}
	// dump/<this test> -> repo root -> extension/src/...
	return filepath.Join(filepath.Dir(self), "..", "extension", "src", "HTTPServices", "MCPService", "Ext", "Module.bsl")
}

// parsePrikladnyeKollekcii extracts the ПрикладныеКоллекции.Добавить("X") entries from the
// ПодсистемыGET universe block (between `ПрикладныеКоллекции = Новый Массив` and the next
// `ВсеОбъекты = Новый Массив`). It fails loudly if the block or its entries cannot be found,
// so a renamed block can never make this guard vacuously pass.
func parsePrikladnyeKollekcii(t *testing.T, src string) map[string]bool {
	t.Helper()
	start := strings.Index(src, "ПрикладныеКоллекции = Новый Массив")
	if start < 0 {
		t.Fatal("ПрикладныеКоллекции block not found in Module.bsl")
	}
	rest := src[start:]
	end := strings.Index(rest, "ВсеОбъекты = Новый Массив")
	if end < 0 {
		t.Fatal("end marker (ВсеОбъекты = Новый Массив) not found after ПрикладныеКоллекции in Module.bsl")
	}
	block := rest[:end]
	re := regexp.MustCompile(`ПрикладныеКоллекции\.Добавить\("([^"]+)"\)`)
	found := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		found[m[1]] = true
	}
	if len(found) == 0 {
		t.Fatal("no ПрикладныеКоллекции.Добавить entries parsed from Module.bsl")
	}
	return found
}
