package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrRefreshDebtNotFound = errors.New("metadata refresh debt not found")

const metadataRefreshDebtColumns = `target_type, content_id, priority, reason_mask, next_refresh_at,
	claimed_at, lease_expires_at, last_attempt_at, last_success_at, attempt_count, last_error, updated_at`

const metadataRefreshDebtReturningColumns = `d.target_type, d.content_id, d.priority, d.reason_mask, d.next_refresh_at,
	d.claimed_at, d.lease_expires_at, d.last_attempt_at, d.last_success_at, d.attempt_count, d.last_error, d.updated_at`

const metadataRefreshDebtLeaseDuration = 15 * time.Minute

const metadataRefreshDebtEnabledAccessPredicate = `(
		(d.target_type = 'item' AND EXISTS (
			SELECT 1
			FROM media_item_libraries mil
			JOIN media_folders folders ON folders.id = mil.media_folder_id
			WHERE mil.content_id = d.content_id
			  AND folders.enabled = TRUE
		))
		OR (d.target_type = 'season' AND EXISTS (
			SELECT 1
			FROM seasons s
			JOIN media_item_libraries mil ON mil.content_id = s.series_id
			JOIN media_folders folders ON folders.id = mil.media_folder_id
			WHERE s.content_id = d.content_id
			  AND folders.enabled = TRUE
		))
		OR (d.target_type = 'episode' AND EXISTS (
			SELECT 1
			FROM episode_libraries el
			JOIN media_folders folders ON folders.id = el.media_folder_id
			WHERE el.episode_id = d.content_id
			  AND folders.enabled = TRUE
		))
	)`

type RefreshDebtRepository struct {
	pool *pgxpool.Pool
}

func NewRefreshDebtRepository(pool *pgxpool.Pool) *RefreshDebtRepository {
	return &RefreshDebtRepository{pool: pool}
}

func (r *RefreshDebtRepository) requireConfigured() error {
	if r == nil || r.pool == nil {
		return errors.New("metadata refresh debt repository is not configured")
	}
	return nil
}

func scanRefreshDebt(row pgx.Row) (*models.MetadataRefreshDebt, error) {
	var debt models.MetadataRefreshDebt
	if err := row.Scan(
		&debt.TargetType,
		&debt.ContentID,
		&debt.Priority,
		&debt.ReasonMask,
		&debt.NextRefreshAt,
		&debt.ClaimedAt,
		&debt.LeaseExpiresAt,
		&debt.LastAttemptAt,
		&debt.LastSuccessAt,
		&debt.AttemptCount,
		&debt.LastError,
		&debt.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRefreshDebtNotFound
		}
		return nil, fmt.Errorf("scanning metadata refresh debt: %w", err)
	}
	return &debt, nil
}

func scanRefreshDebts(rows pgx.Rows) ([]*models.MetadataRefreshDebt, error) {
	var debts []*models.MetadataRefreshDebt
	for rows.Next() {
		debt, err := scanRefreshDebt(rows)
		if err != nil {
			return nil, err
		}
		debts = append(debts, debt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating metadata refresh debt rows: %w", err)
	}
	if debts == nil {
		debts = []*models.MetadataRefreshDebt{}
	}
	return debts, nil
}

func (r *RefreshDebtRepository) Get(ctx context.Context, contentID string) (*models.MetadataRefreshDebt, error) {
	return r.GetTarget(ctx, RefreshTargetItem, contentID)
}

func (r *RefreshDebtRepository) GetTarget(ctx context.Context, targetType, contentID string) (*models.MetadataRefreshDebt, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, err
	}
	targetType = NormalizeRefreshTargetType(targetType)
	contentID = strings.TrimSpace(contentID)
	if targetType == "" || contentID == "" {
		return nil, ErrRefreshDebtNotFound
	}

	return scanRefreshDebt(r.pool.QueryRow(ctx, `
		SELECT `+metadataRefreshDebtColumns+`
		FROM metadata_refresh_debt
		WHERE target_type = $1 AND content_id = $2
	`, targetType, contentID))
}

func (r *RefreshDebtRepository) UpsertDebt(ctx context.Context, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error {
	return r.UpsertTargetDebt(ctx, RefreshTargetItem, contentID, priority, reasonMask, nextRefreshAt)
}

func (r *RefreshDebtRepository) UpsertTargetDebt(ctx context.Context, targetType, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	targetType = NormalizeRefreshTargetType(targetType)
	contentID = strings.TrimSpace(contentID)
	if targetType == "" || contentID == "" || reasonMask == 0 {
		return nil
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO metadata_refresh_debt (
			target_type,
			content_id,
			priority,
			reason_mask,
			next_refresh_at,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (target_type, content_id) DO UPDATE SET
			priority = GREATEST(metadata_refresh_debt.priority, EXCLUDED.priority),
			reason_mask = metadata_refresh_debt.reason_mask | EXCLUDED.reason_mask,
			next_refresh_at = LEAST(metadata_refresh_debt.next_refresh_at, EXCLUDED.next_refresh_at),
			updated_at = NOW()
	`, targetType, contentID, priority, reasonMask, nextRefreshAt.UTC())
	if err != nil {
		return fmt.Errorf("upserting metadata refresh debt: %w", err)
	}
	return nil
}

func (r *RefreshDebtRepository) RequestDue(
	ctx context.Context,
	targetType string,
	contentID string,
	priority int,
	reasonMask int64,
	nextRefreshAt time.Time,
	cooldown time.Duration,
) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	targetType = NormalizeRefreshTargetType(targetType)
	contentID = strings.TrimSpace(contentID)
	if targetType == "" || contentID == "" || reasonMask == 0 {
		return nil
	}
	if cooldown < 0 {
		cooldown = 0
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO metadata_refresh_debt (
			target_type,
			content_id,
			priority,
			reason_mask,
			next_refresh_at,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (target_type, content_id) DO UPDATE SET
			priority = GREATEST(metadata_refresh_debt.priority, EXCLUDED.priority),
			reason_mask = metadata_refresh_debt.reason_mask | EXCLUDED.reason_mask,
			next_refresh_at = CASE
				WHEN metadata_refresh_debt.lease_expires_at IS NOT NULL
				 AND metadata_refresh_debt.lease_expires_at >= NOW()
					THEN metadata_refresh_debt.next_refresh_at
				WHEN metadata_refresh_debt.last_attempt_at IS NOT NULL
				 AND metadata_refresh_debt.last_attempt_at > NOW() - $6::interval
					THEN metadata_refresh_debt.next_refresh_at
				WHEN metadata_refresh_debt.last_success_at IS NOT NULL
				 AND metadata_refresh_debt.last_success_at > NOW() - $6::interval
					THEN metadata_refresh_debt.next_refresh_at
				ELSE LEAST(metadata_refresh_debt.next_refresh_at, EXCLUDED.next_refresh_at)
			END,
			updated_at = NOW()
	`, targetType, contentID, priority, reasonMask, nextRefreshAt.UTC(), intervalLiteral(cooldown))
	if err != nil {
		return fmt.Errorf("requesting due metadata refresh debt: %w", err)
	}
	return nil
}

func (r *RefreshDebtRepository) ClaimDue(ctx context.Context, limit int) ([]*models.MetadataRefreshDebt, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return []*models.MetadataRefreshDebt{}, nil
	}

	rows, err := r.pool.Query(ctx, `
		WITH due AS (
			SELECT d.target_type, d.content_id
			FROM metadata_refresh_debt d
			WHERE d.next_refresh_at <= NOW()
			  AND (d.lease_expires_at IS NULL OR d.lease_expires_at < NOW())
			  AND `+metadataRefreshDebtEnabledAccessPredicate+`
			ORDER BY d.priority DESC, d.next_refresh_at ASC, d.updated_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE metadata_refresh_debt d
		SET claimed_at = NOW(),
			lease_expires_at = NOW() + $2::interval,
			last_attempt_at = NOW(),
			attempt_count = d.attempt_count + 1,
			updated_at = NOW()
		FROM due
		WHERE d.target_type = due.target_type
		  AND d.content_id = due.content_id
		RETURNING `+metadataRefreshDebtReturningColumns, limit, intervalLiteral(metadataRefreshDebtLeaseDuration))
	if err != nil {
		return nil, fmt.Errorf("claiming due metadata refresh debt: %w", err)
	}
	defer rows.Close()

	return scanRefreshDebts(rows)
}

func (r *RefreshDebtRepository) PruneDisabledLibraryDebt(ctx context.Context) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}

	if _, err := r.pool.Exec(ctx, `
		DELETE FROM metadata_refresh_debt d
		WHERE NOT `+metadataRefreshDebtEnabledAccessPredicate+`
	`); err != nil {
		return fmt.Errorf("pruning disabled-library metadata refresh debt: %w", err)
	}
	return nil
}

func (r *RefreshDebtRepository) DeleteDebt(ctx context.Context, contentID string) error {
	return r.DeleteTargetDebt(ctx, RefreshTargetItem, contentID)
}

func (r *RefreshDebtRepository) DeleteTargetDebt(ctx context.Context, targetType, contentID string) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	targetType = NormalizeRefreshTargetType(targetType)
	contentID = strings.TrimSpace(contentID)
	if targetType == "" || contentID == "" {
		return nil
	}

	_, err := r.pool.Exec(ctx, `
		DELETE FROM metadata_refresh_debt
		WHERE target_type = $1 AND content_id = $2
	`, targetType, contentID)
	if err != nil {
		return fmt.Errorf("deleting metadata refresh debt: %w", err)
	}
	return nil
}

func (r *RefreshDebtRepository) MarkFailure(
	ctx context.Context,
	contentID string,
	priority int,
	reasonMask int64,
	nextRefreshAt time.Time,
	attemptCount int,
	lastError string,
) error {
	return r.MarkTargetFailure(ctx, RefreshTargetItem, contentID, priority, reasonMask, nextRefreshAt, attemptCount, lastError)
}

func (r *RefreshDebtRepository) MarkTargetFailure(
	ctx context.Context,
	targetType string,
	contentID string,
	priority int,
	reasonMask int64,
	nextRefreshAt time.Time,
	attemptCount int,
	lastError string,
) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	targetType = NormalizeRefreshTargetType(targetType)
	contentID = strings.TrimSpace(contentID)
	if targetType == "" || contentID == "" || reasonMask == 0 {
		return nil
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO metadata_refresh_debt (
			target_type,
			content_id,
			priority,
			reason_mask,
			next_refresh_at,
			claimed_at,
			lease_expires_at,
			last_attempt_at,
			attempt_count,
			last_error,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, NULL, NULL, NOW(), $6, $7, NOW())
		ON CONFLICT (target_type, content_id) DO UPDATE SET
			priority = EXCLUDED.priority,
			reason_mask = EXCLUDED.reason_mask,
			next_refresh_at = EXCLUDED.next_refresh_at,
			claimed_at = NULL,
			lease_expires_at = NULL,
			last_attempt_at = NOW(),
			attempt_count = GREATEST(metadata_refresh_debt.attempt_count, EXCLUDED.attempt_count),
			last_error = EXCLUDED.last_error,
			updated_at = NOW()
	`, targetType, contentID, priority, reasonMask, nextRefreshAt.UTC(), attemptCount, strings.TrimSpace(lastError))
	if err != nil {
		return fmt.Errorf("marking metadata refresh debt failure: %w", err)
	}
	return nil
}

func (r *RefreshDebtRepository) MarkSuccess(
	ctx context.Context,
	contentID string,
	priority int,
	reasonMask int64,
	nextRefreshAt time.Time,
) error {
	return r.MarkTargetSuccess(ctx, RefreshTargetItem, contentID, priority, reasonMask, nextRefreshAt)
}

func (r *RefreshDebtRepository) MarkTargetSuccess(
	ctx context.Context,
	targetType string,
	contentID string,
	priority int,
	reasonMask int64,
	nextRefreshAt time.Time,
) error {
	if err := r.requireConfigured(); err != nil {
		return err
	}
	targetType = NormalizeRefreshTargetType(targetType)
	contentID = strings.TrimSpace(contentID)
	if targetType == "" || contentID == "" {
		return nil
	}
	if reasonMask == 0 {
		return r.DeleteTargetDebt(ctx, targetType, contentID)
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO metadata_refresh_debt (
			target_type,
			content_id,
			priority,
			reason_mask,
			next_refresh_at,
			claimed_at,
			lease_expires_at,
			last_success_at,
			last_error,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, NULL, NULL, NOW(), '', NOW())
		ON CONFLICT (target_type, content_id) DO UPDATE SET
			priority = EXCLUDED.priority,
			reason_mask = EXCLUDED.reason_mask,
			next_refresh_at = EXCLUDED.next_refresh_at,
			claimed_at = NULL,
			lease_expires_at = NULL,
			last_success_at = NOW(),
			last_error = '',
			updated_at = NOW()
	`, targetType, contentID, priority, reasonMask, nextRefreshAt.UTC())
	if err != nil {
		return fmt.Errorf("marking metadata refresh debt success: %w", err)
	}
	return nil
}

func (r *RefreshDebtRepository) GetMetrics(ctx context.Context, sampleLimit int) (*models.MetadataRefreshMetrics, error) {
	if err := r.requireConfigured(); err != nil {
		return nil, err
	}
	if sampleLimit <= 0 {
		sampleLimit = 10
	}

	metrics := &models.MetadataRefreshMetrics{
		ReasonCounts:   []models.MetadataRefreshReasonCount{},
		AttemptBuckets: []models.MetadataRefreshAttemptBucket{},
		DueSamples:     []models.MetadataRefreshDebtSample{},
		RecentErrors:   []models.MetadataRefreshDebtSample{},
	}

	if err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE next_refresh_at <= NOW()) AS due,
			COUNT(*) FILTER (WHERE lease_expires_at IS NOT NULL AND lease_expires_at >= NOW()) AS leased,
			MIN(next_refresh_at) FILTER (WHERE next_refresh_at <= NOW()) AS oldest_due_at,
			MIN(lease_expires_at) FILTER (WHERE lease_expires_at IS NOT NULL AND lease_expires_at >= NOW()) AS oldest_lease_expires_at
		FROM metadata_refresh_debt
	`).Scan(
		&metrics.Total,
		&metrics.Due,
		&metrics.Leased,
		&metrics.OldestDueAt,
		&metrics.OldestLeaseExpiresAt,
	); err != nil {
		return nil, fmt.Errorf("querying metadata refresh metrics summary: %w", err)
	}

	reasonDefs := []struct {
		reason string
		mask   int64
	}{
		{reason: "episode_incomplete", mask: RefreshDebtReasonEpisodeIncomplete},
		{reason: "stale_provider_id", mask: RefreshDebtReasonStaleProviderID},
		{reason: "provider_id_incomplete", mask: RefreshDebtReasonProviderIDIncomplete},
		{reason: "refresh_failure", mask: RefreshDebtReasonRefreshFailure},
		{reason: "core_metadata_incomplete", mask: RefreshDebtReasonCoreMetadataIncomplete},
	}
	for _, def := range reasonDefs {
		var count int
		if err := r.pool.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM metadata_refresh_debt
			WHERE (reason_mask & $1) <> 0
		`, def.mask).Scan(&count); err != nil {
			return nil, fmt.Errorf("querying metadata refresh reason count for %s: %w", def.reason, err)
		}
		metrics.ReasonCounts = append(metrics.ReasonCounts, models.MetadataRefreshReasonCount{
			Reason: def.reason,
			Count:  count,
		})
	}

	rows, err := r.pool.Query(ctx, `
		SELECT label, count
		FROM (
			SELECT '0' AS label, COUNT(*) FILTER (WHERE attempt_count = 0) AS count FROM metadata_refresh_debt
			UNION ALL
			SELECT '1' AS label, COUNT(*) FILTER (WHERE attempt_count = 1) AS count FROM metadata_refresh_debt
			UNION ALL
			SELECT '2-3' AS label, COUNT(*) FILTER (WHERE attempt_count BETWEEN 2 AND 3) AS count FROM metadata_refresh_debt
			UNION ALL
			SELECT '4-7' AS label, COUNT(*) FILTER (WHERE attempt_count BETWEEN 4 AND 7) AS count FROM metadata_refresh_debt
			UNION ALL
			SELECT '8+' AS label, COUNT(*) FILTER (WHERE attempt_count >= 8) AS count FROM metadata_refresh_debt
		) buckets
		ORDER BY label
	`)
	if err != nil {
		return nil, fmt.Errorf("querying metadata refresh attempt buckets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bucket models.MetadataRefreshAttemptBucket
		if err := rows.Scan(&bucket.Label, &bucket.Count); err != nil {
			return nil, fmt.Errorf("scanning metadata refresh attempt bucket: %w", err)
		}
		metrics.AttemptBuckets = append(metrics.AttemptBuckets, bucket)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating metadata refresh attempt buckets: %w", err)
	}

	dueSamples, err := r.querySamples(ctx, `
		SELECT
			d.target_type,
			d.content_id,
			COALESCE(CASE d.target_type
				WHEN 'item' THEN COALESCE(mi.title, '')
				WHEN 'season' THEN TRIM(BOTH ' ' FROM COALESCE(smi.title || ' - ', '') || COALESCE(NULLIF(s.title, ''), 'Season ' || s.season_number::text))
				WHEN 'episode' THEN TRIM(BOTH ' ' FROM COALESCE(emi.title || ' - ', '') || 'S' || e.season_number::text || 'E' || e.episode_number::text || ' ' || COALESCE(NULLIF(e.title, ''), ''))
				ELSE ''
			END, '') AS title,
			CASE d.target_type
				WHEN 'item' THEN COALESCE(mi.type, '')
				ELSE d.target_type
			END AS type,
			d.reason_mask,
			d.next_refresh_at,
			d.last_attempt_at,
			d.attempt_count,
			d.last_error
		FROM metadata_refresh_debt d
		LEFT JOIN media_items mi ON d.target_type = 'item' AND mi.content_id = d.content_id
		LEFT JOIN seasons s ON d.target_type = 'season' AND s.content_id = d.content_id
		LEFT JOIN media_items smi ON smi.content_id = s.series_id
		LEFT JOIN episodes e ON d.target_type = 'episode' AND e.content_id = d.content_id
		LEFT JOIN media_items emi ON emi.content_id = e.series_id
		WHERE d.next_refresh_at <= NOW()
		ORDER BY d.priority DESC, d.next_refresh_at ASC, d.updated_at ASC
		LIMIT $1
	`, sampleLimit)
	if err != nil {
		return nil, err
	}
	metrics.DueSamples = dueSamples

	recentErrors, err := r.querySamples(ctx, `
		SELECT
			d.target_type,
			d.content_id,
			COALESCE(CASE d.target_type
				WHEN 'item' THEN COALESCE(mi.title, '')
				WHEN 'season' THEN TRIM(BOTH ' ' FROM COALESCE(smi.title || ' - ', '') || COALESCE(NULLIF(s.title, ''), 'Season ' || s.season_number::text))
				WHEN 'episode' THEN TRIM(BOTH ' ' FROM COALESCE(emi.title || ' - ', '') || 'S' || e.season_number::text || 'E' || e.episode_number::text || ' ' || COALESCE(NULLIF(e.title, ''), ''))
				ELSE ''
			END, '') AS title,
			CASE d.target_type
				WHEN 'item' THEN COALESCE(mi.type, '')
				ELSE d.target_type
			END AS type,
			d.reason_mask,
			d.next_refresh_at,
			d.last_attempt_at,
			d.attempt_count,
			d.last_error
		FROM metadata_refresh_debt d
		LEFT JOIN media_items mi ON d.target_type = 'item' AND mi.content_id = d.content_id
		LEFT JOIN seasons s ON d.target_type = 'season' AND s.content_id = d.content_id
		LEFT JOIN media_items smi ON smi.content_id = s.series_id
		LEFT JOIN episodes e ON d.target_type = 'episode' AND e.content_id = d.content_id
		LEFT JOIN media_items emi ON emi.content_id = e.series_id
		WHERE COALESCE(d.last_error, '') <> ''
		ORDER BY d.updated_at DESC
		LIMIT $1
	`, sampleLimit)
	if err != nil {
		return nil, err
	}
	metrics.RecentErrors = recentErrors

	return metrics, nil
}

func (r *RefreshDebtRepository) querySamples(ctx context.Context, query string, limit int) ([]models.MetadataRefreshDebtSample, error) {
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("querying metadata refresh debt samples: %w", err)
	}
	defer rows.Close()

	samples := make([]models.MetadataRefreshDebtSample, 0, limit)
	for rows.Next() {
		var sample models.MetadataRefreshDebtSample
		if err := rows.Scan(
			&sample.TargetType,
			&sample.ContentID,
			&sample.Title,
			&sample.Type,
			&sample.ReasonMask,
			&sample.NextRefreshAt,
			&sample.LastAttemptAt,
			&sample.AttemptCount,
			&sample.LastError,
		); err != nil {
			return nil, fmt.Errorf("scanning metadata refresh debt sample: %w", err)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating metadata refresh debt samples: %w", err)
	}
	return samples, nil
}
