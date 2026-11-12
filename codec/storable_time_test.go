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

// A map key is rendered through MarshalText and read back with UnmarshalText,
// not through a time.Time's MarshalJSON, so a time key needs its own round
// trip. A named type over time.Time and one that embeds it both delegate that
// text to time.
type namedTimeKey time.Time

func (t namedTimeKey) MarshalText() ([]byte, error) { return time.Time(t).MarshalText() }

type embeddedTimeKey struct{ time.Time }

// survivesJSONMapKey reports whether an object keyed by at comes back as the
// same instant, the way an entry does: the reference the key check is written
// against.
func survivesJSONMapKey(at time.Time) bool {
	data, err := json.Marshal(map[time.Time]int{at: 1})
	if err != nil {
		return false
	}
	var back map[time.Time]int
	if err := json.Unmarshal(data, &back); err != nil {
		return false
	}
	for key := range back {
		return len(back) == 1 && key.Equal(at)
	}
	return false
}

// A time map key the JSON codec cannot carry is refused as surely as a time in
// a field: the key would be read back at a different instant.
func TestATimeMapKeyTheJSONCodecCannotCarryIsNotStorable(t *testing.T) {
	at := time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))
	if survivesJSONMapKey(at) {
		t.Fatalf("encoding/json carries %v unchanged; nothing to refuse", at)
	}
	for name, value := range map[string]any{
		"time.Time key":            map[time.Time]int{at: 1},
		"named time.Time key":      map[namedTimeKey]int{namedTimeKey(at): 1},
		"embedded time.Time key":   map[embeddedTimeKey]int{{Time: at}: 1},
		"pointer to time.Time key": map[*time.Time]int{&at: 1},
	} {
		t.Run(name, func(t *testing.T) {
			err := codec.CheckStorable(value)
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("CheckStorable(%v) = %v, want a NotStorableError", value, err)
			}
			if notStorable.Path != "value key" {
				t.Fatalf("path %q, want %q", notStorable.Path, "value key")
			}
		})
	}
}

// A map keyed by a time JSON carries unchanged is still storable, including a
// key type that delegates its text to time.
func TestATimeMapKeyTheJSONCodecCarriesIsStorable(t *testing.T) {
	at := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("IST", 5*60*60+30*60))
	if !survivesJSONMapKey(at) {
		t.Fatalf("encoding/json does not carry %v; the case is not the storable one", at)
	}
	for name, value := range map[string]any{
		"time.Time key":            map[time.Time]int{at: 1},
		"named time.Time key":      map[namedTimeKey]int{namedTimeKey(at): 1},
		"embedded time.Time key":   map[embeddedTimeKey]int{{Time: at}: 1},
		"pointer to time.Time key": map[*time.Time]int{&at: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := codec.CheckStorable(value); err != nil {
				t.Fatalf("CheckStorable(%v) = %v, want storable", value, err)
			}
		})
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

// A type over time.Time may encode its time deliberately another way — the
// date, the year, an object around it. That is an encoding of its own, not
// time's, so only its text is checked and the instant it drops is not a reason
// to refuse it.
type dateOnlyTime time.Time

func (t dateOnlyTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).Format("2006-01-02"))
}

type yearOnlyTime time.Time

func (t yearOnlyTime) MarshalText() ([]byte, error) {
	return []byte(time.Time(t).Format("2006")), nil
}

type dateOnlyHolder struct {
	At dateOnlyTime `json:"at"`
}

type yearOnlyHolder struct {
	At yearOnlyTime `json:"at"`
}

func TestATimeLikeTypeWithItsOwnEncodingIsCheckedAsText(t *testing.T) {
	at := time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))
	for name, value := range map[string]any{
		"JSON that writes the date only": dateOnlyHolder{At: dateOnlyTime(at)},
		"text that writes the year only": yearOnlyHolder{At: yearOnlyTime(at)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := codec.CheckStorable(value); err != nil {
				t.Fatalf("a type with an encoding of its own was refused: %v", err)
			}
		})
	}
}

// A wrapped time is held to the range check a plain time.Time gets: time's own
// marshaller refuses a year JSON cannot write, and a type defined over a
// time.Time or embedding one must reach that refusal rather than failing later
// in Encode, after Decide has said yes.
type namedRangeTime time.Time

func (t namedRangeTime) MarshalJSON() ([]byte, error) { return time.Time(t).MarshalJSON() }

type embeddedRangeTime struct{ time.Time }

type namedRangeHolder struct {
	At namedRangeTime `json:"at"`
}

type embeddedRangeHolder struct {
	At embeddedRangeTime `json:"at"`
}

// Exported so the embedding field is exported too: reflect cannot read an
// unexported embedded field, though json promotes its marshaller either way.
type ExportedNamedRange time.Time

func (t ExportedNamedRange) MarshalJSON() ([]byte, error) { return time.Time(t).MarshalJSON() }

type embeddedNamedTime struct{ ExportedNamedRange }

type embeddedNamedHolder struct {
	At embeddedNamedTime `json:"at"`
}

// A type over time.Time may write time's own RFC 3339 at a coarser precision.
// Its offset is exact but the fraction of a second is not, so an instant with
// nanoseconds comes back as a different one, and the refusal should name that
// rather than the zone offset it did not lose.
type truncatedTime time.Time

func (t truncatedTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).Format(time.RFC3339))
}

type truncatedTimeHolder struct {
	At truncatedTime `json:"at"`
}

func TestAWrappedTimeThatDropsTheFractionIsRefusedForThatReason(t *testing.T) {
	at := time.Date(2026, time.March, 4, 5, 6, 7, 123456789, time.UTC)
	err := codec.CheckStorable(truncatedTimeHolder{At: truncatedTime(at)})
	var notStorable *codec.NotStorableError
	if !errors.As(err, &notStorable) {
		t.Fatalf("CheckStorable(%v) = %v, want a NotStorableError", at, err)
	}
	if notStorable.Path != "value.At" {
		t.Fatalf("path %q, want %q", notStorable.Path, "value.At")
	}
	if !strings.Contains(notStorable.Why, "fraction of a second") {
		t.Fatalf("the refusal %q does not name the fraction of a second", notStorable.Why)
	}
	if strings.Contains(notStorable.Why, "zone offset") {
		t.Fatalf("the refusal %q blames the zone offset, which is exact", notStorable.Why)
	}

	whole := at.Truncate(time.Second)
	if err := codec.CheckStorable(truncatedTimeHolder{At: truncatedTime(whole)}); err != nil {
		t.Fatalf("CheckStorable(%v) = %v, want storable", whole, err)
	}
}

func TestATimeJSONCannotWriteIsRefusedInAWrappedType(t *testing.T) {
	at := time.Date(300000, time.January, 1, 0, 0, 0, 0, time.UTC)
	for name, value := range map[string]any{
		"a type defined over time.Time":   namedRangeHolder{At: namedRangeTime(at)},
		"a struct embedding time.Time":    embeddedRangeHolder{At: embeddedRangeTime{at}},
		"a struct embedding a named time": embeddedNamedHolder{At: embeddedNamedTime{ExportedNamedRange(at)}},
	} {
		t.Run(name, func(t *testing.T) {
			err := codec.CheckStorable(value)
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("CheckStorable(%v) = %v, want a NotStorableError", at, err)
			}
			if notStorable.Path != "value.At" {
				t.Fatalf("path %q, want %q", notStorable.Path, "value.At")
			}
		})
	}
}
