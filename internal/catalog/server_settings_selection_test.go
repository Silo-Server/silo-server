package catalog

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *memSettings) UpdateValueAndSettingsAtomic(ctx context.Context, _ string, update func(map[string]string) (map[string]string, error)) error {
	return s.UpdateAtomic(ctx, update)
}

func TestEncryptedSelectionWritesCredentialsAndModelTogether(t *testing.T) {
	raw := newMemSettings()
	store := NewEncryptedSettingsRepo(raw, newCipher(t))
	for key, value := range map[string]string{chatgpt.CredentialsKey: "old", "ai.chat_model": "old-model"} {
		if err := store.Set(t.Context(), key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateValueAndSettingsAtomic(t.Context(), chatgpt.CredentialsKey, func(values map[string]string) (map[string]string, error) {
		if values[chatgpt.CredentialsKey] != "old" || values["ai.chat_model"] != "old-model" {
			t.Fatal("callback did not read plaintext current state")
		}
		return map[string]string{chatgpt.CredentialsKey: "new", "ai.chat_model": "new-model"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !secret.IsEncrypted(raw.m[chatgpt.CredentialsKey]) || raw.m["ai.chat_model"] != "new-model" {
		t.Fatal("coordinated writes lost encryption or model")
	}
}

func TestEncryptedSelectionUnreadableReceiptPreservesCredentialCommit(t *testing.T) {
	for _, key := range []string{config.StorageTransitionTargetKey, "ai.api_key"} {
		t.Run(key, func(t *testing.T) {
			raw := newMemSettings()
			store := NewEncryptedSettingsRepo(raw, newCipher(t))
			for name, value := range map[string]string{chatgpt.CredentialsKey: "old", "ai.chat_model": "old-model"} {
				if err := store.Set(t.Context(), name, value); err != nil {
					t.Fatal(err)
				}
			}
			const corrupt = "enc:v1:invalid-envelope"
			raw.m[key] = corrupt
			previousCredential := raw.m[chatgpt.CredentialsKey]
			called := false
			err := store.UpdateValueAndSettingsAtomic(t.Context(), chatgpt.CredentialsKey, func(values map[string]string) (map[string]string, error) {
				called = true
				if _, present := values[key]; present {
					t.Fatal("unreadable receipt reached the selection callback")
				}
				return map[string]string{chatgpt.CredentialsKey: "accepted-replacement", "ai.chat_model": "selected-model"}, nil
			})
			if raw.m[key] != corrupt {
				t.Fatal("coordinated update changed the unreadable row")
			}
			if key != config.StorageTransitionTargetKey {
				if err == nil || called || raw.m[chatgpt.CredentialsKey] != previousCredential || raw.m["ai.chat_model"] != "old-model" {
					t.Fatal("unreadable active setting did not prevent the entire update")
				}
				return
			}
			if err != nil || !called {
				t.Fatalf("unreadable recovery receipt discarded accepted credentials: %v", err)
			}
			credentials, err := store.Get(t.Context(), chatgpt.CredentialsKey)
			if err != nil || credentials != "accepted-replacement" || !secret.IsEncrypted(raw.m[chatgpt.CredentialsKey]) || raw.m["ai.chat_model"] != "selected-model" {
				t.Fatal("accepted credentials and selected model were not committed")
			}
			if _, err := store.Get(t.Context(), key); err == nil {
				t.Fatal("receipt owner lost its decryption error")
			}
		})
	}
}

func TestSelectionUsesOneConnectionAndWaitsBeforeGeneralLockDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("test.selection.%d", time.Now().UnixNano())
	newPool := func(name string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.MaxConns = 1
		cfg.ConnConfig.RuntimeParams["application_name"] = name
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	holderPool, selectionPool, generalPool := newPool(prefix+".holder"), newPool(prefix+".selection"), newPool(prefix+".general")
	key, modelKey := prefix+".credentials", prefix+".model"
	t.Cleanup(func() {
		_, _ = generalPool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = ANY($1)`, []string{key, modelKey})
	})
	if err := NewServerSettingsRepo(generalPool).SetMany(ctx, map[string]string{key: "old", modelKey: "old-model"}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- NewServerSettingsRepo(holderPool).UpdateValueAtomic(ctx, key, func(string) (string, error) {
			close(entered)
			select {
			case <-release:
				return "rotated", nil
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
	selectionDone := make(chan error, 1)
	go func() {
		selectionDone <- NewServerSettingsRepo(selectionPool).UpdateValueAndSettingsAtomic(ctx, key, func(values map[string]string) (map[string]string, error) {
			if values[key] != "rotated" || values[modelKey] != "concurrent-model" {
				t.Error("selection read a stale credential/settings snapshot")
			}
			return map[string]string{key: "selected", modelKey: "selected-model"}, nil
		})
	}()
	for tick := time.Tick(10 * time.Millisecond); ; {
		var waiting bool
		if err := generalPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory')`, prefix+".selection").Scan(&waiting); err != nil {
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
	if err := NewServerSettingsRepo(generalPool).Set(writeCtx, modelKey, "concurrent-model"); err != nil {
		t.Fatalf("selection blocked settings while waiting for refresh: %v", err)
	}
	unblock()
	for _, done := range []chan error{holderDone, selectionDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	values, err := NewServerSettingsRepo(generalPool).GetMany(ctx, key, modelKey)
	if err != nil || values[key] != "selected" || values[modelKey] != "selected-model" {
		t.Fatalf("committed selection=%v,%v", values, err)
	}
}
