package playback

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/clientip"
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
		// Playlist form, not media bytes, but a reconstruct must keep it.
		HLSVariableSubstitution: true,
	}
}

// TestRecipeCardRoundTripOpts verifies recipe cards restore transcode options.
func TestRecipeCardRoundTripOpts(t *testing.T) {
	revision := tonemap.SourceRevision{MediaFileID: 77, FileSize: 100, FileModifiedUnixNano: 200, StreamSignature: "stream"}
	opts := recipeCardFixtureOpts(revision)

	card := NewRecipeCard(42, "profile-1", 77, "", opts)
	if card.SessionID != "abc" || card.UserID != 42 || card.ProfileID != "profile-1" || card.MediaFileID != 77 {
		t.Fatalf("identity not captured: %+v", card)
	}

	// Rebuild opts; environment-specific fields are re-supplied by the caller.
	got := card.TranscodeOpts("/tmp/silo-transcode/abc", "/usr/bin/ffmpeg", nil)
	checkRoundTripStreamOpts(t, got)
	checkRoundTripRecipeOpts(t, got, opts, revision)
}

// checkRoundTripStreamOpts checks the seek, track and encode fields
// TestRecipeCardRoundTripOpts rebuilds from the card.
func checkRoundTripStreamOpts(t *testing.T, got TranscodeOpts) {
	t.Helper()
	if got.StartSegmentNumber != 450 {
		t.Errorf("StartSegmentNumber = %d, want 450", got.StartSegmentNumber)
	}
	if got.SeekSeconds != 900 {
		t.Errorf("SeekSeconds = %v, want 900", got.SeekSeconds)
	}
	if got.StreamOriginSeconds != 896 || !got.CopySeekAnchorResolved {
		t.Errorf("copy seek anchor lost: origin=%v resolved=%v", got.StreamOriginSeconds, got.CopySeekAnchorResolved)
	}
	if !got.SubtitleBurnIn {
		t.Errorf("SubtitleBurnIn lost in round trip")
	}
	if got.SubtitleCodec != "hdmv_pgs_subtitle" {
		t.Errorf("SubtitleCodec = %q, want hdmv_pgs_subtitle", got.SubtitleCodec)
	}
	if got.AudioTrackIndex != 1 || got.SubtitleTrackIndex != 3 {
		t.Errorf("track indices wrong: audio=%d sub=%d", got.AudioTrackIndex, got.SubtitleTrackIndex)
	}
	if got.TargetCodecVideo != "h264" || got.TargetBitrateKbps != 8000 {
		t.Errorf("encode params wrong: %+v", got)
	}
	if got.SourceAudioChannels != 6 || got.TargetAudioChannels != 1 || got.TargetAudioBitrateKbps != 96 {
		t.Errorf("audio encode params wrong: %+v", got)
	}
	if got.ThrottleSeconds != 180 {
		t.Errorf("ThrottleSeconds = %d, want 180", got.ThrottleSeconds)
	}
}

// checkRoundTripRecipeOpts checks the video recipe, tone-map and
// environment fields TestRecipeCardRoundTripOpts rebuilds from the card.
func checkRoundTripRecipeOpts(t *testing.T, got, opts TranscodeOpts, revision tonemap.SourceRevision) {
	t.Helper()
	if got.VideoBitstreamFilter != DV7ToHDR10BitstreamFilter {
		t.Errorf("VideoBitstreamFilter = %q", got.VideoBitstreamFilter)
	}
	if got.VideoSampleEntry != VideoSampleEntryDVH1 {
		t.Errorf("VideoSampleEntry = %q", got.VideoSampleEntry)
	}
	if !got.SoftwareVideoDecode {
		t.Error("SoftwareVideoDecode lost in round trip")
	}
	if got.ToneMapPolicy != opts.ToneMapPolicy || got.ToneMapMode != opts.ToneMapMode ||
		got.ToneMapSourceKind != opts.ToneMapSourceKind || got.ToneMapFilter != opts.ToneMapFilter ||
		got.ToneMapRecipeVersion != opts.ToneMapRecipeVersion || got.ToneMapPreflightRequired != opts.ToneMapPreflightRequired || got.ToneMapSourceRevision != revision {
		t.Errorf("tone-map recipe lost in round trip: %+v", got)
	}
	if !got.ToneMapDVConfigPresent || !got.ToneMapDVBLCompatIDPresent || !got.ToneMapDVBLPresent || !got.ToneMapDVRPUPresent {
		t.Errorf("Dolby Vision presence flags lost in round trip: %+v", got)
	}
	if got.SourceVideoProfile != "Main 10" || got.SourceVideoBitDepth != 10 {
		t.Errorf("source video facts lost in round trip: profile=%q bit_depth=%d", got.SourceVideoProfile, got.SourceVideoBitDepth)
	}
	if got.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("FFmpegPath not re-supplied: %q", got.FFmpegPath)
	}
	if !got.CopyVideoMPEGTS || !got.KeyframePlaylist {
		t.Errorf("copy-video MPEG-TS or keyframe playlist lost in round trip: %+v", got)
	}
	if !got.HLSVariableSubstitution {
		t.Error("HLSVariableSubstitution lost in round trip")
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

// A transcode card must record whether audio is actually re-encoded so the
// reconstructed session classifies correctly in admin views: only an explicit
// "copy" leaves the audio untouched (an empty codec runs ffmpeg's aac default).
func TestRecipeCardDerivesTranscodeAudioFromOpts(t *testing.T) {
	cases := []struct {
		codec string
		want  bool
	}{
		{"aac", true},
		{"eac3", true},
		{"", true},
		{"copy", false},
		{"COPY", false},
	}
	for _, tc := range cases {
		card := NewRecipeCard(1, "p", 2, "", TranscodeOpts{SessionID: "t", TargetCodecAudio: tc.codec})
		if card.TranscodeAudio != tc.want {
			t.Errorf("TargetCodecAudio %q: TranscodeAudio = %v, want %v", tc.codec, card.TranscodeAudio, tc.want)
		}
	}
}

// Client metadata rides in stored cards (label + JF pill survive restarts) but
// must NOT leak into stream-token claims, where a user agent would bloat every
// stream URL.
func TestRecipeCardClientMetadataStoredNotInClaims(t *testing.T) {
	card := NewRecipeCard(1, "p", 2, "", TranscodeOpts{SessionID: "t"})
	card.ClientName = "Findroid"
	card.ClientVersion = "0.15"
	card.ClientBuild = "20260814"
	card.ClientChannel = "beta"
	card.ClientUserAgent = "Findroid/0.15"

	encoded, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	var back RecipeCard
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	if back.ClientName != "Findroid" || back.ClientVersion != "0.15" || back.ClientBuild != "20260814" || back.ClientChannel != "beta" || back.ClientUserAgent != "Findroid/0.15" {
		t.Fatalf("client metadata lost in stored-card round trip: %+v", back)
	}

	claims := card.ToClaims()
	fromClaims := RecipeCardFromClaims(&claims)
	if fromClaims.ClientName != "" || fromClaims.ClientVersion != "" || fromClaims.ClientBuild != "" || fromClaims.ClientChannel != "" || fromClaims.ClientUserAgent != "" {
		t.Fatalf("client metadata must not travel via token claims: %+v", fromClaims)
	}
}

// TestReconstructSessionRestoresClientMetadata verifies a rebuilt session keeps its client identity,
// so the admin client label and Jellyfin pill survive a server restart, and takes the viewer's
// address from the media request that rebuilt it.
func TestReconstructSessionRestoresClientMetadata(t *testing.T) {
	tm := NewTranscodeManager()
	tm.Sessions = NewSessionManager(0, 0)

	card := NewRecipeCard(42, "profile-1", 77, "", TranscodeOpts{SessionID: "sess-jf", InputPath: "/media/movie.mkv", HWAccel: "qsv", ToneMapMode: tonemap.ModeHardware})
	card.ClientName = "  Findroid  "
	card.ClientVersion = "  0.15  "
	card.ClientBuild = "  20260814\x00  "
	card.ClientChannel = "  beta\t  "
	card.ClientUserAgent = "  Findroid/0.15 (Android)  "

	session := tm.ReconstructSession(clientip.SetContext(t.Context(), "203.0.113.20"), "sess-jf", 42, card)
	if session == nil {
		t.Fatal("reconstruct returned nil")
	}
	if session.ClientIP != "203.0.113.20" {
		t.Fatalf("ClientIP = %q, want the rebuilding request's 203.0.113.20", session.ClientIP)
	}
	if session.ClientName != "Findroid" || session.ClientVersion != "0.15" || session.ClientBuild != "20260814" || session.ClientChannel != "beta" || session.ClientUserAgent != "Findroid/0.15 (Android)" {
		t.Fatalf("client metadata not restored: %+v", session)
	}
	if !session.TranscodeAudio {
		t.Fatal("TranscodeAudio must be restored from the card (aac default re-encodes)")
	}
	if session.TranscodeHWAccel != "qsv" || session.ToneMapMode != tonemap.ModeHardware {
		t.Fatalf("execution facts not restored: hw=%q tone_map=%q", session.TranscodeHWAccel, session.ToneMapMode)
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

func TestReconstructSessionRestoresSourceAudioChannels(t *testing.T) {
	tm := NewTranscodeManager()
	tm.Sessions = NewSessionManager(0, 0)
	card := NewRecipeCard(42, "profile-1", 77, "", TranscodeOpts{
		SessionID: "source-audio", InputPath: "/media/movie.mkv",
		TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2,
	})
	card.RoutingWorkload = "remux"
	card.RoutingExecution = "proxy"
	card.RoutingEgress = "proxy"
	card.RoutingEgressNodeID = 11

	claims := card.ToClaims()
	if claims.SourceAudioChannels != 6 || claims.AudioChannels != 0 {
		t.Fatalf("source audio claim = %d, legacy ambiguous claim = %d", claims.SourceAudioChannels, claims.AudioChannels)
	}
	reconstructed := tm.ReconstructSession(t.Context(), card.SessionID, card.UserID, RecipeCardFromClaims(&claims))
	if reconstructed == nil || reconstructed.SourceAudioChannels != 6 ||
		reconstructed.RoutingWorkload != card.RoutingWorkload ||
		reconstructed.RoutingExecution != card.RoutingExecution ||
		reconstructed.RoutingEgress != card.RoutingEgress ||
		reconstructed.RoutingEgressNodeID != card.RoutingEgressNodeID {
		t.Fatalf("reconstructed session = %#v, want source channels and route assignment restored", reconstructed)
	}

	legacy := RecipeCardFromClaims(&streamtoken.Claims{SessionID: "legacy", UserID: 42, MediaFileID: 77})
	if legacy.SourceAudioChannels != 0 {
		t.Fatalf("legacy token source audio channels = %d, want unknown", legacy.SourceAudioChannels)
	}
}

func TestRecipeCardAudioV2DiscriminatorRequiresExactAACStereoDownmix(t *testing.T) {
	tests := []struct {
		name               string
		card               RecipeCard
		wantMethod         string
		wantSourceChannels int
		wantTargetChannels int
	}{
		{
			name:       "explicit stereo downmix",
			card:       RecipeCard{PlayMethod: PlayTranscode, TranscodeAudio: true, TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2},
			wantMethod: streamtoken.PlayMethodAudioDownmixTranscode, wantSourceChannels: 6, wantTargetChannels: 2,
		},
		{
			name:       "default stereo downmix",
			card:       RecipeCard{PlayMethod: PlayRemux, TranscodeAudio: true, TargetCodecAudio: "aac", SourceAudioChannels: 6},
			wantMethod: streamtoken.PlayMethodAudioDownmixRemux, wantSourceChannels: 6, wantTargetChannels: 2,
		},
		{
			name:       "stereo source",
			card:       RecipeCard{PlayMethod: PlayRemux, TranscodeAudio: true, TargetCodecAudio: "aac", SourceAudioChannels: 2, TargetAudioChannels: 2},
			wantMethod: string(PlayRemux), wantTargetChannels: 2,
		},
		{
			name:       "copy-only remux",
			card:       RecipeCard{PlayMethod: PlayRemux, TranscodeAudio: false, TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2},
			wantMethod: string(PlayRemux), wantTargetChannels: 2,
		},
		{
			name:       "non AAC output",
			card:       RecipeCard{PlayMethod: PlayTranscode, TranscodeAudio: true, TargetCodecAudio: "eac3", SourceAudioChannels: 6, TargetAudioChannels: 2},
			wantMethod: string(PlayTranscode), wantTargetChannels: 2,
		},
		{
			name:       "surround output",
			card:       RecipeCard{PlayMethod: PlayTranscode, TranscodeAudio: true, TargetCodecAudio: "aac", SourceAudioChannels: 6, TargetAudioChannels: 6},
			wantMethod: string(PlayTranscode), wantTargetChannels: 6,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := test.card.ToClaims()
			if got := RecipeCardFromClaims(&claims).PlayMethod; got != test.card.PlayMethod {
				t.Fatalf("current reader method = %q, want %q", got, test.card.PlayMethod)
			}
			if claims.PlayMethod != test.wantMethod || claims.SourceAudioChannels != test.wantSourceChannels || claims.TargetAudioChannels != test.wantTargetChannels {
				t.Fatalf("claims method/source/target = %q/%d/%d, want %q/%d/%d", claims.PlayMethod, claims.SourceAudioChannels, claims.TargetAudioChannels, test.wantMethod, test.wantSourceChannels, test.wantTargetChannels)
			}
		})
	}
}

// An empty-method token (direct/remux carry only identity) decodes to a usable
// card; a transcode discriminator is restored for a token with no method.
func TestRecipeCardFromClaimsEmptyMethodIsTranscode(t *testing.T) {
	claims := &streamtoken.Claims{SessionID: "x", UserID: 1, MediaFileID: 2}
	if got := RecipeCardFromClaims(claims); got.PlayMethod != PlayTranscode {
		t.Fatalf("empty method should decode as transcode, got %q", got.PlayMethod)
	}
}

func TestToneMapRecipeClaimsUseOldReaderVisibleDiscriminator(t *testing.T) {
	card := RecipeCard{PlayMethod: PlayTranscode, ToneMapMode: tonemap.ModeHardware}

	claims := card.ToClaims()
	if claims.PlayMethod == string(PlayTranscode) || claims.PlayMethod == "" {
		t.Fatalf("tone-map token method = %q, want a method rejected by pre-tone-map readers", claims.PlayMethod)
	}
	if got := RecipeCardFromClaims(&claims).PlayMethod; got != PlayTranscode {
		t.Fatalf("current reader method = %q, want %q", got, PlayTranscode)
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

func TestRecipeExecutorNamesAnotherNode(t *testing.T) {
	oldNode := RecipeExecutor{NodeID: 1, NodeURL: "http://old:9000"}
	for _, tc := range []struct {
		name     string
		executor RecipeExecutor
		card     RecipeCard
		want     bool
	}{
		{"own card", oldNode, RecipeCard{RoutingExecutionNodeID: 1, TranscodeNodeURL: "http://old:9000/"}, false},
		{"moved to another node", oldNode, RecipeCard{RoutingExecutionNodeID: 2, TranscodeNodeURL: "http://new:9000"}, true},
		// Without a row id on the card the move cannot be told from a URL edit.
		{"card without a row id", oldNode, RecipeCard{TranscodeNodeURL: "http://new:9000"}, false},
		{"node URL edited, URL-only card", RecipeExecutor{NodeID: 1, NodeURL: "http://alias:9000"}, RecipeCard{TranscodeNodeURL: "http://old:9000"}, false},
		{"node re-added under a new row", RecipeExecutor{NodeID: 3, NodeURL: "http://old:9000"}, RecipeCard{RoutingExecutionNodeID: 1, TranscodeNodeURL: "http://old:9000"}, false},
		{"node URL repointed on the same row", RecipeExecutor{NodeID: 1, NodeURL: "http://renamed:9000"}, RecipeCard{RoutingExecutionNodeID: 1, TranscodeNodeURL: "http://old:9000"}, false},
		{"node identity unresolved", RecipeExecutor{}, RecipeCard{RoutingExecutionNodeID: 2, TranscodeNodeURL: "http://new:9000"}, false},
		{"node URL unresolved", RecipeExecutor{NodeID: 1}, RecipeCard{RoutingExecutionNodeID: 2, TranscodeNodeURL: "http://new:9000"}, false},
	} {
		if got := tc.executor.NamesAnotherNode(tc.card); got != tc.want {
			t.Errorf("%s: NamesAnotherNode = %v, want %v", tc.name, got, tc.want)
		}
	}
}
