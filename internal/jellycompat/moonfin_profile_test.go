package jellycompat

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// moonfinIOSProfile is the DeviceProfile Moonfin for iOS 2.5.1 sends with
// PlaybackInfo, as Silo stored it. Every H.264 rule in it is IsRequired.
func moonfinIOSProfile(t *testing.T) DeviceProfile {
	t.Helper()
	data, err := os.ReadFile("testdata/moonfin_ios_device_profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var profile DeviceProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

// A 1080p H.264 file as FFmpeg 8.1 probes it: profile and level known,
// reference frames unknown (ffprobe reports refs only when it reads frames).
func moonfinH264Version() catalog.FileVersion {
	return catalog.FileVersion{
		FileID: 90516, Container: "mkv", CodecVideo: "h264", CodecAudio: "ac3", Resolution: "1080p", Bitrate: 12000,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 41, Width: 1920, Height: 804, BitDepth: 8}},
		AudioTracks: []models.AudioTrack{{Codec: "ac3", Channels: 6, Default: true}},
	}
}

// Moonfin's H.264 rules (a known VideoProfile; RefFrames <= 4 from 1900
// pixels wide) used to refuse every H.264 encode, and with the source's
// reference frames unreported, a 1080p H.264 file had no route at all:
// PlaybackInfo answered 400 PlaybackUnavailable for every such title.
func TestMoonfinIOSProfileCanTranscodeH264(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	req := playbackInfoRequest{AudioStreamIndex: compatIntValuePtr(1), MaxStreamingBitrate: 120_000_000}
	source := h.buildPlaybackSource("item", "play", moonfinH264Version(), moonfinIOSProfile(t), req, true)
	if !source.SupportsTranscoding {
		t.Errorf("SupportsTranscoding = false: the encode target can satisfy profile, level and reference-frame rules")
	}
}

// FFmpeg 8.1 never reports reference frames, so Moonfin's required
// RefFrames <= 4 rule evaluates against the 1 that FFmpeg 7 reported, and a
// 1080p H.264 file Moonfin direct plays on Jellyfin direct plays here too.
func TestMoonfinIOSProfileDirectPlaysH264WithUnreportedRefFrames(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	req := playbackInfoRequest{AudioStreamIndex: compatIntValuePtr(1), MaxStreamingBitrate: 120_000_000}
	source := h.buildPlaybackSource("item", "play", moonfinH264Version(), moonfinIOSProfile(t), req, true)
	if !source.SupportsDirectPlay {
		t.Errorf("SupportsDirectPlay = false for a file Moonfin direct plays on Jellyfin")
	}

	// A count the probe did report is still enforced.
	version := moonfinH264Version()
	version.VideoTracks[0].ReferenceFrames = 8
	source = h.buildPlaybackSource("item", "play", version, moonfinIOSProfile(t), req, true)
	if source.SupportsDirectPlay || !source.SupportsTranscoding {
		t.Errorf("eight known reference frames: direct=%v transcoding=%v, want an encode", source.SupportsDirectPlay, source.SupportsTranscoding)
	}
}
