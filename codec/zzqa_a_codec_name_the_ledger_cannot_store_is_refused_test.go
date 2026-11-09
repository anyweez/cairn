package codec_test

// A registration name is how decode finds a stored type: it is written into
// every entry's envelope, and it is the only thing that tells a replayed entry
// which Go type its data is. So a name the ledger cannot keep must be refused
// where the other registration mistakes are refused — Register already panics
// on a version below 1, a duplicate name and a duplicate type — or at the
// latest when Encode builds an envelope out of it.
//
// Neither happens today. A name that is not valid UTF-8 is accepted, Encode
// succeeds, and the entry is written with the name json.Marshal coerced to
// valid UTF-8 (the stored type reads "circle\ufffd"); decoding that entry then
// fails with `codec: unknown type "circle\ufffd"`. The ledger holds an entry
// nothing can read, and nothing reported it when it was written. A name with a
// NUL is accepted as well, and the envelope's JSON then cannot go into the
// entry's jsonb column at all.
//
// Each case accepts either refusal — a panic from Register or an error from
// Encode — and fails only when the codec hands out an envelope the ledger
// cannot store and read back as the value it encoded.

import (
	"encoding/json"
	"testing"

	"github.com/anyweez/cairn/codec"
)

type namedSum interface{ isNamedSum() }

type namedCircle struct{ R int }

func (namedCircle) isNamedSum() {}

// encodeUnder registers namedCircle under name and encodes one, reporting
// whether the codec refused the name. A refusal is Register panicking (a
// registration mistake) or Encode returning an error (a value that cannot be
// stored).
func encodeUnder(t *testing.T, name string) (env codec.Envelope, c *codec.Codec[namedSum], refused bool) {
	t.Helper()
	c = codec.New[namedSum]()
	func() {
		defer func() {
			if p := recover(); p != nil {
				refused = true
			}
		}()
		codec.Register[namedCircle](c, name, 1)
	}()
	if refused {
		return codec.Envelope{}, nil, true
	}
	env, err := c.Encode(namedCircle{R: 1})
	if err != nil {
		return codec.Envelope{}, nil, true
	}
	return env, c, false
}

func TestACodecNameTheLedgerCannotStoreIsRefused(t *testing.T) {
	t.Run("a name that is not valid UTF-8", func(t *testing.T) {
		env, c, refused := encodeUnder(t, "circle\xff")
		if refused {
			return
		}
		// The round trip an entry makes: the envelope is stored as JSON in a
		// jsonb column and read back, then decoded by the codec that wrote it.
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("the codec accepted the name and its envelope cannot be stored: %v", err)
		}
		var stored codec.Envelope
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatalf("the codec accepted the name and its envelope cannot be read back: %v", err)
		}
		back, err := c.Decode(stored)
		if err != nil {
			t.Fatalf("the codec accepted the name %q, and the entry written with it cannot be decoded again: %v\n  stored: %s",
				env.Type, err, raw)
		}
		if got, ok := back.(namedCircle); !ok || got != (namedCircle{R: 1}) {
			t.Fatalf("the entry came back as %+v, want the value that was encoded", back)
		}
	})

	t.Run("a name containing a NUL", func(t *testing.T) {
		env, _, refused := encodeUnder(t, "cir\x00cle")
		if refused {
			return
		}
		if err := codec.CheckStorable(env); err != nil {
			t.Fatalf("the codec accepted the name %q and returned an envelope the ledger cannot store, so every entry written with it fails at the database: %v",
				env.Type, err)
		}
	})
}
