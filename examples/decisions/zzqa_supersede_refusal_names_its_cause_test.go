package decisions

import (
	"context"
	"errors"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// Supersede takes the replacement's ID from its caller, so that ID is input
// from outside: a URL path, a form field, a typo. The register answers three
// different mistakes with the same code, and two of them with a message that
// names a cause the input does not have:
//
//	Supersede(old, "d-does-not-exist")  not_allowed: only a decided decision can supersede another one
//	Supersede(old, "")                  invalid:     a decision cannot supersede itself
//	Supersede(old, old)                 invalid:     a decision cannot supersede itself
//
// The first names a decision that does not exist at all, and every other
// message on a missing stream is refused not_found ("no such decision"). The
// second names no replacement; it is not a self-supersede, and the message
// hides which input was wrong. Only the third says what its message says.
//
// The register's refusal codes are its contract (the constants in
// decisions.go), so a caller mapping them to HTTP statuses tells a client whose
// replacement ID is a typo that it has a lifecycle conflict instead.
func TestSupersedeRefusalNamesTheCause(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))

	decided := func(title string) string {
		t.Helper()
		id, err := svc.Create(ctx, "ana", title, "yes")
		must(t, err)
		state, err := svc.Get(ctx, id)
		must(t, err)
		must(t, svc.RecordOutcome(ctx, id, "ana", state.Options[0].ID, "because"))
		return id
	}
	first := decided("Ship the beta?")

	refusalOf := func(what string, err error) *cairn.Refusal {
		t.Helper()
		var refusal *cairn.Refusal
		if !errors.As(err, &refusal) {
			t.Fatalf("%s: want the register's refusal, got %v", what, err)
		}
		return refusal
	}

	// A replacement that exists but has not been decided is a lifecycle
	// refusal, and stays one.
	undecided, err := svc.Create(ctx, "ana", "not decided yet?", "yes")
	must(t, err)
	if refusal := refusalOf("undecided replacement", svc.Supersede(ctx, first, undecided, "ana")); refusal.Code != NotAllowed {
		t.Fatalf("undecided replacement: code %q, want %q", refusal.Code, NotAllowed)
	}

	// A replacement that does not exist is not a lifecycle refusal: the caller
	// has to be able to tell the two apart, or a typo reads as a conflict.
	missing := refusalOf("missing replacement", svc.Supersede(ctx, first, "d-no-such-decision", "ana"))
	if missing.Code == NotAllowed {
		t.Fatalf("a supersede naming a decision that does not exist was refused %q (%q), the same code as a replacement that exists but is not decided; "+
			"every other message on a missing stream is refused %q", missing.Code, missing.Message, NotFound)
	}

	// Naming no replacement at all and naming the decision itself are
	// different mistakes, and the refusal has to say which.
	self := refusalOf("self replacement", svc.Supersede(ctx, first, first, "ana"))
	empty := refusalOf("empty replacement", svc.Supersede(ctx, first, "", "ana"))
	if self.Code == empty.Code && self.Message == empty.Message {
		t.Fatalf("naming no replacement is refused the same way as naming the decision itself (%q: %q), "+
			"so the message cannot say which input was wrong", empty.Code, empty.Message)
	}

	// Nothing above changed the decision.
	after, err := svc.Get(ctx, first)
	must(t, err)
	if after.Status != Decided || after.SupersededBy != "" {
		t.Fatalf("a refused supersede changed the decision: %+v", after)
	}
}
