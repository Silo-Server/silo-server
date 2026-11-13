package ebooks

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

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
