package downloads

import (
	"context"
	"errors"
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
	detailedCaps := playback.ClientCapabilities{
		VideoEvidence: playback.EvidencePlatformAttestedV3,
		CodecsVideo:   []string{"hevc"},
		CodecsAudio:   []string{"aac"},
		Containers:    []string{"mp4"},
		VideoDecode: []playback.VideoDecodeCapabilityV3{{
			Codec: "hevc", BitDepths: []int{8, 10}, MaxWidth: 1920,
			MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 40_000,
			Hardware: true,
		}},
	}

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
