package codec_test

import (
	"errors"
	"testing"
	"time"

	"github.com/anyweez/cairn/codec"
)

// An application that wants a timestamp with its own methods defines a type
// over time.Time (or embeds one) and lets time.Time's own JSON encoding write
// it. The encoding is exactly the one codec refuses for a plain time.Time: the
// RFC 3339 offset is whole minutes, so an instant whose zone offset has a
// seconds part (every tzdata zone before about 1900) is stored as a different
// instant.
type zzqaNamedTime time.Time

func (t zzqaNamedTime) MarshalJSON() ([]byte, error) { return time.Time(t).MarshalJSON() }

func (t *zzqaNamedTime) UnmarshalJSON(b []byte) error {
	var tt time.Time
	if err := tt.UnmarshalJSON(b); err != nil {
		return err
	}
	*t = zzqaNamedTime(tt)
	return nil
}

// zzqaEmbeddedTime is the other shape: time.Time's methods are promoted, so
// json reaches the same encoding.
type zzqaEmbeddedTime struct{ time.Time }

// zzqaTextTime keeps the same instant but is written through
// encoding.TextMarshaler, which json calls for a value that does not implement
// json.Marshaler itself.
type zzqaTextTime time.Time

func (t zzqaTextTime) MarshalText() ([]byte, error) { return time.Time(t).MarshalText() }

func (t *zzqaTextTime) UnmarshalText(b []byte) error {
	var tt time.Time
	if err := tt.UnmarshalText(b); err != nil {
		return err
	}
	*t = zzqaTextTime(tt)
	return nil
}

type zzqaNamedTimeHolder struct {
	At zzqaNamedTime `json:"at"`
}

func (zzqaNamedTimeHolder) area() int { return 0 }

type zzqaEmbeddedTimeHolder struct {
	At zzqaEmbeddedTime `json:"at"`
}

type zzqaTextTimeHolder struct {
	At zzqaTextTime `json:"at"`
}

func (zzqaTextTimeHolder) area() int { return 0 }

func (zzqaEmbeddedTimeHolder) area() int { return 0 }

type zzqaPlainTimeHolder struct {
	At time.Time `json:"at"`
}

func (zzqaPlainTimeHolder) area() int { return 0 }

// A value whose JSON encoding is time.Time's must be checked like a time.Time:
// Encode either refuses an instant JSON moves as not storable, or the decoded
// value is the instant the caller sent. A named or embedded time is stored as
// a different instant today, while the plain time.Time in the same position is
// refused, so a message carrying one is recorded as a time the caller never
// sent.
func TestANamedTimeValueIsNotStoredAsADifferentInstant(t *testing.T) {
	at := time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))
	if survivesJSON(at) {
		t.Fatalf("encoding/json carries %v unchanged; nothing to refuse", at)
	}

	c := codec.New[shape]()
	codec.Register[zzqaPlainTimeHolder](c, "zzqaPlainTimeHolder", 1)
	codec.Register[zzqaNamedTimeHolder](c, "zzqaNamedTimeHolder", 1)
	codec.Register[zzqaEmbeddedTimeHolder](c, "zzqaEmbeddedTimeHolder", 1)
	codec.Register[zzqaTextTimeHolder](c, "zzqaTextTimeHolder", 1)

	// The premise: the same instant in a plain time.Time is refused, because
	// its zone offset cannot survive JSON. The wrappers below encode it the
	// same way and must be refused too.
	if _, err := c.Encode(zzqaPlainTimeHolder{At: at}); err == nil {
		t.Fatal("a plain time.Time whose instant JSON moves was encoded, so the case is not the one under test")
	}

	for name, sent := range map[string]shape{
		"a type defined over time.Time": zzqaNamedTimeHolder{At: zzqaNamedTime(at)},
		"a struct embedding time.Time":  zzqaEmbeddedTimeHolder{At: zzqaEmbeddedTime{at}},
		"a text-marshalled named time":  zzqaTextTimeHolder{At: zzqaTextTime(at)},
	} {
		t.Run(name, func(t *testing.T) {
			env, err := c.Encode(sent)
			if err != nil {
				var notStorable *codec.NotStorableError
				if !errors.As(err, &notStorable) {
					t.Fatalf("Encode refused with %v, want a NotStorableError", err)
				}
				return
			}
			decoded, err := c.Decode(env)
			if err != nil {
				t.Fatalf("decoding: %v", err)
			}
			var back time.Time
			switch v := decoded.(type) {
			case zzqaNamedTimeHolder:
				back = time.Time(v.At)
			case zzqaEmbeddedTimeHolder:
				back = v.At.Time
			case zzqaTextTimeHolder:
				back = time.Time(v.At)
			default:
				t.Fatalf("decoded unexpected type %T", decoded)
			}
			if !back.Equal(at) {
				t.Fatalf("stored %s (%s), %s after the instant sent %s (%s)",
					back.Format(time.RFC3339Nano), back.Location(), back.Sub(at), at.Format(time.RFC3339Nano), at.Location())
			}
		})
	}
}
