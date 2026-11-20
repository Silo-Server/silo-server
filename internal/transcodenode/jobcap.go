package transcodenode

import (
	"maps"
	"math"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
)

// Vio keeps the node's job cap and the refusals it publishes, but not
// upstream's admission lifecycle (preparations, starts, remux notes), which
// the fork's DB-backed node does not use.

// SetJobCap sets the node's own job cap, 0 for none. A cap above the int32
// range is clamped: the value only ever guards an admission counter.
func (s *Server) SetJobCap(limit int) {
	s.jobCap.Store(int32(min(max(limit, 0), math.MaxInt32)))
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
