package s3client

import (
	"context"
	"net/url"
	"strconv"
	"strings"
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

// SigV4 rejects X-Amz-Expires above seven days, so a long expiry shrinks the
// window instead of exceeding the limit or cutting the expiry.
func TestPresignGetURLAtFitsTheSigV4Limit(t *testing.T) {
	ctx := context.Background()
	c := NewClient(windowTestConfig())
	now := time.Date(2026, 1, 2, 15, 7, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		expiry time.Duration
		signed time.Time
	}{
		{"window shrinks to fit", maxPresignLifetime - 12*time.Hour, now.Truncate(12 * time.Hour)},
		{"no room for a window", maxPresignLifetime, now},
	} {
		u, _, err := c.PresignGetURLAt(ctx, "silo", windowTestKey, now, tc.expiry, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		signed, lifetime := presignedValidity(t, u)
		if !signed.Equal(tc.signed) || lifetime != maxPresignLifetime {
			t.Fatalf("%s: signed %s for %s, want %s for %s", tc.name, signed, lifetime, tc.signed, maxPresignLifetime)
		}
	}
}

// The WAF rule fixes a token's lifetime from its timestamp, so the window is a
// quarter of the token TTL: the URL still has three quarters of it left.
func TestPresignGetURLAtHoldsCloudflareTokenForAQuarterOfItsTTL(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		tokenTTL    time.Duration
		maxDistinct int
	}{
		{"default three-hour token", 3 * time.Hour, 33}, // 45-minute windows; the day starts mid-window
		{"four-day token", 96 * time.Hour, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := windowTestConfig()
			cfg.PublicEndpoint = "https://cdn.example.test"
			cfg.URLAuth = URLAuthCloudflareToken
			cfg.TokenSecret = "secret"
			cfg.TokenTTL = int(tc.tokenTTL / time.Second)
			a, b := NewClient(cfg), NewClient(cfg)
			start := time.Date(2026, 1, 2, 3, 7, 0, 0, time.UTC)
			distinct := map[string]bool{}
			for i := range 24 * 60 {
				now := start.Add(time.Duration(i) * time.Minute)
				u, expires, err := a.PresignGetURLAt(ctx, "silo", windowTestKey, now, tc.tokenTTL, 24*time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if replica, _, _ := b.PresignGetURLAt(ctx, "silo", windowTestKey, now.Add(20*time.Second), tc.tokenTTL, 24*time.Hour); replica != u {
					t.Fatalf("replicas disagree at %s:\n%s\n%s", now, u, replica)
				}
				parsed, err := url.Parse(u)
				if err != nil {
					t.Fatal(err)
				}
				ts, _, _ := strings.Cut(parsed.Query().Get("verify"), "-")
				issued, err := strconv.ParseInt(ts, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				if !time.Unix(issued, 0).Add(tc.tokenTTL).Equal(expires) {
					t.Fatalf("reported expiry %s, token issued at %d", expires, issued)
				}
				if remaining := expires.Sub(now); remaining < tc.tokenTTL*3/4 {
					t.Fatalf("remaining lifetime %s at %s", remaining, now)
				}
				distinct[u] = true
			}
			t.Logf("distinct token URLs over a day of per-minute resolves: %d", len(distinct))
			if len(distinct) > tc.maxDistinct {
				t.Fatalf("token URL took %d values in a day, want at most %d", len(distinct), tc.maxDistinct)
			}
		})
	}
}

// A zero window keeps PresignGetURL's behavior for downloads, diagnostics, and
// seven-day links: signed now, for exactly the expiry.
func TestPresignGetURLAtWithoutWindowIssuesFreshURLs(t *testing.T) {
	ctx := context.Background()
	c := NewClient(windowTestConfig())
	now := time.Date(2026, 1, 2, 3, 7, 0, 0, time.UTC)
	first, expires, err := c.PresignGetURLAt(ctx, "silo", windowTestKey, now, time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	signed, lifetime := presignedValidity(t, first)
	if !signed.Equal(now) || lifetime != time.Hour || !expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("signed %s for %s, reported %s", signed, lifetime, expires)
	}
	next, _, err := c.PresignGetURLAt(ctx, "silo", windowTestKey, now.Add(time.Second), time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next == first {
		t.Fatal("zero window reused a URL")
	}
}
