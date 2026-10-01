package pgstore

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresUserStore) WithPreferenceSettingsSnapshot(ctx context.Context, fn func(userstore.PreferenceSettingsReader) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("beginning preference snapshot: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := fn(&preferenceSettingsTx{exec: tx, userID: s.userID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (tx *preferenceSettingsTx) GetAudioPreference(ctx context.Context, profileID, seriesID string) (*userstore.AudioPreference, error) {
	return getAudioPreference(ctx, tx.exec, tx.userID, profileID, seriesID)
}

func (tx *preferenceSettingsTx) GetSubtitlePreference(ctx context.Context, profileID, seriesID string) (*userstore.SubtitlePreference, error) {
	return getSubtitlePreference(ctx, tx.exec, tx.userID, profileID, seriesID)
}
