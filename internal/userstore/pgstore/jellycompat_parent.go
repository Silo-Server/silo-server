package pgstore

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *PostgresUserStore) ApplyJellycompatParent(ctx context.Context, profileID string, edit userstore.JellycompatParentEdit) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if edit.Played {
		if _, err := s.markWatchedBatchTx(ctx, tx, profileID, edit.Targets, edit.History); err != nil {
			return err
		}
	} else if len(edit.Targets) > 0 {
		ids := make([]string, 0, len(edit.Targets))
		for _, target := range edit.Targets {
			ids = append(ids, target.MediaItemID)
		}
		if err := s.removeHistoryItems(ctx, tx, profileID, ids, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := s.setJellycompatFavorite(ctx, tx, profileID, edit.MediaItemID, edit.IsFavorite); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
