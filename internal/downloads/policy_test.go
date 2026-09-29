package downloads

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestDownloadQualityResolverResolve(t *testing.T) {
	file := &models.MediaFile{
		ID:         1,
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mp4",
		Resolution: "1080p",
	}
	remuxFile := &models.MediaFile{
		ID:         2,
		CodecVideo: "h264",
		CodecAudio: "aac",
		Container:  "mkv",
		Resolution: "1080p",
	}
	transcodeFile := &models.MediaFile{
		ID:         3,
		CodecVideo: "hevc",
		CodecAudio: "aac",
		Container:  "mp4",
		Resolution: "1080p",
	}
	// Sparse probe metadata: no video track, so bit depth, dimensions, frame
	// rate and bitrate are unknown and the detailed decoder bounds cannot be
	// evaluated against the source.
	sparseFile := &models.MediaFile{
		ID:         4,
		CodecVideo: "hevc",
		CodecAudio: "aac",
		Container:  "mp4",
		Resolution: "1080p",
	}
	boundedFile := &models.MediaFile{
		ID:         5,
		CodecVideo: "hevc",
		CodecAudio: "aac",
		Container:  "mp4",
		Resolution: "2160p",
		Bitrate:    55_000,
		VideoTracks: []models.VideoTrack{{
			Codec: "hevc", Profile: "Main 10", Width: 3840, Height: 2160,
			FrameRate: "60/1", Bitrate: 55_000, BitDepth: 10,
		}},
	}
	caps := playback.ClientCapabilities{
		CodecsVideo: []string{"h264"},
		CodecsAudio: []string{"aac"},
		Containers:  []string{"mp4"},
	}
	hevcDecoder := playback.VideoDecodeCapabilityV3{
		Codec: "hevc", BitDepths: []int{8, 10}, MaxWidth: 1920,
		MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 40_000,
		Hardware: true,
	}
	detailedCaps := playback.ClientCapabilities{
		VideoEvidence: playback.EvidencePlatformAttestedV3,
		CodecsVideo:   []string{"h264", "hevc"},
		CodecsAudio:   []string{"aac"},
		Containers:    []string{"mp4"},
		VideoDecode: []playback.VideoDecodeCapabilityV3{hevcDecoder, {
			Codec: "h264", BitDepths: []int{8}, MaxWidth: 1920,
			MaxHeight: 1080, MaxFrameRate: 60, Hardware: true,
		}},
	}
	// Strict caps that attest no H.264 decoder leave a server without HEVC
	// encoding nothing the device is known to play.
	hevcOnlyCaps := detailedCaps
	hevcOnlyCaps.CodecsVideo = []string{"hevc"}
	hevcOnlyCaps.VideoDecode = []playback.VideoDecodeCapabilityV3{hevcDecoder}

	cases := []struct {
		name               string
		requested          string
		file               *models.MediaFile
		caps               playback.ClientCapabilities
		transcodeEnabled   bool
		userTranscode      bool
		artifactsAvailable bool
		wantFormat         string
		wantQuality        string
		wantEffective      string
		wantBitrate        int
		wantErr            error
	}{
		{
			name:          "empty defaults to direct original without caps",
			file:          file,
			wantFormat:    FormatOriginal,
			wantQuality:   QualityOriginal,
			wantEffective: QualityOriginal,
		},
		{
			name:               "bitrate quality resolves to transcode target",
			requested:          Quality5Mbps,
			file:               file,
			transcodeEnabled:   true,
			userTranscode:      true,
			artifactsAvailable: true,
			wantFormat:         FormatTranscode,
			wantQuality:        Quality5Mbps,
			wantEffective:      Quality5Mbps,
			wantBitrate:        5000,
		},
		{
			name:               "original falls back to remux when container is incompatible",
			requested:          QualityOriginal,
			file:               remuxFile,
			caps:               caps,
			transcodeEnabled:   true,
			userTranscode:      true,
			artifactsAvailable: true,
			wantFormat:         FormatRemux,
			wantQuality:        QualityOriginal,
			wantEffective:      QualityOriginal,
		},
		{
			name:               "original falls back to 20mbps when video transcode is required",
			requested:          QualityOriginal,
			file:               transcodeFile,
			caps:               caps,
			transcodeEnabled:   true,
			userTranscode:      true,
			artifactsAvailable: true,
			wantFormat:         FormatTranscode,
			wantQuality:        QualityOriginal,
			wantEffective:      Quality20Mbps,
			wantBitrate:        20000,
		},
		{
			// "Can't tell" must not cost the user an original download: with
			// probe metadata too sparse to check the decoder bounds, the flat
			// codec lists decide, exactly as they do without detailed caps.
			name:               "original with detailed caps stays direct when probe metadata is sparse",
			requested:          QualityOriginal,
			file:               sparseFile,
			caps:               detailedCaps,
			transcodeEnabled:   true,
			userTranscode:      true,
			artifactsAvailable: true,
			wantFormat:         FormatOriginal,
			wantQuality:        QualityOriginal,
			wantEffective:      QualityOriginal,
		},
		{
			name:               "original with detailed caps transcodes a source beyond the decoder bounds",
			requested:          QualityOriginal,
			file:               boundedFile,
			caps:               detailedCaps,
			transcodeEnabled:   true,
			userTranscode:      true,
			artifactsAvailable: true,
			wantFormat:         FormatTranscode,
			wantQuality:        QualityOriginal,
			wantEffective:      Quality20Mbps,
			wantBitrate:        20000,
		},
		{
			name:               "no attested decoder for a converted download",
			requested:          Quality10Mbps,
			file:               boundedFile,
			caps:               hevcOnlyCaps,
			transcodeEnabled:   true,
			userTranscode:      true,
			artifactsAvailable: true,
			wantErr:            ErrQualityUnavailable,
		},
		{
			name:      "remux is not a public quality",
			requested: FormatRemux,
			file:      file,
			wantErr:   ErrInvalidQuality,
		},
		{
			name:               "bitrate blocked by server gate",
			requested:          Quality10Mbps,
			file:               file,
			userTranscode:      true,
			artifactsAvailable: true,
			wantErr:            ErrTranscodeDisabled,
		},
		{
			name:               "bitrate blocked by user flag",
			requested:          Quality10Mbps,
			file:               file,
			transcodeEnabled:   true,
			artifactsAvailable: true,
			wantErr:            ErrDownloadNotAllowed,
		},
		{
			name:             "bitrate blocked without artifact pipeline",
			requested:        Quality10Mbps,
			file:             file,
			transcodeEnabled: true,
			userTranscode:    true,
			wantErr:          ErrQualityUnavailable,
		},
	}

	var resolver DownloadQualityResolver
	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := &PolicyUser{Policy: access.EffectiveUserPolicy{DownloadAllowed: true, DownloadTranscodeAllowed: tc.userTranscode}}
			cfg := config.DownloadConfig{Enabled: true, TranscodeEnabled: tc.transcodeEnabled}

			got, err := resolver.Resolve(ctx, tc.requested, user, cfg, tc.file, tc.caps, tc.artifactsAvailable, "")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Resolve(%q) err = %v, want %v", tc.requested, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q) unexpected err: %v", tc.requested, err)
			}
			if got.DeliveryFormat != tc.wantFormat || got.RequestedQuality != tc.wantQuality ||
				got.EffectiveQuality != tc.wantEffective || got.TargetBitrateKbps != tc.wantBitrate {
				t.Fatalf("Resolve(%q) = %+v", tc.requested, got)
			}
		})
	}
}

func ladderPolicyFile(width, height int, codec string, bitrateKbps int, hdr bool) *models.MediaFile {
	track := models.VideoTrack{Codec: codec, Profile: "High", Width: width, Height: height, FrameRate: "24000/1001", BitDepth: 8, Bitrate: bitrateKbps}
	if hdr {
		track.ColorTransfer, track.ColorPrimaries, track.BitDepth, track.Profile = "smpte2084", "bt2020", 10, "Main 10"
	}
	return &models.MediaFile{
		ID: 9, CodecVideo: codec, CodecAudio: "aac", Container: "mp4", Resolution: "1080p",
		Bitrate: bitrateKbps + 256, HDR: hdr, VideoTracks: []models.VideoTrack{track},
	}
}

func attestedDownloadCaps() playback.ClientCapabilities {
	return playback.ClientCapabilities{
		VideoEvidence: playback.EvidencePlatformAttestedV3,
		CodecsVideo:   []string{"h264", "hevc"},
		CodecsAudio:   []string{"aac"},
		Containers:    []string{"mp4"},
		VideoDecode: []playback.VideoDecodeCapabilityV3{
			{Codec: "h264", BitDepths: []int{8}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 120_000, Hardware: true},
			{Codec: "hevc", BitDepths: []int{8, 10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 120_000, Hardware: true},
		},
	}
}

func TestResolvePresetOnTheLadder(t *testing.T) {
	var resolver DownloadQualityResolver
	user := &PolicyUser{Policy: access.EffectiveUserPolicy{DownloadAllowed: true, DownloadTranscodeAllowed: true}}
	cfg := config.DownloadConfig{Enabled: true, TranscodeEnabled: true}
	for _, tc := range []struct {
		name          string
		requested     string
		file          *models.MediaFile
		caps          playback.ClientCapabilities
		hevc          bool
		wantFormat    string
		wantEffective string
		wantCodec     string
		wantRes       string
		wantBitrate   int
	}{
		{"a small source the device plays is served as-is", Quality5Mbps, ladderPolicyFile(1920, 1080, "h264", 3_000, false), attestedDownloadCaps(), false, FormatOriginal, QualityOriginal, "", "", 0},
		{"without caps the fitting source is still transcoded", Quality5Mbps, ladderPolicyFile(1920, 1080, "h264", 3_000, false), playback.ClientCapabilities{}, false, FormatTranscode, Quality5Mbps, "h264", "", 3_000},
		{"an HDR source that fits is still converted", Quality20Mbps, ladderPolicyFile(1920, 1080, "hevc", 8_000, true), attestedDownloadCaps(), false, FormatTranscode, Quality20Mbps, "h264", "", 13_333},
		{"a source above the preset bitrate is transcoded", Quality5Mbps, ladderPolicyFile(1920, 1080, "h264", 12_000, false), attestedDownloadCaps(), false, FormatTranscode, Quality5Mbps, "h264", "", 5_000},
		{"1 Mbps drops 1080p to 480p", Quality1Mbps, ladderPolicyFile(1920, 1080, "h264", 12_000, false), attestedDownloadCaps(), false, FormatTranscode, Quality1Mbps, "h264", "480p", 1_000},
		{"HEVC output when the server allows it", Quality10Mbps, ladderPolicyFile(3840, 2160, "hevc", 40_000, false), attestedDownloadCaps(), true, FormatTranscode, Quality10Mbps, "hevc", "1080p", 10_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfg
			cfg.AllowHEVCEncoding = tc.hevc
			got, err := resolver.Resolve(context.Background(), tc.requested, user, cfg, tc.file, tc.caps, true, "")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.RequestedQuality != tc.requested {
				t.Errorf("RequestedQuality = %q, want the requested %q", got.RequestedQuality, tc.requested)
			}
			if got.DeliveryFormat != tc.wantFormat || got.EffectiveQuality != tc.wantEffective ||
				got.PrepareTarget.CodecVideo != tc.wantCodec || got.PrepareTarget.Resolution != tc.wantRes || got.TargetBitrateKbps != tc.wantBitrate {
				t.Fatalf("Resolve = format %q effective %q codec %q res %q bitrate %d", got.DeliveryFormat, got.EffectiveQuality, got.PrepareTarget.CodecVideo, got.PrepareTarget.Resolution, got.TargetBitrateKbps)
			}
		})
	}
}

// Without 4K transcoding a preset stops at 1080p, as quality_options says: a
// 4K original within the preset is not served as-is and a 1440p source is
// scaled down. With it, the same original is served, unless the account's
// own quality ceiling is 1080p.
func TestResolvePresetHonorsTheFourKSetting(t *testing.T) {
	var resolver DownloadQualityResolver
	uhd := ladderPolicyFile(3840, 2160, "h264", 15_000, false)
	qhd := ladderPolicyFile(2560, 1440, "h264", 25_000, false)
	for _, tc := range []struct {
		name       string
		file       *models.MediaFile
		allow4K    bool
		maxQuality string
		wantFormat string
		wantRes    string
	}{
		{"4K off: a playable 4K original is not served", uhd, false, "", FormatTranscode, "1080p"},
		{"4K on: the same original is served", uhd, true, "", FormatOriginal, ""},
		{"4K off: 1440p scales to 1080p", qhd, false, "", FormatTranscode, "1080p"},
		{"4K on: 1440p keeps its size", qhd, true, "", FormatTranscode, ""},
		{"4K on, account capped at 1080p: 4K scales to 1080p", uhd, true, "1080p", FormatTranscode, "1080p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := &PolicyUser{Policy: access.EffectiveUserPolicy{DownloadAllowed: true, DownloadTranscodeAllowed: true, MaxPlaybackQuality: tc.maxQuality}}
			cfg := config.DownloadConfig{Enabled: true, TranscodeEnabled: true, Allow4KTranscode: tc.allow4K}
			got, err := resolver.Resolve(context.Background(), Quality20Mbps, user, cfg, tc.file, attestedDownloadCaps(), true, "")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.DeliveryFormat != tc.wantFormat || got.PrepareTarget.Resolution != tc.wantRes {
				t.Fatalf("Resolve = format %q res %q, want %q %q", got.DeliveryFormat, got.PrepareTarget.Resolution, tc.wantFormat, tc.wantRes)
			}
		})
	}
}

func TestQualityOptionsFor(t *testing.T) {
	presets := []string{QualityOriginal, Quality20Mbps, Quality10Mbps, Quality5Mbps, Quality2Mbps, Quality1Mbps}
	heights := func(cfg config.DownloadConfig, user *PolicyUser, policyCeiling string) []int {
		var out []int
		for _, option := range qualityOptionsFor(presets, cfg, user, policyCeiling) {
			out = append(out, option.MaxHeight)
		}
		return out
	}
	for _, tc := range []struct {
		name          string
		cfg           config.DownloadConfig
		user          *PolicyUser
		policyCeiling string
		want          []int
	}{
		{"4K transcoding allowed", config.DownloadConfig{Allow4KTranscode: true}, nil, "", []int{0, 2160, 1080, 1080, 720, 480}},
		{"4K transcoding off caps at 1080p", config.DownloadConfig{}, nil, "", []int{0, 1080, 1080, 1080, 720, 480}},
		{"HEVC stretches the lowest preset", config.DownloadConfig{Allow4KTranscode: true, AllowHEVCEncoding: true}, nil, "", []int{0, 2160, 1080, 1080, 720, 540}},
		{"a policy ceiling applies", config.DownloadConfig{Allow4KTranscode: true}, &PolicyUser{Policy: access.EffectiveUserPolicy{MaxPlaybackQuality: "1080p"}}, "", []int{0, 1080, 1080, 1080, 720, 480}},
		{"an override's transcode ceiling applies", config.DownloadConfig{Allow4KTranscode: true}, &PolicyUser{Policy: access.EffectiveUserPolicy{MaxPlaybackQuality: "2160p"}}, "1080p", []int{0, 1080, 1080, 1080, 720, 480}},
	} {
		if got := heights(tc.cfg, tc.user, tc.policyCeiling); !slices.Equal(got, tc.want) {
			t.Errorf("%s: max heights = %v, want %v", tc.name, got, tc.want)
		}
	}
	options := qualityOptionsFor(presets, config.DownloadConfig{}, nil, "")
	if options[0] != (QualityOption{Preset: QualityOriginal}) || options[2] != (QualityOption{Preset: Quality10Mbps, BitrateKbps: 10_000, MaxHeight: 1080}) {
		t.Fatalf("options = %+v", options)
	}
}
