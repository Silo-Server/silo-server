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

// The reservation must count the work the node actually runs. A prepared track
// layout encodes every audio track it cannot copy into MP4, even when the
// top-level audio target is "copy", and an audio-only preparation maps no
// video, whatever its video target.
func TestDownloadReservesTheWorkItRuns(t *testing.T) {
	cases := []struct {
		name    string
		request downloadprepare.Request
		want    nodepool.JobKind
	}{
		{
			name: "a track layout that encodes one audio track",
			request: downloadprepare.Request{
				TargetCodecVideo:   "copy",
				TargetCodecAudio:   "copy",
				TrackRecipeVersion: playback.PreparedTracksRecipeVersion,
				PreparedTracks: &playback.PreparedTracks{Audio: []playback.PreparedAudioTrack{
					{SourceIndex: 0, Codec: playback.PreparedAudioCopy},
					{SourceIndex: 1, Codec: playback.PreparedAudioAAC, SourceChannels: 8},
				}},
			},
			want: nodepool.JobKindAudioTranscode,
		},
		{
			name: "an audio-only download with a video target",
			request: downloadprepare.Request{
				TargetCodecVideo: "h264",
				TargetCodecAudio: "aac",
				AudioOnly:        true,
			},
			want: nodepool.JobKindAudioTranscode,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planner := &kindRecordingPlanner{nonReservingCapacityPlanner: nonReservingCapacityPlanner{node: &nodepool.Node{URL: "http://node"}}}
			preparer := &NodeAwarePreparer{planner: planner}

			preparer.reserveNode(t.Context(), "artifact", tc.request, playback.TranscodeOpts{}, nil)
			if planner.kind != tc.want {
				t.Fatalf("reserved as %q, want %q", planner.kind, tc.want)
			}
		})
	}
}
