package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ImageService defines the image-related methods on MetadataService
// needed by the admin image handler.
type ImageService interface {
	FetchItemImages(ctx context.Context, providerIDs map[string]string, contentType string, language string, folderID int) ([]metadata.RemoteImage, map[string]string, error)
	ApplyItemImage(ctx context.Context, req metadata.ApplyItemImageRequest) (*metadata.ApplyItemImageResult, error)
}

// ImageItemLookup loads media items, seasons, and episodes by content ID.
type ImageItemLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
}

// ImageSeasonLookup loads seasons by content ID.
type ImageSeasonLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.Season, error)
}

// ImageEpisodeLookup loads episodes by content ID.
type ImageEpisodeLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
}

// ImageURLResolver resolves image URLs for display (plugin-prefixed → HTTP).
type ImageURLResolver interface {
	ResolveImageURL(ctx context.Context, path string, variant string) string
	ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string
}

// AdminImageHandler handles endpoints for browsing and selecting item images.
type AdminImageHandler struct {
	items         ImageItemLookup
	seasons       ImageSeasonLookup
	episodes      ImageEpisodeLookup
	folders       MatchFolderLookup
	imageSvc      ImageService
	imageResolver ImageURLResolver
	detailSvc     *catalog.DetailService
	EventsHub     *evt.Hub
}

// NewAdminImageHandler creates a handler for admin image selection endpoints.
func NewAdminImageHandler(
	items ImageItemLookup,
	seasons ImageSeasonLookup,
	episodes ImageEpisodeLookup,
	folders MatchFolderLookup,
	imageSvc ImageService,
	imageResolver ImageURLResolver,
	detailSvc *catalog.DetailService,
) *AdminImageHandler {
	return &AdminImageHandler{
		items:         items,
		seasons:       seasons,
		episodes:      episodes,
		folders:       folders,
		imageSvc:      imageSvc,
		imageResolver: imageResolver,
		detailSvc:     detailSvc,
	}
}

// --- Request/Response types ---

type itemImageEntry struct {
	ProviderID  string  `json:"provider_id"`
	URL         string  `json:"url"`
	OriginalURL string  `json:"original_url"`
	Type        string  `json:"type"`
	Language    string  `json:"language"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Rating      float64 `json:"rating"`
}

type getItemImagesResponse struct {
	Images         []itemImageEntry  `json:"images"`
	Current        currentImages     `json:"current"`
	ProviderErrors map[string]string `json:"provider_errors,omitempty"`
}

type currentImages struct {
	PosterURL   string `json:"poster_url,omitempty"`
	BackdropURL string `json:"backdrop_url,omitempty"`
	LogoURL     string `json:"logo_url,omitempty"`
}

type applyItemImageRequest struct {
	OriginalURL string `json:"original_url"`
	Type        string `json:"type"`
	ProviderID  string `json:"provider_id"`
}

type applyItemImageResponse struct {
	ContentID  string `json:"content_id"`
	StoredPath string `json:"stored_path"`
	Thumbhash  string `json:"thumbhash"`
	ImageURL   string `json:"image_url,omitempty"`
	Revision   string `json:"revision,omitempty"`
}

// resolvedItem holds the result of looking up a content ID across all three tables.
type resolvedItem struct {
	contentType string // "movie", "series", "season", "episode"
	// The parent MediaItem (for movies/series it's the item itself;
	// for seasons/episodes it's the owning series).
	parentItem *models.MediaItem
	// The season/episode model, if applicable.
	season  *models.Season
	episode *models.Episode
}

// resolveContentID looks up a content ID in media_items, then seasons, then episodes.
// Returns the resolved item info including the parent MediaItem (for provider IDs).
func (h *AdminImageHandler) resolveContentID(ctx context.Context, contentID string) (*resolvedItem, error) {
	// Try media_items first.
	item, err := h.items.GetByID(ctx, contentID)
	if err == nil {
		return &resolvedItem{
			contentType: item.Type,
			parentItem:  item,
		}, nil
	}
	if !errors.Is(err, catalog.ErrItemNotFound) {
		return nil, err
	}

	// Try seasons.
	season, err := h.seasons.GetByID(ctx, contentID)
	if err == nil {
		// Load the parent series for provider IDs.
		parentItem, err := h.items.GetByID(ctx, season.SeriesID)
		if err != nil {
			return nil, err
		}
		return &resolvedItem{
			contentType: "season",
			parentItem:  parentItem,
			season:      season,
		}, nil
	}
	if !errors.Is(err, catalog.ErrSeasonNotFound) {
		return nil, err
	}

	// Try episodes.
	ep, err := h.episodes.GetByID(ctx, contentID)
	if err == nil {
		parentItem, err := h.items.GetByID(ctx, ep.SeriesID)
		if err != nil {
			return nil, err
		}
		return &resolvedItem{
			contentType: "episode",
			parentItem:  parentItem,
			episode:     ep,
		}, nil
	}
	if !errors.Is(err, catalog.ErrEpisodeNotFound) {
		return nil, err
	}

	return nil, catalog.ErrItemNotFound
}

// HandleGetItemImages handles GET /admin/items/{id}/images.
// It fetches available images from all enabled metadata providers.
func (h *AdminImageHandler) HandleGetItemImages(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	resolved, err := h.resolveContentID(r.Context(), contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		slog.ErrorContext(r.Context(), "admin images: resolve content ID failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve item")
		return
	}

	// Determine the folder ID for chain resolution.
	// For seasons/episodes, use the parent series content ID.
	lookupContentID := resolved.parentItem.ContentID
	folderID, err := h.resolveImageFolderID(r.Context(), lookupContentID)
	if err != nil {
		slog.ErrorContext(r.Context(), "admin images: resolve folder failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not determine library for item")
		return
	}

	// Build provider IDs map from the parent item.
	providerIDs := buildProviderIDs(resolved.parentItem)

	// Determine the language preference. Use the parent item's default
	// metadata language, or fall back to "en".
	language := resolved.parentItem.DefaultMetadataLanguage
	if language == "" {
		language = "en"
	}

	// Use the parent item's type for the plugin call (always "movie" or "series").
	images, providerErrors, err := h.imageSvc.FetchItemImages(
		r.Context(), providerIDs, resolved.parentItem.Type, language, folderID,
	)
	if err != nil {
		slog.ErrorContext(r.Context(), "admin images: fetch failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch images")
		return
	}

	// Batch-resolve plugin-prefixed URLs for display.
	rawPaths := make([]string, len(images))
	for i, img := range images {
		rawPaths[i] = img.URL
	}
	var resolvedURLs map[string]string
	if h.imageResolver != nil && len(rawPaths) > 0 {
		resolvedURLs = h.imageResolver.ResolveImageURLs(r.Context(), rawPaths, "card")
	}

	// Build the response entries.
	entries := make([]itemImageEntry, 0, len(images))
	for _, img := range images {
		displayURL := img.URL
		if resolved, ok := resolvedURLs[img.URL]; ok && resolved != "" {
			displayURL = resolved
		}
		entries = append(entries, itemImageEntry{
			ProviderID:  img.ProviderID,
			URL:         displayURL,
			OriginalURL: img.URL,
			Type:        metadata.ImageTypeToString(img.Type),
			Language:    img.Language,
			Width:       img.Width,
			Height:      img.Height,
			Rating:      img.Rating,
		})
	}

	// Include current image paths so the frontend can highlight them.
	current := currentImages{
		PosterURL:   resolved.parentItem.PosterPath,
		BackdropURL: resolved.parentItem.BackdropPath,
		LogoURL:     resolved.parentItem.LogoPath,
	}
	// For seasons, use the season's poster if it has one.
	if resolved.season != nil && resolved.season.PosterPath != "" {
		current.PosterURL = resolved.season.PosterPath
	}

	writeJSON(w, http.StatusOK, getItemImagesResponse{
		Images:         entries,
		Current:        current,
		ProviderErrors: providerErrors,
	})
}

// HandleApplyItemImage handles POST /admin/items/{id}/images/apply.
// It downloads the selected image, caches it to S3, and updates the item.
func (h *AdminImageHandler) HandleApplyItemImage(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	var req applyItemImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.OriginalURL == "" || req.Type == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "original_url and type are required")
		return
	}

	resolved, err := h.resolveContentID(r.Context(), contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		slog.ErrorContext(r.Context(), "admin images: resolve content ID failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve item")
		return
	}

	imageType := metadata.ImageTypeFromString(req.Type)
	// Episodes only have stills. Clients historically sent "poster" here (the
	// old flow silently dropped it), so coerce rather than reject.
	if resolved.contentType == "episode" {
		imageType = metadata.ImageStill
	}
	// Reject unsupported target/image combinations before spending a download
	// and an S3 upload on an image that can never be published.
	if err := catalog.ValidateArtworkSelectionTarget(resolved.contentType, metadata.ImageTypeToString(imageType)); err != nil {
		writeError(w, http.StatusBadRequest, "unsupported_image_type",
			fmt.Sprintf("%s items do not accept %s images", resolved.contentType, metadata.ImageTypeToString(imageType)))
		return
	}
	providerID := req.ProviderID
	if providerID == "" {
		providerID = primaryProvider(resolved.parentItem)
	}

	// Use the parent item's ContentID for S3 key construction. Season /
	// episode numbers (when present) scope the S3 key beneath the series
	// prefix so siblings do not collide.
	cacheContentID := findBestContentID(resolved.parentItem, providerID)
	var seasonNumber, episodeNumber *int
	switch resolved.contentType {
	case "season":
		if resolved.season != nil {
			n := resolved.season.SeasonNumber
			seasonNumber = &n
		}
	case "episode":
		if resolved.episode != nil {
			s := resolved.episode.SeasonNumber
			e := resolved.episode.EpisodeNumber
			seasonNumber = &s
			episodeNumber = &e
		}
	}

	result, err := h.imageSvc.ApplyItemImage(r.Context(), metadata.ApplyItemImageRequest{
		OriginalURL:   req.OriginalURL,
		ProviderID:    providerID,
		ContentType:   resolved.parentItem.Type,
		ContentID:     cacheContentID,
		ImageType:     imageType,
		SeasonNumber:  seasonNumber,
		EpisodeNumber: episodeNumber,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "admin images: apply failed", "component", "api", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to apply image")
		return
	}

	err = h.detailSvc.PublishArtworkSelection(r.Context(), catalog.ArtworkSelection{
		TargetType:      resolved.contentType,
		TargetContentID: contentID,
		ParentContentID: resolved.parentItem.ContentID,
		ImageType:       metadata.ImageTypeToString(imageType),
		StoredPath:      result.StoredPath,
		SourcePath:      req.OriginalURL,
		Thumbhash:       result.Thumbhash,
		LockField:       int(metadata.FieldImages),
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "admin images: persist failed", "component", "api", "content_id", contentID, "error", err)
		if cleanupErr := h.detailSvc.QueueArtworkRevisionGC(
			r.Context(), result.StoredPath, metadata.ImageTypeToString(imageType), time.Now().Add(time.Hour),
		); cleanupErr != nil {
			slog.ErrorContext(r.Context(), "admin images: failed to queue unpublished artwork cleanup", "component", "api",
				"content_id", contentID, "stored_path", result.StoredPath, "error", cleanupErr)
		}
		switch {
		case errors.Is(err, catalog.ErrItemNotFound),
			errors.Is(err, catalog.ErrSeasonNotFound),
			errors.Is(err, catalog.ErrEpisodeNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Item not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Image cached but failed to update item")
		}
		return
	}

	publishEventMetadataUpdate(r.Context(), h.EventsHub, 0, contentID)
	if resolved.parentItem.ContentID != contentID {
		publishEventMetadataUpdate(r.Context(), h.EventsHub, 0, resolved.parentItem.ContentID)
	}

	imageURL := ""
	if h.imageResolver != nil {
		imageURL = h.imageResolver.ResolveImageURL(r.Context(), result.StoredPath, "original")
	}

	writeJSON(w, http.StatusOK, applyItemImageResponse{
		ContentID:  contentID,
		StoredPath: result.StoredPath,
		Thumbhash:  result.Thumbhash,
		ImageURL:   imageURL,
		Revision:   result.Revision,
	})
}

// resolveImageFolderID finds the primary library folder for a content ID.
func (h *AdminImageHandler) resolveImageFolderID(ctx context.Context, contentID string) (int, error) {
	if h.folders == nil {
		return 0, nil
	}
	folderID, err := h.folders.GetFolderIDForItem(ctx, contentID)
	if err != nil {
		return 0, fmt.Errorf("resolving folder for %s: %w", contentID, err)
	}
	return folderID, nil
}

// buildProviderIDs extracts the provider IDs map from a MediaItem.
func buildProviderIDs(item *models.MediaItem) map[string]string {
	ids := make(map[string]string)
	if item.TmdbID != "" {
		ids["tmdb"] = item.TmdbID
	}
	if item.TvdbID != "" {
		ids["tvdb"] = item.TvdbID
	}
	if item.ImdbID != "" {
		ids["imdb"] = item.ImdbID
	}
	return ids
}

// primaryProvider returns the primary provider slug for a media item.
func primaryProvider(item *models.MediaItem) string {
	if item.TmdbID != "" {
		return "tmdb"
	}
	if item.TvdbID != "" {
		return "tvdb"
	}
	return ""
}

// findBestContentID returns the best provider-specific ID for S3 key construction.
func findBestContentID(item *models.MediaItem, providerID string) string {
	switch providerID {
	case "tmdb":
		if item.TmdbID != "" {
			return item.TmdbID
		}
	case "tvdb":
		if item.TvdbID != "" {
			return item.TvdbID
		}
	}
	if item.TmdbID != "" {
		return item.TmdbID
	}
	if item.TvdbID != "" {
		return item.TvdbID
	}
	return item.ContentID
}
