package autoscan

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A poll that fails after an admin reset its source was talking to the old
// upstream. Its error must not land on the source, and last_run_at must stay
// as the reset left it, so the next cycle polls the new upstream. Edits that
// keep the upstream still record the error.
func TestRecordErrorSkipsStalePoll(t *testing.T) {
	const msg = "dial tcp: i/o timeout"
	for name, tc := range map[string]struct {
		unmarked     bool // the poll started without a marker
		noConnection bool // the poll failed before it read its connection row
		edit         func(ctx context.Context, t *testing.T, repo *Repository, src Source, other Connection)
		wantRecord   bool
	}{
		"unchanged source records the error": {
			edit:       func(context.Context, *testing.T, *Repository, Source, Connection) {},
			wantRecord: true,
		},
		"label change still records the error": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				src.Label = "Renamed"
				mustUpdateSource(ctx, t, repo, src)
			},
			wantRecord: true,
		},
		"source config change drops the error": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				src.SourceConfig = map[string]string{"scope": "movies"}
				mustUpdateSource(ctx, t, repo, src)
			},
		},
		"connection switch drops the error": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, other Connection) {
				src.ConnectionID = &other.ID
				mustUpdateSource(ctx, t, repo, src)
			},
		},
		"connection repoint drops the error": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				editConnection(ctx, t, repo, *src.ConnectionID, func(c *Connection) { c.BaseURL = "http://sonarr-4k.invalid" })
			},
		},
		"connection repoint without a starting marker drops the error": {
			unmarked: true,
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				editConnection(ctx, t, repo, *src.ConnectionID, func(c *Connection) { c.BaseURL = "http://sonarr-4k.invalid" })
			},
		},
		"no connection row, unchanged source records the error": {
			noConnection: true,
			edit:         func(context.Context, *testing.T, *Repository, Source, Connection) {},
			wantRecord:   true,
		},
		"no connection row, source config change drops the error": {
			noConnection: true,
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				src.SourceConfig = map[string]string{"scope": "movies"}
				mustUpdateSource(ctx, t, repo, src)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, repo, src, other := newSourceMarkerDBTest(t)
			if tc.unmarked {
				// Clear the seeded marker, as a fresh source has none.
				src.SourceConfig = map[string]string{"scope": "fresh"}
				src = mustUpdateSource(ctx, t, repo, src)
				if src.Marker != nil {
					t.Fatalf("marker = %q, want none before the poll", *src.Marker)
				}
			}
			snap := pollSnapshot(ctx, t, repo, src)
			failure := PollFailure{Source: snap.Source, Connection: snap.Connection, Message: msg}
			if tc.noConnection {
				failure.Connection = nil
			}

			tc.edit(ctx, t, repo, snap.Source, other)
			afterEdit := mustGetSource(ctx, t, repo, src.ID)

			recorded, err := repo.RecordError(ctx, failure)
			if err != nil {
				t.Fatalf("record error: %v", err)
			}
			got := mustGetSource(ctx, t, repo, src.ID)
			if recorded != tc.wantRecord {
				t.Fatalf("recorded = %v, want %v", recorded, tc.wantRecord)
			}
			if tc.wantRecord {
				if got.LastError == nil || *got.LastError != msg {
					t.Fatalf("last_error = %v, want %q", got.LastError, msg)
				}
				if got.LastRunAt == nil {
					t.Fatal("last_run_at not stamped by the error")
				}
				return
			}
			if got.LastError != nil {
				t.Fatalf("stale poll wrote last_error %q over the reset", *got.LastError)
			}
			if !sameTime(got.LastRunAt, afterEdit.LastRunAt) {
				t.Fatalf("last_run_at = %v, want %v as the edit left it", got.LastRunAt, afterEdit.LastRunAt)
			}
		})
	}
}

func TestRecordErrorUnknownSource(t *testing.T) {
	ctx, repo, src, _ := newSourceMarkerDBTest(t)
	snap := pollSnapshot(ctx, t, repo, src)
	snap.Source.ID = "00000000-0000-0000-0000-000000000000"
	if _, err := repo.RecordError(ctx, PollFailure{Source: snap.Source, Connection: snap.Connection, Message: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A connection row that isn't the source's binding can't vouch for its upstream.
func TestRecordErrorRejectsAnotherConnectionsRow(t *testing.T) {
	ctx, repo, src, other := newSourceMarkerDBTest(t)
	snap := pollSnapshot(ctx, t, repo, src)
	if _, err := repo.RecordError(ctx, PollFailure{Source: snap.Source, Connection: &other, Message: "x"}); err == nil {
		t.Fatal("recording with another connection's row succeeded")
	}
	if got := mustGetSource(ctx, t, repo, src.ID); got.LastError != nil {
		t.Fatalf("last_error = %q, want none", *got.LastError)
	}
}

func mustGetSource(ctx context.Context, t *testing.T, repo *Repository, id string) Source {
	t.Helper()
	src, err := repo.GetSource(ctx, id)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	return src
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
