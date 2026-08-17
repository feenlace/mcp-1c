package dump

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// TWO CLAIMS IN formparser.go WERE COUNTS, AND BOTH WENT STALE THE SAME WAY.
//
// One said «THE FOUR SENTINELS ABOVE AND THIS ONE» when three stood above it and
// six lived in the file. The other said that a file with no <Form> leaves
// Title, Elements, Commands and Handlers empty and concluded «all four», which
// stopped being the whole list the moment DynamicLists was added beside them,
// under the same condition, in the same place.
//
// Neither had a test, and neither could have had one while it was a numeral: a
// number in prose is checked by a reader who happens to recount. Both claims are
// now DERIVED here, from the source and from the type, so the next field or
// sentinel fails a test instead of quietly falsifying a sentence.

// TestFormSentinelsAreEnumeratedWhereTheyAreClaimed reads the sentinel
// declarations out of formparser.go and requires the paragraph that claims to
// list them to name every one.
//
// It works from the AST rather than from a grep, so a declaration cannot hide
// behind formatting, and it takes the doc comment of the declaration the claim
// is attached to rather than searching the file for a phrase.
func TestFormSentinelsAreEnumeratedWhereTheyAreClaimed(t *testing.T) {
	const file = "formparser.go"
	const claimant = "ErrFormUnknownObjectType"

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}

	var names []string
	var claim string
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, ident := range vs.Names {
				if !strings.HasPrefix(ident.Name, "ErrForm") {
					continue
				}
				names = append(names, ident.Name)
				if ident.Name == claimant && gen.Doc != nil {
					claim = gen.Doc.Text()
				}
			}
		}
	}

	// POSITIVE CONTROLS. Without them a scan that found nothing, or a doc comment
	// that was never located, would report a clean pass.
	if len(names) < 2 {
		t.Fatalf("control failed: the scan found %d ErrForm sentinels in %s, so it is not "+
			"reading the declarations at all: %v", len(names), file, names)
	}
	if strings.TrimSpace(claim) == "" {
		t.Fatalf("control failed: %s has no doc comment, so there is no enumeration to check "+
			"and this test measures nothing", claimant)
	}
	t.Logf("%d exported ErrForm sentinels in %s: %v", len(names), file, names)

	for _, name := range names {
		if !strings.Contains(claim, name) {
			t.Errorf("%s is an exported sentinel of %s and the paragraph that claims to list "+
				"them does not name it. Enumerate it there, or the next reader takes the list "+
				"for the whole set:\n\n%s", name, file, claim)
		}
	}

	// POSITIVE CONTROL over the detector: the same scan run against the same
	// paragraph with one name removed has to fire, or the loop above is measuring
	// a comparison that cannot fail.
	stripped := strings.ReplaceAll(claim, names[0], "")
	fired := false
	for _, name := range names {
		if !strings.Contains(stripped, name) {
			fired = true
		}
	}
	if !fired {
		t.Fatalf("control failed: removing %q from the paragraph did not make the check fire, "+
			"so the assertions above prove nothing", names[0])
	}
}

// TestParseFormXML_NoFormRootLeavesEveryRecordedFieldEmpty derives the claim
// NoFormRoot makes about the rest of the struct.
//
// It walks FormInfo BY REFLECTION rather than naming the fields, which is the
// whole point: the enumeration in the comment went stale because a field was
// added beside the ones it named, and a test that named them too would have gone
// stale in exactly the same commit. Every field except the two flags must be its
// zero value, including one nobody has added yet.
func TestParseFormXML_NoFormRootLeavesEveryRecordedFieldEmpty(t *testing.T) {
	// A well formed document whose root is not <Form>: read to a clean end, and
	// no <Form> ever entered.
	const rootless = `<?xml version="1.0" encoding="UTF-8"?>
<НеФорма xmlns="http://v8.1c.ru/8.3/xcf/logform">
  <Title><v8:item xmlns:v8="http://v8.1c.ru/8.1/data/core"><v8:content>Не форма</v8:content></v8:item></Title>
  <ChildItems><InputField name="Поле" id="1"><DataPath>Объект.Поле</DataPath></InputField></ChildItems>
  <Commands><Command name="К" id="1"><Action>Д</Action></Command></Commands>
  <Events><Event name="OnOpen">ПриОткрытии</Event></Events>
  <Attributes><Attribute name="Список" id="1">
    <Settings xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="DynamicList">
      <ManualQuery>true</ManualQuery><MainTable>Catalog.Валюты</MainTable>
    </Settings>
  </Attribute></Attributes>
</НеФорма>`

	form, err := parseFormXMLData([]byte(rootless))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !form.NoFormRoot {
		t.Fatalf("control failed: this document did not set NoFormRoot, so the emptiness below "+
			"is not the property this test is about: %+v", form)
	}
	if form.ParseIncomplete {
		t.Fatalf("control failed: the document was abandoned on a syntax error, so it never "+
			"reached the branch this test is about: %+v", form)
	}

	// The two flags are the report itself and are excluded by NAME, which is the
	// only exclusion in this test; everything else is required to be zero
	// whatever it is called.
	flags := map[string]bool{"ParseIncomplete": true, "NoFormRoot": true}
	v := reflect.ValueOf(*form)
	typ := v.Type()
	checked := 0
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if flags[name] {
			continue
		}
		checked++
		if !v.Field(i).IsZero() {
			t.Errorf("NoFormRoot is set, so nothing was read inside a <Form>, yet %s is %#v. "+
				"Either the field is filled outside <Form> or the flag is wrong",
				name, v.Field(i).Interface())
		}
	}
	if checked == 0 {
		t.Fatal("control failed: no field was checked, so the reflection walk proves nothing")
	}
	t.Logf("checked %d recorded fields of FormInfo besides the two flags", checked)

	// POSITIVE CONTROL over the walk: the SAME markup under a real <Form> root
	// fills fields, so the zeroes above are the rule and not a document this
	// reader cannot read at all.
	//
	// The control is stated as a SET and not as a count, for the reason the whole
	// file is about. It is also not «all of them»: FormInfo.Name is filled by no
	// branch of this parser at any time (the form's identity comes from the
	// directory it was found in, see tools/form.go convertDumpForm), so a control
	// demanding every field be filled would be demanding something the reader
	// cannot do and would fail on a correct parse.
	filled, err := parseFormXMLData([]byte(strings.ReplaceAll(rootless, "НеФорма", "Form")))
	if err != nil {
		t.Fatalf("parsing the control document: %v", err)
	}
	if filled.NoFormRoot {
		t.Fatal("control failed: the control document also reported no form root")
	}
	var fillable, neverFilled []string
	fv := reflect.ValueOf(*filled)
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if flags[name] {
			continue
		}
		if fv.Field(i).IsZero() {
			neverFilled = append(neverFilled, name)
			continue
		}
		fillable = append(fillable, name)
	}
	t.Logf("under a real <Form> root this markup fills %v; it fills nothing into %v",
		fillable, neverFilled)
	if len(fillable) == 0 {
		t.Fatal("control failed: the same markup under a real <Form> root filled no field at " +
			"all, so the emptiness above measures a document this reader cannot read")
	}
	// Every field the control document DOES fill must be one of the fields that
	// came back empty above. That is the pairing, and it is what a count cannot
	// express: it fails if the rootless parse ever fills one of them.
	rootlessValue := reflect.ValueOf(*form)
	for _, name := range fillable {
		f := rootlessValue.FieldByName(name)
		if !f.IsZero() {
			t.Errorf("%s is filled by this markup under <Form> AND is filled without one: %#v",
				name, f.Interface())
		}
	}
}
