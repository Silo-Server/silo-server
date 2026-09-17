package artworkurl

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

var (
	ErrExpired      = errors.New("artwork URL expired")
	ErrBadSignature = errors.New("artwork URL signature invalid")
)

// Capability domains. Each names a distinct kind of signed URL: a key derived
// from one domain cannot verify a URL minted under another, so an artwork
// capability can never be replayed as a job-artifact download.
const (
	artworkDomain     = "silo-artwork-url-v1"
	artworkLabel      = "artwork-v1"
	artworkRoute      = "/api/v2/artwork/"
	jobArtifactDomain = "silo-job-artifact-url-v1"
	jobArtifactLabel  = "job-artifact-v1"
	jobArtifactRoute  = "/api/v2/admin/jobs/"
)

type Signer struct {
	key   []byte
	label string
	route string
	ttl   time.Duration
}

func NewSigner(jwtSecret string, ttl time.Duration) *Signer {
	return newSigner(jwtSecret, artworkDomain, artworkLabel, artworkRoute, ttl)
}

// NewJobArtifactSigner signs admin job artifact downloads. A presigned S3 URL
// authorizes itself, so its replacement must too: the browser opens the URL in
// a new tab and sends no Authorization header. The route is
// "/api/v2/admin/jobs/<id>/artifact", so the signed key is the job ID.
func NewJobArtifactSigner(jwtSecret string, ttl time.Duration) *Signer {
	return newSigner(jwtSecret, jobArtifactDomain, jobArtifactLabel, jobArtifactRoute, ttl)
}

func newSigner(jwtSecret, domain, label, route string, ttl time.Duration) *Signer {
	ttl = clampTTL(ttl, 4*time.Hour)
	h := hmac.New(sha256.New, []byte(jwtSecret))
	_, _ = h.Write([]byte(domain))
	return &Signer{key: h.Sum(nil), label: label, route: route, ttl: ttl}
}

// clampTTL bounds a URL lifetime to [1m, 24h], substituting fallback for a
// non-positive value.
func clampTTL(ttl, fallback time.Duration) time.Duration {
	if ttl <= 0 {
		ttl = fallback
	}
	return max(time.Minute, min(ttl, 24*time.Hour))
}

func (s *Signer) signature(key string, exp int64) string {
	h := hmac.New(sha256.New, s.key)
	_, _ = fmt.Fprintf(h, "%s\n%s\n%d", s.label, key, exp)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16])
}
func (s *Signer) Sign(key string, now time.Time) (string, time.Time) {
	return s.SignFor(key, now, s.ttl)
}

// SignFor signs key with a caller-chosen lifetime instead of the signer's
// default, clamped to the same bounds. Capabilities that were presigned for a
// short window under S3, such as avatars and chapter thumbnails, keep that
// window under local delivery. A non-positive ttl means the default.
func (s *Signer) SignFor(key string, now time.Time, ttl time.Duration) (string, time.Time) {
	ttl = clampTTL(ttl, s.ttl)
	// Keep URLs stable within an issuance bucket and valid for at least ttl.
	// Short TTLs use shorter buckets, bounding the extra lifetime to ttl.
	bucket := min(15*time.Minute, ttl)
	expires := now.Truncate(bucket).Add(bucket + ttl)
	exp := expires.Unix()
	route := &url.URL{Path: s.route + strings.TrimPrefix(key, "/") + s.suffix()}
	return route.EscapedPath() + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + s.signature(key, exp), expires
}

// suffix completes a route whose signed key sits in the middle of the path
// rather than at the end. A job artifact lives at ".../jobs/<id>/artifact".
func (s *Signer) suffix() string {
	if s.label == jobArtifactLabel {
		return "/artifact"
	}
	return ""
}
func (s *Signer) Verify(key string, exp int64, sig string, now time.Time) error {
	if now.Unix() >= exp {
		return ErrExpired
	}
	expected := s.signature(key, exp)
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return ErrBadSignature
	}
	return nil
}

type Resolver interface {
	ResolveURLs(context.Context, []string) map[string]catalog.ResolvedImageURL
}

// TTLResolver resolves one key with a caller-chosen lifetime. Both resolvers
// implement it so a short-lived capability keeps its window on either backend.
type TTLResolver interface {
	Resolver
	ResolveURLFor(context.Context, string, time.Duration) (catalog.ResolvedImageURL, bool)
}

// ResolveURLFor resolves key through resolver with ttl when the resolver
// supports lifetimes, and with the resolver's default otherwise. The bool
// reports whether a URL was produced.
func ResolveURLFor(ctx context.Context, resolver Resolver, key string, ttl time.Duration) (catalog.ResolvedImageURL, bool) {
	if resolver == nil {
		return catalog.ResolvedImageURL{}, false
	}
	if ttlResolver, ok := resolver.(TTLResolver); ok {
		return ttlResolver.ResolveURLFor(ctx, key, ttl)
	}
	resolved, ok := resolver.ResolveURLs(ctx, []string{key})[key]
	return resolved, ok && resolved.URL != ""
}

type ServerResolver struct{ signer *Signer }

func NewServerResolver(signer *Signer) Resolver { return ServerResolver{signer: signer} }
func (r ServerResolver) ResolveURLs(ctx context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := make(map[string]catalog.ResolvedImageURL, len(keys))
	for _, key := range keys {
		if ctx.Err() != nil {
			break
		}
		url, exp := r.signer.Sign(key, time.Now())
		out[key] = catalog.ResolvedImageURL{URL: url, ExpiresAt: &exp}
	}
	return out
}
func (r ServerResolver) ResolveURLFor(_ context.Context, key string, ttl time.Duration) (catalog.ResolvedImageURL, bool) {
	url, exp := r.signer.SignFor(key, time.Now(), ttl)
	return catalog.ResolvedImageURL{URL: url, ExpiresAt: &exp}, true
}

type directResolver struct {
	direct blobstore.DirectURLer
	ttl    time.Duration
}

func NewDirectResolver(direct blobstore.DirectURLer, ttl time.Duration) Resolver {
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	return directResolver{direct: direct, ttl: ttl}
}
func (r directResolver) ResolveURLFor(ctx context.Context, key string, ttl time.Duration) (catalog.ResolvedImageURL, bool) {
	if ttl <= 0 {
		ttl = r.ttl
	}
	url, err := r.direct.DirectURL(ctx, key, ttl)
	if err != nil || url == "" {
		return catalog.ResolvedImageURL{}, false
	}
	expiry := time.Now().Add(ttl)
	return catalog.ResolvedImageURL{URL: url, ExpiresAt: &expiry}, true
}
func (r directResolver) ResolveURLs(ctx context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := make(map[string]catalog.ResolvedImageURL, len(keys))
	for _, key := range keys {
		url, err := r.direct.DirectURL(ctx, key, r.ttl)
		if err == nil {
			expiry := time.Now().Add(r.ttl)
			out[key] = catalog.ResolvedImageURL{URL: url, ExpiresAt: &expiry}
		}
	}
	return out
}
