package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// Divergence is one stored decision the current Decider would make differently.
type Divergence struct {
	Stream string
	Seq    int64
	// Aspect is what differs: "refusal", "result", "events" or "effects", or
	// "decode" when the stored message no longer decodes.
	Aspect string
	Stored string
	Now    string
}

func (d Divergence) String() string {
	return fmt.Sprintf("%s#%d %s: stored %s, now %s", d.Stream, d.Seq, d.Aspect, d.Stored, d.Now)
}

// Verify re-decides every stored message of a stream with the current
// Decider and reports where it disagrees with what was recorded.
//
// State between messages is rebuilt from the stored events, never from the
// re-decided ones, so one divergence does not cascade into every later entry:
// each message is judged against the state it was actually decided in. A
// divergence is not necessarily a bug — an intended behaviour change diverges
// by design — but every one should be explained by a change someone meant.
func (a *Aggregate[S, M, R, Ev, Ef]) Verify(ctx context.Context, id string) ([]Divergence, error) {
	stream := a.Stream(id)
	entries, err := readStream(ctx, a.Store.pool, stream, 0)
	if err != nil {
		return nil, err
	}

	var out []Divergence
	state := a.Decider.Initial()
	for _, e := range entries {
		diverge := func(aspect string, stored, now any) {
			out = append(out, Divergence{Stream: e.Stream, Seq: e.Seq, Aspect: aspect,
				Stored: fmt.Sprintf("%+v", stored), Now: fmt.Sprintf("%+v", now)})
		}

		storedEvents, err := a.Events.DecodeAll(e.Events)
		if err != nil {
			return out, fmt.Errorf("cairn: %s#%d: %w", e.Stream, e.Seq, err)
		}

		msg, err := a.Messages.Decode(e.Message)
		if err != nil {
			diverge("decode", e.Message.Type, err)
			state = a.Decider.Fold(state, storedEvents)
			continue
		}

		d := a.Decider.Decide(state, msg)

		switch {
		case e.Refusal != nil && d.Refusal == nil:
			diverge("refusal", e.Refusal.Code, "accepted")
		case e.Refusal == nil && d.Refusal != nil:
			diverge("refusal", "accepted", d.Refusal.Code)
		case e.Refusal != nil && d.Refusal != nil && e.Refusal.Code != d.Refusal.Code:
			diverge("refusal", e.Refusal.Code, d.Refusal.Code)
		}

		// Once the verdict differs, so must everything that follows from it;
		// the refusal is the one divergence worth reading.
		if e.Refusal == nil && d.Refusal == nil {
			now, err := json.Marshal(d.Result)
			if err != nil {
				return out, err
			}
			if !sameJSON(e.Result, now) {
				diverge("result", string(e.Result), string(now))
			}
			if !sameSlice(storedEvents, d.Events) {
				diverge("events", storedEvents, d.Events)
			}
			storedEffects, err := a.Effects.DecodeAll(e.Effects)
			if err != nil {
				return out, fmt.Errorf("cairn: %s#%d: %w", e.Stream, e.Seq, err)
			}
			if !sameSlice(storedEffects, d.Effects) {
				diverge("effects", storedEffects, d.Effects)
			}
		}

		state = a.Decider.Fold(state, storedEvents)
	}
	return out, nil
}

// VerifyAll verifies every stream of the aggregate's kind that exists when it
// starts listing them. Streams are read one by one, so entries written while it
// runs may or may not be checked; run it against a quiet database, or treat a
// clean result as covering the history up to its start.
func (a *Aggregate[S, M, R, Ev, Ef]) VerifyAll(ctx context.Context) ([]Divergence, error) {
	streams, err := a.Store.Streams(ctx, a.Kind)
	if err != nil {
		return nil, err
	}
	var out []Divergence
	for _, s := range streams {
		d, err := a.Verify(ctx, s.ID)
		if err != nil {
			return out, err
		}
		out = append(out, d...)
	}
	return out, nil
}

func sameSlice[T any](a, b []T) bool {
	return (len(a) == 0 && len(b) == 0) || reflect.DeepEqual(a, b)
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}
