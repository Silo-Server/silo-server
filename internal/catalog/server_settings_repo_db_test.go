package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestServerSettingsRepoGetManyReadsPresentKeys(t *testing.T) {
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

	prefix := fmt.Sprintf("test.get_many.%d.", time.Now().UnixNano())
	first, second, missing := prefix+"first", prefix+"second", prefix+"missing"
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, []string{first, second}); err != nil {
			t.Logf("clean up test settings: %v", err)
		}
	})
	repo := NewServerSettingsRepo(pool)
	if err := repo.SetMany(ctx, map[string]string{first: "one", second: ""}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetMany(ctx, first, second, missing)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[first] != "one" || got[second] != "" {
		t.Fatalf("GetMany = %v, want only the two stored keys", got)
	}
	if _, ok := got[missing]; ok {
		t.Fatalf("GetMany returned a key without a row: %v", got)
	}
}

func TestEncryptedSettingsValueAtomicSerializesWithoutBlockingGeneralSettingsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	appName := fmt.Sprintf("test.ai_credentials.%d", time.Now().UnixNano())
	cfg.ConnConfig.RuntimeParams["application_name"] = appName
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// A different pool represents another process and must still wait on the
	// PostgreSQL advisory lock, outside this process's pool admission slot.
	otherPool, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(otherPool.Close)
	raw := NewServerSettingsRepo(pool)
	store := NewEncryptedSettingsRepo(raw, newCipher(t))
	key := chatgpt.CredentialsKey
	other := appName + ".quota"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = ANY($1)`, []string{key, other})
	})
	if err := store.Set(ctx, key, "seed"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		firstDone <- store.UpdateValueAtomic(ctx, key, func(current string) (string, error) {
			if current != "seed" {
				t.Errorf("first mutation did not decrypt current value")
			}
			close(entered)
			select {
			case <-release:
				return "first", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		// A second repository represents another server sharing PostgreSQL.
		otherNode := NewEncryptedSettingsRepo(NewServerSettingsRepo(otherPool), store.cipher)
		secondDone <- otherNode.UpdateValueAtomic(ctx, key, func(current string) (string, error) {
			if current != "first" {
				t.Errorf("concurrent mutation read stale credentials")
			}
			return "second", nil
		})
	}()
	// Observe the second node waiting on the credentials lock before trying a
	// general settings write. No timing assumption decides whether it ran.
	for tick := time.Tick(10 * time.Millisecond); ; {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name = $1 AND wait_event = 'advisory')`, appName).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-tick:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	writeCtx, stopWrite := context.WithTimeout(ctx, time.Second)
	defer stopWrite()
	if err := raw.Set(writeCtx, other, "5"); err != nil {
		t.Fatalf("credentials callback blocked an unrelated setting: %v", err)
	}
	unblock()
	for _, done := range []chan error{firstDone, secondDone} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	plain, err := store.Get(ctx, key)
	if err != nil || plain != "second" {
		t.Fatal("serialized mutation did not preserve the latest credentials")
	}
	ciphertext, err := raw.Get(ctx, key)
	if err != nil || !secret.IsEncrypted(ciphertext) {
		t.Fatal("atomic mutation stored plaintext credentials")
	}
	commitCtx, stopCommit := context.WithCancel(ctx)
	if err := store.UpdateValueAtomic(commitCtx, key, func(string) (string, error) {
		stopCommit()
		return "after-cancel", nil
	}); err != nil {
		t.Fatalf("completed rotation was lost on caller cancellation: %v", err)
	}
	plain, err = store.Get(ctx, key)
	if err != nil || plain != "after-cancel" {
		t.Fatal("completed rotation was not committed")
	}
}

func TestSettingsValueWaitersPreservePoolCapacityDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	key := fmt.Sprintf("test.value_admission.%d", time.Now().UnixNano())
	other := key + ".other"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key=ANY($1)`, []string{key, other})
	})
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	holder := make(chan error, 1)
	go func() {
		holder <- NewServerSettingsRepo(pool).UpdateValueAtomic(ctx, key, func(string) (string, error) {
			close(entered)
			select {
			case <-release:
				return "fresh", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case err := <-holder:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	const waiters = 8
	waitCtx, stopWaiters := context.WithCancel(ctx)
	defer stopWaiters()
	done := make(chan error, waiters)
	for range waiters {
		go func() {
			done <- NewServerSettingsRepo(pool).UpdateValueAtomic(waitCtx, key, func(string) (string, error) {
				t.Error("canceled waiter reached database callback")
				return "wrong", nil
			})
		}()
	}
	// All wrappers have entered admission while the holder owns its database
	// connection; assert capacity and general writes before releasing it.
	for tick := time.Tick(time.Millisecond); ; {
		settingsValueSlots.Lock()
		slot := settingsValueSlots.active[settingsValueSlotKey{pool, key}]
		users := slot.users
		settingsValueSlots.Unlock()
		if users == waiters+1 {
			break
		}
		select {
		case <-tick:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 1 {
		t.Fatalf("same-key waiters consumed pool: acquired=%d", acquired)
	}
	writeCtx, stopWrite := context.WithTimeout(ctx, time.Second)
	defer stopWrite()
	if err := NewServerSettingsRepo(pool).Set(writeCtx, other, "independent"); err != nil {
		t.Fatalf("unrelated setting blocked: %v", err)
	}
	stopWaiters()
	for range waiters {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting cancellation: %v", err)
		}
	}
	// A canceled queue must leave the slot reusable and read the holder's commit.
	next := make(chan error, 1)
	go func() {
		next <- NewServerSettingsRepo(pool).UpdateValueAtomic(ctx, key, func(current string) (string, error) {
			if current != "fresh" {
				t.Errorf("serialized read=%q", current)
			}
			return "next", nil
		})
	}()
	unblock()
	for _, result := range []chan error{holder, next} {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	settingsValueSlots.Lock()
	_, retained := settingsValueSlots.active[settingsValueSlotKey{pool, key}]
	settingsValueSlots.Unlock()
	if retained {
		t.Fatal("idle admission retained pool and key")
	}
	// Cancellation must win even when the admission slot is immediately free.
	canceledCtx, stop := context.WithCancel(ctx)
	stop()
	err = NewServerSettingsRepo(pool).UpdateValueAtomic(canceledCtx, key, func(string) (string, error) {
		t.Error("already canceled request reached database callback")
		return "wrong", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled admission: %v", err)
	}
}
