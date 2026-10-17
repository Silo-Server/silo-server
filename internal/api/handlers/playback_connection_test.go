package handlers

import (
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestBufferedAutoPrefersCopyVersion(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Bitrate = 63000
	source.VideoTracks[0].Bitrate = 63000
	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 84
	alternate.Bitrate = 31600
	alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	alternate.VideoTracks[0].Bitrate = 31600
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}})
	h.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	req := v3HandlerStartRequest()
	req.Capabilities.VideoDecode[0].MaxBitrateKbps = 100_000
	req.QualityPreference = "auto"
	req.BandwidthEstimateKbps = new(44100)
	req.BandwidthEstimateSource = "observed_rebuffer"
	req.ClientFeatures = append(req.ClientFeatures, playback.FeatureBufferingQualityV1)
	r := httptest.NewRequest("POST", "/", nil).WithContext(newAuthorizedPlaybackContext())
	chosen, _, _ := h.lowerBufferedVersion(r, req, source, 0, h.plannerSettingsV3(r.Context()))
	if chosen.ID != alternate.ID {
		t.Fatalf("selected %d, want lower original %d", chosen.ID, alternate.ID)
	}
	req.AllowAlternateVersions = new(false)
	chosen, _, _ = h.lowerBufferedVersion(r, req, source, 0, h.plannerSettingsV3(r.Context()))
	if chosen.ID != source.ID {
		t.Fatal("fixed-file request changed version")
	}
	req.AllowAlternateVersions = nil
	req.BandwidthEstimateSource = "probe"
	chosen, _, _ = h.lowerBufferedVersion(r, req, source, 0, h.plannerSettingsV3(r.Context()))
	if chosen.ID != source.ID {
		t.Fatal("legacy probe behavior changed")
	}
}

func TestBufferedHDRChoosesSDRVersionWithoutToneMap(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Bitrate = 63000
	source.Resolution = "4k"
	source.CodecVideo = "hevc"
	source.VideoTracks[0] = models.VideoTrack{Codec: "hevc", Width: 3840, Height: 2160, BitDepth: 10, Bitrate: 63000, VideoRange: "HDR", VideoRangeType: "HDR10"}
	alternate := v3HandlerFixtureFile(t)
	alternate.ID = 84
	alternate.Bitrate = 31600
	alternate.VideoTracks[0].Bitrate = 31600
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}})
	h.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	req := v3HandlerStartRequest()
	req.Capabilities.VideoDecode[0].MaxBitrateKbps = 100000
	req.QualityPreference = playback.QualityAutoV3
	req.BandwidthEstimateKbps = new(44100)
	req.BandwidthEstimateSource = playback.BandwidthEstimateObservedRebufferV3
	r := httptest.NewRequest("POST", "/", nil).WithContext(newAuthorizedPlaybackContext())
	chosen, _, _ := h.lowerBufferedVersion(r, req, source, 0, playback.PlannerSettingsV3{})
	if chosen.ID != alternate.ID {
		t.Fatalf("HDR without tone-map policy chose %d, want SDR original %d", chosen.ID, alternate.ID)
	}
}

func TestPlaybackCallerKeepsConnectionDeviceSeparate(t *testing.T) {
	for _, device := range []string{"declared-device", ""} {
		r := playbackCallerRequest(newAuthorizedPlaybackContext(), PlaybackCaller{DeviceID: "playback-device", ConnectionDeviceID: device})
		got, _ := r.Context().Value(playbackConnectionDeviceContextKey{}).(string)
		if got != device {
			t.Fatalf("connection device = %q, want %q", got, device)
		}
		if r.Header.Get(deviceIDHeader) != "playback-device" {
			t.Fatal("changed existing playback device identity")
		}
	}
}

func TestLearnedLimitAnnotationRequiresAdditionalReduction(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Bitrate = 63000
	source.VideoTracks[0].Bitrate = 63000
	request := v3HandlerStartRequest()
	request.QualityPreference = playback.QualityAutoV3
	request.BandwidthEstimateKbps = new(44100)
	limit := &playback.BandwidthLimitV3{Kbps: 44100, Source: playback.BandwidthEstimateObservedRebufferV3}
	for _, tc := range []struct {
		name           string
		delivered, cap int
		annotated      bool
	}{
		{"lower original", 31600, 0, true},
		{"unchanged source", 63000, 0, false},
		{"admin cap already lower", 8000, 8000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := playback.PlannerResultV3{Plan: &playback.PlanV3{}}
			result.Plan.Source.BitrateKbps = tc.delivered
			markLearnedLimit(&result, limit, source, request, tc.cap)
			if (result.Plan.BandwidthLimit != nil) != tc.annotated {
				t.Fatalf("annotation=%+v", result.Plan.BandwidthLimit)
			}
		})
	}
}
