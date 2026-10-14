package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// TestRequestRoutesFollowActingAdminDB: on the ordinary request routes, a
// child profile on an admin's account has no more reach than any other
// profile. It reads its own account's requests, as every household profile
// does, but neither reads nor cancels another account's, which the admin
// request queue already refuses it. The primary profile keeps admin reach.
func TestRequestRoutesFollowActingAdminDB(t *testing.T) {
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
	suffix := fmt.Sprintf("actadm%d", time.Now().UnixNano())

	users := auth.NewUserRepository(pool)
	stores := pgstore.NewPostgresProvider(pool)
	account := func(name, role string) int {
		t.Helper()
		u, err := users.Create(ctx, models.CreateUserInput{Username: name + "-" + suffix, Email: name + "-" + suffix + "@example.test",
			Password: "long enough password", Role: role})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup := context.WithoutCancel(ctx)
			_, _ = pool.Exec(cleanup, `DELETE FROM media_requests WHERE requested_by_user_id = $1`, u.ID)
			_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, u.ID)
		})
		return u.ID
	}
	profile := func(userID int, p userstore.Profile) string {
		t.Helper()
		store, err := stores.ForUser(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		p.ID = uuid.NewString()
		if err := store.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
		return p.ID
	}

	adminID := account("admin", models.RoleAdmin)
	pin, err := bcrypt.GenerateFromPassword([]byte("2468"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	parent := profile(adminID, userstore.Profile{Name: "Parent", IsPrimary: true, PINHash: string(pin)})
	kid := profile(adminID, userstore.Profile{Name: "Kid", IsChild: true})
	otherID := account("other", models.RoleUser)
	otherProfile := profile(otherID, userstore.Profile{Name: "Other", IsPrimary: true})

	cipher, err := secret.New([]byte("acting-admin-requests-test-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	repo := mediarequests.NewRepository(pool, cipher)
	settings, err := repo.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.RequestsEnabled {
		settings.RequestsEnabled = true
		if _, err := repo.UpdateSettings(ctx, settings); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			settings.RequestsEnabled = false
			_, _ = repo.UpdateSettings(context.WithoutCancel(ctx), settings)
		})
	}
	tmdbBase := int(time.Now().UnixNano()%1_000_000_000) + 1
	newRequest := func(requester mediarequests.Viewer, tmdbID int) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := repo.CreateRequest(ctx, mediarequests.CreateRequestRecord{
			ID:        id,
			Input:     mediarequests.CreateRequestInput{MediaType: mediarequests.MediaTypeMovie, TMDBID: tmdbID, Title: "Request " + suffix},
			Status:    mediarequests.StatusPending,
			Outcome:   mediarequests.OutcomeActive,
			Requester: requester,
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	othersRequest := newRequest(mediarequests.Viewer{UserID: otherID, ProfileID: otherProfile}, tmdbBase)
	householdRequest := newRequest(mediarequests.Viewer{UserID: adminID, ProfileID: parent}, tmdbBase+1)

	checkPrimary := func(ctx context.Context, userID int, profileID string) (bool, bool, error) {
		store, err := stores.ForUser(ctx, userID)
		if err != nil {
			return false, false, err
		}
		p, err := store.GetProfile(ctx, profileID)
		if err != nil || p == nil {
			return false, false, err
		}
		return p.IsPrimary, true, nil
	}
	household := func(ctx context.Context, userID int) (bool, error) {
		store, err := stores.ForUser(ctx, userID)
		if err != nil {
			return false, err
		}
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			return false, err
		}
		return access.HouseholdRequiresProfile(profiles), nil
	}
	decide := apimw.NewActingAdminDecider(checkPrimary, household)

	h := NewRequestsHandler(mediarequests.NewService(repo, nil, nil))
	router := chi.NewRouter()
	router.Use(apimw.ProvideActingAdmin(decide))
	// Stands in for authentication and RequireViewerAccess: the admin's
	// login session, acting as the profile it names, already verified.
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			profileID := r.Header.Get("X-Profile-Id")
			rctx := apimw.SetClaims(r.Context(), &auth.Claims{UserID: adminID, Role: models.RoleAdmin, TokenType: auth.TokenTypeAccess})
			rctx = access.SetScope(rctx, access.Scope{UserID: adminID, ProfileID: profileID, ProfileVerified: true})
			rctx = apimw.SetProfileID(rctx, profileID)
			next.ServeHTTP(w, r.WithContext(rctx))
		})
	})
	router.Get("/api/v1/requests/{id}", h.HandleGet)
	router.Post("/api/v1/requests/{id}/cancel", h.HandleCancel)
	router.With(apimw.RequireActingAdminWith(decide)).Post("/api/v1/admin/requests/{id}/cancel", h.HandleCancel)

	as := func(profileID, method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-Profile-Id", profileID)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	outcome := func(id string) string {
		var got string
		if err := pool.QueryRow(ctx, `SELECT outcome FROM media_requests WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := as(kid, http.MethodPost, "/api/v1/admin/requests/"+othersRequest+"/cancel"); got != http.StatusForbidden {
		t.Fatalf("admin queue cancel as the child profile: %d, want 403", got)
	}
	if got := as(kid, http.MethodGet, "/api/v1/requests/"+othersRequest); got != http.StatusForbidden {
		t.Errorf("child profile read another account's request: %d, want 403", got)
	}
	if got := as(kid, http.MethodPost, "/api/v1/requests/"+othersRequest+"/cancel"); got != http.StatusForbidden {
		t.Errorf("child profile cancel of another account's request: %d, want 403", got)
	}
	if got := outcome(othersRequest); got != string(mediarequests.OutcomeActive) {
		t.Fatalf("another account's request is now %q", got)
	}
	if got := as(kid, http.MethodGet, "/api/v1/requests/"+householdRequest); got != http.StatusOK {
		t.Errorf("child profile read its own account's request: %d, want 200", got)
	}

	if got := as(parent, http.MethodGet, "/api/v1/requests/"+othersRequest); got != http.StatusOK {
		t.Errorf("primary profile read another account's request: %d, want 200", got)
	}
	if got := as(parent, http.MethodPost, "/api/v1/requests/"+othersRequest+"/cancel"); got != http.StatusOK {
		t.Errorf("primary profile cancel of another account's request: %d, want 200", got)
	}
}
