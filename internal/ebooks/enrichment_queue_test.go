package ebooks

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestEnrichmentQueueClaimQueryUsesAtomicLeasedClaims(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		lanePredicate  string
		statePredicate string
		rejected       string
	}{
		{
			name:           "incremental",
			query:          claimIncrementalEnrichmentJobsQuery,
			lanePredicate:  "priority >= 0",
			statePredicate: "state.priority >= 0",
			rejected:       "priority < 0",
		},
		{
			name:           "legacy",
			query:          claimLegacyEnrichmentJobsQuery,
			lanePredicate:  "priority < 0",
			statePredicate: "state.priority < 0",
			rejected:       "priority >= 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := strings.Join(strings.Fields(tt.query), " ")
			for _, fragment := range []string{
				"WITH high_priority_candidates AS MATERIALIZED",
				"oldest_due_candidates AS MATERIALIZED",
				"candidate_pool AS",
				"UNION",
				"ranked_candidates AS",
				"FROM ranked_candidates ranked",
				"JOIN ebook_enrichment_state state",
				"FOR UPDATE OF state SKIP LOCKED",
				"status = 'pending' OR (status = 'running' AND lease_until < now())",
				"next_attempt_at <= now()",
				tt.lanePredicate,
				tt.statePredicate,
				"ORDER BY priority DESC, next_attempt_at, updated_at",
				"ORDER BY next_attempt_at, updated_at, priority DESC",
				"LIMIT $3",
				"priority + FLOOR(EXTRACT(EPOCH FROM (now() - next_attempt_at)) / 3600)::integer",
				"SET status = 'running'",
				"lease_until = now() + $2::interval",
				"claim_token = gen_random_uuid()::text",
				"attempts = attempts + 1",
				"RETURNING state.content_id, state.claim_token, state.attempts",
				"state.protected_fields",
			} {
				if !strings.Contains(query, fragment) {
					t.Fatalf("claim query missing %q:\n%s", fragment, tt.query)
				}
			}
			if strings.Contains(query, tt.rejected) {
				t.Fatalf("claim query crosses lanes with %q:\n%s", tt.rejected, tt.query)
			}
			if strings.Contains(query, "$3 = 'legacy'") {
				t.Fatalf("claim query uses a parameterized lane predicate:\n%s", tt.query)
			}
			if got := strings.Count(query, "LIMIT $3"); got != 2 {
				t.Fatalf("claim query has %d bounded candidate windows, want 2:\n%s", got, tt.query)
			}
			if got := strings.Count(query, "FLOOR(EXTRACT(EPOCH"); got != 1 {
				t.Fatalf("aging expression count = %d, want exactly one bounded computation:\n%s", got, tt.query)
			}
			agingAt := strings.Index(query, "FLOOR(EXTRACT(EPOCH")
			poolAt := strings.Index(query, "candidate_pool AS")
			if agingAt < poolAt {
				t.Fatalf("aging is computed before the bounded candidate pool:\n%s", tt.query)
			}
		})
	}
	if claimCandidateWindow != maxEnrichWorkers {
		t.Fatalf("claim candidate window = %d, want worker ceiling %d", claimCandidateWindow, maxEnrichWorkers)
	}
}

func TestEnrichmentQueueSelectsLiteralLaneQueries(t *testing.T) {
	if got := claimEnrichmentJobsQueryForScope(EnrichmentScopeIncremental); got != claimIncrementalEnrichmentJobsQuery {
		t.Fatal("incremental scope did not select incremental claim SQL")
	}
	if got := claimEnrichmentJobsQueryForScope(EnrichmentScopeLegacy); got != claimLegacyEnrichmentJobsQuery {
		t.Fatal("legacy scope did not select legacy claim SQL")
	}
	if got := countReadyEnrichmentJobsQueryForScope(EnrichmentScopeIncremental); got != countReadyIncrementalEnrichmentJobsQuery {
		t.Fatal("incremental scope did not select incremental count SQL")
	}
	if got := countReadyEnrichmentJobsQueryForScope(EnrichmentScopeLegacy); got != countReadyLegacyEnrichmentJobsQuery {
		t.Fatal("legacy scope did not select legacy count SQL")
	}
}

func TestEnrichmentQueueReadyCountUsesTheSameLiteralLaneAsClaims(t *testing.T) {
	for _, tt := range []struct {
		name      string
		query     string
		predicate string
	}{
		{name: "incremental", query: countReadyIncrementalEnrichmentJobsQuery, predicate: "priority >= 0"},
		{name: "legacy", query: countReadyLegacyEnrichmentJobsQuery, predicate: "priority < 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			query := strings.Join(strings.Fields(tt.query), " ")
			for _, fragment := range []string{
				"SELECT COUNT(*)",
				"next_attempt_at <= now()",
				"status = 'pending' OR (status = 'running' AND lease_until < now())",
				tt.predicate,
			} {
				if !strings.Contains(query, fragment) {
					t.Fatalf("ready-count query missing %q:\n%s", fragment, tt.query)
				}
			}
			if strings.Contains(query, "$1 = 'legacy'") {
				t.Fatalf("ready-count query uses a parameterized lane predicate:\n%s", tt.query)
			}
		})
	}
}

func TestEnrichmentQueueHasReadyUsesBoundedOrderedScalarQueries(t *testing.T) {
	for _, tt := range []struct {
		name      string
		query     string
		predicate string
	}{
		{name: "incremental", query: hasReadyIncrementalEnrichmentJobsQuery, predicate: "priority >= 0"},
		{name: "legacy", query: hasReadyLegacyEnrichmentJobsQuery, predicate: "priority < 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			query := strings.Join(strings.Fields(tt.query), " ")
			for _, fragment := range []string{
				"SELECT COALESCE((",
				"SELECT true",
				"next_attempt_at <= now()",
				"status = 'pending' OR (status = 'running' AND lease_until < now())",
				tt.predicate,
				"ORDER BY next_attempt_at, updated_at, priority DESC",
				"LIMIT 1",
				"), false)",
			} {
				if !strings.Contains(query, fragment) {
					t.Fatalf("has-ready query missing %q:\n%s", fragment, tt.query)
				}
			}
			if strings.Contains(query, "EXISTS") {
				t.Fatalf("has-ready query uses EXISTS, which lets PostgreSQL discard ordering:\n%s", tt.query)
			}
			if strings.Contains(query, "COUNT(") {
				t.Fatalf("has-ready query performs an exact count:\n%s", tt.query)
			}
		})
	}
}

func TestEnrichmentQueueReconcileMissingIsBoundedAndLaneSafe(t *testing.T) {
	recentQuery := strings.Join(strings.Fields(reconcileRecentMissingEnrichmentJobsQuery), " ")
	for _, fragment := range []string{
		"recent_memberships AS MATERIALIZED",
		"WHERE membership.media_folder_id = $1",
		"ORDER BY membership.first_seen_at DESC, membership.content_id DESC",
		"LIMIT $3",
		"FROM recent_memberships candidate",
		"LEFT JOIN ebook_enrichment_state state ON state.content_id = candidate.content_id",
		"state.content_id IS NULL",
		"(SELECT COUNT(*)::integer FROM inserted) AS reconciled",
		"(SELECT COUNT(*)::integer FROM recent_memberships) AS inspected",
	} {
		if !strings.Contains(recentQuery, fragment) {
			t.Fatalf("recent reconcile query missing %q:\n%s", fragment, reconcileRecentMissingEnrichmentJobsQuery)
		}
	}
	recentLimitAt := strings.Index(recentQuery, "LIMIT $3")
	recentItemJoinAt := strings.Index(recentQuery, "JOIN media_items mi")
	recentStateJoinAt := strings.Index(recentQuery, "LEFT JOIN ebook_enrichment_state state")
	if recentLimitAt < 0 || recentItemJoinAt < 0 || recentStateJoinAt < 0 ||
		recentLimitAt > recentItemJoinAt || recentLimitAt > recentStateJoinAt {
		t.Fatalf("recent reconciliation is not bounded before catalog joins:\n%s", reconcileRecentMissingEnrichmentJobsQuery)
	}

	query := strings.Join(strings.Fields(reconcileMissingEnrichmentJobsQuery), " ")
	for _, fragment := range []string{
		"membership_candidates AS MATERIALIZED",
		"WHERE membership.media_folder_id = $1",
		"$4::timestamptz IS NULL",
		"(membership.first_seen_at, membership.content_id) < ($4, $5)",
		"ORDER BY membership.first_seen_at DESC, membership.content_id DESC",
		"LIMIT $3",
		"FROM membership_candidates candidate",
		"mi.type = 'ebook'",
		"state.content_id IS NULL",
		"SELECT candidates.content_id, 'pending', $2",
		"ON CONFLICT (content_id) DO NOTHING",
		"COUNT(*)::integer AS inspected",
		"FROM membership_candidates",
		"(SELECT COUNT(*)::integer FROM inserted) AS reconciled",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("reconcile query missing %q:\n%s", fragment, reconcileMissingEnrichmentJobsQuery)
		}
	}
	windowAt := strings.Index(query, "membership_candidates AS MATERIALIZED")
	boundedQuery := query[windowAt:]
	limitAt := strings.Index(boundedQuery, "LIMIT $3")
	itemJoinAt := strings.Index(boundedQuery, "JOIN media_items mi")
	stateJoinAt := strings.Index(boundedQuery, "LEFT JOIN ebook_enrichment_state state")
	if windowAt < 0 || limitAt < 0 || itemJoinAt < 0 || stateJoinAt < 0 {
		t.Fatalf("reconcile query is missing bounded traversal structure:\n%s", reconcileMissingEnrichmentJobsQuery)
	}
	if limitAt > itemJoinAt || limitAt > stateJoinAt {
		t.Fatalf("reconcile query filters missing jobs before bounding raw membership traversal:\n%s", reconcileMissingEnrichmentJobsQuery)
	}
	statsAt := strings.Index(query, "COUNT(*)::integer AS inspected")
	insertedAt := strings.Index(query, "(SELECT COUNT(*)::integer FROM inserted) AS reconciled")
	if statsAt < 0 || insertedAt < 0 || statsAt > insertedAt {
		t.Fatalf("cursor advancement is not derived independently from the inspected membership window:\n%s", reconcileMissingEnrichmentJobsQuery)
	}

	ensureQuery := strings.Join(strings.Fields(ensureEnrichmentReconcileCursorQuery), " ")
	if !strings.Contains(ensureQuery, "ON CONFLICT (folder_id) DO NOTHING") {
		t.Fatalf("cursor ensure query is not idempotent: %s", ensureEnrichmentReconcileCursorQuery)
	}
	lockQuery := strings.Join(strings.Fields(lockEnrichmentReconcileCursorQuery), " ")
	for _, fragment := range []string{
		"SELECT after_first_seen_at, after_content_id",
		"WHERE folder_id = $1",
		"FOR UPDATE",
	} {
		if !strings.Contains(lockQuery, fragment) {
			t.Fatalf("cursor lock query missing %q: %s", fragment, lockEnrichmentReconcileCursorQuery)
		}
	}
	updateQuery := strings.Join(strings.Fields(updateEnrichmentReconcileCursorQuery), " ")
	for _, fragment := range []string{
		"UPDATE ebook_enrichment_reconcile_cursors",
		"SET after_first_seen_at = $2",
		"after_content_id = $3",
		"WHERE folder_id = $1",
	} {
		if !strings.Contains(updateQuery, fragment) {
			t.Fatalf("cursor update query missing %q: %s", fragment, updateEnrichmentReconcileCursorQuery)
		}
	}
}

func TestEnrichmentScopeValidation(t *testing.T) {
	for _, scope := range []EnrichmentScope{EnrichmentScopeIncremental, EnrichmentScopeLegacy} {
		if err := scope.validate(); err != nil {
			t.Fatalf("validate(%q) error = %v", scope, err)
		}
	}
	if err := EnrichmentScope("everything").validate(); err == nil {
		t.Fatal("unknown enrichment scope was accepted")
	}
}

func TestEnrichmentQueueCapturesEveryProviderWritablePendingField(t *testing.T) {
	query := strings.Join(strings.Fields(ebookProtectedFieldsSQL), " ")
	for _, field := range []string{
		"title",
		"year",
		"overview",
		"tagline",
		"content_rating",
		"runtime",
		"release_date",
		"genres",
		"studios",
		"authors",
		"poster_path",
		"backdrop_path",
		"logo_path",
	} {
		if !strings.Contains(query, "'"+field+"'") {
			t.Fatalf("protected-field capture missing %q:\n%s", field, ebookProtectedFieldsSQL)
		}
	}
}

func TestEnrichmentQueueChecksExactClaimTokenBeforePersistence(t *testing.T) {
	query := strings.Join(strings.Fields(checkEnrichmentClaimQuery), " ")
	for _, fragment := range []string{
		"SELECT EXISTS",
		"content_id = $1",
		"status = 'running'",
		"claim_token = $2",
		"lease_until > now()",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("claim ownership query missing %q:\n%s", fragment, checkEnrichmentClaimQuery)
		}
	}
}

func TestEnrichmentQueueEnqueueMakesPendingRowsDueAndDefersRunningRequeue(t *testing.T) {
	query := strings.Join(strings.Fields(enqueueEnrichmentJobQuery), " ")
	for _, fragment := range []string{
		"INSERT INTO ebook_enrichment_state",
		"FROM media_items mi",
		"ON CONFLICT (content_id) DO UPDATE SET",
		"WHEN ebook_enrichment_state.status = 'running' THEN ebook_enrichment_state.next_attempt_at",
		"ELSE now()",
		"WHEN ebook_enrichment_state.priority < 0 THEN ebook_enrichment_state.priority",
		"requeue_requested = ebook_enrichment_state.requeue_requested OR ebook_enrichment_state.status = 'running'",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("enqueue query missing %q:\n%s", fragment, enqueueEnrichmentJobQuery)
		}
	}
	if strings.Contains(query, "lease_until =") || strings.Contains(query, "claim_token =") {
		t.Fatalf("enqueue must not mutate an active lease:\n%s", enqueueEnrichmentJobQuery)
	}
}

func TestEnrichmentQueueKeepsLegacyLaneRowsUntilTerminalOutcome(t *testing.T) {
	enqueue := strings.Join(strings.Fields(enqueueEnrichmentJobQuery), " ")
	if !strings.Contains(enqueue, "WHEN ebook_enrichment_state.priority < 0 THEN ebook_enrichment_state.priority") {
		t.Fatalf("enqueue must keep legacy-lane rows in the legacy lane:\n%s", enqueueEnrichmentJobQuery)
	}
	for _, tt := range []struct {
		name  string
		query string
	}{
		{name: "fail", query: failEnrichmentJobQuery},
		{name: "release", query: releaseEnrichmentJobQuery},
	} {
		query := strings.Join(strings.Fields(tt.query), " ")
		if strings.Contains(query, "WHEN requeue_requested THEN 100") {
			t.Fatalf("%s requeue must not promote legacy-lane rows without a terminal outcome:\n%s", tt.name, tt.query)
		}
		if !strings.Contains(query, "WHEN requeue_requested AND priority >= 0 THEN 100 ELSE priority END") {
			t.Fatalf("%s requeue must stay within the row's lane:\n%s", tt.name, tt.query)
		}
	}
}

func TestEnrichmentQueueMigrationIsCrashSafeAndSeedsAllLegacyEbooks(t *testing.T) {
	body, err := os.ReadFile("../../migrations/sql/20260719090000_ebook_enrichment_jobs.sql")
	if err != nil {
		t.Fatalf("read enrichment queue migration: %v", err)
	}
	migration := strings.Join(strings.Fields(string(body)), " ")

	for _, fragment := range []string{
		"-- +goose NO TRANSACTION",
		"ADD COLUMN IF NOT EXISTS claim_token text",
		"ADD COLUMN IF NOT EXISTS requeue_requested boolean NOT NULL DEFAULT false",
		"ADD COLUMN IF NOT EXISTS protected_fields text[] NOT NULL DEFAULT '{}'::text[]",
		"INSERT INTO ebook_enrichment_state",
		"CASE WHEN mi.last_refreshed IS NULL THEN -100 ELSE 0 END",
		"GREATEST(mi.last_refreshed + interval '90 days', now())",
		"mi.type = 'ebook'",
		"NOT EXISTS",
		"manga_chapters",
		"ON CONFLICT (content_id) DO NOTHING",
		"NOT i.indisvalid",
		"DROP INDEX public.ebook_enrichment_incremental_due_idx",
		"DROP INDEX public.ebook_enrichment_incremental_priority_idx",
		"DROP INDEX public.ebook_enrichment_legacy_due_idx",
		"DROP INDEX public.ebook_enrichment_legacy_priority_idx",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS ebook_enrichment_incremental_due_idx",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS ebook_enrichment_incremental_priority_idx",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS ebook_enrichment_legacy_due_idx",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS ebook_enrichment_legacy_priority_idx",
		"WHERE status IN ('pending', 'running') AND priority >= 0",
		"WHERE status IN ('pending', 'running') AND priority < 0",
		"DROP INDEX CONCURRENTLY IF EXISTS ebook_enrichment_incremental_due_idx",
		"DROP INDEX CONCURRENTLY IF EXISTS ebook_enrichment_incremental_priority_idx",
		"DROP INDEX CONCURRENTLY IF EXISTS ebook_enrichment_legacy_due_idx",
		"DROP INDEX CONCURRENTLY IF EXISTS ebook_enrichment_legacy_priority_idx",
	} {
		if !strings.Contains(migration, fragment) {
			t.Fatalf("legacy backlog migration missing %q:\n%s", fragment, body)
		}
	}
	if strings.Contains(migration, "AND mi.last_refreshed IS NULL") {
		t.Fatalf("migration must seed refreshed ebooks too:\n%s", body)
	}
}

func TestEnrichmentQueueMigrationDownPreservesCurrentFailureHistory(t *testing.T) {
	body, err := os.ReadFile("../../migrations/sql/20260719090000_ebook_enrichment_jobs.sql")
	if err != nil {
		t.Fatalf("read enrichment queue migration: %v", err)
	}
	parts := strings.SplitN(strings.Join(strings.Fields(string(body)), " "), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("migration missing Down section")
	}
	down := parts[1]
	for _, fragment := range []string{
		"SET failures = attempts",
		"WHERE outcome = 'failed'",
		"DELETE FROM ebook_enrichment_state WHERE outcome IN ('success', 'no_match')",
		"IF EXISTS",
	} {
		if !strings.Contains(down, fragment) {
			t.Fatalf("down migration missing %q:\n%s", fragment, body)
		}
	}
	if strings.Contains(down, "WHERE failures = 0") {
		t.Fatalf("down migration must not erase post-migration failures based on the stale legacy counter:\n%s", body)
	}
}

func TestEnrichmentQueueTransitionsKeepDurableRowsAndReleaseLeases(t *testing.T) {
	complete := strings.Join(strings.Fields(completeEnrichmentJobQuery), " ")
	for _, fragment := range []string{
		"UPDATE ebook_enrichment_state",
		"status = 'pending'",
		"lease_until = NULL",
		"completed_at = now()",
		"ELSE now() + $3::interval",
		"outcome = $2",
		"attempts = 0",
		"WHEN requeue_requested THEN 100 ELSE 0 END",
		"requeue_requested = false",
		"AND claim_token = $4",
	} {
		if !strings.Contains(complete, fragment) {
			t.Fatalf("complete query missing %q:\n%s", fragment, completeEnrichmentJobQuery)
		}
	}
	if strings.Contains(strings.ToUpper(complete), "DELETE") {
		t.Fatalf("completion must retain durable queue state:\n%s", completeEnrichmentJobQuery)
	}

	release := strings.Join(strings.Fields(releaseEnrichmentJobQuery), " ")
	for _, fragment := range []string{
		"status = 'pending'",
		"lease_until = NULL",
		"attempts = GREATEST(attempts - 1, 0)",
		"WHERE content_id = $1",
		"AND status = 'running'",
		"AND claim_token = $2",
	} {
		if !strings.Contains(release, fragment) {
			t.Fatalf("release query missing %q:\n%s", fragment, releaseEnrichmentJobQuery)
		}
	}

	failure := strings.Join(strings.Fields(failEnrichmentJobQuery), " ")
	for _, fragment := range []string{
		"WHEN requeue_requested THEN now()",
		"WHEN requeue_requested AND priority >= 0 THEN 100 ELSE priority END",
		"AND claim_token = $5",
	} {
		if !strings.Contains(failure, fragment) {
			t.Fatalf("failure transition missing %q:\n%s", fragment, failEnrichmentJobQuery)
		}
	}

	discard := strings.Join(strings.Fields(discardEnrichmentJobQuery), " ")
	for _, fragment := range []string{
		"status = 'discarded'",
		"lease_until = NULL",
		"claim_token = NULL",
		"outcome = 'discarded'",
		"WHERE content_id = $1",
		"AND claim_token = $2",
	} {
		if !strings.Contains(discard, fragment) {
			t.Fatalf("discard transition missing %q:\n%s", fragment, discardEnrichmentJobQuery)
		}
	}
}

func TestEnrichmentRetryPolicy(t *testing.T) {
	t.Run("outcome refresh horizons", func(t *testing.T) {
		if got := enrichmentRefreshHorizon(EnrichmentOutcomeSuccess); got != 90*24*time.Hour {
			t.Fatalf("success refresh horizon = %s, want 90 days", got)
		}
		if got := enrichmentRefreshHorizon(EnrichmentOutcomeNoMatch); got != 30*24*time.Hour {
			t.Fatalf("no-match refresh horizon = %s, want 30 days", got)
		}
	})

	t.Run("transient failures use capped exponential backoff", func(t *testing.T) {
		if got := enrichmentRetryDelay(EnrichmentErrorTransient, 1, 0); got != 5*time.Minute {
			t.Fatalf("first transient retry = %s, want 5m", got)
		}
		if got := enrichmentRetryDelay(EnrichmentErrorTransient, 2, 0); got != 10*time.Minute {
			t.Fatalf("second transient retry = %s, want 10m", got)
		}
		if got := enrichmentRetryDelay(EnrichmentErrorTransient, 20, 0); got != 24*time.Hour {
			t.Fatalf("capped transient retry = %s, want 24h", got)
		}
	})

	t.Run("rate limits honor provider horizon with a 24 hour cap", func(t *testing.T) {
		if got := enrichmentRetryDelay(EnrichmentErrorRateLimited, 1, 45*time.Minute); got != 45*time.Minute {
			t.Fatalf("rate-limited retry = %s, want 45m", got)
		}
		if got := enrichmentRetryDelay(EnrichmentErrorRateLimited, 1, 72*time.Hour); got != 24*time.Hour {
			t.Fatalf("capped rate-limited retry = %s, want 24h", got)
		}
	})

	t.Run("rate limits never requeue below the cooldown floor", func(t *testing.T) {
		// A provider's 1s RetryInfo is request-pacing advice, not a queue
		// horizon; adopting it verbatim re-claims the same saturated tail.
		if got := enrichmentRetryDelay(EnrichmentErrorRateLimited, 1, time.Second); got != 15*time.Minute {
			t.Fatalf("short-hint rate-limited retry = %s, want 15m floor", got)
		}
		if got := enrichmentRetryDelay(EnrichmentErrorRateLimited, 1, 0); got != 15*time.Minute {
			t.Fatalf("no-hint rate-limited retry = %s, want 15m floor", got)
		}
		if got := enrichmentRetryDelay(EnrichmentErrorRateLimited, 20, 0); got != 24*time.Hour {
			t.Fatalf("no-hint high-attempt rate-limited retry = %s, want capped backoff", got)
		}
	})

	t.Run("cooldown floor is env tunable with a tolerant fallback", func(t *testing.T) {
		t.Setenv("SILO_EBOOK_RATE_LIMIT_COOLDOWN", "30m")
		if got := enrichmentRetryDelay(EnrichmentErrorRateLimited, 1, time.Second); got != 30*time.Minute {
			t.Fatalf("tuned floor retry = %s, want 30m", got)
		}
		if got := enrichmentRetryDelay(EnrichmentErrorRateLimited, 1, 45*time.Minute); got != 45*time.Minute {
			t.Fatalf("hint above tuned floor = %s, want 45m", got)
		}
		t.Setenv("SILO_EBOOK_RATE_LIMIT_COOLDOWN", "banana")
		if got := enrichmentRetryDelay(EnrichmentErrorRateLimited, 1, time.Second); got != 15*time.Minute {
			t.Fatalf("invalid floor retry = %s, want 15m default", got)
		}
		t.Setenv("SILO_EBOOK_RATE_LIMIT_COOLDOWN", "-5m")
		if got := enrichmentRetryDelay(EnrichmentErrorRateLimited, 1, time.Second); got != 15*time.Minute {
			t.Fatalf("non-positive floor retry = %s, want 15m default", got)
		}
	})

	t.Run("permanent failures refresh after 30 days", func(t *testing.T) {
		if got := enrichmentRetryDelay(EnrichmentErrorPermanent, 1, 0); got != 30*24*time.Hour {
			t.Fatalf("permanent retry = %s, want 30 days", got)
		}
	})
}

func TestEnrichmentRetryPolicyClassifiesProviderErrors(t *testing.T) {
	limited, err := status.New(codes.ResourceExhausted, "provider quota exhausted").WithDetails(
		&errdetails.RetryInfo{RetryDelay: durationpb.New(45 * time.Minute)},
	)
	if err != nil {
		t.Fatalf("attach retry info: %v", err)
	}
	providerErrors := errors.Join(fmt.Errorf("openlibrary search: %w", limited.Err()))
	errorClass, retryAfter := classifyEnrichmentError(fmt.Errorf("no metadata obtained: %w", providerErrors))
	if errorClass != EnrichmentErrorRateLimited || retryAfter != 45*time.Minute {
		t.Fatalf("rate-limit classification = (%q, %s), want (rate_limited, 45m)", errorClass, retryAfter)
	}

	for _, code := range []codes.Code{
		codes.InvalidArgument,
		codes.NotFound,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.FailedPrecondition,
		codes.Unimplemented,
	} {
		errorClass, retryAfter = classifyEnrichmentError(status.Error(code, "deterministic provider failure"))
		if errorClass != EnrichmentErrorPermanent || retryAfter != 0 {
			t.Fatalf("%s classification = (%q, %s), want (permanent, 0)", code, errorClass, retryAfter)
		}
	}

	errorClass, retryAfter = classifyEnrichmentError(status.Error(codes.Unavailable, "provider down"))
	if errorClass != EnrichmentErrorTransient || retryAfter != 0 {
		t.Fatalf("unavailable classification = (%q, %s), want (transient, 0)", errorClass, retryAfter)
	}
}
