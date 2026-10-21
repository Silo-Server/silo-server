package apiv2

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/onboarding"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
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
