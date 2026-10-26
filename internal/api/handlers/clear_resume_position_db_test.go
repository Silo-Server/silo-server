package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// TestClearResumePositionTargetsDB pins which catalog items have a resume
// position to clear (issue #2134): a movie or an episode clears and publishes
// one progress change; a season, series or ebook is an invalid target; an
// item without a resume position is a silent no-op.
func TestClearResumePositionTargetsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("resume-reset-%d", time.Now().UnixNano())
	movie, series, season, episode, ebook := prefix+"-movie", prefix+"-series", prefix+"-s1", prefix+"-s1e1", prefix+"-ebook"
	libraries := map[string]int{}
	for _, kind := range []string{"movies", "tv", "ebooks"} {
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES($1,$2) RETURNING id`, kind, prefix+"-"+kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		libraries[kind] = id
	}
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM user_watch_history WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM episodes WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(bg, `DELETE FROM seasons WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		for _, id := range libraries {
			_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id = $1`, id)
		}
	})
	for _, item := range []struct{ id, kind, library string }{{movie, "movie", "movies"}, {series, "series", "tv"}, {ebook, "ebook", "ebooks"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,genres,default_metadata_language) VALUES($1,$2,'Synthetic','{}','en')`, item.id, item.kind); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, item.id, libraries[item.library]); err != nil {
			t.Fatal(err)
		}
	}
	seasons, episodes := catalog.NewSeasonRepository(pool), catalog.NewEpisodeRepository(pool)
	if err := seasons.Upsert(ctx, &models.Season{ContentID: season, SeriesID: series, SeasonNumber: 1, Title: "Season 1", DefaultMetadataLanguage: "en"}); err != nil {
		t.Fatal(err)
	}
	if err := episodes.Upsert(ctx, &models.Episode{ContentID: episode, SeriesID: series, SeasonID: season, SeasonNumber: 1, EpisodeNumber: 1, Title: "Episode", DefaultMetadataLanguage: "en"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)`, episode, libraries["tv"]); err != nil {
		t.Fatal(err)
	}

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	const profileID = "resume-reset"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Resume Reset"}); err != nil {
		t.Fatal(err)
	}
	hub := evt.NewHub("test", &cache.NoopEventBus{})
	received, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)
	h := &ItemsHandler{
		itemRepo:    catalog.NewItemRepository(pool),
		seasonRepo:  seasons,
		episodeRepo: episodes,
		watchState:  watchstate.NewService(provider),
		EventsHub:   hub,
	}
	for _, id := range []string{movie, episode} {
		if _, err := h.watchState.RecordPlaybackStop(ctx, userID, profileID, id, 3600, 1200, time.Now().UTC().Add(-time.Hour), userstore.VersionHints{}, userstore.ProgressThresholds{}); err != nil {
			t.Fatal(err)
		}
	}

	for _, id := range []string{movie, episode} {
		if err := h.ClearResumePosition(ctx, userID, profileID, id, catalog.AccessFilter{}); err != nil {
			t.Fatalf("clear %s: %v", id, err)
		}
		progress, err := store.GetProgress(ctx, profileID, id)
		if err != nil || progress == nil || progress.PositionSeconds != 0 {
			t.Fatalf("%s progress = %+v, %v; want position 0", id, progress, err)
		}
		select {
		case envelope := <-received:
			var payload userStateEventPayload
			if err := json.Unmarshal(envelope.Data, &payload); err != nil {
				t.Fatal(err)
			}
			if envelope.Channel != evt.ChannelUserState || payload.Change != "progress" || payload.ContentID != id || payload.ProfileID != profileID {
				t.Fatalf("event for %s = %+v %+v, want one progress change", id, envelope, payload)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no user state event for %s", id)
		}
	}

	for _, id := range []string{series, season, ebook} {
		var apiErr *APIError
		if err := h.ClearResumePosition(ctx, userID, profileID, id, catalog.AccessFilter{}); !errors.As(err, &apiErr) || apiErr.Code != "invalid_watch_target" || apiErr.Status != http.StatusBadRequest {
			t.Fatalf("clear %s = %v, want invalid_watch_target", id, err)
		}
	}
	var apiErr *APIError
	if err := h.ClearResumePosition(ctx, userID, profileID, prefix+"-missing", catalog.AccessFilter{}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("clear missing = %v, want 404", err)
	}

	// Mark watched shares the lookup and still expands a season or series
	// into its episodes; the reset above rejected them before that.
	for _, want := range []struct{ id, kind string }{{movie, "movie"}, {series, "series"}, {season, "season"}, {episode, "episode"}} {
		kind, targets, err := h.resolveWatchedTargets(ctx, want.id, true, catalog.AccessFilter{})
		leaf := episode
		if want.id == movie {
			leaf = movie
		}
		if err != nil || kind != want.kind || len(targets) != 1 || targets[0].ContentID != leaf {
			t.Fatalf("resolveWatchedTargets(%s) = %q, %+v, %v; want %s with leaf %s", want.id, kind, targets, err, want.kind, leaf)
		}
	}

	// Nothing left to clear: no write and no event.
	if err := h.ClearResumePosition(ctx, userID, profileID, movie, catalog.AccessFilter{}); err != nil {
		t.Fatal(err)
	}
	select {
	case envelope := <-received:
		t.Fatalf("a no-op clear published %+v", envelope)
	default:
	}
}
