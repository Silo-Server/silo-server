// Package artworkurl mints and verifies target-bound resilient delivery
// capabilities for stored artwork.
//
// Resilient delivery uses the same signed Silo route for every backend. The
// capability names the catalog target rather than a physical object so the
// handler can reload the current selection and fall back or repair after loss:
//
//	GET /api/v1/artwork/{base64url-target-json}.{hmac}/{variant}
//
// The signature is a bearer capability, exactly like a presigned S3 URL:
// access is authorized when the surrounding catalog response is built, and the
// resulting URL is usable by a browser <img> element that cannot attach a
// bearer header. It is deliberately not bound to a profile, client, or address
// so several clients and any future edge cache can share identical bytes;
// revocation is bounded by the short lifetime.
//
// The signing key is derived from the cluster authentication secret rather than
// configured separately, which keeps artwork tokens cryptographically separate
// from login and playback tokens while adding no setup step. Rotating the
// cluster secret invalidates outstanding artwork URLs along with every other
// derived token family; the short lifetime bounds that to one refresh.
package artworkurl

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

const (
	// RoutePrefix is the path every signed artwork URL starts with. Minted
	// URLs are root-relative: they are correct for every origin the server is
	// reached through (LAN address, tunnel, reverse proxy), which a single
	// configured public origin would not be, and they stay cacheable by the
	// resolver across those origins.
	RoutePrefix            = "/api/v1/artwork/"
	LibraryRoutePrefix     = "/api/v1/artwork-library/"
	LibraryReferencePrefix = "library-artwork://"

	// ExpiresParam carries the signed expiry as a Unix timestamp in seconds.
	ExpiresParam = "expires"

	// SignatureParam carries the base64url HMAC over the key and expiry.
	SignatureParam = "signature"

	// secretContext domain-separates the artwork signing key from every other
	// secret derived from the cluster authentication secret. Never reuse it.
	secretContext = "silo-artwork-url-v1"

	// routeVersion is covered by the signature, so a future change to the URL
	// grammar invalidates outstanding URLs instead of letting an old signature
	// authenticate a new meaning.
	routeVersion        = "v1"
	targetRouteVersion  = "target-v1"
	libraryRouteVersion = "library-v1"

	// DefaultTTL is the artwork URL lifetime used when none is configured. It
	// mirrors the default S3 presign lifetime.
	DefaultTTL = 4 * time.Hour

	// minTTL and maxTTL bound a configured lifetime. The floor keeps
	// quantization from minting a URL that expires before a page finishes
	// loading; the ceiling bounds how long a leaked URL stays usable.
	minTTL = time.Minute
	maxTTL = 24 * time.Hour
)

type targetCapability struct {
	Version   string `json:"version"`
	Target    Target `json:"target"`
	Variant   string `json:"variant"`
	ExpiresAt int64  `json:"expires_at"`
}

// SignTarget mints the default resilient route. The capability is one
// canonical base64url JSON payload plus an HMAC, while variant remains a
// readable bounded path component and is covered inside the payload.
func (s *Signer) SignTarget(target Target, variant string, now time.Time) (artworkstore.ResolvedURL, error) {
	if s == nil {
		return artworkstore.ResolvedURL{}, errors.New("artworkurl: signer is not configured")
	}
	if err := target.Validate(); err != nil {
		return artworkstore.ResolvedURL{}, err
	}
	if err := ValidateVariant(target.Slot, variant); err != nil {
		return artworkstore.ResolvedURL{}, err
	}
	target.Reference = ""
	expiresAt := quantizedExpiry(now, s.TTL())
	capability := targetCapability{Version: targetRouteVersion, Target: target, Variant: variant, ExpiresAt: expiresAt.Unix()}
	payload, err := json.Marshal(capability)
	if err != nil {
		return artworkstore.ResolvedURL{}, fmt.Errorf("artworkurl: encode target capability: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := s.capabilitySignature(payload)
	return artworkstore.ResolvedURL{URL: RoutePrefix + encoded + "." + signature + "/" + variant, ExpiresAt: &expiresAt}, nil
}

// VerifyTarget authenticates a target-bound resilient route and rejects
// non-canonical payloads before any catalog or store access.
func (s *Signer) VerifyTarget(encodedCapability, variant string, now time.Time) (Target, time.Time, error) {
	if s == nil || encodedCapability == "" || variant == "" {
		return Target{}, time.Time{}, ErrInvalidSignature
	}
	encoded, signature, ok := strings.Cut(encodedCapability, ".")
	if !ok || encoded == "" || signature == "" || strings.Contains(signature, ".") {
		return Target{}, time.Time{}, ErrInvalidSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != encoded {
		return Target{}, time.Time{}, ErrInvalidSignature
	}
	expected := s.capabilitySignature(payload)
	if len(expected) != len(signature) || subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return Target{}, time.Time{}, ErrInvalidSignature
	}
	var capability targetCapability
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capability); err != nil {
		return Target{}, time.Time{}, ErrInvalidSignature
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Target{}, time.Time{}, ErrInvalidSignature
	}
	canonical, err := json.Marshal(capability)
	if err != nil || subtle.ConstantTimeCompare(canonical, payload) != 1 || capability.Version != targetRouteVersion || capability.Variant != variant {
		return Target{}, time.Time{}, ErrInvalidSignature
	}
	if err := capability.Target.Validate(); err != nil || ValidateVariant(capability.Target.Slot, variant) != nil || capability.ExpiresAt <= 0 {
		return Target{}, time.Time{}, ErrInvalidSignature
	}
	expiresAt := time.Unix(capability.ExpiresAt, 0).UTC()
	if now.After(expiresAt) {
		return capability.Target, expiresAt, ErrExpired
	}
	return capability.Target, expiresAt, nil
}

func (s *Signer) capabilitySignature(payload []byte) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(targetRouteVersion))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

var (
	// ErrInvalidSignature reports a URL that this server did not mint: a
	// malformed key, a key outside the logical-key grammar, a missing or
	// unparsable expiry, or a signature mismatch. Callers must answer it
	// identically to an unknown key so the response reveals nothing about
	// which keys exist.
	ErrInvalidSignature = errors.New("artworkurl: invalid artwork url signature")

	// ErrExpired reports a correctly signed URL whose lifetime has passed. It
	// is only ever returned after the signature verifies, so answering it
	// distinctly still reveals nothing about stored objects.
	ErrExpired = errors.New("artworkurl: artwork url expired")
)

// DeriveSecret produces the artwork URL signing key from the cluster
// authentication secret. Same construction as the OAuth state secret, with its
// own context string.
func DeriveSecret(jwtSecret []byte) []byte {
	mac := hmac.New(sha256.New, jwtSecret)
	_, _ = mac.Write([]byte(secretContext))
	return mac.Sum(nil)
}

// LibraryIdentity names a catalog artwork target without containing its
// source path. Keys are the target table's stable primary-key values and the
// source fingerprint binds the identity to the exact sidecar revision.
type LibraryIdentity struct {
	Surface     string   `json:"surface"`
	Keys        []string `json:"keys"`
	Fingerprint string   `json:"fingerprint"`
}

func (s *Signer) LibraryReference(identity LibraryIdentity) (string, error) {
	return EncodeLibraryReference(identity)
}

func EncodeLibraryReference(identity LibraryIdentity) (string, error) {
	if strings.TrimSpace(identity.Surface) == "" || len(identity.Keys) == 0 || strings.TrimSpace(identity.Fingerprint) == "" {
		return "", errors.New("artworkurl: invalid direct-library identity")
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("artworkurl: encode direct-library identity: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return LibraryReferencePrefix + encoded, nil
}

func (s *Signer) ParseLibraryReference(reference string) (LibraryIdentity, error) {
	var identity LibraryIdentity
	if !strings.HasPrefix(reference, LibraryReferencePrefix) {
		return identity, ErrInvalidSignature
	}
	encoded := strings.TrimPrefix(reference, LibraryReferencePrefix)
	if encoded == "" || strings.Contains(encoded, ".") {
		return identity, ErrInvalidSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != encoded {
		return identity, ErrInvalidSignature
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil || strings.TrimSpace(identity.Surface) == "" || len(identity.Keys) == 0 || identity.Fingerprint == "" {
		return LibraryIdentity{}, ErrInvalidSignature
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return LibraryIdentity{}, ErrInvalidSignature
	}
	canonical, err := json.Marshal(identity)
	if err != nil || subtle.ConstantTimeCompare(canonical, payload) != 1 {
		return LibraryIdentity{}, ErrInvalidSignature
	}
	return identity, nil
}

func (s *Signer) SignLibraryReference(reference string, now time.Time) (artworkstore.ResolvedURL, error) {
	if _, err := s.ParseLibraryReference(reference); err != nil {
		return artworkstore.ResolvedURL{}, err
	}
	expiresAt := quantizedExpiry(now, s.TTL())
	unix := expiresAt.Unix()
	encoded := base64.RawURLEncoding.EncodeToString([]byte(reference))
	url := LibraryRoutePrefix + encoded + "?" + ExpiresParam + "=" + strconv.FormatInt(unix, 10) +
		"&" + SignatureParam + "=" + s.libraryURLSignature(reference, unix)
	return artworkstore.ResolvedURL{URL: url, ExpiresAt: &expiresAt}, nil
}

func (s *Signer) VerifyLibraryURL(encodedReference, expires, signature string, now time.Time) (string, LibraryIdentity, time.Time, error) {
	var identity LibraryIdentity
	if s == nil || encodedReference == "" || expires == "" || signature == "" {
		return "", identity, time.Time{}, ErrInvalidSignature
	}
	data, err := base64.RawURLEncoding.DecodeString(encodedReference)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != encodedReference {
		return "", identity, time.Time{}, ErrInvalidSignature
	}
	reference := string(data)
	identity, err = s.ParseLibraryReference(reference)
	if err != nil {
		return "", LibraryIdentity{}, time.Time{}, err
	}
	unix, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || unix <= 0 {
		return "", LibraryIdentity{}, time.Time{}, ErrInvalidSignature
	}
	expected := s.libraryURLSignature(reference, unix)
	if len(expected) != len(signature) || subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return "", LibraryIdentity{}, time.Time{}, ErrInvalidSignature
	}
	expiresAt := time.Unix(unix, 0).UTC()
	if now.After(expiresAt) {
		return reference, identity, expiresAt, ErrExpired
	}
	return reference, identity, expiresAt, nil
}

func (s *Signer) libraryURLSignature(reference string, expiresUnix int64) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(libraryRouteVersion))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(reference))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strconv.FormatInt(expiresUnix, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Signer mints and verifies signed artwork URLs. It holds no per-URL state:
// every API and proxy node deriving from the same cluster secret mints and
// accepts the same URLs.
type Signer struct {
	secret []byte
	ttl    func() time.Duration
}

// NewSigner derives a signer from the cluster authentication secret. ttl reads
// the configured artwork URL lifetime and may be nil; it is read per mint so an
// administrator's change takes effect without a restart. Already-minted URLs
// keep the lifetime they were signed with.
func NewSigner(jwtSecret string, ttl func() time.Duration) (*Signer, error) {
	if strings.TrimSpace(jwtSecret) == "" {
		return nil, errors.New("artworkurl: a cluster authentication secret is required to sign artwork urls")
	}
	return &Signer{secret: DeriveSecret([]byte(jwtSecret)), ttl: ttl}, nil
}

// TTL is the currently configured URL lifetime, clamped to the supported range.
func (s *Signer) TTL() time.Duration {
	if s == nil {
		return DefaultTTL
	}
	return clampTTL(s.ttl)
}

// clampTTL reads a configured lifetime and holds it inside the supported range.
// A missing or nonsensical value falls back to the default rather than minting
// a URL that expires immediately or never.
func clampTTL(ttl func() time.Duration) time.Duration {
	if ttl == nil {
		return DefaultTTL
	}
	value := ttl()
	switch {
	case value <= 0:
		return DefaultTTL
	case value < minTTL:
		return minTTL
	case value > maxTTL:
		return maxTTL
	}
	return value
}

// Sign returns the root-relative signed URL for a logical artwork key, plus the
// instant it stops working.
//
// The expiry is quantized to a fixed boundary rather than measured from now:
// every mint inside one window produces a byte-identical URL, which is what
// lets a client cache the bytes at all — browsers key their cache on the whole
// URL, so a per-request signature would defeat caching of objects that never
// change. Quantization at most doubles the maximum lifetime and changes no
// other property of the capability.
func (s *Signer) Sign(key string, now time.Time) (artworkstore.ResolvedURL, error) {
	if s == nil {
		return artworkstore.ResolvedURL{}, errors.New("artworkurl: signer is not configured")
	}
	if err := artworkstore.ValidateKey(key); err != nil {
		return artworkstore.ResolvedURL{}, err
	}
	expiresAt := quantizedExpiry(now, s.TTL())
	unix := expiresAt.Unix()
	url := RoutePrefix + base64.RawURLEncoding.EncodeToString([]byte(key)) +
		"?" + ExpiresParam + "=" + strconv.FormatInt(unix, 10) +
		"&" + SignatureParam + "=" + s.signature(key, unix)
	return artworkstore.ResolvedURL{URL: url, ExpiresAt: &expiresAt}, nil
}

// Verify authenticates a signed artwork URL and returns the logical key it
// authorizes together with its expiry.
//
// The key is recovered from the signed payload, never interpreted as a path:
// callers pass the returned key to the store, which resolves it beneath its own
// root. Verification order matters — the signature is checked before expiry,
// and both before any store access, so a forged request can never reach the
// backend.
func (s *Signer) Verify(encodedKey, expires, signature string, now time.Time) (string, time.Time, error) {
	if s == nil {
		return "", time.Time{}, ErrInvalidSignature
	}
	if encodedKey == "" || expires == "" || signature == "" {
		return "", time.Time{}, ErrInvalidSignature
	}

	keyBytes, err := base64.RawURLEncoding.DecodeString(encodedKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: undecodable key", ErrInvalidSignature)
	}
	key := string(keyBytes)
	// Reject any non-canonical encoding of the same key so one stored object
	// cannot be addressed through several distinct URLs.
	if base64.RawURLEncoding.EncodeToString(keyBytes) != encodedKey {
		return "", time.Time{}, fmt.Errorf("%w: non-canonical key encoding", ErrInvalidSignature)
	}
	if err := artworkstore.ValidateKey(key); err != nil {
		// Both sentinels matter: the caller answers on ErrInvalidSignature,
		// and the key error explains why in a log line.
		return "", time.Time{}, errors.Join(ErrInvalidSignature, err)
	}

	unix, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || unix <= 0 {
		return "", time.Time{}, fmt.Errorf("%w: unparsable expiry", ErrInvalidSignature)
	}

	expected := s.signature(key, unix)
	if len(expected) != len(signature) ||
		subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return "", time.Time{}, ErrInvalidSignature
	}

	expiresAt := time.Unix(unix, 0).UTC()
	if now.After(expiresAt) {
		return key, expiresAt, ErrExpired
	}
	return key, expiresAt, nil
}

// signature is the HMAC over the route version, the logical key, and the
// expiry. Every field a client could vary is covered, and the fields are
// NUL-separated so no two different triples share one signed message.
func (s *Signer) signature(key string, expiresUnix int64) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(routeVersion))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(key))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strconv.FormatInt(expiresUnix, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// quantizedExpiry returns the next TTL boundary after now, plus one TTL. Every
// mint within one window returns the same instant, so the whole URL is stable
// for at least one TTL and at most two.
func quantizedExpiry(now time.Time, ttl time.Duration) time.Time {
	step := int64(ttl / time.Second)
	if step <= 0 {
		step = int64(DefaultTTL / time.Second)
	}
	boundary := (now.Unix()/step + 1) * step
	return time.Unix(boundary+step, 0).UTC()
}
