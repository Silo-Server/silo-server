package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/logredact"
)

var ErrAudiobookEnrichmentClaimLost = errors.New("audiobook enrichment claim lost")

// Audiobook enrichment used to record its outcome in exactly one place:
// media_items.last_refreshed. That stamp had to mean "matched", "genuinely
// unmatchable" and "the provider was down that minute" simultaneously, so a bad
// afternoon on a provider was indistinguishable from a real no-match and the
// item was burned either way.
//
// audiobook_enrichment_state separates those. last_refreshed stays
// authoritative for eligibility -- this records why an item is where it is, and
// when it may be tried again.

// EnrichmentOutcome mirrors the ebook vocabulary so the two backlogs can be
// reported on with the same queries.
type EnrichmentOutcome string

const (
	EnrichmentOutcomeSuccess EnrichmentOutcome = "success"
	EnrichmentOutcomeNoMatch EnrichmentOutcome = "no_match"
	EnrichmentOutcomeSkipped EnrichmentOutcome = "skipped"
	// EnrichmentOutcomeFailed is terminal: the providers failed on this item
	// too many times in a row. The sweep stops calling them for it; an admin's
	// manual refresh or match does not read this state and can still try it.
	EnrichmentOutcomeFailed EnrichmentOutcome = "failed"
)

// Clean no-match results are retried a small number of times. Provider
// catalogs change, and a one-shot no-match made a temporary provider omission
// indistinguishable from a real absence. The no_matches count survives
// provider failures in between, so a flaky provider cannot hold an item in a
// retry loop. Skips (for example, a library with no configured provider
// chain) cost no provider calls and remain retryable indefinitely, hourly, so
// installing a provider later picks the item up soon without a rescan.
const (
	audiobookNoMatchRetryLimit = 3
	audiobookNoMatchRetryDelay = 24 * time.Hour
	audiobookSkippedRetryDelay = time.Hour
)

// Consecutive provider failures before an item becomes terminally failed. With
// the exponential backoff below, transient failures span about three days and
// rate-limited failures about five before the cap. A permanent failure gets
// one recheck, 30 days later.
const (
	audiobookFailureRetryLimit          = 10
	audiobookPermanentFailureRetryLimit = 2
	// audiobookMaxRetryAfter bounds a provider-supplied retry-after so a
	// garbage value cannot park an item or a provider for years.
	audiobookMaxRetryAfter = 7 * 24 * time.Hour
)

// EnrichmentErrorClass distinguishes failures that should come back from those
// that should not. Recording it is the difference between a readable backlog
// and the ebook situation, where 90,721 rows carried no error class at all and
// a rate-limited sweep was indistinguishable from 90,721 genuine no-matches.
type EnrichmentErrorClass string

const (
	EnrichmentErrorTransient   EnrichmentErrorClass = "transient"
	EnrichmentErrorRateLimited EnrichmentErrorClass = "rate_limited"
	EnrichmentErrorPermanent   EnrichmentErrorClass = "permanent"
)

// failurePolicy returns how a failure class backs off and how many
// consecutive failures make the item terminal. The parked interval is
// min(step * 2^(failures-1), ceiling), raised to any provider retry-after,
// computed inside the update itself so the write stays a single statement.
//
// Rate limiting starts slower than a transient failure: it is a statement
// about the provider, not the item, and retrying into a closed window is what
// turns a throttle into a backlog. Permanent failures are parked far out and
// rechecked once, because "permanent" is a classification and classifications
// are wrong sometimes.
func failurePolicy(class EnrichmentErrorClass) (step, ceiling time.Duration, limit int) {
	switch class {
	case EnrichmentErrorRateLimited:
		return time.Hour, 24 * time.Hour, audiobookFailureRetryLimit
	case EnrichmentErrorPermanent:
		return 30 * 24 * time.Hour, 30 * 24 * time.Hour, audiobookPermanentFailureRetryLimit
	default: // transient
		return 15 * time.Minute, 24 * time.Hour, audiobookFailureRetryLimit
	}
}

// enrichmentStateStore records attempt history for audiobook enrichment.
// A nil pool makes every method a no-op so partially wired constructions and
// tests behave exactly as they did before this table existed.
type enrichmentStateStore struct {
	pool *pgxpool.Pool
}

func newEnrichmentStateStore(pool *pgxpool.Pool) *enrichmentStateStore {
	return &enrichmentStateStore{pool: pool}
}

// claimFence limits a state write to the caller's live claim. An empty token
// ($2) is reserved for administrative repair paths and DB tests that write
// state without claiming work.
const claimFence = `content_id = $1 AND ($2 = '' OR (claim_token = $2 AND lease_until > now()))`

// recordOutcomeSet records a clean result. $3 outcome, $4 no-match limit,
// $5 no-match delay seconds, $6 skipped delay seconds. Right-hand column
// references read the row as it was before this update.
const recordOutcomeSet = `
	attempts         = attempts + 1,
	no_matches       = CASE WHEN $3 = 'no_match' THEN no_matches + 1
	                        WHEN $3 = 'success' THEN 0
	                        ELSE no_matches END,
	failures         = CASE WHEN $3 = 'skipped' THEN failures ELSE 0 END,
	outcome          = $3,
	last_error_class = NULL,
	last_error       = NULL,
	next_attempt_at  = CASE
	    WHEN $3 = 'no_match' AND no_matches + 1 < $4
	        THEN now() + make_interval(secs => $5::double precision)
	    WHEN $3 = 'skipped'
	        THEN now() + make_interval(secs => $6::double precision)
	    ELSE NULL
	END,
	last_attempt_at  = now(),
	completed_at     = CASE
	    WHEN $3 = 'success' OR ($3 = 'no_match' AND no_matches + 1 >= $4)
	        THEN now() ELSE NULL END,
	claim_token      = NULL,
	lease_until      = NULL,
	updated_at       = now()`

func recordOutcomeArgs(contentID, claimToken string, outcome EnrichmentOutcome) []any {
	return []any{contentID, claimToken, string(outcome), audiobookNoMatchRetryLimit,
		audiobookNoMatchRetryDelay.Seconds(), audiobookSkippedRetryDelay.Seconds()}
}

// ensureRow creates the state row an unclaimed write updates.
func ensureRow(ctx context.Context, tx pgx.Tx, contentID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO audiobook_enrichment_state (content_id, updated_at)
		VALUES ($1, now())
		ON CONFLICT (content_id) DO NOTHING
	`, contentID)
	return err
}

// execState runs a state update fenced by claimFence. A claimed write that
// matches no row lost its claim. An unclaimed write first creates the row,
// in the same transaction.
func (s *enrichmentStateStore) execState(ctx context.Context, contentID, claimToken, sql string, args []any) error {
	if claimToken != "" {
		tag, err := s.pool.Exec(ctx, sql, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrAudiobookEnrichmentClaimLost
		}
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := ensureRow(ctx, tx, contentID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
}

// RecordOutcome records a clean result and clears any active lease. Successful
// results are terminal; no-match results are retried a bounded number of times
// and skipped results are parked for a later configuration retry.
func (s *enrichmentStateStore) RecordOutcome(ctx context.Context, contentID, claimToken string, outcome EnrichmentOutcome) error {
	if s == nil || s.pool == nil || contentID == "" {
		return nil
	}
	err := s.execState(ctx, contentID, claimToken,
		`UPDATE audiobook_enrichment_state SET `+recordOutcomeSet+` WHERE `+claimFence,
		recordOutcomeArgs(contentID, claimToken, outcome))
	if err != nil && !errors.Is(err, ErrAudiobookEnrichmentClaimLost) {
		return fmt.Errorf("recording audiobook enrichment outcome: %w", err)
	}
	return err
}

// ReleaseClaim returns unprocessed work to the queue when a sweep is
// canceled. A canceled request context cannot be used for cleanup, so callers
// should pass a short-lived context derived from context.WithoutCancel.
func (s *enrichmentStateStore) ReleaseClaim(ctx context.Context, contentID, claimToken string) error {
	if s == nil || s.pool == nil || contentID == "" || claimToken == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE audiobook_enrichment_state
		SET claim_token = NULL,
		    lease_until = NULL,
		    updated_at  = now()
		WHERE content_id = $1 AND claim_token = $2
	`, contentID, claimToken)
	if err != nil {
		return fmt.Errorf("releasing audiobook enrichment claim: %w", err)
	}
	return nil
}

// Defer returns a claimed item to the queue without counting an attempt and
// keeps it out until until. The sweep uses it when every provider in the
// item's chain is paused after a rate limit: no provider was asked, so the
// item's history is unchanged.
func (s *enrichmentStateStore) Defer(ctx context.Context, contentID, claimToken string, until time.Time) error {
	if s == nil || s.pool == nil || contentID == "" || claimToken == "" {
		return nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE audiobook_enrichment_state
		SET next_attempt_at = GREATEST(COALESCE(next_attempt_at, $3), $3),
		    claim_token     = NULL,
		    lease_until     = NULL,
		    updated_at      = now()
		WHERE content_id = $1 AND claim_token = $2 AND lease_until > now()
	`, contentID, claimToken, until)
	if err != nil {
		return fmt.Errorf("deferring audiobook enrichment claim: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAudiobookEnrichmentClaimLost
	}
	return nil
}

// recordFailureSet stamps a failed attempt. $3 class, $4 cause, $5 step
// seconds, $6 ceiling seconds, $7 retry-after seconds, $8 failure limit.
// Reaching the limit makes the item terminal with outcome 'failed'; below it
// the outcome is cleared, because the item is pending another try.
const recordFailureSet = `
	attempts         = attempts + 1,
	failures         = failures + 1,
	outcome          = CASE WHEN failures + 1 >= $8 THEN 'failed' ELSE NULL END,
	last_error_class = $3,
	last_error       = $4,
	next_attempt_at  = CASE WHEN failures + 1 >= $8 THEN NULL
	    ELSE now() + make_interval(secs => GREATEST(
	        LEAST($5::double precision * power(2, LEAST(failures, 30)), $6::double precision),
	        $7::double precision
	    )) END,
	last_attempt_at  = now(),
	completed_at     = CASE WHEN failures + 1 >= $8 THEN now() ELSE NULL END,
	claim_token      = NULL,
	lease_until      = NULL,
	updated_at       = now()`

// RecordFailure stamps a failed attempt and parks the item for a retry sized to
// how it failed, at least retryAfter out. After too many consecutive failures
// the item becomes terminally failed and the sweep stops spending provider
// calls on it.
//
// The claimed write is one statement, deliberately. Recording the failure and
// parking the retry used to be two round trips, and anything landing between
// them -- a canceled context, a dropped connection, a concurrent
// RecordOutcome on the same row -- left the item with an incremented attempts
// count but no backoff at all, immediately re-claimable against the very
// provider that just failed.
func (s *enrichmentStateStore) RecordFailure(
	ctx context.Context,
	contentID, claimToken string,
	class EnrichmentErrorClass,
	retryAfter time.Duration,
	cause string,
) error {
	if s == nil || s.pool == nil || contentID == "" {
		return nil
	}
	// Truncate on a rune boundary: a provider stack trace in a status column
	// helps nobody, and a byte-index slice can split a UTF-8 sequence --
	// Postgres rejects invalid UTF-8, which would silently fail the whole
	// failure/backoff write for that call.
	// last_error is shown to admins and kept in the database; mask secrets a
	// provider may have echoed into its error text, as the log handler does.
	cause = redactProviderText(cause)
	const maxCause = 500
	if len(cause) > maxCause {
		cause = strings.ToValidUTF8(cause[:maxCause], "")
	}
	step, ceiling, limit := failurePolicy(class)
	retryAfter = min(max(retryAfter, 0), audiobookMaxRetryAfter)
	err := s.execState(ctx, contentID, claimToken,
		`UPDATE audiobook_enrichment_state SET `+recordFailureSet+` WHERE `+claimFence,
		[]any{contentID, claimToken, string(class), cause, step.Seconds(), ceiling.Seconds(), retryAfter.Seconds(), limit})
	if err != nil && !errors.Is(err, ErrAudiobookEnrichmentClaimLost) {
		return fmt.Errorf("recording audiobook enrichment failure: %w", err)
	}
	return err
}

// AssertClaimTx fences terminal media writes with the durable claim row. The
// row lock prevents another replica from replacing the token between this
// check and the transaction's metadata/state commit.
func (s *enrichmentStateStore) AssertClaimTx(ctx context.Context, tx pgx.Tx, contentID, claimToken string) error {
	if s == nil || tx == nil || claimToken == "" {
		return nil
	}
	// claimBatch locks media_items before it inserts or updates the state row.
	// Keep terminal transactions in the same lock order to avoid a deadlock at
	// the instant an old lease expires and another replica tries to reclaim it.
	var lockedContentID string
	err := tx.QueryRow(ctx, `
		SELECT content_id
		FROM media_items
		WHERE content_id = $1
		FOR UPDATE
	`, contentID).Scan(&lockedContentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAudiobookEnrichmentClaimLost
	}
	if err != nil {
		return fmt.Errorf("locking claimed audiobook: %w", err)
	}
	var token string
	err = tx.QueryRow(ctx, `
		SELECT claim_token
		FROM audiobook_enrichment_state
		WHERE content_id = $1
		  AND claim_token = $2
		  AND lease_until > now()
		FOR UPDATE
	`, contentID, claimToken).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAudiobookEnrichmentClaimLost
	}
	if err != nil {
		return fmt.Errorf("checking audiobook enrichment claim: %w", err)
	}
	return nil
}

// RecordOutcomeTx completes a claim inside the same transaction as its durable
// provider IDs, scalar metadata, search event, and media timestamp. A clean
// no-match or skipped result remains eligible according to its retry policy.
func (s *enrichmentStateStore) RecordOutcomeTx(
	ctx context.Context,
	tx pgx.Tx,
	contentID, claimToken string,
	outcome EnrichmentOutcome,
) error {
	if s == nil || tx == nil || claimToken == "" {
		return nil
	}
	tag, err := tx.Exec(ctx,
		`UPDATE audiobook_enrichment_state SET `+recordOutcomeSet+` WHERE `+claimFence,
		recordOutcomeArgs(contentID, claimToken, outcome)...)
	if err != nil {
		return fmt.Errorf("completing audiobook enrichment claim: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAudiobookEnrichmentClaimLost
	}
	return nil
}

// redactProviderText masks credentials in provider error text before it is
// stored or logged. Plugin errors are free text, often a gRPC status
// description quoting the upstream request, and the log handler only masks
// secret-named keys, not values.
func redactProviderText(text string) string {
	return logredact.SanitizeText(logredact.RedactSecretAssignments(text))
}
