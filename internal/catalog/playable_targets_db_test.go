package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPlayableTargetResolverProfileStateAvailabilityAndAccess(t *testing.T) {
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
	id := func(name string) string { return fmt.Sprintf("play-target-%s-%d", name, suffix) }
	profileA, profileB := id("profile-a"), id("profile-b")
	movie, missingMovie := id("movie"), id("missing-movie")
	series, completedSeries, deniedSeries := id("series"), id("completed"), id("denied")
	season := id("season-1")
	special, episode1, episode2, uhdEpisode := id("special"), id("episode-1"), id("episode-2"), id("episode-4k")
	crossLibraryEpisode := id("cross-library-episode")
	completed1, completed2 := id("completed-1"), id("completed-2")
	deniedEpisode := id("denied-episode")

	var userID, allowedFolderID, deniedFolderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, username, role, enabled)
		VALUES ($1, $1, 'user', TRUE)
		RETURNING id
	`, id("user")+"@example.invalid").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $3, 'A'), ($2, $3, 'B')
	`, profileA, profileB, userID); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('mixed', $1, TRUE) RETURNING id`, id("allowed-folder")).Scan(&allowedFolderID); err != nil {
		t.Fatalf("seed allowed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, TRUE) RETURNING id`, id("denied-folder")).Scan(&deniedFolderID); err != nil {
		t.Fatalf("seed denied folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movie, missingMovie, series, completedSeries, deniedSeries})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{allowedFolderID, deniedFolderID})
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Movie', 'matched', '{}'),
		       ($2, 'movie', 'Missing Movie', 'matched', '{}'),
		       ($3, 'series', 'Series', 'matched', '{}'),
		       ($4, 'series', 'Completed Series', 'matched', '{}'),
		       ($5, 'series', 'Denied Series', 'matched', '{}')
	`, movie, missingMovie, series, completedSeries, deniedSeries); err != nil {
		t.Fatalf("seed media items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO seasons (content_id, series_id, season_number, title)
		VALUES ($1, $2, 1, 'Season 1')
	`, season, series); err != nil {
		t.Fatalf("seed season: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $8, 0, 1, 'Special'),
		       ($2, $8, 1, 1, 'One'),
		       ($3, $8, 1, 2, 'Two'),
		       ($4, $9, 1, 1, 'Completed One'),
		       ($5, $9, 1, 2, 'Completed Two'),
		       ($6, $10, 1, 1, 'Denied'),
		       ($7, $8, 1, 3, 'Unavailable'),
		       ($11, $8, 1, 0, 'Other Library')
	`, special, episode1, episode2, completed1, completed2, deniedEpisode, id("unavailable-episode"), series, completedSeries, deniedSeries, crossLibraryEpisode); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}
	seedFile := func(contentID, episodeID string, folderID int, missing bool) {
		var missingSince *time.Time
		if missing {
			value := time.Now().UTC()
			missingSince = &value
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (content_id, episode_id, media_folder_id, file_path, missing_since)
			VALUES ($1, $2, $3, $4, $5)
		`, nilIfBlank(contentID), nilIfBlank(episodeID), folderID, id("file-"+contentID+episodeID)+".mkv", missingSince); err != nil {
			t.Fatalf("seed file for %s%s: %v", contentID, episodeID, err)
		}
	}
	seedFile(movie, "", allowedFolderID, false)
	if _, err := pool.Exec(ctx, `UPDATE media_files SET resolution = '2160p' WHERE content_id = $1`, movie); err != nil {
		t.Fatalf("mark movie as 4K: %v", err)
	}
	seedFile(missingMovie, "", allowedFolderID, true)
	for _, episodeID := range []string{special, episode1, episode2, completed1, completed2} {
		seedFile("", episodeID, allowedFolderID, false)
	}
	seedFile("", deniedEpisode, deniedFolderID, false)
	seedFile("", crossLibraryEpisode, deniedFolderID, false)
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $2, 1, 4, 'Four Kay Only')
	`, uhdEpisode, series); err != nil {
		t.Fatalf("seed 4K episode: %v", err)
	}
	seedFile("", uhdEpisode, allowedFolderID, false)
	if _, err := pool.Exec(ctx, `UPDATE media_files SET resolution = '2160p' WHERE episode_id = $1`, uhdEpisode); err != nil {
		t.Fatalf("mark episode as 4K: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, $2, $3, 0, 1200, TRUE, $8),
		       ($1, $2, $4, 300, 1200, FALSE, $9),
		       ($1, $2, $5, 0, 1200, TRUE, $8),
		       ($1, $2, $6, 0, 1200, TRUE, $8),
		       ($1, $7, $3, 200, 1200, FALSE, $10),
		       ($1, $2, $11, 500, 1200, FALSE, $12)
	`, userID, profileA, episode1, episode2, completed1, completed2, profileB, base, base.Add(time.Minute), base.Add(2*time.Minute), crossLibraryEpisode, base.Add(3*time.Minute)); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	inputs := []PlayableTargetInput{
		{ContentID: movie, Type: "movie"},
		{ContentID: missingMovie, Type: "movie"},
		{ContentID: episode1, Type: "episode"},
		{ContentID: series, Type: "series"},
		{ContentID: completedSeries, Type: "series"},
		{ContentID: season, Type: "season"},
		{ContentID: deniedSeries, Type: "series"},
	}
	resolver := NewPlayableTargetResolver(pool)
	progressStore, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("create postgres progress store: %v", err)
	}
	targetsA, err := resolver.Resolve(ctx, PlayableTargetQuery{
		UserID: userID, ProfileID: profileA, Items: inputs,
		Access: AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}}, ProgressStore: progressStore,
	})
	if err != nil {
		t.Fatalf("resolve profile A: %v", err)
	}
	// Results are keyed by PlayableTargetInput.Key, not by content ID, so two
	// cards showing the same item can resolve to different targets.
	wantA := map[string]string{
		PlayableTargetInput{ContentID: movie, Type: "movie"}.Key():            movie,
		PlayableTargetInput{ContentID: episode1, Type: "episode"}.Key():       episode1,
		PlayableTargetInput{ContentID: series, Type: "series"}.Key():          episode2,
		PlayableTargetInput{ContentID: completedSeries, Type: "series"}.Key(): completed1,
		PlayableTargetInput{ContentID: season, Type: "season"}.Key():          episode2,
	}
	if !reflect.DeepEqual(targetsA, wantA) {
		t.Fatalf("profile A targets = %#v, want %#v", targetsA, wantA)
	}

	cappedInput := PlayableTargetInput{ContentID: movie, Type: "movie"}
	qualityCapped, err := resolver.Resolve(ctx, PlayableTargetQuery{
		UserID: userID, ProfileID: profileA,
		Items:         []PlayableTargetInput{cappedInput},
		Access:        AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}, MaxPlaybackQuality: "1080p"},
		ProgressStore: progressStore,
	})
	if err != nil || qualityCapped[cappedInput.Key()] != "" {
		t.Fatalf("quality-capped target = %#v, err %v; want no 4K movie target", qualityCapped, err)
	}

	// A card's anchor hint (for example the episode a recently-added event is
	// about) is profile-independent and may come from a shared cache, so it is
	// honored only when it clears the same per-profile file conditions as any
	// other candidate.
	for name, tc := range map[string]struct {
		hint    string
		access  AccessFilter
		want    string
		wantMsg string
	}{
		"available hint wins over progress ranking": {
			hint:   uhdEpisode,
			access: AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}},
			want:   uhdEpisode,
		},
		"hint above the quality ceiling falls back": {
			hint:   uhdEpisode,
			access: AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}, MaxPlaybackQuality: "1080p"},
			want:   episode2,
		},
		"hint outside the card falls back": {
			hint:   deniedEpisode,
			access: AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}},
			want:   episode2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := PlayableTargetInput{ContentID: series, Type: "series", PreferredContentID: tc.hint}
			targets, err := resolver.Resolve(ctx, PlayableTargetQuery{
				UserID: userID, ProfileID: profileA,
				Items:         []PlayableTargetInput{input},
				Access:        tc.access,
				ProgressStore: progressStore,
			})
			if err != nil || targets[input.Key()] != tc.want {
				t.Fatalf("hinted target = %#v, err %v; want %s", targets, err, tc.want)
			}
		})
	}

	// Recently-added TV renders one card per scan-run event, so the same series
	// can appear twice with different anchors in a single page. Each card must
	// keep its own target instead of both collapsing onto the first one.
	t.Run("repeated cards for one series keep separate hints", func(t *testing.T) {
		first := PlayableTargetInput{ContentID: series, Type: "series", PreferredContentID: episode1}
		second := PlayableTargetInput{ContentID: series, Type: "series", PreferredContentID: episode2}
		targets, err := resolver.Resolve(ctx, PlayableTargetQuery{
			UserID: userID, ProfileID: profileA,
			Items:         []PlayableTargetInput{first, second},
			Access:        AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}},
			ProgressStore: progressStore,
		})
		if err != nil {
			t.Fatalf("resolve repeated series cards: %v", err)
		}
		if targets[first.Key()] != episode1 || targets[second.Key()] != episode2 {
			t.Fatalf("repeated series targets = %#v; want %s and %s", targets, episode1, episode2)
		}
	})

	libraryScopedInput := PlayableTargetInput{ContentID: series, Type: "series"}
	libraryScoped, err := resolver.Resolve(ctx, PlayableTargetQuery{
		UserID: userID, ProfileID: profileA,
		Items:         []PlayableTargetInput{libraryScopedInput},
		LibraryIDs:    []int{allowedFolderID},
		ProgressStore: progressStore,
	})
	if err != nil || libraryScoped[libraryScopedInput.Key()] != episode2 {
		t.Fatalf("library-scoped target = %#v, err %v; want in-library episode %s", libraryScoped, err, episode2)
	}

	seriesInput := PlayableTargetInput{ContentID: series, Type: "series"}
	targetsB, err := resolver.Resolve(ctx, PlayableTargetQuery{
		UserID: userID, ProfileID: profileB,
		Items:  []PlayableTargetInput{seriesInput},
		Access: AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}}, ProgressStore: progressStore,
	})
	if err != nil || targetsB[seriesInput.Key()] != episode1 {
		t.Fatalf("profile B target = %#v, err %v; want resumable %s", targetsB, err, episode1)
	}

	untouched, err := resolver.Resolve(ctx, PlayableTargetQuery{
		UserID: userID, ProfileID: id("no-progress"),
		Items:  []PlayableTargetInput{seriesInput},
		Access: AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}}, ProgressStore: progressStore,
	})
	if err != nil || untouched[seriesInput.Key()] != episode1 {
		t.Fatalf("untouched target = %#v, err %v; want regular season before special %s", untouched, err, episode1)
	}

	for name, input := range map[string]PlayableTargetInput{
		"explicit season identity": {ContentID: season, Type: "season", SeriesID: series, SeasonNumber: intPtr(1)},
		"stale explicit hints":     {ContentID: season, Type: "season", SeriesID: "wrong-series", SeasonNumber: intPtr(99)},
	} {
		t.Run(name, func(t *testing.T) {
			targets, err := resolver.Resolve(ctx, PlayableTargetQuery{
				UserID: userID, ProfileID: profileA, Items: []PlayableTargetInput{input},
				Access: AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}}, ProgressStore: progressStore,
			})
			if err != nil || targets[input.Key()] != episode2 {
				t.Fatalf("season target = %#v, err %v; want %s", targets, err, episode2)
			}
		})
	}

	t.Run("sqlite progress backend", func(t *testing.T) {
		db, err := sql.Open("sqlite3", ":memory:")
		if err != nil {
			t.Fatalf("open sqlite user store: %v", err)
		}
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Errorf("close sqlite user store: %v", err)
			}
		})
		if err := userdb.InitSchema(db); err != nil {
			t.Fatalf("initialize sqlite user store: %v", err)
		}
		sqliteStore := userdb.NewSQLiteUserStore(db)
		if err := sqliteStore.CreateProfile(ctx, userstore.Profile{ID: profileA, Name: "A"}); err != nil {
			t.Fatalf("create sqlite profile: %v", err)
		}
		if err := sqliteStore.SetProgressAt(ctx, profileA, episode1, 400, 1200, false, time.Now().UTC()); err != nil {
			t.Fatalf("seed sqlite progress: %v", err)
		}

		targets, err := resolver.Resolve(ctx, PlayableTargetQuery{
			UserID: userID, ProfileID: profileA,
			Items:         []PlayableTargetInput{seriesInput},
			Access:        AccessFilter{AllowedLibraryIDs: []int{allowedFolderID}},
			ProgressStore: sqliteStore,
		})
		if err != nil || targets[seriesInput.Key()] != episode1 {
			t.Fatalf("sqlite-backed target = %#v, err %v; want resumable %s", targets, err, episode1)
		}
	})
}

// Long-running series are the case this project exists for. Candidates must
// stay uncapped: bounding them in season/episode order would hide an
// in-progress episode deep in the run and silently degrade the card to "play
// the first episode". The bind-parameter ceiling is handled by batching the
// progress lookup instead, which costs nothing behaviorally.
func TestPlayableTargetResolverResumesDeepInsideLongSeries(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	const episodeCount = 420
	const inProgressEpisode = 400

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	series := fmt.Sprintf("deep-series-%d", suffix)
	episodeID := func(number int) string { return fmt.Sprintf("deep-episode-%d-%d", suffix, number) }
	profileID := fmt.Sprintf("deep-profile-%d", suffix)

	var userID, folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, username, role, enabled)
		VALUES ($1, $1, 'user', TRUE)
		RETURNING id
	`, fmt.Sprintf("deep-%d@example.invalid", suffix)).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, TRUE) RETURNING id`, series).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, series)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'Deep')`, profileID, userID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'Very Long Runner', 'matched', '{}')
	`, series); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		SELECT format('deep-episode-%s-%s', $1::text, g), $2, 1, g, 'Episode ' || g
		FROM generate_series(1, $3) g
	`, fmt.Sprintf("%d", suffix), series, episodeCount); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (episode_id, media_folder_id, file_path)
		SELECT format('deep-episode-%s-%s', $1::text, g), $2, format('/deep/%s-%s.mkv', $1::text, g)
		FROM generate_series(1, $3) g
	`, fmt.Sprintf("%d", suffix), folderID, episodeCount); err != nil {
		t.Fatalf("seed files: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, $2, $3, 600, 1200, FALSE, NOW())
	`, userID, profileID, episodeID(inProgressEpisode)); err != nil {
		t.Fatalf("seed deep progress: %v", err)
	}

	progressStore, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("create progress store: %v", err)
	}
	input := PlayableTargetInput{ContentID: series, Type: "series"}
	targets, err := NewPlayableTargetResolver(pool).Resolve(ctx, PlayableTargetQuery{
		UserID: userID, ProfileID: profileID,
		Items:         []PlayableTargetInput{input},
		Access:        AccessFilter{AllowedLibraryIDs: []int{folderID}},
		ProgressStore: progressStore,
	})
	if err != nil {
		t.Fatalf("resolve long series: %v", err)
	}
	if got := targets[input.Key()]; got != episodeID(inProgressEpisode) {
		t.Fatalf("long-series target = %q, want the in-progress episode %q", got, episodeID(inProgressEpisode))
	}
}

func nilIfBlank(value string) any {
	if value == "" {
		return nil
	}
	return value
}
