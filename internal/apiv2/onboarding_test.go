package apiv2

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/onboarding"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testOnboardingStore(t *testing.T, store userstore.UserStore) {
	t.Helper()
	progress := store.(userstore.OnboardingProgressStore)
	ctx := t.Context()
	base, err := progress.ReadOnboardingProgress(ctx, "p-owner", onboarding.TourID)
	if err != nil || base.Revision != 0 {
		t.Fatal(base, err)
	}
	state := userstore.OnboardingState{ProfileID: "p-owner", TourID: onboarding.TourID, LastStep: "welcome", UpdatedAt: "2026-01-01T00:00:00Z"}
	// Both initial writes race with the same witness; exactly one may insert.
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, e := progress.SaveOnboardingProgress(ctx, state, 0); results <- e })
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for e := range results {
		if e == nil {
			successes++
		} else if errors.Is(e, userstore.ErrOnboardingRevision) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	first, err := progress.ReadOnboardingProgress(ctx, state.ProfileID, state.TourID)
	if err != nil || first.Revision != 1 {
		t.Fatal(first, err)
	}
	state.LastStep = "playback"
	state.CompletedAt = "2026-01-01T00:01:00Z"
	second, err := progress.SaveOnboardingProgress(ctx, state, 1)
	if err != nil || second.Revision != 2 {
		t.Fatal(second, err)
	}
	state.LastStep = "welcome"
	state.CompletedAt = ""
	if _, err := progress.SaveOnboardingProgress(ctx, state, 1); !errors.Is(err, userstore.ErrOnboardingRevision) {
		t.Fatal("stale write", err)
	}
	// The unchanged bridge writer invalidates a validator while keeping terminal state.
	if err := store.UpsertOnboardingState(ctx, state); err != nil {
		t.Fatal(err)
	}
	third, err := progress.ReadOnboardingProgress(ctx, state.ProfileID, state.TourID)
	if err != nil || third.Revision != 3 || third.CompletedAt == "" {
		t.Fatal(third, err)
	}
	if _, err := progress.SaveOnboardingProgress(ctx, state, 2); !errors.Is(err, userstore.ErrOnboardingRevision) {
		t.Fatal("bridge did not fence", err)
	}
	other, err := progress.ReadOnboardingProgress(ctx, "other-profile", state.TourID)
	if err != nil || other.Revision != 0 {
		t.Fatal(other, err)
	}
	// Competing updates must also consume their shared revision only once.
	results = make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, e := progress.SaveOnboardingProgress(ctx, state, 3); results <- e })
	}
	wg.Wait()
	close(results)
	successes, conflicts = 0, 0
	for e := range results {
		if e == nil {
			successes++
		} else if errors.Is(e, userstore.ErrOnboardingRevision) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
}
func TestOnboardingSQLiteAndTransport(t *testing.T) {
	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "onboarding.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := userdb.NewSQLiteUserStore(db.DB)
	testOnboardingStore(t, store)
	deps := pilotDeps(nil, nil)
	deps.Onboarding = handlers.NewOnboardingHandler(notifications.WrapUserStoreProvider(collectionGuardHTTPProvider{stores: map[int]userstore.UserStore{1: store}}, &notifications.System{}), onboarding.Gates{})
	h := newTestHandler(t, deps)
	read := do(t, h, "GET", Prefix+"/onboarding/state", "", profileOwner())
	if read.Code != 200 {
		t.Fatal(read.Code, read.Body)
	}
	tag := read.Header().Get("ETag")
	if tag == "" {
		t.Fatal("missing ETag")
	}
	conditional := do(t, h, "GET", Prefix+"/onboarding/state", "", with(profileOwner(), "If-None-Match", tag))
	if conditional.Code != 304 || conditional.Body.Len() != 0 {
		t.Fatal(conditional.Code, conditional.Body)
	}
	body := `{"tour_id":"` + onboarding.TourID + `","last_step":"notifications"}`
	requireProblem(t, do(t, h, "PUT", Prefix+"/onboarding/progress", body, profileOwner()), TypePreconditionRequired)
	write := do(t, h, "PUT", Prefix+"/onboarding/progress", body, with(profileOwner(), "If-Match", tag))
	if write.Code != 200 || write.Header().Get("ETag") == tag {
		t.Fatal(write.Code, write.Body)
	}
	requireProblem(t, do(t, h, "PUT", Prefix+"/onboarding/progress", body, with(profileOwner(), "If-Match", tag)), TypePreconditionFailed)
	requireProblem(t, do(t, h, "PUT", Prefix+"/onboarding/progress", body, with(profileOwner(), "If-Match", "*")), TypeMalformedRequest)
	requireProblem(t, do(t, h, "GET", Prefix+"/onboarding/state", "", bearer(memberToken)), TypeValidationFailed)
	// The tag is scoped to the active account/profile, not just the same numeric revision.

}

func TestOnboardingPostgresDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	// A private schema lets two connections race without touching shared tables.
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema := "onboarding_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// Quote the generated identifier rather than interpolating arbitrary SQL input.
	schema = `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := pool.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(t.Context(), "DROP SCHEMA "+schema+" CASCADE") }()
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "SET search_path TO "+schema+",public"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `CREATE TABLE user_profile_onboarding(user_id INTEGER NOT NULL,profile_id TEXT NOT NULL,tour_id TEXT NOT NULL,last_step TEXT NOT NULL DEFAULT '',completed_at TEXT,skipped_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(user_id,profile_id,tour_id))`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906041154_guard_onboarding_progress.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(migration), "-- +goose Down")[0]
	if _, err := conn.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	// Open a new pool whose every connection uses the fixture schema.
	scoped := cfg.Copy()
	scoped.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	fixture, err := pgxpool.NewWithConfig(t.Context(), scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	store, err := pgstore.NewPostgresProvider(fixture).ForUser(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	testOnboardingStore(t, store)
	deps := pilotDeps(nil, nil)
	provider := notifications.WrapUserStoreProvider(collectionGuardHTTPProvider{stores: map[int]userstore.UserStore{1: store}}, &notifications.System{})
	deps.Onboarding = handlers.NewOnboardingHandler(provider, onboarding.Gates{})
	h := newTestHandler(t, deps)
	read := do(t, h, "GET", Prefix+"/onboarding/state", "", profileOwner())
	if read.Code != 200 {
		t.Fatalf("wrapped PostgreSQL onboarding: %d %s", read.Code, read.Body)
	}
	write := do(t, h, "PUT", Prefix+"/onboarding/progress", `{"tour_id":"`+onboarding.TourID+`","last_step":"playback"}`, with(profileOwner(), "If-Match", read.Header().Get("ETag")))
	if write.Code != 200 {
		t.Fatalf("wrapped PostgreSQL progress: %d %s", write.Code, write.Body)
	}

	other, err := pgstore.NewPostgresProvider(fixture).ForUser(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	state, err := other.(userstore.OnboardingProgressStore).ReadOnboardingProgress(t.Context(), "p-owner", onboarding.TourID)
	if err != nil || state.Revision != 0 {
		t.Fatal(state, err)
	}
}
