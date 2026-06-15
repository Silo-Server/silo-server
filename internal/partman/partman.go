package partman

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgCheckViolation is the SQLSTATE Postgres returns when CREATE … PARTITION OF
// (or ATTACH PARTITION) would move rows out of the default partition — i.e. the
// default partition already holds a row that belongs in the new partition's
// range. See incident docs/continuum-to-silo-postgres-migration.md.
const pgCheckViolation = "23514"

const deleteBatchSize = 10000

var partitionBoundsRE = regexp.MustCompile(`FROM \('([^']+)'\) TO \('([^']+)'\)`)

type Granularity int

const (
	Daily Granularity = iota + 1
	Weekly
)

type Manager struct {
	pool        *pgxpool.Pool
	table       string
	granularity Granularity
	createAhead int
}

func NewManager(pool *pgxpool.Pool, table string, granularity Granularity, createAhead int) *Manager {
	return &Manager{
		pool:        pool,
		table:       table,
		granularity: granularity,
		createAhead: createAhead,
	}
}

func (m *Manager) EnsureFuturePartitions(ctx context.Context) error {
	if m == nil {
		return nil
	}

	start := m.granularity.truncate(time.Now().UTC())
	for i := 0; i <= m.createAhead; i++ {
		lower := m.granularity.addPeriods(start, i)
		upper := m.granularity.next(lower)
		name := m.partitionName(lower)
		_, err := m.pool.Exec(ctx, fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS public.%s PARTITION OF public.%s FOR VALUES FROM (%s) TO (%s)`,
			quoteIdent(name),
			quoteIdent(m.table),
			quoteLiteralTimestamp(lower),
			quoteLiteralTimestamp(upper),
		))
		if err == nil {
			continue
		}

		// The create fails with a check_violation only when the default
		// partition already holds rows that belong in this partition's range.
		// Rather than treating that as fatal (the crash-loop in the incident),
		// drain those rows out of default and attach the partition so the rows
		// land where they belong. Any other error is genuine and propagates.
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != pgCheckViolation {
			return fmt.Errorf("create partition %s: %w", name, err)
		}
		if healErr := m.healDefaultConflict(ctx, name, lower, upper); healErr != nil {
			return fmt.Errorf("create partition %s: heal default conflict: %w", name, healErr)
		}
	}

	return nil
}

// healDefaultConflict recovers from the case where CREATE … PARTITION OF failed
// because the default partition holds rows belonging in [lower, upper). It moves
// exactly those rows out of the default partition and attaches a fresh partition
// for the range, preserving every row (including its original id).
//
// The entire operation runs in a single transaction: a standalone table is
// created (without copying the parent's identity, so original ids re-insert
// cleanly), the conflicting rows are moved into it with a DELETE … RETURNING,
// and it is then ATTACHed — Postgres re-scans the now-drained default partition
// and the attach succeeds. If any step fails (including the re-insert), the
// transaction rolls back atomically: the rows return to the default partition
// untouched and no partition is created, so the caller can safely retry later.
// No row is ever destroyed.
func (m *Manager) healDefaultConflict(ctx context.Context, name string, lower, upper time.Time) error {
	defaultTable := m.defaultPartitionName()

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Block concurrent writes to the default partition for the duration of the
	// heal. The missing partition is the current period, so live writers are
	// actively routing rows into default; without this lock a row committed
	// between the drain and the attach re-triggers the same check violation and
	// rolls the whole heal back. Locking only the default leaf (not the parent)
	// keeps the rest of the table readable and writable; tuple routing must
	// lock the leaf to insert, so this is sufficient.
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`LOCK TABLE public.%s IN ACCESS EXCLUSIVE MODE`,
		quoteIdent(defaultTable),
	)); err != nil {
		return fmt.Errorf("lock default partition: %w", err)
	}

	// Standalone table matching the parent's columns. INCLUDING DEFAULTS is
	// deliberately the only option: it must NOT copy the parent's identity, so
	// the preserved rows insert with their original ids instead of regenerating.
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE public.%s (LIKE public.%s INCLUDING DEFAULTS)`,
		quoteIdent(name),
		quoteIdent(m.table),
	)); err != nil {
		return fmt.Errorf("create standalone table: %w", err)
	}

	// Move the conflicting rows out of default into the standalone table in one
	// statement. RETURNING * preserves parent column order, matching the LIKE.
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
		WITH moved AS (
			DELETE FROM public.%s
			WHERE "timestamp" >= $1 AND "timestamp" < $2
			RETURNING *
		)
		INSERT INTO public.%s SELECT * FROM moved
	`,
		quoteIdent(defaultTable),
		quoteIdent(name),
	), lower.UTC(), upper.UTC()); err != nil {
		return fmt.Errorf("drain default rows: %w", err)
	}

	// Attach. Postgres re-scans the default partition to confirm no row still
	// falls in [lower, upper); the drain above makes that pass.
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE public.%s ATTACH PARTITION public.%s FOR VALUES FROM (%s) TO (%s)`,
		quoteIdent(m.table),
		quoteIdent(name),
		quoteLiteralTimestamp(lower),
		quoteLiteralTimestamp(upper),
	)); err != nil {
		return fmt.Errorf("attach partition: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

func (m *Manager) DropExpiredPartitions(ctx context.Context, cutoff time.Time) ([]string, error) {
	if m == nil {
		return nil, nil
	}

	type partitionInfo struct {
		name  string
		upper time.Time
	}

	rows, err := m.pool.Query(ctx, `
		SELECT child.relname, pg_get_expr(child.relpartbound, child.oid)
		FROM pg_inherits
		JOIN pg_class parent ON parent.oid = pg_inherits.inhparent
		JOIN pg_class child ON child.oid = pg_inherits.inhrelid
		JOIN pg_namespace ns ON ns.oid = child.relnamespace
		WHERE parent.relname = $1
		  AND ns.nspname = 'public'
	`, m.table)
	if err != nil {
		return nil, fmt.Errorf("query partitions for %s: %w", m.table, err)
	}
	defer rows.Close()

	var partitions []partitionInfo
	for rows.Next() {
		var name string
		var bound string
		if err := rows.Scan(&name, &bound); err != nil {
			return nil, fmt.Errorf("scan partition metadata for %s: %w", m.table, err)
		}
		if strings.Contains(bound, "DEFAULT") {
			continue
		}
		upper, err := parsePartitionUpperBound(bound)
		if err != nil {
			return nil, fmt.Errorf("parse bound for %s: %w", name, err)
		}
		if !upper.After(cutoff.UTC()) {
			partitions = append(partitions, partitionInfo{name: name, upper: upper})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate partitions for %s: %w", m.table, err)
	}

	sort.Slice(partitions, func(i, j int) bool {
		return partitions[i].upper.Before(partitions[j].upper)
	})

	dropped := make([]string, 0, len(partitions))
	for _, partition := range partitions {
		if _, err := m.pool.Exec(ctx, fmt.Sprintf(`DROP TABLE public.%s`, quoteIdent(partition.name))); err != nil {
			return dropped, fmt.Errorf("drop partition %s: %w", partition.name, err)
		}
		dropped = append(dropped, partition.name)
	}

	return dropped, nil
}

func (m *Manager) DeleteExpiredRowsFromDefault(ctx context.Context, cutoff time.Time) (int64, error) {
	if m == nil {
		return 0, nil
	}

	defaultTable := m.defaultPartitionName()
	var exists bool
	if err := m.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+defaultTable).Scan(&exists); err != nil {
		return 0, fmt.Errorf("check default partition for %s: %w", m.table, err)
	}
	if !exists {
		return 0, nil
	}

	totalDeleted := int64(0)
	for {
		tag, err := m.pool.Exec(ctx, fmt.Sprintf(`
			WITH doomed AS (
				SELECT ctid
				FROM public.%s
				WHERE "timestamp" < $1
				LIMIT $2
			)
			DELETE FROM public.%s
			WHERE ctid IN (SELECT ctid FROM doomed)
		`, quoteIdent(defaultTable), quoteIdent(defaultTable)), cutoff.UTC(), deleteBatchSize)
		if err != nil {
			return totalDeleted, fmt.Errorf("delete expired rows from %s: %w", defaultTable, err)
		}
		deleted := tag.RowsAffected()
		totalDeleted += deleted
		if deleted < deleteBatchSize {
			return totalDeleted, nil
		}
	}
}

func (m *Manager) partitionName(lower time.Time) string {
	return fmt.Sprintf("%s_p_%s", m.table, lower.UTC().Format("20060102"))
}

func (m *Manager) defaultPartitionName() string {
	return m.table + "_default"
}

func (g Granularity) truncate(t time.Time) time.Time {
	u := t.UTC()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)

	switch g {
	case Daily:
		return day
	case Weekly:
		offset := int(day.Weekday())
		if offset == 0 {
			offset = 7
		}
		return day.AddDate(0, 0, -(offset - 1))
	default:
		return day
	}
}

func (g Granularity) next(t time.Time) time.Time {
	switch g {
	case Weekly:
		return t.UTC().AddDate(0, 0, 7)
	default:
		return t.UTC().AddDate(0, 0, 1)
	}
}

func (g Granularity) addPeriods(t time.Time, periods int) time.Time {
	switch g {
	case Weekly:
		return t.UTC().AddDate(0, 0, 7*periods)
	default:
		return t.UTC().AddDate(0, 0, periods)
	}
}

func parsePartitionUpperBound(bound string) (time.Time, error) {
	matches := partitionBoundsRE.FindStringSubmatch(bound)
	if len(matches) != 3 {
		return time.Time{}, fmt.Errorf("unrecognized partition bound: %q", bound)
	}
	return parseBoundTimestamp(matches[2])
}

func parseBoundTimestamp(value string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05-07:00",
	}

	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("parse timestamp %q", value)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func quoteLiteralTimestamp(t time.Time) string {
	return "'" + t.UTC().Format("2006-01-02 15:04:05-07") + "'"
}
