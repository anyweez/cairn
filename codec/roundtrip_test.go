package codec_test

// D6 and D7 ask whether a value survived its round trip. JSON keeps a
// time.Time's instant but not its monotonic clock reading or the *Location it
// came from, so the comparison has to be the ledger's — the same instant —
// rather than Go's value identity, and no looser than that, or a genuine loss
// goes unreported. These are the comparison's own cases; the checker's rules
// are held to the same shapes that need a codec or a snapshot.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/anyweez/cairn/codec"
)

type rtStamped struct{ At time.Time }

// rtHiddenStamp is a struct JSON cannot see into: an unexported field is not
// encoded, so a snapshot of it drops the field.
type rtHiddenStamp struct{ at time.Time }

// rtCarried holds a time behind an interface, where json reaches the concrete
// value, as a message's `any` field does.
type rtCarried struct{ At any }

func rtJSONRoundTrip[T any](t *testing.T, v T) T {
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

func TestEqualRoundTripped(t *testing.T) {
	now := time.Now()
	named := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("IST", 5*60*60+30*60))
	// A zone offset with seconds in it, which time.Time.MarshalJSON writes only
	// to the minute: the snapshot keeps every offset it can and moves this one.
	moved := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("LMT", -(5*60*60+53*60+7)))

	for _, tc := range []struct {
		name string
		a, b any
		want bool
	}{
		{
			name: "the instant from the clock, without its monotonic reading",
			a:    rtStamped{At: now},
			b:    rtJSONRoundTrip(t, rtStamped{At: now}),
			want: true,
		},
		{
			name: "the instant in a named zone, without its *Location",
			a:    rtStamped{At: named},
			b:    rtJSONRoundTrip(t, rtStamped{At: named}),
			want: true,
		},
		{
			name: "the instant reached through a pointer",
			a:    &rtStamped{At: now},
			b:    rtJSONRoundTrip(t, &rtStamped{At: now}),
			want: true,
		},
		{
			name: "the instant reached through an interface",
			a:    rtCarried{At: now},
			b:    rtCarried{At: rtJSONRoundTrip(t, now)},
			want: true,
		},
		{
			name: "the instant inside a map",
			a:    map[string]rtStamped{"at": {At: named}},
			b:    rtJSONRoundTrip(t, map[string]rtStamped{"at": {At: named}}),
			want: true,
		},
		{
			name: "the instant as a map key",
			a:    map[time.Time]int{named: 1},
			b:    rtJSONRoundTrip(t, map[time.Time]int{named: 1}),
			want: true,
		},
		{
			name: "an instant JSON moves",
			a:    rtStamped{At: moved},
			b:    rtJSONRoundTrip(t, rtStamped{At: moved}),
			want: false,
		},
		{
			name: "a map key whose instant JSON moves",
			a:    map[time.Time]int{moved: 1},
			b:    rtJSONRoundTrip(t, map[time.Time]int{moved: 1}),
			want: false,
		},
		{
			name: "a map key at a different instant",
			a:    map[time.Time]int{named: 1},
			b:    map[time.Time]int{named.Add(time.Nanosecond): 1},
			want: false,
		},
		{
			name: "a different instant",
			a:    rtStamped{At: now},
			b:    rtStamped{At: now.Add(time.Nanosecond)},
			want: false,
		},
		{
			name: "a field the codec drops",
			a: struct {
				At   time.Time
				Memo string `json:"-"`
			}{At: named, Memo: "rent"},
			b: rtJSONRoundTrip(t, struct {
				At   time.Time
				Memo string `json:"-"`
			}{At: named, Memo: "rent"}),
			want: false,
		},
		{
			name: "a hidden field a snapshot drops",
			a:    rtHiddenStamp{at: named},
			b:    rtJSONRoundTrip(t, rtHiddenStamp{at: named}),
			want: false,
		},
		{
			name: "nil against empty",
			a:    []rtStamped(nil),
			b:    []rtStamped{},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codec.EqualRoundTripped(tc.a, tc.b); got != tc.want {
				t.Fatalf("EqualRoundTripped(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
