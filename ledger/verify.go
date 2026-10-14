package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// Divergence is one stored decision the current Decider would make differently.
type Divergence struct {
	Stream Stream
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
//
// A stored result is upcast to the current version and compared on the fields
// it carries. A field added since it was recorded is checked when its upcaster
// fills it in, and skipped when the upcaster leaves it out, since the entry
// can't say what it would have been. So a field tagged omitempty is checked
// only when the stored result carries it, and a map in R only on the keys the
// stored result has.
func (a *Aggregate[S, M, R, Ev, Ef]) Verify(ctx context.Context, id string) ([]Divergence, error) {
	if a.Codecs.Results == nil {
		return nil, fmt.Errorf("cairn: %s has no Results codec", a.Kind)
	}
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

		storedEvents, err := a.Codecs.Events.DecodeAll(e.Events)
		if err != nil {
			return out, fmt.Errorf("cairn: %s#%d: %w", e.Stream, e.Seq, err)
		}

		msg, err := a.Codecs.Messages.Decode(e.Message)
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
			now, err := a.Codecs.Results.Encode(d.Result)
			if err != nil {
				return out, err
			}
			switch stored, err := a.storedResult(e); {
			case err != nil:
				diverge("decode", "result", err)
			case e.Result.Type != now.Type:
				diverge("result", e.Result.Type, now.Type)
			case !coversJSON(stored, now.Data):
				diverge("result", string(stored), string(now.Data))
			}
			if !sameSlice(storedEvents, d.Events) {
				diverge("events", storedEvents, d.Events)
			}
			storedEffects, err := a.Codecs.Effects.DecodeAll(e.Effects)
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

// storedResult is an accepted entry's result, upcast to the current version.
func (a *Aggregate[S, M, R, Ev, Ef]) storedResult(e Entry) (json.RawMessage, error) {
	if e.Result == nil {
		return nil, errors.New("accepted entry has no result")
	}
	return a.Codecs.Results.Latest(*e.Result)
}

// coversJSON reports whether now agrees with every field stored carries.
func coversJSON(stored, now []byte) bool {
	var x, y any
	if json.Unmarshal(stored, &x) != nil || json.Unmarshal(now, &y) != nil {
		return bytes.Equal(stored, now)
	}
	return covers(x, y)
}

// covers compares decoded JSON. A key missing from a stored object is a field
// the entry predates, which its upcasters could not fill in, so it is not
// compared; every key stored carries must be in now with a value it covers.
// Arrays must have the same length and cover element by element, and anything
// else must be equal.
func covers(stored, now any) bool {
	switch s := stored.(type) {
	case map[string]any:
		n, ok := now.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range s {
			if nv, ok := n[k]; !ok || !covers(v, nv) {
				return false
			}
		}
		return true
	case []any:
		n, ok := now.([]any)
		if !ok || len(n) != len(s) {
			return false
		}
		for i := range s {
			if !covers(s[i], n[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(stored, now)
	}
}
