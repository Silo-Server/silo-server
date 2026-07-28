package userstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type staticUserLister struct {
	users []*models.User
	err   error
}

func (l staticUserLister) List(context.Context) ([]*models.User, error) {
	return l.users, l.err
}

type mapStoreProvider struct {
	stores map[int]userstore.UserStore
}

func (p mapStoreProvider) ForUser(_ context.Context, userID int) (userstore.UserStore, error) {
	store, ok := p.stores[userID]
	if !ok {
		return nil, errors.New("no store for user")
	}
	return store, nil
}

func (p mapStoreProvider) Close() error { return nil }

func newSweeperTestStore(t *testing.T) userstore.UserStore {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return userdb.NewSQLiteUserStore(db)
}

func putReceipt(t *testing.T, store userstore.UserStore, mutationID string, expiresAt time.Time) {
	t.Helper()
	if _, _, err := store.PutSettingMutation(context.Background(), userstore.SettingMutationRecord{
		MutationID:  mutationID,
		RequestHash: "hash-" + mutationID,
		Result:      json.RawMessage(`{"status":"applied"}`),
		ExpiresAt:   expiresAt,
	}); err != nil {
		t.Fatalf("PutSettingMutation(%s): %v", mutationID, err)
	}
}

// TestSettingMutationSweeperDeletesExpiredReceipts pins the retention
// invariant: a sweep removes exactly the receipts whose expires_at has passed,
// in every user's store, and leaves unexpired receipts replayable.
func TestSettingMutationSweeperDeletesExpiredReceipts(t *testing.T) {
	ctx := context.Background()
	storeOne := newSweeperTestStore(t)
	storeTwo := newSweeperTestStore(t)

	now := time.Now().UTC()
	putReceipt(t, storeOne, "user1-expired", now.Add(-time.Hour))
	putReceipt(t, storeOne, "user1-fresh", now.Add(30*24*time.Hour))
	putReceipt(t, storeTwo, "user2-expired-a", now.Add(-48*time.Hour))
	putReceipt(t, storeTwo, "user2-expired-b", now.Add(-time.Minute))
	putReceipt(t, storeTwo, "user2-fresh", now.Add(time.Hour))

	sweeper := userstore.NewSettingMutationSweeper(
		staticUserLister{users: []*models.User{{ID: 1}, {ID: 2}}},
		mapStoreProvider{stores: map[int]userstore.UserStore{1: storeOne, 2: storeTwo}},
	)

	var lastMessage string
	stats, err := sweeper.Sweep(ctx, func(_ int, message string) { lastMessage = message })
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.UsersSwept != 2 || stats.UsersFailed != 0 || stats.ReceiptsDeleted != 3 {
		t.Fatalf("stats = %+v, want 2 users swept, 0 failed, 3 receipts deleted", stats)
	}
	if lastMessage == "" {
		t.Fatal("Sweep reported no progress")
	}

	for _, gone := range []struct {
		store userstore.UserStore
		id    string
	}{
		{storeOne, "user1-expired"},
		{storeTwo, "user2-expired-a"},
		{storeTwo, "user2-expired-b"},
	} {
		if got, err := gone.store.GetSettingMutation(ctx, gone.id); err != nil || got != nil {
			t.Fatalf("GetSettingMutation(%s) = %+v (%v), want swept", gone.id, got, err)
		}
	}
	for _, kept := range []struct {
		store userstore.UserStore
		id    string
	}{
		{storeOne, "user1-fresh"},
		{storeTwo, "user2-fresh"},
	} {
		if got, err := kept.store.GetSettingMutation(ctx, kept.id); err != nil || got == nil {
			t.Fatalf("GetSettingMutation(%s) = %+v (%v), want the unexpired receipt", kept.id, got, err)
		}
	}
}

// TestSettingMutationSweeperSkipsFailedStores pins the skip-on-error behavior:
// a user whose store cannot be opened is counted as failed and does not stop
// the sweep from reaching the users after it.
func TestSettingMutationSweeperSkipsFailedStores(t *testing.T) {
	ctx := context.Background()
	store := newSweeperTestStore(t)
	putReceipt(t, store, "expired", time.Now().UTC().Add(-time.Hour))

	sweeper := userstore.NewSettingMutationSweeper(
		staticUserLister{users: []*models.User{{ID: 1}, {ID: 2}}},
		mapStoreProvider{stores: map[int]userstore.UserStore{2: store}},
	)

	stats, err := sweeper.Sweep(ctx, nil)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if stats.UsersSwept != 1 || stats.UsersFailed != 1 || stats.ReceiptsDeleted != 1 {
		t.Fatalf("stats = %+v, want 1 swept, 1 failed, 1 deleted", stats)
	}
}

// TestSettingMutationSweeperListFailure pins that a user-listing failure is an
// error rather than a silent no-op sweep.
func TestSettingMutationSweeperListFailure(t *testing.T) {
	sweeper := userstore.NewSettingMutationSweeper(
		staticUserLister{err: errors.New("database unavailable")},
		mapStoreProvider{},
	)
	if _, err := sweeper.Sweep(context.Background(), nil); err == nil {
		t.Fatal("Sweep must report user listing failures")
	}
}
