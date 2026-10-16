package ledger

import "testing"

// coversJSON compares a stored result with today's result. Decoding through
// `any` used to round every number to float64, so values past 2^53 compared
// equal; it must now keep them distinct without inventing a divergence for two
// encodings of the same number.
func TestCoversJSONKeepsNumbersDistinct(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stored string
		now    string
		want   bool
	}{
		{"equal small ints", `3`, `3`, true},
		{"ints past float precision", `9007199254740993`, `9007199254740993`, true},
		{"ints one apart past float precision", `9007199254740993`, `9007199254740992`, false},
		{"same number written differently", `1`, `1.0`, true},
		{"nested numbers past float precision", `{"total":[9007199254740993]}`, `{"total":[9007199254740992]}`, false},
		{"a field the entry predates is skipped", `{"total":3}`, `{"total":3,"cap":9}`, true},
		{"a stored field that differs", `{"total":3,"cap":9}`, `{"total":3,"cap":10}`, false},
		{"a number against a string", `3`, `"3"`, false},
	}
	for _, tc := range cases {
		if got := coversJSON([]byte(tc.stored), []byte(tc.now)); got != tc.want {
			t.Errorf("%s: coversJSON(%s, %s) = %v, want %v", tc.name, tc.stored, tc.now, got, tc.want)
		}
	}
}
