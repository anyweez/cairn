package codec_test

// codec.CheckStorable and codec.EqualRoundTripped are documented as the same
// rule, one for a value and one for a pair: "CheckStorable is the ledger's rule
// for what may be written; this is that rule applied to a pair." A type defined
// over time.Time that delegates its JSON to time.Time is a value CheckStorable
// holds to the instant-survival rule (codec/storable_time_test.go pins that),
// so a round trip it accepts must also compare as unchanged.
//
// EqualRoundTripped recognises a time by exact type, so a named time falls
// through to the generic struct branch and compares time.Time's unexported
// wall/ext/loc fields. A decoded time has a new *Location, so the pair compares
// unequal and D6/D7 in spectest and Verify in the ledger report a change the
// codec did not make.

import (
	"reflect"
	"testing"
	"time"

	"github.com/anyweez/cairn/codec"
)

type zzqaRTTime time.Time

func (t zzqaRTTime) MarshalJSON() ([]byte, error) { return time.Time(t).MarshalJSON() }

func (t *zzqaRTTime) UnmarshalJSON(b []byte) error {
	var tt time.Time
	if err := tt.UnmarshalJSON(b); err != nil {
		return err
	}
	*t = zzqaRTTime(tt)
	return nil
}

// A time used as a map key is written through encoding.TextMarshaler, so the
// key shape needs the text methods too.
func (t zzqaRTTime) MarshalText() ([]byte, error) { return time.Time(t).MarshalText() }

func (t *zzqaRTTime) UnmarshalText(b []byte) error {
	var tt time.Time
	if err := tt.UnmarshalText(b); err != nil {
		return err
	}
	*t = zzqaRTTime(tt)
	return nil
}

type zzqaRTField struct {
	At zzqaRTTime `json:"at"`
}

type zzqaRTKey struct {
	M map[zzqaRTTime]int `json:"m"`
}

type zzqaRTSum interface{ zzqaRTValue() }

func (zzqaRTField) zzqaRTValue() {}
func (zzqaRTKey) zzqaRTValue()   {}

func zzqaRegisterRT(t *testing.T, v any) *codec.Codec[zzqaRTSum] {
	t.Helper()
	c := codec.New[zzqaRTSum]()
	codec.Register[zzqaRTField](c, "zzqaRTField", 1)
	codec.Register[zzqaRTKey](c, "zzqaRTKey", 1)
	return c
}

func zzqaRTRoundTrip(t *testing.T, c *codec.Codec[zzqaRTSum], v zzqaRTSum) zzqaRTSum {
	t.Helper()
	env, err := c.Encode(v)
	if err != nil {
		t.Fatalf("encoding %v: %v", v, err)
	}
	back, err := c.Decode(env)
	if err != nil {
		t.Fatalf("decoding %v: %v", v, err)
	}
	return back
}

// A named time the codec stores as the same instant is not a change. The test
// asserts that premise before asserting the comparison: CheckStorable accepts
// the value and the decoded value carries the instant that was sent.
func TestANamedTimeSurvivesItsRoundTrip(t *testing.T) {
	at := zzqaRTTime(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("IST", 5*60*60+30*60)))
	c := zzqaRegisterRT(t, at)

	t.Run("in a field", func(t *testing.T) {
		sent := zzqaRTField{At: at}
		if err := codec.CheckStorable(sent); err != nil {
			t.Fatalf("CheckStorable refuses a time the codec keeps: %v", err)
		}
		back := zzqaRTRoundTrip(t, c, sent).(zzqaRTField)
		if !time.Time(back.At).Equal(time.Time(at)) {
			t.Fatalf("the codec stored %v, want the instant sent %v", time.Time(back.At), time.Time(at))
		}
		if !codec.EqualRoundTripped(sent, back) {
			t.Fatalf("the codec kept the instant %v, but the round trip reports it changed: %v came back as %v",
				time.Time(at), sent, back)
		}
	})

	t.Run("as a map key", func(t *testing.T) {
		sent := zzqaRTKey{M: map[zzqaRTTime]int{at: 1}}
		if err := codec.CheckStorable(sent); err != nil {
			t.Fatalf("CheckStorable refuses a time key the codec keeps: %v", err)
		}
		back := zzqaRTRoundTrip(t, c, sent).(zzqaRTKey)
		if len(back.M) != 1 {
			t.Fatalf("the codec stored %d keys, want 1", len(back.M))
		}
		for key := range back.M {
			if !time.Time(key).Equal(time.Time(at)) {
				t.Fatalf("the codec stored the key as %v, want the instant sent %v", time.Time(key), time.Time(at))
			}
		}
		if !codec.EqualRoundTripped(sent, back) {
			t.Fatalf("the codec kept the key's instant %v, but the round trip reports it changed", time.Time(at))
		}
	})

	// The comparison must still tell a genuinely different instant apart.
	t.Run("a different instant is a change", func(t *testing.T) {
		a := zzqaRTField{At: at}
		b := zzqaRTField{At: zzqaRTTime(time.Time(at).Add(time.Nanosecond))}
		if reflect.DeepEqual(a, b) || codec.EqualRoundTripped(a, b) {
			t.Fatal("two different instants compare as unchanged")
		}
	})
}
