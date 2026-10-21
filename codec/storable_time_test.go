package codec_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anyweez/cairn/codec"
)

// A time.Time encodes itself, so the walk that checks values for text the
// ledger cannot keep has to check the time's own encoding too. json.Marshal
// refuses a year outside [0,9999] and a zone offset of an hour or more, and
// writes a zone offset with seconds in it only to the minute, storing an
// instant the caller never sent. Decide runs CheckStorable before the message
// is recorded, so all of these are refused there rather than at Encode.

type stamped struct{ At time.Time }

// survivesJSON reports whether encoding/json stores t as the same instant, the
// way an entry does: the reference the check is written against.
func survivesJSON(t time.Time) bool {
	data, err := json.Marshal(t)
	if err != nil {
		return false
	}
	var back time.Time
	if err := json.Unmarshal(data, &back); err != nil {
		return false
	}
	return back.Equal(t)
}

func TestATimeTheJSONCodecCannotCarryIsNotStorable(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
	}{
		{"year past 9999", time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{"year before 0", time.Date(-1, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{"offset with seconds", time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))},
		{"negative offset with seconds", time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("LMT", -(17*60+8)))},
		{"zone hour out of range", time.Date(2026, time.January, 1, 0, 0, 0, 0, time.FixedZone("X", 24*60*60))},
	}
	wrappers := []struct {
		name  string
		path  string
		value func(time.Time) any
	}{
		{"struct", "value.At", func(at time.Time) any { return stamped{At: at} }},
		{"pointer field", "value.At", func(at time.Time) any { return struct{ At *time.Time }{At: &at} }},
		{"map value", "value[at]", func(at time.Time) any { return map[string]any{"at": at} }},
		{"slice element", "value[0]", func(at time.Time) any { return []any{at} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if survivesJSON(tc.at) {
				t.Fatalf("encoding/json carries %v unchanged; nothing to refuse", tc.at)
			}
			for _, w := range wrappers {
				err := codec.CheckStorable(w.value(tc.at))
				var notStorable *codec.NotStorableError
				if !errors.As(err, &notStorable) {
					t.Fatalf("CheckStorable(%s of %v) = %v, want a NotStorableError", w.name, tc.at, err)
				}
				if notStorable.Path != w.path {
					t.Fatalf("%s: path %q, want %q", w.name, notStorable.Path, w.path)
				}
			}
		})
	}
}

func TestATimeTheJSONCodecCarriesIsStorable(t *testing.T) {
	cases := []time.Time{
		{},
		time.Date(2026, time.March, 4, 5, 6, 7, 123456789, time.UTC),
		time.Date(2026, time.March, 4, 5, 6, 7, 123456789, time.FixedZone("IST", 5*60*60+30*60)),
		time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("PST", -8*60*60)),
		time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC),
	}
	for _, at := range cases {
		if !survivesJSON(at) {
			t.Fatalf("encoding/json does not carry %v; the case is not the storable one", at)
		}
		if err := codec.CheckStorable(stamped{At: at}); err != nil {
			t.Fatalf("CheckStorable(%v) = %v, want storable", at, err)
		}
		if err := codec.CheckStorable(struct{ At *time.Time }{At: &at}); err != nil {
			t.Fatalf("CheckStorable(&%v) = %v, want storable", at, err)
		}
	}
}

// The refusal prints the instant sent and the one it would be stored as, with
// the offset in seconds so the two do not look identical.
func TestTheRefusalShowsTheOffsetSecondsThatWouldGoMissing(t *testing.T) {
	at := time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))
	err := codec.CheckStorable(stamped{At: at})
	var notStorable *codec.NotStorableError
	if !errors.As(err, &notStorable) {
		t.Fatalf("CheckStorable(%v) = %v, want a NotStorableError", at, err)
	}
	for _, want := range []string{"+00:19:32", "+00:19:00"} {
		if !strings.Contains(notStorable.Why, want) {
			t.Fatalf("the refusal %q does not show %s", notStorable.Why, want)
		}
	}
}

// Encode refuses the same values the walk does, so a decider that does not
// call CheckStorable itself still fails with a NotStorableError rather than
// json's own error after it has said yes.
func TestEncodeRefusesATimeItCannotCarry(t *testing.T) {
	times := codec.New[stamped]()
	codec.Register[stamped](times, "stamped", 1)

	at := time.Date(300000, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, err := times.Encode(stamped{At: at})
	var notStorable *codec.NotStorableError
	if !errors.As(err, &notStorable) {
		t.Fatalf("Encode(%v) = %v, want a NotStorableError", at, err)
	}

	// The same instant in a zone JSON writes exactly, so it round-trips through
	// the codec.
	moved := time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))
	env, err := times.Encode(stamped{At: moved.UTC()})
	if err != nil {
		t.Fatalf("Encode(%v) = %v, want it stored", moved.UTC(), err)
	}
	back, err := times.Decode(env)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !back.At.Equal(moved) {
		t.Fatalf("stored %v, want the instant sent %v", back.At, moved)
	}
}
