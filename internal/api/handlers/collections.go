package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// CollectionHandler handles personal collection CRUD endpoints.
type CollectionHandler struct {
	storeProvider      userstore.UserStoreProvider
	LibraryCollections collectionPreferenceLibraryReader
	Executor           *catalog.QueryExecutor
	ItemReader         collectionMutationItemReader
	ArtworkStore       blobstore.Store
	ArtworkResolver    artworkurl.Resolver
	// HTTPClient fetches poster_source_url images. Any profile supplies those
	// URLs, so the fetch reaches public addresses only (newCollectionImageClient,
	// with no netguard.WithPrivateAccess).
	HTTPClient *http.Client
	// CollectionOwners resolves the owner's access for another profile's
	// shared collection; without it those collections cannot be read.
	CollectionOwners catalog.PersonalCollectionAccess
	// ItemPosters signs catalog item posters for smart previews;
	// ArtworkResolver signs only stored collection artwork keys.
	ItemPosters itemPosterSigner
	// Collages serves and builds the collage a collection without an
	// uploaded or imported poster shows; nil when artwork storage is not
	// configured.
	Collages *catalog.PersonalCollectionCollages
}

// itemPosterSigner resolves item poster paths to delivery URLs in one batch;
// catalog.DetailService implements it.
type itemPosterSigner interface {
	PresignImageURLs(ctx context.Context, paths []string, imageType, size string) map[string]string
}

// NewCollectionHandler creates a new CollectionHandler.
func NewCollectionHandler(provider userstore.UserStoreProvider) *CollectionHandler {
	return &CollectionHandler{storeProvider: provider, HTTPClient: newCollectionImageClient()}
}

// --- Request/Response types ---

type PersonalCollectionCreateRequest struct {
	Name                       string          `json:"name"`
	CollectionType             string          `json:"collection_type"`
	IsShared                   bool            `json:"is_shared"`
	QueryDefinition            json.RawMessage `json:"query_definition"`
	SortConfig                 json.RawMessage `json:"sort_config"`
	DisplayQueryDefinition     json.RawMessage `json:"display_query_definition"`
	IncludeInServerCollections bool            `json:"include_in_server_collections"`
	PosterSourceURL            string          `json:"poster_source_url"`
	// Description is set only by the /api/v2 adapter; the frozen /api/v1
	// create never accepted one and still ignores it.
	Description string `json:"-"`
}

type PersonalCollectionUpdateRequest struct {
	Name                       *string                `json:"name"`
	Description                *string                `json:"description"`
	IsShared                   *bool                  `json:"is_shared"`
	QueryDefinition            json.RawMessage        `json:"query_definition"`
	SortConfig                 json.RawMessage        `json:"sort_config"`
	SourceURL                  *string                `json:"source_url"`
	MaxItems                   *int                   `json:"max_items"`
	LibraryIDs                 *[]int                 `json:"library_ids"`
	DisplayQueryDefinition     json.RawMessage        `json:"display_query_definition"`
	IncludeInServerCollections *bool                  `json:"include_in_server_collections"`
	PosterSourceURL            *string                `json:"poster_source_url"`
	GroupID                    optionalNullableString `json:"group_id"`
	// SyncSchedule is a cadence name (see usercollections.AllowedSyncSchedules)
	// or "" to stop syncing. Set only by the /api/v2 adapter; the frozen
	// /api/v1 update never accepted one and still ignores it.
	SyncSchedule *string `json:"-"`
}

type collectionItemRequest struct {
	Position int `json:"position"`
}

type PersonalCollectionView struct {
	ID               string `json:"id"`
	ProfileID        string `json:"profile_id"`
	CreatorProfileID string `json:"creator_profile_id"`
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	CollectionType   string `json:"collection_type"`
	IsShared         bool   `json:"is_shared"`
	// AllowedProfileIDs is the /api/v1 bridge's report of who sees the
	// collection; only the v1 handlers fill it (v1CollectionAudience).
	AllowedProfileIDs          []string        `json:"allowed_profile_ids"`
	QueryDefinition            json.RawMessage `json:"query_definition"`
	SortConfig                 json.RawMessage `json:"sort_config"`
	SortOrder                  int             `json:"sort_order"`
	GroupID                    *string         `json:"group_id"`
	SourceURL                  string          `json:"source_url,omitempty"`
	SourceConfig               json.RawMessage `json:"source_config,omitempty"`
	SyncSchedule               string          `json:"sync_schedule,omitempty"`
	NextSyncAt                 string          `json:"next_sync_at,omitempty"`
	LastSyncAt                 string          `json:"last_sync_at,omitempty"`
	LastSyncStatus             string          `json:"last_sync_status,omitempty"`
	LastSyncMessage            string          `json:"last_sync_message,omitempty"`
	DisplayQueryDefinition     json.RawMessage `json:"display_query_definition,omitempty"`
	ItemCount                  int             `json:"item_count"`
	IncludeInServerCollections bool            `json:"include_in_server_collections"`
	PosterURL                  string          `json:"poster_url,omitempty"`
	PosterThumbhash            string          `json:"poster_thumbhash,omitempty"`
	// PosterIsCollage reports that PosterURL is the collection's collage for
	// the reading profile rather than an uploaded or imported poster. Only
	// /api/v2 reads show collages and carry it.
	PosterIsCollage bool   `json:"-"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type PersonalCollectionListView struct {
	Collections []PersonalCollectionView `json:"collections"`
	Groups      []CollectionGroupView    `json:"groups"`
}

type CollectionCapabilitiesView struct {
	// DisplayFilterFields are the catalog query fields a personal-collection
	// display filter may use. Clients build a display_query_definition fragment
	// from these rather than a bespoke enum.
	DisplayFilterFields       []string                           `json:"display_filter_fields"`
	DisplayFilterPresets      CollectionDisplayFilterPresetsView `json:"display_filter_presets"`
	CollectionDefaultSort     bool                               `json:"collection_default_sort"`
	CollectionSortPreferences bool                               `json:"collection_sort_preferences"`
	EffectiveCollectionSort   bool                               `json:"effective_collection_sort"`
	// SortPreferenceKinds are the collection_kind values this server accepts on
	// the sort-preference endpoints. CollectionSortPreferences alone cannot
	// distinguish a server that also stores the personal-list kinds
	// ('watchlist', 'favorites') from one that rejects them.
	SortPreferenceKinds []string `json:"sort_preference_kinds"`
	// PosterCollages reports that /api/v2 personal collection reads show a
	// collage when a collection has no uploaded or imported poster. The
	// frozen /api/v1 body does not carry it.
	PosterCollages bool `json:"-"`
}

type CollectionDisplayFilterPresetsView struct {
	Watched []string `json:"watched"`
	Media   []string `json:"media"`
}

type CollectionGroupView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	DefaultSortMode string `json:"default_sort_mode"`
	SortOrder       int    `json:"sort_order"`
}

type CollectionGroupCreateRequest struct {
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	DefaultSortMode string `json:"default_sort_mode"`
}

type CollectionGroupUpdateRequest struct {
	Name            *string `json:"name"`
	Slug            *string `json:"slug"`
	DefaultSortMode *string `json:"default_sort_mode"`
}

type reorderCollectionGroupsRequest struct {
	OrderedIDs []string `json:"ordered_ids"`
}

type PersonalCollectionItemView struct {
	Title        string `json:"-"` // Catalog title for v2 membership; v1 stays unchanged.
	CollectionID string `json:"collection_id"`
	MediaItemID  string `json:"media_item_id"`
	Position     int    `json:"position"`
	AddedAt      string `json:"added_at"`
}

type PersonalCollectionItemsView struct {
	Items []PersonalCollectionItemView `json:"items"`
}

type PersonalCollectionPreviewRequest struct {
	QueryDefinition json.RawMessage `json:"query_definition"`
	Limit           int             `json:"limit"`
	// WithPosters signs each item's poster. Only the /api/v2 adapter sets it;
	// the frozen /api/v1 body drops posters, so it skips the presign batch.
	WithPosters bool `json:"-"`
	// V1Rules keeps the frozen /api/v1 rule vocabulary: a rule on a field or
	// with not_in_last that /api/v2 added is refused. Only /api/v1 sets it.
	V1Rules bool `json:"-"`
}

type PersonalCollectionPreviewView struct {
	Items []PersonalCollectionPreviewItemView `json:"items"`
	Total int                                 `json:"total"`
}

type PersonalCollectionPreviewItemView struct {
	ContentID string `json:"content_id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	// PosterURL is emitted by the /api/v2 adapter only; the frozen /api/v1
	// preview body never carried a poster.
	PosterURL string `json:"-"`
}

// --- Handler methods ---

type reorderRequest struct {
	OrderedIDs []string `json:"ordered_ids"`
	// GroupID is kept for v1 clients; personal collection groups no longer
	// exist, so only null or absent is accepted.
	GroupID *string `json:"group_id,omitempty"`
}

// --- Helpers ---

func toCollectionResponse(c userstore.Collection) PersonalCollectionView {
	queryDefinition := defaultJSON([]byte(c.QueryDefinition))
	sortConfig := defaultJSON([]byte(c.SortConfig))
	resp := PersonalCollectionView{
		ID:                         c.ID,
		ProfileID:                  c.ProfileID,
		CreatorProfileID:           c.CreatorProfileID,
		Name:                       c.Name,
		Description:                c.Description,
		CollectionType:             c.CollectionType,
		IsShared:                   c.IsShared,
		QueryDefinition:            queryDefinition,
		SortConfig:                 sortConfig,
		SortOrder:                  c.SortOrder,
		GroupID:                    c.GroupID,
		SourceURL:                  c.SourceURL,
		LastSyncStatus:             c.LastSyncStatus,
		LastSyncMessage:            c.LastSyncMessage,
		ItemCount:                  c.ItemCount,
		IncludeInServerCollections: c.IncludeInServerCollections,
		PosterURL:                  c.PosterURL,
		PosterThumbhash:            c.PosterThumbhash,
		CreatedAt:                  c.CreatedAt,
		UpdatedAt:                  c.UpdatedAt,
	}
	if strings.TrimSpace(c.DisplayQueryDefinition) != "" {
		resp.DisplayQueryDefinition = json.RawMessage(c.DisplayQueryDefinition)
	}
	if c.SourceConfig != "" && c.SourceConfig != "{}" {
		resp.SourceConfig = json.RawMessage(c.SourceConfig)
	}
	if c.SyncSchedule != nil {
		resp.SyncSchedule = *c.SyncSchedule
	}
	if c.NextSyncAt != nil {
		resp.NextSyncAt = c.NextSyncAt.UTC().Format(time.RFC3339)
	}
	if c.LastSyncAt != nil {
		resp.LastSyncAt = c.LastSyncAt.UTC().Format(time.RFC3339)
	}
	return resp
}

func defaultJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func normalizeQueryDefinitionJSON(raw []byte, allowPersonalizedSorts, allowPersonalizedFields, v1Rules bool) (json.RawMessage, error) {
	var def catalog.QueryDefinition
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &def); err != nil {
			return nil, err
		}
	}
	def = def.Normalize()
	if err := def.ValidateWithOptions(allowPersonalizedSorts, allowPersonalizedFields); err != nil {
		return nil, err
	}
	if v1Rules {
		if err := catalog.ValidateV1Rules(def); err != nil {
			return nil, err
		}
	}
	normalized, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func normalizeSmartCollectionQueryDefinitionJSON(raw []byte, allowPersonalizedSorts, allowPersonalizedFields, v1Rules bool) (json.RawMessage, error) {
	normalized, err := normalizeQueryDefinitionJSON(raw, allowPersonalizedSorts, allowPersonalizedFields, v1Rules)
	if err != nil {
		return nil, err
	}
	var def catalog.QueryDefinition
	if err := json.Unmarshal(normalized, &def); err != nil {
		return nil, err
	}
	def = catalog.ApplySmartCollectionItemLimit(def)
	out, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func firstNonEmptyCollection(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// errCollectionForbidden is returned by the artwork pipeline when the caller
// is not the collection's creator. It surfaces as a 403 to the client.
var errCollectionForbidden = errors.New("only the creator can edit this collection")

// posterInputProvided reports whether the request body or form contained
// poster artwork inputs that the upload pipeline would act on.
// processCollectionPoster persists an uploaded or sourced poster image on the
// given user collection. posterFile reads the uploaded part (nil when the
// request carried no multipart body; http.ErrMissingFile when the part is
// absent). It is a no-op, answering false, when no poster input was provided.
// The caller is responsible for ensuring the request profile owns the
// collection.
func (h *CollectionHandler) processCollectionPoster(
	ctx context.Context,
	store userstore.UserStore,
	collectionID, requestProfileID string,
	posterFile func() ([]byte, error),
	sourceURL string,
) (bool, error) {
	source := strings.TrimSpace(sourceURL)

	var fileData []byte
	if posterFile != nil {
		data, err := posterFile()
		switch {
		case err == nil:
			fileData = data
		case errors.Is(err, http.ErrMissingFile):
			// fall through to source URL handling
		default:
			return true, fmt.Errorf("poster: %w", err)
		}
	}
	if fileData == nil {
		if source == "" {
			return false, nil
		}
		downloaded, err := downloadCollectionImageURL(ctx, h.HTTPClient, source)
		if err != nil {
			return true, fmt.Errorf("poster source: %w", err)
		}
		fileData = downloaded
	}

	artwork := h.ArtworkStore
	if artwork == nil {
		return true, fmt.Errorf("poster upload requires configured artwork storage")
	}
	existing, err := store.GetCollection(ctx, collectionID)
	if err != nil {
		return true, fmt.Errorf("loading collection: %w", err)
	}
	// Revisioned keys (issue #1258) put the replacement under a new key, so
	// the current poster stays in place until the new path is committed.
	s3Path, thumbhash, err := uploadCollectionImageVariants(ctx, artwork, userCollectionImagePrefix, collectionID, collectionImagePoster, fileData)
	if err != nil {
		return true, fmt.Errorf("poster: %w", err)
	}

	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
		ID:               collectionID,
		RequestProfileID: requestProfileID,
		PosterURL:        &s3Path,
		PosterThumbhash:  &thumbhash,
	}); err != nil {
		if strings.Contains(err.Error(), "creator") {
			return true, errCollectionForbidden
		}
		return true, fmt.Errorf("persisting poster: %w", err)
	}
	cleanUpReplacedCollectionImage(ctx, artwork, userCollectionImagePrefix, collectionID, collectionImagePoster, existing.PosterURL, func(ctx context.Context) (string, error) {
		current, err := store.GetCollection(ctx, collectionID)
		if err != nil {
			return "", err
		}
		return current.PosterURL, nil
	})
	return true, nil
}

// presignUserCollectionPoster returns a presigned URL for the card-sized
// variant of the stored poster, mirroring the admin pipeline. Empty paths
// return "".
func (h *CollectionHandler) presignUserCollectionPoster(ctx context.Context, path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "/") {
		return catalog.ImageURLOrEmpty(path)
	}
	if h.ArtworkResolver == nil {
		return ""
	}
	key := cardThumbnailPath(path)
	return h.ArtworkResolver.ResolveURLs(ctx, []string{key})[key].URL
}

// previewCollectionRequest is shared with the library collection bridge handler.
type previewCollectionRequest = PersonalCollectionPreviewRequest
