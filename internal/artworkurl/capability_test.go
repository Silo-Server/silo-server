package artworkurl

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// Artwork URLs are held by every deployed client and cached downstream.
// Generalizing the signer must not have moved a single byte of them.
func TestArtworkURLsUnchangedAfterSignerGeneralization(t *testing.T) {
	signer := NewSigner("test-secret", time.Hour)
	at := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	got, _ := signer.Sign("tmdb/movies/550/poster/original.webp", at)
	// Derived from the pre-refactor signer, not captured from this code.
	const want = "/api/v2/artwork/tmdb/movies/550/poster/original.webp?exp=1789564500&sig=MWEDxdUKUUKZFZmGL_XsqA"
	if got != want {
		t.Fatalf("artwork URL changed:\n got %s\nwant %s", got, want)
	}
}

func TestJobArtifactSignerRoutesAndVerifies(t *testing.T) {
	signer := NewJobArtifactSigner("test-secret", 15*time.Minute)
	now := time.Now()
	raw, expires := signer.SignFor("job-abc", now, 15*time.Minute)
	if !strings.HasPrefix(raw, "/api/v2/admin/jobs/job-abc/artifact?") {
		t.Fatalf("route = %s", raw)
	}
	if !expires.After(now) {
		t.Fatalf("expiry %s is not in the future", expires)
	}
	exp, sig := queryOf(t, raw)
	if err := signer.Verify("job-abc", exp, sig, now); err != nil {
		t.Fatal(err)
	}
	// A capability for one job must not open another's artifact.
	if err := signer.Verify("job-xyz", exp, sig, now); err == nil {
		t.Fatal("signature for one job verified another")
	}
	if err := signer.Verify("job-abc", exp, sig, expires.Add(time.Second)); err == nil {
		t.Fatal("expired capability accepted")
	}
}

// The two capability kinds derive independent keys, so a URL minted for artwork
// cannot be replayed to download a job artifact, or the reverse. Artwork URLs
// are handed out broadly and job artifacts are administrator data.
func TestCapabilityDomainsDoNotCrossVerify(t *testing.T) {
	const secret = "test-secret"
	artwork := NewSigner(secret, time.Hour)
	artifact := NewJobArtifactSigner(secret, time.Hour)
	now := time.Now()

	artworkURL, _ := artwork.SignFor("job-abc", now, time.Hour)
	exp, sig := queryOf(t, artworkURL)
	if err := artifact.Verify("job-abc", exp, sig, now); err == nil {
		t.Fatal("artwork capability authorized a job artifact")
	}

	artifactURL, _ := artifact.SignFor("job-abc", now, time.Hour)
	exp, sig = queryOf(t, artifactURL)
	if err := artwork.Verify("job-abc", exp, sig, now); err == nil {
		t.Fatal("job artifact capability authorized artwork")
	}
}

func queryOf(t *testing.T, raw string) (int64, string) {
	t.Helper()
	_, query, ok := strings.Cut(raw, "?")
	if !ok {
		t.Fatalf("no query in %s", raw)
	}
	var exp int64
	var sig string
	for _, part := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(part, "=")
		switch key {
		case "exp":
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			exp = parsed
		case "sig":
			sig = value
		}
	}
	if exp == 0 || sig == "" {
		t.Fatalf("missing exp/sig in %s", raw)
	}
	return exp, sig
}
