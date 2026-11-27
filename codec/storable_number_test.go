package codec_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// jsonb parses every JSON number into Postgres numeric, which holds at most
// 131072 digits before the decimal point and 16383 after it. JSON has no such
// limit and json.Marshal enforces none, so a number the codec accepts can
// still make the entry insert fail with a raw SQLSTATE. The boundaries in
// these tests were checked against Postgres 17 with SELECT '<literal>'::jsonb.

// A json.Number is written as a bare number, not as the string its Go kind
// suggests, so the storable walk has to check its literal rather than its text.
type numberField struct {
	N json.Number `json:"n"`
}

func (numberField) area() int { return 0 }

// The same number in a slice gets an indexed path in the refusal.
type numberSlice struct {
	Ns []json.Number `json:"ns"`
}

func (numberSlice) area() int { return 0 }

// A json.Number inside an any, the shape Meta.Extra and an opaque payload
// field take.
type numberAny struct {
	Extra map[string]any `json:"extra"`
}

func (numberAny) area() int { return 0 }

// A json.Number used as a map key is quoted by json.Marshal, so it is a string
// on the wire and jsonb stores it.
type numberKey struct {
	M map[json.Number]int `json:"m"`
}

func (numberKey) area() int { return 0 }

// json.RawMessage decides its own bytes, so its numbers reach jsonb through
// the check on a MarshalJSON's output rather than the value walk.
type rawNumber struct {
	Payload json.RawMessage `json:"payload"`
}

func (rawNumber) area() int { return 0 }

// A json.Number whose tag carries the `string` option is written inside a JSON
// string, which jsonb stores as text, so its numeric range does not matter.
type quotedNumber struct {
	N json.Number `json:"n,string"`
}

func (quotedNumber) area() int { return 0 }

// The option applies to the pointer too, which json follows to the number.
type quotedNumberPointer struct {
	N *json.Number `json:"n,string"`
}

func (quotedNumberPointer) area() int { return 0 }

// The option is ignored for a field that is not a scalar, so these numbers are
// written bare and jsonb has to parse them.
type quotedNumberAny struct {
	V any `json:"v,string"`
}

func (quotedNumberAny) area() int { return 0 }

type quotedNumberSlice struct {
	Ns []json.Number `json:"ns,string"`
}

func (quotedNumberSlice) area() int { return 0 }

// Encode refuses a number jsonb cannot hold, naming the field, rather than
// letting the insert fail with SQLSTATE 22003.
func TestEncodeRefusesANumberJSONBCannotStore(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[numberField](c, "numberField", 1)
	codec.Register[numberSlice](c, "numberSlice", 1)
	codec.Register[numberAny](c, "numberAny", 1)
	codec.Register[quotedNumberAny](c, "quotedNumberAny", 1)
	codec.Register[quotedNumberSlice](c, "quotedNumberSlice", 1)
	codec.Register[rawNumber](c, "rawNumber", 1)

	for name, tc := range map[string]struct {
		v        shape
		wantPath string
	}{
		"too many digits before the decimal point": {
			v: numberField{N: "1e200000"}, wantPath: "value.N",
		},
		"a negative number equally out of range": {
			v: numberField{N: "-1e200000"}, wantPath: "value.N",
		},
		"one digit past the integer limit": {
			// 123 followed by 131070 zeros is 131073 digits.
			v: numberField{N: "123e131070"}, wantPath: "value.N",
		},
		"too many digits after the decimal point": {
			v: numberField{N: "1e-16384"}, wantPath: "value.N",
		},
		"an array element names its index": {
			v: numberSlice{Ns: []json.Number{"1", "1e200000"}}, wantPath: "value.Ns[1]",
		},
		"a value held in an any": {
			v:        numberAny{Extra: map[string]any{"n": json.Number("1e200000")}},
			wantPath: "value.Extra[n]",
		},
		"a number the string option does not quote, in an any": {
			v: quotedNumberAny{V: json.Number("1e200000")}, wantPath: "value.V",
		},
		"a number the string option does not quote, in a slice": {
			v: quotedNumberSlice{Ns: []json.Number{"1e200000"}}, wantPath: "value.Ns[0]",
		},
		"an opaque payload's object": {
			v: rawNumber{Payload: json.RawMessage(`{"n":1e200000}`)}, wantPath: "value.Payload",
		},
		"an opaque payload's array": {
			v: rawNumber{Payload: json.RawMessage(`[1,1e-20000]`)}, wantPath: "value.Payload",
		},
		"an opaque payload nested under another object": {
			v: rawNumber{Payload: json.RawMessage(`{"inner":{"n":1e200000}}`)}, wantPath: "value.Payload",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Encode(tc.v)
			if err == nil {
				t.Fatalf("Encode accepted %#v, which jsonb cannot store, so the insert fails with a raw SQLSTATE", tc.v)
			}
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("got %v, want a NotStorableError naming the number", err)
			}
			if notStorable.Path != tc.wantPath {
				t.Fatalf("the refusal names %q, want %q", notStorable.Path, tc.wantPath)
			}
		})
	}
}

// Numbers jsonb can hold keep being accepted, including the exact boundaries
// and the ordinary values of every kind.
func TestEncodeAcceptsNumbersJSONBCanStore(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[numberField](c, "numberField", 1)
	codec.Register[numberAny](c, "numberAny", 1)
	codec.Register[numberKey](c, "numberKey", 1)
	codec.Register[quotedNumber](c, "quotedNumber", 1)
	codec.Register[quotedNumberPointer](c, "quotedNumberPointer", 1)
	codec.Register[rawNumber](c, "rawNumber", 1)
	quoted := json.Number("1e200000")

	for name, v := range map[string]shape{
		"exactly 131072 digits before the decimal point": numberField{N: "1e131071"},
		"the same with a negative sign":                  numberField{N: "-1e131071"},
		"a fraction inside the integer limit":            numberField{N: "1.5e131071"},
		"a leading fraction inside the integer limit":    numberField{N: "0.1e131072"},
		"exactly 16383 digits after the decimal point":   numberField{N: "1e-16383"},
		"a fraction kept within the scale":               numberField{N: "1.0e-16382"},
		"a large but ordinary number":                    numberField{N: "1e400"},
		"an ordinary fraction":                           numberField{N: "3.14"},
		"the zero value, which json writes as 0":         numberField{N: ""},
		"a zero moved left by a huge exponent":           numberField{N: "0e200000"},
		"a number held in an any":                        numberAny{Extra: map[string]any{"n": json.Number("1e400")}},
		"a json.Number map key is quoted by json": numberKey{
			M: map[json.Number]int{"1e200000": 1},
		},
		"a number the string option quotes": quotedNumber{N: "1e200000"},
		"a number the string option quotes behind a pointer": quotedNumberPointer{
			N: &quoted,
		},
		"an opaque payload's number in range": rawNumber{
			Payload: json.RawMessage(`{"n":1e400}`),
		},
		"an opaque payload's number as an object key": rawNumber{
			Payload: json.RawMessage(`{"1e200000":1}`),
		},
		"an opaque payload's number in a string": rawNumber{
			Payload: json.RawMessage(`{"s":"1e200000"}`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.Encode(v); err != nil {
				t.Fatalf("a storable value was refused: %v", err)
			}
		})
	}
}

// A json.Number holding something that is not a number is json.Marshal's to
// refuse, not the storable walk's: nothing is stored either way, and the
// encoder's own message says what was wrong with the literal.
func TestEncodeLeavesAnInvalidNumberLiteralToJSONMarshal(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[numberField](c, "numberField", 1)

	if err := codec.CheckStorable(numberField{N: "not a number"}); err != nil {
		t.Fatalf("CheckStorable refused an invalid literal, which json.Marshal reports itself: %v", err)
	}
	_, err := c.Encode(numberField{N: "not a number"})
	if err == nil {
		t.Fatal("Encode accepted a json.Number that is not a number")
	}
	var notStorable *codec.NotStorableError
	if errors.As(err, &notStorable) {
		t.Fatalf("got %v, want json.Marshal's own error about the literal", err)
	}
}

// The `string` option is encoding/json's to apply, and these fixtures are what
// its output looks like. The walk mirrors it with a quoted flag, so this pins
// the reference that flag was written against.
func TestStringOptionQuotesANumberTheWayJSONMarshalDoes(t *testing.T) {
	quoted := json.Number("1e400")

	for name, tc := range map[string]struct {
		v      any
		quoted bool
	}{
		"a number field":                 {numberField{N: "1e400"}, false},
		"a number field with the option": {quotedNumber{N: "1e400"}, true},
		"a pointer with the option":      {quotedNumberPointer{N: &quoted}, true},
		"an any field with the option":   {quotedNumberAny{V: json.Number("1e400")}, false},
		"a slice field with the option": {
			quotedNumberSlice{Ns: []json.Number{"1e400"}}, false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatal(err)
			}
			if got := bytes.Contains(data, []byte(`"1e400"`)); got != tc.quoted {
				t.Fatalf("json.Marshal writes %s, so quoted=%v, want %v; the walk's quoted flag no longer matches encoding/json", data, got, tc.quoted)
			}
		})
	}
}
