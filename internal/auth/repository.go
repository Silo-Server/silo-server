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
	max_streams, max_transcodes, max_profiles, download_allowed,
	download_transcode_allowed, created_at, updated_at`

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
		&u.MaxProfiles,
		&u.DownloadAllowed,
		&u.DownloadTranscodeAllowed,
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
			&u.MaxProfiles,
			&u.DownloadAllowed,
			&u.DownloadTranscodeAllowed,
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

	permissions, err := NormalizePermissions(input.Permissions)
	if err != nil {
		return nil, err
	}

	cols := []string{"email", "username", "password_hash", "local_password_login_enabled", "role", "permissions", "library_ids", "max_playback_quality"}
	args := []any{
		NormalizeEmail(input.Email),
		NormalizeUsername(input.Username),
		string(hash),
		localPasswordLoginEnabled,
		input.Role,
		permissions,
		input.LibraryIDs,
		input.MaxPlaybackQuality,
	}

	// Optional columns: nil means use DB default.
	if input.MaxStreams != nil {
		cols = append(cols, "max_streams")
		args = append(args, *input.MaxStreams)
	}
	if input.MaxTranscodes != nil {
		cols = append(cols, "max_transcodes")
		args = append(args, *input.MaxTranscodes)
	}
	if input.MaxProfiles != nil {
		cols = append(cols, "max_profiles")
		args = append(args, *input.MaxProfiles)
	}
	if input.DownloadAllowed != nil {
		cols = append(cols, "download_allowed")
		args = append(args, *input.DownloadAllowed)
	}
	if input.DownloadTranscodeAllowed != nil {
		cols = append(cols, "download_transcode_allowed")
		args = append(args, *input.DownloadTranscodeAllowed)
	}

	// Build placeholders: $1, $2, ..., $N
	placeholders := make([]string, len(args))
	for i := range args {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
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

// Update modifies a user's fields. Only non-nil fields in the input are updated.
// If the input contains a Password, it is bcrypt-hashed before storage.
func (r *UserRepository) Update(ctx context.Context, id int, input models.UpdateUserInput) error {
	setClauses := []string{}
	accessPolicyPredicates := []string{}
	args := []any{}
	argIndex := 1

	if input.Email != nil {
		setClauses = append(setClauses, fmt.Sprintf("email = $%d", argIndex))
		args = append(args, NormalizeEmail(*input.Email))
		argIndex++
	}
	if input.Username != nil {
		setClauses = append(setClauses, fmt.Sprintf("username = $%d", argIndex))
		args = append(args, NormalizeUsername(*input.Username))
		argIndex++
	}
	if input.Password != nil {
		hash, err := bcrypt.GenerateFromPassword([]byte(*input.Password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hashing password: %w", err)
		}
		setClauses = append(setClauses, fmt.Sprintf("password_hash = $%d", argIndex))
		args = append(args, string(hash))
		argIndex++
	}
	if input.LocalPasswordLoginEnabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("local_password_login_enabled = $%d", argIndex))
		args = append(args, *input.LocalPasswordLoginEnabled)
		argIndex++
	}
	if input.Role != nil {
		setClauses = append(setClauses, fmt.Sprintf("role = $%d", argIndex))
		accessPolicyPredicates = append(accessPolicyPredicates, fmt.Sprintf("role IS DISTINCT FROM $%d", argIndex))
		args = append(args, *input.Role)
		argIndex++
	}
	if input.Permissions != nil {
		permissions, err := NormalizePermissions(*input.Permissions)
		if err != nil {
			return err
		}
		setClauses = append(setClauses, fmt.Sprintf("permissions = $%d", argIndex))
		accessPolicyPredicates = append(accessPolicyPredicates, fmt.Sprintf("permissions IS DISTINCT FROM $%d", argIndex))
		args = append(args, permissions)
		argIndex++
	}
	if input.Enabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("enabled = $%d", argIndex))
		accessPolicyPredicates = append(accessPolicyPredicates, fmt.Sprintf("enabled IS DISTINCT FROM $%d", argIndex))
		args = append(args, *input.Enabled)
		argIndex++
	}
	if input.LibraryIDs != nil {
		setClauses = append(setClauses, fmt.Sprintf("library_ids = $%d", argIndex))
		// Library scope is resolved from users.library_ids on each request, so
		// changing it must not invalidate durable profile/session tokens.
		args = append(args, *input.LibraryIDs)
		argIndex++
	}
	if input.MaxPlaybackQuality != nil {
		setClauses = append(setClauses, fmt.Sprintf("max_playback_quality = $%d", argIndex))
		accessPolicyPredicates = append(accessPolicyPredicates, fmt.Sprintf("max_playback_quality IS DISTINCT FROM $%d", argIndex))
		args = append(args, *input.MaxPlaybackQuality)
		argIndex++
	}
	if input.MaxStreams != nil {
		setClauses = append(setClauses, fmt.Sprintf("max_streams = $%d", argIndex))
		args = append(args, *input.MaxStreams)
		argIndex++
	}
	if input.MaxTranscodes != nil {
		setClauses = append(setClauses, fmt.Sprintf("max_transcodes = $%d", argIndex))
		args = append(args, *input.MaxTranscodes)
		argIndex++
	}
	if input.MaxProfiles != nil {
		setClauses = append(setClauses, fmt.Sprintf("max_profiles = $%d", argIndex))
		args = append(args, *input.MaxProfiles)
		argIndex++
	}
	if input.DownloadAllowed != nil {
		setClauses = append(setClauses, fmt.Sprintf("download_allowed = $%d", argIndex))
		args = append(args, *input.DownloadAllowed)
		argIndex++
	}
	if input.DownloadTranscodeAllowed != nil {
		setClauses = append(setClauses, fmt.Sprintf("download_transcode_allowed = $%d", argIndex))
		args = append(args, *input.DownloadTranscodeAllowed)
		argIndex++
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

	query := fmt.Sprintf("UPDATE users SET %s WHERE id = $%d",
		strings.Join(setClauses, ", "), argIndex)
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
