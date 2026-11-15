package ledger

import (
	"strings"
	"testing"
)

// derivedCorrelation names the flow an effect starts when its entry had no
// correlation ID. It is written to an indexed column, and a stream name may
// already fill that column's bound, so the derived value has to stay storable.
func TestADerivedCorrelationFitsTheIndexedBound(t *testing.T) {
	t.Parallel()
	ordinary := Stream{Kind: "counter", ID: "a"}
	if got := derivedCorrelation(ordinary, 1); got != "counter/a#1" {
		t.Fatalf("derived correlation %q, want %q", got, "counter/a#1")
	}

	// Both names fill the ledger's bound for a new stream exactly. The second
	// ends in a multi-byte rune, which a byte-wise cut would corrupt.
	longest := Stream{Kind: "counter", ID: strings.Repeat("s", maxIndexedTextBytes-len("counter/"))}
	unicode := Stream{Kind: "counter", ID: strings.Repeat("s", maxIndexedTextBytes-len("counter/")-len("😀")) + "😀"}
	for _, st := range []Stream{longest, unicode} {
		if got := len(st.String()); got != maxIndexedTextBytes {
			t.Fatalf("fixture stream %q is %d bytes, want %d", st, got, maxIndexedTextBytes)
		}
		got := derivedCorrelation(st, 1)
		if err := validIndexedText("correlation ID", got); err != nil {
			t.Fatalf("derived correlation for a %d-byte stream: %v", len(st.String()), err)
		}
	}

	// Shortening must not merge two streams that share every byte before the
	// bound into one flow.
	twin := longest
	twin.ID = strings.TrimSuffix(twin.ID, "s") + "t"
	if a, b := derivedCorrelation(longest, 1), derivedCorrelation(twin, 1); a == b {
		t.Fatalf("two long streams share derived correlation %q", a)
	}
}
