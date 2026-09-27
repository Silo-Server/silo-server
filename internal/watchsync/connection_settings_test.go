package watchsync

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestConnectionSettingsGuardDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "watch-guard-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,'guard-p','Guard')", userID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("watch-settings-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	initial, err := repo.UpsertConnection(ctx, Connection{Provider: "guard", UserID: userID, ProfileID: "guard-p", AccessToken: "token", ImportWatchedEnabled: true, ScrobbleEnabled: true, SyncWatchlistOrderEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	expected := ConnectionVersion{ID: initial.ID, UpdatedAt: initial.UpdatedAt}
	start := make(chan struct{})
	result := make(chan error, 2)
	var effects atomic.Int32
	var wg sync.WaitGroup
	for _, update := range []ConnectionUpdate{{ImportWatchedEnabled: new(false)}, {ScrobbleEnabled: new(false)}} {
		wg.Go(func() {
			<-start
			_, err := repo.UpdateConnectionSettings(ctx, "guard", userID, "guard-p", &expected, update, func(Connection) error { effects.Add(1); return nil })
			result <- err
		})
	}
	close(start)
	wg.Wait()
	close(result)
	successes, stale := 0, 0
	for err := range result {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrStaleConnection):
			stale++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 || effects.Load() != 1 {
		t.Fatalf("success=%d stale=%d effects=%d", successes, stale, effects.Load())
	}
	saved, ok, err := repo.GetConnection(ctx, "guard", userID, "guard-p")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if !saved.UpdatedAt.After(initial.UpdatedAt) || saved.ImportWatchedEnabled == saved.ScrobbleEnabled {
		t.Fatalf("concurrent preferences=%+v", saved)
	}
	// Background writers and reconnects must not restore a stale preference snapshot.
	initial.AccessToken = "refreshed"
	initial.LastError = "worker status"
	worker, err := repo.UpsertConnection(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	if worker.ImportWatchedEnabled != saved.ImportWatchedEnabled || worker.ScrobbleEnabled != saved.ScrobbleEnabled || worker.AccessToken != "refreshed" {
		t.Fatal("stale worker overwrote preferences or failed to update token")
	}
	version := ConnectionVersion{ID: worker.ID, UpdatedAt: worker.UpdatedAt}
	cleanupFailure := errors.New("order cleanup failed")
	_, err = repo.UpdateConnectionSettings(ctx, "guard", userID, "guard-p", &version, ConnectionUpdate{SyncWatchlistOrderEnabled: new(false)}, func(Connection) error { return cleanupFailure })
	if !errors.Is(err, cleanupFailure) {
		t.Fatalf("callback error=%v", err)
	}
	current, _, err := repo.GetConnection(ctx, "guard", userID, "guard-p")
	if err != nil || !current.SyncWatchlistOrderEnabled || !current.UpdatedAt.Equal(version.UpdatedAt) {
		t.Fatal("failed cleanup changed connection")
	}
	// Unguarded bridge settings updates still merge only their supplied fields.
	legacy, err := repo.UpdateConnectionSettings(ctx, "guard", userID, "guard-p", nil, ConnectionUpdate{ExportWatchedEnabled: new(true)}, nil)
	if err != nil || !legacy.ExportWatchedEnabled || legacy.ImportWatchedEnabled != saved.ImportWatchedEnabled || legacy.ScrobbleEnabled != saved.ScrobbleEnabled {
		t.Fatalf("legacy merge=%+v %v", legacy, err)
	}
	if err := repo.DeleteConnection(ctx, "guard", userID, "guard-p"); err != nil {
		t.Fatal(err)
	}
	recreated, err := repo.UpsertConnection(ctx, Connection{Provider: "guard", UserID: userID, ProfileID: "guard-p", AccessToken: "new", ScrobbleEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// Even if a timestamp is reused, an old resource generation cannot authorize it.
	if _, err := pool.Exec(ctx, "UPDATE watch_provider_connections SET updated_at=$2 WHERE id=$1", recreated.ID, expected.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = repo.UpdateConnectionSettings(ctx, "guard", userID, "guard-p", &expected, ConnectionUpdate{ScrobbleEnabled: new(false)}, func(Connection) error { called = true; return nil })
	if !errors.Is(err, ErrStaleConnection) || called {
		t.Fatalf("generation guard=%v effect=%v", err, called)
	}
	if _, err := repo.UpdateConnectionSettings(ctx, "guard", userID, "foreign", &expected, ConnectionUpdate{}, nil); !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("profile guard=%v", err)
	}
	// A user store sharing a one-connection pool cannot clear ordering while
	// the settings transaction holds that connection. Cancellation must release
	// the transaction without persisting the disable.
	ready, err := repo.UpdateConnectionSettings(ctx, "guard", userID, "guard-p", nil, ConnectionUpdate{SyncWatchlistOrderEnabled: new(true)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	smallPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer smallPool.Close()
	registry := NewRegistry()
	if err := registry.Register(guardProvider{}); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewPostgresRepository(smallPool, cipher), registry).WithUserStoreProvider(pgstore.NewPostgresProvider(smallPool))
	deadlineCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err = service.UpdateConnectionConditional(deadlineCtx, userID, "guard-p", "guard", ConnectionVersion{ID: ready.ID, UpdatedAt: ready.UpdatedAt}, ConnectionUpdate{SyncWatchlistOrderEnabled: new(false)})
	if !errors.Is(err, ErrSettingsCleanupUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool exhaustion error=%v", err)
	}
	unchanged, _, err := repo.GetConnection(ctx, "guard", userID, "guard-p")
	if err != nil || !unchanged.SyncWatchlistOrderEnabled || !unchanged.UpdatedAt.Equal(ready.UpdatedAt) {
		t.Fatalf("canceled settings changed: %+v %v", unchanged, err)
	}

}

type guardProvider struct{}

func (guardProvider) Key() string                { return "guard" }
func (guardProvider) DisplayName() string        { return "Guard" }
func (guardProvider) Capabilities() Capabilities { return Capabilities{} }
