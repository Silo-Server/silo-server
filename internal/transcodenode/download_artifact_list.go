package transcodenode

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadstorage"
)

// artifactListTimeout bounds how long the listing route waits for the
// directory read; s.artifactLister admits one read at a time.
const artifactListTimeout = 15 * time.Second

// handleListDownloadArtifacts lists the files in this node's prepared-download
// directory, with the measurement they add up to, so the API can find files no
// download_artifacts row accounts for. The answer carries the directory path,
// which is why the route requires the node bearer.
func (s *Server) handleListDownloadArtifacts(w http.ResponseWriter, r *http.Request) {
	if s.artifactRoot == "" {
		http.Error(w, "artifact directory unavailable", http.StatusServiceUnavailable)
		return
	}
	listing, err := s.artifactLister.Inspect(r.Context(), s.artifactRoot, s.transcodeDir, artifactListTimeout)
	switch {
	case errors.Is(err, downloadstorage.ErrDirBusy):
		http.Error(w, "an artifact listing is already running", http.StatusServiceUnavailable)
	case errors.Is(err, downloadstorage.ErrDirTimeout):
		http.Error(w, "artifact directory did not answer in time", http.StatusServiceUnavailable)
	case err != nil:
		// The request ended; nobody is waiting for an answer.
	case listing.Usage.Error != "" && len(listing.Files) == 0:
		http.Error(w, "artifact directory unreadable", http.StatusServiceUnavailable)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listing)
	}
}
