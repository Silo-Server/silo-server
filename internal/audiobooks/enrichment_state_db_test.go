package audiobooks

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A failed attempt must be recorded WITHOUT a terminal outcome and with a
// retry parked. Conflating "failed once" with "no match" is what made the ebook
// backlog unreadable: 90,721 rows carrying outcome='no_match', attempts=0 and
// no error class, so a rate-limited afternoon looked identical to 90,721
// genuine misses.
func TestRecordFailureParksARetryWithoutStampingAnOutcome(t *testing.T) {
	pool := newClaimTestPool(t)
	store := newEnrichmentStateStore(pool)
	ctx := context.Background()

	contentID := seedAudiobook(t, pool, "failure", "/covers/embedded.jpg", false)

	if err := store.RecordFailure(ctx, contentID, EnrichmentErrorRateLimited, "429 too many requests"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	var (
		attempts    int
		outcome     *string
		class       *string
		nextAt      *time.Time
		lastAttempt *time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT attempts, outcome, last_error_class, next_attempt_at, last_attempt_at
		  FROM audiobook_enrichment_state WHERE content_id = $1
	`, contentID).Scan(&attempts, &outcome, &class, &nextAt, &lastAttempt); err != nil {
		t.Fatalf("read state: %v", err)
	}

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if outcome != nil {
		t.Errorf("outcome = %q, want NULL: a failure is not a terminal outcome", *outcome)
	}
	if class == nil || *class != string(EnrichmentErrorRateLimited) {
		t.Errorf("last_error_class = %v, want rate_limited", class)
	}
	if nextAt == nil || !nextAt.After(time.Now()) {
		t.Errorf("next_attempt_at = %v, want a future retry", nextAt)
	}
	if lastAttempt == nil {
		t.Error("last_attempt_at was not stamped")
	}
}

// Attempts accumulate across failures, and the backoff widens with them. The
// ebook table leaves attempts at 0 even on terminal rows, which is precisely
// why "never tried" and "tried and gave up" could not be told apart.
func TestRepeatedFailuresAccumulateAttemptsAndWidenBackoff(t *testing.T) {
	pool := newClaimTestPool(t)
	store := newEnrichmentStateStore(pool)
	ctx := context.Background()

	contentID := seedAudiobook(t, pool, "backoff", "", false)

	readState := func() (int, time.Time) {
		t.Helper()
		var attempts int
		var nextAt time.Time
		if err := pool.QueryRow(ctx, `
			SELECT attempts, next_attempt_at FROM audiobook_enrichment_state WHERE content_id = $1
		`, contentID).Scan(&attempts, &nextAt); err != nil {
			t.Fatalf("read state: %v", err)
		}
		return attempts, nextAt
	}

	if err := store.RecordFailure(ctx, contentID, EnrichmentErrorTransient, "boom"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	attempts1, next1 := readState()

	if err := store.RecordFailure(ctx, contentID, EnrichmentErrorTransient, "boom again"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	attempts2, next2 := readState()

	if attempts1 != 1 || attempts2 != 2 {
		t.Errorf("attempts = %d then %d, want 1 then 2", attempts1, attempts2)
	}
	if !next2.After(next1) {
		t.Errorf("backoff did not widen: %v then %v", next1, next2)
	}
}

// A terminal outcome must clear the parked retry, otherwise a successfully
// enriched item stays flagged as owing another attempt forever.
func TestRecordOutcomeClearsTheParkedRetry(t *testing.T) {
	pool := newClaimTestPool(t)
	store := newEnrichmentStateStore(pool)
	ctx := context.Background()

	contentID := seedAudiobook(t, pool, "outcome", "", false)

	if err := store.RecordFailure(ctx, contentID, EnrichmentErrorTransient, "temporary"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if err := store.RecordOutcome(ctx, contentID, EnrichmentOutcomeSuccess); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}

	var (
		attempts int
		outcome  string
		class    *string
		nextAt   *time.Time
		done     *time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT attempts, outcome, last_error_class, next_attempt_at, completed_at
		  FROM audiobook_enrichment_state WHERE content_id = $1
	`, contentID).Scan(&attempts, &outcome, &class, &nextAt, &done); err != nil {
		t.Fatalf("read state: %v", err)
	}

	if outcome != string(EnrichmentOutcomeSuccess) {
		t.Errorf("outcome = %q, want success", outcome)
	}
	if nextAt != nil {
		t.Errorf("next_attempt_at = %v, want NULL once terminal", nextAt)
	}
	if class != nil {
		t.Errorf("last_error_class = %q, want cleared on success", *class)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (the failure plus the success)", attempts)
	}
	if done == nil {
		t.Error("completed_at was not stamped")
	}
}

// The sweep must not re-claim an item that is parked for a later retry --
// otherwise the backoff is decorative and a throttled provider keeps being
// hammered at full sweep rate.
func TestClaimBatchSkipsItemsParkedForALaterRetry(t *testing.T) {
	pool := newClaimTestPool(t)
	ctx := context.Background()
	e := newTestEnricher(pool)

	parked := seedAudiobook(t, pool, "parked", "/covers/embedded.jpg", false)
	ready := seedAudiobook(t, pool, "ready", "/covers/embedded.jpg", false)

	if err := e.state.RecordFailure(ctx, parked, EnrichmentErrorRateLimited, "429"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	got := claimedIDs(t, e)
	if got[parked] {
		t.Error("an item parked for a future retry was claimed anyway")
	}
	if !got[ready] {
		t.Error("an unparked item was not claimed")
	}

	// Once the park expires the item must come back: a transient failure is
	// not a terminal state.
	if _, err := pool.Exec(ctx, `
		UPDATE audiobook_enrichment_state SET next_attempt_at = now() - interval '1 minute'
		 WHERE content_id = $1
	`, parked); err != nil {
		t.Fatalf("expire park: %v", err)
	}
	if !claimedIDs(t, e)[parked] {
		t.Error("an item whose retry became due was not re-claimed")
	}
}

// HasPendingItems and claimBatch must agree about parked items too, or the
// scheduler wakes for work the claim query will refuse to hand over.
func TestHasPendingItemsRespectsParkedRetries(t *testing.T) {
	pool := newClaimTestPool(t)
	ctx := context.Background()
	e := newTestEnricher(pool)

	only := seedAudiobook(t, pool, "onlyparked", "/covers/embedded.jpg", false)
	if err := e.state.RecordFailure(ctx, only, EnrichmentErrorPermanent, "403 forbidden"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	if claimedIDs(t, e)[only] {
		t.Fatal("parked item was claimable; the rest of this test is meaningless")
	}
}

func TestClassifyProviderErrorSortsByHowItShouldRetry(t *testing.T) {
	cases := []struct {
		msg  string
		want EnrichmentErrorClass
	}{
		{"HTTP 429 too many requests", EnrichmentErrorRateLimited},
		{"daily quota exceeded", EnrichmentErrorRateLimited},
		{"rate limit reached", EnrichmentErrorRateLimited},
		{"HTTP 403: Forbidden", EnrichmentErrorPermanent},
		{"unauthorized: bad token", EnrichmentErrorPermanent},
		{"connection reset by peer", EnrichmentErrorTransient},
		{"context deadline exceeded", EnrichmentErrorTransient},
	}
	for _, tc := range cases {
		if got := classifyProviderError(errString(tc.msg)); got != tc.want {
			t.Errorf("classifyProviderError(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
	if got := classifyProviderError(nil); got != EnrichmentErrorTransient {
		t.Errorf("classifyProviderError(nil) = %q, want transient", got)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// Backoff shape: rate limiting must back off harder than a transient blip,
// because retrying into a closed window is what turns a throttle into a
// backlog.
func TestRetryBackoffOrdering(t *testing.T) {
	transient := retryAfterFor(EnrichmentErrorTransient, 1)
	limited := retryAfterFor(EnrichmentErrorRateLimited, 1)
	permanent := retryAfterFor(EnrichmentErrorPermanent, 1)

	if !(transient < limited && limited < permanent) {
		t.Errorf("backoff not ordered: transient=%v limited=%v permanent=%v", transient, limited, permanent)
	}
	if capped := retryAfterFor(EnrichmentErrorRateLimited, 1000); capped > 24*time.Hour {
		t.Errorf("rate-limited backoff = %v, want capped at 24h", capped)
	}
	if capped := retryAfterFor(EnrichmentErrorTransient, 1000); capped > 6*time.Hour {
		t.Errorf("transient backoff = %v, want capped at 6h", capped)
	}
}

// A long cause whose 500-byte boundary falls inside a multi-byte rune must not
// produce invalid UTF-8: Postgres rejects it, which would silently fail the
// whole failure/backoff write and leave the item with no backoff at all.
func TestRecordFailureTruncatesCauseOnARuneBoundary(t *testing.T) {
	pool := newClaimTestPool(t)
	store := newEnrichmentStateStore(pool)
	ctx := context.Background()

	contentID := seedAudiobook(t, pool, "utf8", "", false)

	// 499 ASCII bytes then a 3-byte rune: the byte-index cut lands mid-rune.
	cause := strings.Repeat("x", 499) + "日本語エラー"
	if err := store.RecordFailure(ctx, contentID, EnrichmentErrorTransient, cause); err != nil {
		t.Fatalf("RecordFailure with multi-byte cause: %v", err)
	}

	var stored string
	if err := pool.QueryRow(ctx,
		`SELECT last_error FROM audiobook_enrichment_state WHERE content_id = $1`,
		contentID).Scan(&stored); err != nil {
		t.Fatalf("read stored cause: %v", err)
	}
	if !utf8.ValidString(stored) {
		t.Error("stored cause is not valid UTF-8")
	}
	if len(stored) == 0 || len(stored) > 500 {
		t.Errorf("stored cause length = %d, want (0, 500]", len(stored))
	}
}
