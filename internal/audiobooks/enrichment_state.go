package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// backoffParams returns the per-attempt step and the ceiling for a failure
// class. The parked interval is min(step * attempts, cap), computed inside the
// upsert itself so the write stays a single atomic statement.
//
// Rate limiting backs off hardest and fastest: it is a statement about the
// provider, not the item, and retrying into a closed window is what turns a
// throttle into a backlog. Permanent failures are parked far out rather than
// never, because "permanent" is a classification and classifications are wrong
// sometimes -- the step equals the cap so attempts do not extend it.
func backoffParams(class EnrichmentErrorClass) (step, ceiling time.Duration) {
	switch class {
	case EnrichmentErrorRateLimited:
		return time.Hour, 24 * time.Hour
	case EnrichmentErrorPermanent:
		return 30 * 24 * time.Hour, 30 * 24 * time.Hour
	default: // transient
		return 15 * time.Minute, 6 * time.Hour
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

// RecordOutcome stamps a terminal result and clears any parked retry or active
// lease. Production passes the claim token; an empty token is reserved for
// administrative repair paths and DB tests that intentionally write state
// without claiming work first.
func (s *enrichmentStateStore) RecordOutcome(ctx context.Context, contentID, claimToken string, outcome EnrichmentOutcome) error {
	if s == nil || s.pool == nil || contentID == "" {
		return nil
	}
	if claimToken != "" {
		tag, err := s.pool.Exec(ctx, `
			UPDATE audiobook_enrichment_state
			SET attempts         = attempts + 1,
			    outcome          = $3,
			    last_error_class = NULL,
			    last_error       = NULL,
			    next_attempt_at  = NULL,
			    last_attempt_at  = now(),
			    completed_at     = now(),
			    claim_token      = NULL,
			    lease_until      = NULL,
			    updated_at       = now()
			WHERE content_id = $1
			  AND claim_token = $2
			  AND lease_until > now()
		`, contentID, claimToken, string(outcome))
		if err != nil {
			return fmt.Errorf("recording claimed audiobook enrichment outcome: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrAudiobookEnrichmentClaimLost
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audiobook_enrichment_state (
			content_id, attempts, outcome, last_error_class, last_error,
			next_attempt_at, last_attempt_at, completed_at, claim_token,
			lease_until, updated_at
		) VALUES ($1, 1, $2, NULL, NULL, NULL, now(), now(), NULL, NULL, now())
		ON CONFLICT (content_id) DO UPDATE SET
			attempts         = audiobook_enrichment_state.attempts + 1,
			outcome          = EXCLUDED.outcome,
			last_error_class = NULL,
			last_error       = NULL,
			next_attempt_at  = NULL,
			last_attempt_at  = now(),
			completed_at     = now(),
			claim_token      = NULL,
			lease_until      = NULL,
			updated_at       = now()
	`, contentID, string(outcome))
	if err != nil {
		return fmt.Errorf("recording audiobook enrichment outcome: %w", err)
	}
	return nil
}

// RecordFailure stamps a failed attempt and parks the item for a retry sized to
// how it failed. It deliberately does not set outcome: the item has not reached
// a terminal state, and conflating the two is what made the ebook backlog
// unreadable.
func (s *enrichmentStateStore) RecordFailure(
	ctx context.Context,
	contentID, claimToken string,
	class EnrichmentErrorClass,
	cause string,
) error {
	if s == nil || s.pool == nil || contentID == "" {
		return nil
	}
	// Truncate on a rune boundary: a provider stack trace in a status column
	// helps nobody, and a byte-index slice can split a UTF-8 sequence --
	// Postgres rejects invalid UTF-8, which would silently fail the whole
	// failure/backoff write for that call.
	const maxCause = 500
	if len(cause) > maxCause {
		cause = strings.ToValidUTF8(cause[:maxCause], "")
	}

	// One statement, deliberately. Recording the failure and parking the retry
	// used to be two round trips, and anything landing between them -- a
	// canceled context, a dropped connection, a concurrent RecordOutcome on
	// the same row -- left the item with an incremented attempts count but no
	// backoff at all, immediately re-claimable against the very provider that
	// just failed. The parked interval is min(step * attempts, cap), computed
	// on the post-increment attempts value inside the upsert.
	step, ceiling := backoffParams(class)
	if claimToken != "" {
		tag, err := s.pool.Exec(ctx, `
			UPDATE audiobook_enrichment_state
			SET attempts         = attempts + 1,
			    outcome          = NULL,
			    last_error_class = $3,
			    last_error       = $4,
			    next_attempt_at  = now() + make_interval(secs => LEAST(
			        $5::double precision * (attempts + 1),
			        $6::double precision
			    )),
			    last_attempt_at  = now(),
			    completed_at     = NULL,
			    claim_token      = NULL,
			    lease_until      = NULL,
			    updated_at       = now()
			WHERE content_id = $1
			  AND claim_token = $2
			  AND lease_until > now()
		`, contentID, claimToken, string(class), cause, step.Seconds(), ceiling.Seconds())
		if err != nil {
			return fmt.Errorf("recording claimed audiobook enrichment failure: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrAudiobookEnrichmentClaimLost
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audiobook_enrichment_state (
			content_id, attempts, last_error_class, last_error,
			next_attempt_at, last_attempt_at, updated_at
		) VALUES (
			$1, 1, $2, $3,
			now() + make_interval(secs => LEAST($4::double precision, $5::double precision)),
			now(), now()
		)
		ON CONFLICT (content_id) DO UPDATE SET
			attempts         = audiobook_enrichment_state.attempts + 1,
			outcome          = NULL,
			last_error_class = EXCLUDED.last_error_class,
			last_error       = EXCLUDED.last_error,
			next_attempt_at  = now() + make_interval(secs => LEAST(
				$4::double precision * (audiobook_enrichment_state.attempts + 1),
				$5::double precision
			)),
			last_attempt_at  = now(),
			completed_at     = NULL,
			claim_token      = NULL,
			lease_until      = NULL,
			updated_at       = now()
	`, contentID, string(class), cause, step.Seconds(), ceiling.Seconds())
	if err != nil {
		return fmt.Errorf("recording audiobook enrichment failure: %w", err)
	}
	return nil
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
// provider IDs, scalar metadata, search event, and terminal media timestamp.
func (s *enrichmentStateStore) RecordOutcomeTx(
	ctx context.Context,
	tx pgx.Tx,
	contentID, claimToken string,
	outcome EnrichmentOutcome,
) error {
	if s == nil || tx == nil || claimToken == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE audiobook_enrichment_state
		SET attempts         = attempts + 1,
		    outcome          = $3,
		    last_error_class = NULL,
		    last_error       = NULL,
		    next_attempt_at  = NULL,
		    last_attempt_at  = now(),
		    completed_at     = now(),
		    claim_token      = NULL,
		    lease_until      = NULL,
		    updated_at       = now()
		WHERE content_id = $1
		  AND claim_token = $2
	`, contentID, claimToken, string(outcome))
	if err != nil {
		return fmt.Errorf("completing audiobook enrichment claim: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAudiobookEnrichmentClaimLost
	}
	return nil
}
