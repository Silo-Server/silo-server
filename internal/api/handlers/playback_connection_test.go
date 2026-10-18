package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

// TestSeekReplayKeepsLearnedLimitDB: a seek-scoped replan replays the stored
// start request, which applyLearnedLimit has already rewritten to the limit or
// marked observed_rebuffer over a lower probe. Only the client's own request
// may record or clear the limit, so a replay neither renews it nor stores the
// probe estimate as a new limit.
func TestSeekReplayKeepsLearnedLimitDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("learned-limit-replay-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, user) })

	h := &PlaybackHandler{Connections: &connectionstore.Store{Pool: pool}}
	requestCtx := apimw.SetClaims(WithNativeAPIV2(ctx), &auth.Claims{UserID: user, Role: "user", TokenType: auth.TokenTypeAccess})
	requestCtx = context.WithValue(clientip.SetContext(requestCtx, "192.168.1.10"), playbackConnectionDeviceContextKey{}, "tv-1")
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(requestCtx)
	request := func(source string, estimate *int, control string) playback.StartRequestV3 {
		return playback.StartRequestV3{QualityPreference: playback.QualityAutoV3, ClientFeatures: []string{playback.FeatureBufferingQualityV1},
			BandwidthEstimateKbps: estimate, BandwidthEstimateSource: source, LearnedLimit: control}
	}
	stored := func() (int, time.Time, bool) {
		var kbps *int
		var observed *time.Time
		if err := pool.QueryRow(ctx, `SELECT limit_kbps, observed_at FROM playback_connections WHERE user_id = $1`, user).Scan(&kbps, &observed); err != nil {
			t.Fatal(err)
		}
		if kbps == nil || observed == nil {
			return 0, time.Time{}, false
		}
		return *kbps, *observed, true
	}

	lowered := request(playback.BandwidthEstimateObservedRebufferV3, new(44100), "")
	if limit, err := h.applyLearnedLimit(r, &lowered, true); err != nil || limit == nil || limit.Kbps != 44100 {
		t.Fatalf("client lowering: limit=%+v err=%v", limit, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE playback_connections SET observed_at = now() - interval '1 day' WHERE user_id = $1`, user); err != nil {
		t.Fatal(err)
	}
	_, before, _ := stored()
	probed := request(playback.BandwidthEstimateProbeV3, new(20000), "")
	if limit, err := h.applyLearnedLimit(r, &probed, true); err != nil || limit != nil || probed.BandwidthEstimateSource != playback.BandwidthEstimateObservedRebufferV3 {
		t.Fatalf("lower probe: limit=%+v source=%q err=%v", limit, probed.BandwidthEstimateSource, err)
	}
	for _, replay := range []playback.StartRequestV3{lowered, probed} {
		if _, err := h.applyLearnedLimit(r, &replay, false); err != nil {
			t.Fatal(err)
		}
	}
	if kbps, observed, ok := stored(); !ok || kbps != 44100 || !observed.Equal(before) {
		t.Fatalf("replay changed the limit: kbps=%d observed=%v want 44100 at %v", kbps, observed, before)
	}

	cleared := request("", nil, playback.LearnedLimitClearV3)
	replayedClear := cleared
	if _, err := h.applyLearnedLimit(r, &replayedClear, false); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := stored(); !ok {
		t.Fatal("a replayed clear removed the limit")
	}
	if _, err := h.applyLearnedLimit(r, &cleared, true); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := stored(); ok {
		t.Fatal("the client's clear kept the limit")
	}
}

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
