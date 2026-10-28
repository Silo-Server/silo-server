package handlers

import (
	"context"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

type RequestService interface {
	Search(ctx context.Context, viewer mediarequests.Viewer, query string, mediaType mediarequests.MediaType, page int) (*mediarequests.MediaPage, error)
	// DiscoverRows and DiscoverRow back the Discover layout.
	DiscoverRows(ctx context.Context, viewer mediarequests.Viewer) (*mediarequests.DiscoverLayoutPage, error)
	DiscoverRow(ctx context.Context, viewer mediarequests.Viewer, rowID string, page int) (*mediarequests.DiscoverySection, error)
	GetDetail(ctx context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) (*mediarequests.MediaDetail, error)
	CreateRequest(ctx context.Context, viewer mediarequests.Viewer, input mediarequests.CreateRequestInput) (*mediarequests.Request, error)
	ListMine(ctx context.Context, viewer mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error)
	ListAdmin(ctx context.Context, viewer mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error)
	GetRequest(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	Approve(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	Decline(ctx context.Context, viewer mediarequests.Viewer, id, reason string) (*mediarequests.Request, error)
	Cancel(ctx context.Context, viewer mediarequests.Viewer, id, reason string) (*mediarequests.Request, error)
	Retry(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	GetFeatureStatus(ctx context.Context, viewer mediarequests.Viewer) (mediarequests.FeatureStatus, error)
	RequestCapabilityAllowed(ctx context.Context, viewer mediarequests.Viewer) (bool, error)
	GetSettings(ctx context.Context, viewer mediarequests.Viewer) (mediarequests.Settings, error)
	UpdateSettings(ctx context.Context, viewer mediarequests.Viewer, settings mediarequests.Settings) (mediarequests.Settings, error)
	GetUserLimit(ctx context.Context, viewer mediarequests.Viewer, userID int) (*mediarequests.UserLimit, error)
	UpsertUserLimit(ctx context.Context, viewer mediarequests.Viewer, limit mediarequests.UserLimit) (*mediarequests.UserLimit, error)
	ListIntegrations(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.Integration, error)
	CreateIntegration(ctx context.Context, viewer mediarequests.Viewer, integration mediarequests.Integration) (*mediarequests.Integration, error)
	UpdateIntegration(ctx context.Context, viewer mediarequests.Viewer, integration mediarequests.Integration) (*mediarequests.Integration, error)
	DeleteIntegration(ctx context.Context, viewer mediarequests.Viewer, id string) error
	LoadIntegrationOptions(ctx context.Context, viewer mediarequests.Viewer, integration mediarequests.Integration) (map[string][]mediarequests.RouterOption, error)

	ListStudios(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error)
	ListNetworks(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error)
	ListGenres(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error)
	BrowseStudio(ctx context.Context, viewer mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
	BrowseNetwork(ctx context.Context, viewer mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
	BrowseGenre(ctx context.Context, viewer mediarequests.Viewer, slug string, mediaType mediarequests.MediaType, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
	// Follow and Unfollow back the v2 follow operations; v1 has no follow route.
	Follow(ctx context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) (mediarequests.RequestState, error)
	Unfollow(ctx context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) error
}

type RequestsHandler struct {
	service RequestService
}

func NewRequestsHandler(service RequestService) *RequestsHandler {
	return &RequestsHandler{service: service}
}

// Service is the request service this handler routes to; the v2 listener
// calls the same value so both surfaces share one store and one policy.
func (h *RequestsHandler) Service() RequestService { return h.service }
