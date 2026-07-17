package playback

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestRecipeCardRoundTripOpts(t *testing.T) {
	opts := TranscodeOpts{
		InputPath:            "/media/movie.mkv",
		OutputDir:            "/tmp/silo-transcode/abc",
		SessionID:            "abc",
		SourceVideoCodec:     "hevc",
		VideoBitstreamFilter: "dovi_rpu=strip=1",
		SeekSeconds:          900,
		TargetResolution:     "1080p",
		TargetCodecVideo:     "h264",
		TargetCodecAudio:     "aac",
		SegmentDuration:      2,
		StartSegmentNumber:   450,
		HWAccel:              "qsv",
		HWDevice:             "/dev/dri/renderD128",
		SubtitleTrackIndex:   3,
		SubtitleBurnIn:       true,
		SubtitleCodec:        "hdmv_pgs_subtitle",
		AudioTrackIndex:      1,
		TargetBitrateKbps:    8000,
		TotalDuration:        7200,
		FastStart:            true,
	}

	card := NewRecipeCard(42, "profile-1", 77, "", opts)
	if card.SessionID != "abc" || card.UserID != 42 || card.ProfileID != "profile-1" || card.MediaFileID != 77 {
		t.Fatalf("identity not captured: %+v", card)
	}

	// Rebuild opts; environment-specific fields are re-supplied by the caller.
	got := card.TranscodeOpts("/tmp/silo-transcode/abc", "/usr/bin/ffmpeg", nil)
	if got.StartSegmentNumber != 450 {
		t.Errorf("StartSegmentNumber = %d, want 450", got.StartSegmentNumber)
	}
	if got.SeekSeconds != 900 {
		t.Errorf("SeekSeconds = %v, want 900", got.SeekSeconds)
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
	if got.VideoBitstreamFilter != "dovi_rpu=strip=1" {
		t.Errorf("VideoBitstreamFilter = %q", got.VideoBitstreamFilter)
	}
	if got.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("FFmpegPath not re-supplied: %q", got.FFmpegPath)
	}
}

func TestRecipeCardPlayMethodConstructors(t *testing.T) {
	if c := NewRecipeCard(1, "p", 2, "", TranscodeOpts{SessionID: "t"}); c.PlayMethod != PlayTranscode {
		t.Errorf("transcode card PlayMethod = %q, want transcode", c.PlayMethod)
	}
	d := NewDirectRecipeCard("d", 1, "p", 2)
	if d.PlayMethod != PlayDirect || d.SessionID != "d" || d.MediaFileID != 2 {
		t.Errorf("direct card wrong: %+v", d)
	}
	r := NewRemuxRecipeCard("r", 1, "p", 2, true, 3)
	if r.PlayMethod != PlayRemux || !r.TranscodeAudio || r.AudioTrackIndex != 3 {
		t.Errorf("remux card wrong: %+v", r)
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
	card.ClientUserAgent = "Findroid/0.15"

	encoded, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	var back RecipeCard
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	if back.ClientName != "Findroid" || back.ClientVersion != "0.15" || back.ClientUserAgent != "Findroid/0.15" {
		t.Fatalf("client metadata lost in stored-card round trip: %+v", back)
	}

	claims := card.ToClaims()
	fromClaims := RecipeCardFromClaims(&claims)
	if fromClaims.ClientName != "" || fromClaims.ClientUserAgent != "" {
		t.Fatalf("client metadata must not travel via token claims: %+v", fromClaims)
	}
}

// A session rebuilt from a card keeps the client identity for its lifetime,
// so the admin client label and Jellyfin pill survive a server restart.
func TestReconstructSessionRestoresClientMetadata(t *testing.T) {
	tm := NewTranscodeManager()
	tm.Sessions = NewSessionManager(0, 0)

	card := NewRecipeCard(42, "profile-1", 77, "", TranscodeOpts{SessionID: "sess-jf", InputPath: "/media/movie.mkv"})
	card.ClientName = "Findroid"
	card.ClientVersion = "0.15"
	card.ClientUserAgent = "Findroid/0.15 (Android)"

	session := tm.ReconstructSession(t.Context(), "sess-jf", 42, card)
	if session == nil {
		t.Fatal("reconstruct returned nil")
	}
	if session.ClientName != "Findroid" || session.ClientVersion != "0.15" || session.ClientUserAgent != "Findroid/0.15 (Android)" {
		t.Fatalf("client metadata not restored: %+v", session)
	}
	if !session.TranscodeAudio {
		t.Fatal("TranscodeAudio must be restored from the card (aac default re-encodes)")
	}
}

// SetTranscodeStreamDetails records the running transcode's encode decisions
// on the live session so sync rows classify by actual work (video copy =
// repackage) rather than the transport method.
func TestSetTranscodeStreamDetails(t *testing.T) {
	m := NewSessionManager(0, 0)
	m.RegisterReconstructed(&Session{ID: "sess-1", UserID: 7, PlayMethod: PlayTranscode})

	if err := m.SetTranscodeStreamDetails("sess-1", "copy", "aac", true); err != nil {
		t.Fatalf("SetTranscodeStreamDetails: %v", err)
	}
	s, err := m.GetSession("sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.TargetVideoCodec != "copy" || s.TargetAudioCodec != "aac" || !s.TranscodeAudio {
		t.Fatalf("details not recorded: %+v", s)
	}
	if err := m.SetTranscodeStreamDetails("missing", "h264", "aac", true); err == nil {
		t.Fatal("expected ErrSessionNotFound for unknown session")
	}
}

// A card persisted before the play_method discriminator existed must decode with
// an empty PlayMethod so reconstruct can treat it as a transcode (back-compat).
func TestRecipeCardLegacyDecodeHasEmptyPlayMethod(t *testing.T) {
	legacy := []byte(`{"session_id":"old","user_id":7,"media_file_id":9,"segment_duration":2,"start_segment_number":10}`)
	var card RecipeCard
	if err := json.Unmarshal(legacy, &card); err != nil {
		t.Fatalf("decode legacy card: %v", err)
	}
	if card.PlayMethod != "" {
		t.Fatalf("legacy card PlayMethod = %q, want empty (decodes as transcode)", card.PlayMethod)
	}
	if card.SessionID != "old" || card.UserID != 7 || card.StartSegmentNumber != 10 {
		t.Fatalf("legacy fields lost: %+v", card)
	}
}

// A transcode recipe must survive a full round trip through stream-token claims:
// the token IS the durable descriptor under token-carried reconstruction, so any
// dropped byte-affecting field would reconstruct a divergent encode. HWAccel and
// HWDevice are deliberately excluded (re-resolved from live config), so they are
// not asserted here.
func TestRecipeCardClaimsRoundTrip(t *testing.T) {
	card := NewRecipeCard(42, "profile-1", 77, "http://node:9000", TranscodeOpts{
		InputPath:            "/media/movie.mkv",
		SessionID:            "abc",
		SourceVideoCodec:     "hevc",
		VideoBitstreamFilter: "dovi_rpu=strip=1",
		SeekSeconds:          900,
		TargetResolution:     "1080p",
		TargetCodecVideo:     "h264",
		TargetCodecAudio:     "aac",
		SegmentDuration:      2,
		StartSegmentNumber:   450,
		SubtitleTrackIndex:   3,
		SubtitleBurnIn:       true,
		SubtitleCodec:        "hdmv_pgs_subtitle",
		AudioTrackIndex:      1,
		TargetBitrateKbps:    8000,
		TotalDuration:        7200,
		FastStart:            true,
	})

	claims := card.ToClaims()
	got := RecipeCardFromClaims(&claims)

	// Identity + routing.
	if got.SessionID != card.SessionID || got.UserID != card.UserID ||
		got.ProfileID != card.ProfileID || got.MediaFileID != card.MediaFileID ||
		got.TranscodeNodeURL != card.TranscodeNodeURL || got.PlayMethod != card.PlayMethod {
		t.Fatalf("identity/routing lost: %+v", got)
	}
	// Byte-affecting encode parameters.
	if got.InputPath != card.InputPath || got.SourceVideoCodec != card.SourceVideoCodec ||
		got.VideoBitstreamFilter != card.VideoBitstreamFilter ||
		got.SeekSeconds != card.SeekSeconds || got.TargetResolution != card.TargetResolution ||
		got.TargetCodecVideo != card.TargetCodecVideo || got.TargetCodecAudio != card.TargetCodecAudio ||
		got.SegmentDuration != card.SegmentDuration || got.StartSegmentNumber != card.StartSegmentNumber ||
		got.SubtitleTrackIndex != card.SubtitleTrackIndex || got.SubtitleBurnIn != card.SubtitleBurnIn ||
		got.SubtitleCodec != card.SubtitleCodec ||
		got.AudioTrackIndex != card.AudioTrackIndex || got.TargetBitrateKbps != card.TargetBitrateKbps ||
		got.TotalDuration != card.TotalDuration || got.FastStart != card.FastStart {
		t.Fatalf("encode parameters lost in round trip:\n have %+v\n want %+v", got, card)
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
