package decisions

import (
	"flag"
	"os"
	"strconv"
	"testing"
)

// TestMain keeps a shortened rapid budget from rounding down to no checks at
// all. rapid divides -rapid.checks by five under -short, so -short with
// -rapid.checks=1 runs nothing: no message generator is drawn and every mutant
// test in this package survives whatever its generator would have reached.
// The mutant tests aim their generators at the transition they need (see
// aimed), so one check is enough; zero is not. Leave any budget that survives
// the division alone, and leave the full budget untouched.
func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		if f := flag.Lookup("rapid.checks"); f != nil {
			if n, err := strconv.Atoi(f.Value.String()); err == nil && n > 0 && n < 5 {
				// Five is the smallest full budget rapid turns into one check.
				_ = flag.Set("rapid.checks", "5")
			}
		}
	}
	os.Exit(m.Run())
}
