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
