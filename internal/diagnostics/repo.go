package diagnostics

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	diagnosticQuotaLockNamespace = 173
	defaultShortIDRetries        = 8
	defaultAdminListLimit        = 50
	maxAdminListLimit            = 200
)

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) InsertReceiving(ctx context.Context, input InsertReceivingInput) (InsertReceivingResult, error) {
	if err := validateInsertReceivingInput(input); err != nil {
		return InsertReceivingResult{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return InsertReceivingResult{}, fmt.Errorf("begin diagnostic report insert: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, $2::int4)`,
		diagnosticQuotaLockNamespace, input.UserID); err != nil {
		return InsertReceivingResult{}, fmt.Errorf("acquire diagnostic quota lock: %w", err)
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := r.checkQuotas(ctx, tx, input, now); err != nil {
		return InsertReceivingResult{}, err
	}

	shortIDGenerator := input.ShortIDGenerator
	if shortIDGenerator == nil {
		shortIDGenerator = NewShortID
	}
	reportIDGenerator := input.ReportIDGenerator
	if reportIDGenerator == nil {
		reportIDGenerator = uuid.NewString
	}
	retries := input.ShortIDCollisionRetries
	if retries <= 0 {
		retries = defaultShortIDRetries
	}

	crashSummary := truncateCrashSummary(input.CrashSummary)
	sessionIDs := input.PlaybackSessionIDs
	if sessionIDs == nil {
		// pgx binds a nil slice as SQL NULL, which bypasses the column's
		// '{}' default and violates its NOT NULL constraint.
		sessionIDs = []string{}
	}
	for attempt := 0; attempt < retries; attempt++ {
		id := reportIDGenerator()
		if _, err := uuid.Parse(id); err != nil {
			return InsertReceivingResult{}, fmt.Errorf("%w: report id must be a UUID", ErrInvalidReportInput)
		}
		shortID, err := shortIDGenerator()
		if err != nil {
			return InsertReceivingResult{}, fmt.Errorf("generate diagnostic short id: %w", err)
		}
		shortID, err = ParseShortID(shortID)
		if err != nil {
			return InsertReceivingResult{}, fmt.Errorf("%w: generated short id is invalid", err)
		}

		var insertedID, insertedShortID string
		err = tx.QueryRow(ctx, `
			INSERT INTO client_diagnostic_reports (
				id, short_id, user_id, profile_id, state, captured_at, report_type,
				platform, app_version, crash_summary, manifest, playback_session_ids,
				blob_bytes
			)
			VALUES ($1::uuid, $2, $3, $4, 'receiving', $5, $6, $7, $8, $9, $10::jsonb, $11, $12)
			ON CONFLICT DO NOTHING
			RETURNING id::text, short_id
		`, id, shortID, input.UserID, nullableString(input.ProfileID), input.CapturedAt,
			strings.TrimSpace(input.ReportType), strings.TrimSpace(input.Platform),
			strings.TrimSpace(input.AppVersion), nullableString(crashSummary),
			string(input.Manifest), sessionIDs, input.ExpectedBlobBytes).Scan(&insertedID, &insertedShortID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return InsertReceivingResult{}, fmt.Errorf("insert diagnostic report: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return InsertReceivingResult{}, fmt.Errorf("commit diagnostic report insert: %w", err)
		}
		return InsertReceivingResult{ID: insertedID, ShortID: insertedShortID}, nil
	}

	return InsertReceivingResult{}, ErrShortIDExhausted
}

func (r *PostgresRepository) checkQuotas(ctx context.Context, tx pgx.Tx, input InsertReceivingInput, now time.Time) error {
	if input.MaxReportsPerUserDay > 0 {
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM client_diagnostic_reports
			WHERE user_id = $1
			  AND state IN ('receiving', 'ready')
			  AND received_at >= date_trunc('day', $2::timestamptz)
		`, input.UserID, now).Scan(&count); err != nil {
			return fmt.Errorf("count diagnostic reports for quota: %w", err)
		}
		if count >= input.MaxReportsPerUserDay {
			return &QuotaError{Kind: QuotaKindReportsPerDay, Limit: int64(input.MaxReportsPerUserDay)}
		}
	}

	if input.MaxBytesPerUser > 0 {
		// Count 'receiving' rows too: a new upload reserves its expected size in
		// blob_bytes before the bundle streams in, so concurrent or multi-node
		// uploads can't each pass a near-limit check and both mark ready. Ready
		// rows overwrite the reservation with their actual size on completion.
		var used int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(blob_bytes), 0)
			FROM client_diagnostic_reports
			WHERE user_id = $1
			  AND state IN ('receiving', 'ready')
		`, input.UserID).Scan(&used); err != nil {
			return fmt.Errorf("sum diagnostic bytes for quota: %w", err)
		}
		if input.ExpectedBlobBytes > input.MaxBytesPerUser || used+input.ExpectedBlobBytes > input.MaxBytesPerUser {
			return &QuotaError{Kind: QuotaKindBytesPerUser, Limit: input.MaxBytesPerUser}
		}
	}
	return nil
}

func (r *PostgresRepository) MarkReady(ctx context.Context, id string, blob BlobInfo) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE client_diagnostic_reports
		SET state = 'ready',
		    blob_bucket = $2,
		    blob_key = $3,
		    blob_bytes = $4,
		    uncompressed_bytes = $5,
		    blob_sha256 = $6
		WHERE id = $1::uuid
	`, id, strings.TrimSpace(blob.Bucket), strings.TrimSpace(blob.Key), blob.Bytes, blob.UncompressedBytes, strings.TrimSpace(blob.SHA256))
	if err != nil {
		return fmt.Errorf("mark diagnostic report ready: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) MarkFailed(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE client_diagnostic_reports
		SET state = 'failed'
		WHERE id = $1::uuid
	`, id)
	if err != nil {
		return fmt.Errorf("mark diagnostic report failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) GetByID(ctx context.Context, id string) (*Report, error) {
	row := r.pool.QueryRow(ctx, reportSelectSQL()+`
		WHERE id = $1::uuid
	`, id)
	report, err := scanReport(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return report, nil
}

func (r *PostgresRepository) ListForAdmin(ctx context.Context, filters ListFilters) (ListResult, error) {
	limit := filters.Limit
	if limit <= 0 || limit > maxAdminListLimit {
		limit = defaultAdminListLimit
	}

	conditions := []string{"1=1"}
	args := make([]any, 0, 10)
	argIdx := 1

	if filters.UserID != nil {
		conditions = append(conditions, fmt.Sprintf("user_id = $%d", argIdx))
		args = append(args, *filters.UserID)
		argIdx++
	}
	if strings.TrimSpace(filters.Platform) != "" {
		conditions = append(conditions, fmt.Sprintf("platform = $%d", argIdx))
		args = append(args, strings.TrimSpace(filters.Platform))
		argIdx++
	}
	if strings.TrimSpace(filters.ReportType) != "" {
		conditions = append(conditions, fmt.Sprintf("report_type = $%d", argIdx))
		args = append(args, strings.TrimSpace(filters.ReportType))
		argIdx++
	}
	if filters.From != nil {
		conditions = append(conditions, fmt.Sprintf("received_at >= $%d", argIdx))
		args = append(args, *filters.From)
		argIdx++
	}
	if filters.To != nil {
		conditions = append(conditions, fmt.Sprintf("received_at <= $%d", argIdx))
		args = append(args, *filters.To)
		argIdx++
	}
	if strings.TrimSpace(filters.ShortID) != "" {
		shortID, err := ParseShortID(filters.ShortID)
		if err != nil {
			return ListResult{}, err
		}
		conditions = append(conditions, fmt.Sprintf("lower(short_id) = lower($%d)", argIdx))
		args = append(args, shortID)
		argIdx++
	}
	if strings.TrimSpace(filters.Cursor) != "" {
		cursorReceivedAt, cursorID, err := decodeReportCursor(filters.Cursor)
		if err != nil {
			return ListResult{}, err
		}
		conditions = append(conditions, fmt.Sprintf("(received_at, id) < ($%d, $%d::uuid)", argIdx, argIdx+1))
		args = append(args, cursorReceivedAt, cursorID)
		argIdx += 2
	}

	query := fmt.Sprintf("%s\n\t\tWHERE %s\n\t\tORDER BY received_at DESC, id DESC\n\t\tLIMIT $%d",
		reportListSelectSQL(), strings.Join(conditions, " AND "), argIdx)
	args = append(args, limit+1)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list diagnostic reports: %w", err)
	}
	defer rows.Close()

	reports := make([]Report, 0, limit+1)
	for rows.Next() {
		report, err := scanReportSummary(rows)
		if err != nil {
			return ListResult{}, err
		}
		reports = append(reports, *report)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("iterate diagnostic reports: %w", err)
	}

	result := ListResult{}
	if len(reports) > limit {
		last := reports[limit-1]
		result.NextCursor = encodeReportCursor(last.ReceivedAt, last.ID)
		reports = reports[:limit]
	}
	result.Reports = reports
	return result, nil
}

func (r *PostgresRepository) DeleteByID(ctx context.Context, id string) (*Report, error) {
	row := r.pool.QueryRow(ctx, `
		DELETE FROM client_diagnostic_reports
		WHERE id = $1::uuid
		RETURNING id::text, short_id, user_id, profile_id, state, captured_at, received_at,
		          report_type, platform, app_version, crash_summary, manifest, playback_session_ids,
		          blob_bucket, blob_key, blob_bytes, uncompressed_bytes, blob_sha256,
		          COALESCE(manifest->'report'->>'app_build', '')
	`, id)
	report, err := scanReport(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return report, nil
}

func (r *PostgresRepository) RetentionCandidates(ctx context.Context, olderThan time.Time, perUserByteCap int64) ([]Report, error) {
	if olderThan.IsZero() && perUserByteCap <= 0 {
		return nil, nil
	}

	query := reportCleanupSelectSQL() + `
		WHERE received_at < $1
		ORDER BY received_at ASC, id ASC
	`
	args := []any{olderThan}
	if olderThan.IsZero() {
		query = reportCleanupSelectSQL() + `
			WHERE false
		`
		args = nil
	}

	if perUserByteCap > 0 {
		byteCapQuery := `
			WITH quota_candidates AS (
				SELECT id
				FROM (
					SELECT id,
					       SUM(COALESCE(blob_bytes, 0)) OVER (
					           PARTITION BY user_id
					           ORDER BY received_at DESC, id DESC
					       ) AS retained_bytes
					FROM client_diagnostic_reports
					WHERE state = 'ready'
				) ranked
				WHERE retained_bytes > $1
			)
		` + reportCleanupSelectSQL() + `
			WHERE id IN (SELECT id FROM quota_candidates)
			ORDER BY received_at ASC, id ASC
		`
		if olderThan.IsZero() {
			return r.queryReportCleanup(ctx, byteCapQuery, perUserByteCap)
		}

		query = `
			WITH quota_candidates AS (
				SELECT id
				FROM (
					SELECT id,
					       SUM(COALESCE(blob_bytes, 0)) OVER (
					           PARTITION BY user_id
					           ORDER BY received_at DESC, id DESC
					       ) AS retained_bytes
					FROM client_diagnostic_reports
					WHERE state = 'ready'
				) ranked
				WHERE retained_bytes > $2
			)
		` + reportCleanupSelectSQL() + `
			WHERE received_at < $1 OR id IN (SELECT id FROM quota_candidates)
			ORDER BY received_at ASC, id ASC
		`
		args = []any{olderThan, perUserByteCap}
	}

	return r.queryReportCleanup(ctx, query, args...)
}

func (r *PostgresRepository) StaleReceiving(ctx context.Context, grace time.Duration) ([]Report, error) {
	if grace <= 0 {
		grace = time.Hour
	}
	cutoff := time.Now().UTC().Add(-grace)
	return r.queryReportCleanup(ctx, reportCleanupSelectSQL()+`
		WHERE state IN ('receiving', 'failed')
		  AND received_at < $1
		ORDER BY received_at ASC, id ASC
	`, cutoff)
}

func (r *PostgresRepository) LiveBlobKeys(ctx context.Context, keys []string) (map[string]ReportState, error) {
	live := make(map[string]ReportState, len(keys))
	if len(keys) == 0 {
		return live, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT candidate.key, report.state
		FROM unnest($1::text[]) AS candidate(key)
		JOIN client_diagnostic_reports report
		  ON report.blob_key = candidate.key
		  OR ($2 || report.user_id::text || '/' || report.id::text || '.tar.gz') = candidate.key
		WHERE report.state IN ('ready', 'receiving')
	`, keys, ObjectPrefix)
	if err != nil {
		return nil, fmt.Errorf("load live diagnostic blob keys: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var state ReportState
		if err := rows.Scan(&key, &state); err != nil {
			return nil, fmt.Errorf("scan live diagnostic blob key: %w", err)
		}
		live[key] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate live diagnostic blob keys: %w", err)
	}
	return live, nil
}

// queryReportCleanup runs a reportCleanupSelectSQL-shaped query; the returned
// reports carry no manifest and no app_build. Only the retention and
// stale-cleanup batches use it.
func (r *PostgresRepository) queryReportCleanup(ctx context.Context, query string, args ...any) ([]Report, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query diagnostic reports: %w", err)
	}
	defer rows.Close()

	reports := []Report{}
	for rows.Next() {
		report, err := scanReportCleanup(rows)
		if err != nil {
			return nil, err
		}
		reports = append(reports, *report)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate diagnostic reports: %w", err)
	}
	return reports, nil
}

func validateInsertReceivingInput(input InsertReceivingInput) error {
	if input.UserID <= 0 {
		return fmt.Errorf("%w: user id is required", ErrInvalidReportInput)
	}
	if input.CapturedAt.IsZero() {
		return fmt.Errorf("%w: captured_at is required", ErrInvalidReportInput)
	}
	if strings.TrimSpace(input.ReportType) == "" {
		return fmt.Errorf("%w: report type is required", ErrInvalidReportInput)
	}
	if strings.TrimSpace(input.Platform) == "" {
		return fmt.Errorf("%w: platform is required", ErrInvalidReportInput)
	}
	if strings.TrimSpace(input.AppVersion) == "" {
		return fmt.Errorf("%w: app version is required", ErrInvalidReportInput)
	}
	if len(input.Manifest) == 0 || !json.Valid(input.Manifest) {
		return fmt.Errorf("%w: manifest must be valid JSON", ErrInvalidReportInput)
	}
	if input.ExpectedBlobBytes < 0 {
		return fmt.Errorf("%w: expected blob bytes cannot be negative", ErrInvalidReportInput)
	}
	return nil
}

// reportSelectSQL is the full projection including the manifest JSONB (up to
// MaxManifestBytes per row). Reserve it for single-report detail/download paths;
// the admin list uses reportListSelectSQL and retention/stale batches use
// reportCleanupSelectSQL so browsing a page or running a cleanup pass doesn't
// drag every report's manifest through the DB.
func reportSelectSQL() string {
	return `SELECT id::text, short_id, user_id, profile_id, state, captured_at, received_at,
		       report_type, platform, app_version, crash_summary, manifest, playback_session_ids,
		       blob_bucket, blob_key, blob_bytes, uncompressed_bytes, blob_sha256,
		       COALESCE(manifest->'report'->>'app_build', '')
		FROM client_diagnostic_reports`
}

// reportListSelectSQL is the admin-list projection: it omits the full manifest
// column but still extracts app_build from the manifest JSONB (a cheap text
// extraction) so list rows can show the build number without shipping the whole
// manifest. Reports scanned with scanReportSummary therefore have a nil
// Manifest. Cleanup batches must not pay for the JSONB extraction, so they use
// reportCleanupSelectSQL instead.
func reportListSelectSQL() string {
	return `SELECT id::text, short_id, user_id, profile_id, state, captured_at, received_at,
		       report_type, platform, app_version, crash_summary, playback_session_ids,
		       blob_bucket, blob_key, blob_bytes, uncompressed_bytes, blob_sha256,
		       COALESCE(manifest->'report'->>'app_build', '')
		FROM client_diagnostic_reports`
}

// reportCleanupSelectSQL is the retention/stale-cleanup projection. It omits
// both the manifest column and the app_build extraction because cleanup only
// needs id/state/blob fields to delete a row and its object; touching the JSONB
// per candidate would turn a scheduled pass over many large manifests into
// needless manifest I/O. Reports scanned with scanReportCleanup therefore have
// a nil Manifest and an empty AppBuild.
func reportCleanupSelectSQL() string {
	return `SELECT id::text, short_id, user_id, profile_id, state, captured_at, received_at,
		       report_type, platform, app_version, crash_summary, playback_session_ids,
		       blob_bucket, blob_key, blob_bytes, uncompressed_bytes, blob_sha256
		FROM client_diagnostic_reports`
}

type reportScanner interface {
	Scan(dest ...any) error
}

// reportNulls holds the nullable columns shared by the full and summary
// projections so both scanners apply them the same way.
type reportNulls struct {
	profileID         sql.NullString
	crashSummary      sql.NullString
	blobBucket        sql.NullString
	blobKey           sql.NullString
	blobSHA256        sql.NullString
	blobBytes         sql.NullInt64
	uncompressedBytes sql.NullInt64
}

func (n *reportNulls) apply(report *Report) {
	if n.profileID.Valid {
		report.ProfileID = &n.profileID.String
	}
	if n.crashSummary.Valid {
		report.CrashSummary = &n.crashSummary.String
	}
	if report.PlaybackSessionIDs == nil {
		report.PlaybackSessionIDs = []string{}
	}
	if n.blobBucket.Valid {
		report.BlobBucket = &n.blobBucket.String
	}
	if n.blobKey.Valid {
		report.BlobKey = &n.blobKey.String
	}
	if n.blobBytes.Valid {
		report.BlobBytes = &n.blobBytes.Int64
	}
	if n.uncompressedBytes.Valid {
		report.UncompressedBytes = &n.uncompressedBytes.Int64
	}
	if n.blobSHA256.Valid {
		report.BlobSHA256 = &n.blobSHA256.String
	}
}

// scanReport scans the full reportSelectSQL projection, including the manifest.
func scanReport(row reportScanner) (*Report, error) {
	var report Report
	var nulls reportNulls
	var manifest []byte
	if err := row.Scan(
		&report.ID,
		&report.ShortID,
		&report.UserID,
		&nulls.profileID,
		&report.State,
		&report.CapturedAt,
		&report.ReceivedAt,
		&report.ReportType,
		&report.Platform,
		&report.AppVersion,
		&nulls.crashSummary,
		&manifest,
		&report.PlaybackSessionIDs,
		&nulls.blobBucket,
		&nulls.blobKey,
		&nulls.blobBytes,
		&nulls.uncompressedBytes,
		&nulls.blobSHA256,
		&report.AppBuild,
	); err != nil {
		return nil, fmt.Errorf("scan diagnostic report: %w", err)
	}
	if len(manifest) > 0 {
		report.Manifest = append(json.RawMessage(nil), manifest...)
	}
	nulls.apply(&report)
	return &report, nil
}

// summaryScanTargets returns the scan destinations shared by the admin-list and
// cleanup projections, in column order. The two scanners differ only in whether
// app_build is appended, so keeping the common prefix in one place stops the
// long Scan lists from drifting apart.
func summaryScanTargets(report *Report, nulls *reportNulls) []any {
	return []any{
		&report.ID,
		&report.ShortID,
		&report.UserID,
		&nulls.profileID,
		&report.State,
		&report.CapturedAt,
		&report.ReceivedAt,
		&report.ReportType,
		&report.Platform,
		&report.AppVersion,
		&nulls.crashSummary,
		&report.PlaybackSessionIDs,
		&nulls.blobBucket,
		&nulls.blobKey,
		&nulls.blobBytes,
		&nulls.uncompressedBytes,
		&nulls.blobSHA256,
	}
}

// scanReportSummary scans the reportListSelectSQL projection, which omits the
// manifest but includes app_build; the returned report's Manifest is left nil.
func scanReportSummary(row reportScanner) (*Report, error) {
	var report Report
	var nulls reportNulls
	targets := append(summaryScanTargets(&report, &nulls), &report.AppBuild)
	if err := row.Scan(targets...); err != nil {
		return nil, fmt.Errorf("scan diagnostic report: %w", err)
	}
	nulls.apply(&report)
	return &report, nil
}

// scanReportCleanup scans the reportCleanupSelectSQL projection, which omits
// both the manifest and app_build; the returned report's Manifest is nil and
// AppBuild is empty. Cleanup only reads id/state/blob fields.
func scanReportCleanup(row reportScanner) (*Report, error) {
	var report Report
	var nulls reportNulls
	if err := row.Scan(summaryScanTargets(&report, &nulls)...); err != nil {
		return nil, fmt.Errorf("scan diagnostic report: %w", err)
	}
	nulls.apply(&report)
	return &report, nil
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(*s) == "" {
		return nil
	}
	return *s
}

func encodeReportCursor(receivedAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%s", receivedAt.UnixNano(), id)))
}

func decodeReportCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: decode: %v", ErrInvalidCursor, err) //nolint:errorlint // the sentinel is the verdict on a client cursor; the decode error is only reported
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 2 {
		return time.Time{}, "", ErrInvalidCursor
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: timestamp: %v", ErrInvalidCursor, err) //nolint:errorlint // the sentinel is the verdict on a client cursor; the decode error is only reported
	}
	id := parts[1]
	if _, err := uuid.Parse(id); err != nil {
		return time.Time{}, "", fmt.Errorf("%w: id: %v", ErrInvalidCursor, err) //nolint:errorlint // the sentinel is the verdict on a client cursor; the decode error is only reported
	}
	return time.Unix(0, nanos).UTC(), id, nil
}
