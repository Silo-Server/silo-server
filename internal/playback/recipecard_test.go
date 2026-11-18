package playback

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// recipeCardFixtureOpts is a transcode recipe with every byte-affecting
// field set, for the card and claims round-trip tests.
func recipeCardFixtureOpts(revision tonemap.SourceRevision) TranscodeOpts {
	return TranscodeOpts{
		InputPath:                "/media/movie.mkv",
		OutputDir:                "/tmp/silo-transcode/abc",
		SessionID:                "abc",
		SourceVideoCodec:         "hevc",
		SourceVideoProfile:       "Main 10",
		SourceVideoBitDepth:      10,
		SoftwareVideoDecode:      true,
		ToneMapPolicy:            tonemap.PolicyHardwareThenSoftware,
		ToneMapMode:              tonemap.ModeHardware,
		ToneMapSourceKind:        tonemap.SourcePQ,
		ToneMapFilter:            "tonemap_vaapi",
		ToneMapRecipeVersion:     TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapPreflightRequired: true,
		ToneMapSourceRevision:    revision,
		ToneMapDVConfigPresent:   true, ToneMapDVBLCompatIDPresent: true, ToneMapDVBLPresent: true, ToneMapDVRPUPresent: true,
		VideoBitstreamFilter:   DV7ToHDR10BitstreamFilter,
		VideoSampleEntry:       VideoSampleEntryDVH1,
		SeekSeconds:            900,
		StreamOriginSeconds:    896,
		CopySeekAnchorResolved: true,
		TargetResolution:       "1080p",
		TargetCodecVideo:       "h264",
		TargetCodecAudio:       "aac",
		SourceAudioChannels:    6,
		TargetAudioChannels:    1,
		TargetAudioBitrateKbps: 96,
		SegmentDuration:        2,
		StartSegmentNumber:     450,
		HWAccel:                "qsv",
		HWDevice:               "/dev/dri/renderD128",
		SubtitleTrackIndex:     3,
		SubtitleBurnIn:         true,
		SubtitleCodec:          "hdmv_pgs_subtitle",
		AudioTrackIndex:        1,
		TargetBitrateKbps:      8000,
		TotalDuration:          7200,
		FastStart:              true,
		ThrottleSeconds:        180,
		CopyVideoMPEGTS:        true,
		KeyframePlaylist:       true,
		KeyframePlanned:        true,
		// Playlist form, not media bytes, but a reconstruct must keep it.
		HLSVariableSubstitution: true,
	}
}

func TestRecipeCardOriginalStartedAtRoundTripAndReconstruct(t *testing.T) {
	started := time.Date(2026, 8, 16, 12, 34, 56, 987654321, time.UTC)
	card := NewRecipeCard(42, "profile-1", 77, "", TranscodeOpts{SessionID: "started", InputPath: "/media/movie.mkv"})
	card.OriginalStartedAt = started
	card.StreamLocation = "local"
	encoded, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	var stored RecipeCard
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	if !stored.OriginalStartedAt.Equal(started) || stored.StreamLocation != "local" {
		t.Fatalf("stored-card round trip = %s, want %s", stored.OriginalStartedAt, started)
	}

	claims := card.ToClaims()
	if claims.OriginalStartedAtUnixNano != started.UnixNano() {
		t.Fatalf("ostn = %d, want %d", claims.OriginalStartedAtUnixNano, started.UnixNano())
	}
	back := RecipeCardFromClaims(&claims)
	if !back.OriginalStartedAt.Equal(started) || back.StreamLocation != "local" {
		t.Fatalf("claim round trip = %s, want %s", back.OriginalStartedAt, started)
	}

	tm := NewTranscodeManager()
	tm.Sessions = NewSessionManager(0, 0)
	session := tm.ReconstructSession(t.Context(), "started", 42, back)
	if session == nil || !session.StartedAt.Equal(started) || session.StreamLocation != "local" {
		t.Fatalf("reconstructed StartedAt = %v, want %s", session, started)
	}
}

func TestRecipeCardNetworkRouteSurvivesRecovery(t *testing.T) {
	for _, provider := range []*string{nil, new(""), new("tailscale")} {
		card := NewDirectRecipeCard("network-route", 42, "profile-1", 77)
		card.RoutingNetworkProvider = provider
		card.RoutingWorkload = "remux"
		card.RoutingExecution = "transcode"
		card.RoutingExecutionNodeID = 7
		card.RoutingEgress = "proxy"
		card.RoutingEgressNodeID = 11
		for _, token := range []bool{false, true} {
			var recovered RecipeCard
			if token {
				wire, err := json.Marshal(card.ToClaims())
				if err != nil {
					t.Fatal(err)
				}
				var claims streamtoken.Claims
				if err := json.Unmarshal(wire, &claims); err != nil {
					t.Fatal(err)
				}
				recovered = RecipeCardFromClaims(&claims)
			} else {
				wire, err := json.Marshal(card)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(wire, &recovered); err != nil {
					t.Fatal(err)
				}
			}
			tm := NewTranscodeManager()
			tm.Sessions = NewSessionManager(0, 0)
			session := tm.ReconstructSession(t.Context(), card.SessionID, card.UserID, recovered)
			if session == nil || session.RoutingExecutionNodeID != 7 || session.RoutingEgressNodeID != 11 {
				t.Fatalf("recovered route: %#v", session)
			}
			got := session.RoutingNetworkProvider
			if (got == nil) != (provider == nil) || (got != nil && *got != *provider) {
				t.Fatalf("network provider changed through recovery: got %v want %v", got, provider)
			}
		}
	}
}

// TestRecipeCardClaimsRoundTrip verifies a transcode recipe survives stream-token claims:
// the token IS the durable descriptor under token-carried reconstruction, so any
// dropped byte-affecting field would reconstruct a divergent encode. HWAccel,
// HWDevice, and the derived tone-map filter are deliberately excluded and
// re-resolved from live config, so they are not asserted here.
func TestRecipeCardClaimsRoundTrip(t *testing.T) {
	revision := tonemap.SourceRevision{MediaFileID: 77, FileSize: 100, FileModifiedUnixNano: 200, StreamSignature: "stream"}
	opts := recipeCardFixtureOpts(revision)
	opts.OutputDir, opts.HWAccel, opts.HWDevice = "", "", ""
	opts.ToneMapPolicy = tonemap.PolicySoftwareOnly
	opts.ToneMapMode = tonemap.ModeSoftware
	opts.ToneMapSourceKind = tonemap.SourceHLG
	opts.ToneMapFilter = "tonemapx"
	opts.SourceAudioChannels, opts.TargetAudioChannels, opts.TargetAudioBitrateKbps = 8, 6, 320
	opts.CopyAudioCodec = "aac"
	card := NewRecipeCard(42, "profile-1", 77, "http://node:9000", opts)
	card.RoutingWorkload = "video_transcode"
	card.RoutingExecution = "transcode"
	card.RoutingEgress = "proxy"

	claims := card.ToClaims()
	got := RecipeCardFromClaims(&claims)

	// Identity + routing.
	if got.SessionID != card.SessionID || got.UserID != card.UserID ||
		got.ProfileID != card.ProfileID || got.MediaFileID != card.MediaFileID ||
		got.TranscodeNodeURL != card.TranscodeNodeURL || got.PlayMethod != card.PlayMethod ||
		got.RoutingWorkload != card.RoutingWorkload || got.RoutingExecution != card.RoutingExecution || got.RoutingEgress != card.RoutingEgress {
		t.Fatalf("identity/routing lost: %+v", got)
	}
	// Byte-affecting encode parameters.
	if got.InputPath != card.InputPath || got.SourceVideoCodec != card.SourceVideoCodec ||
		got.SourceVideoProfile != card.SourceVideoProfile || got.SourceVideoBitDepth != card.SourceVideoBitDepth ||
		got.SourceAudioChannels != 0 ||
		got.SoftwareVideoDecode != card.SoftwareVideoDecode ||
		got.ToneMapPolicy != card.ToneMapPolicy || got.ToneMapMode != card.ToneMapMode ||
		got.ToneMapSourceKind != card.ToneMapSourceKind || got.ToneMapFilter != "" ||
		got.ToneMapRecipeVersion != card.ToneMapRecipeVersion || got.ToneMapPreflightRequired != card.ToneMapPreflightRequired || got.ToneMapSourceRevision != revision ||
		got.ToneMapDVConfigPresent != card.ToneMapDVConfigPresent || got.ToneMapDVBLCompatIDPresent != card.ToneMapDVBLCompatIDPresent || got.ToneMapDVBLPresent != card.ToneMapDVBLPresent || got.ToneMapDVRPUPresent != card.ToneMapDVRPUPresent ||
		got.VideoBitstreamFilter != card.VideoBitstreamFilter ||
		got.VideoSampleEntry != card.VideoSampleEntry ||
		got.SeekSeconds != card.SeekSeconds || got.StreamOriginSeconds != card.StreamOriginSeconds ||
		got.CopySeekAnchorResolved != card.CopySeekAnchorResolved || got.TargetResolution != card.TargetResolution ||
		got.TargetCodecVideo != card.TargetCodecVideo || got.TargetCodecAudio != card.TargetCodecAudio ||
		got.CopyAudioCodec != "aac" ||
		got.TargetAudioChannels != card.TargetAudioChannels || got.TargetAudioBitrateKbps != card.TargetAudioBitrateKbps ||
		got.SegmentDuration != card.SegmentDuration || got.StartSegmentNumber != card.StartSegmentNumber ||
		got.SubtitleTrackIndex != card.SubtitleTrackIndex || got.SubtitleBurnIn != card.SubtitleBurnIn ||
		got.SubtitleCodec != card.SubtitleCodec ||
		got.AudioTrackIndex != card.AudioTrackIndex || got.TargetBitrateKbps != card.TargetBitrateKbps ||
		got.TotalDuration != card.TotalDuration || got.FastStart != card.FastStart ||
		got.ThrottleSeconds != card.ThrottleSeconds ||
		!got.CopyVideoMPEGTS || !got.KeyframePlaylist {
		t.Fatalf("encode parameters lost in round trip (non-v2 source channels must be stripped):\n have %+v\n want %+v", got, card)
	}
	if !claims.HLSVariableSubstitution || !got.HLSVariableSubstitution {
		t.Fatal("HLSVariableSubstitution lost in the stream token")
	}
}

func TestCopyFMP4RecipeCardsFailClosedAcrossReaderGenerations(t *testing.T) {
	card := NewRecipeCard(1, "profile-1", 2, "http://node", TranscodeOpts{
		SessionID: "copy-fmp4", TargetCodecVideo: "copy", SegmentDuration: 2,
	})
	if card.CopyFMP4RecipeVersion != CopyFMP4RecipeVersion {
		t.Fatalf("stored copy recipe version = %q, want %q", card.CopyFMP4RecipeVersion, CopyFMP4RecipeVersion)
	}
	if card.PlayMethod == PlayTranscode || card.PlayMethod == "" {
		t.Fatalf("stored copy recipe method = %q, want a method rejected by old readers", card.PlayMethod)
	}
	if err := ValidateCopyFMP4RecipeCard(card); err != nil {
		t.Fatalf("current stored copy recipe rejected: %v", err)
	}

	claims := card.ToClaims()
	if claims.PlayMethod != streamtoken.PlayMethodCopyFMP4Transcode || claims.CopyFMP4RecipeVersion != CopyFMP4RecipeVersion {
		t.Fatalf("copy token discriminator/version = %q/%q", claims.PlayMethod, claims.CopyFMP4RecipeVersion)
	}
	roundTrip := RecipeCardFromClaims(&claims)
	if err := ValidateCopyFMP4RecipeCard(roundTrip); err != nil {
		t.Fatalf("round-tripped copy recipe rejected: %v", err)
	}

	legacy := RecipeCard{SessionID: "legacy-copy", PlayMethod: PlayTranscode, TargetCodecVideo: "copy", SegmentDuration: 2}
	if err := ValidateCopyFMP4RecipeCard(legacy); !errors.Is(err, ErrCopyFMP4RecipeVersionMismatch) {
		t.Fatalf("legacy copy recipe error = %v, want version mismatch", err)
	}
	ordinary := RecipeCard{SessionID: "encoded", PlayMethod: PlayTranscode, TargetCodecVideo: "h264", SegmentDuration: 2}
	if err := ValidateCopyFMP4RecipeCard(ordinary); err != nil {
		t.Fatalf("ordinary encoded recipe rejected: %v", err)
	}
}
