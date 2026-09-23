package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

// UserLibraryView is a simplified library view for non-admin users.
type UserLibraryView struct {
	CertificationCountry string `json:"-"`
	ID                   int    `json:"id"`
	Name                 string `json:"name"`
	Type                 string `json:"type"`
	SortOrder            int    `json:"sort_order"`
	PosterURL            string `json:"poster_url,omitempty"`
}

// ListUserLibraries uses the resolved viewer scope, or the account policy when
// no profile scope exists. It never returns administrator storage metadata.
func (h *LibraryHandler) ListUserLibraries(ctx context.Context, userID int) ([]UserLibraryView, error) {
	var folders []*models.MediaFolder
	var err error
	if scope, ok := access.GetScope(ctx); ok {
		if scope.LibrariesRestricted {
			folders, err = h.folderRepo.ListByIDs(ctx, scope.AllowedLibraryIDs)
		} else {
			folders, err = h.folderRepo.GetEnabled(ctx)
		}
	} else {
		if h.userRepo != nil {
			user, userErr := h.userRepo.GetByID(ctx, userID)
			if userErr != nil {
				slog.ErrorContext(ctx, "looking up user for library access", "component", "api", "error", userErr)
				return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to look up user")
			}

			effective, policyErr := access.EffectivePolicyForUser(ctx, user, h.AccessGroups)
			if policyErr != nil {
				slog.ErrorContext(ctx, "resolving user policy for library access", "component", "api", "error", policyErr)
				return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve user access")
			}
			if effective.LibraryIDs != nil {
				folders, err = h.folderRepo.ListByIDs(ctx, effective.LibraryIDs)
			} else {
				folders, err = h.folderRepo.GetEnabled(ctx)
			}
		} else {
			folders, err = h.folderRepo.GetEnabled(ctx)
		}
	}

	if err != nil {
		slog.ErrorContext(ctx, "listing user libraries", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list libraries")
	}

	resp := make([]UserLibraryView, 0, len(folders))
	for _, f := range folders {
		entry := UserLibraryView{
			ID:                   f.ID,
			CertificationCountry: f.CertificationCountry,
			Name:                 f.Name,
			Type:                 f.Type,
			SortOrder:            f.SortOrder,
		}
		if f.PosterPath != "" && h.ArtworkResolver != nil {
			entry.PosterURL = h.ArtworkResolver.ResolveURLs(ctx, []string{f.PosterPath})[f.PosterPath].URL
		}
		resp = append(resp, entry)
	}

	return resp, nil
}
