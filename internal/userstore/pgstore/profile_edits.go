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
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
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

// settingEffect follows a profile-scope setting's effective value across a
// write: the value the profile has, resolved as the settings read routes
// resolve it (the profile's own value, else what it inherits: the account's
// value where the key resolves through the account, else the
// administrator's default, else the contract's). A write or reset that leaves
// it as it was is not an edit; one that changes it is, even when the stored
// row then equals a default.
type settingEffect struct {
	before json.RawMessage
	active bool
}

// beginSettingEffect locks id's row, if there is one, and resolves the
// effective value before the write, when ctx records a change of id.
func beginSettingEffect(ctx context.Context, exec preferenceSettingsExecutor, userID int, defaults settingsresolve.ServerSettings, id userstore.SettingIdentity) (settingEffect, error) {
	if !settingRecordable(ctx, userID, id) {
		return settingEffect{}, nil
	}
	clause, args := settingIdentityPredicate(userID, id)
	if _, err := exec.Exec(ctx, "SELECT 1 FROM user_setting_values WHERE "+clause+" FOR UPDATE", args...); err != nil {
		return settingEffect{}, fmt.Errorf("locking setting %q before a write: %w", id.Key, err)
	}
	before, err := effectiveSetting(ctx, exec, userID, defaults, id)
	if err != nil {
		return settingEffect{}, err
	}
	return settingEffect{before: before, active: true}, nil
}

// finish resolves the effective value after the write and records an edit
// when it changed.
func (e settingEffect) finish(ctx context.Context, exec preferenceSettingsExecutor, userID int, defaults settingsresolve.ServerSettings, id userstore.SettingIdentity) error {
	if !e.active {
		return nil
	}
	after, err := effectiveSetting(ctx, exec, userID, defaults, id)
	if err != nil {
		return err
	}
	if sameSettingValue(e.before, after) {
		return nil
	}
	return recordSettingEdit(ctx, exec, userID, id)
}

// effectiveSetting resolves id.Key for id.ProfileID with the resolver and
// candidate read the settings routes use, on exec, with no policy
// constraint: the value the profile chose or inherits, not what a cap lets
// it play.
func effectiveSetting(ctx context.Context, exec preferenceSettingsExecutor, userID int, defaults settingsresolve.ServerSettings, id userstore.SettingIdentity) (json.RawMessage, error) {
	contract, err := settingscontract.Load()
	if err != nil {
		return nil, err
	}
	resolver := settingsresolve.New(contract)
	if defaults != nil {
		resolver = resolver.WithAdminDefaults(defaults)
	}
	values, err := resolver.Resolve(ctx, resolutionRows{exec: exec, userID: userID},
		settingsresolve.Context{ProfileID: id.ProfileID}, []string{id.Key}, nil)
	if err != nil {
		return nil, fmt.Errorf("resolving setting %q: %w", id.Key, err)
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values[0].Value, nil
}

// resolutionRows reads candidate rows for the resolver on one executor.
type resolutionRows struct {
	exec   preferenceSettingsExecutor
	userID int
}

func (r resolutionRows) ListSettingValuesForResolution(ctx context.Context, q userstore.SettingResolutionQuery) ([]userstore.SettingValue, error) {
	return listSettingValuesForResolution(ctx, r.exec, r.userID, q)
}

// sameSettingValue reports whether two JSON values are equal as values.
func sameSettingValue(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// inRecordingTx runs fn, a pool-level write of id, on the pool, or in a
// transaction when the write may record an edit, so the write and its record
// commit together under the account's lock. Whether the profile is in a set
// is not asked first: a set can add it between an unlocked check and the
// write, and the recorder checks it under the lock anyway.
func inRecordingTx[T any](ctx context.Context, s *PostgresUserStore, id userstore.SettingIdentity, fn func(preferenceSettingsExecutor) (T, error)) (T, error) {
	var out T
	if !settingRecordable(ctx, s.userID, id) {
		// Nothing this write does can be recorded: no recording actor, or a
		// key or scope a set doesn't carry.
		return fn(s.pool)
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
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
