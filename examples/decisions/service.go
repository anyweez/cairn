package decisions

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
)

// Service is the register's shell: it turns a request into a fully resolved
// message — stamping the time, minting IDs, reading other streams under lock —
// and hands it to the ledger. Nothing here decides anything.
type Service struct {
	Register *ledger.Aggregate[State, Msg, Result, Event, Effect]
	Now      func() time.Time
	NewID    func(prefix string) string
}

// NewService builds a service over a store.
func NewService(store *ledger.Store) *Service {
	msgs, events, effects := Codecs()
	var n atomic.Int64
	return &Service{
		Register: &ledger.Aggregate[State, Msg, Result, Event, Effect]{
			Store: store, Kind: "decision", Decider: Decider,
			Messages: msgs, Events: events, Effects: effects,
			StateVersion: "decision/1",
		},
		Now: time.Now,
		NewID: func(prefix string) string {
			return fmt.Sprintf("%s%d", prefix, n.Add(1))
		},
	}
}

// Create files a decision with its seed options and returns its ID.
func (s *Service) Create(ctx context.Context, by, title string, options ...string) (string, error) {
	id := s.NewID("d-")
	seeded := make([]Option, len(options))
	for i, title := range options {
		seeded[i] = Option{ID: s.NewID("o-"), Title: title}
	}
	_, err := s.Register.Handle(ctx, id, Create{ID: id, By: by, At: s.Now().UTC(), Title: title, Options: seeded})
	return id, err
}

// AddOption adds an option to an open decision and returns its ID.
func (s *Service) AddOption(ctx context.Context, id, by, title string) (string, error) {
	option := Option{ID: s.NewID("o-"), Title: title}
	_, err := s.Register.Handle(ctx, id, AddOption{By: by, Option: option})
	return option.ID, err
}

// SavePosition records where a member stands.
func (s *Service) SavePosition(ctx context.Context, id, by, option, body string) error {
	_, err := s.Register.Handle(ctx, id, SavePosition{By: by, Option: option, Body: body})
	return err
}

// RecordOutcome settles an open decision.
func (s *Service) RecordOutcome(ctx context.Context, id, by, chosen, rationale string) error {
	_, err := s.Register.Handle(ctx, id, RecordOutcome{By: by, At: s.Now().UTC(), Chosen: chosen, Rationale: rationale})
	return err
}

// SetStatus defers, drops or reopens a decision.
func (s *Service) SetStatus(ctx context.Context, id, by string, to Status) error {
	_, err := s.Register.Handle(ctx, id, SetStatus{By: by, To: to})
	return err
}

// Acknowledge records a member's answer to the outcome.
func (s *Service) Acknowledge(ctx context.Context, id, by string, committed bool) error {
	_, err := s.Register.Handle(ctx, id, Acknowledge{By: by, Committed: committed})
	return err
}

// Supersede marks id replaced by with. Both streams are locked in one
// transaction, so the replacement cannot be reopened or superseded back while
// this decides; what was read of it travels in the message, so replay sees
// exactly the facts the decision was made on.
func (s *Service) Supersede(ctx context.Context, id, with, by string) error {
	var refusal *cairn.Refusal
	err := s.Register.Store.Transact(ctx, func(tx *ledger.Tx) error {
		if err := tx.Lock(ctx, s.Register.Stream(id), s.Register.Stream(with)); err != nil {
			return err
		}
		target, err := s.Register.LoadLocked(ctx, tx, id)
		if err != nil {
			return err
		}
		replacement, err := s.Register.LoadLocked(ctx, tx, with)
		if err != nil {
			return err
		}
		h, err := s.Register.Apply(ctx, tx, &target, Supersede{
			By:               by,
			With:             with,
			WithStatus:       replacement.State.Status,
			WithSupersededBy: replacement.State.SupersededBy,
		})
		refusal = h.Refusal
		return err
	})
	if err != nil {
		return err
	}
	if refusal != nil {
		return refusal
	}
	return nil
}

// Get returns a decision's current state.
func (s *Service) Get(ctx context.Context, id string) (State, error) {
	loaded, err := s.Register.Load(ctx, id)
	return loaded.State, err
}
