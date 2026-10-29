package handlers

import (
	"context"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/keptsub"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// overrideTagFactsLoader answers one override-tag fact for every track and
// records the files planning asked about.
type overrideTagFactsLoader struct {
	fact  playback.SubtitleFactV3
	files []int
}

func (l *overrideTagFactsLoader) SubtitleTrackFacts(_ context.Context, file *models.MediaFile) playback.SubtitleTrackFactsV3 {
	l.files = append(l.files, file.ID)
	return l
}

func (l *overrideTagFactsLoader) OverrideTags(*models.MediaFile, models.SubtitleTrack) playback.SubtitleFactV3 {
	return l.fact
}

type recordingSubtitlePrefetcher struct {
	mu      sync.Mutex
	started []keptsub.PrefetchRequest
	ended   []string
}

func (p *recordingSubtitlePrefetcher) SessionStarted(req keptsub.PrefetchRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = append(p.started, req)
}

func (p *recordingSubtitlePrefetcher) SessionEnded(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ended = append(p.ended, sessionID)
}

// A Roku-style entry that declares literal_override_tags plays an embedded
// SubRip track natively until the server learns the track carries override
// tags; then the track goes as a sidecar. The start also asks for the
// playing file's tracks, so the facts get learned.
func TestNativeEmbeddedSubtitleFollowsOverrideTagFacts(t *testing.T) {
	for _, tc := range []struct {
		fact       playback.SubtitleFactV3
		wantNative bool
	}{
		{fact: playback.SubtitleFactUnknownV3, wantNative: true},
		{fact: playback.SubtitleFactNoV3, wantNative: true},
		{fact: playback.SubtitleFactYesV3},
	} {
		file := v3HandlerFixtureFile(t)
		file.Container = "mkv"
		file.SubtitleTracks = []models.SubtitleTrack{{Index: 2, ContainerTrackID: "3", Codec: "subrip", Language: "eng"}}
		handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
		handler.ItemAccess = allowAllPlaybackItemAccess{}
		handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
		facts := &overrideTagFactsLoader{fact: tc.fact}
		prefetch := &recordingSubtitlePrefetcher{}
		handler.SubtitleTrackFacts, handler.SubtitlePrefetch = facts, prefetch
		start := v3HandlerStartRequest()
		start.Capabilities.Containers = append(start.Capabilities.Containers, "mkv")
		start.ClientFeatures = append(start.ClientFeatures, playback.FeatureEmbeddedSubtitlesV3)
		start.SubtitleTrackIndex = new(0)
		start.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", 0)
		caps := start.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
		caps.Subtitles.NativeEmbedded = []playback.NativeEmbeddedSubtitleCapabilityV3{{
			Container: "mkv", Codecs: []string{"subrip"}, TrackIdentity: "container_track_id",
			Limitations: []string{playback.NativeEmbeddedLimitationForcedUnlistedV3, playback.NativeEmbeddedLimitationHiddenTrackPreemptionV3, playback.NativeEmbeddedLimitationLiteralOverrideTagsV3},
		}}
		start.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = caps
		started := startPlayablePlaybackV3(t, handler, marshalV3StartRequest(t, start))
		plan := started.PlaybackPlan
		if native := plan.Subtitle.Embedded != nil; native != tc.wantNative || native == (plan.Subtitle.Artifact != nil) {
			t.Fatalf("fact %v: embedded %+v, artifact %+v", tc.fact, plan.Subtitle.Embedded, plan.Subtitle.Artifact)
		}
		if len(facts.files) == 0 || facts.files[0] != file.ID {
			t.Fatalf("facts asked for files %v", facts.files)
		}
		if len(prefetch.started) != 1 || prefetch.started[0].SessionID != started.SessionID || !prefetch.started[0].LearnOverrideTags ||
			prefetch.started[0].Language != "eng" || prefetch.started[0].File.ID != file.ID {
			t.Fatalf("prefetch requests %+v", prefetch.started)
		}
		handler.cancelPlaybackStartSideEffectsV3(t.Context(), started.SessionID)
		if len(prefetch.ended) != 1 || prefetch.ended[0] != started.SessionID {
			t.Fatalf("prefetch ended for %v", prefetch.ended)
		}
	}
}
