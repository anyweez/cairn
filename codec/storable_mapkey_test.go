package codec_test

import (
	"errors"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// A map key whose Go kind is not a string is rendered by encoding/json through
// the key type's MarshalText, and the bytes it returns become the JSON object
// key. That text can carry what jsonb cannot hold (a NUL) or what json.Marshal
// coerces (invalid UTF-8), so Encode has to check it like any other string.
type renderedKey int

func (k renderedKey) MarshalText() ([]byte, error) {
	if k == 1 {
		return []byte("a\x00b"), nil // a NUL, which jsonb rejects with 22P05
	}
	return []byte("a\xffb"), nil // invalid UTF-8, which json.Marshal rewrites
}

type keyHolder struct {
	M map[renderedKey]string `json:"m"`
}

func (keyHolder) area() int { return 0 }

// A string-kind key whose MarshalText would return text the ledger cannot keep.
// json renders this key from the string and never calls MarshalText, so the
// walk must check the string too, in both directions.
type stringKey string

func (stringKey) MarshalText() ([]byte, error) { return []byte("a\x00b"), nil }

type stringKeyHolder struct {
	M map[stringKey]string `json:"m"`
}

func (stringKeyHolder) area() int { return 0 }

// A key whose MarshalText is storable must still be accepted.
type fineKey int

func (fineKey) MarshalText() ([]byte, error) { return []byte("fine"), nil }

type fineKeyHolder struct {
	M map[fineKey]string `json:"m"`
}

func (fineKeyHolder) area() int { return 0 }

// Encode refuses a map key rendered through MarshalText as text the ledger
// cannot store, rather than storing it and letting Postgres reject it or
// json.Marshal replace bytes the caller sent.
func TestEncodeRefusesAKeyThatIsNotStorable(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[keyHolder](c, "keyHolder", 1)

	for name, key := range map[string]renderedKey{
		"a NUL":         1,
		"invalid UTF-8": 2,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Encode(keyHolder{M: map[renderedKey]string{key: "v"}})
			if err == nil {
				t.Fatal("a map key the ledger cannot store was encoded")
			}
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("got %v, want a NotStorableError", err)
			}
		})
	}
}

// A key rendered through MarshalText is checked against what the ledger can
// keep: storable text passes, and a string-kind key is judged by its string
// because that is what json writes.
func TestEncodeChecksAStorableKeyAsJSONRendersIt(t *testing.T) {
	for name, check := range map[string]func(t *testing.T){
		"a storable MarshalText key is accepted": func(t *testing.T) {
			c := codec.New[shape]()
			codec.Register[fineKeyHolder](c, "fineKeyHolder", 1)
			if _, err := c.Encode(fineKeyHolder{M: map[fineKey]string{1: "v"}}); err != nil {
				t.Fatalf("a storable key was refused: %v", err)
			}
		},
		"a string-kind key is checked as its string, not its MarshalText": func(t *testing.T) {
			c := codec.New[shape]()
			codec.Register[stringKeyHolder](c, "stringKeyHolder", 1)
			if _, err := c.Encode(stringKeyHolder{M: map[stringKey]string{"fine": "v"}}); err != nil {
				t.Fatalf("a storable string key was refused: %v", err)
			}
			if _, err := c.Encode(stringKeyHolder{M: map[stringKey]string{"bad\xff": "v"}}); err == nil {
				t.Fatal("an unstorable string key was encoded")
			}
		},
	} {
		t.Run(name, check)
	}
}
