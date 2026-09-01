package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for repository operations.
var (
	ErrNotFound  = errors.New("user not found")
	ErrDuplicate = errors.New("duplicate user")
)

// IsNotFound returns true if the error is a "not found" error.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// IsDuplicate returns true if the error is a "duplicate" error.
func IsDuplicate(err error) bool {
	return errors.Is(err, ErrDuplicate)
}

// CheckPassword verifies a plaintext password against the user's bcrypt hash.
// This is a standalone function, not a repository method.
func CheckPassword(user *models.User, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
	return err == nil
}

// UserRepository provides CRUD operations for the users table.
type UserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository creates a new UserRepository backed by the given pool.
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// allColumns is the list of columns returned by all SELECT queries.
// Kept in one place so scanUser stays in sync.
const allColumns = `id, email, username, password_hash, local_password_login_enabled, role, permissions, enabled,
	library_ids, max_playback_quality, access_policy_revision,
	max_streams, max_transcodes, transcode_allowed, audio_transcode_allowed, max_profiles, download_allowed,
	download_transcode_allowed, requests_allowed, access_group_id, created_at, updated_at`

// scanUser scans a single row into a *models.User.
func scanUser(row pgx.Row) (*models.User, error) {
	var u models.User
	err := row.Scan(
		&u.ID,
		&u.Email,
		&u.Username,
		&u.PasswordHash,
		&u.LocalPasswordLoginEnabled,
		&u.Role,
		&u.Permissions,
		&u.Enabled,
		&u.LibraryIDs,
		&u.MaxPlaybackQuality,
		&u.AccessPolicyRevision,
		&u.MaxStreams,
		&u.MaxTranscodes,
		&u.TranscodeAllowed,
		&u.AudioTranscodeAllowed,
		&u.MaxProfiles,
		&u.DownloadAllowed,
		&u.DownloadTranscodeAllowed,
		&u.RequestsAllowed,
		&u.AccessGroupID,
		&u.CreatedAt,
		&u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning user: %w", err)
	}
	return &u, nil
}

// scanUsers scans multiple rows into a []*models.User slice.
func scanUsers(rows pgx.Rows) ([]*models.User, error) {
	var users []*models.User
	for rows.Next() {
		var u models.User
		err := rows.Scan(
			&u.ID,
			&u.Email,
			&u.Username,
			&u.PasswordHash,
			&u.LocalPasswordLoginEnabled,
			&u.Role,
			&u.Permissions,
			&u.Enabled,
			&u.LibraryIDs,
			&u.MaxPlaybackQuality,
			&u.AccessPolicyRevision,
			&u.MaxStreams,
			&u.MaxTranscodes,
			&u.TranscodeAllowed,
			&u.AudioTranscodeAllowed,
			&u.MaxProfiles,
			&u.DownloadAllowed,
			&u.DownloadTranscodeAllowed,
			&u.RequestsAllowed,
			&u.AccessGroupID,
			&u.CreatedAt,
			&u.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning user row: %w", err)
		}
		users = append(users, &u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating user rows: %w", err)
	}
	return users, nil
}

// Create inserts a new user with a bcrypt-hashed password and returns the created user.
func (r *UserRepository) Create(ctx context.Context, input models.CreateUserInput) (*models.User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	// Base columns that are always included.
	localPasswordLoginEnabled := true
	if input.LocalPasswordLoginEnabled != nil {
		localPasswordLoginEnabled = *input.LocalPasswordLoginEnabled
	}

	permissions := append([]string(nil), input.Permissions...)
	if input.Permissions == nil && input.Role != "admin" {
		permissions = DefaultUserPermissions()
	}
	permissions, err = NormalizePermissions(permissions)
	if err != nil {
		return nil, err
	}

	// Policy columns are written explicitly: a nil pointer stores NULL, which
	// means "inherit from the access group" (the columns carry no defaults).
	cols := []string{
		"email", "username", "password_hash", "local_password_login_enabled", "role", "permissions",
		"library_ids", "max_playback_quality", "max_streams", "max_transcodes",
		"transcode_allowed", "audio_transcode_allowed", "download_allowed", "download_transcode_allowed",
		"requests_allowed",
	}
	args := []any{
		NormalizeEmail(input.Email),
		NormalizeUsername(input.Username),
		string(hash),
		localPasswordLoginEnabled,
		input.Role,
		permissions,
		input.LibraryIDs,
		normalizeQualityOverride(input.MaxPlaybackQuality),
		input.MaxStreams,
		input.MaxTranscodes,
		input.TranscodeAllowed,
		input.AudioTranscodeAllowed,
		input.DownloadAllowed,
		input.DownloadTranscodeAllowed,
		input.RequestsAllowed,
	}

	// Optional columns: nil means use DB default.
	if input.MaxProfiles != nil {
		cols = append(cols, "max_profiles")
		args = append(args, *input.MaxProfiles)
	}
	accessGroupID := input.AccessGroupID
	if input.Role == models.RoleAdmin {
		accessGroupID = nil
	}
	if accessGroupID != nil {
		cols = append(cols, "access_group_id")
		args = append(args, *accessGroupID)
	}

	// Build placeholders: $1, $2, ..., $N
	placeholders := make([]string, len(args))
	for i := range args {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	// Admins stay ungrouped: scope/action decisions are role-blind, so the
	// default group's ceilings would cap the server owner (mirrors the
	// exclusion in the assign_default_group_to_existing_users migration).
	if accessGroupID == nil && input.Role != models.RoleAdmin {
		cols = append(cols, "access_group_id")
		placeholders = append(placeholders, "(SELECT id FROM access_groups WHERE is_default)")
	}

	query := fmt.Sprintf("INSERT INTO users (%s) VALUES (%s) RETURNING %s",
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
		allColumns,
	)

	row := r.pool.QueryRow(ctx, query, args...)

	user, err := scanUser(row)
	if err != nil {
		if isDuplicateKeyError(err) {
			return nil, fmt.Errorf("%w: %s", ErrDuplicate, extractConstraint(err))
		}
		return nil, fmt.Errorf("creating user: %w", err)
	}

	return user, nil
}

// GetByID retrieves a user by their numeric ID.
func (r *UserRepository) GetByID(ctx context.Context, id int) (*models.User, error) {
	query := `SELECT ` + allColumns + ` FROM users WHERE id = $1`
	return scanUser(r.pool.QueryRow(ctx, query, id))
}

// GetByUsername retrieves a user by their username (case-insensitive).
func (r *UserRepository) GetByUsername(ctx context.Context, username string) (*models.User, error) {
	query := `SELECT ` + allColumns + ` FROM users WHERE username = $1`
	return scanUser(r.pool.QueryRow(ctx, query, NormalizeUsername(username)))
}

// GetByEmail retrieves a user by their email address (case-insensitive).
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*models.User, error) {
	query := `SELECT ` + allColumns + ` FROM users WHERE email = $1`
	return scanUser(r.pool.QueryRow(ctx, query, NormalizeEmail(email)))
}

// userUpdateColumn is one candidate column of a user update: it is written
// only when set, and bumpsAccessPolicy marks the columns whose change has to
// invalidate durable session/profile tokens by bumping
// access_policy_revision. Values are pre-computed, so every entry is safe to
// build even when set is false.
type userUpdateColumn struct {
	column            string
	set               bool
	value             any
	bumpsAccessPolicy bool
}

// accessGroupSetClause builds the SET clause and access-policy predicate for
// access_group_id given the next free placeholder index. access_group_id is
// handled outside the generic userUpdateColumn machinery because, unlike
// every other column, what gets written depends on the row's current role:
//
//   - Granting admin (input.Role == "admin") clears the group unconditionally.
//   - Changing role to anything else without naming a group lands the row on
//     the default group, but only if it was an admin (accounts are never
//     un-grouped by an unrelated role change).
//   - Setting a group on its own (input.Role == nil) is guarded by a CASE so
//     a write that races an admin promotion cannot leave the admin grouped.
//   - Otherwise (explicit NULL, or a group set alongside a non-admin role
//     change) the value is bound directly.
//
// Admin accounts are never grouped (see Create). Returns an empty setClause
// if access_group_id is not touched by this update.
//
// The default-group branch reads from a CTE (aliased in defaultGroupCTE)
// instead of inlining the subselect, because the same expression is spliced
// into both the SET clause and the access_policy_revision predicate — as a
// literal subselect it would run twice per UPDATE, but a CTE referenced more
// than once is materialized once by Postgres.
func accessGroupSetClause(input models.UpdateUserInput, argIndex int) (setClause, predicate, defaultGroupCTE string, args []any, nextArgIndex int) {
	const isAdmin = "role = '" + models.RoleAdmin + "'"
	nextArgIndex = argIndex
	switch {
	case input.Role != nil && *input.Role == models.RoleAdmin:
		placeholder := fmt.Sprintf("$%d", argIndex)
		setClause = "access_group_id = " + placeholder
		args = []any{(*int64)(nil)}
		nextArgIndex++
	case input.Role != nil && !input.AccessGroupID.Set:
		defaultGroupCTE = "default_group AS (SELECT id FROM access_groups WHERE is_default)"
		expr := "(CASE WHEN " + isAdmin + " THEN (SELECT id FROM default_group) ELSE access_group_id END)"
		setClause = "access_group_id = " + expr
	case input.Role == nil && input.AccessGroupID.Set && input.AccessGroupID.Value != nil:
		placeholder := fmt.Sprintf("$%d", argIndex)
		// The cast pins the parameter type; inside a CASE the driver would
		// otherwise send it as text.
		expr := "(CASE WHEN " + isAdmin + " THEN NULL ELSE " + placeholder + "::bigint END)"
		setClause = "access_group_id = " + expr
		args = []any{input.AccessGroupID.Value}
		nextArgIndex++
	default:
		if !input.AccessGroupID.Set {
			return "", "", "", nil, argIndex
		}
		placeholder := fmt.Sprintf("$%d", argIndex)
		setClause = "access_group_id = " + placeholder
		args = []any{input.AccessGroupID.Value}
		nextArgIndex++
	}
	predicate = "access_group_id IS DISTINCT FROM " + strings.TrimPrefix(setClause, "access_group_id = ")
	return setClause, predicate, defaultGroupCTE, args, nextArgIndex
}

// Update modifies a user's fields. Only non-nil fields in the input are updated.
// If the input contains a Password, it is bcrypt-hashed before storage.
func (r *UserRepository) Update(ctx context.Context, id int, input models.UpdateUserInput) error {
	var email *string
	if input.Email != nil {
		normalized := NormalizeEmail(*input.Email)
		email = &normalized
	}
	var username *string
	if input.Username != nil {
		normalized := NormalizeUsername(*input.Username)
		username = &normalized
	}
	var passwordHash *string
	if input.Password != nil {
		hash, err := bcrypt.GenerateFromPassword([]byte(*input.Password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hashing password: %w", err)
		}
		hashed := string(hash)
		passwordHash = &hashed
	}
	var permissions []string
	if input.Permissions != nil {
		normalized, err := NormalizePermissions(*input.Permissions)
		if err != nil {
			return err
		}
		permissions = normalized
	}

	// Library scope is resolved from users.library_ids on each request, so
	// changing it must not invalidate durable profile/session tokens — hence
	// no access-policy bump on that column.
	columns := []userUpdateColumn{
		{column: "email", set: email != nil, value: email},
		{column: "username", set: username != nil, value: username},
		{column: "password_hash", set: passwordHash != nil, value: passwordHash},
		{column: "local_password_login_enabled", set: input.LocalPasswordLoginEnabled != nil, value: input.LocalPasswordLoginEnabled},
		{column: "role", set: input.Role != nil, value: input.Role, bumpsAccessPolicy: true},
		{column: "permissions", set: input.Permissions != nil, value: permissions, bumpsAccessPolicy: true},
		{column: "enabled", set: input.Enabled != nil, value: input.Enabled, bumpsAccessPolicy: true},
		{column: "library_ids", set: input.LibraryIDs.Set, value: derefSlice(input.LibraryIDs.Value)},
		{
			column:            "max_playback_quality",
			set:               input.MaxPlaybackQuality.Set,
			value:             normalizeQualityOverride(input.MaxPlaybackQuality.Value),
			bumpsAccessPolicy: true,
		},
		{column: "max_streams", set: input.MaxStreams.Set, value: input.MaxStreams.Value},
		{column: "max_transcodes", set: input.MaxTranscodes.Set, value: input.MaxTranscodes.Value},
		{column: "transcode_allowed", set: input.TranscodeAllowed.Set, value: input.TranscodeAllowed.Value},
		{column: "audio_transcode_allowed", set: input.AudioTranscodeAllowed.Set, value: input.AudioTranscodeAllowed.Value},
		{column: "max_profiles", set: input.MaxProfiles != nil, value: input.MaxProfiles},
		{column: "download_allowed", set: input.DownloadAllowed.Set, value: input.DownloadAllowed.Value},
		{column: "download_transcode_allowed", set: input.DownloadTranscodeAllowed.Set, value: input.DownloadTranscodeAllowed.Value},
		{column: "requests_allowed", set: input.RequestsAllowed.Set, value: input.RequestsAllowed.Value},
	}

	setClauses := []string{}
	accessPolicyPredicates := []string{}
	args := []any{}
	argIndex := 1
	for _, col := range columns {
		if !col.set {
			continue
		}
		placeholder := fmt.Sprintf("$%d", argIndex)
		setClauses = append(setClauses, fmt.Sprintf("%s = %s", col.column, placeholder))
		if col.bumpsAccessPolicy {
			accessPolicyPredicates = append(
				accessPolicyPredicates,
				fmt.Sprintf("%s IS DISTINCT FROM %s", col.column, placeholder),
			)
		}
		args = append(args, col.value)
		argIndex++
	}

	// access_group_id is not a plain userUpdateColumn: what gets written
	// depends on the row's current role, so it is assembled directly rather
	// than through the generic column loop above.
	var defaultGroupCTE string
	if setClause, predicate, cte, groupArgs, nextArgIndex := accessGroupSetClause(input, argIndex); setClause != "" {
		setClauses = append(setClauses, setClause)
		accessPolicyPredicates = append(accessPolicyPredicates, predicate)
		defaultGroupCTE = cte
		args = append(args, groupArgs...)
		argIndex = nextArgIndex
	}

	if len(setClauses) == 0 {
		// Nothing to update; still verify the user exists.
		_, err := r.GetByID(ctx, id)
		return err
	}

	if len(accessPolicyPredicates) > 0 {
		setClauses = append(setClauses, fmt.Sprintf(
			"access_policy_revision = CASE WHEN %s THEN access_policy_revision + 1 ELSE access_policy_revision END",
			strings.Join(accessPolicyPredicates, " OR "),
		))
	}

	// Always bump updated_at.
	setClauses = append(setClauses, "updated_at = NOW()")

	var query string
	if defaultGroupCTE != "" {
		query = fmt.Sprintf("WITH %s UPDATE users SET %s WHERE id = $%d",
			defaultGroupCTE, strings.Join(setClauses, ", "), argIndex)
	} else {
		query = fmt.Sprintf("UPDATE users SET %s WHERE id = $%d",
			strings.Join(setClauses, ", "), argIndex)
	}
	args = append(args, id)

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		if isDuplicateKeyError(err) {
			return fmt.Errorf("%w: %s", ErrDuplicate, extractConstraint(err))
		}
		return fmt.Errorf("updating user: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

// CompareAndSwapPassword replaces the bcrypt hash only if it is still the one
// the caller verified. Concurrent password changes using the same old password
// therefore cannot both succeed with different replacements.
func (r *UserRepository) CompareAndSwapPassword(ctx context.Context, id int, expectedHash, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE users
		SET password_hash = $1, updated_at = NOW()
		WHERE id = $2 AND password_hash = $3`, string(hash), id, expectedHash)
	if err != nil {
		return fmt.Errorf("updating password: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	if _, err := r.GetByID(ctx, id); err != nil {
		return err
	}
	return ErrCurrentPasswordInvalid
}

// Delete removes a user by their ID.
func (r *UserRepository) Delete(ctx context.Context, id int) error {
	tag, err := r.pool.Exec(ctx, "DELETE FROM users WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("deleting user: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

// List returns all users ordered by ID ascending.
func (r *UserRepository) List(ctx context.Context) ([]*models.User, error) {
	query := `SELECT ` + allColumns + ` FROM users ORDER BY id ASC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

	return scanUsers(rows)
}

// Count returns the number of users in the database.
func (r *UserRepository) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return 0, fmt.Errorf("counting users: %w", err)
	}
	return count, nil
}

// isDuplicateKeyError checks if the error is a PostgreSQL unique_violation (code 23505).
func isDuplicateKeyError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// extractConstraint extracts the constraint name from a PgError for diagnostic messages.
func extractConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return "unknown"
}

// normalizeQualityOverride keeps the stored quality preset canonical while
// preserving nil (inherit).
func normalizeQualityOverride(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := access.NormalizePlaybackQuality(*value)
	return &normalized
}

// derefSlice maps a nil pointer to a NULL array and a non-nil pointer to its
// (possibly empty) slice, so Postgres distinguishes "inherit" from "none".
func derefSlice(value *[]int) []int {
	if value == nil {
		return nil
	}
	if *value == nil {
		return []int{}
	}
	return *value
}
