package artworkurl

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSignerRejectsExpiryAndTamper(t *testing.T) {
	s := NewSigner("secret", time.Hour)
	now := time.Now()
	u, exp := s.Sign("a.webp", now)
	sig := strings.Split(strings.Split(u, "sig=")[1], "&")[0]
	if err := s.Verify("b.webp", exp.Unix(), sig, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err=%v", err)
	}
	if err := s.Verify("a.webp", exp.Unix(), sig, exp); !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry err=%v", err)
	}
}

func TestDirectResolver(t *testing.T) {
	resolver := NewDirectResolver(fakeDirect{}, time.Hour)
	if got := resolver.ResolveURLs(context.Background(), []string{"a.webp", "missing"}); got["a.webp"].URL != "https://example/a.webp" || got["a.webp"].ExpiresAt == nil || len(got) != 1 {
		t.Fatal(got)
	}
}

type fakeDirect struct{}

func (fakeDirect) DirectURL(_ context.Context, key string, ttl, _ time.Duration) (string, time.Time, error) {
	if key == "missing" {
		return "", time.Time{}, errors.New("unavailable")
	}
	return "https://example/" + key, time.Now().Add(ttl), nil
}

func TestSignerRejectsWrongSecretAndExpiryTamper(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 7, 0, 0, time.UTC)
	signer := NewSigner("secret", time.Hour)
	path, exp := signer.Sign("a.webp", now)
	parsed, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	sig := parsed.Query().Get("sig")
	if err := NewSigner("other", time.Hour).Verify("a.webp", exp.Unix(), sig, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := signer.Verify("a.webp", exp.Add(time.Minute).Unix(), sig, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered expiry: %v", err)
	}
}

func TestSignerTTL(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		input, want time.Duration
	}{
		{"default", 0, 4 * time.Hour}, {"minimum", time.Second, time.Minute}, {"maximum", 48 * time.Hour, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, exp := NewSigner("secret", tc.input).Sign("a.webp", now)
			if got := exp.Sub(now); got != tc.want+min(15*time.Minute, tc.want) {
				t.Fatalf("TTL = %s, want %s", got, tc.want+min(15*time.Minute, tc.want))
			}
		})
	}
}

func TestSignerEscapesKey(t *testing.T) {
	key := "uploads/a b#c?d%25.webp"
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	signer := NewSigner("secret", time.Hour)
	path, exp := signer.Sign(key, now)
	parsed, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimPrefix(parsed.Path, "/api/v2/artwork/"); got != key {
		t.Fatalf("decoded key = %q, want %q", got, key)
	}
	if parsed.Fragment != "" || len(parsed.Query()) != 2 {
		t.Fatalf("key escaped into URL fields: %s", path)
	}
	if err := signer.Verify(key, exp.Unix(), parsed.Query().Get("sig"), now); err != nil {
		t.Fatal(err)
	}
}

func TestSignerMinimumLifetimeAndStableBuckets(t *testing.T) {
	for _, ttl := range []time.Duration{time.Minute, 7 * time.Minute, 15 * time.Minute, 4 * time.Hour} {
		t.Run(ttl.String(), func(t *testing.T) {
			signer := NewSigner("secret", ttl)
			start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
			bucket := min(15*time.Minute, ttl)
			start = start.Truncate(bucket)
			first, _ := signer.Sign("a.webp", start)
			for _, offset := range []time.Duration{0, bucket / 2, bucket - time.Second} {
				now := start.Add(offset)
				path, exp := signer.Sign("a.webp", now)
				if path != first {
					t.Fatal("URL changed within bucket")
				}
				if remaining := exp.Sub(now); remaining < ttl || remaining > ttl+bucket {
					t.Fatalf("remaining lifetime %s for TTL %s", remaining, ttl)
				}
			}
			next, _ := signer.Sign("a.webp", start.Add(bucket))
			if next == first {
				t.Fatal("URL did not rotate at bucket boundary")
			}
		})
	}
}

func TestSignForHonorsRequestedTTL(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	signer := NewSigner("secret", 4*time.Hour)
	_, exp := signer.SignFor("a.webp", now, 15*time.Minute)
	if got := exp.Sub(now); got != 30*time.Minute {
		t.Fatalf("15m capability lived %s", got)
	}
	// Non-positive falls back to the signer default; out of range clamps.
	if _, exp := signer.SignFor("a.webp", now, 0); exp.Sub(now) != 4*time.Hour+15*time.Minute {
		t.Fatalf("default TTL not applied: %s", exp.Sub(now))
	}
	if _, exp := signer.SignFor("a.webp", now, time.Second); exp.Sub(now) != 2*time.Minute {
		t.Fatalf("minimum not clamped: %s", exp.Sub(now))
	}
	path, exp := signer.SignFor("a.webp", now, 15*time.Minute)
	parsed, _ := url.Parse(path)
	if err := signer.Verify("a.webp", exp.Unix(), parsed.Query().Get("sig"), now); err != nil {
		t.Fatal(err)
	}
}

func TestResolveURLForUsesLifetimeOnBothResolvers(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	server := NewServerResolver(NewSigner("secret", 4*time.Hour))
	resolved, ok := ResolveURLFor(ctx, server, "a.webp", 15*time.Minute)
	if !ok || resolved.ExpiresAt == nil || resolved.ExpiresAt.Sub(now) > 31*time.Minute {
		t.Fatalf("server resolver ignored the lifetime: %+v", resolved)
	}
	direct := NewDirectResolver(ttlRecordingDirect{}, time.Hour)
	resolved, ok = ResolveURLFor(ctx, direct, "a.webp", 15*time.Minute)
	if !ok || resolved.URL != "https://example/a.webp?ttl=15m0s&window=0s" {
		t.Fatalf("direct resolver ignored the lifetime: %+v", resolved)
	}
	if _, ok := ResolveURLFor(ctx, direct, "missing", 15*time.Minute); ok {
		t.Fatal("missing object resolved")
	}
	if _, ok := ResolveURLFor(ctx, nil, "a.webp", time.Minute); ok {
		t.Fatal("nil resolver resolved")
	}
}

type ttlRecordingDirect struct{}

func (ttlRecordingDirect) DirectURL(_ context.Context, key string, ttl, window time.Duration) (string, time.Time, error) {
	if key == "missing" {
		return "", time.Time{}, errors.New("unavailable")
	}
	return "https://example/" + key + "?ttl=" + ttl.String() + "&window=" + window.String(), time.Now().Add(ttl), nil
}

// Mutable keys, capabilities shorter than the default, and job artifacts keep
// 15-minute buckets: a day that starts mid-bucket spans 97 of them.
func TestMutableAndShortLivedURLsKeepShortBuckets(t *testing.T) {
	artwork := NewSigner("secret", 4*time.Hour)
	jobs := NewJobArtifactSigner("secret", 15*time.Minute)
	start := time.Date(2026, 1, 2, 3, 7, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		ttl  time.Duration
		sign func(time.Time) (string, time.Time)
	}{
		{"mutable key", 4 * time.Hour, func(now time.Time) (string, time.Time) {
			return artwork.Sign("library-posters/5.jpg", now)
		}},
		{"short capability", 15 * time.Minute, func(now time.Time) (string, time.Time) {
			return artwork.SignFor("chapters/1/w300.0123abcd.webp", now, 15*time.Minute)
		}},
		{"job artifact", 15 * time.Minute, func(now time.Time) (string, time.Time) {
			return jobs.SignFor("job.0123abcd", now, 15*time.Minute)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			distinct := map[string]bool{}
			for i := range 24 * 60 {
				now := start.Add(time.Duration(i) * time.Minute)
				path, exp := tc.sign(now)
				if remaining := exp.Sub(now); remaining < tc.ttl || remaining > tc.ttl+shortURLBucket {
					t.Fatalf("remaining lifetime %s at %s", remaining, now)
				}
				distinct[path] = true
			}
			if len(distinct) != 97 {
				t.Fatalf("got %d URLs in a day, want 97 15-minute buckets", len(distinct))
			}
		})
	}
}

func TestDirectResolverHoldsOnlyRevisionedDefaultLifetimeURLs(t *testing.T) {
	ctx := context.Background()
	direct := NewDirectResolver(ttlRecordingDirect{}, time.Hour)
	revisioned := "tmdb/movie/1/poster/w500.0123abcd.webp"
	for _, tc := range []struct {
		name, key string
		ttl       time.Duration
		window    string
	}{
		{"revisioned default", revisioned, 0, "24h0m0s"},
		{"mutable default", "library-posters/5.jpg", 0, "0s"},
		{"revisioned short", revisioned, 15 * time.Minute, "0s"},
	} {
		resolved, ok := ResolveURLFor(ctx, direct, tc.key, tc.ttl)
		if !ok || !strings.HasSuffix(resolved.URL, "&window="+tc.window) {
			t.Fatalf("%s: got %+v, want window %s", tc.name, resolved, tc.window)
		}
	}
	if got := direct.ResolveURLs(ctx, []string{revisioned})[revisioned].URL; !strings.HasSuffix(got, "&window=24h0m0s") {
		t.Fatalf("batch resolve did not hold the revisioned URL: %s", got)
	}
}

func TestSignedKeyAcceptsOnlyItsOwnValidURLs(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	signer := NewSigner("secret", time.Hour)
	key := "uploads/a b#c?d%25.webp"
	signed, exp := signer.Sign(key, now)
	if got, ok := signer.SignedKey(signed, now); !ok || got != key {
		t.Fatalf("SignedKey(%s) = %q, %v", signed, got, ok)
	}

	other, _ := NewSigner("other", time.Hour).Sign(key, now)
	artifact, _ := NewJobArtifactSigner("secret", time.Hour).Sign("job-1", now)
	for name, rawURL := range map[string]string{
		"expired":        signed,
		"other secret":   other,
		"other domain":   artifact,
		"tampered key":   strings.Replace(signed, "uploads/", "uploadz/", 1),
		"absolute":       "https://cdn.example" + signed,
		"invalid key":    strings.Replace(signed, "uploads/", "../", 1),
		"not a URL":      "%zz",
		"missing expiry": strings.Split(signed, "?")[0],
	} {
		at := now
		if name == "expired" {
			at = exp
		}
		if got, ok := signer.SignedKey(rawURL, at); ok {
			t.Errorf("%s: SignedKey(%s) = %q, want rejection", name, rawURL, got)
		}
	}
}
