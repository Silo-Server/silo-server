package jellycompat

import (
	"context"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
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
	// frontendFS is optional; when set, app-relative artwork references (bundled
	// collection-template posters like "/images/collection-templates/x.jpg") are
	// served straight from the embedded frontend assets. Without it those paths
	// have no fetchable origin on the compat surface.
	frontendFS fs.FS
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

	// The synthetic Collections library tile and individual BoxSets resolve to
	// generated or bundled artwork that the URL-redirect path below cannot serve,
	// so they own a dedicated handler that authorizes via the signed tag or the
	// session and writes bytes directly.
	if isCollectionsViewID(routeID) {
		h.serveCollectionsViewImage(w, r, imageType, tag)
		return
	}
	if collectionID, err := h.codec.DecodeStringID(EncodedIDCollection, routeID); err == nil {
		h.serveCollectionImage(w, r, routeID, imageType, tag, collectionID)
		return
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
		// Jellyfin item/chapter image GETs are anonymous (200/404 only, never
		// 401): media players can't attach auth headers to <img> requests, and
		// absent or unsupported art (e.g. Chapter) must degrade to a clean 404.
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
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
	// Collection (BoxSet) and Collections-view artwork are intercepted earlier in
	// HandleItemImage by serveCollectionImage / serveCollectionsViewImage, so they
	// never reach this generic resolver.
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

// collectionImageTagSeed returns the tag seed boxSetFromCollection signs for the
// given collection and compat image type, plus whether that type is served at
// all. Primary always resolves (a generated poster backs collections without
// stored art); Backdrop only when a stored backdrop exists.
func collectionImageTagSeed(routeID, imageType string, c *models.LibraryCollection) (string, bool) {
	switch imageType {
	case "Primary":
		if key := strings.TrimSpace(c.PosterURL); key != "" {
			return imageTagSeed(routeID, "Primary", compatCardImageSize, key, "", time.Time{}), true
		}
		return imageTagSeed(routeID, "Primary", compatCardImageSize, generatedPosterSeed(c.Title), "", time.Time{}), true
	case "Backdrop":
		if key := strings.TrimSpace(c.BackdropURL); key != "" {
			return imageTagSeed(routeID, "Backdrop", compatCardImageSize, key, "", time.Time{}), true
		}
	}
	return "", false
}

// serveCollectionImage serves BoxSet artwork. It authorizes via the signed tag
// (a capability minted only for visible collections) or, when no tag is given,
// via an authenticated session whose libraries include the collection. Stored
// artwork is presigned/served as before; collections without a usable poster
// fall back to a generated gradient poster captioned with the title.
func (h *ImagesHandler) serveCollectionImage(w http.ResponseWriter, r *http.Request, routeID, imageType, tag, collectionID string) {
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

	seed, served := collectionImageTagSeed(routeID, imageType, collection)
	if !served {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}

	authorized := tag != "" && h.imageTags != nil && h.imageTags.Equal(seed, "", tag)
	if !authorized {
		ok, err := h.collectionVisibleToRequest(r, collection)
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "NotFound", "Item not found")
			return
		}
	}

	if key := collectionArtworkKey(collection, imageType); key != "" {
		if imageURL := h.presignCollectionArtwork(r.Context(), key); imageURL != "" {
			h.serveImageURL(w, r, imageURL)
			return
		}
	}

	if imageType != "Primary" {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	h.serveGeneratedPoster(w, collection.Title)
}

// collectionVisibleToRequest reports whether the request's session may see the
// collection. A missing session resolves to not-visible (anonymous image GETs
// without a valid tag get a clean 404).
func (h *ImagesHandler) collectionVisibleToRequest(r *http.Request, collection *models.LibraryCollection) (bool, error) {
	session := SessionFromContext(r.Context())
	if session == nil && h.sessions != nil {
		if token, ok := ExtractToken(r); ok {
			session, _ = h.sessions.Get(token)
		}
	}
	if session == nil {
		return false, nil
	}
	visible, err := visibleLibraryIDSet(r.Context(), h.content, session)
	if err != nil {
		return false, err
	}
	return collectionVisible(collection, visible), nil
}

// serveCollectionsViewImage serves the synthetic Collections library tile. It is
// always a generated "Collections" poster, authorized by the signed tag or any
// authenticated session.
func (h *ImagesHandler) serveCollectionsViewImage(w http.ResponseWriter, r *http.Request, imageType, tag string) {
	if imageType != "Primary" {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	seed := imageTagSeed(collectionsViewID, "Primary", compatCardImageSize, generatedPosterSeed(collectionsViewCaption), "", time.Time{})
	authorized := tag != "" && h.imageTags != nil && h.imageTags.Equal(seed, "", tag)
	if !authorized {
		session := SessionFromContext(r.Context())
		if session == nil && h.sessions != nil {
			if token, ok := ExtractToken(r); ok {
				session, _ = h.sessions.Get(token)
			}
		}
		if session == nil {
			writeError(w, http.StatusNotFound, "NotFound", "Image not found")
			return
		}
	}
	h.serveGeneratedPoster(w, collectionsViewCaption)
}

// serveGeneratedPoster renders (or reuses) a gradient poster captioned with text
// and writes it as a cacheable PNG.
func (h *ImagesHandler) serveGeneratedPoster(w http.ResponseWriter, caption string) {
	pngBytes, err := generatedCollectionPoster(caption)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", "Failed to render image")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pngBytes)
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
	// App-relative references (bundled collection-template posters) have no
	// remote origin to redirect or proxy to, so serve their bytes from the
	// embedded frontend assets instead.
	if strings.HasPrefix(imageURL, "/") {
		h.serveBundledAsset(w, imageURL)
		return
	}
	if shouldProxyCompatImageRequest(r) {
		h.proxyImageURL(w, r, imageURL)
		return
	}
	h.redirectImageURL(w, r, imageURL)
}

// serveBundledAsset serves an app-relative asset (e.g.
// "/images/collection-templates/x.jpg") straight from the embedded frontend
// filesystem. A missing FS or file degrades to a clean 404.
func (h *ImagesHandler) serveBundledAsset(w http.ResponseWriter, assetPath string) {
	if h.frontendFS == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	clean := path.Clean("/" + strings.TrimPrefix(assetPath, "/"))
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" || strings.HasPrefix(rel, "../") {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	data, err := fs.ReadFile(h.frontendFS, rel)
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	contentType := mime.TypeByExtension(path.Ext(rel))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
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
//
// Jellyfin user-image GETs are anonymous (200/404 only): clients fetch avatars
// via plain <img> tags that carry no auth. The response is selected from a
// precomputed palette by hashing the path pseudo-user id, so it stays
// deterministic per id without per-request PNG generation (which an anonymous
// varying-{id} flood would otherwise turn into a CPU DoS amplifier).
func (h *ImagesHandler) HandleUserImage(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	if id == "" {
		// The modern /UserImage route carries the id as a ?userId= query param
		// rather than a path segment. Fall back to it so each user still hashes
		// to a stable palette entry instead of every caller sharing the empty-id
		// avatar.
		id = firstNonEmpty(r.URL.Query().Get("userId"), r.URL.Query().Get("UserId"))
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(avatarPalette[avatarPaletteIndex(id)])
}
