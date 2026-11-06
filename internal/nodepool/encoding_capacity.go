package nodepool

import (
	"encoding/json"
	"math"
)

// encodingCapacity is reported by the worker, independently of the customer's
// editable max_jobs. The worker itself enforces this process-lifetime ceiling.
type encodingCapacity struct {
	MaxJobs    int `json:"max_jobs"`
	ActiveJobs int `json:"active_jobs"`
}

func reportedEncodingCapacity(n *Node) *encodingCapacity {
	var stats struct {
		Capacity *encodingCapacity `json:"encoding_capacity"`
	}
	if json.Unmarshal(n.LastStats, &stats) != nil || stats.Capacity == nil || stats.Capacity.MaxJobs < 0 || stats.Capacity.MaxJobs > math.MaxInt32 || stats.Capacity.ActiveJobs < 0 {
		return nil
	}
	return stats.Capacity
}

// clampEncodingCap reconciles the customer's configuration with the worker's
// finite ceiling. An unlimited worker never changes an existing configuration.
func clampEncodingCap(n *Node) {
	capacity := reportedEncodingCapacity(n)
	if n.Type != NodeTypeTranscode || capacity == nil || capacity.MaxJobs <= 0 {
		return
	}
	if n.MaxJobs == nil || *n.MaxJobs <= 0 || *n.MaxJobs > capacity.MaxJobs {
		limit := capacity.MaxJobs
		n.MaxJobs = &limit
	}
}
