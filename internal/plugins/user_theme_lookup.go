package plugins

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgUserThemeLookup resolves the user's effective UI theme so the plugin proxy
// can stamp X-Silo-Theme on every plugin request.
//
// The theme is the canonical profile-scoped ui.theme setting in
// user_setting_values — the row the settings contract's typed API writes. The
// legacy account-level user_settings.ui_theme row is read only as a fallback,
// for a store whose one-time backfill has not produced canonical rows;
// without the fallback those users would flash the default theme, and without
// the canonical read a profile's theme change would never reach plugins.
type PgUserThemeLookup struct {
	pool *pgxpool.Pool
}

func NewPgUserThemeLookup(pool *pgxpool.Pool) *PgUserThemeLookup {
	return &PgUserThemeLookup{pool: pool}
}

func (l *PgUserThemeLookup) LookupUITheme(ctx context.Context, userID int, profileID string) (string, error) {
	if l == nil || l.pool == nil || userID <= 0 {
		return "", nil
	}

	// The canonical rows first: the profile's own override when the request
	// names a profile, else any profile-scope row is meaningless, so only the
	// named profile participates. Values are JSON, so a stored theme is a
	// quoted string.
	if profileID != "" {
		var raw []byte
		err := l.pool.QueryRow(ctx, `
SELECT value FROM user_setting_values
 WHERE user_id = $1 AND profile_id = $2 AND key = 'ui.theme' AND scope = 'profile'`,
			userID, profileID,
		).Scan(&raw)
		switch {
		case err == nil:
			var theme string
			if json.Unmarshal(raw, &theme) == nil && theme != "" {
				return theme, nil
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return "", err
		}
	}

	var value string
	err := l.pool.QueryRow(ctx,
		"SELECT value FROM user_settings WHERE user_id = $1 AND key = 'ui_theme'",
		userID,
	).Scan(&value)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}
