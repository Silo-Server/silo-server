package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The start's audio refinement asks the planner, so a track counts as
// unconverted only when its real route copies it.
func TestPreferUnconvertedAudioTrackV3FollowsThePlanner(t *testing.T) {
	tracks := []models.AudioTrack{
		{Language: "en", Codec: "truehd", Channels: 8, Layout: "7.1", Default: true},
		{Language: "en", Codec: "eac3", Channels: 6, Layout: "5.1(side)"},
	}
	stereoAACV3 := func(r StartRequestV3) StartRequestV3 {
		for class, delivery := range r.ClientPlaybackContext.Deliveries {
			delivery.MaxChannelsByCodec = map[string]int{"aac": 2}
			r.ClientPlaybackContext.Deliveries[class] = delivery
		}
		return r
	}
	for _, test := range []struct {
		name    string
		tracks  []models.AudioTrack
		request func(StartRequestV3) StartRequestV3
		want    int
	}{
		{name: "E-AC-3 copied over a TrueHD conversion", request: func(r StartRequestV3) StartRequestV3 { return r }, want: 1},
		{
			// The original's container is unsupported and HLS decodes only
			// AAC, so E-AC-3 would be downmixed to stereo AAC there while
			// TrueHD keeps 5.1 AAC.
			name: "no swap when the other track is converted too",
			request: func(r StartRequestV3) StartRequestV3 {
				r.Capabilities.Containers = []string{"mp4", "m3u8", "hls"}
				original := r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
				original.Containers = []string{"mp4"}
				r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = original
				hls := r.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
				hls.AudioDecodeCodecs = []string{"aac"}
				r.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
				return r
			},
			want: 0,
		},
		{
			// A device that decodes AAC only in stereo: multichannel AAC is
			// converted like TrueHD, while stereo AAC is copied and keeps
			// every channel the conversion would.
			name:    "a codec ceiling counts as a conversion",
			tracks:  []models.AudioTrack{tracks[0], {Language: "en", Codec: "aac", Channels: 6, Layout: "5.1"}},
			request: stereoAACV3,
			want:    0,
		},
		{
			name:    "a codec ceiling sets the conversion's channels",
			tracks:  []models.AudioTrack{tracks[0], {Language: "en", Codec: "aac", Channels: 2, Layout: "stereo"}},
			request: stereoAACV3,
			want:    1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fileTracks := tracks
			if test.tracks != nil {
				fileTracks = test.tracks
			}
			file, request := androidMedia3FFmpegFixtureV3("tv", fileTracks, []string{"aac", "ac3", "eac3"}, 0)
			request = test.request(request)
			outcome := func(index int) StartAudioOutcomeV3 {
				return StartAudioOutcomeFromResultV3(PlanPlaybackV3(PlannerInputV3{
					Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: index,
					Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
				}))
			}
			if got := PreferUnconvertedAudioTrackV3(file, 0, outcome); got != test.want {
				t.Fatalf("PreferUnconvertedAudioTrackV3 = %d, want %d (outcomes %+v, %+v)", got, test.want, outcome(0), outcome(1))
			}
		})
	}
}
