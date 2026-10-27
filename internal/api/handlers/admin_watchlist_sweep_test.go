package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// watchlistSweepRecorder counts orphan-title sweeps and checks each runs on a
// live context.
type watchlistSweepRecorder struct {
	calls  int
	ctxErr error
}

func (r *watchlistSweepRecorder) SweepOrphanTitles(ctx context.Context) error {
	r.calls++
	r.ctxErr = ctx.Err()
	return nil
}

// Deleting an account drops its watchlist entries through the users foreign
// key; the delete then sweeps the titles left without entries. A refused
// delete sweeps nothing.
func TestAdminAccountDeleteSweepsWatchlistTitles(t *testing.T) {
	for _, tc := range []struct {
		name      string
		target    models.User
		wantErr   bool
		wantSweep int
	}{
		{name: "delete", target: userAccount(), wantSweep: 1},
		{name: "refused delete", target: adminAccount(), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sweeper := &watchlistSweepRecorder{}
			h := &AdminHandler{userRepo: &mutatingUserRepo{current: tc.target}, WatchlistTitlesSweeper: sweeper}
			if err := h.DeleteAdminAccount(claimsCtx(7), testAdminID, -1, 0); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if sweeper.calls != tc.wantSweep || sweeper.ctxErr != nil {
				t.Fatalf("sweeps = %d ctxErr = %v, want %d on a live context", sweeper.calls, sweeper.ctxErr, tc.wantSweep)
			}
		})
	}
}
