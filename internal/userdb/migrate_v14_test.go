package userdb

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Completed/skipped are monotonic: a later progress write must not clear an
// earlier completion (finishing on one device, then opening on another).
func TestOnboardingCompletionIsMonotonic(t *testing.T) {
	db := newSchemaDB(t)

	store := NewSQLiteUserStore(db)
	ctx := context.Background()

	if err := store.UpsertOnboardingState(ctx, userstore.OnboardingState{
		ProfileID: "p1", TourID: "t", LastStep: "end", CompletedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := store.UpsertOnboardingState(ctx, userstore.OnboardingState{
		ProfileID: "p1", TourID: "t", LastStep: "welcome",
	}); err != nil {
		t.Fatalf("later progress write: %v", err)
	}

	state, err := store.GetOnboardingState(ctx, "p1", "t")
	if err != nil || state == nil {
		t.Fatalf("read back: state=%v err=%v", state, err)
	}
	if state.CompletedAt == "" {
		t.Error("completion cleared by a later progress write")
	}
	if state.LastStep != "welcome" {
		t.Errorf("last_step = %q, want welcome", state.LastStep)
	}
}
