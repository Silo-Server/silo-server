package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// A special the profile watched must not decide where the regular seasons
// resume: specials carry no airing position, so season 0 sorts before season 1
// and anchoring on one offers the next special instead of the next episode.
// Series that only have specials still step through them in order.
func TestNextUpSkipsSpecialAnchorsPostgres(t *testing.T) {
	ctx := t.Context()
	f := newNextUpSpecialsFixture(t)

	type episode = nextUpTestEpisode
	cases := []struct {
		name     string
		episodes []episode
		watched  []episode // oldest first
		want     string    // the offered episode, "" for no row
	}{
		{
			name:     "special after regular episodes",
			episodes: []episode{{0, 5, false}, {0, 6, false}, {2, 4, false}, {2, 5, false}},
			watched:  []episode{{2, 4, false}, {0, 5, false}},
			want:     "s02e05",
		},
		{
			name:     "special before starting the show",
			episodes: []episode{{0, 1, false}, {0, 2, false}, {1, 1, false}, {1, 2, false}},
			watched:  []episode{{0, 1, false}},
			want:     "s01e01",
		},
		{
			name:     "series with only specials",
			episodes: []episode{{0, 1, false}, {0, 2, false}},
			watched:  []episode{{0, 1, false}},
			want:     "s00e02",
		},
		{
			// Continue Watching carries S02E05; Next Up must not skip past it.
			name:     "unfinished regular episode before a special",
			episodes: []episode{{0, 5, false}, {0, 6, false}, {2, 4, false}, {2, 5, false}, {2, 6, false}},
			watched:  []episode{{2, 4, false}, {2, 5, true}, {0, 5, false}},
			want:     "",
		},
		{
			// An unfinished special still holds the series back, as before.
			name:     "special started after the anchor moved",
			episodes: []episode{{0, 5, false}, {0, 6, false}, {2, 4, false}, {2, 5, false}},
			watched:  []episode{{2, 4, false}, {0, 5, false}, {0, 6, true}},
			want:     "",
		},
		{
			name:     "regular seasons finished",
			episodes: []episode{{0, 1, false}, {0, 2, false}, {1, 1, false}},
			watched:  []episode{{1, 1, false}, {0, 1, false}},
			want:     "",
		},
	}

	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	wantBySeries := make(map[string]string, len(cases))
	for i, tc := range cases {
		series := fmt.Sprintf("%s-series-%d", f.prefix, i)
		f.seedSeries(t, series, tc.episodes, tc.watched, start.Add(time.Duration(i)*time.Hour))
		if tc.want != "" {
			wantBySeries[series] = series + "-" + tc.want
		}
	}

	// Series-scoped and global lookups anchor through different SQL; each
	// must agree. Neither includes resumable rows, as on Home.
	repo := NewNextUpRepository(f.pool, pgstore.NewPostgresProvider(f.pool))
	global, err := repo.ListNextUp(ctx, NextUpQuery{UserID: f.userID, ProfileID: f.profileID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			series := fmt.Sprintf("%s-series-%d", f.prefix, i)
			scoped, err := repo.ListNextUp(ctx, NextUpQuery{UserID: f.userID, ProfileID: f.profileID, SeriesID: series, Limit: 5})
			if err != nil {
				t.Fatal(err)
			}
			var gotScoped, gotGlobal string
			if len(scoped) > 0 {
				gotScoped = scoped[0].ContentID
			}
			for _, res := range global {
				if res.SeriesID == series {
					gotGlobal = res.ContentID
				}
			}
			if gotScoped != wantBySeries[series] || gotGlobal != wantBySeries[series] {
				t.Fatalf("next up = %q (series), %q (global); want %q", gotScoped, gotGlobal, wantBySeries[series])
			}
		})
	}
}

// nextUpTestEpisode is one episode of a seeded series; partial marks a watched
// episode the profile stopped partway through.
type nextUpTestEpisode struct {
	season, number int
	partial        bool
}

// nextUpSpecialsFixture is a series library, an account and one of its
// profiles, removed with everything named after prefix.
type nextUpSpecialsFixture struct {
	pool              *pgxpool.Pool
	prefix, profileID string
	folder, userID    int
}

func newNextUpSpecialsFixture(t *testing.T) nextUpSpecialsFixture {
	t.Helper()
	f := nextUpSpecialsFixture{pool: newTestPool(t), prefix: fmt.Sprintf("nextup-specials-%d", time.Now().UnixNano())}
	ctx := t.Context()
	if err := f.pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, f.prefix).Scan(&f.folder); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, f.prefix).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = f.pool.Exec(bg, `DELETE FROM users WHERE id=$1`, f.userID)
		_, _ = f.pool.Exec(bg, `DELETE FROM media_files WHERE media_folder_id=$1`, f.folder)
		_, _ = f.pool.Exec(bg, `DELETE FROM episodes WHERE series_id LIKE $1`, f.prefix+"%")
		_, _ = f.pool.Exec(bg, `DELETE FROM media_items WHERE content_id LIKE $1`, f.prefix+"%")
		_, _ = f.pool.Exec(bg, `DELETE FROM media_folders WHERE id=$1`, f.folder)
	})
	f.profileID = f.prefix + "-profile"
	f.exec(t, `INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$2,'Specials fixture')`, f.profileID, f.userID)
	return f
}

func (f nextUpSpecialsFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// seedSeries creates a series whose episodes all have a file, and records the
// watched episodes a minute apart from start, oldest first. Episode IDs are
// the series ID followed by -sNNeNN.
func (f nextUpSpecialsFixture) seedSeries(t *testing.T, series string, episodes, watched []nextUpTestEpisode, start time.Time) {
	t.Helper()
	episodeID := func(e nextUpTestEpisode) string { return fmt.Sprintf("%s-s%02de%02d", series, e.season, e.number) }
	f.exec(t, `INSERT INTO media_items (content_id,type,title) VALUES ($1,'series',$1)`, series)
	for _, e := range episodes {
		id := episodeID(e)
		f.exec(t, `INSERT INTO episodes (content_id,series_id,season_number,episode_number,title) VALUES ($1,$2,$3,$4,$1)`, id, series, e.season, e.number)
		f.exec(t, `INSERT INTO media_files (episode_id,media_folder_id,file_path) VALUES ($1,$2,$3)`, id, f.folder, "/"+id)
	}
	for i, e := range watched {
		// A completed write clears the resume point, as pgstore does.
		position := 0
		if e.partial {
			position = 30
		}
		f.exec(t, `INSERT INTO user_watch_progress (user_id,profile_id,media_item_id,position_seconds,completed,updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			f.userID, f.profileID, episodeID(e), position, !e.partial, start.Add(time.Duration(i)*time.Minute))
	}
}

// A viewer with a playback-quality ceiling never meets an episode whose every
// file exceeds it. Next Up waits for a version they can play rather than
// offering it or skipping past it, the Jellyfin season browse and Recently
// Added leave it out, and a multi-episode file within the ceiling still serves
// every episode it spans.
func TestEpisodesAboveQualityCeilingStayHiddenPostgres(t *testing.T) {
	ctx := t.Context()
	f := newNextUpSpecialsFixture(t)
	series, started := f.prefix+"-series", f.prefix+"-started"
	episode := func(n int) string { return fmt.Sprintf("%s-s01e%02d", series, n) }
	startedEpisode := started + "-s01e01"
	other := seedQualityCeilingSeries(t, f, series, started)

	all := []string{episode(1), episode(2), episode(3), episode(4)}
	for _, tc := range []struct {
		name    string
		access  AccessFilter
		nextUp  string
		resumed string // the started series' resumable episode
		browse  []string
		// adjacent is Jellyfin's AdjacentTo window around E1: a hidden E2
		// drops out rather than giving way to E3.
		adjacent []string
		recent   []string // the first library's row, where E2 arrived in 4K
	}{
		{"no ceiling", AccessFilter{}, episode(2), startedEpisode, all, all[:2], []string{episode(2), series}},
		{"1080p ceiling", AccessFilter{MaxPlaybackQuality: "1080p", DisabledLibraryIDs: []int{other}}, "", "", []string{episode(1), episode(3), episode(4)}, all[:1], []string{series}},
		{"1080p ceiling with the 1080p library", AccessFilter{MaxPlaybackQuality: "1080p"}, episode(2), "", all, all[:2], []string{series}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewNextUpRepository(f.pool, pgstore.NewPostgresProvider(f.pool))
			for _, q := range []NextUpQuery{
				{UserID: f.userID, ProfileID: f.profileID, AccessFilter: tc.access, Limit: 20, RankByRelease: true},
				{UserID: f.userID, ProfileID: f.profileID, AccessFilter: tc.access, SeriesID: series, Limit: 5},
				// The resumable branch offers a started episode only when it
				// can play; the started series has only E2, in 4K.
				{UserID: f.userID, ProfileID: f.profileID, AccessFilter: tc.access, SeriesID: started, Limit: 5, EnableResumable: true},
			} {
				results, err := repo.ListNextUp(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				wantSeries, want := series, tc.nextUp
				if q.SeriesID == started {
					wantSeries, want = started, tc.resumed
				}
				got := ""
				for _, res := range results {
					if res.SeriesID == wantSeries {
						got = res.ContentID
					}
				}
				if got != want {
					t.Errorf("next up (series %q) = %q, want %q", q.SeriesID, got, want)
				}
			}

			season := 1
			browsed, _, err := NewEpisodeRepository(f.pool).BrowseEpisodes(ctx, series, "", &season, "", BrowseFilters{Limit: 20}, tc.access, false)
			if err != nil {
				t.Fatal(err)
			}
			var gotBrowse []string
			for _, ep := range browsed {
				gotBrowse = append(gotBrowse, ep.ContentID)
			}
			if fmt.Sprint(gotBrowse) != fmt.Sprint(tc.browse) {
				t.Errorf("season browse = %v, want %v", gotBrowse, tc.browse)
			}

			adjacent, err := NewEpisodeRepository(f.pool).ListAdjacentInSeriesFor(ctx, series, 1, 1, tc.access)
			if err != nil {
				t.Fatal(err)
			}
			var gotAdjacent []string
			for _, ep := range adjacent {
				gotAdjacent = append(gotAdjacent, ep.ContentID)
			}
			if fmt.Sprint(gotAdjacent) != fmt.Sprint(tc.adjacent) {
				t.Errorf("episodes adjacent to E1 = %v, want %v", gotAdjacent, tc.adjacent)
			}

			assertRecentTVTargets(t, f.pool, f.folder, f.prefix, tc.access, tc.recent)
		})
	}
}

// assertRecentTVTargets checks every recently added TV query shape for one
// library: the home row's ListNewest, the plain List, and a name-prefix List.
func assertRecentTVTargets(t *testing.T, pool *pgxpool.Pool, folder int, prefix string, access AccessFilter, want []string) {
	t.Helper()
	ctx := t.Context()
	recentRepo := NewRecentTVRepository(pool)
	q := RecentTVQuery{LibraryIDs: []int{folder}, Access: access, Limit: 10}
	newest, _, err := recentRepo.ListNewest(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, _, err := recentRepo.List(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	q.NamePrefix = prefix
	prefixed, _, _, err := recentRepo.List(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	for name, targets := range map[string][]RecentTVTarget{"newest": newest, "plain": plain, "name prefix": prefixed} {
		var got []string
		for _, target := range targets {
			got = append(got, target.ContentID)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("recently added (%s) = %v, want %v", name, got, want)
		}
	}
}

// seedQualityCeilingSeries seeds TestEpisodesAboveQualityCeilingStayHiddenPostgres
// and returns the second library. In series, E1 (1080p, watched), E2 (4K,
// arriving alone a week later), and E3-E4 (one 1080p multi-episode file);
// a second library holds a 1080p E2, which counts only where the viewer may
// use that library. In started, the profile is partway through a 4K-only E1.
// It has no library arrival row, so Recently Added doesn't count it.
func seedQualityCeilingSeries(t *testing.T, f nextUpSpecialsFixture, series, started string) int {
	t.Helper()
	ctx := t.Context()
	episode := func(n int) string { return fmt.Sprintf("%s-s01e%02d", series, n) }
	startedEpisode := started + "-s01e01"
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	f.exec(t, `INSERT INTO media_items (content_id,type,title) VALUES ($1,'series',$1)`, series)
	f.exec(t, `INSERT INTO media_item_libraries (content_id,media_folder_id,first_seen_at) VALUES ($1,$2,$3)`, series, f.folder, base)
	for n, arrived := range map[int]time.Time{1: base, 2: base.Add(7 * 24 * time.Hour), 3: base, 4: base} {
		f.exec(t, `INSERT INTO episodes (content_id,series_id,season_number,episode_number,title) VALUES ($1,$2,1,$3,$1)`, episode(n), series, n)
		f.exec(t, `INSERT INTO episode_libraries (episode_id,media_folder_id,first_seen_at) VALUES ($1,$2,$3)`, episode(n), f.folder, arrived)
	}
	f.exec(t, `INSERT INTO media_files (episode_id,media_folder_id,file_path,resolution) VALUES ($1,$2,$3,'1080p')`, episode(1), f.folder, "/"+episode(1))
	f.exec(t, `INSERT INTO media_files (episode_id,media_folder_id,file_path,resolution) VALUES ($1,$2,$3,'2160p')`, episode(2), f.folder, "/"+episode(2))
	f.exec(t, `INSERT INTO media_files (episode_id,media_folder_id,file_path,resolution,multi_episode_start,multi_episode_end) VALUES ($1,$2,$3,'1080p',3,4)`, episode(3), f.folder, "/"+episode(3))
	f.exec(t, `INSERT INTO user_watch_progress (user_id,profile_id,media_item_id,position_seconds,completed,updated_at) VALUES ($1,$2,$3,0,true,$4)`,
		f.userID, f.profileID, episode(1), base.Add(time.Hour))
	var other int
	if err := f.pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, f.prefix+"-other").Scan(&other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = f.pool.Exec(bg, `DELETE FROM media_files WHERE media_folder_id=$1`, other)
		_, _ = f.pool.Exec(bg, `DELETE FROM media_folders WHERE id=$1`, other)
	})
	f.exec(t, `INSERT INTO media_files (episode_id,media_folder_id,file_path,resolution) VALUES ($1,$2,$3,'1080p')`, episode(2), other, "/other/"+episode(2))

	f.exec(t, `INSERT INTO media_items (content_id,type,title) VALUES ($1,'series',$1)`, started)
	f.exec(t, `INSERT INTO episodes (content_id,series_id,season_number,episode_number,title) VALUES ($1,$2,1,1,$1)`, startedEpisode, started)
	f.exec(t, `INSERT INTO media_files (episode_id,media_folder_id,file_path,resolution) VALUES ($1,$2,$3,'2160p')`, startedEpisode, f.folder, "/"+startedEpisode)
	f.exec(t, `INSERT INTO user_watch_progress (user_id,profile_id,media_item_id,position_seconds,completed,updated_at) VALUES ($1,$2,$3,30,false,$4)`,
		f.userID, f.profileID, startedEpisode, base.Add(2*time.Hour))
	return other
}
