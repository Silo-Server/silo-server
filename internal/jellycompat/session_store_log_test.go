package jellycompat

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// failingSessionRepo fails every persistence call whose flag is set. The
// error text echoes the token as a store error message could, so the test
// also covers the error being sanitized before it is logged.
type failingSessionRepo struct {
	failUpsert bool
	failGet    bool
	failDelete bool
}

func (r *failingSessionRepo) storeErr(op, token string) error {
	return errors.New(op + " failed: token=" + token + " password=store-secret")
}

func (r *failingSessionRepo) Upsert(_ context.Context, session Session) error {
	if r.failUpsert {
		return r.storeErr("upsert", session.Token)
	}
	return nil
}

func (r *failingSessionRepo) GetByToken(_ context.Context, token string, _ time.Time) (*Session, error) {
	if r.failGet {
		return nil, r.storeErr("load", token)
	}
	return nil, ErrSessionNotFound
}

func (r *failingSessionRepo) DeleteByToken(_ context.Context, token string) error {
	if r.failDelete {
		return r.storeErr("delete", token)
	}
	return nil
}

func TestSessionStoreFailureLogsOmitFullToken(t *testing.T) {
	const token = "abcdef0123456789abcdef0123456789"
	prefix := safeTokenPrefix(token)

	cases := []struct {
		name    string
		message string
		run     func(t *testing.T, repo *failingSessionRepo, store *SessionStore, advance func(time.Duration))
	}{
		{
			name:    "load",
			message: "jellycompat session store load failed",
			run: func(t *testing.T, repo *failingSessionRepo, store *SessionStore, _ func(time.Duration)) {
				repo.failGet = true
				if _, ok := store.Get(token); ok {
					t.Fatal("Get returned a session while the store was failing")
				}
			},
		},
		{
			name:    "extend",
			message: "jellycompat session store extend failed",
			run: func(t *testing.T, repo *failingSessionRepo, store *SessionStore, advance func(time.Duration)) {
				if err := store.Put(Session{Token: token, StreamAppUserID: 1}); err != nil {
					t.Fatalf("Put: %v", err)
				}
				repo.failUpsert = true
				advance(20 * time.Hour) // past half of the 24h TTL
				if _, ok := store.Get(token); !ok {
					t.Fatal("Get dropped a live session when the extend write failed")
				}
			},
		},
		{
			name:    "delete",
			message: "jellycompat session store delete failed",
			run: func(t *testing.T, repo *failingSessionRepo, store *SessionStore, _ func(time.Duration)) {
				repo.failDelete = true
				store.Delete(token)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })

			now := fixedNow()
			repo := &failingSessionRepo{}
			store := NewPersistentSessionStore(24*time.Hour, func() time.Time { return now }, repo)
			tc.run(t, repo, store, func(d time.Duration) { now = now.Add(d) })

			out := logs.String()
			if !strings.Contains(out, tc.message) {
				t.Fatalf("expected %q in log, got: %s", tc.message, out)
			}
			assertLogSecretsAbsent(t, out, token, "store-secret")
			for _, want := range []string{`"token_prefix":"` + prefix + `"`, `failed: token=[REDACTED]`} {
				if !strings.Contains(out, want) {
					t.Errorf("missing diagnostic %q: %s", want, out)
				}
			}
		})
	}
}
