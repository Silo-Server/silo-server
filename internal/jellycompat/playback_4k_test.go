package jellycompat

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type stubSettingsReader struct {
	values map[string]string
	err    error
}

func (s stubSettingsReader) Get(_ context.Context, key string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.values[key], nil
}

func TestAllow4KVideoTranscode(t *testing.T) {
	tests := []struct {
		name string
		repo SettingsReader
		want bool
	}{
		{name: "nil repo defaults to deny", repo: nil, want: false},
		{name: "unset defaults to deny", repo: stubSettingsReader{}, want: false},
		{name: "read error defaults to deny", repo: stubSettingsReader{err: errors.New("read failed")}, want: false},
		{name: "explicit false denies", repo: stubSettingsReader{values: map[string]string{"allow_4k_transcode": "false"}}, want: false},
		{name: "explicit true allows", repo: stubSettingsReader{values: map[string]string{"allow_4k_transcode": "true"}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &PlaybackHandler{SettingsRepo: tt.repo}
			if got := h.allow4KVideoTranscode(context.Background()); got != tt.want {
				t.Errorf("allow4KVideoTranscode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIs4KResolution(t *testing.T) {
	for res, want := range map[string]bool{
		"2160p": true,
		"4320p": true,
		"1080p": false,
		"720p":  false,
		"":      false,
	} {
		if got := is4KResolution(res); got != want {
			t.Errorf("is4KResolution(%q) = %v, want %v", res, got, want)
		}
	}
}

func TestBuildPlaybackSource4KVideoTranscodeGate(t *testing.T) {
	version4K := catalog.FileVersion{
		FileID:     1,
		Resolution: "2160p",
		CodecVideo: "hevc",
		CodecAudio: "eac3",
		Container:  "mkv",
	}
	version1080 := version4K
	version1080.Resolution = "1080p"

	// Can only decode h264: HEVC versions need a full video transcode.
	h264Only := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{
			{Type: "Video", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"},
		},
	}
	// Decodes HEVC but not EAC3: transcoding path stream-copies the video.
	hevcNoEac3 := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{
			{Type: "Video", Container: "mp4", VideoCodec: "h264,hevc", AudioCodec: "aac"},
		},
	}

	tests := []struct {
		name               string
		version            catalog.FileVersion
		profile            DeviceProfile
		allow4K            bool
		wantTranscoding    bool
		wantTranscodeAudio bool
	}{
		{
			name:            "4K video transcode blocked when disallowed",
			version:         version4K,
			profile:         h264Only,
			allow4K:         false,
			wantTranscoding: false,
		},
		{
			name:            "4K video transcode offered when allowed",
			version:         version4K,
			profile:         h264Only,
			allow4K:         true,
			wantTranscoding: true,
		},
		{
			name:               "4K audio-only transcode (video copy) stays allowed",
			version:            version4K,
			profile:            hevcNoEac3,
			allow4K:            false,
			wantTranscoding:    true,
			wantTranscodeAudio: true,
		},
		{
			name:            "non-4K video transcode unaffected",
			version:         version1080,
			profile:         h264Only,
			allow4K:         false,
			wantTranscoding: true,
		},
	}

	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := h.buildPlaybackSource("item", "ps", tt.version, tt.profile, playbackInfoRequest{}, tt.allow4K)
			if source.SupportsTranscoding != tt.wantTranscoding {
				t.Errorf("SupportsTranscoding = %v, want %v", source.SupportsTranscoding, tt.wantTranscoding)
			}
			if source.TranscodeAudio != tt.wantTranscodeAudio {
				t.Errorf("TranscodeAudio = %v, want %v", source.TranscodeAudio, tt.wantTranscodeAudio)
			}
		})
	}
}

func TestEnsureTranscodeSession4KGuard(t *testing.T) {
	h := &PlaybackHandler{} // nil SettingsRepo: 4K video transcodes denied

	source := PlaybackMediaSource{
		FileID:  1,
		Version: catalog.FileVersion{FileID: 1, Resolution: "2160p", CodecVideo: "hevc"},
	}
	if _, err := h.ensureTranscodeSession(context.Background(), "ps", "session", source); !errors.Is(err, errTranscode4KDisallowed) {
		t.Errorf("ensureTranscodeSession() error = %v, want errTranscode4KDisallowed", err)
	}

	// Video-copy sessions pass the guard (and fail later on the missing file
	// resolver, which is fine for this test).
	source.TranscodeAudio = true
	if _, err := h.ensureTranscodeSession(context.Background(), "ps", "session", source); errors.Is(err, errTranscode4KDisallowed) {
		t.Error("ensureTranscodeSession() blocked a video-copy session")
	}
}

func TestStartRemoteTranscode4KGuard(t *testing.T) {
	h := &PlaybackHandler{} // nil SettingsRepo: 4K video transcodes denied

	source := PlaybackMediaSource{
		FileID:  1,
		Version: catalog.FileVersion{FileID: 1, Resolution: "2160p", CodecVideo: "hevc"},
	}
	if err := h.startRemoteTranscode(context.Background(), "session", source, nil, 0, "http://node"); !errors.Is(err, errTranscode4KDisallowed) {
		t.Errorf("startRemoteTranscode() error = %v, want errTranscode4KDisallowed", err)
	}
}
