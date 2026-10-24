package spectest

// D6 and D7 ask whether a value survived its round trip. JSON keeps a
// time.Time's instant but not its monotonic clock reading or the *Location it
// came from, so the comparison has to be the ledger's — the same instant —
// rather than Go's value identity, and no looser than that, or a genuine loss
// goes unreported. These are the comparison's own cases; the rules that use it
// are held to the same two shapes in spectest_test.

import (
	"encoding/json"
	"testing"
	"time"
)

type stamped struct{ At time.Time }

// hiddenStamp is a struct JSON cannot see into: an unexported field is not
// encoded, so a snapshot of it drops the field.
type hiddenStamp struct{ at time.Time }

// carried holds a time behind an interface, where json reaches the concrete
// value, as a message's `any` field does.
type carried struct{ At any }

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
			a:    stamped{At: now},
			b:    jsonRoundTrip(t, stamped{At: now}),
			want: true,
		},
		{
			name: "the instant in a named zone, without its *Location",
			a:    stamped{At: named},
			b:    jsonRoundTrip(t, stamped{At: named}),
			want: true,
		},
		{
			name: "the instant reached through a pointer",
			a:    &stamped{At: now},
			b:    jsonRoundTrip(t, &stamped{At: now}),
			want: true,
		},
		{
			name: "the instant reached through an interface",
			a:    carried{At: now},
			b:    carried{At: jsonRoundTrip(t, now)},
			want: true,
		},
		{
			name: "the instant inside a map",
			a:    map[string]stamped{"at": {At: named}},
			b:    jsonRoundTrip(t, map[string]stamped{"at": {At: named}}),
			want: true,
		},
		{
			name: "an instant JSON moves",
			a:    stamped{At: moved},
			b:    jsonRoundTrip(t, stamped{At: moved}),
			want: false,
		},
		{
			name: "a different instant",
			a:    stamped{At: now},
			b:    stamped{At: now.Add(time.Nanosecond)},
			want: false,
		},
		{
			name: "a field the codec drops",
			a: struct {
				At   time.Time
				Memo string `json:"-"`
			}{At: named, Memo: "rent"},
			b: jsonRoundTrip(t, struct {
				At   time.Time
				Memo string `json:"-"`
			}{At: named, Memo: "rent"}),
			want: false,
		},
		{
			name: "a hidden field a snapshot drops",
			a:    hiddenStamp{at: named},
			b:    jsonRoundTrip(t, hiddenStamp{at: named}),
			want: false,
		},
		{
			name: "nil against empty",
			a:    []stamped(nil),
			b:    []stamped{},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := equalRoundTripped(tc.a, tc.b); got != tc.want {
				t.Fatalf("equalRoundTripped(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
