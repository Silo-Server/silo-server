package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ImagesHandler serves Jellyfin-compatible image routes.
type ImagesHandler struct {
	content      ContentService
	codec        *ResourceIDCodec
	httpClient   *http.Client
	sessions     *SessionStore
	images       *ImageCache
	personRepo   *catalog.PersonRepository
	detailSvc    *catalog.DetailService
	itemRepo     imageItemRepository
	folderRepo   imageFolderRepository
	seasonRepo   imageSeasonRepository
	episodeRepo  imageEpisodeRepository
	accessFilter AccessFilterResolver
	posterSigner LibraryPosterPresigner
	presignTTL   time.Duration
	imageTags    *imageTagSigner
}

type imageItemRepository interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
}

type imageSeasonRepository interface {
	GetByID(ctx context.Context, contentID string) (*models.Season, error)
}

type imageEpisodeRepository interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
}

type imageFolderRepository interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
}

// NewImagesHandler creates an image proxy handler.
func NewImagesHandler(content ContentService, codec *ResourceIDCodec, httpClient *http.Client, sessions *SessionStore, images *ImageCache, personRepo *catalog.PersonRepository, detailSvc *catalog.DetailService, itemRepo *catalog.ItemRepository, folderRepo *catalog.FolderRepository, seasonRepo *catalog.SeasonRepository, episodeRepo *catalog.EpisodeRepository, accessFilter AccessFilterResolver, posterSigner LibraryPosterPresigner, presignTTL time.Duration, imageTagSecret string) *ImagesHandler {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ImagesHandler{
		content:      content,
		codec:        codec,
		httpClient:   httpClient,
		sessions:     sessions,
		images:       images,
		personRepo:   personRepo,
		detailSvc:    detailSvc,
		itemRepo:     itemRepo,
		folderRepo:   folderRepo,
		seasonRepo:   seasonRepo,
		episodeRepo:  episodeRepo,
		accessFilter: accessFilter,
		posterSigner: posterSigner,
		presignTTL:   presignTTL,
		imageTags:    newImageTagSigner(imageTagSecret),
	}
}

// HandleItemImage serves item artwork through compat-owned routes.
func (h *ImagesHandler) HandleItemImage(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())

	routeID := chiURLParam(r, "id")
	imageType := chiURLParam(r, "imageType")
	imageSize := compatRequestImageSize(r, imageType)
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	if tag != "" {
		imageURL, ok, err := h.resolveItemImageURLFromTag(r.Context(), routeID, imageType, imageSize, tag)
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
		if ok {
			h.images.RememberSizedUntil(routeID, imageType, imageURL.URL, imageSize, imageURL.ExpiresAt)
			h.proxyImageURL(w, r, imageURL.URL)
			return
		}
		if imageURL, ok := h.images.LookupTag(tag); ok {
			h.proxyImageURL(w, r, imageURL)
			return
		}
	} else if imageURL, ok := h.images.LookupSized(routeID, imageType, "", imageSize); ok {
		h.proxyImageURL(w, r, imageURL)
		return
	}

	if session == nil && h.sessions != nil {
		if token, ok := ExtractToken(r); ok {
			session, _ = h.sessions.Get(token)
		}
	}
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	// Try decoding as a person ID first.
	if personID, err := h.codec.DecodeIntID(EncodedIDPerson, routeID); err == nil {
		h.handlePersonImage(w, r, routeID, imageType, personID)
		return
	}

	contentID, err := decodeContentID(h.codec, routeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	resolvedImage, err := h.resolveItemImageURL(r.Context(), session, contentID, imageType, r)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	imageURL := resolvedImage.URL
	if imageURL == "" {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	h.images.RememberSizedUntil(routeID, imageType, imageURL, imageSize, resolvedImage.ExpiresAt)
	h.proxyImageURL(w, r, imageURL)
}

// handlePersonImage serves person photo images.
func (h *ImagesHandler) handlePersonImage(w http.ResponseWriter, r *http.Request, routeID, imageType string, personID int64) {
	if imageType != "Primary" {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	if h.personRepo == nil || h.detailSvc == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	person, err := h.personRepo.Get(r.Context(), personID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Person not found")
		return
	}
	imageSize := compatRequestImageSize(r, imageType)
	resolvedImage := compatPresignImageWithExpiry(h.detailSvc, r.Context(), person.PhotoPath, "poster", imageSize)
	imageURL := resolvedImage.URL
	if imageURL == "" {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	h.images.RememberSizedUntil(routeID, imageType, imageURL, imageSize, resolvedImage.ExpiresAt)
	h.proxyImageURL(w, r, imageURL)
}

func (h *ImagesHandler) resolveItemImageURL(ctx context.Context, session *Session, contentID, imageType string, r *http.Request) (catalog.ResolvedImageURL, error) {
	if imageURL, ok, err := h.resolveItemImageURLFromRepos(ctx, session, contentID, imageType, r); ok || err != nil {
		return imageURL, err
	}

	detail, err := h.content.GetItemDetail(ctx, session, contentID, nil)
	if err != nil {
		return catalog.ResolvedImageURL{}, err
	}
	switch imageType {
	case "Primary":
		return catalog.ResolvedImageURL{URL: firstNonEmpty(detail.PosterURL, detail.BackdropURL)}, nil
	case "Backdrop", "Thumb":
		return catalog.ResolvedImageURL{URL: firstNonEmpty(detail.BackdropURL, detail.PosterURL)}, nil
	case "Logo":
		return catalog.ResolvedImageURL{URL: detail.LogoURL}, nil
	default:
		return catalog.ResolvedImageURL{}, &HTTPError{StatusCode: http.StatusNotFound, Message: "Image not found"}
	}
}

func (h *ImagesHandler) resolveItemImageURLFromRepos(ctx context.Context, session *Session, contentID, imageType string, r *http.Request) (catalog.ResolvedImageURL, bool, error) {
	access := catalog.AccessFilter{}
	if h.accessFilter != nil {
		access = h.accessFilter(ctx, session.StreamAppUserID, session.ProfileID)
	}
	imageSize := compatRequestImageSize(r, imageType)

	if h.itemRepo != nil {
		if item, err := h.itemRepo.GetByID(ctx, contentID); err == nil {
			if err := h.itemRepo.EnsureAccessible(ctx, item.ContentID, access); err != nil {
				return catalog.ResolvedImageURL{}, false, wrapCatalogError(err)
			}
			if imageURL := h.imageURLForItem(ctx, item.PosterPath, "poster", item.BackdropPath, item.LogoPath, imageType, imageSize); imageURL.URL != "" {
				return imageURL, true, nil
			}
		} else if !errors.Is(err, catalog.ErrItemNotFound) {
			return catalog.ResolvedImageURL{}, false, wrapCatalogError(err)
		}
	}

	if h.episodeRepo != nil && h.itemRepo != nil {
		if episode, err := h.episodeRepo.GetByID(ctx, contentID); err == nil {
			series, seriesErr := h.itemRepo.GetByID(ctx, episode.SeriesID)
			if seriesErr != nil {
				if !errors.Is(seriesErr, catalog.ErrItemNotFound) {
					return catalog.ResolvedImageURL{}, false, wrapCatalogError(seriesErr)
				}
			} else {
				if err := h.itemRepo.EnsureAccessible(ctx, series.ContentID, access); err != nil {
					return catalog.ResolvedImageURL{}, false, wrapCatalogError(err)
				}
				if imageURL := h.imageURLForItem(ctx, episode.StillPath, "still", series.BackdropPath, series.LogoPath, imageType, imageSize); imageURL.URL != "" {
					return imageURL, true, nil
				}
			}
		} else if !errors.Is(err, catalog.ErrEpisodeNotFound) {
			return catalog.ResolvedImageURL{}, false, wrapCatalogError(err)
		}
	}

	if h.seasonRepo != nil && h.itemRepo != nil {
		if season, err := h.seasonRepo.GetByID(ctx, contentID); err == nil {
			series, seriesErr := h.itemRepo.GetByID(ctx, season.SeriesID)
			if seriesErr != nil {
				if errors.Is(seriesErr, catalog.ErrItemNotFound) {
					return catalog.ResolvedImageURL{}, false, nil
				}
				return catalog.ResolvedImageURL{}, false, wrapCatalogError(seriesErr)
			}
			if err := h.itemRepo.EnsureAccessible(ctx, series.ContentID, access); err != nil {
				return catalog.ResolvedImageURL{}, false, wrapCatalogError(err)
			}
			if imageURL := h.imageURLForItem(ctx, season.PosterPath, "poster", series.BackdropPath, series.LogoPath, imageType, imageSize); imageURL.URL != "" {
				return imageURL, true, nil
			}
		} else if !errors.Is(err, catalog.ErrSeasonNotFound) {
			return catalog.ResolvedImageURL{}, false, wrapCatalogError(err)
		}
	}

	return catalog.ResolvedImageURL{}, false, nil
}

func (h *ImagesHandler) resolveItemImageURLFromTag(ctx context.Context, routeID, imageType, imageSize, tag string) (catalog.ResolvedImageURL, bool, error) {
	if h.imageTags == nil || tag == "" {
		return catalog.ResolvedImageURL{}, false, nil
	}
	if libraryID, err := h.codec.DecodeIntID(EncodedIDLibrary, routeID); err == nil {
		return h.resolveLibraryImageURLFromTag(ctx, routeID, int(libraryID), imageType, imageSize, tag)
	}
	contentID, err := decodeContentID(h.codec, routeID)
	if err != nil {
		return catalog.ResolvedImageURL{}, false, nil
	}
	return h.resolveItemImageURLFromReposWithoutSession(ctx, routeID, contentID, imageType, imageSize, tag)
}

func (h *ImagesHandler) resolveLibraryImageURLFromTag(ctx context.Context, routeID string, libraryID int, imageType, _ string, tag string) (catalog.ResolvedImageURL, bool, error) {
	if imageType != "Primary" || h.folderRepo == nil || h.posterSigner == nil {
		return catalog.ResolvedImageURL{}, false, nil
	}
	folder, err := h.folderRepo.GetByID(ctx, libraryID)
	if err != nil {
		return catalog.ResolvedImageURL{}, false, nil
	}
	if folder.PosterPath == "" || !h.imageTags.Equal(
		imageTagSeed(routeID, "Primary", compatCardImageSize, folder.PosterPath, "", time.Time{}),
		"",
		tag,
	) {
		return catalog.ResolvedImageURL{}, false, nil
	}
	imageURL := h.presignLibraryPosterURL(ctx, folder.PosterPath)
	if imageURL == "" {
		return catalog.ResolvedImageURL{}, false, nil
	}
	return catalog.ResolvedImageURL{URL: imageURL}, true, nil
}

func (h *ImagesHandler) presignLibraryPosterURL(ctx context.Context, posterPath string) string {
	if posterPath == "" || h.posterSigner == nil {
		return ""
	}
	ttl := h.presignTTL
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	imageURL, err := h.posterSigner.PresignGetURL(ctx, h.posterSigner.Bucket(), posterPath, ttl)
	if err != nil {
		return ""
	}
	return imageURL
}

func (h *ImagesHandler) resolveItemImageURLFromReposWithoutSession(ctx context.Context, routeID, contentID, imageType, imageSize, tag string) (catalog.ResolvedImageURL, bool, error) {
	if h.itemRepo != nil {
		if item, err := h.itemRepo.GetByID(ctx, contentID); err == nil {
			if imageURL := h.imageURLForItem(ctx, item.PosterPath, "poster", item.BackdropPath, item.LogoPath, imageType, imageSize); imageURL.URL != "" {
				if !h.signedImageTagMatches(routeID, contentID, imageType, tag, item.PosterPath, item.PosterThumbhash, item.BackdropPath, item.BackdropThumbhash, item.LogoPath, item.UpdatedAt, imageURL.URL) {
					return catalog.ResolvedImageURL{}, false, nil
				}
				return imageURL, true, nil
			}
		} else if !errors.Is(err, catalog.ErrItemNotFound) {
			return catalog.ResolvedImageURL{}, false, wrapCatalogError(err)
		}
	}

	if h.episodeRepo != nil && h.itemRepo != nil {
		if episode, err := h.episodeRepo.GetByID(ctx, contentID); err == nil {
			series, seriesErr := h.itemRepo.GetByID(ctx, episode.SeriesID)
			if seriesErr != nil {
				if !errors.Is(seriesErr, catalog.ErrItemNotFound) {
					return catalog.ResolvedImageURL{}, false, wrapCatalogError(seriesErr)
				}
			} else {
				if imageURL := h.imageURLForItem(ctx, episode.StillPath, "still", series.BackdropPath, series.LogoPath, imageType, imageSize); imageURL.URL != "" {
					if !h.signedImageTagMatches(routeID, contentID, imageType, tag, episode.StillPath, episode.StillThumbhash, series.BackdropPath, series.BackdropThumbhash, series.LogoPath, episode.UpdatedAt, imageURL.URL) {
						return catalog.ResolvedImageURL{}, false, nil
					}
					return imageURL, true, nil
				}
			}
		} else if !errors.Is(err, catalog.ErrEpisodeNotFound) {
			return catalog.ResolvedImageURL{}, false, wrapCatalogError(err)
		}
	}

	if h.seasonRepo != nil && h.itemRepo != nil {
		if season, err := h.seasonRepo.GetByID(ctx, contentID); err == nil {
			series, seriesErr := h.itemRepo.GetByID(ctx, season.SeriesID)
			if seriesErr != nil {
				if errors.Is(seriesErr, catalog.ErrItemNotFound) {
					return catalog.ResolvedImageURL{}, false, nil
				}
				return catalog.ResolvedImageURL{}, false, wrapCatalogError(seriesErr)
			}
			if imageURL := h.imageURLForItem(ctx, season.PosterPath, "poster", series.BackdropPath, series.LogoPath, imageType, imageSize); imageURL.URL != "" {
				if !h.signedImageTagMatches(routeID, contentID, imageType, tag, season.PosterPath, season.PosterThumbhash, series.BackdropPath, series.BackdropThumbhash, series.LogoPath, season.UpdatedAt, imageURL.URL) {
					return catalog.ResolvedImageURL{}, false, nil
				}
				return imageURL, true, nil
			}
		} else if !errors.Is(err, catalog.ErrSeasonNotFound) {
			return catalog.ResolvedImageURL{}, false, wrapCatalogError(err)
		}
	}

	return catalog.ResolvedImageURL{}, false, nil
}

func (h *ImagesHandler) signedImageTagMatches(routeID, contentID, imageType, tag, primaryPath, primaryThumbhash, backdropPath, backdropThumbhash, logoPath string, updatedAt time.Time, resolvedURL string) bool {
	var path, thumbhash, tagImageType string
	switch imageType {
	case "Primary":
		path = primaryPath
		thumbhash = primaryThumbhash
		tagImageType = "Primary"
	case "Backdrop", "Thumb":
		path = backdropPath
		thumbhash = backdropThumbhash
		tagImageType = "Backdrop"
	case "Logo":
		path = logoPath
		tagImageType = "Logo"
	default:
		return false
	}
	if path != "" && h.imageTags.Equal(
		imageTagSeed(contentID, tagImageType, compatCardImageSize, path, thumbhash, updatedAt),
		path,
		tag,
	) {
		return true
	}
	if resolvedURL == "" {
		return false
	}
	return h.imageTags.Equal(
		imageTagSeed(routeID, tagImageType, compatCardImageSize, resolvedURL, "", time.Time{}),
		resolvedURL,
		tag,
	)
}

func (h *ImagesHandler) imageURLForItem(ctx context.Context, primaryPath, primaryImageType, backdropPath, logoPath, imageType, size string) catalog.ResolvedImageURL {
	primaryURL := compatPresignImageWithExpiry(h.detailSvc, ctx, primaryPath, primaryImageType, size)
	backdropURL := compatPresignImageWithExpiry(h.detailSvc, ctx, backdropPath, "backdrop", size)
	logoURL := compatPresignImageWithExpiry(h.detailSvc, ctx, logoPath, "logo", size)

	switch imageType {
	case "Primary":
		return firstResolvedImageURL(primaryURL, backdropURL)
	case "Backdrop", "Thumb":
		return firstResolvedImageURL(backdropURL, primaryURL)
	case "Logo":
		return logoURL
	default:
		return catalog.ResolvedImageURL{}
	}
}

func firstResolvedImageURL(values ...catalog.ResolvedImageURL) catalog.ResolvedImageURL {
	for _, value := range values {
		if value.URL != "" {
			return value
		}
	}
	return catalog.ResolvedImageURL{}
}

func (h *ImagesHandler) proxyImageURL(w http.ResponseWriter, r *http.Request, imageURL string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, imageURL, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}
	for _, header := range []string{"If-None-Match", "If-Modified-Since"} {
		if value := r.Header.Get(header); value != "" {
			req.Header.Set(header, value)
		}
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}
	proxyImage(w, resp)
}

// HandleUserImage returns a deterministic placeholder avatar.
func (h *ImagesHandler) HandleUserImage(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	if !validatePseudoUser(w, chiURLParam(r, "id"), session) {
		return
	}

	data, err := placeholderAvatarPNG(fmt.Sprintf("%s:%s", session.Username, session.ProfileID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ServerError", "Failed to generate avatar")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
