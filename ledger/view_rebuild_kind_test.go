package ledger_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
)

// Aggregate.Validate checks that Kind is non-empty and contains no slash, but
// not that Postgres can store it. Handle refuses an unstorable Kind cleanly
// because validStream reaches it through the lock path; the two view rebuild
// paths filter on Kind directly, so a Kind with a NUL or invalid UTF-8 comes
// back as a raw *pgconn.PgError. A setup mistake must be a cairn error, the
// way the read paths t2555 fixed behave.
func TestRebuildingAViewRefusesAnUnstorableKind(t *testing.T) {
	t.Parallel()
	for name, kind := range map[string]string{
		"NUL":          "counter\x00x",
		"invalid utf8": "counter\xffx",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			agg := newCounter(t)
			agg.Kind = kind
			view := ledger.View[counter]{Name: "v", Write: func(*ledger.Batch, ledger.Loaded[counter], ledger.Entry) error {
				return nil
			}}
			entryView := ledger.EntryView[counter, event]{Name: "ev", Write: func(*ledger.Batch, ledger.Loaded[counter], ledger.Loaded[counter], ledger.Entry, []event) error {
				return nil
			}}

			for what, err := range map[string]error{
				"Rebuild":        nil,
				"RebuildEntries": nil,
			} {
				switch what {
				case "Rebuild":
					_, err = agg.Rebuild(ctx, view)
				case "RebuildEntries":
					_, err = agg.RebuildEntries(ctx, entryView)
				}
				if err == nil {
					t.Fatalf("%s: expected the unstorable Kind to be refused", what)
				}
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) {
					t.Fatalf("%s: caller got a raw Postgres error (%s: %s) instead of a cairn error",
						what, pgErr.SQLState(), pgErr.Message)
				}
			}
		})
	}
}
