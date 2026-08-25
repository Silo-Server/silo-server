package handlers

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// historyImportCapabilityResponse describes what a client may send when it
// starts a Plex history import.
//
// The case it exists for is plex_base_urls. A server predating advertised
// connection fallback ignores the field and silently imports against whichever
// single address the client happened to prefer; when that address is
// unreachable the run fails with a connection error indistinguishable from the
// server genuinely being down. A client that reads plex_connection_fallback
// false knows to keep probing addresses itself (or to say the deployment is out
// of date) rather than blaming the Plex server.
type historyImportCapabilityResponse struct {
	SchemaVersion int `json:"schema_version"`
	// PlexConnectionFallback reports that a run accepts plex_base_urls, and
	// that the server races the advertised connections and imports from the
	// first one that answers. Always true here; named so a future removal is
	// detectable rather than silent.
	PlexConnectionFallback bool `json:"plex_connection_fallback"`
	// MaxPlexConnectionCandidates is how many addresses one run will race,
	// counting the preferred one. A client that sends more has the surplus
	// dropped from the end, so it should order plex_base_urls by preference
	// rather than assume every address is tried.
	MaxPlexConnectionCandidates int `json:"max_plex_connection_candidates"`
}

// HandleCapability reports the history-import features this server supports.
//
// Per the v1 rules, new functionality is feature-detected rather than inferred
// from a version. This follows the existing per-subsystem convention
// (/events/capability, /playback/capability, /downloads/capability).
func (h *HistoryImportHandler) HandleCapability(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, historyImportCapabilityResponse{
		SchemaVersion:               1,
		PlexConnectionFallback:      true,
		MaxPlexConnectionCandidates: historyimport.MaxPlexConnectionCandidates,
	})
}
