package scanqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	ModeLibrary = "library"
	ModeSubtree = "subtree"
	ModeFile    = "file"

	StatusAccepted  = "accepted"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	libraryClaimAdvisoryLockID int64 = 8_500_001
)

var ErrScanRunNotFound = errors.New("scan run not found")

type CreateInput struct {
	LibraryID       int
	Mode            string
	Path            string
	Trigger         string
	AutoscanEventID *int64
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const scanRunColumns = `id, media_folder_id, mode, path, trigger, status, result_payload,
	error_message, autoscan_event_id, requested_at, started_at, completed_at, heartbeat_at, updated_at`

func scanRunRow(row pgx.Row) (*models.ScanRun, error) {
	var run models.ScanRun
	if err := row.Scan(
		&run.ID,
		&run.MediaFolderID,
		&run.Mode,
		&run.Path,
		&run.Trigger,
		&run.Status,
		&run.ResultPayload,
		&run.ErrorMessage,
		&run.AutoscanEventID,
		&run.RequestedAt,
		&run.StartedAt,
		&run.CompletedAt,
		&run.HeartbeatAt,
		&run.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrScanRunNotFound
		}
		return nil, fmt.Errorf("scan scan run row: %w", err)
	}
	return &run, nil
}

func scanRunRows(rows pgx.Rows) ([]*models.ScanRun, error) {
	defer rows.Close()

	runs := make([]*models.ScanRun, 0)
	for rows.Next() {
		run, err := scanRunRow(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scan runs: %w", err)
	}
	return runs, nil
}

func (r *Repository) Create(ctx context.Context, input CreateInput) (*models.ScanRun, bool, error) {
	run, err := scanRunRow(r.pool.QueryRow(ctx, `
		INSERT INTO scan_runs (
			id, media_folder_id, mode, path, trigger, status, autoscan_event_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+scanRunColumns,
		ulid.Make().String(),
		input.LibraryID,
		input.Mode,
		input.Path,
		input.Trigger,
		StatusAccepted,
		input.AutoscanEventID,
	))
	if err == nil {
		return run, true, nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		existing, lookupErr := r.GetActiveByScope(ctx, input.LibraryID, input.Mode, input.Path)
		if lookupErr != nil {
			return nil, false, lookupErr
		}
		return existing, false, nil
	}

	return nil, false, fmt.Errorf("create scan run: %w", err)
}

func (r *Repository) CreateBatch(ctx context.Context, inputs []CreateInput) ([]*models.ScanRun, []bool, error) {
	if len(inputs) == 0 {
		return []*models.ScanRun{}, []bool{}, nil
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("begin scan run batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	runs := make([]*models.ScanRun, 0, len(inputs))
	created := make([]bool, 0, len(inputs))
	for _, input := range inputs {
		run, err := scanRunRow(tx.QueryRow(ctx, `
			INSERT INTO scan_runs (
				id, media_folder_id, mode, path, trigger, status, autoscan_event_id
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT DO NOTHING
			RETURNING `+scanRunColumns,
			ulid.Make().String(),
			input.LibraryID,
			input.Mode,
			input.Path,
			input.Trigger,
			StatusAccepted,
			input.AutoscanEventID,
		))
		if err == nil {
			runs = append(runs, run)
			created = append(created, true)
			continue
		}
		if !errors.Is(err, ErrScanRunNotFound) {
			return nil, nil, fmt.Errorf("create scan run: %w", err)
		}

		existing, lookupErr := scanRunRow(tx.QueryRow(ctx, `
			SELECT `+scanRunColumns+`
			FROM scan_runs
			WHERE media_folder_id = $1
			  AND mode = $2
			  AND path = $3
			  AND status = ANY($4)
			ORDER BY requested_at ASC
			LIMIT 1`,
			input.LibraryID,
			input.Mode,
			input.Path,
			[]string{StatusAccepted, StatusRunning},
		))
		if lookupErr != nil {
			return nil, nil, lookupErr
		}
		runs = append(runs, existing)
		created = append(created, false)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit scan run batch: %w", err)
	}
	return runs, created, nil
}

func (r *Repository) GetActiveByScope(ctx context.Context, libraryID int, mode, path string) (*models.ScanRun, error) {
	return scanRunRow(r.pool.QueryRow(ctx, `
		SELECT `+scanRunColumns+`
		FROM scan_runs
		WHERE media_folder_id = $1
		  AND mode = $2
		  AND path = $3
		  AND status = ANY($4)
		ORDER BY requested_at ASC
		LIMIT 1`,
		libraryID,
		mode,
		path,
		[]string{StatusAccepted, StatusRunning},
	))
}

func (r *Repository) ListActive(ctx context.Context) ([]*models.ScanRun, error) {
	return r.listActive(ctx, 0)
}

func (r *Repository) ListActiveLimit(ctx context.Context, limit int) ([]*models.ScanRun, error) {
	return r.listActive(ctx, limit)
}

func (r *Repository) listActive(ctx context.Context, limit int) ([]*models.ScanRun, error) {
	query := `
		SELECT ` + scanRunColumns + `
		FROM scan_runs
		WHERE status = ANY($1)
		ORDER BY requested_at ASC`
	args := []any{[]string{StatusAccepted, StatusRunning}}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list active scan runs: %w", err)
	}
	return scanRunRows(rows)
}

func (r *Repository) ClaimNextAccepted(ctx context.Context, maxRunningLibraries, maxRunningScoped int) (*models.ScanRun, error) {
	if maxRunningLibraries < 1 {
		maxRunningLibraries = 1
	}
	if maxRunningScoped < 1 {
		maxRunningScoped = 1
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin scan claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, libraryClaimAdvisoryLockID).Scan(&locked); err != nil {
		return nil, fmt.Errorf("lock scan claim: %w", err)
	}
	if !locked {
		return nil, tx.Commit(ctx)
	}

	var runningLibraries int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM scan_runs
		WHERE mode = $1
		  AND status = $2`,
		ModeLibrary,
		StatusRunning,
	).Scan(&runningLibraries); err != nil {
		return nil, fmt.Errorf("count running library scan runs: %w", err)
	}

	var runningScoped int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM scan_runs
		WHERE mode = ANY($1)
		  AND status = $2`,
		[]string{ModeSubtree, ModeFile},
		StatusRunning,
	).Scan(&runningScoped); err != nil {
		return nil, fmt.Errorf("count running scoped scan runs: %w", err)
	}

	canClaimLibrary := runningLibraries < maxRunningLibraries
	canClaimScoped := runningScoped < maxRunningScoped
	if !canClaimLibrary && !canClaimScoped {
		return nil, tx.Commit(ctx)
	}

	var id string
	if err := tx.QueryRow(ctx, `
			SELECT id
			FROM scan_runs
			WHERE status = $1
			  AND (
				($2 AND mode = $3) OR
				($4 AND mode = ANY($5))
			  )
			ORDER BY requested_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1`,
		StatusAccepted,
		canClaimLibrary,
		ModeLibrary,
		canClaimScoped,
		[]string{ModeSubtree, ModeFile},
	).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, tx.Commit(ctx)
		}
		return nil, fmt.Errorf("claim scan run: %w", err)
	}

	run, err := scanRunRow(tx.QueryRow(ctx, `
		UPDATE scan_runs
		SET status = $2,
			started_at = NOW(),
			heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1
		RETURNING `+scanRunColumns,
		id,
		StatusRunning,
	))
	if err != nil {
		return nil, fmt.Errorf("mark scan run running: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit scan claim: %w", err)
	}
	return run, nil
}

func (r *Repository) TouchHeartbeat(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE scan_runs
		SET heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1
		  AND status = $2`,
		id,
		StatusRunning,
	)
	if err != nil {
		return fmt.Errorf("touch scan heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrScanRunNotFound
	}
	return nil
}

func (r *Repository) UpdateProgress(ctx context.Context, id string, result *evt.ScanRunResult) (*models.ScanRun, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal scan progress: %w", err)
	}
	return scanRunRow(r.pool.QueryRow(ctx, `
		UPDATE scan_runs
		SET result_payload = $2,
			updated_at = NOW()
		WHERE id = $1
		  AND status = $3
		RETURNING `+scanRunColumns,
		id,
		payload,
		StatusRunning,
	))
}

func (r *Repository) Complete(ctx context.Context, id string, result *evt.ScanRunResult) (*models.ScanRun, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal scan result: %w", err)
	}
	return scanRunRow(r.pool.QueryRow(ctx, `
		UPDATE scan_runs
		SET status = $2,
			result_payload = $3,
			error_message = '',
			completed_at = NOW(),
			heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1
		  AND status = $4
		RETURNING `+scanRunColumns,
		id,
		StatusCompleted,
		payload,
		StatusRunning,
	))
}

func (r *Repository) Fail(ctx context.Context, id string, errorMessage string) (*models.ScanRun, error) {
	return scanRunRow(r.pool.QueryRow(ctx, `
		UPDATE scan_runs
		SET status = $2,
			error_message = $3,
			completed_at = NOW(),
			heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1
		  AND status = $4
		RETURNING `+scanRunColumns,
		id,
		StatusFailed,
		errorMessage,
		StatusRunning,
	))
}

func (r *Repository) CancelAcceptedByLibrary(ctx context.Context, libraryID int) ([]*models.ScanRun, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE scan_runs
		SET status = $2,
			completed_at = NOW(),
			updated_at = NOW()
		WHERE media_folder_id = $1
		  AND status = $3
		RETURNING `+scanRunColumns,
		libraryID,
		StatusCancelled,
		StatusAccepted,
	)
	if err != nil {
		return nil, fmt.Errorf("cancel accepted scan runs: %w", err)
	}
	return scanRunRows(rows)
}

func (r *Repository) MarkCancelled(ctx context.Context, id string) (*models.ScanRun, bool, error) {
	run, err := scanRunRow(r.pool.QueryRow(ctx, `
		UPDATE scan_runs
		SET status = $2,
			completed_at = NOW(),
			heartbeat_at = NOW(),
			updated_at = NOW()
		WHERE id = $1
		  AND status = ANY($3)
		RETURNING `+scanRunColumns,
		id,
		StatusCancelled,
		[]string{StatusAccepted, StatusRunning},
	))
	if err == nil {
		return run, true, nil
	}
	if !errors.Is(err, ErrScanRunNotFound) {
		return nil, false, err
	}
	run, err = r.GetByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return run, false, nil
}

func (r *Repository) GetByID(ctx context.Context, id string) (*models.ScanRun, error) {
	return scanRunRow(r.pool.QueryRow(ctx, `
		SELECT `+scanRunColumns+`
		FROM scan_runs
		WHERE id = $1`,
		id,
	))
}

func (r *Repository) RequeueStaleRunning(ctx context.Context, before time.Time) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE scan_runs
		SET status = $2,
			started_at = NULL,
			heartbeat_at = NULL,
			completed_at = NULL,
			error_message = '',
			updated_at = NOW()
		WHERE status = $1
		  AND COALESCE(heartbeat_at, started_at, requested_at) < $3`,
		StatusRunning,
		StatusAccepted,
		before,
	)
	if err != nil {
		return 0, fmt.Errorf("requeue stale scan runs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
