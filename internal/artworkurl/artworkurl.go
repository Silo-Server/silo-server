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

	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

var (
	ErrExpired      = errors.New("artwork URL expired")
	ErrBadSignature = errors.New("artwork URL signature invalid")
)

type Signer struct {
	key []byte
	ttl time.Duration
}

func NewSigner(jwtSecret string, ttl time.Duration) *Signer {
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	if ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	h := hmac.New(sha256.New, []byte(jwtSecret))
	_, _ = h.Write([]byte("silo-artwork-url-v1"))
	return &Signer{key: h.Sum(nil), ttl: ttl}
}
func (s *Signer) signature(key string, exp int64) string {
	h := hmac.New(sha256.New, s.key)
	_, _ = fmt.Fprintf(h, "artwork-v1\n%s\n%d", key, exp)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16])
}
func (s *Signer) Sign(key string, now time.Time) (string, time.Time) {
	// Keep URLs stable within an issuance bucket and valid for at least ttl.
	// Short TTLs use shorter buckets, bounding the extra lifetime to ttl.
	bucket := min(15*time.Minute, s.ttl)
	expires := now.Truncate(bucket).Add(bucket + s.ttl)
	exp := expires.Unix()
	route := &url.URL{Path: "/api/v2/artwork/" + strings.TrimPrefix(key, "/")}
	return route.EscapedPath() + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + s.signature(key, exp), expires
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

type directResolver struct {
	direct artworkstore.DirectURLer
	ttl    time.Duration
}

func NewDirectResolver(direct artworkstore.DirectURLer, ttl time.Duration) Resolver {
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	return directResolver{direct: direct, ttl: ttl}
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
