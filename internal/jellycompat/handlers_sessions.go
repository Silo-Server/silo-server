package jellycompat

import "net/http"

// sessionInfoDTO is a minimal subset of Jellyfin's SessionInfoDto. It exists to
// give GET /Sessions a correctly-typed array element; the list is currently
// always empty (see HandleSessions), so only the contract shape matters.
type sessionInfoDTO struct {
	ID         string `json:"Id"`
	UserID     string `json:"UserId"`
	UserName   string `json:"UserName,omitempty"`
	Client     string `json:"Client,omitempty"`
	DeviceID   string `json:"DeviceId,omitempty"`
	DeviceName string `json:"DeviceName,omitempty"`
}

// HandleSessions serves GET /Sessions. Jellyfin returns the caller's visible
// sessions (SessionInfoDto[]); clients such as Wholphin poll it (optionally
// filtered by ?deviceId=) every few seconds while playing to read their own
// session state. The route was unregistered, so those polls hit a chi 404 that
// the jellyfin-sdk could not deserialize — observed as a ~289-per-4h 404 storm.
//
// Silo does not yet expose live session/now-playing state through the compat
// surface, so this returns a contract-shaped empty list: it stops the 404 storm
// and lets clients degrade cleanly. Populating it from the playback session
// store (so the now-playing/transcoding overlay works) is a follow-up.
func HandleSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []sessionInfoDTO{})
}
