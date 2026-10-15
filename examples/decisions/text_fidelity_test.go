package decisions

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// These reproduce a defect: text that is not valid UTF-8 is accepted by the
// decider and then silently rewritten by the codec (encoding/json replaces
// invalid bytes with U+FFFD), so the ledger stores a title or position body
// different from the one the caller sent. cairn's own D6 says a message must
// survive encoding and decoding unchanged; the codec round trip below shows it
// does not.

// The user-facing symptom, through the service and a real ledger. Skipped when
// no test database is configured; the codec test below needs none.
func TestInvalidUTF8TitleIsStoredUnchanged(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))

	title := "ship \xff\xfe it"
	id, err := svc.Create(ctx, "ana", title, "yes")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	state, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if state.Title != title {
		t.Fatalf("title sent %q came back as %q", title, state.Title)
	}
}

// The root cause, with no database: the message codec does not round-trip a
// message whose text is not valid UTF-8.
func TestInvalidUTF8SurvivesTheMessageCodec(t *testing.T) {
	set := Codecs()
	for name, msg := range map[string]Msg{
		"create title":  Create{ID: "d-1", By: "ana", Title: "ship \xff\xfe it"},
		"position body": SavePosition{By: "ana", Body: "a\xffb"},
	} {
		t.Run(name, func(t *testing.T) {
			env, err := set.Messages.Encode(msg)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			// Through bytes, exactly as the ledger stores the envelope.
			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var stored codec.Envelope
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			back, err := set.Messages.Decode(stored)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !reflect.DeepEqual(msg, back) {
				t.Fatalf("sent %+v, came back %+v", msg, back)
			}
		})
	}
}
