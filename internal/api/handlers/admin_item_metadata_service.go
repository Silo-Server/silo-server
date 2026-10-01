package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func (h *AdminHandler) CreateItemMetadataRefresh(ctx context.Context, contentID string, mode adminjob.ItemRefreshMode, userID int) (*models.AdminJob, error) {
	if h == nil || h.JobRepo == nil || h.ItemRefreshResolver == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Item refresh jobs are not configured")
	}
	if contentID == "" {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Item ID is required")
	}
	if mode == "" {
		mode = adminjob.ItemRefreshModeQuick
	}
	if mode != adminjob.ItemRefreshModeQuick && mode != adminjob.ItemRefreshModeComplete {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Invalid refresh mode")
	}
	payload, err := h.ItemRefreshResolver.ResolveWithMode(ctx, contentID, mode)
	if err != nil {
		if scopeErr, ok := errors.AsType[*adminjob.ScopeResolutionError](err); ok {
			code := "bad_request"
			if scopeErr.StatusCode == http.StatusNotFound {
				code = "not_found"
			} else if scopeErr.StatusCode >= http.StatusConflict {
				code = "conflict"
			}
			return nil, apiError(scopeErr.StatusCode, code, scopeErr.Message)
		}
		slog.ErrorContext(ctx, "admin: resolve item refresh scope failed", "component", "api", "content_id", contentID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve item refresh scope")
	}

	job, err := h.JobRepo.Create(ctx, adminjob.CreateJobInput{
		JobType:         adminjob.JobTypeItemRefresh,
		CreatedByUserID: userID,
		RequestPayload:  payload,
		Message:         "Queued item metadata refresh",
	})
	if err != nil {
		slog.ErrorContext(ctx, "admin: create item refresh job failed", "component", "api", "content_id", contentID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to queue item metadata refresh")
	}
	if h.RealtimeHub != nil {
		publishEventJob(ctx, h.RealtimeHub.EventsHub(), "job.created", job)
	}

	return job, nil
}

func (h *AdminHandler) UpdateCatalogItemMetadata(ctx context.Context, contentID string, req UpdateItemMetadataRequest) (*catalog.ItemDetail, error) {
	if h == nil || h.DetailSvc == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Catalog metadata is not configured")
	}
	if contentID == "" {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Item ID is required")
	}
	if req.AirTimezone != nil {
		trimmed := strings.TrimSpace(*req.AirTimezone)
		req.AirTimezone = new(trimmed)
		if !catalog.ValidateAirTimezone(trimmed) {
			return nil, apiError(http.StatusBadRequest, "bad_request", "air_timezone must be a valid IANA timezone")
		}
	}

	upd := catalog.MetadataUpdate{
		Title: req.Title, SortTitle: req.SortTitle, OriginalTitle: req.OriginalTitle,
		Overview: req.Overview, Tagline: req.Tagline, ContentRating: req.ContentRating,
		Year: req.Year, Runtime: req.Runtime,
		Genres: req.Genres, Studios: req.Studios, Networks: req.Networks, Countries: req.Countries,
		ReleaseDate: req.ReleaseDate, FirstAirDate: req.FirstAirDate, LastAirDate: req.LastAirDate,
		AirTime: req.AirTime, AirTimezone: req.AirTimezone,
		AirDate: req.AirDate, Status: req.Status,
		RatingIMDB: req.RatingIMDB, RatingTMDB: req.RatingTMDB,
		RatingRTCritic: req.RatingRTCritic, RatingRTAudience: req.RatingRTAudience,
		ImdbID: req.ImdbID, TmdbID: req.TmdbID, TvdbID: req.TvdbID,
		SeasonNumber: req.SeasonNumber, EpisodeNumber: req.EpisodeNumber,
		LockedFields: req.LockedFields,
	}

	// Try media_items first, then seasons, then episodes.
	if err := h.DetailSvc.UpdateMediaItemMetadata(ctx, contentID, &upd); err != nil {
		if !errors.Is(err, catalog.ErrItemNotFound) {
			slog.ErrorContext(ctx, "admin: update item metadata failed", "component", "api", "content_id", contentID, "error", err)
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to update metadata")
		}
		if err := h.DetailSvc.UpdateSeasonMetadata(ctx, contentID, &upd); err != nil {
			if !errors.Is(err, catalog.ErrSeasonNotFound) {
				slog.ErrorContext(ctx, "admin: update season metadata failed", "component", "api", "content_id", contentID, "error", err)
				return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to update metadata")
			}
			if err := h.DetailSvc.UpdateEpisodeMetadata(ctx, contentID, &upd); err != nil {
				if errors.Is(err, catalog.ErrEpisodeNotFound) {
					return nil, apiError(http.StatusNotFound, "not_found", "Item not found")
				}
				slog.ErrorContext(ctx, "admin: update episode metadata failed", "component", "api", "content_id", contentID, "error", err)
				return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to update metadata")
			}
		}
	}

	if h.EventBus != nil {
		_ = h.EventBus.Publish(ctx, cache.ChannelAdmin,
			cache.Event{Type: "item:updated", Payload: contentID})
	}
	if h.RealtimeHub != nil {
		publishEventMetadataUpdate(ctx, h.RealtimeHub.EventsHub(), 0, contentID)
	}

	detail, err := h.DetailSvc.GetItemDetail(ctx, contentID, catalog.AccessFilter{})
	if err != nil {
		slog.ErrorContext(ctx, "admin: fetch updated detail failed", "component", "api", "content_id", contentID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Updated but failed to fetch result")
	}
	return detail, nil
}
