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

// Turning a library on or off changes which items the admin catalog listing
// returns, so a catalog mirror needs a catalog.library.changed to re-list.
func TestLibraryEnabledTogglePublishesCatalogLibraryChangedDB(t *testing.T) {
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
				if env.Event != string(notifications.TypeCatalogLibraryChanged) {
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
