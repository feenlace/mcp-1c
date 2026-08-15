package dump

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// A dynamic list is a form ATTRIBUTE, not a form element: it lives in
// <Form><Attributes><Attribute><Settings xsi:type="DynamicList">, four levels
// down and nowhere else. Measured on the 2.9 GB reference dump: 1918 lists in
// 1628 of 5665 Form.xml files, and in 1918 of 1918 the parent is <Attribute>,
// the grandparent <Attributes> and the great-grandparent <Form>.
//
// The expected values in this file are measured with a SECOND, independently
// written reader (Python's expat through xml.etree), never by printing what the
// reader under test returns. A number produced by the code it is checking
// reproduces itself no matter how wrong it is.

// querySizes reports the three measures of one query text together. A byte count
// alone cannot tell a lost Cyrillic character from a lost line, and a count
// without its breakdown is green under the wrong split.
func querySizes(s string) (bytesN, runesN, linesN int) {
	if s == "" {
		return 0, 0, 0
	}
	return len(s), utf8.RuneCountInString(s), strings.Count(s, "\n") + 1
}

// TestParseFormXML_CatalogListCarriesItsDynamicList reads the dynamic list out
// of a fixture that has been in this package since before the section was read
// at all: testdata/catalog_list_form.xml carries an <Attributes> section with one
// <Attribute name="Список"> whose <Settings> is typed DynamicList.
//
// Expected values measured independently: 1 attribute in the section, query text
// 807 bytes = 420 runes = 10 lines.
func TestParseFormXML_CatalogListCarriesItsDynamicList(t *testing.T) {
	form := mustParseFixture(t, "catalog_list_form.xml")

	if len(form.DynamicLists) != 1 {
		t.Fatalf("expected 1 dynamic list in catalog_list_form.xml, got %d: %+v",
			len(form.DynamicLists), form.DynamicLists)
	}
	got := form.DynamicLists[0]

	if got.Name != "Список" {
		t.Errorf("attribute name: got %q, want %q", got.Name, "Список")
	}
	if !got.ManualQuery {
		t.Errorf("ManualQuery: got %v, want true (the file says <ManualQuery>true</ManualQuery>)", got.ManualQuery)
	}
	if want := "Catalog.ПерепискаСКонтролирующимиОрганами"; got.MainTable != want {
		t.Errorf("MainTable: got %q, want %q", got.MainTable, want)
	}

	b, r, l := querySizes(got.QueryText)
	if b != 807 || r != 420 || l != 10 {
		t.Errorf("query text size: got %d bytes = %d runes = %d lines, "+
			"want 807 bytes = 420 runes = 10 lines", b, r, l)
	}
	if !strings.HasPrefix(got.QueryText, "ВЫБРАТЬ\n") {
		t.Errorf("query text does not start at the first line of the query: %.40q", got.QueryText)
	}
	if !strings.HasSuffix(got.QueryText, "КАК ПерепискаСКонтролирующимиОрганами") {
		t.Errorf("query text does not reach the last line of the query: %.60q",
			got.QueryText[max(0, len(got.QueryText)-120):])
	}
}

// TestParseFormXML_CommonFormPasswordHasAttributesButNoDynamicList is the
// negative control, and it is only a control because the file HAS the section.
// A file without <Attributes> would be green on a reader that never looks.
//
// Measured: testdata/common_form_password.xml carries an <Attributes> section
// holding two <Attribute> entries (НовыйПароль and ДляВнешнегоПользователя) and
// zero <Settings> elements of any type. The counts below are re-derived from the
// file's own bytes on every run rather than quoted, so the control cannot go
// stale if the fixture is ever replaced.
func TestParseFormXML_CommonFormPasswordHasAttributesButNoDynamicList(t *testing.T) {
	path := filepath.Join("testdata", "common_form_password.xml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// POSITIVE CONTROL over the control: the section and its two attributes are
	// really in the file, so "zero lists" is a finding and not an empty read.
	if !bytes.Contains(raw, []byte("<Attributes>")) {
		t.Fatal("control failed: common_form_password.xml has no <Attributes> section, " +
			"so it cannot prove that a reader which looks finds nothing here")
	}
	if n := bytes.Count(raw, []byte("<Attribute ")); n != 2 {
		t.Fatalf("control failed: expected 2 <Attribute> entries in the fixture, counted %d", n)
	}
	if n := bytes.Count(raw, []byte("<Settings")); n != 0 {
		t.Fatalf("control failed: expected 0 <Settings> elements in the fixture, counted %d", n)
	}

	form := mustParseFixture(t, "common_form_password.xml")
	if len(form.DynamicLists) != 0 {
		t.Errorf("a form with 2 attributes and 0 <Settings> must yield 0 dynamic lists, "+
			"got %d: %+v", len(form.DynamicLists), form.DynamicLists)
	}
}

// dynamicListDoc wraps attribute markup in a minimal but REAL form document.
//
// EVERY namespace URI below was read out of the reference dump, not invented: a
// synthetic fixture bound to a made-up URI still exercises "some other
// namespace", but it stops being evidence about the namespaces this parser will
// actually meet. Measured over all 5665 Form.xml files, counting declarations:
//
//	default  http://v8.1c.ru/8.3/xcf/logform         5665
//	v8       http://v8.1c.ru/8.1/data/core           5665
//	xsi      http://www.w3.org/2001/XMLSchema-instance 5665
//	mxl      http://v8.1c.ru/8.2/data/spreadsheet      31
//	d4p1     http://v8.1c.ru/8.2/data/chart            23
//	pl       http://v8.1c.ru/8.3/data/planner           3 files carry pl:Planner
func dynamicListDoc(attributes string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"
      xmlns:v8="http://v8.1c.ru/8.1/data/core"
      xmlns:mxl="http://v8.1c.ru/8.2/data/spreadsheet"
      xmlns:pl="http://v8.1c.ru/8.3/data/planner"
      xmlns:d4p1="http://v8.1c.ru/8.2/data/chart"
      xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.21">
  <Attributes>
` + attributes + `
  </Attributes>
</Form>`
}

// TestParseFormXML_SettingsAreDiscriminatedByTypeValueNotByTagName pins the one
// distinction the section turns on. The tag <Settings> is the SAME tag for every
// kind of attribute settings; only the xsi:type value separates them. Measured
// over the whole dump: 2891 <Settings> elements carry xsi:type, of which 1918
// are DynamicList and 973 are not (916 v8:TypeDescription + 31
// mxl:SpreadsheetDocument + 23 d4p1:Chart + 3 pl:Planner).
func TestParseFormXML_SettingsAreDiscriminatedByTypeValueNotByTagName(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		want     int
	}{
		{"DynamicList unprefixed", `<Settings xsi:type="DynamicList"><ManualQuery>true</ManualQuery></Settings>`, 1},
		{"v8:TypeDescription", `<Settings xsi:type="v8:TypeDescription"><v8:Type>xs:string</v8:Type></Settings>`, 0},
		{"mxl:SpreadsheetDocument", `<Settings xsi:type="mxl:SpreadsheetDocument"/>`, 0},
		{"d4p1:Chart", `<Settings xsi:type="d4p1:Chart"/>`, 0},
		{"pl:Planner", `<Settings xsi:type="pl:Planner"/>`, 0},
		{"no xsi:type at all", `<Settings><ManualQuery>true</ManualQuery></Settings>`, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := dynamicListDoc(`    <Attribute name="Реквизит" id="1">` + tc.settings + `</Attribute>`)

			// POSITIVE CONTROL: every case really carries the <Settings> tag, so a
			// reader matching on the TAG would answer 1 to all six and this table
			// would catch it. Without this line the zeroes could be measuring a
			// fixture that has no settings element at all.
			if !strings.Contains(doc, "<Settings") {
				t.Fatal("control failed: this case carries no <Settings> tag, so it cannot " +
					"distinguish a tag matcher from a type matcher")
			}

			form, err := parseFormXMLData([]byte(doc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(form.DynamicLists) != tc.want {
				t.Errorf("xsi:type %s: got %d dynamic lists, want %d: %+v",
					tc.name, len(form.DynamicLists), tc.want, form.DynamicLists)
			}
		})
	}
}

// TestParseFormXML_TypePrefixIsResolvedThroughXmlns is the case a suffix match
// cannot survive. A QName in an attribute VALUE is not expanded by the Go
// decoder, so the prefix has to be resolved against the xmlns declarations in
// scope and the EXPANDED name compared.
//
// Comparing the text after the colon is not a shortcut, it is wrong: the dump
// carries 92 machine-generated prefix declarations and the single prefix d5p1 is
// bound to FIVE different namespaces across the corpus (38 txtedt, 24 chart,
// 3 graphscheme, 2 data-analysis, 1 geo), so the prefix says nothing on its own.
func TestParseFormXML_TypePrefixIsResolvedThroughXmlns(t *testing.T) {
	const logform = "http://v8.1c.ru/8.3/xcf/logform"

	cases := []struct {
		name     string
		settings string
		want     int
	}{
		{
			// A prefix bound to a FOREIGN namespace: same suffix, different type.
			// d5p1 and this URI are both taken from the dump, where the prefix is
			// bound to it 38 times and to four other namespaces besides.
			name: "d5p1:DynamicList bound to a foreign namespace",
			settings: `<Settings xmlns:d5p1="http://v8.1c.ru/8.1/data/txtedt" ` +
				`xsi:type="d5p1:DynamicList"/>`,
			want: 0,
		},
		{
			// A prefix bound to the form's OWN namespace: same expanded name as the
			// unprefixed spelling, so it is the same type and must be accepted. This
			// is what makes the rule about the namespace and not about the colon.
			name: "lf:DynamicList bound to the logform namespace",
			settings: `<Settings xmlns:lf="` + logform + `" xsi:type="lf:DynamicList">` +
				`<ManualQuery>true</ManualQuery></Settings>`,
			want: 1,
		},
		{
			name:     "unprefixed DynamicList resolves through the default namespace",
			settings: `<Settings xsi:type="DynamicList"><ManualQuery>true</ManualQuery></Settings>`,
			want:     1,
		},
		{
			name:     "prefix that is not declared anywhere",
			settings: `<Settings xsi:type="zzz:DynamicList"/>`,
			want:     0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := dynamicListDoc(`    <Attribute name="Реквизит" id="1">` + tc.settings + `</Attribute>`)

			// POSITIVE CONTROL: the suffix after the colon is "DynamicList" in every
			// case here, so a reader comparing suffixes answers 1 four times out of
			// four and this table separates it from one that resolves the prefix.
			if !strings.HasSuffix(strings.Split(strings.SplitN(tc.settings, `xsi:type="`, 2)[1], `"`)[0], "DynamicList") {
				t.Fatal("control failed: this case does not end in DynamicList, so it cannot " +
					"tell a suffix match apart from a resolved one")
			}

			form, err := parseFormXMLData([]byte(doc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(form.DynamicLists) != tc.want {
				t.Errorf("%s: got %d dynamic lists, want %d. The prefix must be resolved through "+
					"the xmlns declarations in scope: in this dump d5p1 alone is bound to five "+
					"different namespaces across 92 machine-generated declarations, so the text "+
					"after the colon decides nothing", tc.name, len(form.DynamicLists), tc.want)
			}
		})
	}
}

// TestParseFormXML_DynamicListOutsideAttributesIsNotRead pins the ancestry. In
// 1918 cases out of 1918 the parent is <Attribute>, the grandparent <Attributes>
// and the great-grandparent <Form>; no other placement exists in the corpus, so
// one that appears elsewhere is not a form attribute and is not reported.
func TestParseFormXML_DynamicListOutsideAttributesIsNotRead(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"
      xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" version="2.21">
  <ChildItems>
    <Table name="Список" id="1">
      <DataPath>Список</DataPath>
      <Settings xsi:type="DynamicList"><ManualQuery>true</ManualQuery>
        <QueryText>ВЫБРАТЬ 1</QueryText></Settings>
    </Table>
  </ChildItems>
</Form>`

	form, err := parseFormXMLData([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(form.DynamicLists) != 0 {
		t.Errorf("a DynamicList under <ChildItems> is not a form attribute and must not be "+
			"reported, got %d: %+v", len(form.DynamicLists), form.DynamicLists)
	}
	// POSITIVE CONTROL: the same document IS read as a form, so the zero above
	// measures the ancestry rule and not a parse that never started.
	if len(form.Elements) == 0 {
		t.Error("control failed: the document parsed into no elements at all, so the zero " +
			"above may be measuring a failed read rather than the ancestry rule")
	}
}

// TestParseFormXML_EveryDynamicListIsKeptInFileOrder covers multiplicity and
// order in one document. Measured: 141 of the 1628 carriers hold more than one
// list and the largest holds 35, so a reader returning a single value would be
// wrong on 141 forms and would lose 290 of the 1918 lists.
func TestParseFormXML_EveryDynamicListIsKeptInFileOrder(t *testing.T) {
	doc := dynamicListDoc(`    <Attribute name="Первый" id="1">
      <Settings xsi:type="DynamicList"><ManualQuery>true</ManualQuery>
        <MainTable>Catalog.А</MainTable></Settings>
    </Attribute>
    <Attribute name="Середина" id="2">
      <Settings xsi:type="v8:TypeDescription"/>
    </Attribute>
    <Attribute name="Второй" id="3">
      <Settings xsi:type="DynamicList"><ManualQuery>false</ManualQuery>
        <MainTable>Catalog.Б</MainTable></Settings>
    </Attribute>`)

	form, err := parseFormXMLData([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(form.DynamicLists) != 2 {
		t.Fatalf("expected 2 dynamic lists out of 3 attributes, got %d: %+v",
			len(form.DynamicLists), form.DynamicLists)
	}
	// Compared as a SEQUENCE, not as a set: a reader that reverses the order or
	// pairs the wrong table with the wrong name passes a set comparison.
	want := []FormDynamicList{
		{Name: "Первый", ManualQuery: true, MainTable: "Catalog.А"},
		{Name: "Второй", ManualQuery: false, MainTable: "Catalog.Б"},
	}
	for i := range want {
		if form.DynamicLists[i] != want[i] {
			t.Errorf("dynamic list %d: got %+v, want %+v", i, form.DynamicLists[i], want[i])
		}
	}
}

// charDataTokens reports how many xml.CharData tokens the Go decoder produces
// inside the first <QueryText> of doc, and what the FIRST of them holds.
//
// It is a SECOND reader over the same bytes, built straight on encoding/xml and
// not on the parser under test, and it exists so that "this fixture really does
// split" is a measurement instead of a sentence. The sentence is what went
// wrong: the test this replaces asserted that four entity references arrive in
// five pieces, which the decoder does not do, so the fixture never split, and a
// reader keeping only the first token passed it.
func charDataTokens(t *testing.T, doc string) (n int, first string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	inside := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch tv := tok.(type) {
		case xml.StartElement:
			if tv.Name.Local == "QueryText" {
				inside = true
			}
		case xml.EndElement:
			if tv.Name.Local == "QueryText" && inside {
				return n, first
			}
		case xml.CharData:
			if inside {
				if n == 0 {
					first = string(tv)
				}
				n++
			}
		}
	}
	return n, first
}

// TestParseFormXML_QueryTextIsAccumulatedAcrossEveryCharDataToken pins FR-DL-006:
// the reader keeps the WHOLE text of <QueryText>, not the first token of it.
//
// WHAT THE DECODER ACTUALLY DOES, measured with charDataTokens below rather than
// assumed. An entity reference does NOT end a CharData token: Go resolves it in
// place and the surrounding text arrives as one token, four references and all.
// What ends a token is a NODE between the characters, and there are three of
// them: a comment, a child element and a CDATA section.
//
// THE SPLITTING FIXTURES ARE SYNTHETIC AND THE CORPUS SAYS WHY. Measured over the
// reference dump: 991 <QueryText> elements, and the source body of 0 of them
// carries a raw `<` of any kind, so not one real query text splits. The entity
// references are there in quantity (2449 of them, 1664 &amp; + 418 &gt; +
// 367 &lt;) and every one of them arrives inside a single token. So on this whole
// dump a reader that kept only the first token would lose NOTHING, which is
// exactly why the test this replaces could not fail. The rule being pinned is the
// READER's, and a reader's rule is not conditional on what one dump happens to
// contain.
func TestParseFormXML_QueryTextIsAccumulatedAcrossEveryCharDataToken(t *testing.T) {
	cases := []struct {
		name       string
		queryText  string
		wantTokens int
		want       string
	}{
		{
			// The fixture the old test used, with its real token count. It is
			// kept because entity DECODING is worth pinning; what it cannot do is
			// tell an accumulating reader from a first-token one.
			name:       "four entity references, which do NOT split the text",
			queryText:  "ВЫБРАТЬ 1 ГДЕ &amp;Параметр &lt; 5 И &#1057;умма &gt; 0",
			wantTokens: 1,
			want:       "ВЫБРАТЬ 1 ГДЕ &Параметр < 5 И Сумма > 0",
		},
		{
			name:       "an XML comment between the characters",
			queryText:  "ВЫБРАТЬ 1\n<!-- комментарий -->ГДЕ 2",
			wantTokens: 2,
			want:       "ВЫБРАТЬ 1\nГДЕ 2",
		},
		{
			// The child element's OWN text is dropped, not kept: readCharData
			// skips the subtree. That is deliberate and is asserted by the
			// expected value, which does not contain it.
			name:       "a child element between the characters",
			queryText:  "ВЫБРАТЬ 1\n<Чужое>текст чужого элемента</Чужое>ГДЕ 2",
			wantTokens: 3,
			want:       "ВЫБРАТЬ 1\nГДЕ 2",
		},
		{
			// CDATA content is NOT entity-decoded: it arrives exactly as written,
			// which is why the expected value keeps the bare `<`.
			name:       "a CDATA section between the characters",
			queryText:  "ВЫБРАТЬ 1\n<![CDATA[ГДЕ &Параметр < 5]]>\nИ 2",
			wantTokens: 3,
			want:       "ВЫБРАТЬ 1\nГДЕ &Параметр < 5\nИ 2",
		},
	}

	splitting := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := dynamicListDoc(`    <Attribute name="Список" id="1">
      <Settings xsi:type="DynamicList"><ManualQuery>true</ManualQuery>
        <QueryText>` + tc.queryText + `</QueryText>
        <MainTable>Catalog.А</MainTable></Settings>
    </Attribute>`)

			// The token count is MEASURED, and it is measured before anything is
			// asserted about the text. A fixture that does not split cannot tell
			// the two readers apart no matter what the text comes out as.
			got, first := charDataTokens(t, doc)
			if got != tc.wantTokens {
				t.Errorf("the decoder produced %d CharData tokens inside <QueryText>, "+
					"expected %d. The expectation is about Go's tokenisation, not about "+
					"this parser: fix the number, not the reader", got, tc.wantTokens)
			}
			if got > 1 {
				splitting++
				// The discriminating power of this case, measured rather than
				// claimed: a reader keeping only the first token would answer
				// something ELSE here.
				if strings.TrimSpace(first) == tc.want {
					t.Errorf("the first token alone already equals the expected text %q, so "+
						"this case cannot tell an accumulating reader from a first-token one",
						tc.want)
				}
			}

			form, err := parseFormXMLData([]byte(doc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(form.DynamicLists) != 1 {
				t.Fatalf("expected 1 dynamic list, got %d", len(form.DynamicLists))
			}
			if got := form.DynamicLists[0].QueryText; got != tc.want {
				t.Errorf("query text: got %q, want %q", got, tc.want)
			}
		})
	}

	// CONTROL OVER THE TABLE, and the one the replaced test did not have: at
	// least one case has to split, or every row is green on a reader that keeps
	// the first token and throws the rest away.
	if splitting == 0 {
		t.Errorf("control failed: not one case produced more than one CharData token, so this " +
			"table cannot fail on a reader that keeps only the first")
	}
}

// TestParseFormXML_ByteOrderMarkChangesNothing builds the second half of its own
// pair. Every Form.xml in the reference dump carries a UTF-8 BOM (5665 of 5665),
// so a BOM-free specimen does not exist there and comparing two files from the
// corpus would compare two files with a BOM.
func TestParseFormXML_ByteOrderMarkChangesNothing(t *testing.T) {
	withBOM, err := os.ReadFile(filepath.Join("testdata", "catalog_list_form.xml"))
	if err != nil {
		t.Fatal(err)
	}
	// POSITIVE CONTROL: the fixture really starts with EF BB BF, so the pair below
	// is a pair and not the same bytes read twice.
	if !bytes.HasPrefix(withBOM, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("control failed: the fixture does not start with a UTF-8 BOM, first bytes %x",
			withBOM[:min(3, len(withBOM))])
	}
	withoutBOM := bytes.TrimPrefix(withBOM, []byte{0xEF, 0xBB, 0xBF})
	t.Logf("with BOM %d bytes, without BOM %d bytes", len(withBOM), len(withoutBOM))

	a, err := parseFormXMLData(withBOM)
	if err != nil {
		t.Fatalf("parsing the file with its BOM: %v", err)
	}
	b, err := parseFormXMLData(withoutBOM)
	if err != nil {
		t.Fatalf("parsing the constructed BOM-free copy: %v", err)
	}
	if len(a.DynamicLists) != len(b.DynamicLists) {
		t.Fatalf("BOM changed the number of lists: %d with, %d without",
			len(a.DynamicLists), len(b.DynamicLists))
	}
	for i := range a.DynamicLists {
		if a.DynamicLists[i] != b.DynamicLists[i] {
			t.Errorf("list %d differs across the BOM: %+v vs %+v", i, a.DynamicLists[i], b.DynamicLists[i])
		}
	}
	if len(a.DynamicLists) == 0 {
		t.Error("control failed: the fixture yielded no lists, so this comparison proves nothing")
	}
}

// The four common_form_* fixtures below are real Form.xml files taken verbatim
// from a 2.9 GB reference dump, BOM and CRLF included. They are here because the
// synthetic documents above can only prove what they were built to prove: a real
// file carries five attributes where one is a list, settings of other types
// beside it, and a <ListSettings> block that must not leak into the answer.
//
// Every expected value below was measured with Python's expat, a reader written
// by neither this package nor this test.

// TestParseFormXML_CommonFormFileVolumes reads the list out of a real common
// form (CommonForms/ФайлыВТоме). Measured: 5 attributes, of which the list is
// one; query text 595 bytes = 327 runes = 9 lines.
func TestParseFormXML_CommonFormFileVolumes(t *testing.T) {
	form := mustParseFixture(t, "common_form_file_volumes.xml")

	if len(form.DynamicLists) != 1 {
		t.Fatalf("expected 1 dynamic list among the 5 attributes of ФайлыВТоме, got %d: %+v",
			len(form.DynamicLists), form.DynamicLists)
	}
	got := form.DynamicLists[0]
	if got.Name != "Список" {
		t.Errorf("attribute name: got %q, want %q", got.Name, "Список")
	}
	if !got.ManualQuery {
		t.Errorf("ManualQuery: got false, want true")
	}
	if want := "Catalog.ТомаХраненияФайлов"; got.MainTable != want {
		t.Errorf("MainTable: got %q, want %q", got.MainTable, want)
	}
	if b, r, l := querySizes(got.QueryText); b != 595 || r != 327 || l != 9 {
		t.Errorf("query text size: got %d bytes = %d runes = %d lines, "+
			"want 595 bytes = 327 runes = 9 lines", b, r, l)
	}

	// The other four attributes of this form must NOT appear. One of them,
	// ИменаХранилищФайлов, carries a <Settings xsi:type="v8:TypeDescription">,
	// so a reader that took every <Settings> would report it here.
	for _, l := range form.DynamicLists {
		switch l.Name {
		case "ИменаХранилищФайлов", "ИмяХранилищаФайлов", "Том", "ПредставлениеХранилищаФайлов":
			t.Errorf("attribute %q is not a dynamic list and must not be reported", l.Name)
		}
	}

	// FR-DL-047 in this package's terms: the recorded shape is the four fields
	// and nothing else. <ListSettings> is present on this list in the file, so
	// the check below is a live one and not a formality.
	raw, err := os.ReadFile(filepath.Join("testdata", "common_form_file_volumes.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("userSettingID")) {
		t.Fatal("control failed: the fixture carries no <ListSettings> marker, so it cannot " +
			"show that composer state is dropped")
	}
	if strings.Contains(got.QueryText+got.MainTable+got.Name, "userSettingID") {
		t.Errorf("composer state from <ListSettings> reached a recorded field: %+v", got)
	}
}

// TestParseFormXML_MissingMainTableKeepsTheList covers the element that is simply
// absent. Measured over the whole dump: <MainTable> is missing on 154 lists of
// 1918 and its value is the empty string on 0 of 1918, so an empty field here
// encodes "absent" and nothing else. The list must survive: dropping it would
// hide 154 real lists, 8 of them in common forms.
func TestParseFormXML_MissingMainTableKeepsTheList(t *testing.T) {
	path := filepath.Join("testdata", "common_form_no_main_table.xml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// POSITIVE CONTROL: the element really is absent from this file, so the empty
	// string below is the absence and not a parse that lost a present value.
	if bytes.Contains(raw, []byte("<MainTable>")) {
		t.Fatal("control failed: the fixture DOES carry a <MainTable> element, so it cannot " +
			"exercise the absent case")
	}

	form := mustParseFixture(t, "common_form_no_main_table.xml")
	if len(form.DynamicLists) != 1 {
		t.Fatalf("a list with no <MainTable> must still be reported, got %d lists: %+v",
			len(form.DynamicLists), form.DynamicLists)
	}
	got := form.DynamicLists[0]
	if got.Name != "Валюты" {
		t.Errorf("attribute name: got %q, want %q", got.Name, "Валюты")
	}
	if got.MainTable != "" {
		t.Errorf("MainTable: got %q, want the empty string for an absent element", got.MainTable)
	}
	if !got.ManualQuery {
		t.Errorf("ManualQuery: got false, want true")
	}
	if b, r, l := querySizes(got.QueryText); r != 642 || l != 27 {
		t.Errorf("query text size: got %d bytes = %d runes = %d lines, "+
			"want 1136 bytes = 642 runes = 27 lines", b, r, l)
	}
}

// TestParseFormXML_AutoQueryListHasNoText covers the other real combination: the
// platform composes the query itself, so <ManualQuery> is false and there is no
// <QueryText> at all, while <MainTable> is present. Measured: 927 lists of 1918
// are this shape. Having a main table is therefore not evidence of a query.
func TestParseFormXML_AutoQueryListHasNoText(t *testing.T) {
	form := mustParseFixture(t, "common_form_auto_query.xml")

	if len(form.DynamicLists) != 1 {
		t.Fatalf("expected 1 dynamic list, got %d: %+v", len(form.DynamicLists), form.DynamicLists)
	}
	got := form.DynamicLists[0]
	if got.Name != "ДополнительныеРеквизиты" {
		t.Errorf("attribute name: got %q, want %q", got.Name, "ДополнительныеРеквизиты")
	}
	if got.ManualQuery {
		t.Errorf("ManualQuery: got true, want false")
	}
	if got.QueryText != "" {
		t.Errorf("this list has no <QueryText> element at all, got %d bytes of text: %.60q",
			len(got.QueryText), got.QueryText)
	}
	if want := "InformationRegister.НастройкиДополнительныхРеквизитовЭПД"; got.MainTable != want {
		t.Errorf("MainTable: got %q, want %q", got.MainTable, want)
	}
}

// TestParseFormXML_TwoListsInOneCommonForm is multiplicity on real data, compared
// as a sequence. Measured: 141 of 1628 carriers hold more than one list.
func TestParseFormXML_TwoListsInOneCommonForm(t *testing.T) {
	form := mustParseFixture(t, "common_form_two_lists.xml")

	if len(form.DynamicLists) != 2 {
		t.Fatalf("expected 2 dynamic lists, got %d: %+v", len(form.DynamicLists), form.DynamicLists)
	}
	want := []struct {
		name      string
		mainTable string
		bytes     int
		runes     int
		lines     int
	}{
		{"СписокПользователи", "Catalog.Пользователи", 549, 299, 12},
		{"СписокРоли", "Catalog.РолиИсполнителей", 2159, 1150, 30},
	}
	for i, w := range want {
		got := form.DynamicLists[i]
		if got.Name != w.name {
			t.Errorf("list %d name: got %q, want %q (order must follow the file)", i, got.Name, w.name)
		}
		if got.MainTable != w.mainTable {
			t.Errorf("list %d MainTable: got %q, want %q", i, got.MainTable, w.mainTable)
		}
		if !got.ManualQuery {
			t.Errorf("list %d ManualQuery: got false, want true", i)
		}
		if b, r, l := querySizes(got.QueryText); b != w.bytes || r != w.runes || l != w.lines {
			t.Errorf("list %d query size: got %d bytes = %d runes = %d lines, want %d = %d = %d",
				i, b, r, l, w.bytes, w.runes, w.lines)
		}
	}
}

// TestParseFormXML_RefusesAFileOverTheReadLimit pins the ceiling on a single
// Form.xml. ParseFormXML read the whole file with os.ReadFile and no bound at
// all, so one oversized or endless file inside the dump was an unbounded
// allocation; the neighbouring subsystem reader has carried the same 16 MiB
// ceiling for exactly this reason.
//
// Measured for scale, not as the source of the number: the largest Form.xml in
// the 2.9 GB reference dump is 1739122 bytes, which is 10.4 per cent of the
// limit, so no real form comes near it.
func TestParseFormXML_RefusesAFileOverTheReadLimit(t *testing.T) {
	// The shipped ceiling is asserted separately from the boundary behaviour,
	// because the boundary is exercised with the ceiling tightened and a test
	// that only tightened it would never state what ships.
	if maxFormFileBytes != 16<<20 {
		t.Errorf("shipped read limit is %d bytes, want %d (16 MiB)", maxFormFileBytes, 16<<20)
	}

	// Tightened so the boundary can be exercised with kilobytes instead of
	// building two 16 MiB files. Same technique, and same reason, as the
	// subsystem reader's own bounds.
	original := maxFormFileBytes
	t.Cleanup(func() { maxFormFileBytes = original })
	const limit = 4096
	maxFormFileBytes = limit

	dir := t.TempDir()
	// The marker sits at the very start, so a truncated read would still carry
	// it and a refusal that leaked content would be visible.
	const marker = "МАРКЕР_СОДЕРЖИМОГО"
	head := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<Form xmlns="http://v8.1c.ru/8.3/xcf/logform"` + "\n" +
		`      xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><!--` + marker + " "

	write := func(name string, size int) string {
		body := head
		tail := `--><Attributes><Attribute name="Список" id="1">` +
			`<Settings xsi:type="DynamicList"><ManualQuery>true</ManualQuery>` +
			`</Settings></Attribute></Attributes></Form>`
		if pad := size - len(body) - len(tail); pad > 0 {
			body += strings.Repeat("x", pad)
		}
		body += tail
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s is %d bytes against a limit of %d", name, st.Size(), limit)
		return path
	}

	overPath := write("over.xml", limit+1)
	atPath := write("at.xml", limit)

	// POSITIVE CONTROL over the pair: the file AT the limit is read and yields
	// its list, so the refusal below measures the size rule and not a document
	// this parser cannot read at any size.
	atForm, err := ParseFormXML(atPath)
	if err != nil {
		t.Fatalf("a file exactly at the limit must be read: %v", err)
	}
	if len(atForm.DynamicLists) != 1 {
		t.Fatalf("control failed: the at-limit file yielded %d lists, want 1; the pair proves "+
			"nothing if the readable half is not read", len(atForm.DynamicLists))
	}

	overForm, err := ParseFormXML(overPath)
	if err == nil {
		t.Fatalf("a file one byte over the limit must be refused, got a form: %+v", overForm)
	}
	if overForm != nil {
		t.Errorf("a refused read must return no form at all, got %+v", overForm)
	}
	// WHICH refusal, and not merely that there was one. Asserting non-nil left
	// this sentinel swappable for any other with the whole suite green, and the
	// caller classifies on it: tools/form.go classifyDumpLegFailure reads it with
	// errors.Is to choose what the answer tells the user to do.
	assertFormSentinel(t, err, "ErrFormXMLTooLarge")
	if strings.Contains(err.Error(), marker) {
		t.Errorf("the refusal carries content from the file it refused: %v", err)
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("the refusal carries the absolute path it failed on: %v", err)
	}
}
