package ledger_test

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// docs/PERFORMANCE.md says the first stale Handle costs four round trips: the
// batch that raises, the rollback it leaves behind, and the two of the retry
// from stored state. The comment beside trustAbove in cache.go says a wrong
// guess costs three, and concludes from that number that "guessing pays above
// half". This measures the cost and fails while the comment names another one:
// the threshold is justified by that figure, and with four round trips the
// break-even is two guesses in three, not one in two.
func TestTheCostOfAWrongGuessMatchesItsComment(t *testing.T) {
	agg, trips := tracedCounter(t, 1)
	other := counterOn(ledger.New(agg.Store.Pool(), ledger.RequireActor(false)))

	// Fill the cache, then let another process write the stream: the next
	// message here is decided from stale state and refused by the lock.
	for range 3 {
		if _, err := agg.Handle(ctx, "a", add{1, 1 << 30}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := other.Handle(ctx, "a", add{1, 1 << 30}); err != nil {
		t.Fatal(err)
	}
	trips.reset()
	if _, err := agg.Handle(ctx, "a", add{1, 1 << 30}); err != nil {
		t.Fatal(err)
	}
	measured := trips.trips.Load()

	source, err := os.ReadFile("cache.go")
	if err != nil {
		t.Fatal(err)
	}
	claim := regexp.MustCompile(`a wrong one (\w+)`).FindSubmatch(source)
	if claim == nil {
		return // the comment no longer states a cost; nothing to disagree with
	}
	stated, ok := into(string(claim[1]))
	if !ok {
		return
	}
	if stated != measured {
		t.Fatalf("cache.go's comment says a wrong guess costs %d round trips; it cost %d", stated, measured)
	}
}

// into reads the number a comment spells, in words or digits.
func into(word string) (int64, bool) {
	words := map[string]int64{
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
		"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	}
	if n, ok := words[string(word)]; ok {
		return n, true
	}
	n, err := strconv.ParseInt(string(word), 10, 64)
	return n, err == nil
}
