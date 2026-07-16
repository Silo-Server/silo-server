package historyimport

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrMappingNotFound  = errors.New("history import user mapping not found")
	ErrMappingDuplicate = errors.New("a mapping for this external user already exists on this source")
	ErrNoAdminToken     = errors.New("history import source has no admin token configured")
	ErrActiveRunExists  = errors.New("an import run for this mapping is already active")
)

// --- Source admin token ---

// SetSourceAdminToken stores an admin token for the given source.
func (r *Repository) SetSourceAdminToken(ctx context.Context, sourceID int, token string) error {
	encryptedToken, err := r.encryptSourceAdminToken(sourceID, token)
	if err != nil {
		return fmt.Errorf("encrypt admin token for source %d: %w", sourceID, err)
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_sources
		SET admin_token = $2, updated_at = NOW()
		WHERE id = $1`, sourceID, encryptedToken)
	if err != nil {
		return fmt.Errorf("setting admin token for source %d: %w", sourceID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrSourceNotFound
	}
	return nil
}

// ClearSourceAdminToken removes the stored admin token from the given source.
func (r *Repository) ClearSourceAdminToken(ctx context.Context, sourceID int) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_sources
		SET admin_token = NULL, updated_at = NOW()
		WHERE id = $1`, sourceID)
	if err != nil {
		return fmt.Errorf("clearing admin token for source %d: %w", sourceID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrSourceNotFound
	}
	return nil
}

// GetSourceWithAdminToken returns the source and its admin token.
// The raw token is for internal service use only and must not be returned in API responses.
func (r *Repository) GetSourceWithAdminToken(ctx context.Context, sourceID int) (*Source, string, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, source_type, base_url, COALESCE(system_id, ''), enabled, sort_order,
		       (admin_token IS NOT NULL) AS has_admin_token,
		       COALESCE(admin_token, ''),
		       created_at, updated_at
		FROM history_import_sources
		WHERE id = $1`, sourceID)

	var s Source
	var adminToken string
	err := row.Scan(
		&s.ID, &s.Name, &s.SourceType, &s.BaseURL, &s.SystemID,
		&s.Enabled, &s.SortOrder, &s.HasAdminToken, &adminToken,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrSourceNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("getting source %d with admin token: %w", sourceID, err)
	}
	adminToken, err = r.cipher.DecryptIfEncrypted(adminToken, sourceAdminTokenAAD(sourceID))
	if err != nil {
		return nil, "", fmt.Errorf("decrypt admin token for source %d: %w", sourceID, err)
	}
	return &s, adminToken, nil
}

// --- Mapping CRUD ---

func (r *Repository) CreateMapping(ctx context.Context, input CreateMappingInput) (*UserMapping, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO history_import_user_mappings
			(source_id, external_user_id, external_user_name, silo_user_id, silo_profile_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, source_id, external_user_id, external_user_name,
		          silo_user_id, silo_profile_id, last_imported_at,
		          created_at, updated_at`,
		input.SourceID, input.ExternalUserID, input.ExternalUserName,
		input.SiloUserID, input.SiloProfileID,
	)
	m, err := scanMapping(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrMappingDuplicate
		}
		return nil, fmt.Errorf("creating history import mapping: %w", err)
	}
	return enrichMapping(ctx, r, m)
}

func (r *Repository) ListMappingsForSource(ctx context.Context, sourceID int) ([]UserMapping, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.id, m.source_id, m.external_user_id, m.external_user_name,
		       m.silo_user_id, m.silo_profile_id, m.last_imported_at,
		       m.created_at, m.updated_at
		FROM history_import_user_mappings m
		WHERE m.source_id = $1
		ORDER BY m.external_user_name ASC, m.id ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("listing mappings for source %d: %w", sourceID, err)
	}
	defer rows.Close()

	var mappings []UserMapping
	for rows.Next() {
		m, err := scanMappingRow(rows)
		if err != nil {
			return nil, err
		}
		mappings = append(mappings, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating mappings: %w", err)
	}

	// Enrich with silo user/profile names.
	for i := range mappings {
		if enriched, err := enrichMapping(ctx, r, &mappings[i]); err == nil {
			mappings[i] = *enriched
		}
	}
	return mappings, nil
}

// ListMappingsForBulkRun returns mappings for a source that do NOT currently have an active run.
func (r *Repository) ListMappingsForBulkRun(ctx context.Context, sourceID int) ([]UserMapping, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.id, m.source_id, m.external_user_id, m.external_user_name,
		       m.silo_user_id, m.silo_profile_id, m.last_imported_at,
		       m.created_at, m.updated_at
		FROM history_import_user_mappings m
		WHERE m.source_id = $1
		  AND NOT EXISTS (
		      SELECT 1 FROM history_import_runs r
		      WHERE r.mapping_id = m.id
		        AND r.status IN ('queued', 'running')
		  )
		ORDER BY m.external_user_name ASC, m.id ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("listing bulk-run mappings for source %d: %w", sourceID, err)
	}
	defer rows.Close()

	var mappings []UserMapping
	for rows.Next() {
		m, err := scanMappingRow(rows)
		if err != nil {
			return nil, err
		}
		mappings = append(mappings, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating bulk-run mappings: %w", err)
	}
	return mappings, nil
}

func (r *Repository) GetMappingByID(ctx context.Context, id int) (*UserMapping, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, source_id, external_user_id, external_user_name,
		       silo_user_id, silo_profile_id, last_imported_at,
		       created_at, updated_at
		FROM history_import_user_mappings
		WHERE id = $1`, id)
	m, err := scanMapping(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMappingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting mapping %d: %w", id, err)
	}
	return enrichMapping(ctx, r, m)
}

func (r *Repository) UpdateMapping(ctx context.Context, id int, input UpdateMappingInput) (*UserMapping, error) {
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_user_mappings
		SET silo_user_id    = COALESCE($2, silo_user_id),
		    silo_profile_id = COALESCE($3, silo_profile_id),
		    updated_at           = NOW()
		WHERE id = $1`,
		id, input.SiloUserID, input.SiloProfileID,
	)
	if err != nil {
		return nil, fmt.Errorf("updating mapping %d: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return nil, ErrMappingNotFound
	}
	return r.GetMappingByID(ctx, id)
}

func (r *Repository) DeleteMapping(ctx context.Context, id int) error {
	result, err := r.pool.Exec(ctx, `DELETE FROM history_import_user_mappings WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting mapping %d: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return ErrMappingNotFound
	}
	return nil
}

// HasActiveRunForMapping reports whether a queued or running run already exists for the mapping.
func (r *Repository) HasActiveRunForMapping(ctx context.Context, mappingID int) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM history_import_runs
		    WHERE mapping_id = $1 AND status IN ('queued', 'running')
		)`, mappingID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking active run for mapping %d: %w", mappingID, err)
	}
	return exists, nil
}

// TouchMappingLastImported updates last_imported_at to the current time.
func (r *Repository) TouchMappingLastImported(ctx context.Context, mappingID int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE history_import_user_mappings
		SET last_imported_at = NOW(), updated_at = NOW()
		WHERE id = $1`, mappingID)
	if err != nil {
		return fmt.Errorf("touching mapping %d last_imported_at: %w", mappingID, err)
	}
	return nil
}

// --- Admin-scoped run queries ---

// ListAllRuns returns recent runs across all users. If sourceID is non-nil, only runs
// whose mapping belongs to that source are returned.
func (r *Repository) ListAllRuns(ctx context.Context, sourceID *int, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 200 {
		limit = 200
	}

	var rows interface {
		Close()
		Next() bool
		Scan(dest ...any) error
		Err() error
	}
	var err error

	if sourceID != nil {
		rows, err = r.pool.Query(ctx, `
			SELECT r.id, r.user_id, r.profile_id, r.source_type, r.connection_mode, r.status,
			       r.mapping_id,
			       r.fetched, r.matched, r.unmatched, r.progress_updated, r.history_created, r.watchlist_added, r.favorites_imported, r.skipped,
			       r.warnings, r.unmatched_samples, COALESCE(r.error_message, ''),
			       r.created_at, r.started_at, r.completed_at
			FROM history_import_runs r
			LEFT JOIN history_import_user_mappings m ON r.mapping_id = m.id
			WHERE m.source_id = $1
			ORDER BY r.created_at DESC
			LIMIT $2`, *sourceID, limit)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, user_id, profile_id, source_type, connection_mode, status,
			       mapping_id,
			       fetched, matched, unmatched, progress_updated, history_created, watchlist_added, favorites_imported, skipped,
			       warnings, unmatched_samples, COALESCE(error_message, ''),
			       created_at, started_at, completed_at
			FROM history_import_runs
			ORDER BY created_at DESC
			LIMIT $1`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("listing all history import runs: %w", err)
	}
	defer rows.Close()
	return scanAdminRuns(rows)
}

// GetRunByID returns any run by ID regardless of which user owns it.
func (r *Repository) GetRunByID(ctx context.Context, runID string) (*Run, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, source_type, connection_mode, status,
		       mapping_id,
		       fetched, matched, unmatched, progress_updated, history_created, watchlist_added, favorites_imported, skipped,
		       warnings, unmatched_samples, COALESCE(error_message, ''),
		       created_at, started_at, completed_at
		FROM history_import_runs
		WHERE id = $1`, runID)
	run, err := scanAdminRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting run %s: %w", runID, err)
	}
	return run, nil
}

func (r *Repository) ListActiveRuns(ctx context.Context, sourceID *int) ([]Run, error) {
	var rows interface {
		Close()
		Next() bool
		Scan(dest ...any) error
		Err() error
	}
	var err error

	if sourceID != nil {
		rows, err = r.pool.Query(ctx, `
			SELECT r.id, r.user_id, r.profile_id, r.source_type, r.connection_mode, r.status,
			       r.mapping_id,
			       r.fetched, r.matched, r.unmatched, r.progress_updated, r.history_created, r.watchlist_added, r.favorites_imported, r.skipped,
			       r.warnings, r.unmatched_samples, COALESCE(r.error_message, ''),
			       r.created_at, r.started_at, r.completed_at
			FROM history_import_runs r
			LEFT JOIN history_import_user_mappings m ON r.mapping_id = m.id
			WHERE m.source_id = $1
			  AND r.status IN ($2, $3)
			ORDER BY r.created_at DESC`, *sourceID, RunStatusQueued, RunStatusRunning)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, user_id, profile_id, source_type, connection_mode, status,
			       mapping_id,
			       fetched, matched, unmatched, progress_updated, history_created, watchlist_added, favorites_imported, skipped,
			       warnings, unmatched_samples, COALESCE(error_message, ''),
			       created_at, started_at, completed_at
			FROM history_import_runs
			WHERE status IN ($1, $2)
			ORDER BY created_at DESC`, RunStatusQueued, RunStatusRunning)
	}
	if err != nil {
		return nil, fmt.Errorf("listing active history import runs: %w", err)
	}
	defer rows.Close()
	return scanAdminRuns(rows)
}

// CancelRunIfActive sets the run to failed status if it is currently queued or running.
// Returns ErrRunNotFound if the run doesn't exist or is already in a terminal state.
func (r *Repository) CancelRunIfActive(ctx context.Context, runID string) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE history_import_runs
		SET status        = 'cancelled',
		    error_message = 'Cancelled by admin',
		    completed_at  = NOW()
		WHERE id = $1 AND status IN ('queued', 'running')`, runID)
	if err != nil {
		return fmt.Errorf("cancelling run %s: %w", runID, err)
	}
	if result.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

// --- Scan helpers ---

type mappingScanner interface {
	Scan(dest ...any) error
}

func scanMapping(s mappingScanner) (*UserMapping, error) {
	var m UserMapping
	var lastImportedAt *time.Time
	err := s.Scan(
		&m.ID, &m.SourceID, &m.ExternalUserID, &m.ExternalUserName,
		&m.SiloUserID, &m.SiloProfileID, &lastImportedAt,
		&m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	m.LastImportedAt = lastImportedAt
	return &m, nil
}

type mappingRowScanner interface {
	Scan(dest ...any) error
}

func scanMappingRow(s mappingRowScanner) (*UserMapping, error) {
	return scanMapping(s)
}

// enrichMapping looks up the Silo username and profile name for display purposes.
func enrichMapping(ctx context.Context, r *Repository, m *UserMapping) (*UserMapping, error) {
	_ = r.pool.QueryRow(ctx, `
		SELECT u.username, COALESCE(p.name, '')
		FROM users u
		LEFT JOIN user_profiles p ON p.user_id = u.id AND p.id = $2
		WHERE u.id = $1`, m.SiloUserID, m.SiloProfileID,
	).Scan(&m.SiloUsername, &m.SiloProfileName)
	// Non-fatal — enrichment is best-effort for display purposes.
	return m, nil
}

type adminRunScanner interface {
	Scan(dest ...any) error
}

func scanAdminRun(s adminRunScanner) (*Run, error) {
	return scanRunWithMappingID(s)
}

type adminRunsRowSet interface {
	Close()
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanAdminRuns(rows adminRunsRowSet) ([]Run, error) {
	var runs []Run
	for rows.Next() {
		run, err := scanRunWithMappingID(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating admin runs: %w", err)
	}
	return runs, nil
}
