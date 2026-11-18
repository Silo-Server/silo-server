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
		// The lifetime plus one URL bucket (15 minutes, or the TTL if shorter).
		{"default", 0, 4*time.Hour + 15*time.Minute}, {"minimum", time.Second, 2 * time.Minute}, {"maximum", 48 * time.Hour, 24*time.Hour + 15*time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, exp := NewSigner("secret", tc.input).Sign("a.webp", now)
			if got := exp.Sub(now); got != tc.want {
				t.Fatalf("TTL = %s, want %s", got, tc.want)
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

type ttlRecordingDirect struct{}

func (ttlRecordingDirect) DirectURL(_ context.Context, key string, ttl, window time.Duration) (string, time.Time, error) {
	if key == "missing" {
		return "", time.Time{}, errors.New("unavailable")
	}
	return "https://example/" + key + "?ttl=" + ttl.String() + "&window=" + window.String(), time.Now().Add(ttl), nil
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

// The artwork signer never signs, and never accepts, a key under a namespace
// that shares a local root with artwork but is not artwork: a backup's
// database dump, a diagnostic bundle, a job artifact. Other signers (job
// artifacts, backup objects) sign their own keys as before.
func TestArtworkSignerRefusesOperationalKeys(t *testing.T) {
	s := NewSigner("secret", time.Hour)
	now := time.Now()
	for _, key := range []string{"backups/server/20261102t040000z/db.dump", "/backups/x", "diagnostics/1/report.tar.gz", "catalog-seeds/a.json"} {
		if u, _ := s.Sign(key, now); u != "" {
			t.Errorf("signed %s as artwork: %s", key, u)
		}
		exp := now.Add(time.Hour).Unix()
		// A signature made as if the key were artwork is refused too.
		if err := s.Verify(key, exp, s.signature(key, exp), now); !errors.Is(err, ErrRefusedKey) {
			t.Errorf("verifying %s = %v, want ErrRefusedKey", key, err)
		}
	}
	if u, _ := s.Sign("local/movies/m1/abcd1234/poster/original.r1.jpg", now); u == "" {
		t.Error("an artwork key was refused")
	}
	if u, _ := NewBackupObjectSigner("secret", time.Hour).Sign(BackupObjectKey("20261102t040000z", "db.dump"), now); u == "" {
		t.Error("the backup object signer refused its own key")
	}
}
