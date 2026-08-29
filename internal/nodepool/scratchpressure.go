package nodepool

import "encoding/json"

// scratchStatsView is the minimal projection this package parses out of an
// otherwise opaque last_stats payload, in the same spirit as
// capabilityDriftView and gpuIdentityView: nodepool must not depend on
// nodemetrics, and admission only needs the scratch volume's fill.
//
// The scratch entry is found by its own flag rather than by matching a path,
// because the API does not know a node's transcode directory — the node does,
// and it says which entry is the one.
type scratchStatsView struct {
	System struct {
		Disks []struct {
			Scratch     bool    `json:"scratch"`
			UsedGB      float64 `json:"used_gb"`
			TotalGB     float64 `json:"total_gb"`
			Stale       bool    `json:"stale"`
			Unavailable bool    `json:"unavailable"`
		} `json:"disks"`
	} `json:"system"`
}

// scratchPressureFillPercent is the scratch-volume fill at which a transcode
// node stops being offered new sessions.
//
// A transcode writes HLS segments to that volume for the life of the session, so
// a node that is nearly full does not fail fast: it accepts the session, streams
// for a while, and then dies mid-playback with a write error — the worst failure
// shape available, because the client has already committed to it. Five percent
// headroom is a few minutes of segments at any realistic bitrate, which is what
// makes it enough to notice and act on rather than a hard stop.
//
// It is deliberately high. A scratch volume sitting at 80% is a normal steady
// state for a node with a large segment retention, and excluding those would
// shrink a healthy cluster for no reason.
const scratchPressureFillPercent = 95

// scratchDiskFillPercent reports the fill percentage of a node's transcode
// scratch volume from its last health sample.
//
// ok is false whenever the answer would be a guess rather than a measurement:
// no sample, an unparseable one, no scratch entry (a node predating the flag, or
// a proxy with no scratch dir), a path the node could not measure, a capacity of
// zero, or numbers the node itself marked stale. Every one of those means the
// admission guard must not fire — excluding a node on a fill we cannot read
// would take capacity away on no evidence, and a full disk that is still being
// written to shows up as a failing transcode, which is recoverable, while an
// empty pool is not.
func scratchDiskFillPercent(n *Node) (pct int, ok bool) {
	if n == nil || len(n.LastStats) == 0 {
		return 0, false
	}
	var view scratchStatsView
	if err := json.Unmarshal(n.LastStats, &view); err != nil {
		return 0, false
	}
	for _, disk := range view.System.Disks {
		if !disk.Scratch {
			continue
		}
		if disk.Unavailable || disk.Stale || disk.TotalGB <= 0 || disk.UsedGB < 0 {
			return 0, false
		}
		// Floored, so the threshold reads as "95% or more of the volume is
		// used" rather than rounding a 94.6% volume into exclusion.
		return int(disk.UsedGB / disk.TotalGB * 100), true
	}
	return 0, false
}

// scratchPressured reports whether a node's scratch volume is too full to admit
// new work. A node whose fill cannot be read is never pressured; see
// scratchDiskFillPercent.
func scratchPressured(n *Node) bool {
	pct, ok := scratchDiskFillPercent(n)
	return ok && pct >= scratchPressureFillPercent
}
