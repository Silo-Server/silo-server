package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// dvStripProfile8Track is a Dolby Vision profile 8.1 video: HEVC Main 10
// with an HDR10 base layer (compatibility id 1).
func dvStripProfile8Track() models.VideoTrack {
	return models.VideoTrack{
		Codec: "hevc", Profile: "Main 10", Level: 150, Width: 3840, Height: 1920, BitDepth: 10,
		VideoRange: "HDR", VideoRangeType: "DOVIWithHDR10", DolbyVision: "Dolby Vision Profile 8.1",
		DVProfile: 8, DVLevel: 6, DVBLCompatID: 1,
		DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true,
		ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
	}
}

// androidTVRangeProfile mirrors the Jellyfin Android TV device profile, which
// rejects range types with a NotEquals condition gated by an InCollection
// ApplyCondition over the same list.
func androidTVRangeProfile(unsupportedHEVC ...string) string {
	return rangeProfileJSON(nil, unsupportedHEVC...)
}

// rangeProfileJSON builds the Android TV range profile with extra HEVC
// conditions, such as the VideoCodecTag requirement Jellyfin Web sends on
// Safari.
func rangeProfileJSON(extraHEVC []map[string]any, unsupportedHEVC ...string) string {
	values := strings.Join(unsupportedHEVC, "|")
	profile := map[string]any{
		"Name":                "AndroidTV-Default",
		"MaxStreamingBitrate": 120_000_000,
		"MaxStaticBitrate":    120_000_000,
		"DirectPlayProfiles": []map[string]any{{
			"Type": "Video", "Container": "mkv,mp4,ts", "VideoCodec": "h264,hevc,av1", "AudioCodec": "aac,ac3,eac3",
		}},
		"TranscodingProfiles": []map[string]any{
			{"Type": "Video", "Context": "Streaming", "Container": "ts", "Protocol": "hls", "VideoCodec": "hevc,h264", "AudioCodec": "aac,ac3,eac3"},
			{"Type": "Video", "Context": "Streaming", "Container": "mp4", "Protocol": "hls", "VideoCodec": "hevc,h264", "AudioCodec": "aac,ac3,eac3"},
		},
		"CodecProfiles": []map[string]any{{
			"Type": "Video", "Codec": "hevc",
			"Conditions":      []map[string]any{{"Condition": "NotEquals", "Property": "VideoRangeType", "Value": values, "IsRequired": false}},
			"ApplyConditions": []map[string]any{{"Condition": "EqualsAny", "Property": "VideoRangeType", "Value": values, "IsRequired": false}},
		}, {
			"Type": "Video", "Codec": "hevc", "Conditions": extraHEVC,
		}},
	}
	body, _ := json.Marshal(map[string]any{"DeviceProfile": profile})
	return string(body)
}

var (
	androidTVDVProfile8Disabled = []string{"DOVIInvalid", "DOVIWithEL", "DOVIWithELHDR10Plus", "DOVI", "DOVIWithHDR10", "DOVIWithHDR10Plus"}
	androidTVSDRDisplay         = append(append([]string(nil), androidTVDVProfile8Disabled...), "HDR10Plus", "HDR10")
	androidTVDolbyVisionDisplay = []string{"DOVIInvalid", "DOVIWithEL", "DOVIWithELHDR10Plus"}
	// androidTVHDR10Display is what Jellyfin Android TV rejects on a display
	// with HDR10 but neither Dolby Vision nor HDR10+.
	androidTVHDR10Display = append(append([]string(nil), androidTVDVProfile8Disabled...), "HDR10Plus")
)

// newDVStripHandler serves one profile 8.1 version with tone mapping off,
// so a full HDR encode is never available.
func newDVStripHandler(t *testing.T, localStrip bool) (*PlaybackHandler, string) {
	t.Helper()
	return newDVStripHandlerWithTrack(t, localStrip, dvStripProfile8Track())
}

// newDVStripHandlerWithTrack is newDVStripHandler with another primary video.
func newDVStripHandlerWithTrack(t *testing.T, localStrip bool, track models.VideoTrack) (*PlaybackHandler, string) {
	t.Helper()
	handler, routeID := newSubtitleSelectionHandler(t)
	version := subtitleSelectionVersion()
	version.FilePath = "/media/movie.mkv"
	version.CodecVideo, version.CodecAudio = "hevc", "eac3"
	version.Bitrate = 15_183
	version.HDR = true
	version.VideoTracks = []models.VideoTrack{track}
	version.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}}
	version.SubtitleTracks = nil
	handler.content = &stubContentService{detail: &upstreamItemDetail{ContentID: "movie-1", Versions: []catalog.FileVersion{version}}}
	handler.SettingsRepo = stubSettingsReader{values: map[string]string{
		config.PlaybackTranscodeHardwareToneMapSettingKey: "false",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "false",
	}}
	handler.compatDVRPUProbe = func(context.Context, string) bool { return true }
	handler.compatDVStripLocalProbe = func() bool { return localStrip }
	return handler, routeID
}

func TestPlaybackInfoStripsDolbyVisionForClientThatRejectsIt(t *testing.T) {
	handler, routeID := newDVStripHandler(t, true)

	response := postPlaybackInfo(t, handler, routeID, androidTVRangeProfile(androidTVDVProfile8Disabled...))

	if len(response.MediaSources) != 1 {
		t.Fatalf("media sources = %#v, want one", response.MediaSources)
	}
	dto := response.MediaSources[0]
	if dto.SupportsDirectPlay || dto.SupportsDirectStream || !dto.SupportsTranscoding ||
		!strings.HasPrefix(dto.TranscodingURL, "/Videos/"+routeID+"/remux-dv-v1/master.m3u8?") {
		t.Fatalf("media source = %+v, want only the remux-dv-v1 HLS route", dto)
	}
	stored, ok := handler.playbackStore.Get(response.PlaySessionID)
	if !ok || len(stored.MediaSources) != 1 {
		t.Fatalf("stored session = %#v, want one negotiated source", stored)
	}
	source := stored.MediaSources[0]
	if !source.DVStripToHDR10 || !source.HLSRemux || source.TranscodeAudio || source.DOVIVariant {
		t.Fatalf("stored source = %+v, want an HDR10 strip remux with copied audio", source)
	}
	if got := compatPrimaryVideoTrack(source.Version).VideoRangeType; got != "DOVIWithHDR10" {
		t.Fatalf("stored video range type = %q, want the original file described", got)
	}
}

// dvStripNodePlanner lists and resolves pooled transcode nodes by URL, as
// *nodepool.Planner does.
type dvStripNodePlanner struct {
	compatToneMapInventoryPlanner
	nodes map[string]*nodepool.Node
}

func (p dvStripNodePlanner) TranscodeNodeByURL(nodeURL string) (*nodepool.Node, bool) {
	node, ok := p.nodes[nodeURL]
	return node, ok
}

var dvStripTransformation = playback.TransformationV3{
	Name: playback.TransformationServerDV7HDR10V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationServerDV7HDR10RecipeVersionV3,
}

// Under a server cap too low for any encode, the capped pass never loads the
// tone-map policy, so the check without the cap is the first to. A failed
// lookup there leaves the HDR encode unknown, not impossible: the answer is
// the same temporary 503 the negotiation loop gives, not NoCompatibleStream.
func TestPlaybackInfoToneMapPolicyFailureWithoutCapIsTemporary(t *testing.T) {
	version := hdr10PlusRangeTestVersion("HDR10")
	version.Resolution, version.Bitrate = "1080p", 8_000
	version.VideoTracks[0].Width, version.VideoTracks[0].Height = 1920, 1080
	handler, routeID := newUnreadablePlaybackInfoHandler(t, version)
	handler.ScopeResolver = &stubScopeResolver{scope: access.Scope{MaxLocalStreamBitrateKbps: 50, MaxRemoteStreamBitrateKbps: 50}}
	handler.SettingsRepo = stubSettingsReader{err: errors.New("settings store unavailable")}
	body := `{"DeviceProfile":{"DirectPlayProfiles":[],"TranscodingProfiles":[{"Type":"Video","Container":"ts","Protocol":"hls","VideoCodec":"h264","AudioCodec":"aac"}]}}`

	rec := servePlaybackInfo(handler, routeID, body)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Tone-map policy is temporarily unavailable") {
		t.Fatalf("status = %d, body = %s; want 503 for the unknown tone-map policy", rec.Code, rec.Body.String())
	}
}
