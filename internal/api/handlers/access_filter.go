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
		return accessFilterFromScope(scope, apimw.GetUserID(ctx), apimw.GetProfileID(ctx), deviceID)
	}
	return catalog.AccessFilter{
		UserID:    apimw.GetUserID(ctx),
		ProfileID: apimw.GetProfileID(ctx),
		DeviceID:  deviceID,
	}
}

// accessFilterFromScope maps a resolved viewer scope to its catalog filter.
// Handlers that resolve a scope themselves, rather than reading the one the
// gates stored in ctx, use it so a new scope field reaches every filter.
func accessFilterFromScope(scope access.Scope, userID int, profileID, deviceID string) catalog.AccessFilter {
	return catalog.AccessFilter{
		AllowedLibraryIDs:  scope.AllowedLibraryIDs,
		DisabledLibraryIDs: scope.DisabledLibraryIDs,
		MaturityLimits:     scope.MaturityLimits,
		MaxPlaybackQuality: scope.MaxPlaybackQuality,
		UserID:             userID,
		ProfileID:          profileID,
		DeviceID:           deviceID,
	}
}
