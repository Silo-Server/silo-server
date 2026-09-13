package repository

import (
	"context"
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

func (r *PgTriggerRepository) GetTriggers(ctx context.Context, taskKey string) ([]taskmanager.TriggerConfig, bool, error) {
	return getTriggers(ctx, r.pool, taskKey)
}

func (r *PgTriggerRepository) GetOrCreateTriggers(ctx context.Context, taskKey string, defaults []taskmanager.TriggerConfig) ([]taskmanager.TriggerConfig, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := lockTriggerSet(ctx, tx, taskKey)
	if err != nil {
		return nil, err
	}
	configs, _, err := getTriggers(ctx, tx, taskKey)
	if err != nil {
		return nil, err
	}
	// Preserve legacy trigger rows too, including tasks first registered by an
	// older server after the migration's backfill ran.
	if created && len(configs) == 0 {
		if err := insertTriggers(ctx, tx, taskKey, defaults); err != nil {
			return nil, err
		}
		configs = defaults
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit triggers: %w", err)
	}
	return configs, nil
}

// The parent row survives clearing the triggers and serializes initialization
// and replacement across server instances, even for an empty schedule.
func lockTriggerSet(ctx context.Context, tx pgx.Tx, taskKey string) (bool, error) {
	result, err := tx.Exec(ctx, `INSERT INTO task_trigger_sets (task_key)
		VALUES ($1) ON CONFLICT (task_key) DO NOTHING`, taskKey)
	if err != nil {
		return false, fmt.Errorf("creating trigger set: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT task_key FROM task_trigger_sets WHERE task_key = $1 FOR UPDATE`, taskKey); err != nil {
		return false, fmt.Errorf("locking trigger set: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

type triggerQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func getTriggers(ctx context.Context, db triggerQuerier, taskKey string) ([]taskmanager.TriggerConfig, bool, error) {
	// Read existence and trigger rows in one snapshot. Legacy trigger rows also
	// count as saved, even if an older server wrote them after the backfill.
	rows, err := db.Query(ctx, `
		SELECT t.type, t.interval, t.time_of_day, t.day_of_week, t.max_runtime
		FROM (SELECT $1::text AS task_key) requested
		LEFT JOIN task_trigger_sets s USING (task_key)
		LEFT JOIN task_triggers t USING (task_key)
		WHERE s.task_key IS NOT NULL OR t.id IS NOT NULL
		ORDER BY t.id`, taskKey,
	)
	if err != nil {
		return nil, false, fmt.Errorf("getting task triggers: %w", err)
	}
	defer rows.Close()

	var configs []taskmanager.TriggerConfig
	exists := false
	for rows.Next() {
		exists = true
		var (
			cfg       taskmanager.TriggerConfig
			trigType  *string
			interval  *int64
			timeOfDay *string
			dayOfWeek *int
			maxRT     *int64
		)
		if err := rows.Scan(&trigType, &interval, &timeOfDay, &dayOfWeek, &maxRT); err != nil {
			return nil, false, fmt.Errorf("scanning task trigger: %w", err)
		}
		if trigType == nil {
			continue // The schedule exists but its trigger list is empty.
		}
		cfg.Type = taskmanager.TriggerType(*trigType)
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
		configs = append(configs, cfg)
	}
	return configs, exists, rows.Err()
}

func (r *PgTriggerRepository) SetTriggers(ctx context.Context, taskKey string, triggers []taskmanager.TriggerConfig) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := lockTriggerSet(ctx, tx, taskKey); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_triggers WHERE task_key = $1`, taskKey); err != nil {
		return fmt.Errorf("deleting old triggers: %w", err)
	}
	if err := insertTriggers(ctx, tx, taskKey, triggers); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func insertTriggers(ctx context.Context, tx pgx.Tx, taskKey string, triggers []taskmanager.TriggerConfig) error {
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
			return fmt.Errorf("inserting trigger: %w", err)
		}
	}

	return nil
}
