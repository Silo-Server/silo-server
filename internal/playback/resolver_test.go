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

var yes, no = true, false

var resolverPlayMethodCases = []struct {
	name string
	file *models.MediaFile
	// caps replaces defaultCaps when set; edit then adjusts the defaults.
	caps      *playback.ClientCapabilities
	edit      func(*playback.ClientCapabilities, *playback.AdminSettings)
	want      playback.PlayMethod
	wantAudio *bool // TranscodeAudio, when the case asserts it
}{
	{
		name: "direct play",
		file: &models.MediaFile{CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "1080p"},
		want: playback.PlayDirect,
	},
	{
		// h264+aac in mkv — client supports codecs but not container.
		name: "remux",
		file: &models.MediaFile{CodecVideo: "h264", CodecAudio: "aac", Container: "mkv", Resolution: "1080p"},
		want: playback.PlayRemux,
	},
	{
		// h264 video (supported) + dts audio (unsupported) → remux with audio transcode.
		name:      "remux with audio transcode",
		file:      &models.MediaFile{CodecVideo: "h264", CodecAudio: "dts", Container: "mkv", Resolution: "1080p"},
		want:      playback.PlayRemux,
		wantAudio: &yes,
	},
	{
		// h264+dts in mkv would normally remux with audio transcode (video copied),
		// but the source carries conflicting in-band PPS, so the video copy is unsafe
		// and it must fall through to a full transcode.
		name: "copy unsafe forces transcode",
		file: &models.MediaFile{
			CodecVideo: "h264", CodecAudio: "dts", Container: "mkv", Resolution: "1080p",
			VideoTracks: []models.VideoTrack{{Codec: "h264", MultiplePPS: &yes}},
		},
		want: playback.PlayTranscode,
	},
	{
		// An inconclusive safety scan must not fail open to video stream-copy.
		name: "unknown copy safety forces transcode",
		file: &models.MediaFile{
			CodecVideo: "h264", CodecAudio: "dts", Container: "mkv", Resolution: "1080p",
			VideoTracks: []models.VideoTrack{{Codec: "h264", VideoCopyUnsafe: true}},
		},
		want: playback.PlayTranscode,
	},
	{
		// Client can decode h264 but not eac3; its sink advertises eac3
		// passthrough (e.g. HDMI AVR). Should direct-play without audio
		// transcode instead of promoting to remux.
		name: "audio passthrough skips audio transcode",
		file: &models.MediaFile{CodecVideo: "h264", CodecAudio: "eac3", Container: "mp4", Resolution: "1080p"},
		edit: func(c *playback.ClientCapabilities, _ *playback.AdminSettings) {
			c.AudioPassthroughCodecs = []string{"eac3", "ac3"}
		},
		want:      playback.PlayDirect,
		wantAudio: &no,
	},
	{
		// Passthrough covers eac3 but the container is unsupported → remux
		// without audio transcode.
		name: "audio passthrough allows container remux",
		file: &models.MediaFile{CodecVideo: "h264", CodecAudio: "eac3", Container: "mkv", Resolution: "1080p"},
		edit: func(c *playback.ClientCapabilities, _ *playback.AdminSettings) {
			c.AudioPassthroughCodecs = []string{"eac3"}
		},
		want:      playback.PlayRemux,
		wantAudio: &no,
	},
	{
		// hevc is not in client's supported codecs.
		name: "unsupported video codec transcodes",
		file: &models.MediaFile{CodecVideo: "hevc", CodecAudio: "aac", Container: "mp4", Resolution: "1080p"},
		want: playback.PlayTranscode,
	},
	{
		name: "resolution above the client ceiling transcodes",
		file: &models.MediaFile{CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "2160p"},
		edit: func(c *playback.ClientCapabilities, _ *playback.AdminSettings) { c.MaxResolution = "1080p" },
		want: playback.PlayTranscode,
	},
	{
		// HDR should pass through without tone mapping.
		name: "HDR passes through to remux",
		file: &models.MediaFile{CodecVideo: "hevc", CodecAudio: "aac", Container: "mkv", Resolution: "1080p", HDR: true},
		edit: func(c *playback.ClientCapabilities, _ *playback.AdminSettings) {
			c.CodecsVideo = []string{"h264", "hevc"}
			c.HDR = false
		},
		want: playback.PlayRemux,
	},
	{
		name: "transcode disabled falls to direct",
		file: &models.MediaFile{CodecVideo: "hevc", CodecAudio: "aac", Container: "mkv", Resolution: "1080p"},
		edit: func(_ *playback.ClientCapabilities, s *playback.AdminSettings) { s.TranscodeEnabled = false },
		want: playback.PlayDirect,
	},
	{
		name: "detailed hardware evidence overrides legacy download ceiling",
		file: &models.MediaFile{
			CodecVideo: "hevc", CodecAudio: "aac", Container: "mp4",
			Resolution: "2160p", Bitrate: 60_000,
			VideoTracks: []models.VideoTrack{{
				Codec: "hevc", Profile: "Main 10", Width: 3840, Height: 2160,
				FrameRate: "60/1", Bitrate: 60_000, BitDepth: 10,
			}},
		},
		caps: &playback.ClientCapabilities{
			VideoEvidence: playback.EvidencePlatformAttestedV3,
			CodecsVideo:   []string{"hevc"},
			CodecsAudio:   []string{"aac"},
			Containers:    []string{"mp4"},
			// Older servers conservatively stop here. The detailed-aware server
			// validates the per-decoder 4K hardware bound instead.
			MaxResolution: "1080p",
			VideoDecode: []playback.VideoDecodeCapabilityV3{{
				Codec: "hevc", BitDepths: []int{8, 10}, MaxWidth: 3840,
				MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 120_000,
				Hardware: true,
			}},
		},
		want: playback.PlayDirect,
	},
	{
		// Complete probe facts whose decoder entry does not cover the source: a real
		// mismatch, so the flat-list claim must not rescue it.
		name: "detailed evidence fails closed on complete metadata mismatch",
		file: &models.MediaFile{
			CodecVideo: "av1", CodecAudio: "aac", Container: "mp4",
			Resolution: "2160p", Bitrate: 55_000,
			VideoTracks: []models.VideoTrack{{
				Codec: "av1", Profile: "Main", Width: 3840, Height: 2160, FrameRate: "60/1",
				Bitrate: 55_000, BitDepth: 10,
			}},
		},
		caps: &playback.ClientCapabilities{
			VideoEvidence: playback.EvidencePlatformAttestedV3,
			CodecsVideo:   []string{"av1"},
			CodecsAudio:   []string{"aac"},
			Containers:    []string{"mp4"},
			MaxResolution: "2160p",
			VideoDecode: []playback.VideoDecodeCapabilityV3{{
				Codec: "av1", BitDepths: []int{8, 10}, MaxWidth: 1920,
				MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 40_000,
				Hardware: true,
			}},
		},
		want: playback.PlayTranscode,
	},
	{
		// Detailed platform_attested caps with a hardware entry bounded to
		// 1920x1080, but the source's probe metadata is incomplete (zero bitrate)
		// so the detailed bounds walk cannot run at all. The coarse max_resolution
		// ceiling must still apply — same outcome as a flat-only payload — rather
		// than approving an original-quality 2160p download past the device ceiling.
		name: "detailed caps with sparse metadata fail closed to the coarse ceiling",
		file: &models.MediaFile{
			CodecVideo: "hevc", CodecAudio: "aac", Container: "mp4",
			Resolution: "2160p", Bitrate: 0,
			VideoTracks: []models.VideoTrack{{
				Codec: "hevc", Profile: "Main 10", Width: 3840, Height: 2160,
				FrameRate: "60/1", Bitrate: 0, BitDepth: 10,
			}},
		},
		caps: &playback.ClientCapabilities{
			VideoEvidence: playback.EvidencePlatformAttestedV3,
			CodecsVideo:   []string{"hevc"},
			CodecsAudio:   []string{"aac"},
			Containers:    []string{"mp4"},
			MaxResolution: "1080p",
			VideoDecode: []playback.VideoDecodeCapabilityV3{{
				Codec: "hevc", BitDepths: []int{8, 10}, MaxWidth: 1920,
				MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 40_000,
				Hardware: true,
			}},
		},
		want: playback.PlayTranscode,
	},
	{
		// The download's fallback encode would keep 1080p50, so keeping the
		// original takes the same frame-rate trade as playback.
		name: "a download keeps a 1080p50 original on an exact 4K30 decoder",
		file: &models.MediaFile{
			CodecVideo: "h264", CodecAudio: "aac", Container: "mp4",
			Resolution: "1080p", Bitrate: 3_000,
			VideoTracks: []models.VideoTrack{{
				Codec: "h264", Profile: "High", Level: 42, Width: 1920, Height: 1080,
				FrameRate: "50/1", Bitrate: 3_000, BitDepth: 8,
			}},
		},
		caps: &playback.ClientCapabilities{
			VideoEvidence: playback.EvidenceExactV3,
			CodecsVideo:   []string{"h264"},
			CodecsAudio:   []string{"aac"},
			Containers:    []string{"mp4"},
			MaxResolution: "2160p",
			VideoDecode: []playback.VideoDecodeCapabilityV3{{
				Codec: "h264", Profiles: []string{"high"}, Levels: []int{51}, BitDepths: []int{8},
				MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 30, MaxBitrateKbps: 300_000,
				Hardware: true,
			}},
		},
		want: playback.PlayDirect,
	},
}

func TestResolverPlayMethod(t *testing.T) {
	for _, tc := range resolverPlayMethodCases {
		t.Run(tc.name, func(t *testing.T) {
			caps, settings := defaultCaps(), defaultSettings()
			if tc.caps != nil {
				caps = *tc.caps
			}
			if tc.edit != nil {
				tc.edit(&caps, &settings)
			}
			decision := playback.Resolve(tc.file, caps, settings)
			if decision.Method != tc.want {
				t.Errorf("method = %q, want %q", decision.Method, tc.want)
			}
			if tc.wantAudio != nil && decision.TranscodeAudio != *tc.wantAudio {
				t.Errorf("TranscodeAudio = %v, want %v", decision.TranscodeAudio, *tc.wantAudio)
			}
		})
	}
}

func TestResolver_DownloadSoftwareDecodeIsFeatureGatedAndBounded(t *testing.T) {
	file := &models.MediaFile{
		CodecVideo: "av1", CodecAudio: "aac", Container: "mp4",
		Resolution: "1080p", Bitrate: 9_000,
		VideoTracks: []models.VideoTrack{{
			Codec: "av1", Profile: "Main", Width: 1920, Height: 1080, FrameRate: "24/1",
			Bitrate: 9_000, BitDepth: 10,
		}},
	}
	caps := playback.ClientCapabilities{
		ClientFeatures: []string{playback.FeatureSoftwareVideoDecodeV3},
		VideoEvidence:  playback.EvidencePlatformAttestedV3,
		CodecsVideo:    []string{"av1"},
		CodecsAudio:    []string{"aac"},
		Containers:     []string{"mp4"},
		MaxResolution:  "2160p",
		VideoDecode: []playback.VideoDecodeCapabilityV3{{
			Codec: "av1", Profiles: []string{"main"}, BitDepths: []int{10}, MaxWidth: 1920,
			MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 40_000,
			Hardware: false,
		}},
	}

	if decision := playback.Resolve(file, caps, defaultSettings()); decision.Method != playback.PlayDirect {
		t.Fatalf("opted-in bounded software source = %q, want direct", decision.Method)
	}

	withoutFeature := caps
	withoutFeature.ClientFeatures = nil
	if decision := playback.Resolve(file, withoutFeature, defaultSettings()); decision.Method != playback.PlayTranscode {
		t.Fatalf("software source without opt-in = %q, want transcode", decision.Method)
	}

	file.Resolution = "2160p"
	file.VideoTracks[0].Width = 3840
	file.VideoTracks[0].Height = 2160
	if decision := playback.Resolve(file, caps, defaultSettings()); decision.Method != playback.PlayTranscode {
		t.Fatalf("software source beyond decoder bounds = %q, want transcode", decision.Method)
	}
}

func TestResolver_DetailedDownloadEvidenceFallsBackToFlatListsOnSparseProbeFacts(t *testing.T) {
	// No video tracks: bit depth, dimensions, frame rate and bitrate are all
	// unknown, so the decoder bounds cannot be evaluated. "Can't tell" must not
	// force a transcode of an original-quality download.
	file := &models.MediaFile{
		CodecVideo: "av1", CodecAudio: "aac", Container: "mp4",
		Resolution: "1080p",
	}
	caps := playback.ClientCapabilities{
		ClientFeatures: []string{playback.FeatureSoftwareVideoDecodeV3},
		VideoEvidence:  playback.EvidencePlatformAttestedV3,
		CodecsVideo:    []string{"av1"},
		CodecsAudio:    []string{"aac"},
		Containers:     []string{"mp4"},
		MaxResolution:  "2160p",
		VideoDecode: []playback.VideoDecodeCapabilityV3{{
			Codec: "av1", BitDepths: []int{8, 10}, MaxWidth: 1920,
			MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 40_000,
		}},
	}

	if decision := playback.Resolve(file, caps, defaultSettings()); decision.Method != playback.PlayDirect {
		t.Fatalf("sparse-metadata source with detailed caps = %q, want direct", decision.Method)
	}

	flatOnly := caps
	flatOnly.VideoEvidence = ""
	flatOnly.VideoDecode = nil
	flat := playback.Resolve(file, flatOnly, defaultSettings())
	if detailed := playback.Resolve(file, caps, defaultSettings()); detailed.Method != flat.Method {
		t.Fatalf("sparse-metadata detailed caps = %q, flat caps = %q; want identical", detailed.Method, flat.Method)
	}
}

func TestNormalizeAndValidateVideoDecode(t *testing.T) {
	tests := []struct {
		name    string
		caps    playback.ClientCapabilities
		wantErr bool
	}{
		{
			name: "declared evidence with flat lists only",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.EvidenceDeclaredV3,
				CodecsVideo:   []string{"h264"},
				CodecsAudio:   []string{"aac"},
				Containers:    []string{"mp4"},
			},
		},
		{
			name: "legacy flat payload",
			caps: playback.ClientCapabilities{CodecsVideo: []string{"h264"}},
		},
		{
			name: "platform attested entries",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.EvidencePlatformAttestedV3,
				CodecsVideo:   []string{"H264"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "H264", MaxWidth: 1920, MaxHeight: 1080, Hardware: true,
				}},
			},
		},
		{
			name: "entries with declared evidence",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.EvidenceDeclaredV3,
				CodecsVideo:   []string{"av1"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "av1", Hardware: true,
				}},
			},
			wantErr: true,
		},
		{
			name: "entries without evidence",
			caps: playback.ClientCapabilities{
				CodecsVideo: []string{"av1"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "av1", Hardware: true,
				}},
			},
			wantErr: true,
		},
		{
			name: "malformed entry",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.EvidencePlatformAttestedV3,
				CodecsVideo:   []string{"av1"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "av1", MaxWidth: -1, Hardware: true,
				}},
			},
			wantErr: true,
		},
		{
			// A typo'd tier on a flat payload is a client bug the v3 playback
			// path rejects outright. Silently resolving it from the flat lists
			// here would hide the bug behind a working-looking answer.
			name: "unknown evidence tier without entries",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.CapabilityEvidenceV3("exat"),
				CodecsVideo:   []string{"h264"},
				CodecsAudio:   []string{"aac"},
				Containers:    []string{"mp4"},
			},
			wantErr: true,
		},
		{
			name: "unknown evidence tier with entries",
			caps: playback.ClientCapabilities{
				VideoEvidence: playback.CapabilityEvidenceV3("platform-attested"),
				CodecsVideo:   []string{"av1"},
				VideoDecode: []playback.VideoDecodeCapabilityV3{{
					Codec: "av1", MaxWidth: 1920, MaxHeight: 1080, Hardware: true,
				}},
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := tc.caps
			err := caps.NormalizeAndValidateVideoDecode()
			if tc.wantErr {
				if err == nil {
					t.Fatal("err = nil, want a validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

func TestNormalizeAndValidateVideoDecodeLowercasesDetailedEntries(t *testing.T) {
	caps := playback.ClientCapabilities{
		VideoEvidence: playback.EvidencePlatformAttestedV3,
		CodecsVideo:   []string{" HEVC "},
		VideoDecode: []playback.VideoDecodeCapabilityV3{{
			Codec: " HEVC ", MaxWidth: 3840, MaxHeight: 2160, Hardware: true,
		}},
	}
	if err := caps.NormalizeAndValidateVideoDecode(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if caps.CodecsVideo[0] != "hevc" || caps.VideoDecode[0].Codec != "hevc" {
		t.Fatalf("normalization did not apply: %+v", caps)
	}
}
