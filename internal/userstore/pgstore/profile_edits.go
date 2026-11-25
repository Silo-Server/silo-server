package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// recordedProfileFields is a profile's values of the fields a profile set
// carries, read before a write to tell which of them it changes.
type recordedProfileFields struct {
	name, avatar, avatarSHA, pinHash, maxContentRating string
	isChild, requireAdvisoryAge, requestsBlocked       bool
	maxAdvisoryAge                                     int
}

// touchesRecordedFields reports whether u writes a field a set carries. The
// preference columns are not among them: their settings keys are recorded
// where those are written.
func touchesRecordedFields(u userstore.UpdateProfileInput) bool {
	return u.Name != nil || u.Avatar != nil || u.PINHash != nil || u.IsChild != nil || u.MaxContentRating != nil ||
		u.MaxAdvisoryAge != nil || u.RequireAdvisoryAge != nil || u.RequestsBlocked != nil
}

// readRecordedFields locks profile id at the strength its UPDATE takes and
// reads the fields a set carries, when ctx records edits to the account and
// u writes one of them. It answers nil when nothing is to be recorded: no
// recording actor, no such field in u, or a profile outside any set.
func readRecordedFields(ctx context.Context, exec preferenceSettingsExecutor, userID int, id string, u userstore.UpdateProfileInput) (*recordedProfileFields, error) {
	if _, ok := profileedits.ActorFor(ctx, userID); !ok || !touchesRecordedFields(u) {
		return nil, nil
	}
	var synced bool
	var f recordedProfileFields
	err := exec.QueryRow(ctx, `
		SELECT sync_id IS NOT NULL, name, COALESCE(avatar, ''), COALESCE(avatar_source_sha256, ''), COALESCE(pin_hash, ''),
		       is_child, COALESCE(max_content_rating, ''), COALESCE(max_advisory_age, 0), require_advisory_age, requests_blocked
		FROM user_profiles WHERE user_id = $1 AND id = $2 FOR NO KEY UPDATE`, userID, id).Scan(
		&synced, &f.name, &f.avatar, &f.avatarSHA, &f.pinHash,
		&f.isChild, &f.maxContentRating, &f.maxAdvisoryAge, &f.requireAdvisoryAge, &f.requestsBlocked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("profile %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("reading profile %s before an edit: %w", id, err)
	}
	if !synced {
		return nil, nil
	}
	return &f, nil
}

// changedFields lists the fields u gives a value other than before's. A
// picture is the pair of avatar and source hash, so a new upload behind the
// same avatar reference counts; a PIN compares hashes, so setting the same
// digits again counts too.
func (before *recordedProfileFields) changedFields(u userstore.UpdateProfileInput) []string {
	var out []string
	add := func(changed bool, field string) {
		if changed {
			out = append(out, field)
		}
	}
	if u.Name != nil {
		add(*u.Name != before.name, profileedits.FieldName)
	}
	if u.Avatar != nil {
		sha := ""
		if u.AvatarSourceSHA256 != nil {
			sha = *u.AvatarSourceSHA256
		}
		add(*u.Avatar != before.avatar || sha != before.avatarSHA, profileedits.FieldPicture)
	}
	if u.PINHash != nil {
		add(*u.PINHash != before.pinHash, profileedits.FieldPIN)
	}
	if u.IsChild != nil {
		add(*u.IsChild != before.isChild, profileedits.FieldIsChild)
	}
	if u.MaxContentRating != nil {
		add(*u.MaxContentRating != before.maxContentRating, profileedits.FieldMaxContentRating)
	}
	if u.MaxAdvisoryAge != nil {
		add(*u.MaxAdvisoryAge != before.maxAdvisoryAge, profileedits.FieldMaxAdvisoryAge)
	}
	if u.RequireAdvisoryAge != nil {
		add(*u.RequireAdvisoryAge != before.requireAdvisoryAge, profileedits.FieldRequireAdvisoryAge)
	}
	if u.RequestsBlocked != nil {
		add(*u.RequestsBlocked != before.requestsBlocked, profileedits.FieldRequestsBlocked)
	}
	return out
}

// recordSettingEdit records a change of a profile-scope setting.
func recordSettingEdit(ctx context.Context, exec preferenceSettingsExecutor, userID int, id userstore.SettingIdentity) error {
	if id.Scope != settingscontract.ScopeProfile {
		return nil
	}
	return profileedits.RecordSetting(ctx, exec, userID, id.ProfileID, id.Key)
}

// settingRecordable reports whether ctx records a change of id: a
// profile-scope key a set carries, written by an actor recorded for the
// account. It reads nothing.
func settingRecordable(ctx context.Context, userID int, id userstore.SettingIdentity) bool {
	if id.Scope != settingscontract.ScopeProfile || !profileedits.RecordedSetting(id.Key) {
		return false
	}
	_, ok := profileedits.ActorFor(ctx, userID)
	return ok
}

// settingBefore reads, locking its row, the value a write of id replaces:
// the stored value, or the contract default when nothing is stored. A write
// that leaves it as it was is not an edit.
func settingBefore(ctx context.Context, exec preferenceSettingsExecutor, userID int, id userstore.SettingIdentity) (json.RawMessage, error) {
	clause, args := settingIdentityPredicate(userID, id)
	var stored []byte
	err := exec.QueryRow(ctx, "SELECT value FROM user_setting_values WHERE "+clause+" FOR UPDATE", args...).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		contract, err := settingscontract.Load()
		if err != nil {
			return nil, err
		}
		if def, ok := contract.Lookup(id.Key); ok {
			return def.DefaultValue, nil
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading setting %q before a write: %w", id.Key, err)
	}
	return stored, nil
}

// sameSettingValue reports whether two JSON values are equal as values.
func sameSettingValue(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// recordsSettingEdit reports whether a pool-level write of id may record an
// edit, and so runs in a transaction of its own with its record. A profile
// outside the set records nothing and needs none.
func recordsSettingEdit(ctx context.Context, s *PostgresUserStore, id userstore.SettingIdentity) (bool, error) {
	if !settingRecordable(ctx, s.userID, id) {
		return false, nil
	}
	var synced bool
	err := s.pool.QueryRow(ctx, `SELECT sync_id IS NOT NULL FROM user_profiles WHERE user_id = $1 AND id = $2`, s.userID, id.ProfileID).Scan(&synced)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return synced, err
}

// inRecordingTx runs fn, a pool-level write of id, on the pool, or in a
// transaction when the write may record an edit, so the write and its record
// commit together.
func inRecordingTx[T any](ctx context.Context, s *PostgresUserStore, id userstore.SettingIdentity, fn func(preferenceSettingsExecutor) (T, error)) (T, error) {
	var out T
	record, err := recordsSettingEdit(ctx, s, id)
	if err != nil {
		return out, err
	}
	if !record {
		return fn(s.pool)
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The account's lock first, as every recording writer and an
		// account deletion take it (UpdateProfile).
		if err := lockPreferenceSettings(ctx, tx, s.userID); err != nil {
			return err
		}
		var err error
		out, err = fn(tx)
		return err
	})
	return out, err
}
