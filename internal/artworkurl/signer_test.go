package artworkurl

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

const (
	testSecret = "cluster-authentication-secret"
	testKey    = "artwork/v1/objects/poster/ab/abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd/original.webp"
)

func testSigner(t *testing.T, ttl time.Duration) *Signer {
	t.Helper()
	signer, err := NewSigner(testSecret, func() time.Duration { return ttl })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return signer
}

func TestDirectLibraryReferenceRejectsUnknownAndNonCanonicalPayloads(t *testing.T) {
	signer := testSigner(t, time.Hour)
	for _, payload := range []string{
		`{"surface":"item posters","keys":["movie-1"],"fingerprint":"abc","unknown":true}`,
		`{ "surface":"item posters","keys":["movie-1"],"fingerprint":"abc"}`,
	} {
		reference := LibraryReferencePrefix + base64.RawURLEncoding.EncodeToString([]byte(payload))
		if _, err := signer.ParseLibraryReference(reference); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("ParseLibraryReference(%s) = %v, want ErrInvalidSignature", payload, err)
		}
	}
}

// parseSigned splits a minted URL back into the parts the route receives.
func parseSigned(t *testing.T, signed string) (encodedKey, expires, signature string) {
	t.Helper()
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parsing %q: %v", signed, err)
	}
	if !strings.HasPrefix(parsed.Path, RoutePrefix) {
		t.Fatalf("minted path = %q, want prefix %q", parsed.Path, RoutePrefix)
	}
	query := parsed.Query()
	return strings.TrimPrefix(parsed.Path, RoutePrefix), query.Get(ExpiresParam), query.Get(SignatureParam)
}

func TestNewSignerRequiresSecret(t *testing.T) {
	if _, err := NewSigner("   ", nil); err == nil {
		t.Fatal("NewSigner accepted a blank cluster secret")
	}
}

func TestSignMintsRootRelativeURL(t *testing.T) {
	signer := testSigner(t, time.Hour)

	signed, err := signer.Sign(testKey, time.Now())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.HasPrefix(signed.URL, RoutePrefix) {
		t.Fatalf("URL = %q, want a root-relative artwork route URL", signed.URL)
	}
	if signed.ExpiresAt == nil {
		t.Fatal("signed URL reports no expiry")
	}
	// The key is carried in the URL, never a filesystem path.
	if strings.Contains(signed.URL, testKey) {
		t.Fatalf("URL = %q, want the key encoded rather than embedded", signed.URL)
	}
}

func TestTargetCapabilityRoundTripBindsTargetRevisionAndVariant(t *testing.T) {
	signer := testSigner(t, time.Hour)
	now := time.Now()
	target := Target{
		Surface: SurfaceItemPosters,
		Keys:    []string{"movie-1"},
		Slot:    "poster",
	}.WithReference(testKey)
	signed, err := signer.SignTarget(target, "w300", now)
	if err != nil {
		t.Fatalf("SignTarget: %v", err)
	}
	path := strings.TrimPrefix(signed.URL, RoutePrefix)
	capability, variant, ok := strings.Cut(path, "/")
	if !ok {
		t.Fatalf("signed target URL has no variant: %q", signed.URL)
	}
	verified, expiresAt, err := signer.VerifyTarget(capability, variant, now)
	if err != nil {
		t.Fatalf("VerifyTarget: %v", err)
	}
	if verified.Surface != target.Surface || verified.Keys[0] != "movie-1" || verified.ExpectedRevision != target.ExpectedRevision || variant != "w300" {
		t.Fatalf("verified target = %#v, variant %q", verified, variant)
	}
	if !expiresAt.Equal(*signed.ExpiresAt) {
		t.Fatalf("expiry = %s, want %s", expiresAt, signed.ExpiresAt)
	}
	if _, _, err := signer.VerifyTarget(capability, "w500", now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("variant substitution = %v, want ErrInvalidSignature", err)
	}
}

func TestDirectLibraryReferenceIsOpaquePathFreeAndRevisionBound(t *testing.T) {
	signer := testSigner(t, time.Hour)
	identity := LibraryIdentity{Surface: "item posters", Keys: []string{"movie-1"}, Fingerprint: strings.Repeat("a", 64)}
	reference, err := signer.LibraryReference(identity)
	if err != nil {
		t.Fatalf("LibraryReference: %v", err)
	}
	if strings.Contains(reference, "/media/") || strings.Contains(reference, "file://") {
		t.Fatalf("reference exposes a source path: %q", reference)
	}
	if strings.Contains(strings.TrimPrefix(reference, LibraryReferencePrefix), ".") {
		t.Fatalf("stored reference contains a secret-bound inner signature: %q", reference)
	}
	rotated, err := NewSigner("rotated-or-restored-cluster-secret", func() time.Duration { return time.Hour })
	if err != nil {
		t.Fatalf("NewSigner(rotated): %v", err)
	}
	if got, err := rotated.ParseLibraryReference(reference); err != nil || got.Surface != identity.Surface {
		t.Fatalf("stored reference did not survive secret rotation: %#v, %v", got, err)
	}
	if _, err := signer.ParseLibraryReference(reference + ".legacy-inner-hmac"); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("legacy signed stored reference error = %v, want ErrInvalidSignature", err)
	}
	signed, err := signer.SignLibraryReference(reference, time.Now())
	if err != nil {
		t.Fatalf("SignLibraryReference: %v", err)
	}
	parsed, err := url.Parse(signed.URL)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}
	gotReference, gotIdentity, _, err := signer.VerifyLibraryURL(
		strings.TrimPrefix(parsed.Path, LibraryRoutePrefix),
		parsed.Query().Get(ExpiresParam),
		parsed.Query().Get(SignatureParam),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("VerifyLibraryURL: %v", err)
	}
	if gotReference != reference || gotIdentity.Fingerprint != identity.Fingerprint || gotIdentity.Keys[0] != "movie-1" {
		t.Fatalf("verified reference = %#v, %#v", gotReference, gotIdentity)
	}

	tampered := reference[:len(reference)-1] + "A"
	if _, err := signer.ParseLibraryReference(tampered); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("tampered identity error = %v", err)
	}
}

// Quantization is what makes client caching possible: every mint inside one
// window has to produce a byte-identical URL, and the next window a different
// one.
func TestSignQuantizesExpiryToWindow(t *testing.T) {
	const ttl = time.Hour
	signer := testSigner(t, ttl)

	base := time.Unix(1_800_000_000, 0).UTC() // an exact hour boundary
	first, err := signer.Sign(testKey, base.Add(time.Second))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	middle, err := signer.Sign(testKey, base.Add(59*time.Minute))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	next, err := signer.Sign(testKey, base.Add(61*time.Minute))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if first.URL != middle.URL {
		t.Fatalf("URLs differ inside one window:\n%s\n%s", first.URL, middle.URL)
	}
	if next.URL == first.URL {
		t.Fatal("URL did not change after the window boundary")
	}

	// A minted URL lives at least one TTL and at most two, which is the whole
	// cost of quantization.
	life := first.ExpiresAt.Sub(base.Add(time.Second))
	if life < ttl || life > 2*ttl {
		t.Fatalf("signed lifetime = %s, want between %s and %s", life, ttl, 2*ttl)
	}
}

func TestSignRejectsKeysOutsideTheGrammar(t *testing.T) {
	signer := testSigner(t, time.Hour)

	for _, key := range []string{"", "/etc/passwd", "../../etc/passwd", "artwork//v1", "artwork/v1/../secret"} {
		if _, err := signer.Sign(key, time.Now()); !errors.Is(err, artworkstore.ErrInvalidKey) {
			t.Fatalf("Sign(%q) error = %v, want ErrInvalidKey", key, err)
		}
	}
}

func TestVerifyRoundTrip(t *testing.T) {
	signer := testSigner(t, time.Hour)
	now := time.Now()

	signed, err := signer.Sign(testKey, now)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	encodedKey, expires, signature := parseSigned(t, signed.URL)

	key, expiresAt, err := signer.Verify(encodedKey, expires, signature, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if key != testKey {
		t.Fatalf("verified key = %q, want %q", key, testKey)
	}
	if !expiresAt.Equal(*signed.ExpiresAt) {
		t.Fatalf("verified expiry = %s, want %s", expiresAt, *signed.ExpiresAt)
	}
}

func TestVerifyRejectsTamperedRequests(t *testing.T) {
	signer := testSigner(t, time.Hour)
	now := time.Now()

	signed, err := signer.Sign(testKey, now)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	encodedKey, expires, signature := parseSigned(t, signed.URL)

	// A different key under the same signature is the attack that matters: it
	// would turn one authorized object into a read of any other.
	otherKey, err := signer.Sign("artwork/v1/objects/poster/cd/cdef/original.webp", now)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	otherEncodedKey, _, _ := parseSigned(t, otherKey.URL)

	extendedExpiry := strconv.FormatInt(signed.ExpiresAt.Add(24*time.Hour).Unix(), 10)

	cases := []struct {
		name                           string
		encodedKey, expires, signature string
	}{
		{"substituted key", otherEncodedKey, expires, signature},
		{"extended expiry", encodedKey, extendedExpiry, signature},
		{"forged signature", encodedKey, expires, strings.Repeat("A", len(signature))},
		{"missing signature", encodedKey, expires, ""},
		{"missing expiry", encodedKey, "", signature},
		{"unparsable expiry", encodedKey, "soon", signature},
		{"undecodable key", "!!!!", expires, signature},
		{"non-canonical key encoding", encodedKey + "=", expires, signature},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := signer.Verify(tc.encodedKey, tc.expires, tc.signature, now); !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("Verify error = %v, want ErrInvalidSignature", err)
			}
		})
	}
}

func TestVerifyRejectsForeignSecret(t *testing.T) {
	now := time.Now()
	mine := testSigner(t, time.Hour)
	theirs, err := NewSigner("a different cluster secret", func() time.Duration { return time.Hour })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	signed, err := theirs.Sign(testKey, now)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	encodedKey, expires, signature := parseSigned(t, signed.URL)

	if _, _, err := mine.Verify(encodedKey, expires, signature, now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("Verify error = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifyReportsExpiry(t *testing.T) {
	signer := testSigner(t, time.Hour)
	now := time.Now()

	signed, err := signer.Sign(testKey, now)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	encodedKey, expires, signature := parseSigned(t, signed.URL)

	if _, _, err := signer.Verify(encodedKey, expires, signature, signed.ExpiresAt.Add(time.Second)); !errors.Is(err, ErrExpired) {
		t.Fatalf("Verify error = %v, want ErrExpired", err)
	}
	// Still valid on the boundary itself.
	if _, _, err := signer.Verify(encodedKey, expires, signature, *signed.ExpiresAt); err != nil {
		t.Fatalf("Verify at the expiry instant = %v, want valid", err)
	}
}

func TestTTLIsClampedAndLive(t *testing.T) {
	configured := time.Hour
	signer, err := NewSigner(testSecret, func() time.Duration { return configured })
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	if got := signer.TTL(); got != time.Hour {
		t.Fatalf("TTL = %s, want 1h", got)
	}
	configured = 0
	if got := signer.TTL(); got != DefaultTTL {
		t.Fatalf("TTL with no configured value = %s, want %s", got, DefaultTTL)
	}
	configured = time.Second
	if got := signer.TTL(); got != minTTL {
		t.Fatalf("TTL below the floor = %s, want %s", got, minTTL)
	}
	configured = 30 * 24 * time.Hour
	if got := signer.TTL(); got != maxTTL {
		t.Fatalf("TTL above the ceiling = %s, want %s", got, maxTTL)
	}

	var nilSigner *Signer
	if got := nilSigner.TTL(); got != DefaultTTL {
		t.Fatalf("nil signer TTL = %s, want %s", got, DefaultTTL)
	}
}

func TestDeriveSecretIsDomainSeparated(t *testing.T) {
	derived := DeriveSecret([]byte(testSecret))
	if len(derived) == 0 {
		t.Fatal("derived secret is empty")
	}
	if string(derived) == testSecret {
		t.Fatal("derived secret is the cluster secret itself")
	}
	if string(derived) == string(DeriveSecret([]byte("other"))) {
		t.Fatal("different cluster secrets derived the same artwork secret")
	}
}
