package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type compatDetailFiles []*models.MediaFile

func (f compatDetailFiles) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return f, nil
}

func (f compatDetailFiles) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}

type compatDetailUserStores struct{ store userstore.UserStore }

func (p compatDetailUserStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (compatDetailUserStores) Close() error { return nil }

// The Jellyfin listener builds its own detail service; a movie detail read
// through it must carry the viewer's stored subtitle defaults, which the
// Jellyfin item and PlaybackInfo responses select the default subtitle from.
func TestCompatDetailServiceResolvesViewerSubtitleDefaultsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	contentID := fmt.Sprintf("movie-compat-detail-%d", time.Now().UnixNano())
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_items (content_id, type, title, genres) VALUES ($1, 'movie', $1, '{}'::text[])`, contentID); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		settingskeys.PlaybackSubtitleMode:     "always",
		settingskeys.PlaybackSubtitleLanguage: "en",
	} {
		encoded, _ := json.Marshal(value)
		if _, err := store.UpsertSettingValue(t.Context(), userstore.SettingIdentity{Key: key, Scope: settingscontract.ScopeProfile, ProfileID: "profile-1"}, encoded); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	files := compatDetailFiles{{
		ID:             1,
		ContentID:      contentID,
		FilePath:       "/media/movie.mkv",
		SubtitleTracks: []models.SubtitleTrack{{Index: 2, Language: "en", Codec: "subrip"}},
	}}
	svc := newCompatDetailService(&api.Dependencies{DB: pool},
		catalog.NewItemRepository(pool), catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool), catalog.NewPersonRepository(pool),
		files, compatDetailUserStores{store: store})

	detail, err := svc.GetItemDetail(t.Context(), contentID, catalog.AccessFilter{UserID: 1, ProfileID: "profile-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !detail.HasEffectiveSubtitleMode || detail.EffectiveSubtitleMode != "always" || detail.EffectiveSubtitleLanguage != "en" {
		t.Fatalf("subtitle defaults = mode %q (set %t), language %q; want always, en",
			detail.EffectiveSubtitleMode, detail.HasEffectiveSubtitleMode, detail.EffectiveSubtitleLanguage)
	}
}
