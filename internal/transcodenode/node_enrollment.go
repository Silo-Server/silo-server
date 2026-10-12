package transcodenode

import (
	"encoding/json"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/encodinglimit"
	"github.com/Silo-Server/silo-server/internal/nodeenrollment"
)

// SetEnrollmentIdentity is called once before the worker begins serving.
func (s *Server) SetEnrollmentIdentity(id string) { s.enrollmentIdentity = id }
func (s *Server) handleEnrollmentReceipt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.enrollmentIdentity == "" {
		http.Error(w, "node enrollment identity unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(nodeenrollment.Receipt{BindingID: s.enrollmentIdentity, EncodingCapacity: encodinglimit.Snapshot()})
}
