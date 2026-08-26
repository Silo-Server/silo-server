package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	textlessPosterMovieType        = "movie"
	textlessPosterSeriesType       = "series"
	textlessPosterCacheSize        = 512
	textlessPosterCacheTTL         = 6 * time.Hour
	textlessPosterNegativeCacheTTL = 15 * time.Minute
	textlessPosterFetchTimeout     = 20 * time.Second
)

// TextlessPosterItemLookup loads an item and applies the canonical viewer
// access filter before any remote provider work is allowed to start.
type TextlessPosterItemLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
}

// TextlessPosterImageService is the read-only provider operation needed by the
// mobile hero artwork endpoint.
type TextlessPosterImageService interface {
	FetchItemImages(ctx context.Context, providerIDs map[string]string, contentType string, language string, folderID int) ([]metadata.RemoteImage, map[string]string, error)
}

type textlessPosterAccessFilter func(http.ResponseWriter, *http.Request) (catalog.AccessFilter, bool)

type textlessPosterResponse struct {
	PosterURL string `json:"poster_url,omitempty"`
}

type textlessPosterCacheEntry struct {
	path      string
	expiresAt time.Time
	lastUsed  time.Time
}

// textlessPosterCache is bounded and expiry-on-access, so the handler needs no
// background sweeper or lifecycle goroutine.
type textlessPosterCache struct {
	mu      sync.Mutex
	entries map[string]textlessPosterCacheEntry
	maxSize int
}

func newTextlessPosterCache(maxSize int) *textlessPosterCache {
	return &textlessPosterCache{
		entries: make(map[string]textlessPosterCacheEntry),
		maxSize: maxSize,
	}
}

func (c *textlessPosterCache) get(key string) (string, bool) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if !now.Before(entry.expiresAt) {
		delete(c.entries, key)
		return "", false
	}
	entry.lastUsed = now
	c.entries[key] = entry
	return entry.path, true
}

func (c *textlessPosterCache) set(key, path string, ttl time.Duration) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	for existingKey, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, existingKey)
		}
	}
	if c.maxSize > 0 && len(c.entries) >= c.maxSize {
		var oldestKey string
		var oldestTime time.Time
		for existingKey, entry := range c.entries {
			if oldestKey == "" || entry.lastUsed.Before(oldestTime) {
				oldestKey = existingKey
				oldestTime = entry.lastUsed
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = textlessPosterCacheEntry{
		path:      path,
		expiresAt: now.Add(ttl),
		lastUsed:  now,
	}
}

// TextlessPosterHandler returns a provider-backed portrait poster without
// title text for the mobile featured-card presentation.
type TextlessPosterHandler struct {
	accessFilter  textlessPosterAccessFilter
	items         TextlessPosterItemLookup
	folders       MatchFolderLookup
	images        TextlessPosterImageService
	imageResolver ImageURLResolver
	cache         *textlessPosterCache
	flight        singleflight.Group
}

// NewTextlessPosterHandler wires the viewer-scoped textless poster endpoint.
func NewTextlessPosterHandler(
	itemsHandler *ItemsHandler,
	items TextlessPosterItemLookup,
	folders MatchFolderLookup,
	images TextlessPosterImageService,
	imageResolver ImageURLResolver,
) *TextlessPosterHandler {
	return &TextlessPosterHandler{
		accessFilter:  itemsHandler.accessFilterOrError,
		items:         items,
		folders:       folders,
		images:        images,
		imageResolver: imageResolver,
		cache:         newTextlessPosterCache(textlessPosterCacheSize),
	}
}

// HandleGet returns GET /api/v1/catalog/items/{id}/images/textless-poster.
func (h *TextlessPosterHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	contentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	if h == nil || h.accessFilter == nil || h.items == nil || h.images == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Textless posters are not configured")
		return
	}

	filter, ok := h.accessFilter(w, r)
	if !ok {
		return
	}
	if err := h.items.EnsureAccessible(r.Context(), contentID, filter); err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		slog.ErrorContext(r.Context(), "textless poster: access check failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve item access")
		return
	}

	item, err := h.items.GetByID(r.Context(), contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		slog.ErrorContext(r.Context(), "textless poster: item lookup failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load item")
		return
	}
	if item.Type != textlessPosterMovieType && item.Type != textlessPosterSeriesType {
		writeError(w, http.StatusBadRequest, "unsupported_type", "Textless posters are available for movies and series")
		return
	}

	folderID := 0
	if h.folders != nil {
		folderID, err = h.folders.GetFolderIDForItem(r.Context(), contentID)
		if err != nil {
			slog.ErrorContext(r.Context(), "textless poster: folder lookup failed", "component", "api", "content_id", contentID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not determine library for item")
			return
		}
	}

	providerIDs := buildProviderIDs(item)
	if len(providerIDs) == 0 {
		h.writeResponse(w, r, "")
		return
	}
	language := strings.TrimSpace(item.DefaultMetadataLanguage)
	if language == "" {
		language = "en"
	}
	cacheKey := textlessPosterCacheKey(item, folderID, language)
	path, err := h.textlessPosterPath(r.Context(), cacheKey, providerIDs, item.Type, language, folderID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		slog.ErrorContext(r.Context(), "textless poster: provider fetch failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch textless poster")
		return
	}
	h.writeResponse(w, r, path)
}

func (h *TextlessPosterHandler) textlessPosterPath(
	ctx context.Context,
	cacheKey string,
	providerIDs map[string]string,
	contentType string,
	language string,
	folderID int,
) (string, error) {
	if path, ok := h.cache.get(cacheKey); ok {
		return path, nil
	}

	result := h.flight.DoChan(cacheKey, func() (any, error) {
		if path, ok := h.cache.get(cacheKey); ok {
			return path, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), textlessPosterFetchTimeout)
		defer cancel()
		images, _, err := h.images.FetchItemImages(fetchCtx, providerIDs, contentType, language, folderID)
		if err != nil {
			return "", err
		}
		path := selectTextlessPoster(images)
		ttl := textlessPosterCacheTTL
		if path == "" {
			ttl = textlessPosterNegativeCacheTTL
		}
		h.cache.set(cacheKey, path, ttl)
		return path, nil
	})

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case fetched := <-result:
		if fetched.Err != nil {
			return "", fetched.Err
		}
		path, _ := fetched.Val.(string)
		return path, nil
	}
}

func (h *TextlessPosterHandler) writeResponse(w http.ResponseWriter, r *http.Request, path string) {
	posterURL := strings.TrimSpace(path)
	if posterURL != "" && h.imageResolver != nil {
		if resolved := strings.TrimSpace(h.imageResolver.ResolveImageURL(r.Context(), posterURL, "featured")); resolved != "" {
			posterURL = resolved
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=900")
	writeJSON(w, http.StatusOK, textlessPosterResponse{PosterURL: posterURL})
}

func textlessPosterCacheKey(item *models.MediaItem, folderID int, language string) string {
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%d|%s",
		item.ContentID,
		item.Type,
		item.TmdbID,
		item.TvdbID,
		item.ImdbID,
		folderID,
		language,
	)
}

// selectTextlessPoster first trusts an explicit provider IncludesText=false
// signal. Providers that do not expose the flag may fall back to their
// language-neutral poster, which is the established textless convention.
func selectTextlessPoster(images []metadata.RemoteImage) string {
	if path := highestRatedTextlessPoster(images, true); path != "" {
		return path
	}
	return highestRatedTextlessPoster(images, false)
}

func highestRatedTextlessPoster(images []metadata.RemoteImage, explicit bool) string {
	bestPath := ""
	bestRating := -1.0
	for _, image := range images {
		if image.Type != metadata.ImagePoster || strings.TrimSpace(image.URL) == "" {
			continue
		}
		if image.Width > 0 && image.Height > 0 && image.Height <= image.Width {
			continue
		}
		if explicit {
			if image.IncludesText == nil || *image.IncludesText {
				continue
			}
		} else if image.IncludesText != nil || strings.TrimSpace(image.Language) != "" {
			continue
		}
		if bestPath == "" || image.Rating > bestRating {
			bestPath = image.URL
			bestRating = image.Rating
		}
	}
	return bestPath
}
