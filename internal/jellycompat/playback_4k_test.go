package jellycompat

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
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

func TestToneMapPolicyResultPreservesStoreFailure(t *testing.T) {
	handler := &PlaybackHandler{SettingsRepo: stubSettingsReader{err: context.DeadlineExceeded}}
	_, err := handler.toneMapPolicyResult(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("toneMapPolicyResult() error = %v, want context deadline", err)
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

func TestCompatVideoToolboxToneMapBitrateKbps(t *testing.T) {
	videoToolbox := compatToneMapRecipe{mode: tonemap.ModeHardware, hwAccel: tonemap.BackendVideoToolbox}
	tests := []struct {
		name    string
		version catalog.FileVersion
		recipe  compatToneMapRecipe
		want    int
	}{
		{name: "4K track", version: catalog.FileVersion{VideoTracks: []models.VideoTrack{{Height: 2160}}}, recipe: videoToolbox, want: 20_000},
		{name: "4K label", version: catalog.FileVersion{Resolution: "4K"}, recipe: videoToolbox, want: 20_000},
		{name: "1080p", version: catalog.FileVersion{VideoTracks: []models.VideoTrack{{Height: 1080}}}, recipe: videoToolbox, want: 6_000},
		{name: "720p", version: catalog.FileVersion{Resolution: "720p"}, recipe: videoToolbox, want: 2_000},
		{name: "SD", version: catalog.FileVersion{VideoTracks: []models.VideoTrack{{Height: 576}}}, recipe: videoToolbox, want: 1_500},
		{name: "source bitrate fallback", version: catalog.FileVersion{Bitrate: 9_000}, recipe: videoToolbox, want: 9_000},
		{name: "unknown source", recipe: videoToolbox},
		{name: "software mode", version: catalog.FileVersion{Resolution: "2160p"}, recipe: compatToneMapRecipe{mode: tonemap.ModeSoftware, hwAccel: tonemap.BackendSoftware}},
		{name: "other hardware", version: catalog.FileVersion{Resolution: "2160p"}, recipe: compatToneMapRecipe{mode: tonemap.ModeHardware, hwAccel: tonemap.BackendQSV}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := compatVideoToolboxToneMapBitrateKbps(test.version, test.recipe); got != test.want {
				t.Fatalf("compatVideoToolboxToneMapBitrateKbps() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestDowngradeCompatLocalToneMapClearsOnlyAutomaticVideoToolboxBitrate(t *testing.T) {
	capabilities := tonemap.Capabilities{{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware,
		Filter: tonemap.SoftwareFilterHable, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}}
	newOpts := func() playback.TranscodeOpts {
		return playback.TranscodeOpts{
			ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tonemap.ModeHardware,
			ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: tonemap.HardwareFilterVideoToolbox,
			HWAccel: tonemap.BackendVideoToolbox, TargetBitrateKbps: 20_000,
		}
	}

	automatic := newOpts()
	if !downgradeCompatLocalToneMap(&automatic, capabilities, 20_000) {
		t.Fatal("automatic VideoToolbox recipe did not downgrade")
	}
	if automatic.TargetBitrateKbps != 0 || automatic.ToneMapMode != tonemap.ModeSoftware || automatic.HWAccel != playback.HWAccelNone {
		t.Fatalf("automatic fallback = bitrate %d mode %q hw %q", automatic.TargetBitrateKbps, automatic.ToneMapMode, automatic.HWAccel)
	}

	explicit := newOpts()
	if !downgradeCompatLocalToneMap(&explicit, capabilities, 0) {
		t.Fatal("explicitly constrained recipe did not downgrade")
	}
	if explicit.TargetBitrateKbps != 20_000 {
		t.Fatalf("explicit fallback bitrate = %d, want 20000", explicit.TargetBitrateKbps)
	}
}

func TestApplyCompatMaxStreamingBitrateCap(t *testing.T) {
	tests := []struct {
		name    string
		initial int
		cap     int
		want    int
	}{
		{name: "no cap leaves target untouched", initial: 20_000, cap: 0, want: 20_000},
		{name: "cap fills an unset target", initial: 0, cap: 4_000, want: 4_000},
		{name: "cap overrides a looser auto target", initial: 20_000, cap: 4_000, want: 4_000},
		{name: "auto target tighter than cap wins", initial: 2_000, cap: 4_000, want: 2_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := playback.TranscodeOpts{TargetBitrateKbps: tt.initial}
			applyCompatMaxStreamingBitrateCap(&opts, tt.cap)
			if opts.TargetBitrateKbps != tt.want {
				t.Fatalf("TargetBitrateKbps = %d, want %d", opts.TargetBitrateKbps, tt.want)
			}
		})
	}
}

func TestCompatLiveTranscodeHonorsMaxStreamingBitrateCap(t *testing.T) {
	source1080p := PlaybackMediaSource{
		Version: catalog.FileVersion{FileID: 1, Resolution: "1080p", Bitrate: 20_000},
	}
	tests := []struct {
		name   string
		opts   playback.TranscodeOpts
		source PlaybackMediaSource
		want   bool
	}{
		{
			name:   "no cap always honored",
			opts:   playback.TranscodeOpts{TargetBitrateKbps: 20_000},
			source: PlaybackMediaSource{Version: source1080p.Version, MaxStreamingBitrateKbps: 0},
			want:   true,
		},
		{
			name:   "video copy exempt from cap",
			opts:   playback.TranscodeOpts{TargetCodecVideo: compatCopyCodec},
			source: PlaybackMediaSource{Version: source1080p.Version, MaxStreamingBitrateKbps: 420},
			want:   true,
		},
		{
			name:   "unset live bitrate does not honor a cap",
			opts:   playback.TranscodeOpts{TargetCodecVideo: compatTargetVideoCodec, TargetBitrateKbps: 0},
			source: PlaybackMediaSource{Version: source1080p.Version, MaxStreamingBitrateKbps: 420},
			want:   false,
		},
		{
			name:   "live bitrate above the new cap does not honor it",
			opts:   playback.TranscodeOpts{TargetCodecVideo: compatTargetVideoCodec, TargetBitrateKbps: 20_000},
			source: PlaybackMediaSource{Version: source1080p.Version, MaxStreamingBitrateKbps: 420},
			want:   false,
		},
		{
			name:   "live bitrate under cap but source resolution not downscaled",
			opts:   playback.TranscodeOpts{TargetCodecVideo: compatTargetVideoCodec, TargetBitrateKbps: 400},
			source: PlaybackMediaSource{Version: source1080p.Version, MaxStreamingBitrateKbps: 420},
			want:   false,
		},
		{
			name:   "live bitrate and resolution both honor the cap",
			opts:   playback.TranscodeOpts{TargetCodecVideo: compatTargetVideoCodec, TargetBitrateKbps: 400, TargetResolution: "480p"},
			source: PlaybackMediaSource{Version: source1080p.Version, MaxStreamingBitrateKbps: 420},
			want:   true,
		},
		{
			name:   "cap loosened by a fresh negotiation is still honored by a tighter live encode",
			opts:   playback.TranscodeOpts{TargetCodecVideo: compatTargetVideoCodec, TargetBitrateKbps: 4_000, TargetResolution: "720p"},
			source: PlaybackMediaSource{Version: source1080p.Version, MaxStreamingBitrateKbps: 10_000},
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compatLiveTranscodeHonorsMaxStreamingBitrateCap(tt.opts, tt.source); got != tt.want {
				t.Errorf("compatLiveTranscodeHonorsMaxStreamingBitrateCap() = %v, want %v", got, tt.want)
			}
		})
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

func TestBuildPlaybackSourceMaxStreamingBitrateGate(t *testing.T) {
	// A DirectPlayProfile that would otherwise allow direct play outright, so
	// any denial in these cases must come from the bitrate cap, not codec
	// mismatch.
	permissive := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{
			{Type: "Video", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"},
		},
		TranscodingProfiles: []TranscodingProfile{
			{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac"},
		},
	}
	version := catalog.FileVersion{
		FileID:     1,
		Resolution: "1080p",
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mp4",
		Bitrate:    20_000, // 20 Mbps source
	}

	tests := []struct {
		name                 string
		req                  playbackInfoRequest
		profile              DeviceProfile
		wantDirectPlay       bool
		wantDirectStream     bool
		wantHLSRemux         bool
		wantMaxStreamingKbps int
	}{
		{
			name:                 "no cap allows direct play",
			req:                  playbackInfoRequest{},
			profile:              permissive,
			wantDirectPlay:       true,
			wantDirectStream:     true,
			wantMaxStreamingKbps: 0,
		},
		{
			name:                 "cap above source bitrate allows direct play",
			req:                  playbackInfoRequest{MaxStreamingBitrate: 25_000_000},
			profile:              permissive,
			wantDirectPlay:       true,
			wantDirectStream:     true,
			wantMaxStreamingKbps: 25_000,
		},
		{
			name:                 "request cap below source bitrate forces transcode",
			req:                  playbackInfoRequest{MaxStreamingBitrate: 4_000_000},
			profile:              permissive,
			wantMaxStreamingKbps: 4_000,
		},
		{
			name:                 "device profile cap below source bitrate forces transcode",
			req:                  playbackInfoRequest{},
			profile:              deviceProfileWithMaxBitrate(permissive, 4_000_000),
			wantMaxStreamingKbps: 4_000,
		},
		{
			name: "tighter of the two caps applies",
			req:  playbackInfoRequest{MaxStreamingBitrate: 25_000_000},
			// Profile cap (4 Mbps) is tighter than the request cap (25 Mbps).
			profile:              deviceProfileWithMaxBitrate(permissive, 4_000_000),
			wantMaxStreamingKbps: 4_000,
		},
	}

	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := h.buildPlaybackSource("item", "ps", version, tt.profile, tt.req, false)
			if source.SupportsDirectPlay != tt.wantDirectPlay {
				t.Errorf("SupportsDirectPlay = %v, want %v", source.SupportsDirectPlay, tt.wantDirectPlay)
			}
			if source.SupportsDirectStream != tt.wantDirectStream {
				t.Errorf("SupportsDirectStream = %v, want %v", source.SupportsDirectStream, tt.wantDirectStream)
			}
			if source.HLSRemux != tt.wantHLSRemux {
				t.Errorf("HLSRemux = %v, want %v", source.HLSRemux, tt.wantHLSRemux)
			}
			if source.MaxStreamingBitrateKbps != tt.wantMaxStreamingKbps {
				t.Errorf("MaxStreamingBitrateKbps = %d, want %d", source.MaxStreamingBitrateKbps, tt.wantMaxStreamingKbps)
			}
			if !tt.wantDirectPlay && !tt.wantDirectStream && !tt.wantHLSRemux && !source.SupportsTranscoding {
				t.Error("expected a full transcode to remain available when direct methods are withheld")
			}
		})
	}
}

func TestBuildPlaybackSourceMaxStreamingBitrateGateResolutionImplied(t *testing.T) {
	// A DirectPlayProfile that would otherwise allow direct play outright.
	permissive := DeviceProfile{
		DirectPlayProfiles: []DirectPlayProfile{
			{Type: "Video", Container: "mp4", VideoCodec: "h264", AudioCodec: "aac"},
		},
		TranscodingProfiles: []TranscodingProfile{
			{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac"},
		},
	}

	tests := []struct {
		name    string
		version catalog.FileVersion
		req     playbackInfoRequest
	}{
		{
			// An unusually efficient 1080p encode: its bitrate alone fits under
			// a "720p - 4 Mbps" cap, but its resolution does not.
			name: "1080p source under a 720p cap's bitrate",
			version: catalog.FileVersion{
				FileID: 1, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mp4",
				Bitrate: 3_000,
			},
			req: playbackInfoRequest{MaxStreamingBitrate: 4_000_000},
		},
		{
			// Regression: 1440p sits strictly between the 1080p and 2160p
			// buckets. Flooring it into the coarser "1080p" label before
			// comparing would equate it with a genuine 1080p source and let
			// it slip past a 10 Mbps ("1080p") cap unflagged.
			name: "1440p source under a 1080p cap",
			version: catalog.FileVersion{
				FileID: 1, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4",
				VideoTracks: []models.VideoTrack{{Height: 1440}},
				Bitrate:     3_000,
			},
			req: playbackInfoRequest{MaxStreamingBitrate: 10_000_000},
		},
	}

	h := &PlaybackHandler{codec: NewResourceIDCodec()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := h.buildPlaybackSource("item", "ps", tt.version, permissive, tt.req, false)
			if source.SupportsDirectPlay {
				t.Error("SupportsDirectPlay = true, want false: source resolution exceeds the cap's implied ceiling")
			}
			if source.SupportsDirectStream {
				t.Error("SupportsDirectStream = true, want false: source resolution exceeds the cap's implied ceiling")
			}
			if !source.SupportsTranscoding {
				t.Error("expected a full transcode to remain available")
			}
		})
	}
}

func TestCompatMaxResolutionForBitrateKbps(t *testing.T) {
	tests := []struct {
		kbps int
		want string
	}{
		{kbps: 0, want: ""},
		{kbps: 1_500, want: "480p"},
		{kbps: 4_000, want: "720p"},
		{kbps: 10_000, want: "1080p"},
		{kbps: 25_000, want: ""},
	}
	for _, tt := range tests {
		if got := compatMaxResolutionForBitrateKbps(tt.kbps); got != tt.want {
			t.Errorf("compatMaxResolutionForBitrateKbps(%d) = %q, want %q", tt.kbps, got, tt.want)
		}
	}
}

func TestCompatResolutionCeilingHeight(t *testing.T) {
	tests := []struct {
		label string
		want  int
	}{
		{label: "", want: 0},
		{label: "480p", want: 480},
		{label: "720p", want: 720},
		{label: "1080p", want: 1080},
		{label: "2160p", want: 2160},
		{label: "4320p", want: 4320},
	}
	for _, tt := range tests {
		if got := compatResolutionCeilingHeight(tt.label); got != tt.want {
			t.Errorf("compatResolutionCeilingHeight(%q) = %d, want %d", tt.label, got, tt.want)
		}
	}
}

func deviceProfileWithMaxBitrate(profile DeviceProfile, maxStreamingBitrate int64) DeviceProfile {
	profile.MaxStreamingBitrate = maxStreamingBitrate
	return profile
}

// TestApplyCompatToneMapAvailability verifies compatibility sources reflect executor availability.
func TestApplyCompatToneMapAvailability(t *testing.T) {
	hdrVersion := catalog.FileVersion{
		FileID: 1,
		HDR:    true,
		VideoTracks: []models.VideoTrack{{
			VideoRangeType: "HDR10",
			ColorPrimaries: "bt2020",
			ColorTransfer:  "smpte2084",
			ColorSpace:     "bt2020nc",
		}},
	}
	hardware := tonemap.Capability{Mode: tonemap.ModeHardware, Backend: "vaapi", Filter: "tonemap_vaapi", SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}
	software := tonemap.Capability{Mode: tonemap.ModeSoftware, Backend: "software", Filter: "tonemapx", SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}
	tests := []struct {
		name         string
		hardware     bool
		software     bool
		settingsErr  error
		nilSettings  bool
		capabilities tonemap.Capabilities
		source       PlaybackMediaSource
		want         bool
	}{
		{name: "both disabled", source: PlaybackMediaSource{SupportsTranscoding: true, Version: hdrVersion}},
		{name: "hardware enabled and available", hardware: true, capabilities: tonemap.Capabilities{hardware}, source: PlaybackMediaSource{SupportsTranscoding: true, Version: hdrVersion}, want: true},
		{name: "hardware enabled but unavailable", hardware: true, capabilities: tonemap.Capabilities{software}, source: PlaybackMediaSource{SupportsTranscoding: true, Version: hdrVersion}},
		{name: "software enabled and available", software: true, capabilities: tonemap.Capabilities{software}, source: PlaybackMediaSource{SupportsTranscoding: true, Version: hdrVersion}, want: true},
		{name: "both enabled prefer any available executor", hardware: true, software: true, capabilities: tonemap.Capabilities{software, hardware}, source: PlaybackMediaSource{SupportsTranscoding: true, Version: hdrVersion}, want: true},
		{name: "settings error denies HDR transcode", settingsErr: errors.New("read failed"), capabilities: tonemap.Capabilities{software}, source: PlaybackMediaSource{SupportsTranscoding: true, Version: hdrVersion}},
		{name: "nil settings deny HDR transcode", nilSettings: true, capabilities: tonemap.Capabilities{software}, source: PlaybackMediaSource{SupportsTranscoding: true, Version: hdrVersion}},
		{name: "audio-only transcode remains available", source: PlaybackMediaSource{SupportsTranscoding: true, TranscodeAudio: true, Version: hdrVersion}, want: true},
		{name: "direct-only source remains unavailable", source: PlaybackMediaSource{Version: hdrVersion}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := map[string]string{
				config.PlaybackTranscodeHardwareToneMapSettingKey: strconv.FormatBool(tt.hardware),
				config.PlaybackTranscodeSoftwareToneMapSettingKey: strconv.FormatBool(tt.software),
			}
			var settingsRepo SettingsReader = stubSettingsReader{values: settings, err: tt.settingsErr}
			if tt.nilSettings {
				settingsRepo = nil
			}
			h := &PlaybackHandler{SettingsRepo: settingsRepo}
			if tt.nilSettings && h.toneMapPolicy(context.Background()) != tonemap.PolicyNone {
				t.Fatal("nil settings repository did not resolve to PolicyNone")
			}
			got := h.applyCompatToneMapAvailability(context.Background(), tt.source, tt.capabilities)
			if got.SupportsTranscoding != tt.want {
				t.Fatalf("SupportsTranscoding = %t, want %t", got.SupportsTranscoding, tt.want)
			}
		})
	}
}

// TestApplyCompatToneMapAvailabilityAcceptsProfile7ID6Fallback verifies the compatible Dolby Vision base is accepted.
func TestApplyCompatToneMapAvailabilityAcceptsProfile7ID6Fallback(t *testing.T) {
	h := &PlaybackHandler{SettingsRepo: stubSettingsReader{values: map[string]string{
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}}
	source := PlaybackMediaSource{
		SupportsTranscoding: true,
		Version: catalog.FileVersion{HDR: true, VideoTracks: []models.VideoTrack{{
			DolbyVision: "Dolby Vision Profile 7",
			DVProfile:   7, DVBLCompatID: 6,
			DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true,
			ColorRange: "tv", ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc",
		}}},
	}
	capabilities := tonemap.Capabilities{{Mode: tonemap.ModeSoftware, Backend: "software", Filter: "tonemapx", SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}}
	if got := h.applyCompatToneMapAvailability(context.Background(), source, capabilities); !got.SupportsTranscoding {
		t.Fatal("Profile 7 compatibility-ID 6 fallback was not advertised as transcodable")
	}
}

// TestDowngradeToSoftwareToneMap verifies hardware recipes can safely fall back to software.
func TestDowngradeToSoftwareToneMap(t *testing.T) {
	mode := tonemap.ModeHardware
	filter := tonemap.HardwareFilterVAAPI
	hwAccel := "vaapi"
	capabilities := tonemap.Capabilities{{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware,
		Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}}
	if !downgradeToSoftwareToneMap(
		tonemap.PolicyHardwareThenSoftware, &mode, &filter, &hwAccel, tonemap.SourcePQ, capabilities,
	) {
		t.Fatal("eligible hardware recipe did not downgrade")
	}
	if mode != tonemap.ModeSoftware || filter != tonemap.SoftwareFilterBT2390 || hwAccel != playback.HWAccelNone {
		t.Fatalf("downgraded recipe = mode %q filter %q hwaccel %q", mode, filter, hwAccel)
	}
	if downgradeToSoftwareToneMap(
		tonemap.PolicyHardwareThenSoftware, &mode, &filter, &hwAccel, tonemap.SourcePQ, capabilities,
	) {
		t.Fatal("software recipe downgraded more than once")
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
	if err := h.startRemoteTranscode(context.Background(), "play", "session", source, nil, 0, "http://node"); !errors.Is(err, errTranscode4KDisallowed) {
		t.Errorf("startRemoteTranscode() error = %v, want errTranscode4KDisallowed", err)
	}
}
