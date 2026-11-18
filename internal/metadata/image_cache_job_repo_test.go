package metadata

import (
	"testing"
	"time"
)

func TestImageCacheFailureRetryDelayDefersStableProviderFailures(t *testing.T) {
	tests := []string{
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 403",
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 404",
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 410",
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 418",
	}
	for _, errText := range tests {
		if got := imageCacheFailureRetryDelay(1, errText); got != 7*24*time.Hour {
			t.Fatalf("imageCacheFailureRetryDelay(%q) = %s, want 7d", errText, got)
		}
	}
	if got := imageCacheFailureRetryDelay(1, "temporary network error"); got != time.Minute {
		t.Fatalf("transient failure delay = %s, want 1m", got)
	}
}

func TestClassifyImageCacheFailureRetriesEmptyResolverURL(t *testing.T) {
	// The resolver also returns an empty URL while a plugin is disabled,
	// upgrading, or still loading, so this must never tombstone artwork on the
	// first attempt.
	got := classifyImageCacheFailure(0, imageCacheEmptyResolvedURLError)
	if got.status != ImageCacheStatusQueued {
		t.Fatalf("status = %q, want %q", got.status, ImageCacheStatusQueued)
	}
	if got.attempt != 1 {
		t.Fatalf("attempt = %d, want 1", got.attempt)
	}
	if got.retryDelay != time.Minute {
		t.Fatalf("retry delay = %s, want 1m", got.retryDelay)
	}
}

func TestClassifyImageCacheFailureParksExhaustedTransientErrorRecoverably(t *testing.T) {
	got := classifyImageCacheFailure(imageCacheMaxAttempts-1, "temporary network error")
	if got.status != ImageCacheStatusFailed {
		t.Fatalf("status = %q, want %q", got.status, ImageCacheStatusFailed)
	}
	if got.attempt != imageCacheMaxAttempts {
		t.Fatalf("attempt = %d, want %d", got.attempt, imageCacheMaxAttempts)
	}
	if got.retryDelay != imageCacheFailedCooldown {
		t.Fatalf("retry delay = %s, want the recoverable cooldown %s", got.retryDelay, imageCacheFailedCooldown)
	}
	if got.retryDelay >= imageCachePermanentPark {
		t.Fatal("an outage must not park a job past the recovery window")
	}
}

func TestClassifyImageCacheFailureTombstonesExhaustedStableFailure(t *testing.T) {
	got := classifyImageCacheFailure(
		imageCacheMaxAttempts-1,
		"imagecache: download https://example.invalid/missing.jpg: unexpected status 404",
	)
	if got.status != ImageCacheStatusFailed {
		t.Fatalf("status = %q, want %q", got.status, ImageCacheStatusFailed)
	}
	if got.retryDelay != imageCachePermanentPark {
		t.Fatalf("retry delay = %s, want the permanent park %s", got.retryDelay, imageCachePermanentPark)
	}
}
