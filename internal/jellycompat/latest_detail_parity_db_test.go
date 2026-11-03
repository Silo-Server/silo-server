package jellycompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// Field sets real clients send to /Items/Latest.
const (
	latestInfuseFields    = "DateCreated,Etag,Genres,MediaSources,AlternateMediaSources,Overview,ParentId,Path,ProviderIds,SortName,RecursiveItemCount,ChildCount"
	latestAndroidTVFields = "CanDelete,ChannelInfo,Chapters,ChildCount,CumulativeRunTimeTicks,DateCreated,DisplayPreferencesId,Genres,ItemCounts,MediaSourceCount,MediaSources,MediaStreams,Overview,Path,PrimaryImageAspectRatio,Taglines,Trickplay"
)

// latestStatementCounter counts the statements a pool runs, grouped by
// their text so a budget failure can name the statement that grew.
type latestStatementCounter struct {
	n   atomic.Int64
	mu  sync.Mutex
	sql map[string]int
}

func (c *latestStatementCounter) reset() {
	c.mu.Lock()
	c.sql = nil
	c.mu.Unlock()
}

func (c *latestStatementCounter) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.sql))
	maps.Copy(out, c.sql)
	return out
}

func (c *latestStatementCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	c.mu.Lock()
	if c.sql == nil {
		c.sql = map[string]int{}
	}
	c.sql[strings.Join(strings.Fields(d.SQL), " ")]++
	c.mu.Unlock()
	return ctx
}

func (c *latestStatementCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type latestTestImageResolver struct{}

func (latestTestImageResolver) ResolveImageURL(_ context.Context, path, variant string) string {
	return "https://images.test/" + path + "?v=" + variant
}

func (r latestTestImageResolver) ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(ctx, path, variant)
	}
	return out
}

type latestParityFixture struct {
	pool      *pgxpool.Pool
	statement *latestStatementCounter
	handler   *ItemsHandler
	session   *Session
	store     userstore.UserStore
	suffix    string
	seededAt  time.Time
	movieLib  int
	tvLib     int
	// spareLib holds an extra version of the first movie; a viewer may see
	// it unless restrict disables it.
	spareLib int
	movies   []string
	series   []string
	// restrict, when set, narrows the viewer's access filter.
	restrict func(*catalog.AccessFilter)
}

// newLatestParityFixture seeds a movie library and a TV library shaped like
// the ones /Items/Latest serves: multi-version movies with audio, subtitle
// (embedded and external) and chapter data, a movie with a stored audio
// choice, a trailer and a local extra, and series whose episodes carry files
// and watch progress. The handler is wired the way the compat router wires
// production, over the real repositories.
func newLatestParityFixture(t *testing.T, movieCount, seriesCount, episodesPerSeries int) *latestParityFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	f := &latestParityFixture{
		statement: &latestStatementCounter{},
		suffix:    strconv.FormatInt(time.Now().UnixNano(), 36),
		seededAt:  time.Now().UTC().Add(-time.Hour),
	}
	cfg.ConnConfig.Tracer = f.statement
	if f.pool, err = pgxpool.NewWithConfig(context.Background(), cfg); err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(f.pool.Close)

	provider := pgstore.NewPostgresProvider(f.pool)
	f.seedViewer(t, provider)
	f.seedMovies(t, movieCount)
	f.seedSeries(t, seriesCount, episodesPerSeries)
	f.seedCredits(t)
	f.wire(provider)
	return f
}

func (f *latestParityFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed %q: %v", sql, err)
	}
}

func (f *latestParityFixture) queryID(t *testing.T, dest any, sql string, args ...any) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(dest); err != nil {
		t.Fatalf("seed %q: %v", sql, err)
	}
}

// seedViewer creates the two libraries and a profile to browse them with.
func (f *latestParityFixture) seedViewer(t *testing.T, provider *pgstore.PostgresProvider) {
	t.Helper()
	ctx := context.Background()
	f.queryID(t, &f.movieLib, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`, "latest-movies-"+f.suffix)
	f.queryID(t, &f.tvLib, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, TRUE) RETURNING id`, "latest-tv-"+f.suffix)
	f.queryID(t, &f.spareLib, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`, "latest-spare-"+f.suffix)
	var userID int
	f.queryID(t, &userID, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, "latest-"+f.suffix)
	pool := f.pool
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_watch_history WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_audio_preferences WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_setting_values WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = ANY($1)`, []int{f.movieLib, f.tvLib, f.spareLib})
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = ANY($1)`, f.series)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, append(append([]string{}, f.movies...), f.series...))
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{f.movieLib, f.tvLib, f.spareLib})
	})
	f.store, _ = provider.ForUser(ctx, userID)
	f.session = &Session{Token: "token", ProfileID: "latest-" + f.suffix, StreamAppUserID: userID}
	if err := f.store.CreateProfile(ctx, userstore.Profile{ID: f.session.ProfileID, Name: "Latest"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
}

const (
	latestVideo4K    = `[{"codec":"hevc","width":3840,"height":2160,"bit_depth":10,"frame_rate":"24000/1001"}]`
	latestVideo1080  = `[{"codec":"h264","width":1920,"height":1080,"profile":"High","frame_rate":"24"}]`
	latestAudioMulti = `[{"index":1,"codec":"eac3","channels":6,"language":"eng","default":true},{"index":2,"codec":"aac","channels":2,"language":"jpn"}]`
	latestSubsEmbed  = `[{"index":3,"codec":"subrip","language":"eng","default":true},{"index":4,"codec":"hdmv_pgs_subtitle","language":"jpn","forced":true}]`
	latestSubsExt    = `[{"path":"/media/movies/ext.en.srt","language":"eng","format":"srt"},{"path":"/media/movies/ext.fr.forced.ass","language":"fra","format":"ass","forced":true}]`
	latestChapters   = `[{"index":0,"title":"Opening","start_seconds":0,"end_seconds":300},{"index":1,"title":"Middle","start_seconds":300,"end_seconds":5400}]`
)

// seedMovies adds movies with one to two present versions and a missing one.
func (f *latestParityFixture) seedMovies(t *testing.T, count int) {
	t.Helper()
	ctx := context.Background()
	for i := range count {
		id := fmt.Sprintf("latest-movie-%d-%s", i, f.suffix)
		f.movies = append(f.movies, id)
		f.exec(t, `INSERT INTO media_items (content_id, type, title, original_title, sort_title, year, overview, tagline, genres, studios, countries,
				content_rating, status, imdb_id, tmdb_id, release_date, poster_path, backdrop_path, logo_path, original_language)
			VALUES ($1, 'movie', $2, $3, $4, 2020, 'Overview', 'A tagline', '{Drama,Action}', '{Studio A}', '{US,GB}',
				'PG-13', 'matched', 'tt' || $5, $5, '2020-05-01', 'local/movies/' || $1 || '/p/poster/original.webp',
				'local/movies/' || $1 || '/b/backdrop/original.webp', '', 'en')`,
			id, fmt.Sprintf("Movie %d", i), fmt.Sprintf("Original %d", i), fmt.Sprintf("movie %02d", i), strconv.Itoa(1000+i))
		f.exec(t, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, f.movieLib, f.seededAt.Add(time.Duration(i)*time.Minute))
		// Every movie has a 1080p file; every other one also has a 4K
		// version with a lower id, so detail must re-order by width. One in
		// three carries external subtitles, and a missing file must never
		// surface as a source.
		if i%2 == 0 {
			f.exec(t, `INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, container, codec_video, codec_audio, resolution, hdr, duration, bitrate,
					video_tracks, audio_tracks, subtitle_tracks, chapters, created_at)
				VALUES ($1, $2, '/media/movies/' || $1 || '/2160p.mkv', 40000000000, 'mkv', 'hevc', 'eac3', '2160p', TRUE, 7200, 45000,
					$3::jsonb, $4::jsonb, $5::jsonb, $6::jsonb, $7)`, id, f.movieLib, latestVideo4K, latestAudioMulti, latestSubsEmbed, latestChapters, f.seededAt)
		}
		ext := "null"
		if i%3 == 0 {
			ext = latestSubsExt
		}
		f.exec(t, `INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, container, codec_video, codec_audio, resolution, hdr, duration, bitrate,
				video_tracks, audio_tracks, subtitle_tracks, external_subtitles, chapters, intro_start, intro_end, created_at)
			VALUES ($1, $2, '/media/movies/' || $1 || '/1080p.mp4', 8000000000, 'mp4', 'h264', 'aac', '1080p', FALSE, 7195, 9000,
				$3::jsonb, $4::jsonb, $5::jsonb, $6::jsonb, $7::jsonb, 10, 70, $8)`, id, f.movieLib, latestVideo1080, latestAudioMulti, latestSubsEmbed, ext, latestChapters, f.seededAt.Add(time.Minute))
		f.exec(t, `INSERT INTO media_files (content_id, media_folder_id, file_path, container, missing_since) VALUES ($1, $2, '/media/movies/' || $1 || '/gone.mkv', 'mkv', now())`, id, f.movieLib)
	}
	profileID := f.session.ProfileID
	if count > 0 {
		first := f.movies[0]
		f.exec(t, `INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, container, codec_video, resolution, duration, bitrate, video_tracks, audio_tracks)
			VALUES ($1, $2, '/media/spare/' || $1 || '/720p.mkv', 2000000000, 'mkv', 'h264', '720p', 7190, 4000, '[{"codec":"h264","width":1280,"height":720}]'::jsonb, $3::jsonb)`,
			first, f.spareLib, latestAudioMulti)
		f.exec(t, `INSERT INTO item_videos (id, content_id, provider, provider_key, kind, site, site_key, name) VALUES ((SELECT COALESCE(MAX(id), 0) + 1 FROM item_videos), $1, 'tmdb', $1, 'trailer', 'youtube', 'abc123', 'Trailer')`, first)
		f.exec(t, `INSERT INTO media_extras (content_id, parent_id, kind, title) VALUES ($1 || '-extra', $1, 'trailer', 'Local trailer')`, first)
		// The stored choice names the Japanese track, which outranks the
		// spare library's English preference for that version.
		if err := f.store.SetAudioPreference(ctx, userstore.AudioPreference{ProfileID: profileID, SeriesID: first, AudioTrackIndex: 1, AudioLanguage: "jpn",
			TrackSignature: &userstore.AudioTrackSignature{Language: "jpn", Codec: "aac", Channels: 2}}); err != nil {
			t.Fatalf("SetAudioPreference: %v", err)
		}
		if err := f.store.SetProgressAt(ctx, profileID, first, 1800, 7200, false, f.seededAt); err != nil {
			t.Fatalf("SetProgressAt: %v", err)
		}
	}
	if count > 1 {
		if err := f.store.SetProgressAt(ctx, profileID, f.movies[1], 7200, 7200, true, f.seededAt); err != nil {
			t.Fatalf("SetProgressAt: %v", err)
		}
	}
	// Audio languages at every scope the per-item resolution ranks: the
	// profile prefers Japanese, the movie library English, one movie French
	// (no such track) and another its original language.
	f.setAudioLanguage(t, userstore.SettingIdentity{Scope: settingscontract.ScopeProfile}, "jpn")
	f.setAudioLanguage(t, userstore.SettingIdentity{Scope: settingscontract.ScopeProfileLibrary, LibraryID: f.spareLib}, "eng")
	if count > 2 {
		f.setAudioLanguage(t, userstore.SettingIdentity{Scope: settingscontract.ScopeProfileSeries, SeriesID: f.movies[2]}, "fra")
	}
	if count > 3 {
		// English, unlike the profile, so the original-language rule decides
		// this movie's track rather than the profile preference.
		f.exec(t, `UPDATE media_items SET original_language = 'en' WHERE content_id = $1`, f.movies[3])
		f.setAudioLanguage(t, userstore.SettingIdentity{Scope: settingscontract.ScopeProfileSeries, SeriesID: f.movies[3]}, playback.OriginalLanguageSentinel)
	}
}

func (f *latestParityFixture) setAudioLanguage(t *testing.T, id userstore.SettingIdentity, language string) {
	t.Helper()
	id.Key = settingskeys.PlaybackAudioLanguage
	id.ProfileID = f.session.ProfileID
	value, _ := json.Marshal(language)
	if _, err := f.store.UpsertSettingValue(context.Background(), id, value); err != nil {
		t.Fatalf("UpsertSettingValue(%+v): %v", id, err)
	}
}

// seedSeries adds series whose episodes carry files; every other series has
// its first episode watched.
func (f *latestParityFixture) seedSeries(t *testing.T, count, episodes int) {
	t.Helper()
	for i := range count {
		id := fmt.Sprintf("latest-series-%d-%s", i, f.suffix)
		f.series = append(f.series, id)
		f.exec(t, `INSERT INTO media_items (content_id, type, title, original_title, year, overview, tagline, genres, studios, countries, content_rating, status,
				tvdb_id, first_air_date, season_count, poster_path, backdrop_path, logo_path, original_language)
			VALUES ($1, 'series', $2, $2, 2019, 'Series overview', '', '{Comedy}', '{Network}', '{US}', 'TV-14', 'matched',
				$3, '2019-01-01', 1, 'local/tv/' || $1 || '/p/poster/original.webp', '', 'local/tv/' || $1 || '/l/logo/original.webp', 'en')`,
			id, fmt.Sprintf("Series %d", i), strconv.Itoa(5000+i))
		f.exec(t, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, f.tvLib, f.seededAt.Add(time.Duration(i)*time.Minute))
		for e := 1; e <= episodes; e++ {
			epID := fmt.Sprintf("%s-e%d", id, e)
			f.exec(t, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview, runtime, still_path, imdb_id, tmdb_id, tvdb_id)
				VALUES ($1, $2, 1, $3, $4, '', 45, '', '', '', '')`, epID, id, e, fmt.Sprintf("Episode %d", e))
			f.exec(t, `INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $2)`, epID, f.tvLib)
			f.exec(t, `INSERT INTO media_files (content_id, episode_id, season_number, episode_number, media_folder_id, file_path, container, duration, video_tracks, audio_tracks)
				VALUES ($1, $2, 1, $3, $4, '/media/tv/' || $2 || '.mkv', 'mkv', 2700, $5::jsonb, $6::jsonb)`, id, epID, e, f.tvLib, latestVideo1080, latestAudioMulti)
		}
		if i%2 == 0 && episodes > 0 {
			if err := f.store.SetProgressAt(context.Background(), f.session.ProfileID, id+"-e1", 2700, 2700, true, f.seededAt); err != nil {
				t.Fatalf("SetProgressAt episode: %v", err)
			}
		}
	}
}

// seedCredits puts cast and crew on the first movie and series, for People
// requests.
func (f *latestParityFixture) seedCredits(t *testing.T) {
	t.Helper()
	var personID int64
	f.queryID(t, &personID, `INSERT INTO people (id, name, photo_path) VALUES ((SELECT COALESCE(MAX(id), 0) + 1 FROM people), $1, 'local/people/latest/photo/original.webp') RETURNING id`, "Latest Actor "+f.suffix)
	pool := f.pool
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE id = $1`, personID) })
	for _, credited := range []string{first(f.movies), first(f.series)} {
		if credited == "" {
			continue
		}
		f.exec(t, `INSERT INTO item_people (id, content_id, person_id, kind, character) VALUES ((SELECT COALESCE(MAX(id), 0) + 1 FROM item_people), $1, $2, 1, 'Lead')`, credited, personID)
		f.exec(t, `INSERT INTO item_people (id, content_id, person_id, kind) VALUES ((SELECT COALESCE(MAX(id), 0) + 1 FROM item_people), $1, $2, 2)`, credited, personID)
	}
}

// wire builds the items handler the way the compat router does in
// production, over the real repositories.
func (f *latestParityFixture) wire(provider *pgstore.PostgresProvider) {
	pool := f.pool
	itemRepo := catalog.NewItemRepository(pool)
	episodeRepo := catalog.NewEpisodeRepository(pool)
	seasonRepo := catalog.NewSeasonRepository(pool)
	folderRepo := catalog.NewFolderRepository(pool)
	detailSvc := catalog.NewDetailService(itemRepo, episodeRepo, seasonRepo, catalog.NewPersonRepository(pool), scanner.NewFileRepository(pool))
	detailSvc.SetFolderRepository(folderRepo)
	detailSvc.SetGroupClaimRepository(catalog.NewGroupClaimRepository(pool))
	detailSvc.SetImageResolver(latestTestImageResolver{})
	detailSvc.SetUserStoreProvider(provider)
	libraries := []int{f.movieLib, f.tvLib, f.spareLib}
	accessFn := func(_ context.Context, uid int, pid string) catalog.AccessFilter {
		filter := catalog.AccessFilter{UserID: uid, ProfileID: pid, AllowedLibraryIDs: libraries}
		if f.restrict != nil {
			f.restrict(&filter)
		}
		return filter
	}
	browseRepo := catalog.NewBrowseRepository(pool)
	content := newDirectContentService(browseRepo, itemRepo, seasonRepo, episodeRepo, detailSvc, folderRepo, provider, accessFn, nil)
	content.catalogUserState = true
	userData := newDirectUserDataService(provider, itemRepo, episodeRepo, nil, detailSvc, catalog.NewContinueWatchingProgressFilter(pool), nil, nil)
	f.handler = NewItemsHandler(content, userData, NewResourceIDCodec(), &config.Config{Auth: config.AuthConfig{JWTSecret: "secret"}}, nil, nil, browseRepo, nil, detailSvc, itemRepo, episodeRepo, seasonRepo, AccessFilterResolver(accessFn), nil)
}

func first(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func (f *latestParityFixture) latestRequest(fields string, libraryID int, extra url.Values) *http.Request {
	q := url.Values{"Limit": {"50"}, "Fields": {fields}}
	if libraryID > 0 {
		q.Set("ParentId", f.handler.codec.EncodeIntID(EncodedIDLibrary, int64(libraryID)))
	}
	for k, v := range extra {
		q[k] = v
	}
	req := httptest.NewRequest(http.MethodGet, "/Items/Latest?"+q.Encode(), nil)
	return req.WithContext(context.WithValue(req.Context(), compatSessionKey, f.session))
}

// serveLatest runs HandleLatest and returns the body plus the number of
// statements it issued.
func (f *latestParityFixture) serveLatest(t *testing.T, req *http.Request) ([]byte, int64, time.Duration) {
	t.Helper()
	rec := httptest.NewRecorder()
	before := f.statement.n.Load()
	start := time.Now()
	f.handler.HandleLatest(rec, req)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("HandleLatest status %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes(), f.statement.n.Load() - before, elapsed
}

// legacyLatestBody renders the same Latest page through the full single-item
// detail: every card, series included, from GetItemDetail and
// itemFromDetailWithFields, with nothing trimmed or batched. It is the
// reference the batched Latest path must reproduce.
func (f *latestParityFixture) legacyLatestBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	h := f.handler
	ctx := req.Context()
	query := parseItemsQuery(req, h.codec)
	if query.parentLibraryID > 0 && len(query.itemTypes) == 0 {
		if itemType := h.inferLibraryItemType(ctx, f.session, query.parentLibraryID); itemType != "" {
			query.itemTypes = []string{itemType}
		}
	}
	result, err := h.content.BrowseItems(ctx, f.session, buildLatestBrowseParams(query))
	if err != nil {
		t.Fatalf("BrowseItems: %v", err)
	}
	contentIDs := contentIDsFromListItems(result.Items)
	favorites, progress, err := resolveUserStateForContentIDs(ctx, f.session, h.userData, contentIDs)
	if err != nil {
		t.Fatalf("user state: %v", err)
	}
	var libraryParentID string
	if query.parentLibraryID > 0 {
		libraryParentID = h.codec.EncodeIntID(EncodedIDLibrary, int64(query.parentLibraryID))
	}
	items := make([]baseItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		var dto baseItemDTO
		if detail, err := h.content.GetItemDetail(ctx, f.session, item.ContentID, libraryIDPtr(query.parentLibraryID)); err == nil && detail != nil {
			dto = h.mapper.itemFromDetailWithFields(*detail, favorites[detail.ContentID], progress[detail.ContentID], query.requestedFields)
		} else {
			dto = h.mapper.itemFromList(item, favorites[item.ContentID], progress[item.ContentID], query.requestedFields)
		}
		if libraryParentID != "" {
			dto.ParentID = libraryParentID
		}
		items = append(items, dto)
	}
	h.applyListFileFields(ctx, f.session, items, query)
	applyItemsResponseOptions(items, query)
	body, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

// assertSameLatestJSON compares two Latest bodies item by item so a failure
// names the item and shows both renderings.
func assertSameLatestJSON(t *testing.T, label string, got, want []byte) {
	t.Helper()
	var gotItems, wantItems []json.RawMessage
	if err := json.Unmarshal(got, &gotItems); err != nil {
		t.Fatalf("%s: decode got: %v", label, err)
	}
	if err := json.Unmarshal(want, &wantItems); err != nil {
		t.Fatalf("%s: decode want: %v", label, err)
	}
	if len(gotItems) != len(wantItems) {
		t.Fatalf("%s: %d items, want %d", label, len(gotItems), len(wantItems))
	}
	for i := range wantItems {
		if !bytes.Equal(normalizeJSON(t, gotItems[i]), normalizeJSON(t, wantItems[i])) {
			t.Errorf("%s: item %d differs\n got: %s\nwant: %s", label, i, gotItems[i], wantItems[i])
		}
	}
}

func normalizeJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return out
}

// TestLatestMatchesDetailRenderingDB: the Latest response for the field sets
// Infuse and Jellyfin Android TV send is identical to rendering every card
// through the full item detail, for a movie library, a TV library and a
// cross-library page.
func TestLatestMatchesDetailRenderingDB(t *testing.T) {
	f := newLatestParityFixture(t, 6, 3, 4)
	for _, tc := range []struct {
		name    string
		fields  string
		library int
	}{
		{"infuse movies", latestInfuseFields, f.movieLib},
		{"infuse tv", latestInfuseFields, f.tvLib},
		{"android tv movies", latestAndroidTVFields, f.movieLib},
		{"android tv tv", latestAndroidTVFields, f.tvLib},
		{"android tv all", latestAndroidTVFields, 0},
		{"people", "People,MediaSources", f.movieLib},
		{"people tv", "People,MediaSources", f.tvLib},
		{"1080p ceiling", latestAndroidTVFields, f.movieLib},
		{"disabled library", latestInfuseFields, f.movieLib},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.restrict = nil
			switch tc.name {
			case "1080p ceiling":
				f.restrict = func(filter *catalog.AccessFilter) { filter.MaxPlaybackQuality = "1080p" }
			case "disabled library":
				f.restrict = func(filter *catalog.AccessFilter) { filter.DisabledLibraryIDs = []int{f.spareLib} }
			}
			defer func() { f.restrict = nil }()
			req := f.latestRequest(tc.fields, tc.library, nil)
			got, statements, elapsed := f.serveLatest(t, req)
			want := f.legacyLatestBody(t, f.latestRequest(tc.fields, tc.library, nil))
			t.Logf("%s: %d statements, %s", tc.name, statements, elapsed)
			assertSameLatestJSON(t, tc.name, got, want)
			if tc.library == f.movieLib {
				f.assertMovieSourcesVary(t, tc.name, got)
			}
		})
	}
}

// assertMovieSourcesVary guards the comparison against passing on cards that
// carry nothing to compare: the first movie's sources must reflect the
// viewer's access, and the stored audio languages must pick different
// default tracks.
func (f *latestParityFixture) assertMovieSourcesVary(t *testing.T, label string, body []byte) {
	t.Helper()
	var items []baseItemDTO
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("%s: decode: %v", label, err)
	}
	firstID := f.handler.codec.EncodeStringID(EncodedIDItem, f.movies[0])
	wantSources := 3 // 4K, 1080p and the spare-library 720p
	switch label {
	case "1080p ceiling", "disabled library":
		wantSources = 2
	}
	defaultAudio := map[int]bool{}
	for _, item := range items {
		for _, source := range item.MediaSources {
			if source.DefaultAudioStreamIndex != nil {
				defaultAudio[*source.DefaultAudioStreamIndex] = true
			}
		}
		if item.ID == firstID && len(item.MediaSources) != wantSources {
			t.Errorf("%s: first movie has %d sources, want %d", label, len(item.MediaSources), wantSources)
		}
	}
	if !defaultAudio[1] || !defaultAudio[2] {
		t.Errorf("%s: default audio stream indexes %v, want both the English (1) and Japanese (2) tracks chosen somewhere", label, defaultAudio)
	}
}

// TestLatestStatementBudgetDB: a Latest page that asks for MediaSources costs
// the same number of statements for 20 cards as for 5, so no card adds its
// own lookups.
func TestLatestStatementBudgetDB(t *testing.T) {
	f := newLatestParityFixture(t, 20, 20, 3)
	for _, tc := range []struct {
		name    string
		fields  string
		library int
	}{
		{"infuse movies", latestInfuseFields, f.movieLib},
		{"infuse tv", latestInfuseFields, f.tvLib},
		{"android tv movies", latestAndroidTVFields, f.movieLib},
		{"android tv tv", latestAndroidTVFields, f.tvLib},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := func(limit int) (int64, map[string]int) {
				f.statement.reset()
				// Without EnableTotalRecordCount=false a page smaller than the
				// library also counts the whole library, which is not a
				// per-card cost.
				body, statements, _ := f.serveLatest(t, f.latestRequest(tc.fields, tc.library, url.Values{
					"Limit":                  {strconv.Itoa(limit)},
					"EnableTotalRecordCount": {"false"},
				}))
				var items []json.RawMessage
				if err := json.Unmarshal(body, &items); err != nil || len(items) != limit {
					t.Fatalf("Limit=%d returned %d items (%v)", limit, len(items), err)
				}
				return statements, f.statement.snapshot()
			}
			small, smallSQL := count(5)
			large, largeSQL := count(20)
			t.Logf("%s: %d statements for 20 cards", tc.name, large)
			if small != large {
				t.Errorf("%d statements for 5 cards, %d for 20", small, large)
				for sql, n := range largeSQL {
					if smallSQL[sql] != n {
						t.Logf("%d -> %d: %s", smallSQL[sql], n, sql)
					}
				}
			}
		})
	}
}

// TestIdsListKeepsSubtitleDefaultsDB: an Ids list renders each movie's
// DefaultSubtitleStreamIndex from the viewer's stored subtitle defaults, so
// its batched detail must keep them, unlike the Latest batch. The response
// must match the one per-item lookups build.
func TestIdsListKeepsSubtitleDefaultsDB(t *testing.T) {
	f := newLatestParityFixture(t, 3, 0, 0)
	value, _ := json.Marshal("off")
	if _, err := f.store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key: settingskeys.PlaybackSubtitleMode, Scope: settingscontract.ScopeProfile, ProfileID: f.session.ProfileID,
	}, value); err != nil {
		t.Fatalf("UpsertSettingValue: %v", err)
	}
	ids := make([]string, 0, len(f.movies))
	for _, id := range f.movies {
		ids = append(ids, f.handler.codec.EncodeStringID(EncodedIDItem, id))
	}
	serve := func(h *ItemsHandler) []byte {
		req := httptest.NewRequest(http.MethodGet, "/Items?"+url.Values{"Ids": {strings.Join(ids, ",")}, "Fields": {latestInfuseFields}}.Encode(), nil)
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, f.session))
		rec := httptest.NewRecorder()
		h.handleSpecificItems(rec, req, f.session, parseItemsQuery(req, h.codec))
		if rec.Code != http.StatusOK {
			t.Fatalf("Ids status %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	batched := serve(f.handler)
	perItemHandler := *f.handler
	perItemHandler.content = perItemDetails{f.handler.content}
	perItem := serve(&perItemHandler)
	if !bytes.Equal(normalizeJSON(t, batched), normalizeJSON(t, perItem)) {
		t.Errorf("batched Ids list differs from per-item:\nbatched:  %s\nper item: %s", batched, perItem)
	}
	// Mode off is Jellyfin's OnlyForced: the forced Japanese track (4), not
	// the default English one (3) a viewer without the setting gets.
	if bytes.Contains(batched, []byte(`"DefaultSubtitleStreamIndex":3`)) || !bytes.Contains(batched, []byte(`"DefaultSubtitleStreamIndex":4`)) {
		t.Error("the stored subtitle mode did not pick the forced track")
	}
}
