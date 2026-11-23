package decisions

import (
	"strings"
	"testing"
	"time"
)

// Create and AddOption refuse a message with an empty ID with the same
// combined check that refuses a blank title, and answer with the title's
// message. The title was supplied, so the caller is told to fix a field it got
// right and is never told which one is actually empty.
func TestRefusalNamesTheMissingID(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	open := State{ID: "d-1", Status: Open, Options: []Option{{ID: "o1", Title: "yes"}}, Positions: map[Member]Position{}}

	for _, tc := range []struct {
		name  string
		state State
		msg   Msg
	}{
		{
			name:  "create",
			state: State{},
			msg:   Create{ID: "", By: "ana", At: at, Title: "ship it?", Options: []Option{{ID: "o1", Title: "yes"}}},
		},
		{
			name:  "create option",
			state: State{},
			msg:   Create{ID: "d-1", By: "ana", At: at, Title: "ship it?", Options: []Option{{ID: "", Title: "yes"}}},
		},
		{
			name:  "add option",
			state: open,
			msg:   AddOption{By: "ana", Option: Option{ID: "", Title: "yes"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Decider.Decide(tc.state, tc.msg)
			if !d.Refused() {
				t.Fatalf("accepted %T with an empty ID", tc.msg)
			}
			if d.Refusal.Code != Invalid {
				t.Fatalf("refusal code %q, want %q", d.Refusal.Code, Invalid)
			}
			got := strings.ToLower(d.Refusal.Message)
			if !strings.Contains(got, " id") && !strings.Contains(got, "identifier") {
				t.Fatalf("refused %T with %q, which never names the empty ID", tc.msg, d.Refusal.Message)
			}
			// The title it supplied was not the empty field, so blaming it
			// sends the caller to fix something that is already right.
			if strings.Contains(got, "question") || strings.Contains(got, "title") {
				t.Fatalf("refused %T with %q, which names the title rather than the empty ID", tc.msg, d.Refusal.Message)
			}
		})
	}
}
