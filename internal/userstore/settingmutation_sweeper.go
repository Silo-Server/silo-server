package userstore

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// UserLister enumerates login accounts for cross-user maintenance sweeps.
// Satisfied by *auth.UserRepository.
type UserLister interface {
	List(ctx context.Context) ([]*models.User, error)
}

// SettingMutationSweepStats summarizes one retention sweep.
type SettingMutationSweepStats struct {
	// UsersSwept counts users whose store was swept successfully.
	UsersSwept int `json:"users_swept"`
	// UsersFailed counts users skipped because their store could not be opened
	// or swept. The sweep continues past them; the next run picks them up.
	UsersFailed int `json:"users_failed"`
	// ReceiptsDeleted is the total number of expired receipts removed.
	ReceiptsDeleted int64 `json:"receipts_deleted"`
}

// SettingMutationSweeper deletes expired setting-mutation idempotency receipts
// from every user's store. A receipt's expires_at is not self-enforcing —
// PutSettingMutation only records it — so without this sweep receipts
// accumulate forever.
type SettingMutationSweeper struct {
	users  UserLister
	stores UserStoreProvider
	logger *slog.Logger
}

// NewSettingMutationSweeper wires the sweeper.
func NewSettingMutationSweeper(users UserLister, stores UserStoreProvider) *SettingMutationSweeper {
	return &SettingMutationSweeper{
		users:  users,
		stores: stores,
		logger: slog.Default().With("component", "userstore.setting_mutation_sweep"),
	}
}

// Sweep removes every receipt that has expired as of now, one user store at a
// time. A user whose store fails to open or sweep is logged and skipped so one
// broken store cannot stall retention for everyone else; nothing is
// checkpointed because the delete is idempotent and the next run repairs any
// user this one missed.
func (s *SettingMutationSweeper) Sweep(ctx context.Context, progress func(percent int, message string)) (SettingMutationSweepStats, error) {
	report := func(percent int, message string) {
		if progress != nil {
			progress(percent, message)
		}
	}
	var stats SettingMutationSweepStats
	if s == nil || s.users == nil || s.stores == nil {
		return stats, fmt.Errorf("setting mutation sweep requires a user lister and a store provider")
	}

	users, err := s.users.List(ctx)
	if err != nil {
		return stats, fmt.Errorf("list users: %w", err)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })

	cutoff := time.Now().UTC()
	for idx, user := range users {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		store, err := s.stores.ForUser(ctx, user.ID)
		if err != nil {
			s.logger.WarnContext(ctx, "setting mutation sweep: open user store failed", "user_id", user.ID, "error", err)
			stats.UsersFailed++
			continue
		}
		deleted, err := store.DeleteExpiredSettingMutations(ctx, cutoff)
		if err != nil {
			if ctx.Err() != nil {
				return stats, ctx.Err()
			}
			s.logger.WarnContext(ctx, "setting mutation sweep: user sweep failed", "user_id", user.ID, "error", err)
			stats.UsersFailed++
			continue
		}
		stats.UsersSwept++
		stats.ReceiptsDeleted += deleted
		report((idx+1)*100/max(len(users), 1),
			fmt.Sprintf("Swept %d expired receipts across %d users", stats.ReceiptsDeleted, stats.UsersSwept))
	}
	s.logger.InfoContext(ctx, "setting mutation sweep completed",
		"users", stats.UsersSwept, "failed", stats.UsersFailed, "receipts_deleted", stats.ReceiptsDeleted)
	return stats, nil
}
