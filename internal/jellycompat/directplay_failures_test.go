package jellycompat

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// directPlayFailureVersion is the field shape: a Matroska file the profile
// claims and the server sends as-is until the device proves otherwise.
func directPlayFailureVersion() catalog.FileVersion {
	return catalog.FileVersion{
		FileID:      42,
		Container:   "mkv",
		CodecVideo:  "h264",
		CodecAudio:  "aac",
		Resolution:  "1080p",
		Bitrate:     8_000,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 41, Width: 1920, Height: 1080, BitDepth: 8}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}},
	}
}

// directPlayFailureProfile lists the Matroska container for direct play and
// HLS fMP4 for packaged playback, like the Jellyfin clients in the field.
func directPlayFailureProfile() DeviceProfile {
	return DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{{Type: "Video", Container: "mkv,mp4", VideoCodec: "h264", AudioCodec: "aac"}},
		TranscodingProfiles: []TranscodingProfile{
			{Type: "Video", Protocol: "hls", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"},
		},
	}
}

// TestDeviceProfileDirectPlayRequiresListedContainer verifies the Jellyfin
// parity rule the compat surface already enforces: a profile that does not list
// the source container never accepts the file as-is, whatever its codecs say.
func TestDeviceProfileDirectPlayRequiresListedContainer(t *testing.T) {
	version := directPlayFailureVersion()
	profile := directPlayFailureProfile()
	if !profile.SupportsDirectPlay(version) {
		t.Fatal("a profile listing mkv refused direct play for an mkv source")
	}

	profile.DirectPlayProfiles = []DirectPlayProfile{{Type: "Video", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"}}
	if profile.SupportsDirectPlay(version) {
		t.Fatal("a profile without mkv accepted an mkv source as direct play")
	}
	if !profile.directPlayContainerListed("mp4") || profile.directPlayContainerListed("mkv") {
		t.Fatalf("container listing = %v/%v", profile.directPlayContainerListed("mp4"), profile.directPlayContainerListed("mkv"))
	}
}

// TestDirectPlayFailureRotatesTheNextNegotiation is the parity half of the
// native rule: once this device's direct play of the file failed, the next
// negotiation refuses direct play even though the stored profile still claims
// the container, and answers with a packaged route instead of nothing.
func TestDirectPlayFailureRotatesTheNextNegotiation(t *testing.T) {
	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	version := directPlayFailureVersion()
	profile := directPlayFailureProfile()

	direct := h.buildPlaybackSource("item", "play", version, profile, playbackInfoRequest{}, true)
	if !direct.SupportsDirectPlay {
		t.Fatalf("baseline source = %+v, want direct play", direct)
	}

	req := playbackInfoRequest{directPlayFailedFiles: map[int]struct{}{version.FileID: {}}}
	rotated := h.buildPlaybackSource("item", "play", version, profile, req, true)
	if rotated.SupportsDirectPlay {
		t.Fatalf("a recorded failure still negotiated direct play: %+v", rotated)
	}
	if !rotated.SupportsTranscoding && !rotated.SupportsDirectStream && !rotated.HLSRemux {
		t.Fatalf("a recorded failure left no playable route: %+v", rotated)
	}
	if reason := compatDirectPlayDowngradeReason(profile, version, req); reason != compatRouteDowngradeDirectPlayFailed {
		t.Fatalf("downgrade reason = %q, want %q", reason, compatRouteDowngradeDirectPlayFailed)
	}
	// A profile that never listed the container says so on its own.
	if reason := compatDirectPlayDowngradeReason(DeviceProfile{
		DirectPlayProfiles:  []DirectPlayProfile{{Type: "Video", Container: "mp4", VideoCodec: "h264"}},
		TranscodingProfiles: profile.TranscodingProfiles,
	}, version, playbackInfoRequest{}); reason != compatRouteDowngradeProfileContainer {
		t.Fatalf("container refusal reason = %q", reason)
	}
}

// TestDirectPlayFailureRecordExpiresAndStaysBounded keeps the server-side
// memory honest: it outlives the failed play but not the device-profile TTL,
// and one token cannot grow it without limit.
func TestDirectPlayFailureRecordExpiresAndStaysBounded(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	store := NewDeviceProfileStore(6*time.Hour, func() time.Time { return now })
	ctx := context.Background()

	if err := store.RecordDirectPlayFailure(ctx, "token", "device", 42); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.DirectPlayFailures(ctx, "token", "device")[42]; !ok {
		t.Fatal("the recorded failure is not visible to the next negotiation")
	}
	if _, ok := store.DirectPlayFailures(ctx, "token", "other-device")[42]; ok {
		t.Fatal("one device's failure leaked to another device on the same account")
	}

	now = now.Add(compatDirectPlayFailureTTL + time.Minute)
	if _, ok := store.DirectPlayFailures(ctx, "token", "device")[42]; ok {
		t.Fatal("an expired failure still refuses direct play")
	}

	for i := range maxDirectPlayFailuresPerToken + 8 {
		if err := store.RecordDirectPlayFailure(ctx, "token", "device", 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.RLock()
	live := len(store.directPlayFailures)
	store.mu.RUnlock()
	if live > maxDirectPlayFailuresPerToken {
		t.Fatalf("failure records = %d, want at most %d", live, maxDirectPlayFailuresPerToken)
	}
}

// TestRecordCompatDirectPlayFailureNeedsFailureEvidence checks the two signals
// the compat surface accepts: Jellyfin's Failed flag, and a stop that never
// advanced inside the startup window. A direct route that delivered frames is
// never recorded, so a successful play leaves the next negotiation unchanged.
func TestRecordCompatDirectPlayFailureNeedsFailureEvidence(t *testing.T) {
	now := time.Now()
	newHandler := func() *PlaybackHandler {
		return &PlaybackHandler{deviceProfiles: NewDeviceProfileStore(6*time.Hour, func() time.Time { return now })}
	}
	source := &PlaybackMediaSource{Version: directPlayFailureVersion(), SupportsDirectPlay: true}
	session := func() *PlaybackSession {
		return &PlaybackSession{
			ID:                 "play-1",
			CompatToken:        "token",
			ClientDeviceID:     "device",
			CreatedAt:          now.Add(-5 * time.Second),
			UpstreamPlayMethod: string(playback.PlayDirect),
		}
	}

	h := newHandler()
	h.recordCompatDirectPlayFailure(context.Background(), session(), source, false, 120, 120)
	if _, ok := h.deviceProfiles.DirectPlayFailures(context.Background(), "token", "device")[source.Version.FileID]; ok {
		t.Fatal("a direct route that delivered frames was recorded as failed")
	}

	h = newHandler()
	h.recordCompatDirectPlayFailure(context.Background(), session(), source, true, 0, playback.PositionUnknown)
	if _, ok := h.deviceProfiles.DirectPlayFailures(context.Background(), "token", "device")[source.Version.FileID]; !ok {
		t.Fatal("a stop report with Failed=true was not recorded")
	}

	h = newHandler()
	h.recordCompatDirectPlayFailure(context.Background(), session(), source, false, 0, playback.PositionUnknown)
	if _, ok := h.deviceProfiles.DirectPlayFailures(context.Background(), "token", "device")[source.Version.FileID]; !ok {
		t.Fatal("a start-window stop that never advanced was not recorded")
	}

	// Outside the startup window, a stop with no progress is a viewer leaving,
	// not evidence about the route.
	h = newHandler()
	late := session()
	late.CreatedAt = now.Add(-2 * compatDirectPlayStartupWindow)
	h.recordCompatDirectPlayFailure(context.Background(), late, source, false, 0, playback.PositionUnknown)
	if _, ok := h.deviceProfiles.DirectPlayFailures(context.Background(), "token", "device")[source.Version.FileID]; ok {
		t.Fatal("a late stop with no new position was recorded as a failed direct play")
	}

	// A transcoded route is not direct play and is never recorded.
	h = newHandler()
	transcoded := session()
	transcoded.UpstreamPlayMethod = string(playback.PlayTranscode)
	h.recordCompatDirectPlayFailure(context.Background(), transcoded, source, true, 0, playback.PositionUnknown)
	if _, ok := h.deviceProfiles.DirectPlayFailures(context.Background(), "token", "device")[source.Version.FileID]; ok {
		t.Fatal("a transcoded play was recorded as a failed direct play")
	}
}

// TestFailedPackagedRouteClearsTheDirectPlayDowngrade covers the way out of the
// downgrade. A record is not a permanent verdict on the device: the packaged
// route replaces direct play for at most compatDirectPlayFailureTTL, and when
// that replacement itself fails to start the record is dropped so the next
// negotiation offers direct play again instead of holding the viewer on a
// route that does not work. A packaged play that delivered frames leaves the
// record alone, which is what keeps a device with a genuinely unplayable direct
// route from retrying it on every play.
func TestFailedPackagedRouteClearsTheDirectPlayDowngrade(t *testing.T) {
	now := time.Now()
	newHandler := func() *PlaybackHandler {
		h := &PlaybackHandler{deviceProfiles: NewDeviceProfileStore(6*time.Hour, func() time.Time { return now })}
		if err := h.deviceProfiles.RecordDirectPlayFailure(context.Background(), "token", "device", 42); err != nil {
			t.Fatal(err)
		}
		return h
	}
	packaged := &PlaybackMediaSource{Version: directPlayFailureVersion(), HLSRemux: true, SupportsTranscoding: true}
	session := func() *PlaybackSession {
		return &PlaybackSession{
			ID:                 "play-1",
			CompatToken:        "token",
			ClientDeviceID:     "device",
			CreatedAt:          now.Add(-5 * time.Second),
			UpstreamPlayMethod: string(playback.PlayTranscode),
		}
	}
	recorded := func(h *PlaybackHandler) bool {
		_, ok := h.deviceProfiles.DirectPlayFailures(context.Background(), "token", "device")[42]
		return ok
	}

	h := newHandler()
	h.recordCompatDirectPlayFailure(context.Background(), session(), packaged, true, 0, playback.PositionUnknown)
	if recorded(h) {
		t.Fatal("a packaged route that failed still holds the device off direct play")
	}

	h = newHandler()
	h.recordCompatDirectPlayFailure(context.Background(), session(), packaged, false, 0, playback.PositionUnknown)
	if recorded(h) {
		t.Fatal("a packaged route that never started still holds the device off direct play")
	}

	h = newHandler()
	h.recordCompatDirectPlayFailure(context.Background(), session(), packaged, false, 120, 60)
	if !recorded(h) {
		t.Fatal("a packaged play that delivered frames cleared a downgrade it still needs")
	}
}
