package decisions

import (
	"context"
	"errors"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// These reproduce a defect: text that is not valid UTF-8 is accepted by the
// decider and then silently rewritten by encoding/json (which replaces every
// invalid byte with U+FFFD), so the ledger stores a title or position body
// different from the one the caller sent. The fix is to refuse text the
// ledger cannot store, rather than to store something else.

// The user-facing symptom, through the service and a real ledger. Skipped when
// no test database is configured.
func TestInvalidUTF8TitleIsRefusedNotStored(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))

	_, err := svc.Create(ctx, "ana", "ship \xff\xfe it", "yes")
	var refusal *cairn.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a title that is not valid UTF-8 should be refused, got %v", err)
	}
}

// The root cause, with no database: the message codec refuses text it cannot
// store instead of encoding it as U+FFFD.
func TestInvalidUTF8IsRejectedByTheMessageCodec(t *testing.T) {
	set := Codecs()
	for name, msg := range map[string]Msg{
		"create title":  Create{ID: "d-1", By: "ana", Title: "ship \xff\xfe it"},
		"position body": SavePosition{By: "ana", Body: "a\xffb"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := set.Messages.Encode(msg); err == nil {
				t.Fatalf("Encode accepted a message whose text is not valid UTF-8: %+v", msg)
			}
		})
	}
}
