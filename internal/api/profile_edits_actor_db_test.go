package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type actorClaims struct{ claims auth.Claims }

func (v actorClaims) ValidateToken(string) (*auth.Claims, error) {
	c := v.claims
	return &c, nil
}

type actorSessions struct{ role string }

func (s actorSessions) ActiveSession(context.Context, string, int) (auth.SessionAuthority, bool, error) {
	return auth.SessionAuthority{Role: s.role}, true, nil
}

type actorAPIKeys struct{ key *models.APIKey }

func (k actorAPIKeys) GetByKey(context.Context, string) (*models.APIKey, error) { return k.key, nil }
func (actorAPIKeys) UpdateLastUsed(context.Context, int64) error                { return nil }

type actorUsers struct{ user *models.User }

func (u actorUsers) GetByID(context.Context, int) (*models.User, error) { return u.user, nil }

// TestProfileEditActorThroughNativeAuthDB: an edit made in a request the
// native base chain and auth admit is recorded as the signed-in account's.
// It catches auth no longer naming the actor (a login session, a session
// attached to a public route, an API key) or the edits middleware leaving
// the base chain, either of which silently stops recording.
func TestProfileEditActorThroughNativeAuthDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	userID, profileID := insertSyncedProfile(t, pool)
	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	user := &models.User{ID: userID, Role: models.RoleUser, Enabled: true}
	session := auth.Claims{UserID: userID, Role: models.RoleUser, SessionID: "sess", TokenType: auth.TokenTypeAccess}
	am := apimw.NewAuthMiddleware(actorClaims{session}, actorSessions{role: models.RoleUser},
		actorAPIKeys{key: &models.APIKey{ID: 1, UserID: userID, Key: "sa_test"}}, actorUsers{user: user})

	root := chi.NewRouter()
	useBaseMiddleware(root, Dependencies{})
	edit := func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("Edited %d", time.Now().UnixNano())
		if err := store.UpdateProfile(r.Context(), profileID, userstore.UpdateProfileInput{Name: &name}); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}
	root.With(am.RequireAuth).Post("/edit", edit)
	root.With(am.AttachSession).Post("/public-edit", edit)

	for _, tc := range []struct{ name, path, token string }{
		{"login session", "/edit", "Bearer token"},
		{"session attached to a public route", "/public-edit", "Bearer token"},
		{"API key", "/edit", "Bearer sa_test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `DELETE FROM profile_set_edits WHERE user_id = $1`, userID); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req.Header.Set("Authorization", tc.token)
			rec := httptest.NewRecorder()
			root.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			edits, err := profileedits.List(ctx, pool, userID)
			if err != nil {
				t.Fatal(err)
			}
			if len(edits) != 1 || edits[0].Field != profileedits.FieldName || edits[0].By != profileedits.ByAccount || edits[0].ActorUserID != userID {
				t.Fatalf("edits = %+v, want one account mark on name", edits)
			}
		})
	}
}

// insertSyncedProfile creates an account with one profile that has a sync
// ID, and removes both when the test ends.
func insertSyncedProfile(t *testing.T, pool *pgxpool.Pool) (int, string) {
	t.Helper()
	ctx := t.Context()
	name := fmt.Sprintf("edits-actor-%d", time.Now().UnixNano())
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, 'x', 'user') RETURNING id`,
		name, name+"@example.invalid").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM profile_set_edits WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	profileID := pgstore.NewProfileID()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Main", IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_profiles SET sync_id = gen_random_uuid(), sync_position = 0 WHERE user_id = $1 AND id = $2`,
		userID, profileID); err != nil {
		t.Fatal(err)
	}
	return userID, profileID
}
