package abs

import (
	"net/http"
	"strconv"
	"strings"
)

// handleMe — GET /abs/api/me (and /api/me)
//
// Returns the ABS "user object" for the authenticated caller. The mobile app
// calls this on launch (after /authorize) and whenever it needs a fresh
// progress snapshot. We include all audiobook progress rows and the list of
// accessible library IDs so the home tab renders without extra calls.
func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	defaultLibID := VirtualLibraryID
	libs, err := h.deps.MediaStore.ListAudiobookLibraries(r.Context())
	if err == nil && len(libs) > 0 {
		defaultLibID = audiobookLibraryID(libs[0])
	}

	writeJSON(w, http.StatusOK, h.buildUserObject(r, a, defaultLibID))
}

// buildUserObject constructs the ABS user envelope used by /me, /login,
// and /authorize. Progress and bookmarks are best-effort: errors return
// empty slices rather than failing the request.
func (h *Handler) buildUserObject(r *http.Request, a ctxAuth, defaultLibraryID string) map[string]any {
	ctx := r.Context()

	// Media progress — all audiobook progress rows for this user.
	progress := make([]map[string]any, 0)
	if h.deps.ProgressStore != nil {
		rows, _ := h.deps.ProgressStore.ListProgressForAudiobooks(ctx, a.UserID, a.ProfileID, 200)
		for _, p := range rows {
			progress = append(progress, progressRowToABS(p))
		}
	}

	// librariesAccessible drives the mobile app's library picker.
	libs, _ := h.deps.MediaStore.ListAudiobookLibraries(ctx)
	libraryIDs := make([]string, 0, len(libs))
	for _, lib := range libs {
		libraryIDs = append(libraryIDs, audiobookLibraryID(lib))
	}

	displayName := a.UserID // best we have without a users table join

	return map[string]any{
		"id":                  a.UserID,
		"username":            displayName,
		"type":                "user",
		"defaultLibraryId":    defaultLibraryID,
		"librariesAccessible": libraryIDs,
		"mediaProgress":       progress,
		"bookmarks":           []any{},
		"isOldToken":          false,
		"permissions": map[string]any{
			"update":                true,
			"delete":                true,
			"download":              true,
			"accessExplicitContent": true,
		},
	}
}

// audiobookLibraryID returns the ABS-wire library ID string for an
// AudiobookLibrary. Numeric IDs are formatted as decimal strings; when
// the ID is 0 we fall back to the virtual constant.
func audiobookLibraryID(lib AudiobookLibrary) string {
	if lib.ID > 0 {
		return strconv.FormatInt(lib.ID, 10)
	}
	return VirtualLibraryID
}

// audiobookLibraryMap builds the minimal ABS library object shared by
// /libraries (list) and /libraries/{id} (detail).
func audiobookLibraryMap(lib AudiobookLibrary) map[string]any {
	name := strings.TrimSpace(lib.Name)
	if name == "" {
		name = VirtualLibraryName
	}
	return map[string]any{
		"id":        audiobookLibraryID(lib),
		"name":      name,
		"mediaType": LibraryMediaType, // always "book" for audiobook libraries
		"folders": []map[string]any{
			{
				"id":        VirtualFolderID,
				"fullPath":  "/",
				"libraryId": audiobookLibraryID(lib),
			},
		},
		"displayOrder":   1,
		"icon":           "audiobookshelf",
		"settings":       map[string]any{},
		"createdAt":      0,
		"lastUpdate":     0,
	}
}
