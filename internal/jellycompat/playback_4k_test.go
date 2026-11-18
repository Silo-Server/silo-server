package jellycompat

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
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

func TestVideoToolboxAutomaticBitratePreservesClientCeiling(t *testing.T) {
	version := catalog.FileVersion{Resolution: "1080p"}
	recipe := compatToneMapRecipe{mode: tonemap.ModeHardware, hwAccel: tonemap.BackendVideoToolbox}
	if got := compatVideoToolboxToneMapBitrateKbps(version, recipe, 0); got <= 0 {
		t.Fatal("fixture did not select automatic VideoToolbox bitrate")
	}
	if got := compatVideoToolboxToneMapBitrateKbps(version, recipe, 1708); got != 0 {
		t.Fatalf("automatic bitrate %d overrides explicit client ceiling", got)
	}
}
