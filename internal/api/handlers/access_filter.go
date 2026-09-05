package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

func requestAccessFilter(r *http.Request) catalog.AccessFilter {
	return ViewerAccessFilter(r.Context(), deviceMetadataFromRequest(r).DeviceID)
}

// ViewerAccessFilter is the viewer's access filter from the identity
// the middleware stored on the context; deviceID is the caller's declared
// device, "" when it declared none.
func ViewerAccessFilter(ctx context.Context, deviceID string) catalog.AccessFilter {
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
