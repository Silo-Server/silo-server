package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

var _ userstore.ProfileSetStore = (*PostgresUserStore)(nil)

// profileSetRowsQuery reads every profile of the account with its place in
// the set, in creation order.
const profileSetRowsQuery = `
	SELECT id, name, avatar, pin_hash, is_child, is_primary, max_content_rating, COALESCE(max_advisory_age, 0),
	       require_advisory_age, requests_blocked,
	       quality_preference, language, preferred_metadata_language, subtitle_language, subtitle_mode,
	       auto_skip_intro, auto_skip_credits, auto_skip_recap, auto_play_next_preview, library_restrictions_enabled,
	       show_forced_subtitles, max_playback_quality, pin_revision,
	       COALESCE(sync_id::text, ''), COALESCE(avatar_source_sha256, ''), created_at, updated_at,
	       COALESCE(sync_position, 0), sync_applied,
	       ARRAY(
	           SELECT library_id FROM user_profile_allowed_libraries l
	           WHERE l.user_id = user_profiles.user_id AND l.profile_id = user_profiles.id
	           ORDER BY library_id ASC
	       )
	FROM user_profiles WHERE user_id = $1 ORDER BY created_at, id`

// ReadProfileSet reads the account's set state, profiles and profile-scope
// settings from one snapshot, taking no lock.
func (s *PostgresUserStore) ReadProfileSet(ctx context.Context) (userstore.ProfileSetSnapshot, error) {
	var out userstore.ProfileSetSnapshot
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		out, err = readProfileSetSnapshot(ctx, tx, s.userID, "")
		return err
	})
	return out, err
}

// readProfileSetSnapshot reads the snapshot in tx. lock is "" for a plain
// read, or a row-lock clause for the account row.
func readProfileSetSnapshot(ctx context.Context, tx pgx.Tx, userID int, lock string) (userstore.ProfileSetSnapshot, error) {
	var out userstore.ProfileSetSnapshot
	state, err := readProfileSetState(ctx, tx, userID, lock)
	if err != nil {
		return out, err
	}
	out.State = state
	rows, err := tx.Query(ctx, profileSetRowsQuery, userID)
	if err != nil {
		return out, fmt.Errorf("reading the household: %w", err)
	}
	out.Rows, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (userstore.ProfileSetRow, error) {
		var r userstore.ProfileSetRow
		var applied []byte
		var libraries []int
		p, err := scanProfile(row, &r.SyncPosition, &applied, &libraries)
		if err != nil {
			return r, err
		}
		r.Profile = *p
		if len(libraries) > 0 {
			r.AllowedLibraryIDs = libraries
		}
		if applied != nil {
			r.SyncApplied = json.RawMessage(applied)
		}
		return r, nil
	})
	if err != nil {
		return out, fmt.Errorf("reading the household: %w", err)
	}
	if out.Settings, err = readProfileScopeSettings(ctx, tx, userID); err != nil {
		return out, err
	}
	// A plain read: an edit row is locked only last, by the writer that
	// settles or records it.
	out.Edits, err = profileedits.List(ctx, tx, userID)
	return out, err
}

func readProfileSetState(ctx context.Context, tx pgx.Tx, userID int, lock string) (userstore.ProfileSetState, error) {
	var st userstore.ProfileSetState
	var version *int64
	var acceptedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT profile_set_version, profile_set_sha256, profile_set_accepted_at, role, enabled
		FROM users WHERE id = $1 `+lock, userID).Scan(&version, &st.SHA256, &acceptedAt, &st.Role, &st.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, fmt.Errorf("account %d: %w", userID, userstore.ErrProfileSetAccountGone)
	}
	if err != nil {
		return st, fmt.Errorf("reading the account's profile set: %w", err)
	}
	if version != nil {
		st.Held, st.Version = true, *version
	}
	if acceptedAt != nil {
		st.AcceptedAt = *acceptedAt
	}
	return st, nil
}

func readProfileScopeSettings(ctx context.Context, tx pgx.Tx, userID int) (map[string]map[string]json.RawMessage, error) {
	rows, err := tx.Query(ctx, `
		SELECT profile_id, key, value FROM user_setting_values
		WHERE user_id = $1 AND scope = $2 AND profile_id IS NOT NULL`, userID, string(settingscontract.ScopeProfile))
	if err != nil {
		return nil, fmt.Errorf("reading profile settings: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]json.RawMessage{}
	for rows.Next() {
		var profileID, key string
		var value []byte
		if err := rows.Scan(&profileID, &key, &value); err != nil {
			return nil, fmt.Errorf("reading profile settings: %w", err)
		}
		if out[profileID] == nil {
			out[profileID] = map[string]json.RawMessage{}
		}
		out[profileID][key] = json.RawMessage(value)
	}
	return out, rows.Err()
}

// WithProfileSetTransaction runs fn in one transaction that holds, in order:
// the account's preference-settings and section-override locks, the audit
// actors, the account row, the collections of the profiles the set removes
// in ID order, and every profile row of the account in ID order; fn locks the
// account's edit rows only at its end (SettleEdits). The order rests on two
// rules the other writers keep. Every writer that takes a profile row and
// then the account row takes the preference-settings lock first, so it runs
// wholly before or after the set: a profile save that bumps the access
// revision is one; so is an administrator's change of the username, which
// takes it before the account row and then may rename the main profile.
// Every writer that does not take that lock takes the account row before any
// profile row, the set's order: approving a device sign-in for a profile
// locks the account row and then, through the request's foreign key, the
// profile row; an allowance change locks the account row and then the acting
// profile; creating an API key share-locks the account row and then the
// profile. Recording an avatar hash writes one profile row and no account
// row.
func (s *PostgresUserStore) WithProfileSetTransaction(ctx context.Context, inSet []string, fn func(userstore.ProfileSetWriter) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning profile set transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // after Commit, Rollback is a no-op
	if err := LockAccountForImport(ctx, tx, s.userID); err != nil {
		return err
	}
	if auditmutation.Recording(ctx) {
		if err := auditmutation.LockActors(ctx, tx); err != nil {
			return err
		}
	}
	// At the strength the access-revision update a profile save makes takes
	// anyway, and the one Accept's update needs.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE`, s.userID); err != nil {
		return fmt.Errorf("locking the account: %w", err)
	}
	if inSet == nil {
		inSet = []string{}
	}
	// Every writer of sync_id holds the preference-settings lock, so this
	// list is stable for the rest of the transaction.
	rows, err := tx.Query(ctx, `
		SELECT id FROM user_profiles
		WHERE user_id = $1 AND sync_id IS NOT NULL AND NOT is_primary AND NOT (sync_id::text = ANY($2))
		ORDER BY id`, s.userID, inSet)
	if err != nil {
		return fmt.Errorf("finding the profiles the set removes: %w", err)
	}
	removable, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("finding the profiles the set removes: %w", err)
	}
	if len(removable) > 0 {
		// The statement DeleteProfileAudited locks one profile's collections
		// with, for every removed profile at once.
		if _, err := tx.Exec(ctx, `
			SELECT 1 FROM user_personal_collections
			WHERE user_id = $1 AND (profile_id = ANY($2) OR id IN (
				SELECT collection_id FROM user_personal_collection_profiles WHERE user_id = $1 AND profile_id = ANY($2)
			))
			ORDER BY id FOR UPDATE`, s.userID, removable); err != nil {
			return fmt.Errorf("locking the collections of removed profiles: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM user_profiles WHERE user_id = $1 ORDER BY id FOR UPDATE`, s.userID); err != nil {
		return fmt.Errorf("locking the household: %w", err)
	}
	writer := &profileSetTx{preferenceSettingsTx: preferenceSettingsTx{exec: tx, userID: s.userID, defaults: s.adminDefaults}, tx: tx, removable: removable}
	if err := fn(writer); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing profile set: %w", err)
	}
	return nil
}

// profileSetTx is the writer WithProfileSetTransaction passes to fn.
type profileSetTx struct {
	preferenceSettingsTx
	tx        pgx.Tx
	removable []string
}

func (w *profileSetTx) Snapshot(ctx context.Context) (userstore.ProfileSetSnapshot, error) {
	return readProfileSetSnapshot(ctx, w.tx, w.userID, "")
}

func (w *profileSetTx) Removable() []string { return slices.Clone(w.removable) }

func (w *profileSetTx) PutSettingValueIfChanged(ctx context.Context, id userstore.SettingIdentity, value json.RawMessage) (bool, error) {
	return putSettingValueIfChanged(ctx, w.tx, w.userID, id, value)
}

func (w *profileSetTx) SetSyncFields(ctx context.Context, profileID, syncID string, position int) (bool, error) {
	tag, err := w.tx.Exec(ctx, `
		UPDATE user_profiles SET sync_id = $3::uuid, sync_position = $4
		WHERE user_id = $1 AND id = $2
		  AND (sync_id IS DISTINCT FROM $3::uuid OR sync_position IS DISTINCT FROM $4)`,
		w.userID, profileID, syncID, position)
	if err != nil {
		return false, fmt.Errorf("writing the sync ID of profile %s: %w", profileID, err)
	}
	return tag.RowsAffected() > 0, nil
}

func (w *profileSetTx) SetSyncApplied(ctx context.Context, profileID string, doc json.RawMessage) (bool, error) {
	tag, err := w.tx.Exec(ctx, `
		UPDATE user_profiles SET sync_applied = $3::jsonb
		WHERE user_id = $1 AND id = $2 AND sync_applied IS DISTINCT FROM $3::jsonb`,
		w.userID, profileID, []byte(doc))
	if err != nil {
		return false, fmt.Errorf("writing the applied set of profile %s: %w", profileID, err)
	}
	return tag.RowsAffected() > 0, nil
}

func (w *profileSetTx) DeleteProfile(ctx context.Context, profileID string) ([]string, error) {
	if !slices.Contains(w.removable, profileID) {
		return nil, fmt.Errorf("profile %s was not locked for removal", profileID)
	}
	collections, _, err := deleteProfileRowsTx(ctx, w.tx, w.userID, profileID)
	return collections, err
}

func (w *profileSetTx) SettleEdits(ctx context.Context, seen []profileedits.Edit, removed []string) error {
	return profileedits.Settle(ctx, w.tx, w.userID, seen, removed)
}

func (w *profileSetTx) MoveEdits(ctx context.Context, from, to string) error {
	return profileedits.Move(ctx, w.tx, w.userID, from, to)
}

func (w *profileSetTx) ClearOwnerEdits(ctx context.Context, syncID string, fields []string) (int64, error) {
	return profileedits.ClearOwner(ctx, w.tx, w.userID, syncID, fields)
}

func (w *profileSetTx) DismissSettledEdit(ctx context.Context, syncID, field string) (bool, error) {
	return profileedits.DismissSettled(ctx, w.tx, w.userID, syncID, field)
}

func (w *profileSetTx) Accept(ctx context.Context, version int64, sha256 []byte) error {
	if _, err := w.tx.Exec(ctx, `
		UPDATE users SET profile_set_version = $2, profile_set_sha256 = $3, profile_set_accepted_at = now()
		WHERE id = $1`, w.userID, version, sha256); err != nil {
		return fmt.Errorf("storing the accepted profile set: %w", err)
	}
	return nil
}
