package nodepool

import (
	"encoding/json"
	"strings"
)

// JobKind classifies the work a transcode node runs, so the planner can spread
// each kind evenly across nodes instead of only the total job count. Two jobs
// of different kinds cost very different amounts: a video encode holds a GPU
// encoder for the length of a stream, while a remux that copies both streams
// costs a little disk and network. Balancing the total alone can leave one
// node's GPU saturated with encodes while another runs only remuxes.
type JobKind string

// A playback stream and a prepared download are classed by the work they do,
// not by what they are for: a download that tone-maps is a video encode on the
// GPU like any playback encode, and must count against the same kind.
const (
	// JobKindVideoTranscode re-encodes the video stream (tone mapping and
	// subtitle burn-in included), whatever happens to the audio.
	JobKindVideoTranscode JobKind = "video_transcode"
	// JobKindAudioTranscode copies the video stream, or has none, and
	// re-encodes the audio: a remux with audio conversion, or a theme
	// conversion.
	JobKindAudioTranscode JobKind = "audio_transcode"
	// JobKindRemux copies every stream into a new container.
	JobKindRemux JobKind = "remux"
	// JobKindTrickplay extracts trickplay thumbnails.
	JobKindTrickplay JobKind = "trickplay"
)

// JobKinds lists every job kind, in the order a node reports them.
var JobKinds = []JobKind{JobKindVideoTranscode, JobKindAudioTranscode, JobKindRemux, JobKindTrickplay}

// StreamJobKind classifies a stream or download from what it re-encodes.
func StreamJobKind(transcodeVideo, transcodeAudio bool) JobKind {
	switch {
	case transcodeVideo:
		return JobKindVideoTranscode
	case transcodeAudio:
		return JobKindAudioTranscode
	default:
		return JobKindRemux
	}
}

// CodecJobKind classifies a stream or download from its target codecs, where
// "copy" keeps the source stream and anything else re-encodes it.
func CodecJobKind(targetVideoCodec, targetAudioCodec string) JobKind {
	return StreamJobKind(!isCopyCodec(targetVideoCodec), !isCopyCodec(targetAudioCodec))
}

func isCopyCodec(codec string) bool {
	return strings.EqualFold(strings.TrimSpace(codec), "copy")
}

// jobsByKindStatsView is the part of a node's stored last_stats that carries
// its active jobs by kind and its caps by kind; see marshalLastStats.
type jobsByKindStatsView struct {
	ActiveJobsByKind map[JobKind]int `json:"active_jobs_by_kind"`
	JobCapsByKind    map[JobKind]int `json:"job_caps_by_kind"`
}

// reportedJobsByKind returns the active jobs by kind from the node's last
// health report. ok is false when the node did not report a breakdown (a build
// from before job kinds), which callers must treat as "cannot say", never as
// zero: a node that reports nothing would otherwise always look idle.
func reportedJobsByKind(n *Node) (jobs map[JobKind]int, ok bool) {
	view, ok := reportedJobStats(n)
	return view.ActiveJobsByKind, ok
}

// reportedJobStats returns the node's last reported jobs by kind and caps by
// kind (SILO_NODE_MAX_VIDEO_TRANSCODES and the like; a kind without a cap is
// absent). ok is false when the node reported no breakdown, which it always
// sends with its caps.
func reportedJobStats(n *Node) (view jobsByKindStatsView, ok bool) {
	if n == nil || len(n.LastStats) == 0 {
		return jobsByKindStatsView{}, false
	}
	if err := json.Unmarshal(n.LastStats, &view); err != nil || view.ActiveJobsByKind == nil {
		return jobsByKindStatsView{}, false
	}
	return view, true
}
