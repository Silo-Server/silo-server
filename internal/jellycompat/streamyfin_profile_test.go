package jellycompat

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// streamyfinPlaybackInfoBody is what Streamyfin 0.55.1 posts after the viewer
// picks a quality: its MPV device profile plus maxStreamingBitrate, in
// camelCase. testdata holds the profile it builds on Android, with and without
// AV1 transcoding (it offers AV1 when the phone decodes it in hardware).
func streamyfinPlaybackInfoBody(t *testing.T, profileFile string, maxStreamingBitrate int64) string {
	t.Helper()
	profile, err := os.ReadFile("testdata/" + profileFile)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(profile) {
		t.Fatalf("%s is not JSON", profileFile)
	}
	return fmt.Sprintf(`{"deviceProfile":%s,"isPlayback":true,"autoOpenLiveStream":true,"maxStreamingBitrate":%d,"audioStreamIndex":1,"startTimeTicks":0}`,
		profile, maxStreamingBitrate)
}

// A 1080p H.264 file Streamyfin direct plays until the viewer lowers the
// quality (#3215).
func streamyfinH264Version() catalog.FileVersion {
	return catalog.FileVersion{
		FileID: 3215, Container: "mkv", CodecVideo: "h264", CodecAudio: "eac3", Resolution: "1080p", Bitrate: 9816,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 41, Width: 1920, Height: 1080, BitDepth: 8}},
		AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6, Default: true}},
	}
}

// Lowering the quality turns direct play off. With MPEG-TS HLS in the profile
// an H.264 encode fits; a phone that decodes AV1 lists only fMP4 HLS, which
// takes no H.264 encode here, so without HEVC encoding nothing can play and
// PlaybackInfo answers as Jellyfin does instead of failing the request.
func TestStreamyfinLowerQualityTranscodesOrReportsNoCompatibleStream(t *testing.T) {
	t.Run("MPEG-TS HLS gets an H.264 encode", func(t *testing.T) {
		handler, routeID := newUnreadablePlaybackInfoHandler(t, streamyfinH264Version())
		resp := postPlaybackInfo(t, handler, routeID, streamyfinPlaybackInfoBody(t, "streamyfin_mpv_android_device_profile.json", 4_000_000))
		if resp.ErrorCode != "" || len(resp.MediaSources) != 1 {
			t.Fatalf("response = %+v, want one source", resp)
		}
		source := resp.MediaSources[0]
		if source.SupportsDirectPlay || source.TranscodingURL == "" || source.TranscodingContainer != compatContainerTS {
			t.Fatalf("source = %+v, want an MPEG-TS encode under the 4 Mbps ceiling", source)
		}
	})
	t.Run("fMP4-only HLS without HEVC encoding", func(t *testing.T) {
		handler, routeID := newUnreadablePlaybackInfoHandler(t, streamyfinH264Version())
		rec := servePlaybackInfo(handler, routeID, streamyfinPlaybackInfoBody(t, "streamyfin_mpv_android_av1_device_profile.json", 4_000_000))
		assertNoCompatibleStream(t, handler, routeID, rec.Code, rec.Body.Bytes())

		// The same request at full quality still direct plays.
		resp := postPlaybackInfo(t, handler, routeID, streamyfinPlaybackInfoBody(t, "streamyfin_mpv_android_av1_device_profile.json", 0))
		if len(resp.MediaSources) != 1 || !resp.MediaSources[0].SupportsDirectPlay {
			t.Fatalf("response = %+v, want direct play without a ceiling", resp)
		}
	})
}
