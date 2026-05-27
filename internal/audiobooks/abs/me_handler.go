package abs

import (
	"net/http"
	"strconv"
	"strings"
)

// handleMe — GET /abs/api/me (and /api/me)
//
// Minimal {id, username, defaultLibraryId} envelope — matches the
// continuum-plugin-audiobooks shape exactly. ABS clients already
// have the full user object from /login and /authorize; /me is just
// a session-resume probe in real-ABS, so returning the rich user
// envelope here is unnecessary and can confuse clients that pattern-
// match on the minimal shape.
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

	writeJSON(w, http.StatusOK, map[string]any{
		"id":               a.UserID,
		"username":         a.UserID,
		"defaultLibraryId": defaultLibID,
	})
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
// /libraries (list) and /libraries/{id} (detail). Plugin parity:
// {id, name, mediaType} only — the extra folders/displayOrder/icon
// fields some servers emit are ignored by real ABS clients and adding
// them risks behaviour drift.
func audiobookLibraryMap(lib AudiobookLibrary) map[string]any {
	name := strings.TrimSpace(lib.Name)
	if name == "" {
		name = VirtualLibraryName
	}
	return map[string]any{
		"id":        audiobookLibraryID(lib),
		"name":      name,
		"mediaType": LibraryMediaType,
	}
}
