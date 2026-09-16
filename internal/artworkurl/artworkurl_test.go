package artworkurl

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSignerRoundTripAndQuantization(t *testing.T) {
	s := NewSigner("secret", 4*time.Hour)
	now := time.Date(2026, 1, 2, 3, 7, 0, 0, time.UTC)
	path, exp := s.Sign("tmdb/movie/poster.webp", now)
	if !strings.Contains(path, "/api/v2/artwork/tmdb/movie/poster.webp") {
		t.Fatal(path)
	}
	parts := strings.Split(path, "?")[1]
	if err := s.Verify("tmdb/movie/poster.webp", exp.Unix(), strings.Split(parts, "&sig=")[1], now); err != nil {
		t.Fatal(err)
	}
	path2, _ := s.Sign("tmdb/movie/poster.webp", now.Add(5*time.Minute))
	if path != path2 {
		t.Fatalf("quantization differs: %s %s", path, path2)
	}
}
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
func TestServerResolver(t *testing.T) {
	s := NewSigner("secret", time.Hour)
	got := NewServerResolver(s).ResolveURLs(context.Background(), []string{"a.webp"})
	if got["a.webp"].URL == "" || got["a.webp"].ExpiresAt == nil {
		t.Fatal(got)
	}
}
func TestDirectResolver(t *testing.T) {
	resolver := NewDirectResolver(fakeDirect{}, time.Hour)
	if got := resolver.ResolveURLs(context.Background(), []string{"a.webp", "missing"}); got["a.webp"].URL != "https://example/a.webp" || got["a.webp"].ExpiresAt == nil || len(got) != 1 {
		t.Fatal(got)
	}
}

type fakeDirect struct{}

func (fakeDirect) DirectURL(_ context.Context, key string, _ time.Duration) (string, error) {
	if key == "missing" {
		return "", errors.New("unavailable")
	}
	return "https://example/" + key, nil
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

func TestSignerShortTTLAcrossBucketBoundary(t *testing.T) {
	signer := NewSigner("secret", time.Minute)
	for _, now := range []time.Time{
		time.Date(2026, 1, 2, 3, 14, 59, 0, time.UTC),
		time.Date(2026, 1, 2, 3, 15, 0, 0, time.UTC),
	} {
		path, exp := signer.Sign("a.webp", now)
		parsed, err := url.Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		if exp.Before(now.Add(time.Minute)) || exp.After(now.Add(2*time.Minute)) {
			t.Fatalf("expiry %s for now %s", exp, now)
		}
		if err := signer.Verify("a.webp", exp.Unix(), parsed.Query().Get("sig"), now); err != nil {
			t.Fatal(err)
		}
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
