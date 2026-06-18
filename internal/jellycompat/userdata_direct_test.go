package jellycompat

import (
	"context"
	"database/sql"
	"net/url"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type compatTestUserStoreProvider struct {
	store userstore.UserStore
}

func (p compatTestUserStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p compatTestUserStoreProvider) Close() error {
	return nil
}

func TestDirectUserDataServiceProgressUsesCompletedHistory(t *testing.T) {
	store := newJellycompatUserStore(t)
	addCompletedHistoryForJellycompatTest(t, store, "movie-history-only")
	service := &directUserDataService{storeProvider: compatTestUserStoreProvider{store: store}}
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}

	progress, err := service.GetProgress(context.Background(), session, "movie-history-only")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if progress == nil || !progress.Completed {
		t.Fatalf("GetProgress = %+v, want synthetic completed progress", progress)
	}

	progressMap, err := service.ListProgressByMediaItems(context.Background(), session, []string{"movie-history-only"})
	if err != nil {
		t.Fatalf("ListProgressByMediaItems: %v", err)
	}
	if progressMap["movie-history-only"] == nil || !progressMap["movie-history-only"].Completed {
		t.Fatalf("ListProgressByMediaItems = %+v, want completed history overlay", progressMap)
	}
}

func TestBrowseItemsPlayedFilterUsesCompletedHistory(t *testing.T) {
	store := newJellycompatUserStore(t)
	addCompletedHistoryForJellycompatTest(t, store, "movie-history-only")
	browse := &stubBrowseSource{
		items: []*models.MediaItem{
			{ContentID: "movie-history-only", Type: "movie", Title: "Imported"},
			{ContentID: "movie-unplayed", Type: "movie", Title: "Unplayed"},
		},
		total: 2,
	}
	service := newDirectContentServiceForTest(browse, compatTestUserStoreProvider{store: store})
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}

	playedParams := url.Values{}
	playedParams.Set("is_played", "true")
	played, err := service.BrowseItems(context.Background(), session, playedParams)
	if err != nil {
		t.Fatalf("BrowseItems played: %v", err)
	}
	if len(played.Items) != 1 || played.Items[0].ContentID != "movie-history-only" {
		t.Fatalf("played filter items = %+v, want history-only movie", played.Items)
	}

	unplayedParams := url.Values{}
	unplayedParams.Set("is_played", "false")
	unplayed, err := service.BrowseItems(context.Background(), session, unplayedParams)
	if err != nil {
		t.Fatalf("BrowseItems unplayed: %v", err)
	}
	if len(unplayed.Items) != 1 || unplayed.Items[0].ContentID != "movie-unplayed" {
		t.Fatalf("unplayed filter items = %+v, want only unplayed movie", unplayed.Items)
	}
}

func newJellycompatUserStore(t *testing.T) userstore.UserStore {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Profile"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	return store
}

func addCompletedHistoryForJellycompatTest(t *testing.T, store userstore.UserStore, mediaItemID string) {
	t.Helper()
	if err := store.AddHistory(context.Background(), userstore.WatchHistoryEntry{
		ProfileID:       "profile-1",
		MediaItemID:     mediaItemID,
		WatchedAt:       "2026-05-04T12:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourceTrakt,
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}
}
