package watchsync

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestConnectionTokenUpdatesDB(t *testing.T) {
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
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "token-refresh-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	profileID := uuid.NewString()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,$2,'Token refresh')", userID, profileID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("token-refresh-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	now := time.Now().UTC().Truncate(time.Second)

	for _, change := range []string{"unchanged", "disconnect", "delete and recreate", "reconnect", "account switch", "sync state"} {
		t.Run(change, func(t *testing.T) {
			conn, err := repo.UpsertConnection(ctx, Connection{
				Provider: "trakt", UserID: userID, ProfileID: profileID, ProviderAccountID: "old-account",
				AccessToken: testOldAccessToken, RefreshToken: testOldRefreshToken, TokenExpiresAt: new(now.Add(-time.Minute)),
				ExportWatchedEnabled: true, ScrobbleEnabled: true, LastError: "old error",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = repo.DeleteConnection(ctx, conn.Provider, userID, profileID) }()
			provider := &authProviderStub{refreshTokens: TokenSet{
				AccessToken: "new-access", RefreshToken: "new-refresh", TokenExpiresAt: new(now.Add(time.Hour)),
			}}
			service := NewService(repo, NewRegistry())
			service.now = func() time.Time { return now }
			provider.beforeRefresh = func(ctx context.Context, _ Connection) error {
				if change == "disconnect" || change == "delete and recreate" {
					if err := repo.DeleteConnection(ctx, conn.Provider, userID, profileID); err != nil {
						return err
					}
					if _, exists, err := repo.GetConnectionByID(ctx, conn.ID); err != nil || exists {
						t.Fatalf("disconnect did not remove the row: exists=%v err=%v", exists, err)
					}
					if change == "disconnect" {
						return nil
					}
				}
				current := conn
				switch change {
				case "reconnect":
					current.AccessToken, current.RefreshToken = "reconnected-access", "reconnected-refresh"
				case "delete and recreate":
					current.ID = ""
					current.AccessToken, current.RefreshToken = "reconnected-access", "reconnected-refresh"
				case "account switch":
					current.ProviderAccountID = "new-account"
				case "sync state":
					_, err := pool.Exec(ctx, `UPDATE watch_provider_connections SET
						provider_username='current-name', scrobble_enabled=false,
						last_outbound_sync_at=$2, sync_cursors='{"trakt.watched":"new-cursor"}',
						rate_limited_until=$3 WHERE id=$1::uuid`, conn.ID, now, now.Add(time.Hour))
					return err
				default:
					return nil
				}
				_, err := repo.UpsertConnection(ctx, current)
				return err
			}
			refreshed, err := service.refreshConnectionIfNeeded(ctx, provider, ServerConfig{}, conn)
			current, exists, readErr := repo.GetConnection(ctx, conn.Provider, userID, profileID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			switch change {
			case "disconnect":
				if exists || !errors.Is(err, ErrConnectionNotFound) {
					t.Fatalf("disconnect lost: exists=%v err=%v", exists, err)
				}
			case "delete and recreate":
				if !errors.Is(err, ErrConnectionNotFound) || !exists || current.ID == conn.ID || current.AccessToken != "reconnected-access" {
					t.Fatalf("new connection generation changed: id=%s err=%v", current.ID, err)
				}
			case "reconnect":
				if !errors.Is(err, ErrStaleConnection) || current.AccessToken != "reconnected-access" {
					t.Fatalf("reconnect lost: access=%q err=%v", current.AccessToken, err)
				}
			case "account switch":
				if !errors.Is(err, ErrStaleConnection) || current.ProviderAccountID != "new-account" || current.AccessToken != testOldAccessToken {
					t.Fatalf("account switch lost: account=%q err=%v", current.ProviderAccountID, err)
				}
			case "sync state", "unchanged":
				if err != nil || current.AccessToken != "new-access" || current.RefreshToken != "new-refresh" || current.LastError != "" {
					t.Fatalf("refresh failed: access=%q err=%v", current.AccessToken, err)
				}
				var storedAccess, storedRefresh string
				if err := pool.QueryRow(ctx, "SELECT access_token,refresh_token FROM watch_provider_connections WHERE id=$1::uuid", conn.ID).Scan(&storedAccess, &storedRefresh); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(storedAccess, "enc:v1:") || !strings.HasPrefix(storedRefresh, "enc:v1:") {
					t.Fatal("rotated tokens were stored in plaintext")
				}
				if change == "sync state" && (current.ScrobbleEnabled || current.ProviderUsername != "current-name" || current.LastOutboundSyncAt == nil || current.SyncCursors["trakt.watched"] != "new-cursor" || current.RateLimitedUntil == nil || refreshed.ProviderUsername != current.ProviderUsername) {
					t.Fatal("refresh overwrote concurrent state or returned stale metadata")
				}
			}
		})
	}

	t.Run("authoritative plugin credentials", func(t *testing.T) {
		conn, err := repo.UpsertConnection(ctx, Connection{
			Provider: "plugin:1:tracker", UserID: userID, ProfileID: profileID,
			AccessToken: testOldAccessToken, RefreshToken: testOldRefreshToken, TokenExpiresAt: new(now),
			TokenType: testDPoPTokenType, Scopes: []string{"history"}, SecretAttributes: map[string]string{"proof": "old"},
		})
		if err != nil {
			t.Fatal(err)
		}
		updated := connectionWithTokens(conn, TokenSet{AccessToken: testRotatedAccessToken, TokenType: testBearerTokenType})
		if _, err := repo.UpdateConnectionTokens(ctx, conn, updated); err != nil {
			t.Fatal(err)
		}
		current, exists, err := repo.GetConnectionByID(ctx, conn.ID)
		if err != nil || !exists || current.AccessToken != testRotatedAccessToken || current.RefreshToken != "" || current.TokenExpiresAt != nil || current.TokenType != testBearerTokenType || len(current.Scopes) != 0 || len(current.SecretAttributes) != 0 {
			t.Fatalf("plugin credential bundle was not replaced: err=%v exists=%v", err, exists)
		}
	})
}
