package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// PgTriggerRepository implements taskmanager.TriggerRepository using PostgreSQL.
type PgTriggerRepository struct {
	pool *pgxpool.Pool
}

func NewPgTriggerRepository(pool *pgxpool.Pool) *PgTriggerRepository {
	return &PgTriggerRepository{pool: pool}
}

func (r *PgTriggerRepository) GetTriggers(ctx context.Context, taskKey string) ([]taskmanager.TriggerConfig, error) {
	snapshot, err := r.GetSchedule(ctx, taskKey)
	if err != nil {
		return nil, err
	}
	if snapshot.Revision == 0 {
		return nil, nil
	}
	return snapshot.Triggers, nil
}

func (r *PgTriggerRepository) GetSchedule(ctx context.Context, taskKey string) (taskmanager.Schedule, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return taskmanager.Schedule{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	snapshot, err := readSchedule(ctx, tx, taskKey)
	if err != nil {
		return taskmanager.Schedule{}, err
	}
	return snapshot, tx.Commit(ctx)
}

func (r *PgTriggerRepository) GetOrCreateTriggers(ctx context.Context, taskKey string, defaults []taskmanager.TriggerConfig) ([]taskmanager.TriggerConfig, error) {
	// Revision zero requires an absent schedule. Use the same lock and revision
	// guard as administrator edits so startup cannot overwrite a saved schedule.
	saved, err := r.ReplaceSchedule(ctx, taskKey, 0, defaults)
	if _, conflict := errors.AsType[*taskmanager.ScheduleConflict](err); conflict {
		return r.GetTriggers(ctx, taskKey)
	}
	if err != nil {
		return nil, err
	}
	return saved.Triggers, nil
}

func readSchedule(ctx context.Context, tx pgx.Tx, taskKey string) (taskmanager.Schedule, error) {
	var snapshot taskmanager.Schedule
	snapshot.Triggers = []taskmanager.TriggerConfig{}
	err := tx.QueryRow(ctx, `SELECT revision FROM task_schedules WHERE task_key=$1`, taskKey).Scan(&snapshot.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	rows, err := tx.Query(ctx, `SELECT type, interval, time_of_day, day_of_week, max_runtime FROM task_triggers WHERE task_key=$1 ORDER BY id`, taskKey)
	if err != nil {
		return snapshot, fmt.Errorf("getting task triggers: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cfg       taskmanager.TriggerConfig
			trigType  string
			interval  *int64
			timeOfDay *string
			dayOfWeek *int
			maxRT     *int64
		)
		if err := rows.Scan(&trigType, &interval, &timeOfDay, &dayOfWeek, &maxRT); err != nil {
			return snapshot, fmt.Errorf("scanning task trigger: %w", err)
		}
		cfg.Type = taskmanager.TriggerType(trigType)
		if interval != nil {
			cfg.IntervalMs = *interval
		}
		if timeOfDay != nil {
			cfg.TimeOfDay = *timeOfDay
		}
		if dayOfWeek != nil {
			cfg.DayOfWeek = *dayOfWeek
		}
		if maxRT != nil {
			cfg.MaxRuntimeMs = *maxRT
		}
		snapshot.Triggers = append(snapshot.Triggers, cfg)
	}
	return snapshot, rows.Err()
}

func (r *PgTriggerRepository) SetTriggers(ctx context.Context, taskKey string, triggers []taskmanager.TriggerConfig) error {
	_, err := r.ReplaceSchedule(ctx, taskKey, -1, triggers)
	return err
}

// ReplaceSchedule compares the original persisted revision while holding the
// parent row lock. Legacy SetTriggers uses the same transaction with a wildcard.
func (r *PgTriggerRepository) ReplaceSchedule(ctx context.Context, taskKey string, expected int64, triggers []taskmanager.TriggerConfig) (taskmanager.Schedule, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return taskmanager.Schedule{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inserted, err := tx.Exec(ctx, `INSERT INTO task_schedules (task_key) VALUES ($1) ON CONFLICT DO NOTHING`, taskKey)
	if err != nil {
		return taskmanager.Schedule{}, err
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM task_schedules WHERE task_key=$1 FOR UPDATE`, taskKey).Scan(&revision); err != nil {
		return taskmanager.Schedule{}, err
	}
	observed := revision
	if inserted.RowsAffected() == 1 {
		observed = 0
	}
	if expected != -1 && expected != observed {
		return taskmanager.Schedule{}, &taskmanager.ScheduleConflict{Actual: observed}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM task_triggers WHERE task_key = $1`, taskKey); err != nil {
		return taskmanager.Schedule{}, fmt.Errorf("deleting old triggers: %w", err)
	}

	for _, cfg := range triggers {
		var interval *int64
		if cfg.IntervalMs > 0 {
			interval = &cfg.IntervalMs
		}
		var timeOfDay *string
		if cfg.TimeOfDay != "" {
			timeOfDay = &cfg.TimeOfDay
		}
		var dayOfWeek *int
		if cfg.Type == taskmanager.TriggerTypeWeekly {
			dayOfWeek = &cfg.DayOfWeek
		}
		var maxRT *int64
		if cfg.MaxRuntimeMs > 0 {
			maxRT = &cfg.MaxRuntimeMs
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO task_triggers (task_key, type, interval, time_of_day, day_of_week, max_runtime)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			taskKey, string(cfg.Type), interval, timeOfDay, dayOfWeek, maxRT,
		); err != nil {
			return taskmanager.Schedule{}, fmt.Errorf("inserting trigger: %w", err)
		}
	}

	if err := tx.QueryRow(ctx, `UPDATE task_schedules SET revision=revision+1 WHERE task_key=$1 RETURNING revision`, taskKey).Scan(&revision); err != nil {
		return taskmanager.Schedule{}, err
	}
	if triggers == nil {
		triggers = []taskmanager.TriggerConfig{}
	}
	return taskmanager.Schedule{Revision: revision, Triggers: triggers}, tx.Commit(ctx)
}
