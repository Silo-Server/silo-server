package userdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// settingValueColumns is the projection every read shares, in the order
// scanSettingValue expects.
const settingValueColumns = `key, scope, profile_id, device_id, library_id, series_id,
	value, revision, created_at, updated_at`

// settingConflictTargets maps a scope to the partial unique index that enforces
// one explicit value per identity. SQLite requires an upsert against a partial
// index to repeat that index's WHERE clause, so the target carries it.
var settingConflictTargets = map[settingscontract.Scope]string{
	settingscontract.ScopeAccount:        "(key) WHERE scope = 'account'",
	settingscontract.ScopeProfile:        "(profile_id, key) WHERE scope = 'profile'",
	settingscontract.ScopeProfileDevice:  "(profile_id, device_id, key) WHERE scope = 'profile_device'",
	settingscontract.ScopeProfileLibrary: "(profile_id, library_id, key) WHERE scope = 'profile_library'",
	settingscontract.ScopeProfileSeries:  "(profile_id, series_id, key) WHERE scope = 'profile_series'",
}

// settingIdentityPredicate returns the WHERE fragment and bind arguments that
// address exactly one row. Every scope compares only the columns it populates,
// so no clause ever has to reason about NULL equality.
func settingIdentityPredicate(id userstore.SettingIdentity) (string, []any) {
	args := []any{id.Key, string(id.Scope)}
	clause := "key = ? AND scope = ?"
	switch id.Scope {
	case settingscontract.ScopeProfile:
		args = append(args, id.ProfileID)
		clause += " AND profile_id = ?"
	case settingscontract.ScopeProfileDevice:
		args = append(args, id.ProfileID, id.DeviceID)
		clause += " AND profile_id = ? AND device_id = ?"
	case settingscontract.ScopeProfileLibrary:
		args = append(args, id.ProfileID, id.LibraryID)
		clause += " AND profile_id = ? AND library_id = ?"
	case settingscontract.ScopeProfileSeries:
		args = append(args, id.ProfileID, id.SeriesID)
		clause += " AND profile_id = ? AND series_id = ?"
	}
	return clause, args
}

// GetSettingValue returns the explicit value at exactly one scope, or nil when
// that identity is unset.
func GetSettingValue(db *sql.DB, id userstore.SettingIdentity) (*userstore.SettingValue, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	clause, args := settingIdentityPredicate(id)
	row := db.QueryRow("SELECT "+settingValueColumns+" FROM user_setting_values WHERE "+clause, args...)
	value, err := scanSettingValue(row)
	if errors.Is(err, sql.ErrNoRows) {
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
// costs one round trip rather than four lookups per key.
func ListSettingValuesForResolution(
	db *sql.DB,
	query userstore.SettingResolutionQuery,
) ([]userstore.SettingValue, error) {
	q := query.Normalized()
	if len(q.Keys) == 0 {
		return nil, nil
	}

	args := make([]any, 0, len(q.Keys)+len(q.ProfileIDs)+len(q.LibraryIDs)+len(q.SeriesIDs)+1)
	for _, key := range q.Keys {
		args = append(args, key)
	}
	for _, profileID := range q.ProfileIDs {
		args = append(args, profileID)
	}
	args = append(args, q.DeviceID)
	for _, libraryID := range q.LibraryIDs {
		args = append(args, libraryID)
	}
	for _, seriesID := range q.SeriesIDs {
		args = append(args, seriesID)
	}

	// An empty id set is spelled as the false literal rather than an empty
	// IN (), which is a syntax error in SQLite. It drops the scope entirely,
	// which is what "no profile in play" and "no content context" both mean.
	profileClause := "0"
	if len(q.ProfileIDs) > 0 {
		profileClause = "profile_id IN (" + placeholders(len(q.ProfileIDs)) + ")"
	}
	libraryClause := "0"
	if len(q.LibraryIDs) > 0 {
		libraryClause = "scope = 'profile_library' AND library_id IN (" + placeholders(len(q.LibraryIDs)) + ")"
	}
	seriesClause := "0"
	if len(q.SeriesIDs) > 0 {
		seriesClause = "scope = 'profile_series' AND series_id IN (" + placeholders(len(q.SeriesIDs)) + ")"
	}

	rows, err := db.Query(`
		SELECT `+settingValueColumns+`
		FROM user_setting_values
		WHERE key IN (`+placeholders(len(q.Keys))+`)
		  AND (
		        scope = 'account'
		     OR (
		          `+profileClause+`
		          AND (
		                scope = 'profile'
		             OR (scope = 'profile_device' AND device_id = ?)
		             OR (`+libraryClause+`)
		             OR (`+seriesClause+`)
		          )
		        )
		      )
		ORDER BY key, scope, COALESCE(profile_id, ''), COALESCE(device_id, ''),
		         COALESCE(library_id, 0), COALESCE(series_id, '')`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("listing setting values for resolution: %w", err)
	}
	defer func() { _ = rows.Close() }()

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
func ListAllSettingValues(db *sql.DB) ([]userstore.SettingValue, error) {
	rows, err := db.Query(`
		SELECT ` + settingValueColumns + `
		FROM user_setting_values
		ORDER BY key, scope, COALESCE(profile_id, ''), COALESCE(device_id, ''),
		         COALESCE(library_id, 0), COALESCE(series_id, '')`)
	if err != nil {
		return nil, fmt.Errorf("listing all setting values: %w", err)
	}
	defer func() { _ = rows.Close() }()

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

// UpsertSettingValue writes the explicit value at one scope and increments that
// row's revision.
func UpsertSettingValue(
	db *sql.DB,
	id userstore.SettingIdentity,
	value json.RawMessage,
) (*userstore.SettingValue, error) {
	return upsertSettingValue(db, id, value)
}

func upsertSettingValue(
	exec preferenceSettingsExecutor,
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

	now := nowRFC3339()
	row := exec.QueryRow(fmt.Sprintf(`
		INSERT INTO user_setting_values
			(key, scope, profile_id, device_id, library_id, series_id, value, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT %s DO UPDATE SET
			value = excluded.value,
			revision = user_setting_values.revision + 1,
			updated_at = excluded.updated_at
		RETURNING %s`, target, settingValueColumns),
		id.Key, string(id.Scope),
		nullableText(id.ProfileID), nullableText(id.DeviceID),
		nullableInt(id.LibraryID), nullableText(id.SeriesID),
		string(value), now, now,
	)
	stored, err := scanSettingValue(row)
	if err != nil {
		return nil, fmt.Errorf("upserting setting value %q at %s: %w", id.Key, id.Scope, err)
	}
	return &stored, nil
}

// DeleteSettingValue removes the explicit value at one scope — the `unset`
// operation — and reports whether a row existed.
func DeleteSettingValue(db *sql.DB, id userstore.SettingIdentity) (bool, error) {
	return deleteSettingValue(db, id)
}

func deleteSettingValue(exec preferenceSettingsExecutor, id userstore.SettingIdentity) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	clause, args := settingIdentityPredicate(id)
	result, err := exec.Exec("DELETE FROM user_setting_values WHERE "+clause, args...)
	if err != nil {
		return false, fmt.Errorf("deleting setting value %q at %s: %w", id.Key, id.Scope, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting deleted setting value %q: %w", id.Key, err)
	}
	return affected > 0, nil
}

// DeleteSettingValuesForProfile removes every profile-anchored value for one
// profile. Account-scope rows carry a NULL profile_id and survive, which is what
// deleting one household member out of an account has to mean.
func DeleteSettingValuesForProfile(db *sql.DB, profileID string) (int64, error) {
	return execSettingValueDelete(db,
		"DELETE FROM user_setting_values WHERE profile_id = ?",
		fmt.Sprintf("profile %q", profileID), profileID)
}

func DeleteSettingValuesForDevice(db *sql.DB, profileID, deviceID string) (int64, error) {
	return execSettingValueDelete(db,
		"DELETE FROM user_setting_values WHERE scope = 'profile_device' AND profile_id = ? AND device_id = ?",
		fmt.Sprintf("device %q", deviceID), profileID, deviceID)
}

func DeleteSettingValuesForLibrary(db *sql.DB, libraryID int) (int64, error) {
	return execSettingValueDelete(db,
		"DELETE FROM user_setting_values WHERE scope = 'profile_library' AND library_id = ?",
		fmt.Sprintf("library %d", libraryID), libraryID)
}

func DeleteSettingValuesForSeries(db *sql.DB, seriesID string) (int64, error) {
	return execSettingValueDelete(db,
		"DELETE FROM user_setting_values WHERE scope = 'profile_series' AND series_id = ?",
		fmt.Sprintf("series %q", seriesID), seriesID)
}

func execSettingValueDelete(db *sql.DB, query, subject string, args ...any) (int64, error) {
	result, err := db.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("deleting setting values for %s: %w", subject, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting setting values deleted for %s: %w", subject, err)
	}
	return affected, nil
}

// GetSettingMutation returns a recorded idempotency receipt, or nil.
func GetSettingMutation(db *sql.DB, mutationID string) (*userstore.SettingMutationRecord, error) {
	row := db.QueryRow(`
		SELECT mutation_id, request_hash, result, created_at, expires_at
		FROM user_setting_mutations
		WHERE mutation_id = ?`, mutationID)
	record, err := scanSettingMutation(row)
	if errors.Is(err, sql.ErrNoRows) {
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
func PutSettingMutation(
	db *sql.DB,
	record userstore.SettingMutationRecord,
) (userstore.SettingMutationRecord, bool, error) {
	if err := record.Validate(); err != nil {
		return userstore.SettingMutationRecord{}, false, err
	}

	row := db.QueryRow(`
		INSERT INTO user_setting_mutations (mutation_id, request_hash, result, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (mutation_id) DO NOTHING
		RETURNING mutation_id, request_hash, result, created_at, expires_at`,
		record.MutationID, record.RequestHash, string(record.Result),
		nowRFC3339(), record.ExpiresAt.UTC().Format(time.RFC3339),
	)
	stored, err := scanSettingMutation(row)
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return userstore.SettingMutationRecord{}, false, fmt.Errorf("recording setting mutation %q: %w", record.MutationID, err)
	}

	existing, err := GetSettingMutation(db, record.MutationID)
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

// DeleteExpiredSettingMutations removes receipts that expired before the given
// instant and reports how many.
func DeleteExpiredSettingMutations(db *sql.DB, before time.Time) (int64, error) {
	result, err := db.Exec(
		"DELETE FROM user_setting_mutations WHERE expires_at <= ?",
		before.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("sweeping expired setting mutations: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting swept setting mutations: %w", err)
	}
	return affected, nil
}

// sqlRow is the subset of *sql.Row and *sql.Rows the scan helpers need.
type sqlRow interface {
	Scan(dest ...any) error
}

func scanSettingValue(row sqlRow) (userstore.SettingValue, error) {
	var (
		value     userstore.SettingValue
		scope     string
		profileID sql.NullString
		deviceID  sql.NullString
		libraryID sql.NullInt64
		seriesID  sql.NullString
		raw       string
	)
	if err := row.Scan(
		&value.Key, &scope, &profileID, &deviceID, &libraryID, &seriesID,
		&raw, &value.Revision, &value.CreatedAt, &value.UpdatedAt,
	); err != nil {
		return userstore.SettingValue{}, err
	}
	value.Scope = settingscontract.Scope(scope)
	value.ProfileID = profileID.String
	value.DeviceID = deviceID.String
	value.LibraryID = int(libraryID.Int64)
	value.SeriesID = seriesID.String
	value.Value = json.RawMessage(raw)
	return value, nil
}

func scanSettingMutation(row sqlRow) (userstore.SettingMutationRecord, error) {
	var (
		record    userstore.SettingMutationRecord
		raw       string
		createdAt string
		expiresAt string
	)
	if err := row.Scan(&record.MutationID, &record.RequestHash, &raw, &createdAt, &expiresAt); err != nil {
		return userstore.SettingMutationRecord{}, err
	}
	record.Result = json.RawMessage(raw)
	var err error
	if record.CreatedAt, err = parseRFC3339(createdAt); err != nil {
		return userstore.SettingMutationRecord{}, fmt.Errorf("parsing created_at for mutation %q: %w", record.MutationID, err)
	}
	if record.ExpiresAt, err = parseRFC3339(expiresAt); err != nil {
		return userstore.SettingMutationRecord{}, fmt.Errorf("parsing expires_at for mutation %q: %w", record.MutationID, err)
	}
	return record, nil
}

func parseRFC3339(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
