package autoscan

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// newRepositoryDBTest connects to SILO_TEST_DATABASE_URL and returns a
// repository over it. It skips when the URL is unset or the database lacks
// requiredTable, so an unmigrated local database skips instead of failing.
func newRepositoryDBTest(t *testing.T, requiredTable string) (context.Context, *Repository) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1)::text`, requiredTable).Scan(&tableName); err != nil {
		t.Fatalf("check %s table: %v", requiredTable, err)
	}
	if tableName == nil || *tableName == "" {
		t.Skipf("test database has no %s table; apply migrations first", requiredTable)
	}

	cipher, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return ctx, NewRepository(pool, cipher)
}

// newSourceMarkerDBTest connects to SILO_TEST_DATABASE_URL (skipping when unset)
// and returns a repository, two connections, and a poll source bound to the
// first one with a stored marker.
func newSourceMarkerDBTest(t *testing.T) (context.Context, *Repository, Source, Connection) {
	t.Helper()
	ctx, repo := newRepositoryDBTest(t, "autoscan_sources")

	sonarr, err := repo.CreateConnection(ctx, Connection{Name: "marker-test-sonarr", Kind: "sonarr", BaseURL: "http://sonarr.invalid", APIKeyRef: "k1"})
	if err != nil {
		t.Fatalf("create sonarr connection: %v", err)
	}
	radarr, err := repo.CreateConnection(ctx, Connection{Name: "marker-test-radarr", Kind: "radarr", BaseURL: "http://radarr.invalid", APIKeyRef: "k2"})
	if err != nil {
		t.Fatalf("create radarr connection: %v", err)
	}

	interval := 300
	src, err := repo.CreateSource(ctx, Source{
		PluginID:            "silo.autoscan.arr",
		CapabilityID:        "arr",
		ConnectionID:        &sonarr.ID,
		Enabled:             true,
		DeliveryMode:        DeliveryModePoll,
		PollIntervalSeconds: &interval,
		PathRewrites:        []PathRewrite{{From: "/tv", To: "/mnt/tv"}},
		SourceConfig:        map[string]string{"scope": "all"},
		Label:               "Sonarr",
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	// Cleanups run last-in first-out: the source goes before the connections
	// it references (ON DELETE RESTRICT).
	t.Cleanup(func() {
		_ = repo.DeleteConnection(ctx, sonarr.ID)
		_ = repo.DeleteConnection(ctx, radarr.ID)
	})
	t.Cleanup(func() { _ = repo.DeleteSource(ctx, src.ID) })

	if !advanceFrom(ctx, t, repo, pollSnapshot(ctx, t, repo, src), "2026-10-04T16:46:02Z|44") {
		t.Fatal("seed marker advance was skipped")
	}
	src, err = repo.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if src.Marker == nil {
		t.Fatal("expected a stored marker before the update")
	}
	return ctx, repo, src, radarr
}

// A marker is a continuation token into one upstream. Updates that change
// which upstream (or which slice of it) the plugin reads must drop it, so the
// next poll starts from now instead of replaying or skipping history.
func TestUpdateSourceMarkerReset(t *testing.T) {
	for name, tc := range map[string]struct {
		edit      func(s *Source, other Connection)
		wantReset bool
	}{
		"connection switch clears marker": {
			edit:      func(s *Source, other Connection) { s.ConnectionID = &other.ID },
			wantReset: true,
		},
		"connection unbind clears marker": {
			edit:      func(s *Source, _ Connection) { s.ConnectionID = nil },
			wantReset: true,
		},
		"source config change clears marker": {
			edit:      func(s *Source, _ Connection) { s.SourceConfig = map[string]string{"scope": "movies"} },
			wantReset: true,
		},
		"label change keeps marker": {
			edit: func(s *Source, _ Connection) { s.Label = "Renamed" },
		},
		"interval change keeps marker": {
			edit: func(s *Source, _ Connection) { v := 900; s.PollIntervalSeconds = &v },
		},
		"rewrite change keeps marker": {
			edit: func(s *Source, _ Connection) { s.PathRewrites = []PathRewrite{{From: "/data/tv", To: "/mnt/tv"}} },
		},
		"disable keeps marker": {
			edit: func(s *Source, _ Connection) { s.Enabled = false },
		},
		"unchanged save keeps marker": {
			edit: func(*Source, Connection) {},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, repo, src, other := newSourceMarkerDBTest(t)
			before := *src.Marker

			edited := src
			tc.edit(&edited, other)
			updated, err := repo.UpdateSource(ctx, edited)
			if err != nil {
				t.Fatalf("update source: %v", err)
			}

			if tc.wantReset {
				// The source polls at the next cycle instead of waiting out
				// its interval from a run that read the old upstream.
				if updated.Marker != nil || updated.LastRunAt != nil {
					t.Fatalf("marker = %v, last run = %v, want both cleared", updated.Marker, updated.LastRunAt)
				}
				return
			}
			if updated.Marker == nil || *updated.Marker != before {
				t.Fatalf("marker = %v, want %q kept", updated.Marker, before)
			}
			if updated.LastRunAt == nil || !updated.LastRunAt.Equal(*src.LastRunAt) {
				t.Fatalf("last run = %v, want %v kept", updated.LastRunAt, src.LastRunAt)
			}
		})
	}
}

// createMarkedSource adds a poll source bound to connectionID with a stored
// marker, removed again when the test ends.
func createMarkedSource(ctx context.Context, t *testing.T, repo *Repository, connectionID, marker string) Source {
	t.Helper()
	src, err := repo.CreateSource(ctx, Source{
		PluginID:     "silo.autoscan.arr",
		CapabilityID: "arr",
		ConnectionID: &connectionID,
		Enabled:      true,
		DeliveryMode: DeliveryModePoll,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteSource(ctx, src.ID) })
	if !advanceFrom(ctx, t, repo, pollSnapshot(ctx, t, repo, src), marker) {
		t.Fatal("seed marker advance was skipped")
	}
	return src
}

// pollSnapshot is the state a poll reads before calling the plugin: the
// source row and, when bound, its connection row.
func pollSnapshot(ctx context.Context, t *testing.T, repo *Repository, src Source) MarkerAdvance {
	t.Helper()
	src, err := repo.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	adv := MarkerAdvance{Source: src}
	if src.ConnectionID != nil {
		conn, err := repo.GetConnection(ctx, *src.ConnectionID)
		if err != nil {
			t.Fatalf("get connection: %v", err)
		}
		adv.Connection = &conn
	}
	return adv
}

// advanceFrom finishes a poll that read snap, reporting whether the marker
// was stored.
func advanceFrom(ctx context.Context, t *testing.T, repo *Repository, snap MarkerAdvance, next string) bool {
	t.Helper()
	snap.NextMarker = next
	advanced, err := repo.AdvanceMarker(ctx, snap)
	if err != nil {
		t.Fatalf("advance marker: %v", err)
	}
	return advanced
}

// A poll already running when an admin resets the marker must not write the
// old upstream's next marker over the reset. Edits that keep the upstream
// leave the poll's advance in place.
func TestAdvanceMarkerSkipsStalePoll(t *testing.T) {
	const next = "2026-10-04T19:00:00Z|99"
	for name, tc := range map[string]struct {
		unmarked  bool // the poll started without a marker
		edit      func(ctx context.Context, t *testing.T, repo *Repository, src Source, other Connection)
		wantWrite bool
	}{
		"unchanged source stores the marker": {
			edit:      func(context.Context, *testing.T, *Repository, Source, Connection) {},
			wantWrite: true,
		},
		"label change still stores the marker": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				src.Label = "Renamed"
				mustUpdateSource(ctx, t, repo, src)
			},
			wantWrite: true,
		},
		"api key rotation still stores the marker": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				editConnection(ctx, t, repo, *src.ConnectionID, func(c *Connection) { c.APIKeyRef = "rotated-key" })
			},
			wantWrite: true,
		},
		"source config change keeps the reset": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				src.SourceConfig = map[string]string{"scope": "movies"}
				mustUpdateSource(ctx, t, repo, src)
			},
		},
		"connection switch keeps the reset": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, other Connection) {
				src.ConnectionID = &other.ID
				mustUpdateSource(ctx, t, repo, src)
			},
		},
		"connection repoint keeps the reset": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				editConnection(ctx, t, repo, *src.ConnectionID, func(c *Connection) { c.BaseURL = "http://sonarr-4k.invalid" })
			},
		},
		"connection repoint without a starting marker skips the write": {
			unmarked: true,
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				editConnection(ctx, t, repo, *src.ConnectionID, func(c *Connection) { c.BaseURL = "http://sonarr-4k.invalid" })
			},
		},
		"source config change then revert keeps the reset": {
			edit: func(ctx context.Context, t *testing.T, repo *Repository, src Source, _ Connection) {
				original := src.SourceConfig
				src.SourceConfig = map[string]string{"scope": "movies"}
				mustUpdateSource(ctx, t, repo, src)
				src.SourceConfig = original
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
			before, err := repo.GetSource(ctx, src.ID)
			if err != nil {
				t.Fatalf("get source: %v", err)
			}

			tc.edit(ctx, t, repo, before, other)
			afterEdit, err := repo.GetSource(ctx, src.ID)
			if err != nil {
				t.Fatalf("get source: %v", err)
			}

			wrote := advanceFrom(ctx, t, repo, snap, next)
			got, err := repo.GetSource(ctx, src.ID)
			if err != nil {
				t.Fatalf("get source: %v", err)
			}
			if wrote != tc.wantWrite {
				t.Fatalf("advanced = %v, want %v", wrote, tc.wantWrite)
			}
			if tc.wantWrite {
				if got.Marker == nil || *got.Marker != next {
					t.Fatalf("marker = %v, want %q", got.Marker, next)
				}
				if got.LastRunAt == nil {
					t.Fatal("last_run_at not stamped by the advance")
				}
				return
			}
			if afterEdit.Marker != nil {
				t.Fatalf("edit left marker %q, want the reset", *afterEdit.Marker)
			}
			if got.Marker != nil {
				t.Fatalf("stale poll wrote marker %q over the reset", *got.Marker)
			}
		})
	}
}

// Two polls of one source cannot both store a marker from the same start.
func TestAdvanceMarkerSkipsWhenMarkerMoved(t *testing.T) {
	ctx, repo, src, _ := newSourceMarkerDBTest(t)
	snap := pollSnapshot(ctx, t, repo, src)
	if !advanceFrom(ctx, t, repo, snap, "first") {
		t.Fatal("first advance skipped")
	}
	if advanceFrom(ctx, t, repo, snap, "second") {
		t.Fatal("second advance from the same start was stored")
	}
	got, err := repo.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if got.Marker == nil || *got.Marker != "first" {
		t.Fatalf("marker = %v, want %q", got.Marker, "first")
	}
}

func TestAdvanceMarkerUnknownSource(t *testing.T) {
	ctx, repo, src, _ := newSourceMarkerDBTest(t)
	snap := pollSnapshot(ctx, t, repo, src)
	snap.Source.ID = "00000000-0000-0000-0000-000000000000"
	snap.NextMarker = "m1"
	if _, err := repo.AdvanceMarker(ctx, snap); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A bound source's advance must carry that connection's row, or the upstream
// check would be skipped.
func TestAdvanceMarkerRequiresBoundConnectionRow(t *testing.T) {
	ctx, repo, src, other := newSourceMarkerDBTest(t)
	snap := pollSnapshot(ctx, t, repo, src)
	snap.NextMarker = "m1"

	missing := snap
	missing.Connection = nil
	if _, err := repo.AdvanceMarker(ctx, missing); err == nil {
		t.Fatal("advance without the connection row succeeded")
	}
	mismatched := snap
	mismatched.Connection = &other
	if _, err := repo.AdvanceMarker(ctx, mismatched); err == nil {
		t.Fatal("advance with another connection's row succeeded")
	}
	got, err := repo.GetSource(ctx, src.ID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if got.Marker == nil || *got.Marker != *src.Marker {
		t.Fatalf("marker = %v, want %q kept", got.Marker, *src.Marker)
	}
}

func mustUpdateSource(ctx context.Context, t *testing.T, repo *Repository, src Source) Source {
	t.Helper()
	out, err := repo.UpdateSource(ctx, src)
	if err != nil {
		t.Fatalf("update source: %v", err)
	}
	return out
}

func editConnection(ctx context.Context, t *testing.T, repo *Repository, id string, edit func(*Connection)) {
	t.Helper()
	conn, err := repo.GetConnection(ctx, id)
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	edit(&conn)
	if _, err := repo.UpdateConnection(ctx, conn); err != nil {
		t.Fatalf("update connection: %v", err)
	}
}

// Repointing a connection changes the upstream behind every source bound to
// it, so their markers must restart just as if each source had switched
// connections. Credential, name or kind edits leave the upstream alone.
func TestUpdateConnectionMarkerReset(t *testing.T) {
	for name, tc := range map[string]struct {
		edit      func(c *Connection)
		wantReset bool
	}{
		"base url change clears bound markers": {
			edit:      func(c *Connection) { c.BaseURL = "http://sonarr-4k.invalid" },
			wantReset: true,
		},
		"kind change keeps bound markers": {
			edit: func(c *Connection) { c.Kind = "radarr" },
		},
		"api key change keeps bound markers": {
			edit: func(c *Connection) { c.APIKeyRef = "rotated-key" },
		},
		"blank api key keeps bound markers": {
			edit: func(c *Connection) { c.APIKeyRef = "" },
		},
		"rename keeps bound markers": {
			edit: func(c *Connection) { c.Name = "marker-test-sonarr-renamed" },
		},
		"trailing slash keeps bound markers": {
			edit: func(c *Connection) { c.BaseURL = "http://sonarr.invalid/" },
		},
		"host case keeps bound markers": {
			edit: func(c *Connection) { c.BaseURL = "http://SONARR.invalid" },
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, repo, bound, other := newSourceMarkerDBTest(t)
			alsoBound := createMarkedSource(ctx, t, repo, *bound.ConnectionID, "2026-10-04T17:00:00Z|7")
			unrelated := createMarkedSource(ctx, t, repo, other.ID, "2026-10-04T18:00:00Z|9")
			// A source whose first poll failed has a run time but no marker.
			failedFirst, err := repo.CreateSource(ctx, Source{
				PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: bound.ConnectionID,
				Enabled: true, DeliveryMode: DeliveryModePoll,
			})
			if err != nil {
				t.Fatalf("create source: %v", err)
			}
			t.Cleanup(func() { _ = repo.DeleteSource(ctx, failedFirst.ID) })
			if err := repo.RecordError(ctx, failedFirst.ID, "connection refused"); err != nil {
				t.Fatalf("record error: %v", err)
			}

			conn, err := repo.GetConnection(ctx, *bound.ConnectionID)
			if err != nil {
				t.Fatalf("get connection: %v", err)
			}
			tc.edit(&conn)
			if _, err := repo.UpdateConnection(ctx, conn); err != nil {
				t.Fatalf("update connection: %v", err)
			}

			for _, id := range []string{bound.ID, alsoBound.ID} {
				got, err := repo.GetSource(ctx, id)
				if err != nil {
					t.Fatalf("get source: %v", err)
				}
				if tc.wantReset && (got.Marker != nil || got.LastRunAt != nil) {
					t.Fatalf("bound source %s marker = %v, last run = %v, want both cleared", id, got.Marker, got.LastRunAt)
				}
				if !tc.wantReset && (got.Marker == nil || got.LastRunAt == nil) {
					t.Fatalf("bound source %s marker = %v, last run = %v, want both kept", id, got.Marker, got.LastRunAt)
				}
			}
			retry, err := repo.GetSource(ctx, failedFirst.ID)
			if err != nil {
				t.Fatalf("get source: %v", err)
			}
			if (retry.LastRunAt == nil) != tc.wantReset {
				t.Fatalf("source with a failed first poll: last run = %v, want cleared %v", retry.LastRunAt, tc.wantReset)
			}
			got, err := repo.GetSource(ctx, unrelated.ID)
			if err != nil {
				t.Fatalf("get unrelated source: %v", err)
			}
			if got.Marker == nil || *got.Marker != "2026-10-04T18:00:00Z|9" {
				t.Fatalf("unrelated source marker = %v, want untouched", got.Marker)
			}
		})
	}
}
