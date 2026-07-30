package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// settingValueColumns is the projection every read shares, in the order
// scanSettingValue expects.
const settingValueColumns = `key, scope, profile_id, device_id, library_id, series_id,
	value, revision, created_at, updated_at`

// settingConflictTargets maps a scope to the partial unique index that enforces
// one explicit value per identity. The upsert names the matching target so a
// repeated write updates its own row rather than inserting a duplicate.
var settingConflictTargets = map[settingscontract.Scope]string{
	settingscontract.ScopeAccount:        "(user_id, key) WHERE scope = 'account'",
	settingscontract.ScopeProfile:        "(user_id, profile_id, key) WHERE scope = 'profile'",
	settingscontract.ScopeProfileDevice:  "(user_id, profile_id, device_id, key) WHERE scope = 'profile_device'",
	settingscontract.ScopeProfileLibrary: "(user_id, profile_id, library_id, key) WHERE scope = 'profile_library'",
	settingscontract.ScopeProfileSeries:  "(user_id, profile_id, series_id, key) WHERE scope = 'profile_series'",
}

// settingIdentityPredicate returns the WHERE fragment and bind arguments that
// address exactly one row. Every scope compares only the columns it populates,
// so no clause ever has to reason about NULL equality.
func settingIdentityPredicate(userID int, id userstore.SettingIdentity) (string, []any) {
	args := []any{userID, id.Key, string(id.Scope)}
	clause := "user_id = $1 AND key = $2 AND scope = $3"
	switch id.Scope {
	case settingscontract.ScopeProfile:
		args = append(args, id.ProfileID)
		clause += " AND profile_id = $4"
	case settingscontract.ScopeProfileDevice:
		args = append(args, id.ProfileID, id.DeviceID)
		clause += " AND profile_id = $4 AND device_id = $5"
	case settingscontract.ScopeProfileLibrary:
		args = append(args, id.ProfileID, id.LibraryID)
		clause += " AND profile_id = $4 AND library_id = $5"
	case settingscontract.ScopeProfileSeries:
		args = append(args, id.ProfileID, id.SeriesID)
		clause += " AND profile_id = $4 AND series_id = $5"
	}
	return clause, args
}

func (s *PostgresUserStore) GetSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
) (*userstore.SettingValue, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	clause, args := settingIdentityPredicate(s.userID, id)
	row := s.pool.QueryRow(ctx,
		"SELECT "+settingValueColumns+" FROM user_setting_values WHERE "+clause,
		args...,
	)
	value, err := scanSettingValue(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting setting value %q at %s: %w", id.Key, id.Scope, err)
	}
	return &value, nil
}

// ListSettingValuesForResolution collects every candidate row for a resolution
// request in one query. The predicate covers all five scopes at once: ranking by
// each definition's resolution order happens in Go, so a four-scope chain still
// costs one round trip and one index scan rather than four lookups per key.
func (s *PostgresUserStore) ListSettingValuesForResolution(
	ctx context.Context,
	query userstore.SettingResolutionQuery,
) ([]userstore.SettingValue, error) {
	q := query.Normalized()
	if len(q.Keys) == 0 {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+settingValueColumns+`
		FROM user_setting_values
		WHERE user_id = $1
		  AND key = ANY($2::text[])
		  AND (
		        scope = 'account'
		     OR (
		          profile_id = ANY($3::text[])
		          AND (
		                scope = 'profile'
		             OR (scope = 'profile_device' AND device_id = $4)
		             OR (scope = 'profile_library' AND library_id = ANY($5::int[]))
		             OR (scope = 'profile_series' AND series_id = ANY($6::text[]))
		          )
		        )
		      )
		ORDER BY key, scope, COALESCE(profile_id, ''), COALESCE(device_id, ''),
		         COALESCE(library_id, 0), COALESCE(series_id, '')`,
		s.userID, q.Keys, q.ProfileIDs, q.DeviceID, q.LibraryIDs, q.SeriesIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("listing setting values for resolution: %w", err)
	}
	defer rows.Close()

	var values []userstore.SettingValue
	for rows.Next() {
		value, err := scanSettingValue(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning setting value: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// ListAllSettingValues returns every stored explicit value across all scopes,
// ordered by (key, scope, identity) so repeated reads page through the same
// sequence. It backs the admin inspection surface, which wants the stored
// truth rather than a resolution.
func (s *PostgresUserStore) ListAllSettingValues(ctx context.Context) ([]userstore.SettingValue, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+settingValueColumns+`
		FROM user_setting_values
		WHERE user_id = $1
		ORDER BY key, scope, COALESCE(profile_id, ''), COALESCE(device_id, ''),
		         COALESCE(library_id, 0), COALESCE(series_id, '')`,
		s.userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing all setting values: %w", err)
	}
	defer rows.Close()

	var values []userstore.SettingValue
	for rows.Next() {
		value, err := scanSettingValue(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning setting value: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *PostgresUserStore) UpsertSettingValue(
	ctx context.Context,
	id userstore.SettingIdentity,
	value json.RawMessage,
) (*userstore.SettingValue, error) {
	return upsertSettingValue(ctx, s.pool, s.userID, id, value)
}

func upsertSettingValue(
	ctx context.Context,
	exec preferenceSettingsExecutor,
	userID int,
	id userstore.SettingIdentity,
	value json.RawMessage,
) (*userstore.SettingValue, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	if err := userstore.ValidateSettingValueJSON(value); err != nil {
		return nil, err
	}
	target, ok := settingConflictTargets[id.Scope]
	if !ok {
		return nil, fmt.Errorf("%w: %q has no storage identity", userstore.ErrInvalidSettingIdentity, id.Scope)
	}

	row := exec.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO user_setting_values
			(user_id, key, scope, profile_id, device_id, library_id, series_id, value)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT %s DO UPDATE SET
			value = excluded.value,
			revision = user_setting_values.revision + 1,
			updated_at = now()
		RETURNING %s`, target, settingValueColumns),
		userID, id.Key, string(id.Scope),
		nullableText(id.ProfileID), nullableText(id.DeviceID),
		nullableInt(id.LibraryID), nullableText(id.SeriesID),
		[]byte(value),
	)
	stored, err := scanSettingValue(row)
	if err != nil {
		return nil, fmt.Errorf("upserting setting value %q at %s: %w", id.Key, id.Scope, err)
	}
	return &stored, nil
}

func (s *PostgresUserStore) DeleteSettingValue(ctx context.Context, id userstore.SettingIdentity) (bool, error) {
	return deleteSettingValue(ctx, s.pool, s.userID, id)
}

func deleteSettingValue(
	ctx context.Context,
	exec preferenceSettingsExecutor,
	userID int,
	id userstore.SettingIdentity,
) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	clause, args := settingIdentityPredicate(userID, id)
	tag, err := exec.Exec(ctx, "DELETE FROM user_setting_values WHERE "+clause, args...)
	if err != nil {
		return false, fmt.Errorf("deleting setting value %q at %s: %w", id.Key, id.Scope, err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteSettingValuesForProfile removes every profile-anchored value for one
// profile. Account-scope rows carry a NULL profile_id and survive, which is what
// deleting one household member out of an account has to mean.
func (s *PostgresUserStore) DeleteSettingValuesForProfile(ctx context.Context, profileID string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		"DELETE FROM user_setting_values WHERE user_id = $1 AND profile_id = $2",
		s.userID, profileID,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting setting values for profile %q: %w", profileID, err)
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresUserStore) DeleteSettingValuesForDevice(ctx context.Context, profileID, deviceID string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM user_setting_values
		WHERE user_id = $1 AND scope = 'profile_device' AND profile_id = $2 AND device_id = $3`,
		s.userID, profileID, deviceID,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting setting values for device %q: %w", deviceID, err)
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresUserStore) DeleteSettingValuesForLibrary(ctx context.Context, libraryID int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM user_setting_values
		WHERE user_id = $1 AND scope = 'profile_library' AND library_id = $2`,
		s.userID, libraryID,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting setting values for library %d: %w", libraryID, err)
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresUserStore) DeleteSettingValuesForSeries(ctx context.Context, seriesID string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM user_setting_values
		WHERE user_id = $1 AND scope = 'profile_series' AND series_id = $2`,
		s.userID, seriesID,
	)
	if err != nil {
		return 0, fmt.Errorf("deleting setting values for series %q: %w", seriesID, err)
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresUserStore) GetSettingMutation(
	ctx context.Context,
	mutationID string,
) (*userstore.SettingMutationRecord, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT mutation_id, request_hash, result, created_at, expires_at
		FROM user_setting_mutations
		WHERE user_id = $1 AND mutation_id = $2`,
		s.userID, mutationID,
	)
	record, err := scanSettingMutation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting setting mutation %q: %w", mutationID, err)
	}
	return &record, nil
}

// PutSettingMutation never overwrites a receipt: DO NOTHING plus a second read
// keeps a replayed mutation_id answering with the result the first attempt
// produced, which is what makes a client's retry idempotent rather than a
// silent re-run.
func (s *PostgresUserStore) PutSettingMutation(
	ctx context.Context,
	record userstore.SettingMutationRecord,
) (userstore.SettingMutationRecord, bool, error) {
	if err := record.Validate(); err != nil {
		return userstore.SettingMutationRecord{}, false, err
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO user_setting_mutations (user_id, mutation_id, request_hash, result, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, mutation_id) DO NOTHING
		RETURNING mutation_id, request_hash, result, created_at, expires_at`,
		s.userID, record.MutationID, record.RequestHash, []byte(record.Result), record.ExpiresAt,
	)
	stored, err := scanSettingMutation(row)
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return userstore.SettingMutationRecord{}, false, fmt.Errorf("recording setting mutation %q: %w", record.MutationID, err)
	}

	existing, err := s.GetSettingMutation(ctx, record.MutationID)
	if err != nil {
		return userstore.SettingMutationRecord{}, false, err
	}
	if existing == nil {
		// The conflicting row was swept between the insert and this read; the
		// caller can safely retry rather than receive a phantom conflict.
		return userstore.SettingMutationRecord{}, false, fmt.Errorf(
			"recording setting mutation %q: conflicting receipt disappeared", record.MutationID)
	}
	return *existing, false, nil
}

func (s *PostgresUserStore) DeleteExpiredSettingMutations(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		"DELETE FROM user_setting_mutations WHERE user_id = $1 AND expires_at <= $2",
		s.userID, before,
	)
	if err != nil {
		return 0, fmt.Errorf("sweeping expired setting mutations: %w", err)
	}
	return tag.RowsAffected(), nil
}

// pgxRow is the subset of pgx.Row and pgx.Rows the scan helpers need.
type pgxRow interface {
	Scan(dest ...any) error
}

func scanSettingValue(row pgxRow) (userstore.SettingValue, error) {
	var (
		value     userstore.SettingValue
		scope     string
		profileID *string
		deviceID  *string
		libraryID *int
		seriesID  *string
		raw       []byte
		createdAt time.Time
		updatedAt time.Time
	)
	if err := row.Scan(
		&value.Key, &scope, &profileID, &deviceID, &libraryID, &seriesID,
		&raw, &value.Revision, &createdAt, &updatedAt,
	); err != nil {
		return userstore.SettingValue{}, err
	}
	value.Scope = settingscontract.Scope(scope)
	if profileID != nil {
		value.ProfileID = *profileID
	}
	if deviceID != nil {
		value.DeviceID = *deviceID
	}
	if libraryID != nil {
		value.LibraryID = *libraryID
	}
	if seriesID != nil {
		value.SeriesID = *seriesID
	}
	value.Value = json.RawMessage(raw)
	value.CreatedAt = timeToString(createdAt)
	value.UpdatedAt = timeToString(updatedAt)
	return value, nil
}

func scanSettingMutation(row pgxRow) (userstore.SettingMutationRecord, error) {
	var (
		record userstore.SettingMutationRecord
		raw    []byte
	)
	if err := row.Scan(
		&record.MutationID, &record.RequestHash, &raw, &record.CreatedAt, &record.ExpiresAt,
	); err != nil {
		return userstore.SettingMutationRecord{}, err
	}
	record.Result = json.RawMessage(raw)
	record.CreatedAt = record.CreatedAt.UTC()
	record.ExpiresAt = record.ExpiresAt.UTC()
	return record, nil
}

func nullableText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullableInt(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}
