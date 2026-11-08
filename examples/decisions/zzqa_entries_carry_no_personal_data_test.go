package decisions

import (
	"context"
	"strings"
	"testing"

	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// README.md ("Personal data") and docs/REDACTION.md state the rule: messages,
// events, effects, results and entry metadata carry no personal data — "Names,
// addresses, tokens, secrets, comment text and any other content live in the
// application's own mutable tables" — and ledger.Meta.Actor's own contract is
// "an opaque ID for who sent the message. Never a name or an email: entries are
// append-only, so they must not hold personal data."
//
// examples/decisions is the register the README points a reader at, end to
// end, and it stores all of it: the sender the caller names (a display name or
// an address) becomes the entry's Actor and is copied into the events as
// Created.By, PositionSaved.Member and Acknowledged.Member; the question, each
// option's title, a position's body and the outcome's rationale travel in the
// message, the events and the IndexReferences effect. The append-only trigger
// refuses every UPDATE and DELETE, so none of it can be removed afterwards.
//
// This test runs the flow the register documents with text and an identity a
// person would type, then looks for any of it in the entries the ledger stored.
func TestEntriesCarryNoPersonalData(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))

	// A sender, a question, an option, a position body and a rationale as a
	// real register would carry them. The address is deliberate: the contract
	// on Meta.Actor names an email as the thing that must never be in an entry.
	const (
		member    = "ana.sorensen@example.com"
		question  = "Should we ship the beta to Ana's team?"
		option    = "ship it to Ana's team on Friday"
		body      = "Ana says her team can support it after the holidays"
		rationale = "Ana's team has the capacity, so we ship on Friday"
	)

	id, err := svc.Create(ctx, member, question, option)
	must(t, err)
	state, err := svc.Get(ctx, id)
	must(t, err)
	must(t, svc.SavePosition(ctx, id, member, state.Options[0].ID, body))
	must(t, svc.RecordOutcome(ctx, id, member, state.Options[0].ID, rationale))

	// Everything a person typed, and the identity the caller named.
	personal := map[string]string{
		"the sender's address":    member,
		"the question":            question,
		"the option's title":      option,
		"the position's body":     body,
		"the outcome's rationale": rationale,
	}

	streams, err := svc.Register.Store.Streams(ctx, "decision")
	must(t, err)
	if len(streams) == 0 {
		t.Fatal("the register recorded no streams, so nothing was checked")
	}
	var entries []ledger.Entry
	for _, s := range streams {
		read, err := svc.Register.Store.ReadStream(ctx, s, 0)
		must(t, err)
		entries = append(entries, read...)
	}
	if len(entries) == 0 {
		t.Fatal("the register recorded no entries, so nothing was checked")
	}

	for _, e := range entries {
		// Where an entry can carry text: its metadata, the message, the result,
		// and every event and effect.
		carriers := map[string]string{
			"Meta.Actor": e.Meta.Actor,
			"Meta.Via":   e.Meta.Via,
			"message":    string(e.Message.Data),
			"events":     envelopeData(e.Events),
			"effects":    envelopeData(e.Effects),
		}
		if e.Result != nil {
			carriers["result"] = string(e.Result.Data)
		}
		if e.Refusal != nil {
			carriers["refusal"] = e.Refusal.Code + " " + e.Refusal.Message
		}
		for where, text := range carriers {
			for what, want := range personal {
				if strings.Contains(text, want) {
					t.Errorf("%s#%d's %s carries %s (%q); entries are append-only and can never be erased", e.Stream, e.Seq, where, what, want)
				}
			}
		}
	}
}

// envelopeData joins the JSON each envelope carries.
func envelopeData(envs []codec.Envelope) string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = string(e.Data)
	}
	return strings.Join(out, "\n")
}
