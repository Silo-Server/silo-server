package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type adminListingStatementTracer struct{ selects atomic.Int64 }

func (tracer *adminListingStatementTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(strings.TrimSpace(data.SQL), "SELECT") {
		tracer.selects.Add(1)
	}
	return ctx
}

func (*adminListingStatementTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

// adminListingFixture names the rows seedAdminListing writes: present and
// hidden items, a disabled library, and files the listing must leave out.
type adminListingFixture struct {
	movies, movies2, disabled, tv int
	movieA, series, episode1      string
	actor, director               string
	every                         []string
	t0                            time.Time
}

func seedAdminListing(t *testing.T, pool *pgxpool.Pool) adminListingFixture {
	t.Helper()
	ctx := t.Context()
	p := fmt.Sprintf("acl%d", time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed (%s): %v", sql, err)
		}
	}
	folder := func(kind string, enabled bool) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ($1,$2,$3) RETURNING id`, kind, p, enabled).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	movies, movies2, disabled, tv, podcasts, books := folder("movies", true), folder("movies", true), folder("movies", false), folder("series", true), folder("podcasts", true), folder("audiobooks", true)
	folders := []int{movies, movies2, disabled, tv, podcasts, books}
	movieA, movieHidden, movieUnlisted := p+"-movie-a", p+"-movie-hidden", p+"-movie-unlisted"
	series, episode1, episode2 := p+"-series", p+"-episode-1", p+"-episode-2"
	podcast, podcastEpisode, book := p+"-podcast", p+"-podcast-episode", p+"-book"
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = ANY($1)`, folders)
		_, _ = pool.Exec(ctx, `DELETE FROM item_people WHERE content_id LIKE $1`, p+"-%")
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, p+"-%")
		_, _ = pool.Exec(ctx, `DELETE FROM people WHERE name LIKE $1`, p+"-%")
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, folders)
	})
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	exec(`INSERT INTO media_items (content_id,type,title,year,status,imdb_id,tmdb_id) VALUES
		($1,'movie','Movie A',1999,'matched','tt1','11'), ($2,'movie','Hidden',2000,'matched','',''),
		($3,'movie','Unlisted',2001,'matched','',''), ($4,'series','Show',2010,'matched','','')`, movieA, movieHidden, movieUnlisted, series)
	exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'podcast','Cast'), ($2,'audiobook','Book')`, podcast, book)
	exec(`UPDATE media_items SET genres='{Crime,Drama}', content_rating='R', rating_imdb=8.5, rating_rt_critic=0, release_date='1999-07-02' WHERE content_id=$1`, movieA)
	exec(`UPDATE media_items SET first_air_date='2010-05-02', last_air_date='2012-01-08' WHERE content_id=$1`, series)
	person := time.Now().UnixNano() % 1_000_000_000_000
	exec(`INSERT INTO people (id,name,tmdb_id) VALUES ($1,$2,'1158'), ($1+1,$3,'')`, person, p+"-actor", p+"-director")
	exec(`INSERT INTO item_people (id,content_id,person_id,kind,character,sort_order) VALUES
		($1,$2,$1,1,'Michael',0), ($1+1,$2,$1+1,2,'',0)`, person, movieA)
	exec(`INSERT INTO episodes (content_id,series_id,season_number,episode_number,title,air_date,tvdb_id) VALUES
		($1,$3,1,1,'Pilot','2010-05-02',$1), ($2,$3,1,2,'Absent',NULL,''), ($4,$5,1,1,'Cast ep',NULL,'')`,
		episode1, episode2, series, podcastEpisode, podcast)
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id,first_seen_at) VALUES
		($1,$2,$7), ($1,$3,$8), ($4,$5,$7), ($6,$9,$7), ($10,$11,$7), ($12,$13,$7)`,
		movieA, movies, movies2, movieHidden, disabled, series, t0, t0.Add(time.Hour), tv, podcast, podcasts, book, books)
	exec(`INSERT INTO episode_libraries (episode_id,media_folder_id,first_seen_at) VALUES ($1,$2,$5), ($3,$4,$5)`,
		episode1, tv, podcastEpisode, podcasts, t0)
	exec(`INSERT INTO media_extras (content_id,parent_id,kind) VALUES ($1,$2,'trailer')`, p+"-extra", movieA)
	// The first file's default audio track is not its first, so codec,
	// channels and Atmos must all follow the default.
	exec(`INSERT INTO media_files (content_id,media_folder_id,file_path,resolution,codec_video,codec_audio,audio_channels,container,bitrate,file_size,edition_key,video_tracks,audio_tracks)
		VALUES ($1,$2,$3,'2160p','hevc','aac',2,'mkv',60000,5000,'extended',
			'[{"codec":"hevc","dv_profile":8,"video_range_type":"DOVIWithHDR10"}]',
			'[{"codec":"aac","channels":2,"default":false},{"codec":"truehd","channels":8,"profile":"Dolby TrueHD + Dolby Atmos","default":true}]')`,
		movieA, movies, "/"+p+"/a-2160.mkv")
	exec(`INSERT INTO media_files (content_id,media_folder_id,file_path,resolution,codec_video,codec_audio,audio_channels,hdr,container,file_size)
		VALUES ($1,$2,$3,'1080p','h264','ac3',6,false,'mp4',1000)`, movieA, movies2, "/"+p+"/a-1080.mp4")
	exec(`INSERT INTO media_files (content_id,media_folder_id,file_path,missing_since) VALUES ($1,$2,$3,now())`, movieA, movies, "/"+p+"/a-missing.mkv")
	exec(`INSERT INTO media_files (content_id,media_folder_id,file_path) VALUES ($1,$2,$3)`, movieA, disabled, "/"+p+"/a-disabled.mkv")
	exec(`INSERT INTO media_files (content_id,media_folder_id,file_path,extra_id) VALUES ($1,$2,$3,$4)`, movieA, movies, "/"+p+"/a-trailer.mkv", p+"-extra")
	exec(`INSERT INTO media_files (content_id,episode_id,media_folder_id,file_path,resolution,hdr) VALUES ($1,$2,$3,$4,'720p',true)`, series, episode1, tv, "/"+p+"/s01e01.mkv")

	return adminListingFixture{
		movies: movies, movies2: movies2, disabled: disabled, tv: tv,
		movieA: movieA, series: series, episode1: episode1, t0: t0, actor: p + "-actor", director: p + "-director",
		every: []string{movieA, movieHidden, movieUnlisted, series, episode1, episode2, podcast, podcastEpisode, book},
	}
}

// listAdminCatalog walks every page of a listing.
func listAdminCatalog(t *testing.T, repo *ItemRepository, filter AdminCatalogItemFilter, limit int) []AdminCatalogItem {
	t.Helper()
	var out []AdminCatalogItem
	after := ""
	for {
		page, err := repo.ListAdminCatalogItems(t.Context(), filter, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, page.Items...)
		if !page.HasMore {
			return out
		}
		after = page.Items[len(page.Items)-1].ContentID
	}
}

func adminCatalogIDs(items []AdminCatalogItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ContentID)
	}
	return out
}

// TestListAdminCatalogItemsDB pins what the administrator listing returns —
// present movies, series and episodes only, per-library versus earliest
// first-seen times, every present file with consistent track facts, stored
// metadata and opt-in credits — and that a page with files costs two
// statements, whatever else it holds, and three with credits.
func TestListAdminCatalogItemsDB(t *testing.T) {
	tracer := &adminListingStatementTracer{}
	pool := searchOptimizationPool(t, tracer)
	fx := seedAdminListing(t, pool)
	repo := NewItemRepository(pool)

	all := listAdminCatalog(t, repo, AdminCatalogItemFilter{ContentIDs: fx.every}, 50)
	if got, want := adminCatalogIDs(all), []string{fx.episode1, fx.movieA, fx.series}; !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if paged := adminCatalogIDs(listAdminCatalog(t, repo, AdminCatalogItemFilter{ContentIDs: fx.every}, 1)); !slices.Equal(paged, adminCatalogIDs(all)) {
		t.Fatalf("one-item pages listed %v, want %v", paged, adminCatalogIDs(all))
	}
	checkAdminListingItems(t, fx, all[0], all[1], all[2])

	t0 := fx.t0
	for _, c := range []struct {
		name    string
		filter  AdminCatalogItemFilter
		want    []string
		addedAt time.Time
	}{
		{"library time", AdminCatalogItemFilter{LibraryID: fx.movies2}, []string{fx.movieA}, t0.Add(time.Hour)},
		{"disabled library", AdminCatalogItemFilter{LibraryID: fx.disabled}, nil, time.Time{}},
		{"type", AdminCatalogItemFilter{Type: AdminCatalogTypeEpisode, ContentIDs: fx.every}, []string{fx.episode1}, t0},
		{"added_after inclusive", AdminCatalogItemFilter{AddedAfter: &t0, ContentIDs: []string{fx.movieA}}, []string{fx.movieA}, t0},
		{"added_after uses earliest", AdminCatalogItemFilter{AddedAfter: new(t0.Add(time.Minute)), ContentIDs: []string{fx.movieA}}, nil, time.Time{}},
		{"added_after per library", AdminCatalogItemFilter{LibraryID: fx.movies2, AddedAfter: new(t0.Add(time.Minute))}, []string{fx.movieA}, t0.Add(time.Hour)},
	} {
		got := listAdminCatalog(t, repo, c.filter, 50)
		if !slices.Equal(adminCatalogIDs(got), c.want) {
			t.Fatalf("%s: listed %v, want %v", c.name, adminCatalogIDs(got), c.want)
		}
		if len(got) > 0 && !got[0].AddedAt.Equal(c.addedAt) {
			t.Fatalf("%s: added_at %v, want %v", c.name, got[0].AddedAt, c.addedAt)
		}
	}

	for _, c := range []struct {
		filter AdminCatalogItemFilter
		want   int64
	}{
		{AdminCatalogItemFilter{ContentIDs: fx.every}, 2},
		{AdminCatalogItemFilter{ContentIDs: []string{fx.movieA}}, 2},
		{AdminCatalogItemFilter{ContentIDs: fx.every, People: true}, 3},
	} {
		tracer.selects.Store(0)
		page, err := repo.ListAdminCatalogItems(t.Context(), c.filter, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		if n := tracer.selects.Load(); n != c.want {
			t.Fatalf("a page of %+v ran %d statements, want %d", c.filter, n, c.want)
		}
		if c.filter.People {
			movie := page.Items[1]
			if len(movie.Cast) != 1 || movie.Cast[0].Name != fx.actor || movie.Cast[0].Character != "Michael" || movie.Cast[0].TmdbID != "1158" ||
				len(movie.Crew) != 1 || movie.Crew[0].Name != fx.director || movie.Crew[0].Job != "Director" {
				t.Fatalf("movie credits = %+v / %+v", movie.Cast, movie.Crew)
			}
		}
	}
}

func checkAdminListingItems(t *testing.T, fx adminListingFixture, ep, movie, show AdminCatalogItem) {
	t.Helper()
	if ep.Type != AdminCatalogTypeEpisode || ep.SeriesID != fx.series || ep.SeriesTitle != "Show" || ep.Year != 2010 ||
		*ep.SeasonNumber != 1 || *ep.EpisodeNumber != 1 || ep.TvdbID != fx.episode1 || ep.Status != "" || !slices.Equal(ep.LibraryIDs, []int{fx.tv}) {
		t.Fatalf("episode = %+v", ep)
	}
	if len(ep.Versions) != 1 || ep.Versions[0].DynamicRange != "hdr_unknown" || ep.Versions[0].Resolution != "720p" {
		t.Fatalf("episode versions = %+v", ep.Versions)
	}
	if show.Type != AdminCatalogTypeSeries || len(show.Versions) != 0 || show.SeasonNumber != nil {
		t.Fatalf("series = %+v", show)
	}
	if !movie.AddedAt.Equal(fx.t0) || !slices.Equal(movie.LibraryIDs, []int{fx.movies, fx.movies2}) || movie.ImdbID != "tt1" || movie.Status != "matched" {
		t.Fatalf("movie = %+v", movie)
	}
	// A zero score is a score, and credits load only on request.
	if !slices.Equal(movie.Genres, []string{"Crime", "Drama"}) || movie.ContentRating != "R" || movie.RatingIMDB == nil || *movie.RatingIMDB != 8.5 ||
		movie.RatingRTCritic == nil || *movie.RatingRTCritic != 0 || movie.RatingTMDB != nil || movie.Cast != nil || movie.Crew != nil ||
		movie.ReleaseDate == nil || *movie.ReleaseDate != "1999-07-02" || movie.AirDate != nil {
		t.Fatalf("movie metadata = %+v", movie)
	}
	if show.FirstAirDate == nil || *show.FirstAirDate != "2010-05-02" || show.LastAirDate == nil || *show.LastAirDate != "2012-01-08" {
		t.Fatalf("series dates = %+v", show)
	}
	if ep.Genres != nil || ep.ContentRating != "" || ep.RatingIMDB != nil || ep.ReleaseDate != nil || ep.AirDate == nil || *ep.AirDate != "2010-05-02" {
		t.Fatalf("episode metadata = %+v", ep)
	}
	want := []AdminCatalogVersion{
		{LibraryID: fx.movies, Resolution: "2160p", CodecVideo: "hevc", DynamicRange: "dolby_vision", CodecAudio: "truehd", AudioChannels: 8, Atmos: true, Container: "mkv", BitrateKbps: 60000, FileSize: 5000, Edition: "Extended"},
		{LibraryID: fx.movies2, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "ac3", AudioChannels: 6, Container: "mp4", FileSize: 1000},
	}
	if len(movie.Versions) != len(want) {
		t.Fatalf("movie versions = %+v", movie.Versions)
	}
	for i := range want {
		want[i].FileID = movie.Versions[i].FileID
		if movie.Versions[i] != want[i] {
			t.Fatalf("version %d = %+v, want %+v", i, movie.Versions[i], want[i])
		}
	}
}
