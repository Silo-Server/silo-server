package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// These tests invoke the registered native routes and production profile,
// account and PostgreSQL stores. Only token validation and the scheduling/fault
// seam are replaced; no in-memory store can establish the quota invariant.
type profileQuotaRig struct {
	pool     *pgxpool.Pool
	provider *pgstore.PostgresProvider
	userID   int
	adminID  int
}

func newProfileQuotaRig(t *testing.T, cap int) *profileQuotaRig {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set; requires a migrated private PostgreSQL fixture")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rig := &profileQuotaRig{pool: pool, provider: pgstore.NewPostgresProvider(pool)}
	suffix := uuid.NewString()
	users := auth.NewUserRepository(pool)
	for _, seed := range []struct {
		role string
		id   *int
	}{{"user", &rig.userID}, {"admin", &rig.adminID}} {
		username := "profile-quota-" + seed.role + "-" + suffix
		user, err := users.Create(t.Context(), models.CreateUserInput{
			Username: username, Email: username + "@example.test", Password: "synthetic-password",
			Role: seed.role, MaxProfiles: new(cap),
		})
		if err != nil {
			t.Fatal(err)
		}
		*seed.id = user.ID
		id := *seed.id
		t.Cleanup(func() {
			ctx := context.WithoutCancel(t.Context())
			if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id); err != nil {
				t.Errorf("remove quota fixture account: %v", err)
			}
			for _, table := range []string{"users", "user_profiles", "user_setting_values", "user_watch_history"} {
				column := "user_id"
				if table == "users" {
					column = "id"
				}
				var remaining int
				if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+column+"=$1", id).Scan(&remaining); err != nil {
					t.Errorf("verify %s cleanup: %v", table, err)
				} else if remaining != 0 {
					t.Errorf("cleanup leaked %d %s rows", remaining, table)
				}
			}
		})
	}
	if _, err := users.GetByID(t.Context(), rig.userID); err != nil {
		t.Fatalf("read production account fixture: %v", err)
	}
	store, err := rig.provider.ForUser(t.Context(), rig.userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "p-primary", Name: "Parent"}); err != nil {
		t.Fatal(err)
	}
	return rig
}

func (rig *profileQuotaRig) handler(provider userstore.UserStoreProvider) http.Handler {
	users := auth.NewUserRepository(rig.pool)
	profiles := handlers.NewProfileHandler(provider)
	profiles.UserRepo = users
	deps := parityDeps(false)
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{claims: map[string]*auth.Claims{
		memberToken: {UserID: rig.userID, Role: "user", SessionID: "quota-user", TokenType: auth.TokenTypeAccess},
		adminToken:  {UserID: rig.adminID, Role: "admin", SessionID: "quota-admin", TokenType: auth.TokenTypeAccess},
	}}, fakeSessions{roles: map[string]string{"quota-user": "user", "quota-admin": "admin"}}, fakeAPIKeys{}, users)
	deps.Profiles = profiles
	admin := handlers.NewAdminHandler(users, rig.pool, rig.provider)
	admin.AccessGroups = access.NewGroupStore(rig.pool)
	deps.AdminAccounts = admin
	return NewHandler(deps)
}

type profileQuotaProvider struct {
	userstore.UserStoreProvider
	entered chan<- struct{}
	release <-chan struct{}
	fault   string
}

func (p profileQuotaProvider) ForUser(ctx context.Context, id int) (userstore.UserStore, error) {
	store, err := p.UserStoreProvider.ForUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return profileQuotaStore{UserStore: store, gate: p}, nil
}

type profileQuotaStore struct {
	userstore.UserStore
	gate profileQuotaProvider
}

func (s profileQuotaStore) WithPreferenceSettingsTransaction(ctx context.Context, fn func(userstore.PreferenceSettingsWriter) error) error {
	if s.gate.entered != nil {
		select {
		case s.gate.entered <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-s.gate.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.UserStore.(userstore.PreferenceSettingsTransactioner).WithPreferenceSettingsTransaction(ctx, func(tx userstore.PreferenceSettingsWriter) error {
		return fn(profileQuotaWriter{PreferenceSettingsWriter: tx, fault: s.gate.fault})
	})
}

type profileQuotaWriter struct {
	userstore.PreferenceSettingsWriter
	fault string
}

func (w profileQuotaWriter) CreateProfile(ctx context.Context, profile userstore.Profile) error {
	if err := w.PreferenceSettingsWriter.CreateProfile(ctx, profile); err != nil {
		return err
	}
	if w.fault == "profile" {
		return errors.New("injected after inserting profile")
	}
	return nil
}

func (w profileQuotaWriter) UpsertSettingValue(ctx context.Context, id userstore.SettingIdentity, value json.RawMessage) (*userstore.SettingValue, error) {
	stored, err := w.PreferenceSettingsWriter.UpsertSettingValue(ctx, id, value)
	if err == nil && w.fault == "canonical" {
		return nil, errors.New("injected after inserting canonical setting")
	}
	return stored, err
}

func waitProfileQuotaPreflight(t *testing.T, entered <-chan struct{}, responses <-chan *httptest.ResponseRecorder) {
	t.Helper()
	select {
	case <-entered:
	case rec := <-responses:
		t.Fatalf("profile request returned before quota preflight: %d %s", rec.Code, rec.Body.String())
	case <-time.After(10 * time.Second):
		t.Fatal("profile request did not reach the transaction after passing preflight")
	}
}

func requireProfileQuotaResponse(t *testing.T, responses <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-responses:
		return rec
	case <-time.After(10 * time.Second):
		t.Fatal("profile request did not complete after releasing quota preflight")
		return nil
	}
}

func requireProfileQuotaSnapshot(t *testing.T, rig *profileQuotaRig) string {
	t.Helper()
	var snapshot string
	if err := rig.pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
		'profiles', COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p WHERE user_id=$1), '[]'::jsonb),
		'settings', COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY profile_id, key) FROM user_setting_values s WHERE user_id=$1), '[]'::jsonb),
		'history', COALESCE((SELECT jsonb_agg(to_jsonb(h) ORDER BY id) FROM user_watch_history h WHERE user_id=$1), '[]'::jsonb))::text`, rig.userID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestProfileQuotaConcurrentNativeCreates(t *testing.T) {
	rig := newProfileQuotaRig(t, 2)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	releaseRequests := sync.OnceFunc(func() { close(release) })
	defer releaseRequests()
	provider := profileQuotaProvider{UserStoreProvider: rig.provider, entered: entered, release: release}
	responses := make(chan *httptest.ResponseRecorder, 2)
	for _, name := range []string{"Household A", "Household B"} {
		h := rig.handler(provider) // separate listeners, shared authoritative database
		go func() {
			responses <- do(t, h, http.MethodPost, "/api/v2/profiles", fmt.Sprintf(`{"name":%q}`, name), with(bearer(memberToken), "X-Profile-Id", "p-primary"))
		}()
	}
	waitProfileQuotaPreflight(t, entered, responses)
	waitProfileQuotaPreflight(t, entered, responses)
	releaseRequests()
	statuses := map[int]int{}
	for range 2 {
		rec := requireProfileQuotaResponse(t, responses)
		statuses[rec.Code]++
		if rec.Code == http.StatusConflict {
			requireProblem(t, rec, TypeConflict)
		}
	}
	if !reflect.DeepEqual(statuses, map[int]int{http.StatusCreated: 1, http.StatusConflict: 1}) {
		t.Fatalf("concurrent statuses = %v; want exactly one create and one quota conflict", statuses)
	}
	var profiles, primary int
	if err := rig.pool.QueryRow(t.Context(), `SELECT COUNT(*), COUNT(*) FILTER (WHERE is_primary) FROM user_profiles WHERE user_id=$1`, rig.userID).Scan(&profiles, &primary); err != nil {
		t.Fatal(err)
	}
	if profiles != 2 || primary != 1 {
		t.Fatalf("committed profiles=%d, primary=%d; want 2,1", profiles, primary)
	}
}

func TestProfileQuotaDowngradeBlocksStaleNativeCreateAndPreservesHistory(t *testing.T) {
	rig := newProfileQuotaRig(t, 3)
	store, err := rig.provider.ForUser(t.Context(), rig.userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "p-child", Name: "Child"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.pool.Exec(t.Context(), `INSERT INTO user_watch_history(id,user_id,profile_id,media_item_id) VALUES('quota-history',$1,'p-child','synthetic-movie')`, rig.userID); err != nil {
		t.Fatal(err)
	}
	before := requireProfileQuotaSnapshot(t, rig)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	releaseRequests := sync.OnceFunc(func() { close(release) })
	defer releaseRequests()
	h := rig.handler(profileQuotaProvider{UserStoreProvider: rig.provider, entered: entered, release: release})
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responses <- do(t, h, http.MethodPost, "/api/v2/profiles", `{"name":"Another member"}`, with(bearer(memberToken), "X-Profile-Id", "p-primary"))
	}()
	waitProfileQuotaPreflight(t, entered, responses)
	admin := rig.handler(rig.provider)
	path := fmt.Sprintf("/api/v2/admin/users/%d", rig.userID)
	get := do(t, admin, http.MethodGet, path, "", bearer(adminToken))
	if get.Code != http.StatusOK || get.Header().Get("ETag") == "" {
		t.Fatalf("read account precondition: %d %s", get.Code, get.Body.String())
	}
	updated := do(t, admin, http.MethodPut, path, `{"max_profiles":1}`, with(bearer(adminToken), "If-Match", get.Header().Get("ETag")))
	if updated.Code != http.StatusNoContent {
		t.Fatalf("downgrade profile cap: %d %s", updated.Code, updated.Body.String())
	}
	releaseRequests()
	requireProblem(t, requireProfileQuotaResponse(t, responses), TypeConflict)
	if after := requireProfileQuotaSnapshot(t, rig); after != before {
		t.Fatalf("downgrade or refused create changed retained household state\nbefore: %s\nafter: %s", before, after)
	}
	// A later request is refused too; an over-cap household remains readable.
	requireProblem(t, do(t, admin, http.MethodPost, "/api/v2/profiles", `{"name":"Later member"}`, with(bearer(memberToken), "X-Profile-Id", "p-primary")), TypeConflict)
	listed := do(t, admin, http.MethodGet, "/api/v2/profiles", "", bearer(memberToken))
	if listed.Code != http.StatusOK {
		t.Fatalf("over-cap household read: %d %s", listed.Code, listed.Body.String())
	}
}

func TestProfileQuotaNativeCreateRollback(t *testing.T) {
	for _, boundary := range []string{"profile", "canonical"} {
		t.Run(boundary, func(t *testing.T) {
			rig := newProfileQuotaRig(t, 2)
			before := requireProfileQuotaSnapshot(t, rig)
			broken := rig.handler(profileQuotaProvider{UserStoreProvider: rig.provider, fault: boundary})
			requireProblem(t, do(t, broken, http.MethodPost, "/api/v2/profiles", `{"name":"Child","language":"en"}`, with(bearer(memberToken), "X-Profile-Id", "p-primary")), TypeInternalError)
			if after := requireProfileQuotaSnapshot(t, rig); after != before {
				t.Fatalf("fault after %s changed household state\nbefore: %s\nafter: %s", boundary, before, after)
			}
			// A rollback must release the lock and restore the available slot.
			rec := do(t, rig.handler(rig.provider), http.MethodPost, "/api/v2/profiles", `{"name":"Child","language":"en"}`, with(bearer(memberToken), "X-Profile-Id", "p-primary"))
			if rec.Code != http.StatusCreated {
				t.Fatalf("create after rollback: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
