package handlers

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"net/http"
)

type MarkerContributionOutcomeView = contributionOutcomeResponse
type MarkerContributionView = contributionRowResponse
type MarkerContributionRequest struct {
	Provider string   `json:"provider"`
	Segments []string `json:"segments"`
}
type MarkerContributionPager interface {
	ListByFilePage(context.Context, int, int, markers.ContributionPagePosition) ([]markers.ContributionRow, bool, error)
}

func (h *MarkersHandler) ContributeAdminMarkers(ctx context.Context, access catalog.AccessFilter, fileID int, body MarkerContributionRequest) ([]MarkerContributionOutcomeView, error) {
	file, err := h.resolveMarkerTarget(ctx, access, MarkerTarget{FileID: fileID})
	if err != nil {
		return nil, err
	}
	return h.contributeMarkers(ctx, file, body)
}
func (h *MarkersHandler) contributeMarkers(ctx context.Context, file *models.MediaFile, body MarkerContributionRequest) ([]MarkerContributionOutcomeView, error) {
	if h.Contributor == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Contribution is not configured")
	}
	var kinds []markers.MarkerKind
	for _, name := range body.Segments {
		kind, ok := markerKindForName(name)
		if !ok {
			return nil, apiError(http.StatusBadRequest, "bad_request", "Unknown segment "+name)
		}
		kinds = append(kinds, kind)
	}
	outcomes, err := h.Contributor.ContributeFile(ctx, file, markers.ContributeOptions{Provider: body.Provider, Segments: kinds})
	if err != nil {
		h.logger.ErrorContext(ctx, "markers: contribute failed", "file_id", file.ID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Contribution failed")
	}
	return contributionOutcomeResponses(outcomes), nil
}
func (h *MarkersHandler) ListAdminMarkerContributions(ctx context.Context, access catalog.AccessFilter, fileID, limit int, after markers.ContributionPagePosition) ([]MarkerContributionView, bool, error) {
	file, err := h.resolveMarkerTarget(ctx, access, MarkerTarget{FileID: fileID})
	if err != nil {
		return nil, false, err
	}
	if h.Contributions == nil {
		return []MarkerContributionView{}, false, nil
	}
	pager, ok := h.Contributions.(MarkerContributionPager)
	if !ok {
		return nil, false, apiError(http.StatusServiceUnavailable, "unavailable", "Contribution paging is not configured")
	}
	rows, more, err := pager.ListByFilePage(ctx, file.ID, limit, after)
	if err != nil {
		return nil, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to load contributions")
	}
	return contributionRowResponses(rows), more, nil
}
