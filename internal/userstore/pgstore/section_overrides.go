package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func sectionOverridesKey(scope, libraryID string) string {
	return fmt.Sprintf("section_overrides:%s:%s", scope, libraryID)
}

func (s *PostgresUserStore) ListSectionOverrides(ctx context.Context, profileID, scope, libraryID string) ([]userstore.SectionOverride, error) {
	overrides, err := s.listAllSectionOverrides(ctx, scope, libraryID)
	if err != nil || len(overrides) == 0 {
		return nil, err
	}

	// Filter by profile.
	var result []userstore.SectionOverride
	for _, o := range overrides {
		if o.ProfileID == profileID {
			result = append(result, o)
		}
	}
	return result, nil
}

// ListAllSectionOverrides returns every section override stored for this
// account, across profiles and page scopes.
func (s *PostgresUserStore) ListAllSectionOverrides(ctx context.Context) ([]userstore.SectionOverride, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT value
		FROM user_settings
		WHERE user_id = $1 AND starts_with(key, 'section_overrides:')`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []userstore.SectionOverride
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		var overrides []userstore.SectionOverride
		if err := json.Unmarshal([]byte(value), &overrides); err != nil {
			return nil, fmt.Errorf("unmarshaling section overrides: %w", err)
		}
		result = append(result, overrides...)
	}
	return result, rows.Err()
}

func (s *PostgresUserStore) SaveSectionOverrides(ctx context.Context, profileID, scope, libraryID string, overrides []userstore.SectionOverride) error {
	// Set profile ID on all overrides.
	for i := range overrides {
		overrides[i].ProfileID = profileID
		if overrides[i].ID == "" {
			overrides[i].ID = generateUUID()
		}
		now := nowUTC()
		overrides[i].CreatedAt = now
		overrides[i].UpdatedAt = now
	}
	return s.updateSectionOverrides(ctx, scope, libraryID, func(existing []userstore.SectionOverride) []userstore.SectionOverride {
		merged := make([]userstore.SectionOverride, 0, len(existing)+len(overrides))
		for _, override := range existing {
			if override.ProfileID != profileID {
				merged = append(merged, override)
			}
		}
		return append(merged, overrides...)
	})
}

func (s *PostgresUserStore) ResetSectionOverrides(ctx context.Context, profileID, scope, libraryID string) error {
	return s.updateSectionOverrides(ctx, scope, libraryID, func(existing []userstore.SectionOverride) []userstore.SectionOverride {
		return slices.DeleteFunc(existing, func(o userstore.SectionOverride) bool { return o.ProfileID == profileID })
	})
}

// sectionOverridesAdvisoryClass is the advisory-lock class every writer of an
// account's section overrides takes (see lockSectionOverrides).
const sectionOverridesAdvisoryClass int32 = 0x5345434f // "SECO"

// lockSectionOverrides serializes the account's section-override writers for
// the rest of tx. Each setting holds every profile's overrides for one scope
// and library, so a writer reads, merges and writes the whole value, and two
// writers that interleave would lose one profile's change. The lock also
// covers a value that does not exist yet, which a row lock cannot.
func lockSectionOverrides(ctx context.Context, tx pgx.Tx, userID int) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)", sectionOverridesAdvisoryClass, int32(userID)); err != nil {
		return fmt.Errorf("locking section overrides: %w", err)
	}
	return nil
}

// updateSectionOverrides replaces one scope and library's overrides with
// update's result, under lockSectionOverrides. JSON marshaling preserves
// the full override payload, including removed. A result with no entries
// deletes the setting.
func (s *PostgresUserStore) updateSectionOverrides(ctx context.Context, scope, libraryID string, update func([]userstore.SectionOverride) []userstore.SectionOverride) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning section overrides update: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockSectionOverrides(ctx, tx, s.userID); err != nil {
		return err
	}
	key := sectionOverridesKey(scope, libraryID)
	val, err := getSetting(ctx, tx, s.userID, key)
	if err != nil {
		return err
	}
	var existing []userstore.SectionOverride
	if val != "" {
		if err := json.Unmarshal([]byte(val), &existing); err != nil {
			return fmt.Errorf("unmarshaling section overrides: %w", err)
		}
	}
	next := update(existing)
	if len(next) == 0 {
		_, err = tx.Exec(ctx, "DELETE FROM user_settings WHERE user_id = $1 AND key = $2", s.userID, key)
	} else {
		var data []byte
		if data, err = json.Marshal(next); err != nil {
			return fmt.Errorf("marshaling section overrides: %w", err)
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)
			 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`,
			s.userID, key, string(data))
	}
	if err != nil {
		return fmt.Errorf("writing section overrides %q: %w", key, err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresUserStore) listAllSectionOverrides(ctx context.Context, scope, libraryID string) ([]userstore.SectionOverride, error) {
	key := sectionOverridesKey(scope, libraryID)
	val, err := s.GetSetting(ctx, key)
	if err != nil || val == "" {
		return nil, err
	}

	var overrides []userstore.SectionOverride
	if err := json.Unmarshal([]byte(val), &overrides); err != nil {
		return nil, fmt.Errorf("unmarshaling section overrides: %w", err)
	}

	return overrides, nil
}
