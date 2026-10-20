package ledger_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// A caller that wants a dispatcher or projection to poll without waiting
// passes a zero interval. Run must report the bad interval rather than
// panicking out of the caller's goroutine. A zero interval is not documented
// as invalid, and the defaulted config fields around it (BatchSize, Lease,
// Backoff) suggest a zero is a value to default rather than to crash on.
func TestRunRejectsANonPositiveIntervalInsteadOfPanicking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // let a fixed Run return instead of waiting forever

	cases := map[string]func(context.Context, time.Duration) error{
		"dispatcher": (&ledger.Dispatcher[struct{}]{}).Run,
		"projection": (&ledger.Projection{}).Run,
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				defer func() {
					if p := recover(); p != nil {
						done <- fmt.Errorf("panic: %v", p)
					}
				}()
				done <- run(ctx, 0)
			}()
			select {
			case err := <-done:
				if err != nil && strings.HasPrefix(err.Error(), "panic:") {
					t.Fatalf("Run panicked on a zero interval: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Run hung on a zero interval")
			}
		})
	}
}
