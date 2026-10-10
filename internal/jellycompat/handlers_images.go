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

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

// ImagesHandler serves Jellyfin-compatible image routes.
const compatImagePrimary = "Primary"

type ImagesHandler struct {
	content      ContentService
	codec        *ResourceIDCodec
	sessions     *SessionStore
	keyAuth      *AdminAPIKeyAuthenticator
	images       *ImageCache
	personRepo   imagePersonRepository
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
	// collectionPosters is optional; when set, BoxSet artwork includes each
	// viewer's collage (see ItemsHandler.collectionPosters).
	collectionPosters CollectionPosterResolver
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

type imagePersonRepository interface {
	Get(ctx context.Context, id int64) (*models.Person, error)
	EnsureAccessible(ctx context.Context, id int64, filter catalog.AccessFilter) error
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
	// A nil concrete repository has to stay nil in the interface field, or the
	// nil checks guarding the person route would see a non-nil interface holding
	// a nil pointer and dereference it.
	var persons imagePersonRepository
	if personRepo != nil {
		persons = personRepo
	}
	return &ImagesHandler{
		content:      content,
		codec:        codec,
		sessions:     sessions,
		images:       images,
		personRepo:   persons,
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
	tag := compatImageRequestTag(r)
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

	// Person IDs, unsigned tags, and the process-wide artwork cache are public
	// identifiers, not authority. Headshots require a signed tag (minted only
	// where a visible credit was served) or a session with a visible credit.
	if personID, err := h.codec.DecodeIntID(EncodedIDPerson, routeID); err == nil {
		if session == nil {
			if token, ok := ExtractToken(r); ok {
				session, _, _ = resolveCompatToken(r.Context(), h.sessions, h.keyAuth, token)
			}
		}
		h.handlePersonImage(w, r, session, routeID, imageType, tag, personID)
		return
	}

	if tag != "" {
		if libraryID, err := h.codec.DecodeIntID(EncodedIDLibrary, routeID); err == nil {
			if imageURL, ok := h.resolveLibraryImageURLFromTag(r.Context(), routeID, int(libraryID), imageType, tag); ok {
				h.images.RememberSizedUntil(routeID, imageType, imageURL.URL, imageSize, imageURL.ExpiresAt)
				h.serveImageURL(w, r, imageURL.URL)
				return
			}
		}
		if imageURL, ok := h.images.LookupTag(tag); ok {
			h.serveImageURL(w, r, imageURL)
			return
		}
		// A tag the client just received may name artwork newer than this
		// node's route cache, so tagged requests resolve from the catalog.
	} else if imageURL, ok := h.images.LookupSized(routeID, imageType, "", imageSize); ok {
		h.serveImageURL(w, r, imageURL)
		return
	}

	if session == nil {
		if token, ok := ExtractToken(r); ok {
			session, _, _ = resolveCompatToken(r.Context(), h.sessions, h.keyAuth, token)
		}
	}

	contentID, err := decodeContentID(h.codec, routeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	// Jellyfin serves item artwork to anyone who asks (200/404, never 401):
	// players can't attach auth headers to <img> requests, and the tag is only
	// a cache hint, never checked. Clients keep tags from cached lists after a
	// metadata refresh rotates them, and some send none, so neither a missing
	// nor a stale tag may withhold an image that exists. The session's access
	// filter only narrows what a signed-in viewer is shown; it is not what
	// protects artwork, which anonymous requests can already fetch.
	var resolvedImage catalog.ResolvedImageURL
	if session == nil {
		resolvedImage, _, err = h.resolveCatalogImageURL(r.Context(), contentID, imageType, imageSize, nil)
	} else {
		resolvedImage, err = h.resolveItemImageURL(r.Context(), session, contentID, imageType, r)
	}
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

// handlePersonImage serves person photo images. A signed tag authorizes the
// request on its own, because Jellyfin Web loads images anonymously; without
// one, the session must see a credit for the person.
func (h *ImagesHandler) handlePersonImage(w http.ResponseWriter, r *http.Request, session *Session, routeID, imageType, tag string, personID int64) {
	if imageType != compatImagePrimary || h.personRepo == nil || h.detailSvc == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	var person *models.Person
	if tag != "" && h.imageTags != nil {
		if p, err := h.personRepo.Get(r.Context(), personID); err == nil && h.imageTags.Equal(personImageTagSeed(routeID, p.PhotoPath, p.PhotoThumbhash), "", tag) {
			person = p
		}
	}
	if person == nil {
		if session == nil {
			writeError(w, http.StatusNotFound, "NotFound", "Image not found")
			return
		}
		filter := catalog.AccessFilter{}
		if h.accessFilter != nil {
			filter = h.accessFilter(r.Context(), session.StreamAppUserID, session.ProfileID)
		}
		if err := h.personRepo.EnsureAccessible(r.Context(), personID, filter); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				writeCompatUpstreamError(w, err)
				return
			}
			writeError(w, http.StatusNotFound, "NotFound", "Person not found")
			return
		}
	}
	if person == nil {
		p, err := h.personRepo.Get(r.Context(), personID)
		if err != nil {
			writeError(w, http.StatusNotFound, "NotFound", "Person not found")
			return
		}
		person = p
	}
	// Key the shared cache by the current photo so a replaced photo, which
	// also rotates the signed tag, never serves the previous photo's URL.
	cacheRouteID := personImageCacheRouteID(routeID, person.PhotoPath)
	imageSize := compatRequestImageSize(r, imageType)
	if imageURL, ok := h.images.LookupSized(cacheRouteID, imageType, "", imageSize); ok {
		h.serveImageURL(w, r, imageURL)
		return
	}
	// Headshots ride the profile ladder ({500, 300}), not the poster ladder,
	// which now carries a w780 rung. Resolving them as posters would name a
	// profile/w780 key that is never generated — and the server-side ladder
	// fallback deliberately skips profile, so nothing would rescue it.
	resolvedImage := compatPresignImageWithExpiry(h.detailSvc, r.Context(), person.PhotoPath, artworkkey.ImageProfile, imageSize)
	imageURL := resolvedImage.URL
	if imageURL == "" {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}
	h.images.RememberSizedUntil(cacheRouteID, imageType, imageURL, imageSize, resolvedImage.ExpiresAt)
	h.serveImageURL(w, r, imageURL)
}

// personImageCacheRouteID is the ImageCache route key for a person's current
// photo.
func personImageCacheRouteID(routeID, photoPath string) string {
	return routeID + "\x00" + strings.TrimSpace(photoPath)
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
	return h.resolveCatalogImageURL(ctx, contentID, imageType, compatRequestImageSize(r, imageType), &access)
}

// resolveCatalogImageURL resolves a movie, series, episode, or season image from
// the catalog. A non-nil access filter limits it to what that viewer may see;
// nil serves any item, as Jellyfin does for anonymous image GETs.
func (h *ImagesHandler) resolveCatalogImageURL(ctx context.Context, contentID, imageType, imageSize string, access *catalog.AccessFilter) (catalog.ResolvedImageURL, bool, error) {
	switch imageType {
	case "Primary", "Backdrop", "Thumb", "Logo":
	default:
		// imageURLForItem has nothing for other types (Chapter, Banner, ...).
		return catalog.ResolvedImageURL{}, false, nil
	}
	// ensureAccessible checks the item (or an episode's or season's series)
	// against the viewer's filter. Without one it still hides the media types
	// the compat surface never exposes, which the filter would otherwise drop.
	ensureAccessible := func(item *models.MediaItem) error {
		if access == nil {
			if isCompatExcludedMediaType(item.Type) {
				return wrapCatalogError(catalog.ErrItemNotFound)
			}
			return nil
		}
		return wrapCatalogError(h.itemRepo.EnsureAccessible(ctx, item.ContentID, *access))
	}

	if h.itemRepo != nil {
		if item, err := h.itemRepo.GetByID(ctx, contentID); err == nil {
			if err := ensureAccessible(item); err != nil {
				return catalog.ResolvedImageURL{}, false, err
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
				if err := ensureAccessible(series); err != nil {
					return catalog.ResolvedImageURL{}, false, err
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
			if err := ensureAccessible(series); err != nil {
				return catalog.ResolvedImageURL{}, false, err
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

// collectionArtworkKey returns the stored artwork reference for the requested
// compat image type ("" when the collection has none of that type).
func collectionArtworkKey(c *models.LibraryCollection, imageType string) string {
	switch imageType {
	case "Primary":
		poster, _ := catalog.AssignedCollectionPoster(c)
		return poster.Path
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
		if key := strings.TrimSpace(collectionArtworkKey(c, "Primary")); key != "" {
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
// artwork is presigned/served as before. A collage tag serves the collage it
// names, and an untagged request the collage of the session's viewer.
// Collections without a usable poster fall back to a generated gradient poster
// captioned with the title.
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

	if imageType == "Primary" {
		if key, ok := verifiedCollageImageTag(h.imageTags, routeID, tag); ok {
			// The tag was minted for a viewer this collage belongs to.
			poster, found, err := h.collectionCollage(r.Context(), collectionID, key)
			if err != nil {
				writeCompatUpstreamError(w, err)
				return
			}
			if found {
				if imageURL := h.presignCollectionArtwork(r.Context(), poster.Path); imageURL != "" {
					h.serveImageURL(w, r, imageURL)
					return
				}
			}
			h.serveGeneratedPoster(w, collection.Title)
			return
		}
	}

	seed, served := collectionImageTagSeed(routeID, imageType, collection)
	if !served {
		writeError(w, http.StatusNotFound, "NotFound", "Image not found")
		return
	}

	authorized := tag != "" && h.imageTags != nil && h.imageTags.Equal(seed, "", tag)
	var session *Session
	if !authorized {
		session = h.requestSession(r)
		ok, err := h.collectionVisibleToSession(r.Context(), session, collection)
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
	// An untagged request authorized by its session gets that viewer's collage.
	// A request carrying the generated poster's tag keeps getting that poster.
	if session != nil && h.collectionPosters != nil && h.accessFilter != nil {
		access := withCompatAccessExclusions(h.accessFilter(r.Context(), session.StreamAppUserID, session.ProfileID))
		poster := h.collectionPosters.CollectionPosters(r.Context(), []*models.LibraryCollection{collection}, access)[collection.ID]
		if imageURL := h.presignCollectionArtwork(r.Context(), poster.Path); imageURL != "" {
			h.serveImageURL(w, r, imageURL)
			return
		}
	}
	h.serveGeneratedPoster(w, collection.Title)
}

func (h *ImagesHandler) collectionCollage(ctx context.Context, collectionID, key string) (catalog.CollectionPoster, bool, error) {
	if h.collectionPosters == nil {
		return catalog.CollectionPoster{}, false, nil
	}
	return h.collectionPosters.CollectionCollage(ctx, collectionID, key)
}

// requestSession returns the request's compat session, if any.
func (h *ImagesHandler) requestSession(r *http.Request) *Session {
	session := SessionFromContext(r.Context())
	if session == nil && h.sessions != nil {
		if token, ok := ExtractToken(r); ok {
			session, _ = h.sessions.Get(token)
		}
	}
	return session
}

// collectionVisibleToSession reports whether the session may see the
// collection. A missing session resolves to not-visible (anonymous image GETs
// without a valid tag get a clean 404).
func (h *ImagesHandler) collectionVisibleToSession(ctx context.Context, session *Session, collection *models.LibraryCollection) (bool, error) {
	if session == nil {
		return false, nil
	}
	visible, err := visibleLibraryIDSet(ctx, h.content, session)
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

func (h *ImagesHandler) resolveLibraryImageURLFromTag(ctx context.Context, routeID string, libraryID int, imageType, tag string) (catalog.ResolvedImageURL, bool) {
	if imageType != "Primary" || h.folderRepo == nil || h.posterSigner == nil {
		return catalog.ResolvedImageURL{}, false
	}
	folder, err := h.folderRepo.GetByID(ctx, libraryID)
	if err != nil {
		return catalog.ResolvedImageURL{}, false
	}
	if folder.PosterPath == "" || !h.imageTags.Equal(
		imageTagSeed(routeID, "Primary", compatCardImageSize, folder.PosterPath, "", time.Time{}),
		"",
		tag,
	) {
		return catalog.ResolvedImageURL{}, false
	}
	imageURL := h.presignLibraryPosterURL(ctx, folder.PosterPath)
	if imageURL == "" {
		return catalog.ResolvedImageURL{}, false
	}
	return catalog.ResolvedImageURL{URL: imageURL}, true
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

// imageURLForItem presigns the requested image type, and its fallback type
// only when the requested one yields no URL.
func (h *ImagesHandler) imageURLForItem(ctx context.Context, primaryPath, primaryImageType, backdropPath, logoPath, imageType, size string) catalog.ResolvedImageURL {
	primaryURL := func() catalog.ResolvedImageURL {
		return compatPresignImageWithExpiry(h.detailSvc, ctx, primaryPath, primaryImageType, size)
	}
	backdropURL := func() catalog.ResolvedImageURL {
		return compatPresignImageWithExpiry(h.detailSvc, ctx, backdropPath, "backdrop", size)
	}

	switch imageType {
	case "Primary":
		return firstResolvedImageURL(primaryURL, backdropURL)
	case "Backdrop", "Thumb":
		return firstResolvedImageURL(backdropURL, primaryURL)
	case "Logo":
		return compatPresignImageWithExpiry(h.detailSvc, ctx, logoPath, "logo", size)
	default:
		return catalog.ResolvedImageURL{}
	}
}

// firstResolvedImageURL resolves candidates in order and returns the first
// URL produced, so later candidates are not presigned needlessly.
func firstResolvedImageURL(candidates ...func() catalog.ResolvedImageURL) catalog.ResolvedImageURL {
	for _, resolve := range candidates {
		if value := resolve(); value.URL != "" {
			return value
		}
	}
	return catalog.ResolvedImageURL{}
}

func (h *ImagesHandler) serveImageURL(w http.ResponseWriter, r *http.Request, imageURL string) {
	// App-relative references (bundled collection-template posters) have no
	// remote origin to redirect or proxy to, so serve their bytes from the
	// embedded frontend assets instead.
	if strings.HasPrefix(imageURL, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, imageURL, http.StatusFound)
		return
	}
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
