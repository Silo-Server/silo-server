package playback

import (
	"context"
	"encoding/json"
	"testing"
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

func TestPostgresRecipeStoreDisabledIsNoop(t *testing.T) {
	// A nil pool yields a disabled, fully no-op store so callers never need to
	// special-case an unavailable database.
	store := NewPostgresRecipeStore(nil)
	if store.Enabled() {
		t.Fatal("store with nil pool should be disabled")
	}
	ctx := context.Background()
	if err := store.Save(ctx, NewRecipeCard(1, "p", 2, "", TranscodeOpts{SessionID: "x"})); err != nil {
		t.Errorf("disabled Save should be nil, got %v", err)
	}
	if _, found, err := store.Get(ctx, "x"); err != nil || found {
		t.Errorf("disabled Get should return (_, false, nil), got found=%v err=%v", found, err)
	}
	if err := store.Delete(ctx, "x"); err != nil {
		t.Errorf("disabled Delete should be nil, got %v", err)
	}
	if err := store.Refresh(ctx, "x"); err != nil {
		t.Errorf("disabled Refresh should be nil, got %v", err)
	}
	ids, err := store.ActiveSessionIDs(ctx)
	if err != nil || len(ids) != 0 {
		t.Errorf("disabled ActiveSessionIDs should be empty, got %v err=%v", ids, err)
	}
}
