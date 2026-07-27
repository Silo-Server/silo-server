package audiobooks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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

// retryAfterFor returns how long to park an item given how it failed and how
// many times it has already been tried.
//
// Rate limiting backs off hardest and fastest: it is a statement about the
// provider, not the item, and retrying into a closed window is what turns a
// throttle into a backlog. Permanent failures are parked far out rather than
// never, because "permanent" is a classification and classifications are wrong
// sometimes.
func retryAfterFor(class EnrichmentErrorClass, attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	switch class {
	case EnrichmentErrorRateLimited:
		d := time.Duration(attempts) * time.Hour
		if d > 24*time.Hour {
			d = 24 * time.Hour
		}
		return d
	case EnrichmentErrorPermanent:
		return 30 * 24 * time.Hour
	default: // transient
		d := time.Duration(attempts) * 15 * time.Minute
		if d > 6*time.Hour {
			d = 6 * time.Hour
		}
		return d
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

// RecordOutcome stamps a terminal result and clears any parked retry.
func (s *enrichmentStateStore) RecordOutcome(ctx context.Context, contentID string, outcome EnrichmentOutcome) error {
	if s == nil || s.pool == nil || contentID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audiobook_enrichment_state (
			content_id, attempts, outcome, last_error_class, last_error,
			next_attempt_at, last_attempt_at, completed_at, updated_at
		) VALUES ($1, 1, $2, NULL, NULL, NULL, now(), now(), now())
		ON CONFLICT (content_id) DO UPDATE SET
			attempts         = audiobook_enrichment_state.attempts + 1,
			outcome          = EXCLUDED.outcome,
			last_error_class = NULL,
			last_error       = NULL,
			next_attempt_at  = NULL,
			last_attempt_at  = now(),
			completed_at     = now(),
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
	contentID string,
	class EnrichmentErrorClass,
	cause string,
) error {
	if s == nil || s.pool == nil || contentID == "" {
		return nil
	}
	// Truncate: a provider stack trace in a status column helps nobody and
	// bloats every row that reads it.
	const maxCause = 500
	if len(cause) > maxCause {
		cause = cause[:maxCause]
	}

	var attempts int
	err := s.pool.QueryRow(ctx, `
		INSERT INTO audiobook_enrichment_state (
			content_id, attempts, last_error_class, last_error,
			last_attempt_at, updated_at
		) VALUES ($1, 1, $2, $3, now(), now())
		ON CONFLICT (content_id) DO UPDATE SET
			attempts         = audiobook_enrichment_state.attempts + 1,
			last_error_class = EXCLUDED.last_error_class,
			last_error       = EXCLUDED.last_error,
			last_attempt_at  = now(),
			updated_at       = now()
		RETURNING attempts
	`, contentID, string(class), cause).Scan(&attempts)
	if err != nil {
		return fmt.Errorf("recording audiobook enrichment failure: %w", err)
	}

	if _, err := s.pool.Exec(ctx, `
		UPDATE audiobook_enrichment_state
		   SET next_attempt_at = now() + $2::interval,
		       updated_at      = now()
		 WHERE content_id = $1
	`, contentID, retryAfterFor(class, attempts).String()); err != nil {
		return fmt.Errorf("parking audiobook enrichment retry: %w", err)
	}
	return nil
}
