package userdb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *SQLiteUserStore) WithPreferenceSettingsSnapshot(ctx context.Context, fn func(userstore.PreferenceSettingsReader) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("beginning preference snapshot: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(&preferenceSettingsTx{exec: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

func (tx *preferenceSettingsTx) GetAudioPreference(_ context.Context, profileID, seriesID string) (*userstore.AudioPreference, error) {
	return getAudioPreference(tx.exec, profileID, seriesID)
}

func (tx *preferenceSettingsTx) GetSubtitlePreference(_ context.Context, profileID, seriesID string) (*userstore.SubtitlePreference, error) {
	return getSubtitlePreference(tx.exec, profileID, seriesID)
}
