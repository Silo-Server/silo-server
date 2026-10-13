package transcodenode

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// prepareProgressRegistry holds the live reading of every prepare encode in
// flight on this node, keyed by the attempt's artifact id. It is memory only:
// the API polls it while its synchronous prepare request is open, so nothing
// here needs to survive a restart.
type prepareProgressRegistry struct {
	mu      sync.Mutex
	entries map[string]*prepareProgressEntry
}

type prepareProgressEntry struct {
	mu      sync.Mutex
	reading playback.PrepareProgress
}

func (e *prepareProgressEntry) PrepareProgress(p playback.PrepareProgress) {
	e.mu.Lock()
	e.reading = p
	e.mu.Unlock()
}

func (e *prepareProgressEntry) snapshot() playback.PrepareProgress {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reading
}

// begin registers an encode. The artifact lifecycle lock already serializes
// encodes per id, so an existing entry can only be a finished attempt's.
func (r *prepareProgressRegistry) begin(artifactID string, duration float64) *prepareProgressEntry {
	entry := &prepareProgressEntry{reading: playback.PrepareProgress{DurationSeconds: max(duration, 0)}}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]*prepareProgressEntry)
	}
	r.entries[artifactID] = entry
	return entry
}

// end removes entry only if it is still the registered one.
func (r *prepareProgressRegistry) end(artifactID string, entry *prepareProgressEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[artifactID] == entry {
		delete(r.entries, artifactID)
	}
}

func (r *prepareProgressRegistry) get(artifactID string) (*prepareProgressEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[artifactID]
	return entry, ok
}

// handleDownloadPrepareProgress reports the live reading for one attempt. An
// unknown id is answered with running=false rather than 404: the API treats
// 404 as a node that predates this endpoint.
func (s *Server) handleDownloadPrepareProgress(w http.ResponseWriter, r *http.Request) {
	artifactID := chi.URLParam(r, "artifact_id")
	if !downloadprepare.ValidArtifactID(artifactID) {
		http.Error(w, "invalid artifact id", http.StatusBadRequest)
		return
	}
	body := downloadprepare.Progress{ArtifactID: artifactID}
	if entry, ok := s.prepareProgress.get(artifactID); ok {
		reading := entry.snapshot()
		body.Running = true
		body.EncodedSeconds = reading.EncodedSeconds
		body.DurationSeconds = reading.DurationSeconds
		body.Speed = reading.Speed
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}
