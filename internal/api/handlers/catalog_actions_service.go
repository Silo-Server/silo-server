package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
)

// Seams of the catalog-items section's actions and lookups: trailer refresh,
// on-view description translation, people, and literary works. The v1
// handlers and the v2 operations both call these; each returns *APIError for
// a decision the transport renders in its own shape.

const (
	trailerItemTypeMovie  = "movie"
	trailerItemTypeSeries = "series"
	// metadataAIOnViewOff is the on-view mode of an unwired or unconfigured
	// translation service.
	metadataAIOnViewOff = "off"
	// metadataAIStatusEnabledKey is the v1 status probe's enabled member.
	metadataAIStatusEnabledKey = "enabled"
)

// TrailerRefreshCapabilityView is what a server offers for the viewer-facing
// trailer fetch.
type TrailerRefreshCapabilityView struct {
	Enabled         bool
	CooldownSeconds int
	Statuses        []string
	SupportedTypes  []string
}

// TrailerRefreshCapability answers whether the trailer refresh action is
// wired; unwired it reports Enabled false with empty lists.
func (h *ItemsHandler) TrailerRefreshCapability() TrailerRefreshCapabilityView {
	enabled := h != nil && h.trailerRefreshRequester != nil && h.trailerItemAccess != nil
	view := TrailerRefreshCapabilityView{Enabled: enabled, Statuses: []string{}, SupportedTypes: []string{}}
	if enabled {
		view.CooldownSeconds = int(metadata.TrailerRefreshCooldown / time.Second)
		view.Statuses = []string{
			metadata.TrailerRefreshStatusQueued,
			metadata.TrailerRefreshStatusCooldown,
			metadata.TrailerRefreshStatusDisabled,
		}
		view.SupportedTypes = []string{trailerItemTypeMovie, trailerItemTypeSeries}
	}
	return view
}

// TrailerRefreshView is the outcome of a viewer-triggered trailer fetch.
type TrailerRefreshView struct {
	Status        string
	NextAllowedAt *time.Time
}

// RequestTrailersRefresh asks the metadata service to fetch an item's remote
// trailers for the viewer userID. resolveAccess is called only after the
// per-user limiter and the target lookup passed, so a caller who cannot see
// the item never consumes its cooldown slot and the v1 answer order holds; an
// *APIError it returns is passed through unchanged.
func (h *ItemsHandler) RequestTrailersRefresh(ctx context.Context, userID int, contentID string, resolveAccess func() (catalog.AccessFilter, error)) (TrailerRefreshView, error) {
	if h == nil || h.trailerRefreshRequester == nil || h.trailerItemAccess == nil {
		return TrailerRefreshView{}, apiError(http.StatusServiceUnavailable, policyErrorUnavailable, "Trailer refresh is not configured")
	}
	if contentID == "" {
		return TrailerRefreshView{}, apiError(http.StatusBadRequest, policyErrorBadRequest, "Item ID is required")
	}
	if userID == 0 {
		return TrailerRefreshView{}, apiError(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if h.trailerRefreshLimiter != nil {
		// The limiter may be the process-wide one the middleware uses, so the
		// key is namespaced: an unprefixed user id would share a counter with
		// whatever else keys on the same string.
		result := h.trailerRefreshLimiter.Allow(ctx, trailerRefreshLimiterKey(userID), trailerRefreshRate)
		if !result.Allowed {
			limited := apiError(http.StatusTooManyRequests, "rate_limited", "Too many trailer refresh requests")
			if result.RetryAfter > 0 {
				limited.RetryAfter = max(1, int(result.RetryAfter.Seconds()))
			}
			return TrailerRefreshView{}, limited
		}
	}

	target, err := h.resolveTrailerRefreshTarget(ctx, contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return TrailerRefreshView{}, apiError(http.StatusNotFound, policyErrorNotFound, "Item not found")
		}
		slog.ErrorContext(ctx, "trailers: failed to look up item", "component", "api",
			"content_id", contentID, "error", err)
		return TrailerRefreshView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to authorize item")
	}
	// Authorize against the series for a season or episode ID, exactly as the
	// on-view translation route does, so an unsupported-type answer never
	// leaks the existence of content the caller cannot see.
	filter, err := resolveAccess()
	if err != nil {
		return TrailerRefreshView{}, err
	}
	if err := h.trailerItemAccess.EnsureAccessible(ctx, target.accessContentID, filter); err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return TrailerRefreshView{}, apiError(http.StatusNotFound, policyErrorNotFound, "Item not found")
		}
		slog.ErrorContext(ctx, "trailers: failed to authorize item", "component", "api",
			"content_id", contentID, "error", err)
		return TrailerRefreshView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to authorize item")
	}

	// Only movie and series detail responses carry videos/extras, so anything
	// else — another media_items type, or a season/episode ID, which is not a
	// media_items row at all — is a client bug rather than an empty result.
	if !target.supportsTrailers {
		return TrailerRefreshView{}, apiError(http.StatusBadRequest, "unsupported_type", "Trailers are only available for movies and series")
	}

	outcome, err := h.trailerRefreshRequester.RequestTrailersRefresh(ctx, contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return TrailerRefreshView{}, apiError(http.StatusNotFound, policyErrorNotFound, "Item not found")
		}
		slog.ErrorContext(ctx, "trailers: failed to request refresh", "component", "api",
			"content_id", contentID, "error", err)
		return TrailerRefreshView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to request trailers")
	}
	switch outcome.Status {
	case metadata.TrailerRefreshStatusQueued, metadata.TrailerRefreshStatusCooldown, metadata.TrailerRefreshStatusDisabled:
		return TrailerRefreshView{Status: outcome.Status, NextAllowedAt: outcome.NextAllowedAt}, nil
	}
	slog.ErrorContext(ctx, "trailers: unexpected refresh outcome", "component", "api",
		"content_id", contentID, "status", outcome.Status)
	return TrailerRefreshView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to request trailers")
}

// MetadataAIStatusView is the metadata AI translation availability probe.
type MetadataAIStatusView struct {
	Enabled bool
	OnView  string
}

// Status reports whether metadata AI translation is configured and the
// viewer-facing on-view mode; a nil handler is the clean negative.
func (h *MetadataAIHandler) Status() MetadataAIStatusView {
	if h == nil || h.service == nil {
		return MetadataAIStatusView{OnView: metadataAIOnViewOff}
	}
	return MetadataAIStatusView{Enabled: h.service.Enabled(), OnView: h.service.OnViewMode()}
}

// TranslateOnView queues the viewer-facing description translation of the
// item, season, or episode contentID into targetLanguage, after checking the
// viewer can see it (a season or episode authorizes through its series).
// requestedBy is nil for a caller without an account id.
func (h *MetadataAIHandler) TranslateOnView(ctx context.Context, filter catalog.AccessFilter, contentID, targetLanguage string, requestedBy *int) (*translation.Job, error) {
	if targetLanguage == "" {
		return nil, fieldError("target_language", "target_language is required")
	}
	if h == nil || h.ItemAccess == nil {
		return nil, apiError(http.StatusForbidden, "forbidden", "Viewer access is required")
	}
	target, err := h.resolveTranslationTarget(ctx, contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return nil, apiError(http.StatusNotFound, policyErrorNotFound, "Item not found")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to authorize item")
	}
	if err := h.ItemAccess.EnsureAccessible(ctx, target.accessContentID, filter); err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return nil, apiError(http.StatusNotFound, policyErrorNotFound, "Item not found")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to authorize item")
	}
	job, err := h.service.RequestOnView(ctx, target.kind, contentID, targetLanguage, requestedBy)
	if err != nil {
		switch {
		case errors.Is(err, translation.ErrNotConfigured):
			return nil, &APIError{Status: http.StatusServiceUnavailable, Code: "not_configured",
				Message: "On-view translation is not enabled on this server", cause: err}
		case errors.Is(err, translation.ErrInvalidRequest):
			return nil, &APIError{Status: http.StatusBadRequest, Code: policyErrorBadRequest, Message: err.Error(), cause: err}
		}
		slog.ErrorContext(ctx, "failed to request on-view translation", "component", "api",
			"content_id", contentID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to start translation")
	}
	return job, nil
}

// SearchPeople answers up to limit people matching query; limit <= 0 is 20.
func (h *PeopleHandler) SearchPeople(ctx context.Context, query string, limit int) ([]PersonView, error) {
	if limit <= 0 {
		limit = 20
	}
	people, err := h.personRepo.Search(ctx, query, limit)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "search_failed", err.Error())
	}
	resp := make([]PersonView, len(people))
	for i, p := range people {
		resp[i] = h.toResponse(ctx, p)
	}
	return resp, nil
}

// SearchPeopleScoped serves v2 people search with media scope and exact-name ranking.
func (h *PeopleHandler) SearchPeopleScoped(ctx context.Context, query string, limit int, mediaScope string, filter catalog.AccessFilter) ([]PersonView, error) {
	people, err := h.personRepo.SearchScoped(ctx, query, limit, mediaScope, filter)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "search_failed", err.Error())
	}
	resp := make([]PersonView, len(people))
	for i, p := range people {
		resp[i] = h.toResponse(ctx, p)
	}
	return resp, nil
}

// Person answers one person. A view (queueRefresh) also queues a provider
// refresh when one is due; a speculative prefetch leaves that to the sweep.
func (h *PeopleHandler) Person(ctx context.Context, id int64, queueRefresh bool) (PersonView, error) {
	person, err := h.personRepo.Get(ctx, id)
	if err != nil {
		slog.WarnContext(ctx, "people: get person failed", "component", "api", "id", id, "id_str", strconv.FormatInt(id, 10), "error", err)
		return PersonView{}, apiError(http.StatusNotFound, policyErrorNotFound, "person not found")
	}
	if queueRefresh {
		h.enqueuePersonRefreshIfDue(*person)
	}
	return h.toResponse(ctx, *person), nil
}

// RefreshPerson queues a provider refresh of the person for the viewer
// userID, at most personRefreshRate per user.
func (h *PeopleHandler) RefreshPerson(ctx context.Context, userID int, id int64) error {
	if h == nil || h.refreshQueue == nil {
		return apiError(http.StatusServiceUnavailable, policyErrorUnavailable, "Person refresh is not configured")
	}
	if userID == 0 {
		return apiError(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	result := h.refreshLimiter.Allow(ctx, strconv.Itoa(userID), personRefreshRate)
	if !result.Allowed {
		limited := apiError(http.StatusTooManyRequests, "rate_limited", "Too many person refresh requests")
		if result.RetryAfter > 0 {
			limited.RetryAfter = max(1, int(result.RetryAfter.Seconds()))
		}
		return limited
	}
	person, err := h.personRepo.Get(ctx, id)
	if err != nil || person == nil {
		return apiError(http.StatusNotFound, policyErrorNotFound, "person not found")
	}
	h.refreshQueue.Enqueue(id)
	return nil
}

// Work answers a literary work with the formats the viewer can see.
func (h *LiteraryWorkHandler) Work(ctx context.Context, workID string, filter catalog.AccessFilter) (*literaryworks.DetailResponse, error) {
	if h == nil || h.Service == nil {
		return nil, apiError(http.StatusServiceUnavailable, policyErrorUnavailable, "Literary works are not configured")
	}
	resp, err := h.Service.GetWork(ctx, workID, filter)
	if err != nil {
		if errors.Is(err, literaryworks.ErrWorkNotFound) {
			return nil, apiError(http.StatusNotFound, policyErrorNotFound, "Work not found")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load work")
	}
	return resp, nil
}
