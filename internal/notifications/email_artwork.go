package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// Email artwork kinds. A backdrop is dimmed toward the email canvas so the
// title and synopsis drawn over it stay readable in clients that ignore CSS
// overlays; a wide backdrop, a poster and an episode still are re-encoded as
// they are.
const (
	EmailArtworkBackdrop = "backdrop"
	EmailArtworkWide     = "wide"
	EmailArtworkPoster   = "poster"
	EmailArtworkStill    = "still"
)

const (
	emailArtworkDomain       = "silo-email-artwork-v1"
	emailArtworkRoute        = "/api/v2/notifications/email/artwork/"
	emailBackdropWidth       = 1280
	emailPosterWidth         = 342
	emailStillWidth          = 640
	emailBackdropDim         = 0.62
	emailArtworkCacheEntries = 256
	emailArtworkCacheTTL     = 6 * time.Hour
	emailArtworkMaxBytes     = 25 << 20
)

// ErrEmailArtworkNotFound means the token is invalid or names no artwork.
var ErrEmailArtworkNotFound = errors.New("email artwork not found")

// emailArtworkTint is the email canvas colour (mail.EmailColorCanvas).
var emailArtworkTint = color.RGBA{R: 0x14, G: 0x14, B: 0x17, A: 0xff}

// EmailArtwork serves catalog artwork to notification emails. An email is
// read days after it is sent, in a client with no Silo session, so its image
// links cannot use the expiring artwork capabilities. Each link instead
// carries a non-expiring HMAC over (kind, content id). The link reveals one
// title's poster or backdrop and nothing else; it does not grant any other
// artwork, and rotating the JWT secret revokes every link.
type EmailArtwork struct {
	pool   *pgxpool.Pool
	store  blobstore.Store
	key    []byte
	client *http.Client

	mu    sync.Mutex
	cache map[string]emailArtworkEntry
	// backdropStyle, when set, gives the tint and dim (0-1) of dimmed
	// backdrops; nil uses the email canvas and emailBackdropDim.
	backdropStyle func(context.Context) (color.RGBA, float64)
}

type emailArtworkEntry struct {
	data    []byte
	expires time.Time
}

// NewEmailArtwork creates the email artwork service. store may be nil, in
// which case only provider-hosted artwork can be served.
func NewEmailArtwork(pool *pgxpool.Pool, store blobstore.Store, jwtSecret string) *EmailArtwork {
	mac := hmac.New(sha256.New, []byte(jwtSecret))
	_, _ = mac.Write([]byte(emailArtworkDomain))
	return &EmailArtwork{
		pool:   pool,
		store:  store,
		key:    mac.Sum(nil),
		client: &http.Client{Timeout: 15 * time.Second},
		cache:  make(map[string]emailArtworkEntry),
	}
}

func (a *EmailArtwork) signature(kind, contentID string) string {
	mac := hmac.New(sha256.New, a.key)
	_, _ = fmt.Fprintf(mac, "%s\n%s", kind, contentID)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// Token returns the signed path segment for one title's artwork.
func (a *EmailArtwork) Token(kind, contentID string) string {
	subject := base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + contentID))
	return subject + "." + a.signature(kind, contentID)
}

// URL returns the absolute artwork link for baseURL, or "" without one.
func (a *EmailArtwork) URL(baseURL, kind, contentID string) string {
	if a == nil || baseURL == "" || contentID == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + emailArtworkRoute + a.Token(kind, contentID) + ".jpg"
}

// parseToken verifies a token and returns its kind and content id.
func (a *EmailArtwork) parseToken(token string) (kind, contentID string, ok bool) {
	token = strings.TrimSuffix(token, ".jpg")
	subject, sig, found := strings.Cut(token, ".")
	if !found {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(subject)
	if err != nil {
		return "", "", false
	}
	kind, contentID, found = strings.Cut(string(raw), ":")
	if !found || contentID == "" || !validEmailArtworkKind(kind) {
		return "", "", false
	}
	if !hmac.Equal([]byte(sig), []byte(a.signature(kind, contentID))) {
		return "", "", false
	}
	return kind, contentID, true
}

// Serve returns the JPEG bytes for a token.
func (a *EmailArtwork) Serve(ctx context.Context, token string) ([]byte, error) {
	if a == nil {
		return nil, ErrEmailArtworkNotFound
	}
	kind, contentID, ok := a.parseToken(token)
	if !ok {
		return nil, ErrEmailArtworkNotFound
	}
	tint, dim := emailArtworkTint, emailBackdropDim
	if kind == EmailArtworkBackdrop && a.backdropStyle != nil {
		tint, dim = a.backdropStyle(ctx)
	}
	cacheKey := kind + ":" + contentID
	if kind == EmailArtworkBackdrop {
		cacheKey += fmt.Sprintf(":%02x%02x%02x:%.2f", tint.R, tint.G, tint.B, dim)
	}
	now := time.Now()
	a.mu.Lock()
	if entry, hit := a.cache[cacheKey]; hit && now.Before(entry.expires) {
		a.mu.Unlock()
		return entry.data, nil
	}
	a.mu.Unlock()

	path, source, err := a.artworkPaths(ctx, kind, contentID)
	if err != nil {
		return nil, err
	}
	raw, err := a.readArtwork(ctx, path, source)
	if err != nil {
		return nil, err
	}
	var out []byte
	switch kind {
	case EmailArtworkBackdrop:
		out, err = imageutil.EncodeEmailJPEG(raw, emailBackdropWidth, tint, dim)
	case EmailArtworkWide:
		out, err = imageutil.EncodeEmailJPEG(raw, emailBackdropWidth, emailArtworkTint, 0)
	case EmailArtworkStill:
		out, err = imageutil.EncodeEmailJPEG(raw, emailStillWidth, emailArtworkTint, 0)
	default:
		out, err = imageutil.EncodeEmailJPEG(raw, emailPosterWidth, emailArtworkTint, 0)
	}
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	if len(a.cache) >= emailArtworkCacheEntries {
		for key, entry := range a.cache {
			if len(a.cache) < emailArtworkCacheEntries && now.Before(entry.expires) {
				break
			}
			delete(a.cache, key)
		}
	}
	a.cache[cacheKey] = emailArtworkEntry{data: out, expires: now.Add(emailArtworkCacheTTL)}
	a.mu.Unlock()
	return out, nil
}

func validEmailArtworkKind(kind string) bool {
	switch kind {
	case EmailArtworkBackdrop, EmailArtworkWide, EmailArtworkPoster, EmailArtworkStill:
		return true
	}
	return false
}

// SetBackdropStyle makes dimmed backdrops follow the email theme.
func (a *EmailArtwork) SetBackdropStyle(style func(context.Context) (color.RGBA, float64)) {
	if a != nil {
		a.backdropStyle = style
	}
}

// artworkPaths reads the stored artwork key and its provider source for one
// title. A still is an episode's own; every other kind is a movie's or
// series'.
func (a *EmailArtwork) artworkPaths(ctx context.Context, kind, contentID string) (path, source string, err error) {
	table, column, sourceColumn := "media_items", "backdrop_path", "backdrop_source_path"
	switch kind {
	case EmailArtworkPoster:
		column, sourceColumn = "poster_path", "poster_source_path"
	case EmailArtworkStill:
		table, column, sourceColumn = "episodes", "still_path", "still_source_path"
	}
	err = a.pool.QueryRow(ctx, `
		SELECT COALESCE(`+column+`, ''), COALESCE(`+sourceColumn+`, '')
		FROM `+table+` WHERE content_id = $1`, contentID).Scan(&path, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrEmailArtworkNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("load email artwork path: %w", err)
	}
	if path == "" && source == "" {
		return "", "", ErrEmailArtworkNotFound
	}
	return path, source, nil
}

// readArtwork reads the stored artwork from blob storage, falling back to the
// public provider CDN when the path is provider-origin or storage misses it.
func (a *EmailArtwork) readArtwork(ctx context.Context, path, source string) ([]byte, error) {
	if a.store != nil && path != "" && blobstore.ValidateKey(path) == nil {
		reader, _, err := a.store.Get(ctx, path)
		if err == nil {
			defer func() { _ = reader.Close() }()
			return io.ReadAll(io.LimitReader(reader, emailArtworkMaxBytes))
		}
		if !errors.Is(err, blobstore.ErrNotFound) {
			return nil, fmt.Errorf("read email artwork: %w", err)
		}
	}
	url := embedPosterURL(path, source)
	if !emailArtworkProviderURL(url) {
		return nil, ErrEmailArtworkNotFound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch email artwork: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrEmailArtworkNotFound
	}
	return io.ReadAll(io.LimitReader(resp.Body, emailArtworkMaxBytes))
}

// emailArtworkProviderURL limits the CDN fallback to the metadata providers'
// image hosts, so a stored path can never make the server fetch an arbitrary
// URL.
func emailArtworkProviderURL(url string) bool {
	return strings.HasPrefix(url, "https://image.tmdb.org/") ||
		strings.HasPrefix(url, "https://artworks.thetvdb.com/")
}
