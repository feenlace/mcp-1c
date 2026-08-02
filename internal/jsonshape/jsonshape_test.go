package jsonshape

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestTypeMismatch drives the real decoder rather than constructing
// json.UnmarshalTypeError by hand.
//
// Hand-built fixtures are how a mapping passes against a shape encoding/json
// never produces: the Value field is "number 5" and not "number", and a fixture
// written from the doc comment would have missed that. Every case here is the
// error the decoder actually returned.
func TestTypeMismatch(t *testing.T) {
	type args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}

	cases := []struct {
		name              string
		decode            func() error
		wantOK            bool
		wantField         string
		wantWant, wantGot string
	}{
		{"number into string field", func() error {
			var a args
			return json.Unmarshal([]byte(`{"query":123}`), &a)
		}, true, "query", kindString, kindNumber},
		{"string into int field", func() error {
			var a args
			return json.Unmarshal([]byte(`{"limit":"пятьдесят"}`), &a)
		}, true, "limit", kindNumber, kindString},
		{"array into struct field", func() error {
			var a args
			return json.Unmarshal([]byte(`{"query":[1]}`), &a)
		}, true, "query", kindString, kindArray},
		{"object into struct field", func() error {
			var a args
			return json.Unmarshal([]byte(`{"query":{}}`), &a)
		}, true, "query", kindString, kindObject},
		{"bool into struct field", func() error {
			var a args
			return json.Unmarshal([]byte(`{"query":true}`), &a)
		}, true, "query", kindString, kindBool},
		{"array at the root of a map", func() error {
			var m map[string][]string
			return json.Unmarshal([]byte(`["a"]`), &m)
		}, true, "", kindObject, kindArray},
		{
			// MEASURED, not assumed: the decoder leaves Field EMPTY here. It
			// populates Field from struct field names, and a map key is not one,
			// so the mismatch under a map value is reported with no path at all.
			// The first version of this case expected "k" and went red, which is
			// why the field-less branch in both callers is not decoration.
			"object where a slice was expected", func() error {
				var m map[string][]string
				return json.Unmarshal([]byte(`{"k":{"a":1}}`), &m)
			}, true, "", kindArray, kindObject,
		},
		{"syntax error is not a type mismatch", func() error {
			var a args
			return json.Unmarshal([]byte(`not json`), &a)
		}, false, "", "", ""},
		{"nil error is not a type mismatch", func() error { return nil }, false, "", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.decode()
			if c.wantOK && err == nil {
				t.Fatal("the fixture decoded cleanly, so this case measures nothing")
			}
			field, want, got, ok := TypeMismatch(err)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (err = %v)", ok, c.wantOK, err)
			}
			if !ok {
				return
			}
			if field != c.wantField || want != c.wantWant || got != c.wantGot {
				t.Errorf("got (field=%q want=%q got=%q), expected (field=%q want=%q got=%q); err = %v",
					field, want, got, c.wantField, c.wantWant, c.wantGot, err)
			}
		})
	}
}

// TestNoGoTypeNameSurvives is the property the package exists for.
func TestNoGoTypeNameSurvives(t *testing.T) {
	type inner struct {
		Query string `json:"query"`
	}
	var v map[string][]inner
	err := json.Unmarshal([]byte(`{"k":[{"query":1}]}`), &v)
	if err == nil {
		t.Fatal("the fixture decoded cleanly")
	}

	// The control first: the decoder's own text DOES name Go, so a clean result
	// below is the package's doing and not the fixture's.
	raw := err.Error()
	if !strings.Contains(raw, "Go struct field") {
		t.Fatalf("the decoder did not produce the shape under repair, so nothing is proven: %q", raw)
	}

	field, want, got, ok := TypeMismatch(err)
	if !ok {
		t.Fatal("a genuine type mismatch was not recognised")
	}
	out := fmt.Sprintf("%s %s %s", field, want, got)
	for _, banned := range []string{"Go ", "struct", "inner", "map[", "[]"} {
		if strings.Contains(out, banned) {
			t.Errorf("the description carries %q: %q", banned, out)
		}
	}
}

// TestUnwrappedMismatchIsFound keeps errors.As load-bearing: both decode sites
// wrap before anyone reads the error.
func TestUnwrappedMismatchIsFound(t *testing.T) {
	var s string
	inner := json.Unmarshal([]byte(`5`), &s)
	if inner == nil {
		t.Fatal("the fixture decoded cleanly")
	}
	wrapped := fmt.Errorf("decoding: %w", inner)
	if _, _, _, ok := TypeMismatch(wrapped); !ok {
		t.Error("a wrapped mismatch was not recognised; both call sites wrap")
	}
	if _, _, _, ok := TypeMismatch(errors.New("decoding: something")); ok {
		t.Error("a plain error was reported as a type mismatch")
	}
}
