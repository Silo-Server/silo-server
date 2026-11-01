package backup

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Operations the status row names.
const (
	OperationBackup = "backup"
	OperationDelete = "delete"
)

// Last-run statuses besides the manifest statuses.
const (
	RunFailed      = "failed"
	RunInterrupted = "interrupted"
)

// interruptedRunError is what an interrupted run's error says.
const interruptedRunError = "the server running this backup stopped before it finished"

// ActiveOperation is what the lock holder is doing, published so every API
// server can show it.
type ActiveOperation struct {
	Operation string    `json:"operation"`
	BackupID  string    `json:"backup_id,omitempty"`
	Kind      Kind      `json:"kind,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Progress  float64   `json:"progress"`
	Message   string    `json:"message,omitempty"`
	// Lock is the session holding the subsystem lock for this operation. A
	// reader believes the row only while that session still holds it.
	Lock LockSession `json:"-"`
}

// LastRun is the outcome of the latest backup run that held the lock.
type LastRun struct {
	ID         string    `json:"id"`
	Kind       Kind      `json:"kind"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Error      string    `json:"error,omitempty"`
}

// succeeded reports a run that wrote its manifest.
func (r *LastRun) succeeded() bool {
	return r.Status == string(StatusComplete) || r.Status == string(StatusCompleteWithWarnings)
}

// StatusRecord is the published state.
type StatusRecord struct {
	Active *ActiveOperation
	Last   *LastRun
	// LastSuccessAt is when the latest successful run finished, at whichever
	// destination it wrote to.
	LastSuccessAt *time.Time
}

// StatusStore publishes what the lock holder is doing. Writes run on the
// held lock's own session and apply only while that session holds the lock,
// and each operation's writes after Begin carry the token Begin returned, so
// a holder that lost the lock can never change what its successor published.
type StatusStore interface {
	// Begin publishes op and returns its token. An active operation already
	// in the row belongs to a holder that lost the lock: a backup is first
	// recorded as the latest run, interrupted. It returns ErrNotLockHolder
	// when held's session no longer holds the lock.
	Begin(ctx context.Context, held Held, op ActiveOperation) (token string, err error)
	// Progress applies only while the row still carries token.
	Progress(ctx context.Context, held Held, token string, percent float64, message string) error
	// End clears the active operation and, when last is set, records it
	// (and, when it succeeded, its finish as the last success), only while
	// the row still carries token.
	End(ctx context.Context, held Held, token string, last *LastRun) error
	Read(ctx context.Context) (StatusRecord, error)
}

// ErrNotLockHolder refuses a status write from a session that does not hold
// the subsystem lock.
var ErrNotLockHolder = errors.New("this session does not hold the backup lock")

// holdsLock is true only on the session holding the subsystem lock. Every
// status write carries it, so a write sent by a holder whose lock has gone
// changes nothing, whatever reached the server first.
var holdsLock = fmt.Sprintf(`EXISTS (SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid() AND locktype = 'advisory'
	AND granted AND objsubid = 1 AND classid::bigint = %d AND objid::bigint = %d)`, lockClassID, lockObjID)

// PostgresStatus keeps the status in the backup_status row. Writes run on the
// held lock's session; Read uses the pool.
type PostgresStatus struct {
	Pool *pgxpool.Pool
}

func (p PostgresStatus) Begin(ctx context.Context, held Held, op ActiveOperation) (string, error) {
	token := rand.Text()
	if _, err := held.Exec(ctx, `INSERT INTO backup_status (singleton) SELECT TRUE WHERE `+holdsLock+` ON CONFLICT (singleton) DO NOTHING`); err != nil {
		return "", err
	}
	// One statement: the SET expressions read the row as it was, so a stale
	// backup becomes the interrupted last run in the same write that
	// publishes the new operation.
	tag, err := held.Exec(ctx, `
		UPDATE backup_status SET
			last_run_id          = CASE WHEN active_operation = 'backup' THEN active_backup_id ELSE last_run_id END,
			last_run_kind        = CASE WHEN active_operation = 'backup' THEN active_kind ELSE last_run_kind END,
			last_run_status      = CASE WHEN active_operation = 'backup' THEN 'interrupted' ELSE last_run_status END,
			last_run_started_at  = CASE WHEN active_operation = 'backup' THEN active_started_at ELSE last_run_started_at END,
			last_run_finished_at = CASE WHEN active_operation = 'backup' THEN NULL ELSE last_run_finished_at END,
			last_run_error       = CASE WHEN active_operation = 'backup' THEN $7 ELSE last_run_error END,
			active_operation = $1, active_backup_id = $2, active_kind = $3, active_started_at = $4,
			active_token = $5, active_lock_pid = $6, active_lock_backend_start = $8,
			active_progress = 0, active_message = '', updated_at = now()
		WHERE singleton AND `+holdsLock,
		op.Operation, op.BackupID, string(op.Kind), op.StartedAt, token, op.Lock.PID, interruptedRunError, nullTime(op.Lock.Start))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotLockHolder
	}
	return token, nil
}

func (p PostgresStatus) Progress(ctx context.Context, held Held, token string, percent float64, message string) error {
	_, err := held.Exec(ctx, `UPDATE backup_status SET active_progress = $2, active_message = $3, updated_at = now()
		WHERE singleton AND active_token = $1 AND $1 <> '' AND `+holdsLock, token, percent, message)
	return err
}

func (p PostgresStatus) End(ctx context.Context, held Held, token string, last *LastRun) error {
	const clearActive = `active_operation = '', active_backup_id = '', active_kind = '', active_token = '', active_lock_pid = 0, active_lock_backend_start = NULL,
		active_started_at = NULL, active_progress = 0, active_message = '', updated_at = now()`
	if last == nil {
		_, err := held.Exec(ctx, `UPDATE backup_status SET `+clearActive+` WHERE singleton AND active_token = $1 AND $1 <> '' AND `+holdsLock, token)
		return err
	}
	var finished *time.Time
	if !last.FinishedAt.IsZero() {
		finished = &last.FinishedAt
	}
	_, err := held.Exec(ctx, `UPDATE backup_status SET `+clearActive+`,
			last_run_id = $2, last_run_kind = $3, last_run_status = $4, last_run_started_at = $5,
			last_run_finished_at = $6, last_run_error = $7,
			last_success_at = CASE WHEN $8 THEN $6 ELSE last_success_at END
		WHERE singleton AND active_token = $1 AND $1 <> '' AND `+holdsLock,
		token, last.ID, string(last.Kind), last.Status, last.StartedAt, finished, last.Error, last.succeeded() && finished != nil)
	return err
}

func (p PostgresStatus) Read(ctx context.Context) (StatusRecord, error) {
	var (
		op, activeID, activeKind                string
		activeMessage                           string
		activeStarted                           *time.Time
		progress                                float64
		lockPID                                 int32
		lockStart                               *time.Time
		lastID, lastKind, lastStatus, lastError string
		lastStarted, lastFinished, lastSuccess  *time.Time
	)
	err := p.Pool.QueryRow(ctx, `
		SELECT active_operation, active_backup_id, active_kind, active_started_at, active_progress, active_message, active_lock_pid, active_lock_backend_start,
			last_run_id, last_run_kind, last_run_status, last_run_started_at, last_run_finished_at, last_run_error, last_success_at
		FROM backup_status WHERE singleton`).Scan(&op, &activeID, &activeKind, &activeStarted, &progress, &activeMessage, &lockPID, &lockStart,
		&lastID, &lastKind, &lastStatus, &lastStarted, &lastFinished, &lastError, &lastSuccess)
	if errors.Is(err, pgx.ErrNoRows) {
		return StatusRecord{}, nil
	}
	if err != nil {
		return StatusRecord{}, err
	}
	rec := StatusRecord{LastSuccessAt: lastSuccess}
	if op != "" {
		rec.Active = &ActiveOperation{Operation: op, BackupID: activeID, Kind: Kind(activeKind), Progress: progress, Message: activeMessage, Lock: LockSession{PID: lockPID}}
		if lockStart != nil {
			rec.Active.Lock.Start = *lockStart
		}
		if activeStarted != nil {
			rec.Active.StartedAt = *activeStarted
		}
	}
	if lastID != "" {
		rec.Last = &LastRun{ID: lastID, Kind: Kind(lastKind), Status: lastStatus, Error: lastError}
		if lastStarted != nil {
			rec.Last.StartedAt = *lastStarted
		}
		if lastFinished != nil {
			rec.Last.FinishedAt = *lastFinished
		}
	}
	return rec, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
