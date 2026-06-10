package playback_test

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func defaultCaps() playback.ClientCapabilities {
	return playback.ClientCapabilities{
		CodecsVideo:   []string{"h264"},
		CodecsAudio:   []string{"aac", "opus"},
		Containers:    []string{"mp4", "webm"},
		MaxResolution: "1080p",
		HDR:           false,
	}
}

func defaultSettings() playback.AdminSettings {
	return playback.AdminSettings{
		TranscodeEnabled: true,
		Allow4KTranscode: false,
	}
}

func TestResolver_DirectPlay(t *testing.T) {
	file := &models.MediaFile{
		CodecVideo: "h264", CodecAudio: "aac", Container: "mp4",
		Resolution: "1080p", HDR: false,
	}
	decision := playback.Resolve(file, defaultCaps(), defaultSettings())

	if decision.Method != playback.PlayDirect {
		t.Errorf("method = %q, want direct", decision.Method)
	}
}

func TestResolver_Remux(t *testing.T) {
	// h264+aac in mkv — client supports codecs but not container.
	file := &models.MediaFile{
		CodecVideo: "h264", CodecAudio: "aac", Container: "mkv",
		Resolution: "1080p", HDR: false,
	}
	decision := playback.Resolve(file, defaultCaps(), defaultSettings())

	if decision.Method != playback.PlayRemux {
		t.Errorf("method = %q, want remux", decision.Method)
	}
}

func TestResolver_RemuxWithAudioTranscode(t *testing.T) {
	// h264 video (supported) + dts audio (unsupported) → remux with audio transcode.
	file := &models.MediaFile{
		CodecVideo: "h264", CodecAudio: "dts", Container: "mkv",
		Resolution: "1080p", HDR: false,
	}
	decision := playback.Resolve(file, defaultCaps(), defaultSettings())

	if decision.Method != playback.PlayRemux {
		t.Errorf("method = %q, want remux", decision.Method)
	}
	if !decision.TranscodeAudio {
		t.Error("TranscodeAudio = false, want true")
	}
}

func TestResolver_AudioPassthroughSkipsAudioTranscode(t *testing.T) {
	// Source is h264 + eac3 in mp4. Client can decode h264 but not eac3; its
	// sink advertises eac3 passthrough (e.g. HDMI AVR). Should direct-play
	// without audio transcode instead of promoting to remux.
	file := &models.MediaFile{
		CodecVideo: "h264", CodecAudio: "eac3", Container: "mp4",
		Resolution: "1080p", HDR: false,
	}
	caps := defaultCaps()
	caps.AudioPassthroughCodecs = []string{"eac3", "ac3"}

	decision := playback.Resolve(file, caps, defaultSettings())

	if decision.Method != playback.PlayDirect {
		t.Errorf("method = %q, want direct (passthrough-supported audio)", decision.Method)
	}
	if decision.TranscodeAudio {
		t.Error("TranscodeAudio = true, want false (sink can passthrough)")
	}
}

func TestResolver_AudioPassthroughAllowsContainerRemux(t *testing.T) {
	// Source is h264 + eac3 in mkv. Client passthrough covers eac3 but container
	// is unsupported → remux without audio transcode.
	file := &models.MediaFile{
		CodecVideo: "h264", CodecAudio: "eac3", Container: "mkv",
		Resolution: "1080p", HDR: false,
	}
	caps := defaultCaps()
	caps.AudioPassthroughCodecs = []string{"eac3"}

	decision := playback.Resolve(file, caps, defaultSettings())

	if decision.Method != playback.PlayRemux {
		t.Errorf("method = %q, want remux", decision.Method)
	}
	if decision.TranscodeAudio {
		t.Error("TranscodeAudio = true, want false (sink can passthrough)")
	}
}

func TestResolver_Transcode_UnsupportedVideoCodec(t *testing.T) {
	// hevc is not in client's supported codecs.
	file := &models.MediaFile{
		CodecVideo: "hevc", CodecAudio: "aac", Container: "mp4",
		Resolution: "1080p", HDR: false,
	}
	decision := playback.Resolve(file, defaultCaps(), defaultSettings())

	if decision.Method != playback.PlayTranscode {
		t.Errorf("method = %q, want transcode", decision.Method)
	}
}

func TestResolver_Transcode_ResolutionExceeds(t *testing.T) {
	file := &models.MediaFile{
		CodecVideo: "h264", CodecAudio: "aac", Container: "mp4",
		Resolution: "2160p", HDR: false,
	}
	caps := defaultCaps()
	caps.MaxResolution = "1080p"

	decision := playback.Resolve(file, caps, defaultSettings())

	if decision.Method != playback.PlayTranscode {
		t.Errorf("method = %q, want transcode for resolution downscale", decision.Method)
	}
}

func TestResolver_HDR_PassthroughToRemux(t *testing.T) {
	file := &models.MediaFile{
		CodecVideo: "hevc", CodecAudio: "aac", Container: "mkv",
		Resolution: "1080p", HDR: true,
	}
	caps := defaultCaps()
	caps.CodecsVideo = []string{"h264", "hevc"}
	caps.HDR = false

	decision := playback.Resolve(file, caps, defaultSettings())

	if decision.Method != playback.PlayRemux {
		t.Errorf("method = %q, want remux — HDR should pass through without tone mapping", decision.Method)
	}
}

func TestResolver_TranscodeDisabled_FallsToDirect(t *testing.T) {
	file := &models.MediaFile{
		CodecVideo: "hevc", CodecAudio: "aac", Container: "mkv",
		Resolution: "1080p", HDR: false,
	}
	settings := defaultSettings()
	settings.TranscodeEnabled = false

	decision := playback.Resolve(file, defaultCaps(), settings)

	if decision.Method != playback.PlayDirect {
		t.Errorf("method = %q, want direct (transcode disabled)", decision.Method)
	}
}

func TestSelectVersion_PrefersDirectPlay(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 1, CodecVideo: "hevc", CodecAudio: "truehd", Container: "mkv", Resolution: "2160p", HDR: true, FileSize: 40000000000},
		{ID: 2, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "1080p", HDR: false, FileSize: 5000000000},
	}

	decision, err := playback.SelectVersion(files, defaultCaps(), defaultSettings())
	if err != nil {
		t.Fatalf("SelectVersion: %v", err)
	}

	if decision.Method != playback.PlayDirect {
		t.Errorf("method = %q, want direct", decision.Method)
	}
	if decision.File.ID != 2 {
		t.Errorf("file ID = %d, want 2 (1080p h264 is directly playable)", decision.File.ID)
	}
}

func TestSelectVersion_PrefersHigherQuality(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 1, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "720p", HDR: false, FileSize: 2000000000},
		{ID: 2, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "1080p", HDR: false, FileSize: 5000000000},
	}

	decision, err := playback.SelectVersion(files, defaultCaps(), defaultSettings())
	if err != nil {
		t.Fatalf("SelectVersion: %v", err)
	}

	if decision.File.ID != 2 {
		t.Errorf("file ID = %d, want 2 (1080p > 720p)", decision.File.ID)
	}
}

func TestSelectVersion_4KTranscodeDisabled(t *testing.T) {
	// Only a 4K file available, client max is 1080p, 4K transcode disabled.
	files := []*models.MediaFile{
		{ID: 1, CodecVideo: "hevc", CodecAudio: "truehd", Container: "mkv", Resolution: "2160p", HDR: true, FileSize: 40000000000},
	}
	caps := defaultCaps()
	caps.MaxResolution = "1080p"

	settings := defaultSettings()
	settings.Allow4KTranscode = false

	decision, err := playback.SelectVersion(files, caps, settings)
	if err != nil {
		t.Fatalf("SelectVersion: %v", err)
	}

	// Should fall back to the only file available.
	if decision.File.ID != 1 {
		t.Errorf("file ID = %d, want 1 (only file)", decision.File.ID)
	}
}

func TestSelectVersion_NoFiles(t *testing.T) {
	_, err := playback.SelectVersion(nil, defaultCaps(), defaultSettings())
	if err != playback.ErrNoVersions {
		t.Errorf("err = %v, want ErrNoVersions", err)
	}
}

func TestSelectVersion_SmallerFileBreaksTie(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 1, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "1080p", HDR: false, FileSize: 8000000000},
		{ID: 2, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "1080p", HDR: false, FileSize: 5000000000},
	}

	decision, err := playback.SelectVersion(files, defaultCaps(), defaultSettings())
	if err != nil {
		t.Fatalf("SelectVersion: %v", err)
	}

	if decision.File.ID != 2 {
		t.Errorf("file ID = %d, want 2 (smaller file at same resolution)", decision.File.ID)
	}
}

func TestSelectVersionFiltered_StaysWithinEditionAndPresentation(t *testing.T) {
	files := []*models.MediaFile{
		{
			ID: 1, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "1080p",
			EditionKey: "theatrical", PresentationKind: "multipart_movie", PresentationGroupKey: "movie",
		},
		{
			ID: 2, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "2160p",
			EditionKey: "extended", PresentationKind: "single",
		},
	}

	decision, err := playback.SelectVersionFiltered(
		files,
		defaultCaps(),
		defaultSettings(),
		playback.VersionSelectionFilter{
			EditionKey:           "theatrical",
			PresentationKind:     "multipart_movie",
			PresentationGroupKey: "movie",
		},
	)
	if err != nil {
		t.Fatalf("SelectVersionFiltered: %v", err)
	}
	if decision.File.ID != 1 {
		t.Fatalf("file ID = %d, want 1", decision.File.ID)
	}
}
