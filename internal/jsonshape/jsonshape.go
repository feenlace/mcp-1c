// Package jsonshape describes a failed JSON decode in the vocabulary of JSON,
// so that nothing about this program's Go types reaches the model.
//
// WHY IT EXISTS. encoding/json reports a type mismatch through
// *json.UnmarshalTypeError, whose Error() prints the Go struct name, the Go
// field name and the Go type: "json: cannot unmarshal number into Go struct
// field searchCodeInput.query of type string". Both of this repository's decode
// sites put that text where a model reads it, so the model was told
// "tools.searchCodeInput", "map[string][]string" and "[]string" about failures
// it might be able to correct. It cannot correct anything by knowing the name of
// a Go type: it did not write the Go, it cannot see the Go, and the name changes
// under a rename that does not change the contract. What it can act on is which
// FIELD was wrong and what shape that field takes in JSON.
//
// It lives under internal/ and not beside either caller because there are two
// callers in two packages that cannot import each other: onec decodes the
// answer FROM 1C, tools decodes the arguments FROM the model. One shared
// vocabulary is the point, and the alternative, a copy in each, is the shape
// that produced the Content-Type defect one directory over: a reduction applied
// on one channel and not on the other.
//
// ONE TYPE IS ENOUGH, and that is measured rather than hoped. Three error types
// in encoding/json print a Go type through e.Type.String():
// *UnmarshalTypeError, which this package handles; *UnmarshalFieldError, which
// the standard library marks «Deprecated: No longer used; kept for
// compatibility»; and *InvalidUnmarshalError, which is raised only for a nil or
// non-pointer target, while all ten json.Unmarshal calls under tools/ and onec/
// pass an address. So every other decode error carries no Go type, and both
// callers keep such an error's own text on purpose: a syntax error's offset and
// a truncated stream's «unexpected EOF» ARE the diagnostic.
package jsonshape

import (
	"encoding/json"
	"errors"
	"reflect"
)

// Kind names a JSON type in the words JSON uses for it. The set is exactly the
// seven of RFC 8259 as a decoder can distinguish them, collapsed to the six a
// Go decode can be waiting for.
//
// Customer-facing RU: no тире.
const (
	kindString  = "строка"
	kindNumber  = "число"
	kindBool    = "логическое значение"
	kindArray   = "массив"
	kindObject  = "объект"
	kindUnknown = "значение другого вида"
)

// TypeMismatch reports whether err is a decode failure caused by a value of the
// wrong JSON type, and if so returns the JSON field path and the JSON type that
// was expected there.
//
// field is "" when the decoder could not attribute the mismatch to a named
// field, which happens when the whole document is of the wrong type: an array
// where an object was expected carries no field at all. A caller must be able to
// say something useful in that case too, so the emptiness is reported rather
// than papered over with a placeholder.
func TypeMismatch(err error) (field, wantJSONType, gotJSONType string, ok bool) {
	var ute *json.UnmarshalTypeError
	if !errors.As(err, &ute) {
		return "", "", "", false
	}
	return ute.Field, jsonKindOf(ute.Type), gotKind(ute.Value), true
}

// gotKind normalises the decoder's description of what actually arrived.
//
// json.UnmarshalTypeError.Value is already JSON vocabulary, but not always a
// bare word: a number arrives as "number 5" rather than "number", and the digits
// are the caller's own value echoed back. They are dropped, because this
// sentence is about the SHAPE, and because a value echoed into prose is the
// habit that put a far side header into a success answer.
func gotKind(value string) string {
	switch {
	case value == "":
		return kindUnknown
	case hasWordPrefix(value, "string"):
		return kindString
	case hasWordPrefix(value, "number"):
		return kindNumber
	case hasWordPrefix(value, "bool"):
		return kindBool
	case hasWordPrefix(value, "array"):
		return kindArray
	case hasWordPrefix(value, "object"):
		return kindObject
	default:
		return kindUnknown
	}
}

// hasWordPrefix reports whether s starts with word, at a word boundary.
func hasWordPrefix(s, word string) bool {
	if len(s) < len(word) || s[:len(word)] != word {
		return false
	}
	return len(s) == len(word) || s[len(word)] == ' '
}

// jsonKindOf maps the Go type the decoder was filling to the JSON type that
// would have fitted it.
//
// The mapping is deliberately many to one: several Go types share a JSON type,
// and that is exactly the direction the caller needs. A struct and a map are
// both an object; every numeric width is a number.
func jsonKindOf(t reflect.Type) string {
	if t == nil {
		return kindUnknown
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return kindString
	case reflect.Bool:
		return kindBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return kindNumber
	case reflect.Slice, reflect.Array:
		return kindArray
	case reflect.Map, reflect.Struct:
		return kindObject
	case reflect.Interface:
		// A decode into any never fails on type, so reaching this means the
		// target was something else that happens to be an interface.
		return kindUnknown
	default:
		return kindUnknown
	}
}
