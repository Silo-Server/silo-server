package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type fakeEpisodeAliasLookup struct {
	canonical string
	found     bool
	err       error
	seen      []string
}

func (f *fakeEpisodeAliasLookup) ResolveCanonicalEpisodeIDByAlias(_ context.Context, aliasEpisodeID string) (string, bool, error) {
	f.seen = append(f.seen, aliasEpisodeID)
	return f.canonical, f.found, f.err
}

func TestPlaybackProgressTargetCanonicalizesAliasEpisode(t *testing.T) {
	alias := "episode-layout-2-1"
	canonical := "episode-canonical-1-1"
	lookup := &fakeEpisodeAliasLookup{canonical: canonical, found: true}
	handler := &PlaybackHandler{EpisodeAliasLookup: lookup}
	file := &models.MediaFile{ContentID: "series-1", EpisodeID: alias}

	if got, err := handler.playbackProgressTarget(context.Background(), file); err != nil || got != canonical {
		t.Fatalf("progress target = %q, %v; want canonical episode %q", got, err, canonical)
	}
	if len(lookup.seen) != 1 || lookup.seen[0] != alias {
		t.Fatalf("alias lookup = %v, want [%q]", lookup.seen, alias)
	}
}

func TestPlaybackProgressTargetFallsBackToStoredIdentity(t *testing.T) {
	alias := "episode-layout-2-1"
	handler := &PlaybackHandler{EpisodeAliasLookup: &fakeEpisodeAliasLookup{found: false}}
	file := &models.MediaFile{ContentID: "series-1", EpisodeID: alias}

	if got, err := handler.playbackProgressTarget(context.Background(), file); err != nil || got != alias {
		t.Fatalf("unmapped progress target = %q, %v; want stored episode %q", got, err, alias)
	}
}

func TestPlaybackProgressTargetFailsOnLookupError(t *testing.T) {
	alias := "episode-layout-2-1"
	handler := &PlaybackHandler{EpisodeAliasLookup: &fakeEpisodeAliasLookup{err: errors.New("temporary lookup failure")}}
	file := &models.MediaFile{ContentID: "series-1", EpisodeID: alias}

	if got, err := handler.playbackProgressTarget(context.Background(), file); err == nil || got != "" {
		t.Fatalf("failed progress target = %q, %v; want a retryable error and no alias write", got, err)
	}
}

func TestPlaybackProgressTargetKeepsContentIdentityForMovie(t *testing.T) {
	handler := &PlaybackHandler{EpisodeAliasLookup: &fakeEpisodeAliasLookup{canonical: "wrong", found: true}}
	file := &models.MediaFile{ContentID: "movie-1"}

	if got, err := handler.playbackProgressTarget(context.Background(), file); err != nil || got != "movie-1" {
		t.Fatalf("movie progress target = %q, %v; want movie-1", got, err)
	}
}

func TestPlaybackAliasLookupFailureWritesNoProgress(t *testing.T) {
	store := newPlaybackTestStore(t)
	alias := "episode-layout-2-1"
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "series-1",
		EpisodeID:  alias,
		FilePath:   "/media/series/S02E01.mkv",
		Resolution: "1080p",
		Duration:   1800,
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.EpisodeAliasLookup = &fakeEpisodeAliasLookup{err: errors.New("temporary lookup failure")}
	session := &playback.Session{
		ID:          "alias-lookup-failure",
		UserID:      1,
		ProfileID:   "profile-1",
		MediaFileID: 42,
		Position:    600,
	}

	handler.persistProgress(context.Background(), session, playback.ProgressSampleV3{Position: session.Position, IsPaused: session.IsPaused})
	if progress, err := store.GetProgress(context.Background(), "profile-1", alias); err != nil || progress != nil {
		t.Fatalf("alias progress = %v, %v; want none", progress, err)
	}
	if _, err := handler.persistStopAndHistory(context.Background(), session, ""); err == nil {
		t.Fatal("persistStopAndHistory succeeded despite a failed identity lookup")
	}
	if history, err := store.ListHistory(context.Background(), "profile-1", 10, 0); err != nil || len(history) != 0 {
		t.Fatalf("history = %#v, %v; want none", history, err)
	}
}

func TestPlaybackAliasFileWritesCanonicalProgressAndHistory(t *testing.T) {
	store := newPlaybackTestStore(t)
	alias := "episode-layout-2-1"
	canonical := "episode-canonical-1-1"
	file := &models.MediaFile{
		ID:         42,
		ContentID:  "series-1",
		EpisodeID:  alias,
		FilePath:   "/media/series/S02E01.mkv",
		Resolution: "1080p",
		Duration:   1800,
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.EpisodeAliasLookup = &fakeEpisodeAliasLookup{canonical: canonical, found: true}
	session := &playback.Session{
		ID:          "alias-session",
		UserID:      1,
		ProfileID:   "profile-1",
		MediaFileID: 42,
		Position:    600,
	}

	handler.persistProgress(context.Background(), session, playback.ProgressSampleV3{Position: session.Position, IsPaused: session.IsPaused})
	progress, err := store.GetProgress(context.Background(), "profile-1", canonical)
	if err != nil {
		t.Fatalf("get canonical progress: %v", err)
	}
	if progress == nil || progress.PositionSeconds != 600 {
		t.Fatalf("canonical progress = %v, want 600", progress)
	}
	if aliasProgress, err := store.GetProgress(context.Background(), "profile-1", alias); err != nil || aliasProgress != nil {
		t.Fatalf("alias progress = %v, %v; want nil", aliasProgress, err)
	}

	session.Position = 1790
	if _, err := handler.persistStopAndHistory(context.Background(), session, ""); err != nil {
		t.Fatalf("persist stop and history: %v", err)
	}
	history, err := store.ListHistory(context.Background(), "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 1 || history[0].MediaItemID != canonical {
		t.Fatalf("history = %#v, want one row for %q", history, canonical)
	}
}
