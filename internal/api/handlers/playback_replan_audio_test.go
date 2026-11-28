package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// refusingSurroundAdmissionV3 is the live session manager of a server whose
// operator disabled audio conversion: a replacement that converts audio is
// refused at admission, as a surround conversion is when the account may not
// transcode.
type refusingSurroundAdmissionV3 struct{ *playback.SessionManager }

func (m refusingSurroundAdmissionV3) CheckReplacementAllowed(ctx context.Context, sessionID string, method playback.PlayMethod, transcodeAudio bool) error {
	if transcodeAudio {
		return playback.ErrAudioTranscodingDisabled
	}
	return m.SessionManager.CheckReplacementAllowed(ctx, sessionID, method, transcodeAudio) //nolint:wrapcheck // A test double passes the manager's result through.
}

// A replan that moves to another version of the title carries the audio track
// the viewer chose onto it, at the index that version gives it. When its
// surround conversion is then refused, the replacement is rebuilt on that same
// version and that same track: the playing version's index points at another
// track of the new file.
//
// The source has one DTS 5.1 track at index 0. The lower version lists an AAC
// stereo track first, so the same DTS track is index 1 there.
func TestReplanToAnotherVersionKeepsTheChosenAudioWhenItsSurroundConversionIsRefused(t *testing.T) {
	state := &directStateV3{}
	f := newDirectStartV3(t, state, 0)
	f.request.MediaTransport = playback.MediaTransportRegularV3
	dts := models.AudioTrack{Codec: "dts", Channels: 6, Layout: "5.1(side)", Default: true}
	files := f.handler.fileResolver.(mapPlaybackFileResolver).files
	source, lower := files[f.request.FileID], files[84]
	source.CodecAudio, source.AudioChannels, source.AudioTracks = "dts", 6, []models.AudioTrack{dts}
	lower.CodecAudio, lower.AudioChannels = "dts", 6
	lower.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}, dts}
	f.request.ClientFeatures = append(f.request.ClientFeatures, playback.FeatureLayoutPassthrough)
	f.request.Capabilities.CodecsAudio = []string{"aac", "eac3", "dts"}
	f.request.Capabilities.AudioPassthrough = &playback.AudioPassthroughV3{PassthroughCodecs: []string{"eac3"}, MaxChannels: 6, Entries: []playback.AudioPassthroughEntryV3{{Codec: "eac3", ChannelCounts: []int{2, 6}, Layouts: []string{"stereo", "5.1"}}}}
	f.handler.sessionMgr = refusingSurroundAdmissionV3{playback.NewSessionManager(0, 0)}
	f.handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{TranscodeEnabled: true, Routing: config.DefaultPlaybackRoutingPolicy()}
	}
	presetLocalRegistryV3(f.handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationAudioToEAC3V3, RecipeVersion: playback.TransformationAudioToEAC3RecipeVersionV3, Available: true},
	}))
	started, err := f.handler.StartPlaybackV2(newAuthorizedPlaybackContext(), f.caller, f.request)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: err=%v terminal=%+v", err, started.Terminal)
	}
	plan := started.PlaybackPlan

	// The viewer asks for Auto under a budget the source does not fit and the
	// lower version does, and the client now reports the sink that makes the
	// DTS track a surround conversion. The estimate names no source: a source
	// needs the buffering_quality_v1 feature, and the budget needs neither.
	estimate := 45_000
	request := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationQualityChangeV3,
		PlaybackAttemptID: f.request.PlaybackAttemptID, ReplanRequestID: "audio-index-request",
		FailedPlanID: plan.PlanID, PlanAttemptID: "audio-index-attempt", PlanAttemptKey: plan.PlanAttemptKey,
		AttemptCount: 1, PositionSeconds: 30, QualityPreference: playback.QualityAutoV3, BandwidthEstimateKbps: &estimate,
		SelectedTracks: plan.SelectedTracks, MediaTransport: playback.MediaTransportRegularV3,
		ClientFeatures: f.request.ClientFeatures, Capabilities: f.request.Capabilities, ClientPlaybackContext: f.request.ClientPlaybackContext,
	}
	request.ClientPlaybackContext.Output.AudioPassthrough = f.request.Capabilities.AudioPassthrough
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true, AudioPassthroughCodecs: []string{"eac3"}}

	replanned, err := f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), f.caller, started.SessionID, PlaybackReplanCommand{Request: request, Digest: "audio-index-digest"})

	if err != nil || replanned.PlaybackPlan == nil {
		t.Fatalf("replan: err=%v terminal=%+v; want a plan: the refused conversion falls back to direct play of the DTS track at its index on the lower version (1, the container default there), and with the playing version's index (0, the AAC track) the fallback needs a remux the server does not offer and the refusal stands", err, replanned.Terminal)
	}
	got := replanned.PlaybackPlan
	if got.EffectiveMediaFileID != lower.ID {
		t.Fatalf("fixture: replan played file %d, want the lower version %d", got.EffectiveMediaFileID, lower.ID)
	}
	if audio := got.SelectedTracks.Audio; audio == nil || audio.Index == nil || *audio.Index != 1 {
		t.Fatalf("replan after the refused conversion selected audio %+v, want index 1: the DTS track on the lower version", audio)
	}
}
