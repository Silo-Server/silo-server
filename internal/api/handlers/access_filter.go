package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

func requestAccessFilter(r *http.Request) catalog.AccessFilter {
	return AccessFilterFromContext(r.Context(), deviceMetadataFromRequest(r).DeviceID)
}

// AccessFilterFromContext is the viewer's catalog access filter from the
// claims, profile and viewer scope the gates stored in ctx; deviceID is the
// caller's device when known. v1 reads it from the request (requestAccessFilter);
// v2 handlers, which never read headers, call this directly.
func AccessFilterFromContext(ctx context.Context, deviceID string) catalog.AccessFilter {
	if scope, ok := access.GetScope(ctx); ok {
		return catalog.AccessFilter{
			AllowedLibraryIDs:  scope.AllowedLibraryIDs,
			DisabledLibraryIDs: scope.DisabledLibraryIDs,
			MaxContentRating:   scope.MaxContentRating,
			MaxPlaybackQuality: scope.MaxPlaybackQuality,
			UserID:             apimw.GetUserID(ctx),
			ProfileID:          apimw.GetProfileID(ctx),
			DeviceID:           deviceID,
		}
	}
	return catalog.AccessFilter{
		UserID:    apimw.GetUserID(ctx),
		ProfileID: apimw.GetProfileID(ctx),
		DeviceID:  deviceID,
	}
}
