package dump

import "testing"

// C1: service-kind canonicalization. These tests pin the English->Russian kind
// mapping the dump subsystem reader uses so a configuration dump's English kind
// prefixes (Document., CommonModule., ...) are rendered in the same canonical
// Russian form the live 1C extension emits, which is what makes the offline path
// byte-parity with live for object_structure(Subsystem) Content and for
// analyze_subsystems containing / intersections.

func TestServiceKindNameRu_KnownKinds(t *testing.T) {
	cases := map[string]string{
		"CommonModule":   "ОбщийМодуль",
		"CommonForm":     "ОбщаяФорма",
		"CommonCommand":  "ОбщаяКоманда",
		"CommonTemplate": "ОбщийМакет",
		"CommonPicture":  "ОбщаяКартинка",
		"Role":           "Роль",
		"HTTPService":    "HTTPСервис",
		"WebService":     "WebСервис",
		"ScheduledJob":   "РегламентноеЗадание",
		"CommandGroup":   "ГруппаКоманд",
	}
	for en, want := range cases {
		got, ok := ServiceKindNameRu(en)
		if !ok {
			t.Errorf("ServiceKindNameRu(%q): ok=false, want a mapping to %q", en, want)
			continue
		}
		if got != want {
			t.Errorf("ServiceKindNameRu(%q) = %q, want %q", en, got, want)
		}
	}
}

// The corrected value: DocumentNumerator maps to НумераторДокументов (the form the
// platform full name emits on a real base), NOT the shorter kind singular the
// syntax corpus records. This was a live-verified fix and must never regress.
func TestServiceKindNameRu_DocumentNumeratorCorrected(t *testing.T) {
	got, ok := ServiceKindNameRu("DocumentNumerator")
	if !ok {
		t.Fatalf("ServiceKindNameRu(DocumentNumerator): ok=false, want a mapping")
	}
	if got != "НумераторДокументов" {
		t.Errorf("ServiceKindNameRu(DocumentNumerator) = %q, want НумераторДокументов", got)
	}
	if got == "Нумератор" {
		t.Errorf("regression: DocumentNumerator maps to the OLD value Нумератор, must be НумераторДокументов")
	}
}

func TestServiceKindNameRu_CaseInsensitive(t *testing.T) {
	for _, en := range []string{"commonmodule", "COMMONMODULE", "CommonModule", "cOmMoNmOdUlE"} {
		got, ok := ServiceKindNameRu(en)
		if !ok || got != "ОбщийМодуль" {
			t.Errorf("ServiceKindNameRu(%q) = (%q,%v), want (ОбщийМодуль,true)", en, got, ok)
		}
	}
}

func TestServiceKindNameRu_AppliedKindsAndUnknownReturnFalse(t *testing.T) {
	// Applied/table kinds are resolved via the applied maps, not this function.
	for _, en := range []string{"Document", "Catalog", "Enum", "Task", "AccumulationRegister"} {
		if ru, ok := ServiceKindNameRu(en); ok {
			t.Errorf("ServiceKindNameRu(%q) = (%q,true), want false for an applied kind", en, ru)
		}
	}
	for _, en := range []string{"Bogus", "", "NotAKind", "Документ"} {
		if ru, ok := ServiceKindNameRu(en); ok {
			t.Errorf("ServiceKindNameRu(%q) = (%q,true), want false for an unknown prefix", en, ru)
		}
	}
}

func TestCanonicalizeContentPath_AppliedKinds(t *testing.T) {
	cases := map[string]string{
		"Document.РеализацияТоваров":     "Документ.РеализацияТоваров",
		"Catalog.Контрагенты":            "Справочник.Контрагенты",
		"AccumulationRegister.Продажи":   "РегистрНакопления.Продажи",
		"Enum.СтатусыЗаказов":            "Перечисление.СтатусыЗаказов",
		"InformationRegister.КурсыВалют": "РегистрСведений.КурсыВалют",
	}
	for raw, want := range cases {
		if got := canonicalizeContentPath(raw); got != want {
			t.Errorf("canonicalizeContentPath(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCanonicalizeContentPath_ServiceKinds(t *testing.T) {
	cases := map[string]string{
		"CommonModule.ОбщегоНазначения":  "ОбщийМодуль.ОбщегоНазначения",
		"CommonCommand.АвтономнаяРабота": "ОбщаяКоманда.АвтономнаяРабота",
		"Role.Администратор":             "Роль.Администратор",
		"DocumentNumerator.Основной":     "НумераторДокументов.Основной",
	}
	for raw, want := range cases {
		if got := canonicalizeContentPath(raw); got != want {
			t.Errorf("canonicalizeContentPath(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCanonicalizeContentPath_AlreadyRussianAppliedPrefixKept(t *testing.T) {
	// A dump that already writes a canonical Russian applied prefix must be kept
	// as-is (identity), not dropped or double-mapped.
	if got := canonicalizeContentPath("Документ.РеализацияТоваров"); got != "Документ.РеализацияТоваров" {
		t.Errorf("canonicalizeContentPath(RU applied) = %q, want unchanged", got)
	}
}

func TestCanonicalizeContentPath_UnknownPrefixPreserved(t *testing.T) {
	// Unknown prefixes preserve dump fidelity (returned unchanged).
	if got := canonicalizeContentPath("НекийНовыйВид.Объект"); got != "НекийНовыйВид.Объект" {
		t.Errorf("canonicalizeContentPath(unknown) = %q, want unchanged", got)
	}
}

func TestCanonicalizeContentPath_NoDotDropped(t *testing.T) {
	for _, raw := range []string{"Документ", "", "БезТочки"} {
		if got := canonicalizeContentPath(raw); got != "" {
			t.Errorf("canonicalizeContentPath(%q) = %q, want empty (no dot)", raw, got)
		}
	}
}

// R-40: an object name authored in decomposed (NFD) form in the dump XML must be
// recomposed to NFC so it matches the NFC-normalised universe key (else it would
// show up as a false orphan).
func TestCanonicalizeContentPath_NFCNormalisesSuffix(t *testing.T) {
	nfdMoy := string([]rune{'М', 'о', 'и', 0x0306}) // "Мой" with a decomposed й (NFD)
	nfcMoy := string([]rune{'М', 'о', 0x0439})      // "Мой" with a precomposed й (NFC)
	got := canonicalizeContentPath("Catalog." + nfdMoy)
	want := "Справочник." + nfcMoy
	if got != want {
		t.Errorf("canonicalizeContentPath(NFD) = %q, want NFC %q", got, want)
	}
}

// The applied-kind universe set must carry exactly the 15 applied kinds and must
// NOT include Constant (the live extension does not count constants as applied
// objects for subsystem membership).
func TestAppliedKindRu_HasFifteenAndExcludesConstant(t *testing.T) {
	if len(appliedKindRu) != 15 {
		t.Errorf("appliedKindRu has %d entries, want 15: %v", len(appliedKindRu), appliedKindRu)
	}
	if appliedKindRu["Константа"] {
		t.Errorf("appliedKindRu must NOT include Константа (Constant is not an applied object)")
	}
	for _, ru := range []string{"Справочник", "Документ", "Перечисление", "Отчет", "Обработка"} {
		if !appliedKindRu[ru] {
			t.Errorf("appliedKindRu missing expected applied prefix %q", ru)
		}
	}
}
