package catalog

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// generalPathProgressStore hides the Postgres store's
// CatalogProgressRelationStore capability so Resolve takes the general path:
// every candidate returned to Go, progress read back through the store.
type generalPathProgressStore struct {
	delegate PlayableTargetProgressStore
}

func (s generalPathProgressStore) ListProgressByMediaItems(ctx context.Context, profileID string, ids []string) (map[string]userstore.WatchProgress, error) {
	return s.delegate.ListProgressByMediaItems(ctx, profileID, ids)
}

// TestPlayableTargetsPostgresMatchesGeneral pins resolvePostgresTargets to the
// general path over a randomized library: specials and negative seasons,
// files that are missing or in denied, disabled, or over-quality folders,
// in-progress/started/completed/hidden progress with same-second ties, hints
// valid and invalid, and season cards that name one or two episode scopes.
func TestPlayableTargetsPostgresMatchesGeneral(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	id := func(format string, args ...any) string {
		return fmt.Sprintf("pgtargets-%d-", suffix) + fmt.Sprintf(format, args...)
	}
	profileID, otherProfileID := id("profile"), id("other-profile")
	t.Logf("seed %d", suffix)

	var userID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, username, role, enabled) VALUES ($1, $1, 'user', TRUE) RETURNING id
	`, id("user")+"@example.invalid").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $3, 'P'), ($2, $3, 'O')`, profileID, otherProfileID, userID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	var allowed, denied, disabled int
	for _, folder := range []struct {
		dst     *int
		enabled bool
	}{{&allowed, true}, {&denied, true}, {&disabled, false}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, $2) RETURNING id`,
			id("folder-%t-%p", folder.enabled, folder.dst), folder.enabled).Scan(folder.dst); err != nil {
			t.Fatalf("seed folder: %v", err)
		}
	}

	rng := rand.New(rand.NewSource(suffix))
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	type episode struct {
		contentID, seriesID string
		season              int
	}
	var seriesIDs, movieIDs []string
	var episodes []episode
	seasonIDs := map[string]map[int]string{}
	for s := range 14 {
		seriesIDs = append(seriesIDs, id("series-%d", s))
	}
	for m := range 4 {
		movieIDs = append(movieIDs, id("movie-%d", m))
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE file_path LIKE $1`, id("")+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = ANY($1)`, seriesIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM seasons WHERE series_id = ANY($1)`, seriesIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1) OR content_id = ANY($2)`, seriesIDs, movieIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{allowed, denied, disabled})
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres) SELECT unnest($1::text[]), 'series', 'S', 'matched', '{}'`, seriesIDs); err != nil {
		t.Fatalf("seed series items: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres) SELECT unnest($1::text[]), 'movie', 'M', 'matched', '{}'`, movieIDs); err != nil {
		t.Fatalf("seed movie items: %v", err)
	}
	for s, seriesID := range seriesIDs {
		seasonIDs[seriesID] = map[int]string{}
		seasons := []int{0, 1, 2, 3}
		if s%5 == 0 {
			seasons = append(seasons, -1)
		}
		for _, season := range seasons {
			seasonID := id("series-%d-season-%d", s, season)
			seasonIDs[seriesID][season] = seasonID
			if _, err := pool.Exec(ctx, `INSERT INTO seasons (content_id, series_id, season_number, title) VALUES ($1, $2, $3, 'S')`,
				seasonID, seriesID, season); err != nil {
				t.Fatalf("seed season: %v", err)
			}
			for e := range 1 + rng.Intn(6) {
				e++
				episodes = append(episodes, episode{id("series-%d-s%de%d", s, season, e), seriesID, season})
				if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, $3, $4, 'E')`,
					episodes[len(episodes)-1].contentID, seriesID, season, e); err != nil {
					t.Fatalf("seed episode: %v", err)
				}
			}
		}
	}
	seedFile := func(contentID, episodeID string) {
		folder, missing, resolution := allowed, false, "1080p"
		switch rng.Intn(8) {
		case 0:
			return // no file at all
		case 1:
			folder = denied
		case 2:
			folder = disabled
		case 3:
			missing = true
		case 4:
			resolution = "2160p"
		}
		var missingSince *time.Time
		if missing {
			value := base
			missingSince = &value
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (content_id, episode_id, media_folder_id, file_path, resolution, missing_since)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, nilIfBlank(contentID), nilIfBlank(episodeID), folder, id("file-%d", rng.Int63()), resolution, missingSince); err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}
	for _, movie := range movieIDs {
		seedFile(movie, "")
	}
	for _, ep := range episodes {
		seedFile(ep.seriesID, ep.contentID)
		if rng.Intn(4) == 0 {
			seedFile(ep.seriesID, ep.contentID) // a second version
		}
		// Progress times fall on a handful of seconds with sub-second offsets,
		// so same-second ties between in-progress episodes are common.
		updated := base.Add(time.Duration(rng.Intn(4))*time.Second + time.Duration(rng.Intn(1000))*time.Millisecond)
		var position float64
		completed := false
		switch rng.Intn(5) {
		case 0:
			continue
		case 1:
			position = 300 // in progress
		case 2:
			position = 0 // opened, not started
		default:
			completed = true
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
			VALUES ($1, $2, $3, $4, 1200, $5, $6)
		`, userID, profileID, ep.contentID, position, completed, updated); err != nil {
			t.Fatalf("seed progress: %v", err)
		}
		// Another profile on the same account finished this episode; its rows
		// must never influence this profile's ranking.
		if rng.Intn(2) == 0 {
			if _, err := pool.Exec(ctx, `
				INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
				VALUES ($1, $2, $3, 600, 1200, $4, $5)
			`, userID, otherProfileID, ep.contentID, rng.Intn(2) == 0, base.Add(time.Hour)); err != nil {
				t.Fatalf("seed other profile progress: %v", err)
			}
		}
		if rng.Intn(6) == 0 {
			hiddenBefore := updated.Add(time.Duration(rng.Intn(3)-1) * time.Second) // hides the row two times in three
			if _, err := pool.Exec(ctx, `
				INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before) VALUES ($1, $2, $3, $4)
			`, userID, profileID, ep.contentID, hiddenBefore); err != nil {
				t.Fatalf("seed hidden item: %v", err)
			}
		}
	}

	episodeSeasons := make(map[string]int, len(episodes))
	for _, ep := range episodes {
		episodeSeasons[ep.contentID] = ep.season
	}
	var inputs []PlayableTargetInput
	for _, movie := range movieIDs {
		inputs = append(inputs, PlayableTargetInput{ContentID: movie, Type: "movie"}, PlayableTargetInput{ContentID: movie, Type: "movie", PreferredContentID: movie})
	}
	for i, ep := range episodes {
		if i%3 == 0 {
			inputs = append(inputs, PlayableTargetInput{ContentID: ep.contentID, Type: "episode"})
		}
	}
	for _, seriesID := range seriesIDs {
		own := episodes[rng.Intn(len(episodes))]
		for own.seriesID != seriesID {
			own = episodes[rng.Intn(len(episodes))]
		}
		inputs = append(inputs,
			PlayableTargetInput{ContentID: seriesID, Type: "series"},
			PlayableTargetInput{ContentID: seriesID, Type: "series", PreferredContentID: own.contentID},
			PlayableTargetInput{ContentID: seriesID, Type: "series", PreferredContentID: episodes[rng.Intn(len(episodes))].contentID},
		)
		for season, seasonID := range seasonIDs[seriesID] {
			other := (season + 1) % 4
			inputs = append(inputs,
				// Fields and seasons row agree: one scope.
				PlayableTargetInput{ContentID: seasonID, Type: "season", SeriesID: seriesID, SeasonNumber: intPtr(season)},
				// Seasons row only.
				PlayableTargetInput{ContentID: seasonID, Type: "season"},
				// Fields name a different season than the row: two scopes.
				PlayableTargetInput{ContentID: seasonID, Type: "season", SeriesID: seriesID, SeasonNumber: intPtr(other)},
				PlayableTargetInput{ContentID: seasonID, Type: "season", SeriesID: seriesID, SeasonNumber: intPtr(season), PreferredContentID: own.contentID},
				PlayableTargetInput{ContentID: seasonID, Type: "season", SeriesID: seriesID, SeasonNumber: intPtr(season), PreferredContentID: episodes[rng.Intn(len(episodes))].contentID},
				// Fields name another series entirely: two scopes across series.
				PlayableTargetInput{ContentID: seasonID, Type: "season", SeriesID: seriesIDs[rng.Intn(len(seriesIDs))], SeasonNumber: intPtr(1 + rng.Intn(3))},
			)
		}
	}

	sqlPath, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("create progress store: %v", err)
	}
	relationStore, ok := sqlPath.(userstore.CatalogProgressRelationStore)
	if !ok {
		t.Fatal("the Postgres store must offer a catalog progress relation")
	}
	if _, _, ok := relationStore.CatalogProgressRelation(pool, userID, profileID, 6); !ok {
		t.Fatal("the Postgres store must take the SQL path for the resolver's pool")
	}
	resolver := NewPlayableTargetResolver(pool)
	for _, scope := range []struct {
		access     AccessFilter
		libraryIDs []int
	}{
		{access: AccessFilter{AllowedLibraryIDs: []int{allowed}}},
		{access: AccessFilter{}},
		{access: AccessFilter{DisabledLibraryIDs: []int{denied}}},
		{access: AccessFilter{AllowedLibraryIDs: []int{allowed, disabled}, MaxPlaybackQuality: "1080p"}},
		// A library-scoped surface: the effective-libraries branch.
		{access: AccessFilter{AllowedLibraryIDs: []int{allowed, denied}}, libraryIDs: []int{allowed}},
		{access: AccessFilter{DisabledLibraryIDs: []int{denied}}, libraryIDs: []int{denied, allowed}},
	} {
		access := scope.access
		query := PlayableTargetQuery{UserID: userID, ProfileID: profileID, LibraryIDs: scope.libraryIDs, Items: inputs, Access: access, ProgressStore: sqlPath}
		got, err := resolver.ResolveTargets(ctx, query)
		if err != nil {
			t.Fatalf("SQL-path resolve: %v", err)
		}
		query.ProgressStore = generalPathProgressStore{delegate: sqlPath}
		want, err := resolver.ResolveTargets(ctx, query)
		if err != nil {
			t.Fatalf("general resolve: %v", err)
		}
		if len(want) == 0 {
			t.Fatal("fixture resolved no targets; the comparison would be vacuous")
		}
		for _, input := range inputs {
			if !reflect.DeepEqual(got[input.Key()], want[input.Key()]) {
				t.Errorf("access %+v libraries %v, card %+v: SQL path %+v, general %+v", access, scope.libraryIDs, input, got[input.Key()], want[input.Key()])
			}
			// Both paths must report the season the target was seeded in,
			// specials and negative seasons included.
			if target, ok := got[input.Key()]; ok {
				season, isEpisode := episodeSeasons[target.ContentID]
				if isEpisode != (target.SeasonNumber != nil) || (isEpisode && *target.SeasonNumber != season) {
					t.Errorf("card %+v: target %s season %v, seeded in season %d (episode %v)", input, target.ContentID, target.SeasonNumber, season, isEpisode)
				}
			}
		}
		if len(got) != len(want) {
			t.Errorf("access %+v: SQL path resolved %d cards, general %d", access, len(got), len(want))
		}
	}
}
