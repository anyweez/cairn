package spectest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anyweez/cairn/codec"
)

type stamped struct{ At time.Time }

// anyNumber holds a number whose dynamic type JSON does not keep: an int in an
// `any` comes back a float64.
type anyNumber struct{ N any }

// roundTripMessage is a codec message with the same shape, and roundTripSum is
// the sum type it is registered under.
type roundTripMessage struct{ N any }

type roundTripSum interface{ isRoundTripMessage() }

func (roundTripMessage) isRoundTripMessage() {}

func jsonRoundTrip[T any](t *testing.T, v T) T {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%v cannot be encoded: %v", v, err)
	}
	var back T
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("%s cannot be decoded: %v", data, err)
	}
	return back
}

// D6 and D7 build their failure detail from describeRoundTrip, so it has to
// render apart every pair codec.EqualRoundTripped tells apart. %+v does not,
// and the report then says the value came back as itself.
func TestDescribeRoundTripSeparatesWhatTheComparisonDoes(t *testing.T) {
	// JSON writes this offset only to the minute, so the round trip keeps the
	// wall clock and moves the instant by the seven seconds it drops.
	moved := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("LMT", -(5*60*60+53*60+7)))
	for _, tc := range []struct {
		name         string
		a, b         any
		wantA, wantB string
	}{
		{
			name: "a number's dynamic type in an any",
			a:    anyNumber{N: 5}, b: anyNumber{N: float64(5)},
			wantA: "{N:(int)5,}", wantB: "{N:(float64)5,}",
		},
		{
			name: "a number's dynamic type in a map[string]any",
			a:    map[string]any{"n": 5}, b: map[string]any{"n": float64(5)},
			wantA: "map[\"n\":(int)5,]", wantB: "map[\"n\":(float64)5,]",
		},
		{
			name: "a number's dynamic type in a []any",
			a:    []any{5}, b: []any{float64(5)},
			wantA: "[(int)5,]", wantB: "[(float64)5,]",
		},
		{
			name: "a time whose instant the round trip moves",
			a:    stamped{At: moved}, b: jsonRoundTrip(t, stamped{At: moved}),
			wantA: "{At:2026-03-04T10:59:14Z,}", wantB: "{At:2026-03-04T10:59:07Z,}",
		},
		{
			name: "a nil slice against an empty one",
			a:    []int(nil), b: []int{},
			wantA: "nil", wantB: "[]",
		},
		{
			name: "a nil map against an empty one",
			a:    map[string]int(nil), b: map[string]int{},
			wantA: "nil", wantB: "map[]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if codec.EqualRoundTripped(tc.a, tc.b) {
				t.Fatalf("the comparison calls %v and %v equal, so this is not the case under test", tc.a, tc.b)
			}
			if got := describeRoundTrip(tc.a); got != tc.wantA {
				t.Fatalf("describeRoundTrip(%v) = %q, want %q", tc.a, got, tc.wantA)
			}
			if got := describeRoundTrip(tc.b); got != tc.wantB {
				t.Fatalf("describeRoundTrip(%v) = %q, want %q", tc.b, got, tc.wantB)
			}
		})
	}
}

// D7's detail is not the only one built this way: D6 formats a message the
// same. A codec round trip that changes a field's dynamic type has to name
// both types, or D6 is as unreadable as D7 was.
func TestD6FailureNamesTheTypeThatChanged(t *testing.T) {
	messages := codec.New[roundTripSum]()
	codec.Register[roundTripMessage](messages, "spectest.round_trip_message", 1)

	detail, failed := roundTripMaybeRefused[roundTripSum](messages, roundTripMessage{N: 5}, false).Failed()
	if !failed {
		t.Fatal("a codec round trip that changes a field's dynamic type must fail D6")
	}
	for _, want := range []string{"(int)5", "(float64)5"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("the D6 detail does not name %s, so both sides read alike:\n%s", want, detail)
		}
	}
}
