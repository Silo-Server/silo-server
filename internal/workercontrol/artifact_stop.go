package workercontrol

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

func (r *Runtime) serveArtifactStop(w http.ResponseWriter, request *http.Request) {
	if err := r.verifyCommand(request); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	work := outputPathWork(request.URL.Path)
	encoded := request.Header.Get(workeridentity.HeaderGeneration)
	cutoff, err := strconv.ParseUint(encoded, 10, 64)
	if err != nil || strconv.FormatUint(cutoff, 10) != encoded || work == "" || request.Header.Get(workeridentity.HeaderWorkID) != work {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	known := r.generations[work] != 0
	generations := make(map[uint64]bool)
	if current := r.generations[work]; current != 0 && current <= cutoff {
		generations[current] = true
	}
	for generation := range r.staged[work] {
		if generation <= cutoff {
			generations[generation] = true
		}
	}
	r.mu.Unlock()
	// A restarted worker retains artifact bytes but no local generation map.
	// Recover signed active/candidate authority before waiting on publication.
	if !known {
		if err := r.refresh(request.Context()); err != nil {
			http.Error(w, "artifact delete authority unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	ctx := transcodenode.WithArtifactDeleteScope(request.Context(), work, func() (bool, error) {
		return r.artifactDeleteAllowed(work, cutoff)
	})
	response := &stopResponse{header: make(http.Header)}
	r.handler.ServeHTTP(response, request.WithContext(ctx))
	for generation := range generations {
		r.revoked(work, generation)
	}
	for key, values := range response.header {
		w.Header()[key] = values
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if response.body.Len() > 0 {
		_, _ = w.Write(response.body.Bytes())
	}
}

// Called under artifact lifecycle ownership. All checks are local; staged
// ownership remains visible until its atomic promotion to output-only work.
func (r *Runtime) artifactDeleteAllowed(work string, cutoff uint64) (bool, error) {
	r.mu.Lock()
	current := r.generations[work]
	latest := current
	for generation := range r.staged[work] {
		latest = max(latest, generation)
	}
	r.mu.Unlock()
	observed, fresh := r.authority.ObservedGeneration(work)
	if !fresh {
		return false, errors.New("artifact generation requires current core authority")
	}
	if max(latest, observed) > cutoff {
		return false, nil
	}
	return true, nil
}
