package transcodenode

import (
	"maps"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// Vio keeps the node's job cap and the refusals it publishes, but not
// upstream's admission lifecycle (preparations, starts, remux notes), which
// the fork's DB-backed node does not use.

// refusalRetryAfter is the Retry-After of a capacity or storage refusal, in
// seconds.
const refusalRetryAfter = "5"

const (
	errNodeAtCapacity = "node is at its job limit"
)

// SetJobCap sets the node's own job cap, 0 for none.
func (s *Server) SetJobCap(limit int) {
	s.jobCap.Store(int32(limit))
}

// jobSlot is new work admitted under the job cap and not yet counted in
// activeJobs. The admitted-but-uncounted slots count against the cap too, so
// two starts arriving together at the cap less one cannot both pass.
type jobSlot struct {
	s    *Server
	done bool
}

// admitJob admits new work under the job cap: work that is not a session,
// remux or preparation the node already holds. It reports false at the cap.
// The caller counts the slot with countJob once the job runs, and releases
// it on every other return (release after count does nothing).
func (s *Server) admitJob() (*jobSlot, bool) {
	s.admission.Lock()
	defer s.admission.Unlock()
	if s.fullLocked() {
		return nil, false
	}
	s.admitted++
	return &jobSlot{s: s}, true
}

// countJob counts a job in activeJobs: its admitted slot when it was new
// work, or none for work the node already held, which the cap never refuses.
func (s *Server) countJob(slot *jobSlot) {
	if slot == nil {
		s.activeJobs.Add(1)
		return
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	if slot.done {
		return
	}
	slot.done = true
	s.admitted--
	s.activeJobs.Add(1)
}

// release gives back a slot that was never counted.
func (slot *jobSlot) release() {
	if slot == nil {
		return
	}
	slot.s.admission.Lock()
	defer slot.s.admission.Unlock()
	if !slot.done {
		slot.done = true
		slot.s.admitted--
	}
}

// fullLocked reports whether the counted and admitted jobs fill the cap. The
// caller holds s.admission.
func (s *Server) fullLocked() bool {
	limit := int(s.jobCap.Load())
	return limit > 0 && int(s.activeJobs.Load())+s.admitted >= limit
}

// setRefusal adds the headers of a capacity or storage refusal.
func setRefusal(w http.ResponseWriter, reason string) {
	w.Header().Set("Retry-After", refusalRetryAfter)
	w.Header().Set(nodepool.NodeRefusalHeader, reason)
}

// writeAtCapacity refuses new work at the job cap: 503, Retry-After and
// RefusalAtCapacity, a plain-text body as a draining refusal has.
func writeAtCapacity(w http.ResponseWriter) {
	setRefusal(w, nodepool.RefusalAtCapacity)
	http.Error(w, errNodeAtCapacity, http.StatusServiceUnavailable)
}

// jobCapRefusalHeaders are the headers of a 503 that refuses new work at the
// node's own job cap or declared budget, for the routes' protocol entries.
func jobCapRefusalHeaders() map[string]*huma.Param {
	return map[string]*huma.Param{
		nodepool.NodeRefusalHeader: {Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{nodepool.RefusalAtCapacity, nodepool.RefusalStorageFull}},
			Description: "Why the node refused new work: at_capacity at the job cap it declares (SILO_NODE_MAX_TRANSCODES), storage_full at the prepared-download budget it declares. Absent on every other 503, a draining refusal included; a caller that does not know it treats the 503 as a draining refusal."},
		"Retry-After": {Schema: &huma.Schema{Type: huma.TypeString}, Description: "Seconds, with the refusal header."},
	}
}

// withJobCapRefusal adds the refusal headers to an operation's 503 and notes
// the cap in its description.
func withJobCapRefusal(op workerprotocol.Operation, continued string) workerprotocol.Operation {
	response := op.Responses["503"]
	if response.Headers == nil {
		response.Headers = map[string]*huma.Param{}
	}
	maps.Copy(response.Headers, jobCapRefusalHeaders())
	op.Description += " A node that declares a job cap refuses new work once it runs that many jobs (the jobs health reports as active_jobs, admitted and not yet counted included) with 503, " +
		nodepool.NodeRefusalHeader + ": " + nodepool.RefusalAtCapacity + " and Retry-After"
	if continued != "" {
		op.Description += "; " + continued + " continues a job the node holds and is never refused at the cap"
	}
	op.Description += "."
	return op
}
