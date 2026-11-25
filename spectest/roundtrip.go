package spectest

import (
	"reflect"
	"strings"
	"time"
)

var timeType = reflect.TypeFor[time.Time]()

// describeRoundTrip renders a value the way codec.EqualRoundTripped reads it,
// for the D6 and D7 failure detail: fingerprint's walk, with the mode that
// leaves out what a round trip does not compare (a pointer's identity, a
// slice's spare capacity) and writes a time.Time as the instant it is. %+v
// renders none of those distinctions — int(5) in an `any` and float64(5) both
// print "5" — so a round trip that changed only the type read as "state {N:5}
// came back as {N:5}".
func describeRoundTrip(v any) string {
	var b strings.Builder
	walk(&b, reflect.ValueOf(v), map[visit]bool{}, renderMode{instant: true})
	return b.String()
}
