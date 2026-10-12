// Package worker provides background processing workers for Silo.
package worker

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RefreshCandidate identifies a metadata target ready for scheduled refresh.
type RefreshCandidate struct {
	TargetType string
	ContentID  string
	// ClaimedAt identifies the claim that returned this candidate, so the
	// claim can be renewed without taking over a later one.
	ClaimedAt time.Time
}

// RefreshWorker finds media items that need their metadata refreshed.
// It implements the RefreshCandidateFinder interface used by the task manager's
// RefreshMetadataTask by claiming due rows from the durable refresh-debt queue.
type RefreshWorker struct {
	pool  *pgxpool.Pool
	debts *metadata.RefreshDebtRepository
}

// NewRefreshWorker creates a new RefreshWorker backed by the given database pool.
func NewRefreshWorker(pool *pgxpool.Pool) *RefreshWorker {
	return &RefreshWorker{
		pool:  pool,
		debts: metadata.NewRefreshDebtRepository(pool),
	}
}

// FindCandidates claims due durable metadata refresh debt rows.
func (w *RefreshWorker) FindCandidates(ctx context.Context, limit int) ([]RefreshCandidate, error) {
	if limit <= 0 {
		return nil, nil
	}

	debts, err := w.debts.ClaimDue(ctx, limit)
	if err != nil {
		return nil, err
	}

	candidates := make([]RefreshCandidate, 0, len(debts))
	for _, debt := range debts {
		if debt == nil {
			continue
		}
		candidate := RefreshCandidate{
			TargetType: debt.TargetType,
			ContentID:  debt.ContentID,
		}
		if debt.ClaimedAt != nil {
			candidate.ClaimedAt = *debt.ClaimedAt
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// RenewClaims extends the leases the claim behind each candidate still holds.
func (w *RefreshWorker) RenewClaims(ctx context.Context, candidates []RefreshCandidate) error {
	if w == nil || w.debts == nil {
		return nil
	}
	targetTypes := make([]string, 0, len(candidates))
	contentIDs := make([]string, 0, len(candidates))
	claimedAts := make([]time.Time, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ClaimedAt.IsZero() {
			continue
		}
		targetTypes = append(targetTypes, candidate.TargetType)
		contentIDs = append(contentIDs, candidate.ContentID)
		claimedAts = append(claimedAts, candidate.ClaimedAt)
	}
	return w.debts.RenewClaims(ctx, targetTypes, contentIDs, claimedAts)
}

// ClaimRenewInterval is how often a running batch renews its claims: often
// enough that a lease never lapses while the batch still holds it.
func (w *RefreshWorker) ClaimRenewInterval() time.Duration {
	return metadata.RefreshDebtLeaseDuration / 3
}

func (w *RefreshWorker) PruneDisabledLibraryDebt(ctx context.Context) error {
	if w == nil || w.debts == nil {
		return nil
	}
	return w.debts.PruneDisabledLibraryDebt(ctx)
}
