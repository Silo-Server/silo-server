package downloads

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// kindRecordingPlanner records the job kind a preparation reserves under.
type kindRecordingPlanner struct {
	nonReservingCapacityPlanner
	kind nodepool.JobKind
}

func (p *kindRecordingPlanner) ReserveTranscodeWork(_ string, kind nodepool.JobKind) (*nodepool.Node, func()) {
	p.kind = kind
	return nil, nil
}

func (p *kindRecordingPlanner) ReserveTranscodeWorkWith(_ string, kind nodepool.JobKind, _ func(*nodepool.Node) bool) (*nodepool.Node, func()) {
	p.kind = kind
	return nil, nil
}

// A prepared track layout encodes every audio track it cannot copy into MP4,
// even when the top-level audio target is "copy". The reservation must count
// that encode as audio work, the way the node runs it.
func TestPreparedTrackDownloadReservesTheAudioWorkItEncodes(t *testing.T) {
	planner := &kindRecordingPlanner{nonReservingCapacityPlanner: nonReservingCapacityPlanner{node: &nodepool.Node{URL: "http://node"}}}
	preparer := &NodeAwarePreparer{planner: planner}
	request := downloadprepare.Request{
		TargetCodecVideo:   "copy",
		TargetCodecAudio:   "copy",
		TrackRecipeVersion: playback.PreparedTracksRecipeVersion,
		PreparedTracks: &playback.PreparedTracks{Audio: []playback.PreparedAudioTrack{
			{SourceIndex: 0, Codec: playback.PreparedAudioCopy},
			{SourceIndex: 1, Codec: playback.PreparedAudioAAC, SourceChannels: 8},
		}},
	}

	preparer.reserveNode(t.Context(), "artifact", request, playback.TranscodeOpts{}, nil)
	if planner.kind != nodepool.JobKindAudioTranscode {
		t.Fatalf("reserved as %q, want %q", planner.kind, nodepool.JobKindAudioTranscode)
	}
}
