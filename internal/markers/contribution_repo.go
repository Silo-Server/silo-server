package markers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

const contributionClaimIndex = "marker_contributions_provider_target_active_uidx"

// ContributionRow is one submission audit record from marker_contributions.
type ContributionRow struct {
	ID               string
	ClaimToken       string
	MediaFileID      int
	Provider         string
	SegmentKind      string
	Source           string
	SubmittedStartMs *int64
	SubmittedEndMs   *int64
	VideoDurationMs  *int64
	ContentHash      string
	TargetKey        string
	SubmissionID     *string
	Status           string
	HTTPStatus       *int
	Error            *string
	SubmittedAt      time.Time
	UpdatedAt        time.Time
	// Amend lets Claim take over the target from a settled claim with a
	// different content hash. It is set for providers that support amend and
	// is not stored.
	Amend bool
	// Algorithm is the marker's detection version, sent with the submission.
	// It is not stored.
	Algorithm string
	// Correction makes Claim take a manual marker only as a correction of
	// an earlier submission: the provider has a row for the file's segment.
	// The daily task resends manual markers this way. It is not stored.
	Correction bool
}

// ContributionClaim identifies one ownership generation of a contribution
// row. The token fences a late worker from recording over a reclaimed claim.
type ContributionClaim struct {
	ID    string
	Token string
}

// ContributionStore persists marker contribution attempts for idempotency and
// audit.
type ContributionStore struct {
	pool *pgxpool.Pool
}

// NewContributionStore constructs a store backed by the supplied pool.
func NewContributionStore(pool *pgxpool.Pool) *ContributionStore {
	return &ContributionStore{pool: pool}
}

// ContentHash is a stable hash over the contributed value, the resolved target,
// and for amend providers the marker's source and algorithm. A marker
// correction or rematch hashes differently; only a provider that supports
// amend is sent the new value for a target it already holds.
func ContentHash(segmentKind string, startMs, endMs, durationMs *int64, parts ...string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s", segmentKind, ptrIntStr(startMs), ptrIntStr(endMs), ptrIntStr(durationMs))
	for _, part := range parts {
		fmt.Fprintf(h, "|%s", part)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func ptrIntStr(v *int64) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatInt(*v, 10)
}

// confidenceHashPart is a marker's confidence as an amend hash carries it, in
// whole thousandths, missing as 0 the way the request sends it.
// currentAmendHashSQL writes the same. A value outside 0 to 1, NaN included,
// hashes as "x", which keeps it out of the SQL integer cast.
func confidenceHashPart(confidence *float64) string {
	c := 0.0
	if confidence != nil {
		c = *confidence
	}
	if !(c >= 0 && c <= 1) {
		return "x"
	}
	return strconv.FormatInt(int64(c*1000), 10)
}

// currentAmendHashSQL is the content hash newContributionRow gives an amend
// provider's row for the segment seg (kind, start_s, end_s, source,
// algorithm, confidence) of media file mf, from the database's current state: the
// marker (a manual clear's missing times hash as ContentHash writes them), the
// file's duration, hash and size, and the IDs and numbering
// DBExternalIDResolver reads for the file. It is NULL when the resolver would
// find no item. Claim and both daily candidate queries use it, so they cannot
// disagree.
var currentAmendHashSQL = `left(encode(sha256(convert_to(
	    seg.kind
	    || '|' || COALESCE(trunc(seg.start_s * 1000)::bigint::text, 'null')
	    || '|' || COALESCE(trunc(seg.end_s * 1000)::bigint::text, 'null')
	    || '|' || COALESCE(mf.duration, 0) * 1000
	    || '|' || COALESCE(
	        (SELECT 'episode|' || COALESCE(si.tmdb_id, '') || '|' || COALESCE(si.imdb_id, '') || '|' || COALESCE(si.tvdb_id, '')
	                || '|' || COALESCE(NULLIF(CASE WHEN COALESCE(e.season_number, 0) < 0 THEN COALESCE(mf.season_number, 0)
	                                               ELSE COALESCE(e.season_number, 0) END, 0)::text, '')
	                || '|' || COALESCE(NULLIF(CASE WHEN COALESCE(e.episode_number, 0) <= 0 THEN COALESCE(mf.episode_number, 0)
	                                               ELSE e.episode_number END, 0)::text, '')
	                || '|' || COALESCE(BTRIM(e.tmdb_id), '') || '|' || COALESCE(BTRIM(e.imdb_id), '') || '|' || COALESCE(BTRIM(e.tvdb_id), '')
	                || '|' || ` + episodeNumberingSQL("mf.media_folder_id") + `
	         FROM episodes e
	         LEFT JOIN media_items si ON si.content_id = e.series_id
	         WHERE e.content_id = NULLIF(BTRIM(mf.episode_id), '')),
	        (SELECT 'movie|' || COALESCE(mi.tmdb_id, '') || '|' || COALESCE(mi.imdb_id, '') || '|' || COALESCE(mi.tvdb_id, '') || '||||||'
	         FROM media_items mi
	         WHERE NULLIF(BTRIM(mf.episode_id), '') IS NULL
	           AND mi.content_id = NULLIF(BTRIM(mf.content_id), '') AND mi.type = 'movie'))
	    || '|' || COALESCE(mf.file_hash, '') || '|' || COALESCE(mf.file_size, 0)
	    || '|' || seg.source || '|' || COALESCE(seg.algorithm, '')
	    || '|' || CASE WHEN COALESCE(seg.confidence, 0) BETWEEN 0 AND 1
	                   THEN trunc(COALESCE(seg.confidence, 0) * 1000)::bigint::text ELSE 'x' END,
	    'UTF8')), 'hex'), 32)`

// errContributionInFlight is Claim's answer when row.Amend is set and another
// worker is still submitting a different marker for the target. The daily
// task offers the marker once that submission settles.
var errContributionInFlight = errors.New("another marker submission for this target is in flight")

// errContributionSuperseded is Claim's answer when row.Amend is set and row's
// content hash is no longer the one the file's current state gives
// (currentAmendHashSQL): a later save, detection, probe, or metadata change
// replaced something the payload carries, and the new state is sent instead.
var errContributionSuperseded = errors.New("the file's marker changed since it was read")

// errContributionNotCorrection is Claim's answer when row.Correction is set
// and the provider may not take row as a correction.
var errContributionNotCorrection = errors.New("the manual marker is not a correction for this provider")

// Claim atomically reserves a provider target before the network call. An
// active row for the same provider, segment, and target blocks the claim,
// whatever times it carried, unless row.Amend is set and the row is settled
// with a different content hash: the provider then replaces its earlier
// submission. A fresh in-flight row with a different hash returns
// errContributionInFlight for an amend row, and an amend row whose content
// hash no longer matches the file's current state returns
// errContributionSuperseded, checked under the claim lock so an older payload
// never replaces a newer one at the provider. A row.Correction the provider
// may not take returns errContributionNotCorrection. A stale in-flight row, or
// an invalid refusal older than contributionInvalidRecheck, can be reclaimed;
// recording a retryable error releases the claim. The advisory lock and
// partial unique index serialize claims across local media files and server
// workers.
func (s *ContributionStore) Claim(ctx context.Context, row ContributionRow, staleAfter time.Duration) (ContributionClaim, bool, error) {
	if s == nil || s.pool == nil {
		return ContributionClaim{}, false, fmt.Errorf("contribution store unavailable")
	}
	if staleAfter <= 0 {
		return ContributionClaim{}, false, fmt.Errorf("contribution claim lease must be positive")
	}
	if row.TargetKey == "" {
		return ContributionClaim{}, false, fmt.Errorf("contribution claim target missing")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ContributionClaim{}, false, fmt.Errorf("begin marker contribution claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended($1 || chr(31) || $2 || chr(31) || $3, 0)
		)`, row.Provider, row.SegmentKind, row.TargetKey); err != nil {
		return ContributionClaim{}, false, fmt.Errorf("lock marker contribution claim: %w", err)
	}

	if row.Amend {
		var current bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1
			    FROM media_files mf
			    CROSS JOIN LATERAL (VALUES
			        ('intro', mf.intro_start, mf.intro_end, mf.intro_markers_source, mf.intro_markers_algorithm, mf.intro_markers_confidence),
			        ('credits', mf.credits_start, mf.credits_end, mf.credits_markers_source, mf.credits_markers_algorithm, mf.credits_markers_confidence),
			        ('recap', mf.recap_start, mf.recap_end, mf.recap_markers_source, mf.recap_markers_algorithm, mf.recap_markers_confidence),
			        ('preview', mf.preview_start, mf.preview_end, mf.preview_markers_source, mf.preview_markers_algorithm, mf.preview_markers_confidence)
			    ) AS seg(kind, start_s, end_s, source, algorithm, confidence)
			    WHERE mf.id = $1 AND seg.kind = $2
			      AND `+currentAmendHashSQL+` = $3
			)`, row.MediaFileID, row.SegmentKind, row.ContentHash).Scan(&current); err != nil {
			return ContributionClaim{}, false, fmt.Errorf("check marker contribution is current: %w", err)
		}
		if !current {
			return ContributionClaim{}, false, errContributionSuperseded
		}
	}
	if row.Correction {
		// CandidateManualCorrections applies the same rule.
		var correction bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1 FROM marker_contributions
			    WHERE media_file_id = $1 AND provider = $2 AND segment_kind = $3
			)`, row.MediaFileID, row.Provider, row.SegmentKind).Scan(&correction); err != nil {
			return ContributionClaim{}, false, fmt.Errorf("check manual marker correction: %w", err)
		}
		if !correction {
			return ContributionClaim{}, false, errContributionNotCorrection
		}
	}

	leaseSeconds := int64(staleAfter / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}
	recheckSeconds := int64(contributionInvalidRecheck / time.Second)
	var activeID string
	var reclaimable, replaceable bool
	err = tx.QueryRow(ctx, `
		SELECT id,
		       (status = $4 AND updated_at < now() - ($5 * interval '1 second'))
		       OR (status = $6 AND updated_at < now() - ($7 * interval '1 second'))
		       OR ($8 AND status <> $4 AND content_hash <> $9),
		       $8 AND content_hash <> $9
		FROM marker_contributions
		WHERE provider = $1 AND segment_kind = $2 AND target_key = $3
		  AND claim_active`,
		row.Provider, row.SegmentKind, row.TargetKey,
		contributionStatusClaim, leaseSeconds,
		OutcomeStatusInvalid, recheckSeconds,
		row.Amend, row.ContentHash,
	).Scan(&activeID, &reclaimable, &replaceable)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ContributionClaim{}, false, fmt.Errorf("find marker contribution claim: %w", err)
	}
	if err == nil {
		if replaceable && !reclaimable {
			return ContributionClaim{}, false, errContributionInFlight
		}
		if !reclaimable {
			return ContributionClaim{}, false, nil
		}
		// Hand the target over by retiring the old row; its audit record stays.
		if _, err := tx.Exec(ctx, `
			UPDATE marker_contributions SET claim_active = false, updated_at = now()
			WHERE id = $1 AND claim_active`, activeID); err != nil {
			return ContributionClaim{}, false, fmt.Errorf("release stale marker contribution claim: %w", err)
		}
	}

	var claim ContributionClaim
	err = tx.QueryRow(ctx, `
		INSERT INTO marker_contributions (
			media_file_id, provider, segment_kind, source,
			submitted_start_ms, submitted_end_ms, video_duration_ms,
			content_hash, target_key, status, claim_active, claim_token, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true,gen_random_uuid(),now())
		ON CONFLICT (media_file_id, provider, segment_kind, content_hash) DO UPDATE SET
			source = EXCLUDED.source,
			submitted_start_ms = EXCLUDED.submitted_start_ms,
			submitted_end_ms = EXCLUDED.submitted_end_ms,
			video_duration_ms = EXCLUDED.video_duration_ms,
			target_key = EXCLUDED.target_key,
			submission_id = NULL,
			status = EXCLUDED.status,
			http_status = NULL,
			error = NULL,
			claim_active = true,
			claim_token = gen_random_uuid(),
			updated_at = now()
		WHERE NOT marker_contributions.claim_active
		RETURNING id, claim_token`,
		row.MediaFileID, row.Provider, row.SegmentKind, row.Source,
		row.SubmittedStartMs, row.SubmittedEndMs, row.VideoDurationMs,
		row.ContentHash, row.TargetKey, contributionStatusClaim,
	).Scan(&claim.ID, &claim.Token)
	if errors.Is(err, pgx.ErrNoRows) || isContributionClaimConflict(err) {
		return ContributionClaim{}, false, nil
	}
	if err != nil {
		return ContributionClaim{}, false, fmt.Errorf("claim marker contribution: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ContributionClaim{}, false, fmt.Errorf("commit marker contribution claim: %w", err)
	}
	return claim, true, nil
}

func isContributionClaimConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == contributionClaimIndex
}

// Record completes a claimed contribution. Retryable errors release the
// provider-target claim; every other result preserves it for deduplication.
func (s *ContributionStore) Record(ctx context.Context, row ContributionRow) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("contribution store unavailable")
	}
	if row.ID == "" || row.ClaimToken == "" {
		return fmt.Errorf("record marker contribution: claim identity missing")
	}
	claimActive := row.Status != OutcomeStatusError
	tag, err := s.pool.Exec(ctx, `
		UPDATE marker_contributions SET
			source = $3,
			submitted_start_ms = $4,
			submitted_end_ms = $5,
			video_duration_ms = $6,
			submission_id = $7,
			status = $8,
			http_status = $9,
			error = $10,
			claim_active = $11,
			updated_at = now()
		WHERE id = $1 AND claim_token = $2 AND claim_active`,
		row.ID, row.ClaimToken, row.Source,
		row.SubmittedStartMs, row.SubmittedEndMs, row.VideoDurationMs,
		row.SubmissionID, row.Status, row.HTTPStatus, row.Error,
		claimActive,
	)
	if err != nil {
		return fmt.Errorf("record marker contribution: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("record marker contribution: claim not found")
	}
	return nil
}

// ContributionCandidate is one auto-contribution candidate and its keyset
// position.
type ContributionCandidate struct {
	FileID     int
	Confidence float64
}

// AutoContributor is a provider with automatic sharing enabled, with the
// settings that decide which local markers it accepts.
type AutoContributor struct {
	Provider      string
	MinConfidence float64
	// RequiredExternalIDs are the series IDs the provider requires, in the
	// order it keys submissions by them.
	RequiredExternalIDs []string
	// SupportsAmend keys the provider's claims by file and offers a file again
	// when its marker no longer hashes as the claim did.
	SupportsAmend bool
}

// CandidateLocalMarkerFiles returns episode files carrying a local (scanner)
// marker of kind, an intro or credits, that at least one of providers would
// submit, highest confidence first. A provider would submit a file when the
// marker meets its minimum confidence, the series carries every external ID it
// requires, and it holds no claim on the file's current target for that
// segment, from this file or another version of the episode, that Claim would
// not hand over; a claim on an older target, a stale in-flight claim, and an
// invalid refusal due for its recheck leave the file eligible. For a provider
// that supports amend the target is the file itself, and a settled claim whose
// content hash differs from the marker's current one leaves it eligible too.
// A file every provider declines is not returned, and returns once a
// provider's reason no longer holds.
// Paging is by keyset: pass the last candidate returned (nil for the first
// page).
func (s *ContributionStore) CandidateLocalMarkerFiles(ctx context.Context, kind MarkerKind, providers []AutoContributor, after *ContributionCandidate, limit int) ([]ContributionCandidate, error) {
	if s == nil || s.pool == nil || len(providers) == 0 {
		return nil, nil
	}
	if !autoContributedKind(kind) {
		return nil, fmt.Errorf("automatic contribution does not share %q markers", markerKindName(kind))
	}
	ids := make([]string, len(providers))
	minimums := make([]float64, len(providers))
	required := make([]string, len(providers))
	amend := make([]bool, len(providers))
	minConfidence := providers[0].MinConfidence
	for i, p := range providers {
		ids[i], minimums[i], amend[i] = p.Provider, p.MinConfidence, p.SupportsAmend
		required[i] = strings.Join(normalizeExternalIDKeys(p.RequiredExternalIDs), ",")
		minConfidence = min(minConfidence, p.MinConfidence)
	}
	afterConfidence, afterID := 2.0, 0
	if after != nil {
		afterConfidence, afterID = after.Confidence, after.FileID
	}
	// series_ids holds the series' non-blank external IDs. The target key
	// follows contributionTargetKey: the provider's first required ID, or else
	// TMDB, TVDB, then IMDb; an amend provider's is amendTargetKey.
	rows, err := s.pool.Query(ctx, `
		SELECT mf.id, seg.confidence
		FROM media_files mf
		JOIN episodes e ON e.content_id = mf.episode_id
		LEFT JOIN media_items series ON series.content_id = e.series_id
		CROSS JOIN LATERAL (SELECT jsonb_strip_nulls(jsonb_build_object(
		    'tmdb', NULLIF(BTRIM(series.tmdb_id), ''),
		    'tvdb', NULLIF(BTRIM(series.tvdb_id), ''),
		    'imdb', NULLIF(BTRIM(series.imdb_id), '')
		)) AS ids) AS series_ids
		CROSS JOIN LATERAL (VALUES
		    ('intro', mf.intro_markers_source, mf.intro_start, mf.intro_end, COALESCE(mf.intro_markers_confidence, 0), mf.intro_markers_algorithm),
		    ('credits', mf.credits_markers_source, mf.credits_start, mf.credits_end, COALESCE(mf.credits_markers_confidence, 0), mf.credits_markers_algorithm)
		) AS seg(kind, source, start_s, end_s, confidence, algorithm)
		WHERE seg.kind = $11
		  AND mf.episode_id IS NOT NULL
		  AND seg.source = $1
		  AND seg.start_s IS NOT NULL AND seg.end_s IS NOT NULL
		  AND seg.confidence >= $2
		  AND e.season_number > 0 AND e.episode_number > 0
		  AND series_ids.ids <> '{}'::jsonb
		  AND (seg.confidence, -mf.id) < ($3::double precision, -$4::integer)
		  AND EXISTS (
		      SELECT 1
		      FROM unnest($5::text[], $12::double precision[], $13::text[], $14::boolean[]) AS p(provider, min_confidence, required, amend)
		      CROSS JOIN LATERAL (SELECT string_to_array(NULLIF(p.required, ''), ',') AS keys) AS req
		      WHERE seg.confidence >= p.min_confidence
		        AND series_ids.ids ?& COALESCE(req.keys, '{}')
		        AND NOT EXISTS (
		            SELECT 1
		            FROM marker_contributions mc
		            WHERE mc.provider = p.provider
		              AND mc.segment_kind = seg.kind
		              AND mc.claim_active
		              AND mc.target_key = CASE WHEN p.amend THEN 'file:' || mf.id
		                  ELSE 'episode|' || COALESCE(
		                      req.keys[1] || ':' || (series_ids.ids ->> req.keys[1]),
		                      'tmdb:' || (series_ids.ids ->> 'tmdb'),
		                      'tvdb:' || (series_ids.ids ->> 'tvdb'),
		                      'imdb:' || (series_ids.ids ->> 'imdb')
		                  ) || '|' || e.season_number || '|' || e.episode_number END
		              AND NOT (mc.status = $7 AND mc.updated_at < now() - ($8 * interval '1 second'))
		              AND NOT (mc.status = $9 AND mc.updated_at < now() - ($10 * interval '1 second'))
		              AND (NOT p.amend OR mc.status = $7 OR mc.content_hash = `+currentAmendHashSQL+`)
		        )
		  )
		ORDER BY seg.confidence DESC, mf.id
		LIMIT $6`,
		models.MarkerSourceScanner, minConfidence, afterConfidence, afterID, ids, limit,
		contributionStatusClaim, int64(contributionClaimLease/time.Second),
		OutcomeStatusInvalid, int64(contributionInvalidRecheck/time.Second),
		markerKindName(kind), minimums, required, amend,
	)
	if err != nil {
		return nil, fmt.Errorf("query contribution candidates: %w", err)
	}
	defer rows.Close()

	var out []ContributionCandidate
	for rows.Next() {
		var c ContributionCandidate
		if err := rows.Scan(&c.FileID, &c.Confidence); err != nil {
			return nil, fmt.Errorf("scan contribution candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AmendContributor is a provider that supports amend, as the daily task's
// manual corrections pass sees it.
type AmendContributor struct {
	Provider string
	// SupportsAbsent means the provider also takes a manual clear.
	SupportsAbsent bool
}

// CandidateManualCorrections returns files, by ascending id, carrying a manual
// marker (ManualAlgorithm, any kind, movie or episode) that one of providers,
// all supporting amend, may take as a correction (ContributionRow.Correction)
// and does not hold yet. A manual clear counts only for a provider that
// supports absent submissions. The provider has a row for the file's segment,
// none of its active claims there has the content hash the file's current state
// gives (currentAmendHashSQL), and none is a fresh in-flight claim, which
// Claim would refuse. The hash covers the item's IDs, so a claim taken under
// IDs the item no longer has never matches. A stale in-flight claim and an
// invalid refusal due for its recheck do not count. Paging is by keyset: pass
// the last candidate returned (nil for the first page).
func (s *ContributionStore) CandidateManualCorrections(ctx context.Context, providers []AmendContributor, after *ContributionCandidate, limit int) ([]ContributionCandidate, error) {
	if s == nil || s.pool == nil || len(providers) == 0 || limit <= 0 {
		return nil, nil
	}
	ids := make([]string, len(providers))
	absent := make([]bool, len(providers))
	for i, p := range providers {
		ids[i], absent[i] = p.Provider, p.SupportsAbsent
	}
	afterID := 0
	if after != nil {
		afterID = after.FileID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT mf.id
		FROM media_files mf
		WHERE mf.id > $2
		  AND mf.id IN (SELECT media_file_id FROM marker_contributions WHERE provider = ANY($1::text[]))
		  AND EXISTS (
		      SELECT 1
		      FROM (VALUES
		          ('intro', mf.intro_markers_source, mf.intro_markers_algorithm, mf.intro_start, mf.intro_end, mf.intro_markers_confidence),
		          ('credits', mf.credits_markers_source, mf.credits_markers_algorithm, mf.credits_start, mf.credits_end, mf.credits_markers_confidence),
		          ('recap', mf.recap_markers_source, mf.recap_markers_algorithm, mf.recap_start, mf.recap_end, mf.recap_markers_confidence),
		          ('preview', mf.preview_markers_source, mf.preview_markers_algorithm, mf.preview_start, mf.preview_end, mf.preview_markers_confidence)
		      ) AS seg(kind, source, algorithm, start_s, end_s, confidence)
		      CROSS JOIN unnest($1::text[], $10::boolean[]) AS p(provider, absent)
		      WHERE seg.source = $4 AND seg.algorithm = $5
		        AND ((seg.start_s IS NOT NULL AND seg.end_s IS NOT NULL)
		             OR (p.absent AND seg.start_s IS NULL AND seg.end_s IS NULL))
		        AND EXISTS (
		            SELECT 1 FROM marker_contributions mc
		            WHERE mc.media_file_id = mf.id AND mc.provider = p.provider AND mc.segment_kind = seg.kind
		        )
		        AND NOT EXISTS (
		            SELECT 1 FROM marker_contributions mc
		            WHERE mc.media_file_id = mf.id AND mc.provider = p.provider AND mc.segment_kind = seg.kind
		              AND mc.claim_active
		              AND NOT (mc.status = $6 AND mc.updated_at < now() - ($7 * interval '1 second'))
		              AND NOT (mc.status = $8 AND mc.updated_at < now() - ($9 * interval '1 second'))
		              AND (mc.status = $6 OR mc.content_hash = `+currentAmendHashSQL+`)
		        )
		  )
		ORDER BY mf.id
		LIMIT $3`,
		ids, afterID, limit,
		models.MarkerSourceManual, ManualAlgorithm,
		contributionStatusClaim, int64(contributionClaimLease/time.Second),
		OutcomeStatusInvalid, int64(contributionInvalidRecheck/time.Second),
		absent,
	)
	if err != nil {
		return nil, fmt.Errorf("query manual correction candidates: %w", err)
	}
	defer rows.Close()

	var out []ContributionCandidate
	for rows.Next() {
		var c ContributionCandidate
		if err := rows.Scan(&c.FileID); err != nil {
			return nil, fmt.Errorf("scan manual correction candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListByFile returns the contribution history for a file, newest first.
func (s *ContributionStore) ListByFile(ctx context.Context, fileID int) ([]ContributionRow, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, media_file_id, provider, segment_kind, source,
		       submitted_start_ms, submitted_end_ms, video_duration_ms,
		       content_hash, submission_id, status, http_status, error,
		       submitted_at, updated_at
		FROM marker_contributions WHERE media_file_id = $1
		ORDER BY updated_at DESC`, fileID)
	if err != nil {
		return nil, fmt.Errorf("list marker contributions: %w", err)
	}
	defer rows.Close()

	var out []ContributionRow
	for rows.Next() {
		var r ContributionRow
		if err := rows.Scan(
			&r.ID, &r.MediaFileID, &r.Provider, &r.SegmentKind, &r.Source,
			&r.SubmittedStartMs, &r.SubmittedEndMs, &r.VideoDurationMs,
			&r.ContentHash, &r.SubmissionID, &r.Status, &r.HTTPStatus, &r.Error,
			&r.SubmittedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan marker contribution: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ContributionPagePosition is a descending keyset position, not a snapshot.
// Rows updated during traversal may move ahead of the current page.
type ContributionPagePosition struct {
	UpdatedAt time.Time
	ID        string
}

func nullContributionTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (s *ContributionStore) ListByFilePage(ctx context.Context, fileID int, limit int, after ContributionPagePosition) ([]ContributionRow, bool, error) {
	if s == nil || s.pool == nil {
		return []ContributionRow{}, false, nil
	}
	if limit < 1 || limit > 200 {
		return nil, false, fmt.Errorf("invalid contribution page limit")
	}
	if after.ID == "" {
		after.ID = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, media_file_id, provider, segment_kind, source,
		       submitted_start_ms, submitted_end_ms, video_duration_ms,
		       content_hash, submission_id, status, http_status, error,
		       submitted_at, updated_at
		FROM marker_contributions WHERE media_file_id = $1
		AND ($2::timestamptz IS NULL OR (updated_at, id) < ($2, $3::uuid))
 ORDER BY updated_at DESC, id DESC LIMIT $4`, fileID, nullContributionTime(after.UpdatedAt), after.ID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list marker contributions: %w", err)
	}
	defer rows.Close()

	var out []ContributionRow
	for rows.Next() {
		var r ContributionRow
		if err := rows.Scan(
			&r.ID, &r.MediaFileID, &r.Provider, &r.SegmentKind, &r.Source,
			&r.SubmittedStartMs, &r.SubmittedEndMs, &r.VideoDurationMs,
			&r.ContentHash, &r.SubmissionID, &r.Status, &r.HTTPStatus, &r.Error,
			&r.SubmittedAt, &r.UpdatedAt,
		); err != nil {
			return nil, false, fmt.Errorf("scan marker contribution: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}
