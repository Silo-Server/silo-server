package playback

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestRecipeCardRoundTripOpts(t *testing.T) {
	opts := TranscodeOpts{
		InputPath:          "/media/movie.mkv",
		OutputDir:          "/tmp/silo-transcode/abc",
		SessionID:          "abc",
		SourceVideoCodec:   "hevc",
		SeekSeconds:        900,
		TargetResolution:   "1080p",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		StartSegmentNumber: 450,
		HWAccel:            "qsv",
		HWDevice:           "/dev/dri/renderD128",
		SubtitleTrackIndex: 3,
		SubtitleBurnIn:     true,
		SubtitleCodec:      "hdmv_pgs_subtitle",
		AudioTrackIndex:    1,
		TargetBitrateKbps:  8000,
		TotalDuration:      7200,
		FastStart:          true,
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
		InputPath:          "/media/movie.mkv",
		SessionID:          "abc",
		SourceVideoCodec:   "hevc",
		SeekSeconds:        900,
		TargetResolution:   "1080p",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		StartSegmentNumber: 450,
		SubtitleTrackIndex: 3,
		SubtitleBurnIn:     true,
		SubtitleCodec:      "hdmv_pgs_subtitle",
		AudioTrackIndex:    1,
		TargetBitrateKbps:  8000,
		TotalDuration:      7200,
		FastStart:          true,
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
