package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type PermissionUserLoader interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

type MetadataTargetLibraryResolver interface {
	ResolveMetadataTargetLibraryIDs(ctx context.Context, contentID string) ([]int, error)
}

type PermissionMiddleware struct {
	users     PermissionUserLoader
	libraries MetadataTargetLibraryResolver
}

func NewPermissionMiddleware(users PermissionUserLoader, libraries MetadataTargetLibraryResolver) *PermissionMiddleware {
	return &PermissionMiddleware{users: users, libraries: libraries}
}

// RequireMetadataCurationForItem allows admins or users with metadata_curation
// permission when every library containing the target item is within the user's
// assigned libraries. A nil user library list means unrestricted library access.
func (m *PermissionMiddleware) RequireMetadataCurationForItem(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r.Context())
		if claims == nil {
			writeUnauthorized(w, "Authentication required")
			return
		}
		if claims.Role == "admin" {
			next.ServeHTTP(w, r)
			return
		}
		if m == nil || m.users == nil || m.libraries == nil {
			writeForbidden(w, "Metadata curation permission required")
			return
		}

		contentID := chi.URLParam(r, "id")
		if contentID == "" {
			writePermissionError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
			return
		}

		user, err := m.users.GetByID(r.Context(), claims.UserID)
		if err != nil || user == nil || !user.Enabled {
			writeForbidden(w, "Metadata curation permission required")
			return
		}
		if !auth.HasEffectivePermission(user, auth.PermissionMetadataCuration) {
			writeForbidden(w, "Metadata curation permission required")
			return
		}

		targetLibraries, err := m.libraries.ResolveMetadataTargetLibraryIDs(r.Context(), contentID)
		if err != nil {
			writePermissionError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve item libraries")
			return
		}
		if len(targetLibraries) == 0 {
			writePermissionError(w, http.StatusNotFound, "not_found", "Item not found")
			return
		}
		if !metadataTargetWithinUserLibraries(user.LibraryIDs, targetLibraries) {
			writeForbidden(w, "Item is outside your assigned libraries")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func metadataTargetWithinUserLibraries(allowed []int, target []int) bool {
	if allowed == nil {
		return true
	}
	if len(target) == 0 {
		return false
	}
	allowedSet := make(map[int]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	for _, id := range target {
		if _, ok := allowedSet[id]; !ok {
			return false
		}
	}
	return true
}

type PGMetadataTargetLibraryResolver struct {
	Pool *pgxpool.Pool
}

func NewPGMetadataTargetLibraryResolver(pool *pgxpool.Pool) *PGMetadataTargetLibraryResolver {
	return &PGMetadataTargetLibraryResolver{Pool: pool}
}

func (r *PGMetadataTargetLibraryResolver) ResolveMetadataTargetLibraryIDs(ctx context.Context, contentID string) ([]int, error) {
	if r == nil || r.Pool == nil {
		return nil, fmt.Errorf("database not configured")
	}
	rows, err := r.Pool.Query(ctx, `
		WITH target_root AS (
			SELECT mi.content_id
			FROM media_items mi
			WHERE mi.content_id = $1
			UNION
			SELECT s.series_id
			FROM seasons s
			WHERE s.content_id = $1
			UNION
			SELECT e.series_id
			FROM episodes e
			WHERE e.content_id = $1
		)
		SELECT DISTINCT mil.media_folder_id
		FROM target_root tr
		JOIN media_item_libraries mil ON mil.content_id = tr.content_id
		ORDER BY mil.media_folder_id`, contentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func writePermissionError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: code, Message: message})
}
