package handlers

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// newLibraryToggleFixture returns an enabled movie library, a handler that
// publishes to a test hub, and a drain of the catalog.library.changed reasons
// published so far, which also checks each toggle frame is admin-only.
func newLibraryToggleFixture(t *testing.T) (*pgxpool.Pool, int, *LibraryHandler, func() []string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var folder int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders(name,type,enabled) VALUES('enabled toggle event','movies',true) RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM media_folders WHERE id=$1`, folder)
	})

	hub := notifications.NewHub("api", &cache.NoopEventBus{})
	envelopes, unsubscribe := hub.EventsHub().Subscribe()
	t.Cleanup(unsubscribe)
	h := NewLibraryHandler(catalog.NewFolderRepository(pool), nil, nil, pool, nil)
	h.RealtimeHub = hub

	reasons := func() []string {
		var out []string
		for {
			select {
			case env := <-envelopes:
				if env.Event != string(notifications.TypeCatalogLibraryChanged) && env.Event != string(notifications.TypeLibraryChanged) {
					continue
				}
				if !env.AdminOnly {
					t.Errorf("%s reached non-administrators", env.Event)
				}
				if env.Event == string(notifications.TypeLibraryChanged) {
					continue
				}
				var data struct {
					Reason string `json:"reason"`
				}
				if err := json.Unmarshal(env.Data, &data); err != nil {
					t.Fatal(err)
				}
				out = append(out, data.Reason)
			default:
				return out
			}
		}
	}
	return pool, folder, h, reasons
}

// Turning a library on or off changes which items the admin catalog listing
// returns, so a catalog mirror needs a catalog.library.changed to re-list.
func TestLibraryEnabledTogglePublishesCatalogLibraryChangedDB(t *testing.T) {
	_, folder, h, reasons := newLibraryToggleFixture(t)
	for _, step := range []struct {
		enabled bool
		want    []string
	}{
		{false, []string{"library_disabled"}},
		{false, nil}, // unchanged: nothing to re-list
		{true, []string{"library_enabled"}},
	} {
		if _, err := h.UpdateLibrary(t.Context(), folder, 0, LibraryUpdateRequest{Enabled: new(step.enabled)}); err != nil {
			t.Fatal(err)
		}
		if got := reasons(); !slices.Equal(got, step.want) {
			t.Errorf("enabled=%v published %v, want %v", step.enabled, got, step.want)
		}
	}
}

// A disable that read the library as off, but commits after a concurrent
// enable, still changes what the listing returns and must publish.
func TestLibraryDisableAfterConcurrentEnablePublishesDB(t *testing.T) {
	pool, folder, h, reasons := newLibraryToggleFixture(t)
	if _, err := pool.Exec(t.Context(), `UPDATE media_folders SET enabled=false WHERE id=$1`, folder); err != nil {
		t.Fatal(err)
	}
	enable, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = enable.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := enable.Exec(t.Context(), `UPDATE media_folders SET enabled=true WHERE id=$1`, folder); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.UpdateLibrary(t.Context(), folder, 0, LibraryUpdateRequest{Enabled: new(false)})
		done <- err
	}()
	for {
		var waiting int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%media_folders%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("disable finished before the enable committed: %v", err)
		default:
		}
	}
	if err := enable.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := reasons(); !slices.Equal(got, []string{"library_disabled"}) {
		t.Errorf("disable committed after a concurrent enable published %v, want [library_disabled]", got)
	}
}
