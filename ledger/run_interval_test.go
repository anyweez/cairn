package ledger_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// A caller that passes a zero interval — a natural way to ask for "poll without
// waiting", consistent with the defaulted config fields around it — must get an
// error rather than a panic out of the caller's goroutine.
func TestRunRejectsANonPositiveIntervalInsteadOfPanicking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // let a fixed Run return instead of waiting forever

	cases := map[string]func(context.Context, time.Duration) error{
		"dispatcher": (&ledger.Dispatcher[struct{}]{}).Run,
		"projection": (&ledger.Projection{
			Store: &ledger.Store{}, Name: "test",
			Apply: func(context.Context, *ledger.Batch, ledger.Entry) error { return nil },
		}).Run,
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
				if err == nil || !strings.Contains(err.Error(), "interval") {
					t.Fatalf("Run returned %v; want an error naming the interval", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Run hung on a zero interval")
			}
		})
	}
}
