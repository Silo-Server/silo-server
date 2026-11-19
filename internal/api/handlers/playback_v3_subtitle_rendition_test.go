package handlers

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A lookup held up before it can see the deadline, as a stat on a stalled
// mount is, still answers none by the deadline, so planning keeps the
// pipeline rendition instead of waiting on it.
func TestCueIndexReadsAnswerByTheDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	reads := newCueIndexReadsV3()
	reads.timeout = 20 * time.Millisecond
	reads.lookup = func(context.Context, string) (playback.CueIndexedSubtitles, error) {
		<-release
		return playback.CueIndexedSubtitles{}, nil
	}
	ctx := context.WithValue(t.Context(), cueIndexReadsKeyV3{}, reads)
	answered := make(chan *playback.CueIndexedSubtitles, 1)
	go func() {
		answered <- cueIndexedSubtitleTracksV3(ctx, &models.MediaFile{ID: 7, FilePath: "/media/stalled.mkv"})()
	}()
	select {
	case got := <-answered:
		if got != nil {
			t.Fatalf("stalled lookup answered %+v, want none", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("planning waited on a stalled lookup past its deadline")
	}
}

func TestRehomeRestoresOnDemandRenditionNamesFromTheRecord(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	source := playback.SubtitleRenditionSource{Size: 4096, ModTimeUnixNano: 1_800_000_000_000_000_000}
	frozen := []playback.SubtitleRenditionTrack{
		{StreamIndex: 2, TrackNumber: 3, Name: "English", Language: "eng"},
		{StreamIndex: 3, TrackNumber: 4, Name: "English (2)", Language: "eng", Forced: true, Default: true},
	}
	if err := handler.PlanStoreV3.SaveAttempt(t.Context(), playback.AttemptRecordV3{
		PlaybackAttemptID: "attempt-rehome", SessionID: "session-rehome", ExpiresAt: time.Now().Add(time.Hour),
		FrozenRecipe: playback.ExecutableRecipeV3{SubtitleRendition: true, SubtitleRenditionTracks: frozen, SubtitleRenditionSource: source},
	}); err != nil {
		t.Fatal(err)
	}
	session := &playback.Session{ID: "session-rehome", UserID: 7}
	tokenCard := func(tracks []playback.SubtitleRenditionTrack) *playback.RecipeCard {
		opts := playback.TranscodeOpts{SessionID: session.ID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", SegmentDuration: 2,
			SubtitleRendition: true, SubtitleTrackIndex: -1, SubtitleRenditionTracks: tracks, SubtitleRenditionSource: source}
		claims := playback.NewRecipeCard(session.UserID, "profile", 42, "", opts).ToClaims()
		card := playback.RecipeCardFromClaims(&claims)
		return &card
	}
	card, stored := handler.rehomeRecipeCardV3(t.Context(), session, session.ID, tokenCard(frozen))
	if card == nil || stored || !slices.Equal(card.SubtitleRenditionTracks, frozen) {
		t.Fatalf("rehomed renditions = %+v", card)
	}
	other := []playback.SubtitleRenditionTrack{{TrackNumber: 3}, {TrackNumber: 5}}
	card, _ = handler.rehomeRecipeCardV3(t.Context(), session, session.ID, tokenCard(other))
	if card == nil || card.SubtitleRenditionTracks[0].Name != "" || card.SubtitleRenditionTracks[1].TrackNumber != 5 {
		t.Fatalf("renditions of another plan = %+v", card)
	}
}
