package dump

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// objBody is a minimal applied-object XML file. Enumeration only confirms the
// file exists (it never parses it), so the body content is immaterial.
func objBody(name string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
  <Properties><Name>` + name + `</Name></Properties>
</MetaDataObject>
`
}

func TestEnumerateAppliedObjects_MixedKindsAndShapes(t *testing.T) {
	dir := t.TempDir()
	// Root shape.
	secWrite(t, filepath.Join(dir, "Catalogs", "Контрагенты.xml"), objBody("Контрагенты"))
	secWrite(t, filepath.Join(dir, "Documents", "Реализация.xml"), objBody("Реализация"))
	// Ext shape (<Folder>/<Name>/Ext/<Name>.xml).
	secWrite(t, filepath.Join(dir, "Reports", "Продажи", "Ext", "Продажи.xml"), objBody("Продажи"))

	got := EnumerateAppliedObjects(dir)
	want := []string{"Документ.Реализация", "Отчет.Продажи", "Справочник.Контрагенты"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnumerateAppliedObjects = %v, want %v", got, want)
	}
}

func TestEnumerateAppliedObjects_ExcludesNonApplied(t *testing.T) {
	dir := t.TempDir()
	// Applied (must appear).
	secWrite(t, filepath.Join(dir, "Catalogs", "Валюты.xml"), objBody("Валюты"))
	// Non-applied (must NOT appear): Constant, CommonModule, Subsystem, Role.
	secWrite(t, filepath.Join(dir, "Constants", "Ставка.xml"), objBody("Ставка"))
	secWrite(t, filepath.Join(dir, "CommonModules", "Обмен", "Ext", "Обмен.xml"), objBody("Обмен"))
	secWrite(t, filepath.Join(dir, "Subsystems", "Продажи.xml"), objBody("Продажи"))
	secWrite(t, filepath.Join(dir, "Roles", "Администратор.xml"), objBody("Администратор"))

	got := EnumerateAppliedObjects(dir)
	want := []string{"Справочник.Валюты"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnumerateAppliedObjects = %v, want %v (non-applied kinds must be excluded)", got, want)
	}
}

func TestEnumerateAppliedObjects_LegacySingularFolder(t *testing.T) {
	dir := t.TempDir()
	// Legacy singular ConfigurationDump folder name.
	secWrite(t, filepath.Join(dir, "Document", "Реализация.xml"), objBody("Реализация"))
	got := EnumerateAppliedObjects(dir)
	want := []string{"Документ.Реализация"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnumerateAppliedObjects(singular folder) = %v, want %v", got, want)
	}
}

func TestEnumerateAppliedObjects_NFCNormalisesName(t *testing.T) {
	nfdName := string([]rune{'М', 'о', 'и', 0x0306}) // "Мой" NFD
	nfcName := string([]rune{'М', 'о', 0x0439})      // "Мой" NFC
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Catalogs", nfdName+".xml"), objBody(nfdName))
	got := EnumerateAppliedObjects(dir)
	want := []string{"Справочник." + nfcName}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnumerateAppliedObjects(NFD name) = %v, want NFC %v", got, want)
	}
}

func TestEnumerateAppliedObjects_EmptyWhenNoAppliedFolders(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Subsystems", "S.xml"), objBody("S"))
	if got := EnumerateAppliedObjects(dir); len(got) != 0 {
		t.Errorf("EnumerateAppliedObjects = %v, want empty when there are no applied folders", got)
	}
}

func TestEnumerateAppliedObjects_StrayNonXmlIgnored(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Catalogs", "Валюты.xml"), objBody("Валюты"))
	secWrite(t, filepath.Join(dir, "Catalogs", "README.txt"), "not an object")
	got := EnumerateAppliedObjects(dir)
	want := []string{"Справочник.Валюты"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnumerateAppliedObjects = %v, want %v (stray non-.xml ignored)", got, want)
	}
}

// The orphans cancellation invariant: an applied object's UNIVERSE string must be
// byte-identical to the MEMBERSHIP string canonicalizeContentPath produces for the
// same object referenced from a subsystem Состав. If they ever diverge, a
// distributed object would be miscounted as an orphan.
func TestEnumerateAppliedObjects_CancellationInvariant(t *testing.T) {
	dir := t.TempDir()
	secWrite(t, filepath.Join(dir, "Documents", "Реализация.xml"), objBody("Реализация"))
	secWrite(t, filepath.Join(dir, "Catalogs", "Контрагенты", "Ext", "Контрагенты.xml"), objBody("Контрагенты"))

	universe := EnumerateAppliedObjects(dir)
	uni := map[string]bool{}
	for _, u := range universe {
		uni[u] = true
	}
	for raw := range map[string]bool{"Document.Реализация": true, "Catalog.Контрагенты": true} {
		membership := canonicalizeContentPath(raw)
		if !uni[membership] {
			t.Errorf("cancellation broken: membership %q (from %q) not byte-present in universe %v",
				membership, raw, universe)
		}
	}
}

func TestEnumerateAppliedObjects_SymlinkFileEscapeSkipped(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	secretXML := filepath.Join(outside, "Secret.xml")
	if err := os.WriteFile(secretXML, []byte(objBody("Secret")), 0o644); err != nil {
		t.Fatal(err)
	}
	secWrite(t, filepath.Join(dir, "Catalogs", "Real.xml"), objBody("Real"))
	// Catalogs/Evil.xml -> outside/Secret.xml (escapes the dump).
	if err := os.Symlink(secretXML, filepath.Join(dir, "Catalogs", "Evil.xml")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got := EnumerateAppliedObjects(dir)
	want := []string{"Справочник.Real"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnumerateAppliedObjects = %v, want %v (escaping symlink must be skipped)", got, want)
	}
}

func TestEnumerateAppliedObjects_CapBounded(t *testing.T) {
	old := maxUniverseObjects
	maxUniverseObjects = 3
	defer func() { maxUniverseObjects = old }()

	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		name := "Об" + string(rune('А'+i))
		secWrite(t, filepath.Join(dir, "Catalogs", name+".xml"), objBody(name))
	}
	if got := EnumerateAppliedObjects(dir); len(got) > maxUniverseObjects {
		t.Errorf("cap not enforced: got %d objects, cap %d", len(got), maxUniverseObjects)
	}
}
