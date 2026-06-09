package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ImagesHandler serves Jellyfin-compatible image routes.
type ImagesHandler struct {
	content      ContentService
	codec        *ResourceIDCodec
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
	httpClient   *http.Client
	// collections is optional; when set, BoxSet (library collection) artwork
	// resolves durably instead of depending on the in-memory image cache.
	collections collectionSource
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

// NewImagesHandler creates a Jellyfin-compatible image route handler.
func NewImagesHandler(content ContentService, codec *ResourceIDCodec, sessions *SessionStore, images *ImageCache, personRepo *catalog.PersonRepository, detailSvc *catalog.DetailService, itemRepo *catalog.ItemRepository, folderRepo *catalog.FolderRepository, seasonRepo *catalog.SeasonRepository, episodeRepo *catalog.EpisodeRepository, accessFilter AccessFilterResolver, posterSigner LibraryPosterPresigner, presignTTL time.Duration, imageTagSecret string, httpClient *http.Client) *ImagesHandler {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ImagesHandler{
		content:      content,
		codec:        codec,
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
		httpClient:   httpClient,
	}
}

// HandleItemImage serves item artwork through compat-owned routes.
func (h *ImagesHandler) HandleItemImage(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())

	routeID := chiURLParam(r, "id")
	imageType := chiURLParam(r, "imageType")
	imageSize := compatRequestImageSize(r, imageType)
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	if canonicalRouteID, ok := canonicalCompatImageRouteID(h.codec, routeID); ok {
		routeID = canonicalRouteID
		r = withCompatImageProxyRouteRequest(r)
	}
	if tag != "" {
		imageURL, ok, err := h.resolveItemImageURLFromTag(r.Context(), routeID, imageType, imageSize, tag)
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
		if ok {
			h.images.RememberSizedUntil(routeID, imageType, imageURL.URL, imageSize, imageURL.ExpiresAt)
			h.serveImageURL(w, r, imageURL.URL)
			return
		}
		if imageURL, ok := h.images.LookupTag(tag); ok {
			h.serveImageURL(w, r, imageURL)
			return
		}
	} else if imageURL, ok := h.images.LookupSized(routeID, imageType, "", imageSize); ok {
		h.serveImageURL(w, r, imageURL)
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

	if collectionID, err := h.codec.DecodeStringID(EncodedIDCollection, routeID); err == nil {
		h.handleCollectionImage(w, r, session, routeID, imageType, imageSize, collectionID)
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
	h.serveImageURL(w, r, imageURL)
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
	h.serveImageURL(w, r, imageURL)
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
	if collectionID, err := h.codec.DecodeStringID(EncodedIDCollection, routeID); err == nil {
		return h.resolveCollectionImageURLFromTag(ctx, routeID, collectionID, imageType, tag)
	}
	contentID, err := decodeContentID(h.codec, routeID)
	if err != nil {
		return catalog.ResolvedImageURL{}, false, nil
	}
	return h.resolveItemImageURLFromReposWithoutSession(ctx, routeID, contentID, imageType, imageSize, tag)
}

// collectionArtworkKey returns the stored artwork reference for the requested
// compat image type ("" when the collection has none of that type).
func collectionArtworkKey(c *models.LibraryCollection, imageType string) string {
	switch imageType {
	case "Primary":
		return c.PosterURL
	case "Backdrop":
		return c.BackdropURL
	}
	return ""
}

// presignCollectionArtwork resolves a collection artwork reference like the
// main API's presignGPURL: absolute and app-relative references pass through,
// bare keys presign against the general-purpose bucket.
func (h *ImagesHandler) presignCollectionArtwork(ctx context.Context, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "/") {
		return path
	}
	return h.presignLibraryPosterURL(ctx, path)
}

// resolveCollectionImageURLFromTag serves tag-authenticated BoxSet artwork.
// The tag must match the stable seed boxSetFromCollection signs.
func (h *ImagesHandler) resolveCollectionImageURLFromTag(ctx context.Context, routeID, collectionID, imageType, tag string) (catalog.ResolvedImageURL, bool, error) {
	if h.collections == nil {
		return catalog.ResolvedImageURL{}, false, nil
	}
	collection, err := h.collections.GetByID(ctx, collectionID)
	if err != nil || collection == nil {
		return catalog.ResolvedImageURL{}, false, nil
	}
	key := collectionArtworkKey(collection, imageType)
	if key == "" || !h.imageTags.Equal(
		imageTagSeed(routeID, imageType, compatCardImageSize, key, "", time.Time{}),
		"",
		tag,
	) {
		return catalog.ResolvedImageURL{}, false, nil
	}
	imageURL := h.presignCollectionArtwork(ctx, key)
	if imageURL == "" {
		return catalog.ResolvedImageURL{}, false, nil
	}
	return catalog.ResolvedImageURL{URL: imageURL}, true, nil
}

// handleCollectionImage serves session-authenticated BoxSet artwork, applying
// the same visibility rules as the BoxSet item endpoints.
func (h *ImagesHandler) handleCollectionImage(w http.ResponseWriter, r *http.Request, session *Session, routeID, imageType, imageSize, collectionID string) {
	if h.collections == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	collection, err := h.collections.GetByID(r.Context(), collectionID)
	if err != nil {
		if errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
			writeError(w, http.StatusNotFound, "NotFound", "Item not found")
			return
		}
		writeCompatUpstreamError(w, err)
		return
	}
	if collection == nil || !strings.EqualFold(collection.Visibility, "visible") {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	visible, err := visibleLibraryIDSet(r.Context(), h.content, session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if !collectionVisible(collection, visible) {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	imageURL := h.presignCollectionArtwork(r.Context(), collectionArtworkKey(collection, imageType))
	if imageURL == "" {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	h.images.RememberSized(routeID, imageType, imageURL, imageSize)
	h.serveImageURL(w, r, imageURL)
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

func (h *ImagesHandler) serveImageURL(w http.ResponseWriter, r *http.Request, imageURL string) {
	if shouldProxyCompatImageRequest(r) {
		h.proxyImageURL(w, r, imageURL)
		return
	}
	h.redirectImageURL(w, r, imageURL)
}

func (h *ImagesHandler) redirectImageURL(w http.ResponseWriter, r *http.Request, imageURL string) {
	if _, err := parseRemoteImageURL(imageURL); err != nil {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}

	// Do not let clients cache the temporary redirect itself. The object-store
	// response can still carry its own cache headers after the client follows it.
	setCompatImageRouteNoStore(w.Header())
	http.Redirect(w, r, imageURL, http.StatusFound)
}

func (h *ImagesHandler) proxyImageURL(w http.ResponseWriter, r *http.Request, imageURL string) {
	target, err := parseRemoteImageURL(imageURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}
	copyConditionalImageRequestHeaders(req.Header, r.Header)

	client := h.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "UpstreamError", "Failed to load image")
		return
	}
	proxyImage(w, resp)
}

func parseRemoteImageURL(imageURL string) (*url.URL, error) {
	target, err := url.Parse(imageURL)
	if err != nil || target.Scheme == "" || target.Host == "" ||
		(target.Scheme != "http" && target.Scheme != "https") {
		return nil, errors.New("invalid remote image URL")
	}
	return target, nil
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
