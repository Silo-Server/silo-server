package playback

import (
	"reflect"
	"testing"
)

// The admin views name a plan's subtitle delivery from its decision, so each
// carrier must map to its own value and a plan that shows none to nil.
func TestSessionSubtitleFromPlanV3(t *testing.T) {
	inventory := []SubtitleInventoryItemV3{
		{TrackID: "file:1:subtitle:0", Source: SubtitleSourceEmbeddedV3, Codec: "hdmv_pgs_subtitle", Language: "en", Forced: true},
		{TrackID: "file:1:subtitle:1", Source: SubtitleSourceExternalV3, Codec: "subrip", Language: "fr", HearingImpaired: true},
	}
	for _, tc := range []struct {
		name     string
		decision SubtitleDecisionV3
		want     *SessionSubtitle
	}{
		{"off", SubtitleDecisionV3{Mode: SubtitleOffV3}, nil},
		{"burn in", SubtitleDecisionV3{Mode: SubtitleBurnInV3, TrackID: "file:1:subtitle:0"},
			&SessionSubtitle{TrackID: "file:1:subtitle:0", Language: "en", Codec: "hdmv_pgs_subtitle", Source: "embedded", Delivery: "burn_in", Forced: true}},
		{"embedded", SubtitleDecisionV3{Mode: SubtitleRenderV3, TrackID: "file:1:subtitle:0", Embedded: &EmbeddedSubtitleV3{StreamIndex: 3}},
			&SessionSubtitle{TrackID: "file:1:subtitle:0", Language: "en", Codec: "hdmv_pgs_subtitle", Source: "embedded", Delivery: "embedded", Forced: true}},
		{"hls rendition", SubtitleDecisionV3{Mode: SubtitleConvertV3, TrackID: "file:1:subtitle:1", HLSRendition: &HLSSubtitleRenditionV3{Format: "vtt"}},
			&SessionSubtitle{TrackID: "file:1:subtitle:1", Language: "fr", Codec: "subrip", Format: "vtt", Source: "external", Delivery: "hls_rendition", HearingImpaired: true}},
		{"sidecar", SubtitleDecisionV3{Mode: SubtitleConvertV3, TrackID: "file:1:subtitle:1", Artifact: &SubtitleArtifactV3{Format: "vtt"}},
			&SessionSubtitle{TrackID: "file:1:subtitle:1", Language: "fr", Codec: "subrip", Format: "vtt", Source: "external", Delivery: "sidecar", HearingImpaired: true}},
		{"unlisted track", SubtitleDecisionV3{Mode: SubtitleBurnInV3, TrackID: "file:1:subtitle:9"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.decision.Inventory = inventory
			if got := SessionSubtitleFromPlanV3(&PlanV3{Subtitle: tc.decision}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("subtitle = %+v, want %+v", got, tc.want)
			}
		})
	}
}
