package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"
)

// Meta says who sent a message, through what, and why. It is recorded with
// the message's entry as its audit trail, and Decide never sees it: anything a
// decision depends on belongs in the message, where Verify can replay it.
type Meta struct {
	// Actor is an opaque ID for who sent the message. Never a name or an
	// email: entries are append-only, so they must not hold personal data.
	// It is indexed, so it may be at most 1024 bytes.
	Actor string
	// Via is the channel the message came through: "ui", "api", "mcp", an
	// agent's name. The Dispatcher sets "outbox". It is not indexed.
	Via string
	// CorrelationID ties together every message in one request or flow. It is
	// indexed, so it may be at most 1024 bytes.
	CorrelationID string
	// CausationID names what directly caused this message: an entry, or an
	// effect delivery's Key. It is not indexed.
	CausationID string
	// OccurredAt is when the producer says this happened, which can be well
	// before it was recorded. Zero when unknown.
	OccurredAt time.Time
	// Extra holds anything else worth keeping, stored as JSON.
	Extra map[string]any
}

// decodeMetaTail fills m's OccurredAt and Extra from an entry's occurred_at
// and extra columns, as a row selects them: a NULL occurred_at is the zero
// time, and extra is decoded unless it is the empty object. The error is the
// bare unmarshal error, for the caller to place in its own context.
func decodeMetaTail(m *Meta, occurred *time.Time, extra []byte) error {
	if occurred != nil {
		m.OccurredAt = *occurred
	}
	if string(extra) != "{}" {
		if err := json.Unmarshal(extra, &m.Extra); err != nil {
			return err
		}
	}
	return nil
}

// over returns m with every field set in o replacing m's, and Extra merged
// into a new map, so the result shares no map with m or o.
func (m Meta) over(o Meta) Meta {
	if o.Actor != "" {
		m.Actor = o.Actor
	}
	if o.Via != "" {
		m.Via = o.Via
	}
	if o.CorrelationID != "" {
		m.CorrelationID = o.CorrelationID
	}
	if o.CausationID != "" {
		m.CausationID = o.CausationID
	}
	if !o.OccurredAt.IsZero() {
		m.OccurredAt = o.OccurredAt
	}
	if len(m.Extra) > 0 || len(o.Extra) > 0 {
		extra := maps.Clone(m.Extra)
		if extra == nil {
			extra = map[string]any{}
		}
		maps.Copy(extra, o.Extra)
		m.Extra = extra
	}
	return m
}

type metaKey struct{}

// ContextWithMeta returns ctx carrying m, for every message handled under it.
// Request middleware sets it once (the authenticated actor, the channel, the
// request's ID as CorrelationID) so handlers need not pass it along. Fields
// already in ctx are kept unless m sets them.
func ContextWithMeta(ctx context.Context, m Meta) context.Context {
	return context.WithValue(ctx, metaKey{}, MetaFrom(ctx).over(m))
}

// MetaFrom returns a copy of the metadata ctx carries.
func MetaFrom(ctx context.Context) Meta {
	m, _ := ctx.Value(metaKey{}).(Meta)
	return Meta{}.over(m)
}

// WithMeta sets metadata for one message, over whatever its context carries.
func WithMeta(m Meta) Option { return func(o *options) { o.meta = o.meta.over(m) } }

// ErrNoActor is returned for a message with no actor, on a store that
// requires one.
var ErrNoActor = errors.New("cairn: the message has no actor")

// ErrActorMismatch is returned when the actor a message names disagrees with
// the actor in its metadata: a request acting as someone it is not.
var ErrActorMismatch = errors.New("cairn: the message's actor disagrees with its metadata")

// meta resolves the metadata for one message: the context's, then the
// options', then the actor the message itself names. That actor must agree
// with both the context's and the options', so a per-call option cannot stand
// in for the identity the request carried.
func (a *Aggregate[S, M, R, Ev, Ef]) meta(ctx context.Context, msg M, o options) (Meta, error) {
	carried := MetaFrom(ctx)
	m := carried.over(o.meta)
	if a.Actor != nil {
		if named := a.Actor(msg); named != "" {
			for _, actor := range []string{carried.Actor, o.meta.Actor} {
				if actor != "" && actor != named {
					return Meta{}, fmt.Errorf("%w: the message names %q, the metadata %q", ErrActorMismatch, named, actor)
				}
			}
			m.Actor = named
		}
	}
	if m.Actor == "" && a.Store.requireActor {
		return Meta{}, fmt.Errorf("%w (%s); set one with ContextWithMeta or WithMeta", ErrNoActor, a.Kind)
	}
	return m, nil
}
