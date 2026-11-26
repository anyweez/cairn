package decisions

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
)

// Service is the register's shell: it turns a request into a fully resolved
// message — stamping the time, minting IDs, reading other streams under lock —
// and hands it to the ledger. It is also where the register's words live: a
// caller types a name, a question or a body, the shell stores it in the
// application's own table (Content), and the message carries the opaque ref
// that names it. Nothing here decides anything.
type Service struct {
	Register *ledger.Aggregate[State, Msg, Result, Event, Effect]
	// Content is the application's own table. The ledger never sees it, so
	// deleting a row erases the data while the decision history keeps the ref.
	Content *Content
	Now     func() time.Time
	NewID   func(prefix string) string
}

// NewService builds a service over a store.
func NewService(store *ledger.Store) *Service {
	return &Service{
		Register: &ledger.Aggregate[State, Msg, Result, Event, Effect]{
			Store: store, Kind: "decision", Decider: Decider,
			Codecs:       Codecs(),
			StateVersion: "decision/1",
			Actor:        sender,
			StreamOf:     decisionOf,
		},
		Content: NewContent(),
		Now:     time.Now,
		// Random, so a restarted process never mints an ID already in the ledger.
		NewID: func(prefix string) string { return prefix + rand.Text()[:16] },
	}
}

// sender is the member a message names as its sender. The shell puts the
// member's opaque id there, never their name, and the ledger records it as the
// entry's actor: entries are append-only, so an actor can never be erased. A
// sender that is only whitespace trims to nothing, so it names nobody; it is
// reported as empty, and decide refuses it the way it refuses an empty one.
func sender(m Msg) string {
	var by Member
	switch m := m.(type) {
	case Create:
		by = m.By
	case Edit:
		by = m.By
	case AddOption:
		by = m.By
	case SavePosition:
		by = m.By
	case RecordOutcome:
		by = m.By
	case SetStatus:
		by = m.By
	case Supersede:
		by = m.By
	case Acknowledge:
		by = m.By
	}
	if strings.TrimSpace(string(by)) == "" {
		return ""
	}
	return string(by)
}

// decisionOf is the decision a message names for itself: a Create names the
// decision it opens, and every other message is addressed to a stream the
// caller already has. The ledger records a Create only on the stream it names,
// so a decision's ID and its stream are one identifier — Supersede and the
// reference index rely on that.
func decisionOf(m Msg) string {
	if c, ok := m.(Create); ok {
		return c.ID
	}
	return ""
}

// The slot a row of text belongs to: the decision field whose state names it.
// A row is stored before the message that names it is recorded, so the slot is
// how Content finds the row again when a later state names a replacement.
func titleSlot(id string) string               { return "title/" + id }
func optionSlot(id, option string) string      { return "option/" + id + "/" + option }
func positionSlot(id string, by Member) string { return "position/" + id + "/" + string(by) }
func rationaleSlot(id string) string           { return "rationale/" + id }

// store puts text in the application's own table and returns the ref the
// ledger records. The ledger never sees the words, so its codec cannot notice
// text it could not keep; the shell refuses it here instead. Blank text is
// stored under no ref, so the decider refuses the message exactly as it did
// when the text travelled in it. The slot and the member are recorded with the
// row so an erasure can find every version of it later.
func (s *Service) store(what, slot string, by Member, text string) (Ref, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	if !cairn.StorableText(text) {
		return "", &cairn.Refusal{Code: Invalid,
			Message: fmt.Sprintf("%s is not valid UTF-8 or contains a NUL, so it cannot be stored", what)}
	}
	return s.Content.PutFor(slot, by, text), nil
}

// forget removes exactly the text a message stored when the ledger did not
// record it, so a failed or refused request leaves no rows behind. It deletes
// the request's own rows rather than the slots they were stored for, because an
// earlier row in the same slot is still named by the decision's state.
func (s *Service) forget(refs ...Ref) {
	s.Content.discard(refs...)
}

// uncertain reports the two ledger errors that leave it open whether the
// message was recorded: ErrRecorded says the transaction committed but its
// result could not be read, and ErrUnknownOutcome says the commit may have
// happened — the connection went away before the COMMIT's response arrived, or
// the caller stopped waiting after a recording group had taken its message.
func uncertain(err error) bool {
	return errors.Is(err, ledger.ErrRecorded) || errors.Is(err, ledger.ErrUnknownOutcome)
}

// mayHaveRecorded reports whether err leaves open that the message was recorded:
// nil means it was, the uncertain errors say it may have been, and a refusal is
// an entry like any other. Any other error was refused before anything was
// sent, so nothing names the sender's member row.
func mayHaveRecorded(err error) bool {
	if err == nil || uncertain(err) {
		return true
	}
	var refusal *cairn.Refusal
	return errors.As(err, &refusal)
}

// forgetUnlessRecorded keeps the text a message stored unless the error proves
// nothing was recorded. Either uncertain error leaves that open: the entry may
// name these refs, and nothing else holds the words, so deleting them would
// leave a decision nothing can answer with. Every other error — a refusal, a
// guard, a validation failure, a transaction the database rolled back, or a
// cancellation that provably sent nothing — means no entry names them, so the
// cleanup runs.
func (s *Service) forgetUnlessRecorded(err error, refs ...Ref) {
	if uncertain(err) {
		return
	}
	s.forget(refs...)
}

// Create files a decision with its seed options and returns its ID.
func (s *Service) Create(ctx context.Context, by, title string, options ...string) (string, error) {
	id := s.NewID("d-")
	byID := s.Content.resolve(by)
	recorded := false
	defer func() { s.Content.done(byID, recorded) }()

	titleRef, err := s.store("the question", titleSlot(id), byID, title)
	if err != nil {
		return id, err
	}
	refs := []Ref{titleRef}
	seeded := make([]Option, len(options))
	for i, option := range options {
		optionID := s.NewID("o-")
		ref, err := s.store("an option's title", optionSlot(id, optionID), byID, option)
		if err != nil {
			s.forget(refs...)
			return id, err
		}
		refs = append(refs, ref)
		seeded[i] = Option{ID: optionID, Title: ref}
	}
	_, err = s.Register.Handle(ctx, id, Create{ID: id, By: byID, At: s.Now().UTC(), Title: titleRef, Options: seeded})
	recorded = mayHaveRecorded(err)
	if err != nil {
		s.forgetUnlessRecorded(err, refs...)
	}
	return id, err
}

// AddOption adds an option to an open decision and returns its ID.
func (s *Service) AddOption(ctx context.Context, id, by, title string) (string, error) {
	option := s.NewID("o-")
	byID := s.Content.resolve(by)
	recorded := false
	defer func() { s.Content.done(byID, recorded) }()

	ref, err := s.store("an option's title", optionSlot(id, option), byID, title)
	if err != nil {
		return option, err
	}
	_, err = s.Register.Handle(ctx, id, AddOption{By: byID, Option: Option{ID: option, Title: ref}})
	recorded = mayHaveRecorded(err)
	if err != nil {
		s.forgetUnlessRecorded(err, ref)
	}
	return option, err
}

// SavePosition records where a member stands.
func (s *Service) SavePosition(ctx context.Context, id, by, option, body string) error {
	byID := s.Content.resolve(by)
	recorded := false
	defer func() { s.Content.done(byID, recorded) }()

	ref, err := s.store("a position's body", positionSlot(id, byID), byID, body)
	if err != nil {
		return err
	}
	_, err = s.Register.Handle(ctx, id, SavePosition{By: byID, Option: option, Body: ref})
	recorded = mayHaveRecorded(err)
	if err != nil {
		s.forgetUnlessRecorded(err, ref)
	}
	return err
}

// RecordOutcome settles an open decision.
func (s *Service) RecordOutcome(ctx context.Context, id, by, chosen, rationale string) error {
	byID := s.Content.resolve(by)
	recorded := false
	defer func() { s.Content.done(byID, recorded) }()

	ref, err := s.store("the outcome's rationale", rationaleSlot(id), byID, rationale)
	if err != nil {
		return err
	}
	_, err = s.Register.Handle(ctx, id, RecordOutcome{By: byID, At: s.Now().UTC(), Chosen: chosen, Rationale: ref})
	recorded = mayHaveRecorded(err)
	if err != nil {
		s.forgetUnlessRecorded(err, ref)
	}
	return err
}

// SetStatus defers, drops or reopens a decision.
func (s *Service) SetStatus(ctx context.Context, id, by string, to Status) error {
	byID := s.Content.resolve(by)
	recorded := false
	defer func() { s.Content.done(byID, recorded) }()

	_, err := s.Register.Handle(ctx, id, SetStatus{By: byID, To: to})
	recorded = mayHaveRecorded(err)
	return err
}

// Acknowledge records a member's answer to the outcome.
func (s *Service) Acknowledge(ctx context.Context, id, by string, committed bool) error {
	byID := s.Content.resolve(by)
	recorded := false
	defer func() { s.Content.done(byID, recorded) }()

	_, err := s.Register.Handle(ctx, id, Acknowledge{By: byID, Committed: committed})
	recorded = mayHaveRecorded(err)
	return err
}

// Supersede marks id replaced by with. Both streams are locked in one
// transaction, so the replacement cannot be reopened or superseded back while
// this decides; what was read of it travels in the message, so replay sees
// exactly the facts the decision was made on. Since a decision's stream is its
// ID (see decisionOf), with names the replacement as both.
func (s *Service) Supersede(ctx context.Context, id, with, by string) error {
	byID := s.Content.resolve(by)
	recorded := false
	defer func() { s.Content.done(byID, recorded) }()

	var refusal *cairn.Refusal
	streams := []ledger.Stream{s.Register.Stream(id)}
	if with != "" { // an empty replacement is the decider's to refuse
		streams = append(streams, s.Register.Stream(with))
	}
	err := s.Register.Store.Transact(ctx, streams, func(tx *ledger.Tx) error {
		target, err := s.Register.LoadLocked(ctx, tx, id)
		if err != nil {
			return err
		}
		msg := Supersede{By: byID, With: with}
		if with != "" {
			replacement, err := s.Register.LoadLocked(ctx, tx, with)
			if err != nil {
				return err
			}
			// with is the replacement's stream, and a decision's stream is its
			// ID, so With names the decision the self-supersede guard and DR3
			// compare against.
			msg.WithStatus, msg.WithSupersededBy = replacement.State.Status, replacement.State.SupersededBy
		}
		h, err := s.Register.Apply(ctx, tx, &target, msg)
		refusal = h.Refusal
		return err
	})
	if err != nil {
		recorded = mayHaveRecorded(err)
		return err
	}
	// The transaction committed, so a refusal in it is an entry like any other.
	recorded = true
	if refusal != nil {
		return refusal
	}
	return nil
}

// Get returns a decision's current state. Its words are not in the ledger: the
// refs the state carries name rows of the register's own table, so read them
// with Content.Text.
func (s *Service) Get(ctx context.Context, id string) (State, error) {
	loaded, err := s.Register.Load(ctx, id)
	return loaded.State, err
}
