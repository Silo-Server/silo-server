package s3client

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"
)

const windowTestKey = "tmdb/movie/1/poster/w500.0123abcd.webp"

func windowTestConfig() BucketConfig {
	return BucketConfig{Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "silo", AccessKey: "k", SecretKey: "s", PathStyle: true}
}

// presignedValidity reads the signing time and lifetime a SigV4 URL carries.
func presignedValidity(t *testing.T, raw string) (time.Time, time.Duration) {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := time.Parse("20060102T150405Z", parsed.Query().Get("X-Amz-Date"))
	if err != nil {
		t.Fatal(err)
	}
	seconds, err := strconv.Atoi(parsed.Query().Get("X-Amz-Expires"))
	if err != nil {
		t.Fatal(err)
	}
	return signed, time.Duration(seconds) * time.Second
}

// Clients and CDNs cache images by full URL. Over a simulated day of
// per-minute resolves, a day-long window keeps at most two presigned URLs (one
// UTC midnight rollover), a second replica mints the same URL, and every URL
// stays valid for at least the requested expiry.
func TestPresignGetURLAtHoldsPresignedURLForTheWindow(t *testing.T) {
	ctx := context.Background()
	a, b := NewClient(windowTestConfig()), NewClient(windowTestConfig())
	const expiry, window = 4 * time.Hour, 24 * time.Hour
	start := time.Date(2026, 1, 2, 3, 7, 0, 0, time.UTC)
	distinct := map[string]bool{}
	for i := range 24 * 60 {
		now := start.Add(time.Duration(i) * time.Minute)
		u, expires, err := a.PresignGetURLAt(ctx, "silo", windowTestKey, now, expiry, window)
		if err != nil {
			t.Fatal(err)
		}
		replica, _, err := b.PresignGetURLAt(ctx, "silo", windowTestKey, now.Add(20*time.Second), expiry, window)
		if err != nil {
			t.Fatal(err)
		}
		if replica != u {
			t.Fatalf("replicas disagree at %s:\n%s\n%s", now, u, replica)
		}
		signed, lifetime := presignedValidity(t, u)
		if !signed.Add(lifetime).Equal(expires) {
			t.Fatalf("reported expiry %s, URL expires %s", expires, signed.Add(lifetime))
		}
		if remaining := expires.Sub(now); remaining < expiry {
			t.Fatalf("remaining lifetime %s at %s", remaining, now)
		}
		distinct[u] = true
	}
	t.Logf("distinct presigned URLs over a day of per-minute resolves: %d", len(distinct))
	if len(distinct) > 2 {
		t.Fatalf("presigned URL took %d values in a day, want at most 2", len(distinct))
	}
}
