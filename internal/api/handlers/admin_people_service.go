package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// RefreshAdminPerson waits for the existing provider refresh and returns its result.
func (h *PeopleHandler) RefreshAdminPerson(ctx context.Context, id int64) (PersonView, error) {
	if h == nil || h.refresher == nil {
		return PersonView{}, apiError(http.StatusServiceUnavailable, "unavailable", "Person refresh is not configured")
	}
	requestCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	person, err := h.refresher.RefreshPerson(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, metadata.ErrPersonNotFound):
			return PersonView{}, apiError(http.StatusNotFound, "not_found", "person not found")
		case errors.Is(err, metadata.ErrPersonMetadataNotFound):
			return PersonView{}, apiError(http.StatusBadGateway, "provider_error", "No person metadata found")
		default:
			slog.WarnContext(ctx, "people: admin refresh failed", "component", "api", "id", id, "error", err)
			return PersonView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to refresh person")
		}
	}
	return h.toResponse(requestCtx, *person), nil
}

// UpdateAdminPerson preserves the legacy partial-update semantics: nil means no
// change, empty strings clear values, and dates are parsed before persistence.
func (h *PeopleHandler) UpdateAdminPerson(ctx context.Context, id int64, req UpdatePersonRequest) (PersonView, error) {
	if h == nil || h.personRepo == nil {
		return PersonView{}, apiError(http.StatusServiceUnavailable, "unavailable", "People are not configured")
	}
	person, err := h.personRepo.Get(ctx, id)
	if err != nil || person == nil {
		return PersonView{}, apiError(http.StatusNotFound, "not_found", "person not found")
	}
	// Work on a value copy; a rejected date must not mutate a repository-owned object.
	updated := *person
	if req.Name != nil {
		updated.Name = *req.Name
		updated.SortName = *req.Name
	}
	if req.Bio != nil {
		updated.Bio = *req.Bio
	}
	if req.BirthDate != nil {
		parsed, err := parseOptionalPersonDate(*req.BirthDate)
		if err != nil {
			return PersonView{}, fieldError("birth_date", "Invalid birth_date")
		}
		updated.BirthDate = parsed
	}
	if req.DeathDate != nil {
		parsed, err := parseOptionalPersonDate(*req.DeathDate)
		if err != nil {
			return PersonView{}, fieldError("death_date", "Invalid death_date")
		}
		updated.DeathDate = parsed
	}
	if req.Birthplace != nil {
		updated.Birthplace = *req.Birthplace
	}
	if req.Homepage != nil {
		updated.Homepage = *req.Homepage
	}
	if req.TmdbID != nil {
		updated.TmdbID = *req.TmdbID
	}
	if req.ImdbID != nil {
		updated.ImdbID = *req.ImdbID
	}
	if req.TvdbID != nil {
		updated.TvdbID = *req.TvdbID
	}
	if err := h.personRepo.Update(ctx, updated); err != nil {
		slog.ErrorContext(ctx, "people: admin update failed", "component", "api", "id", id, "error", err)
		return PersonView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to update person")
	}
	return h.toResponse(ctx, updated), nil
}
