package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

type versionsQueryCounter struct{ calls atomic.Int64 }

func (c *versionsQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.calls.Add(1)
	return ctx
}
func (*versionsQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type versionsImageCounter struct{ calls atomic.Int64 }

func (c *versionsImageCounter) ResolveImageURL(_ context.Context, path, _ string) string {
	c.calls.Add(1)
	return "https://images.invalid/" + path
}
func (c *versionsImageCounter) ResolveImageURLs(_ context.Context, paths []string, _ string) map[string]string {
	c.calls.Add(1)
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = "https://images.invalid/" + path
	}
	return out
}

type versionsFileFetcher struct {
	files map[string][]*models.MediaFile
	err   error
}

func (f *versionsFileFetcher) GetByContentID(_ context.Context, id string) ([]*models.MediaFile, error) {
	return slices.Clone(f.files[id]), f.err
}
func (f *versionsFileFetcher) GetByEpisodeID(ctx context.Context, id string) ([]*models.MediaFile, error) {
	return f.GetByContentID(ctx, id)
}
func (f *versionsFileFetcher) GetByExtraID(ctx context.Context, id string) ([]*models.MediaFile, error) {
	return f.GetByContentID(ctx, id)
}

type versionsFixture struct {
	svc     *DetailService
	queries *versionsQueryCounter
	images  *versionsImageCounter
	files   *versionsFileFetcher
	ids     map[string]string
	library int
}

// Catalog reads use real PostgreSQL repositories. File metadata is in memory so
// rich probe data can exercise the shared builder without the scanner import cycle.
func newVersionsFixture(t testing.TB) *versionsFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	counter := &versionsQueryCounter{}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	f := &versionsFixture{queries: counter, images: &versionsImageCounter{}, files: &versionsFileFetcher{files: map[string][]*models.MediaFile{}}, ids: map[string]string{}}
	prefix := fmt.Sprintf("versions-%d-", time.Now().UnixNano())
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type,name) VALUES ('movies',$1) RETURNING id`, prefix).Scan(&f.library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, f.library); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%"); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM people WHERE name LIKE $1`, prefix+"%"); err != nil {
			t.Error(err)
		}
	})
	for _, kind := range []string{"movie", "audiobook", "ebook", "manga", "series"} {
		id := prefix + kind
		f.ids[kind] = id
		exec(`INSERT INTO media_items (content_id,type,title,overview,genres,content_rating,default_metadata_language,poster_path) VALUES ($1,$2,$3,'Overview','{}','PG','en','tmdb/poster/original.jpg')`, id, kind, kind)
		exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, id, f.library)
	}
	f.ids["season"] = prefix + "season"
	f.ids["episode"] = prefix + "episode"
	f.ids["extra"] = prefix + "extra"
	if err := NewSeasonRepository(pool).Upsert(t.Context(), &models.Season{ContentID: f.ids["season"], SeriesID: f.ids["series"], SeasonNumber: 1, Title: "Season", DefaultMetadataLanguage: "en"}); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title) VALUES ($1,$2,$3,1,1,'Episode')`, f.ids["episode"], f.ids["series"], f.ids["season"])
	exec(`INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES ($1,$2)`, f.ids["episode"], f.library)
	exec(`INSERT INTO media_extras (content_id,parent_id,kind,title) VALUES ($1,$2,'featurette','Extra')`, f.ids["extra"], f.ids["movie"])
	for _, kind := range []string{"movie", "audiobook", "ebook", "manga", "episode", "extra"} {
		id := f.ids[kind]
		for i := range 2 {
			file := &models.MediaFile{ID: i + 1, ContentID: id, MediaFolderID: f.library, FilePath: fmt.Sprintf("/media/%s/part-%d.mkv", kind, i+1), Container: "mkv", Resolution: []string{"1080p", "2160p"}[i], CodecVideo: "h264", CodecAudio: "aac", FileSize: int64(1000 + i), Duration: 600,
				VideoTracks: []models.VideoTrack{{Codec: "h264"}}, AudioTracks: []models.AudioTrack{{Language: "en", Default: true}, {Language: "fr"}}, SubtitleTracks: []models.SubtitleTrack{{Language: "en", Codec: "srt"}}, Chapters: []models.MediaChapter{{Index: 0, Title: "Opening", StartSeconds: 0, EndSeconds: 50}}, IntroStart: new(1.0), IntroEnd: new(8.0), CreditsStart: new(550.0), CreditsEnd: new(590.0)}
			if kind == "audiobook" {
				file.PresentationKind = "multipart"
				file.PresentationGroupKey = "book"
				file.PresentationPartIndex = 2 - i
				file.PresentationPartTotal = 2
			}
			f.files.files[id] = append(f.files.files[id], file)
		}
	}
	// A presentation-heavy movie demonstrates the dependency work the endpoint
	// should avoid, without assigning artificial network latency to the resolver.
	for i := range 40 {
		personID := time.Now().UnixNano()
		if err := pool.QueryRow(t.Context(), `INSERT INTO people (id,name,photo_path) VALUES ($1,$2,$3) RETURNING id`, personID, fmt.Sprintf("%sperson-%d", prefix, i), fmt.Sprintf("tmdb/people/%d/original.jpg", i)).Scan(&personID); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO item_people (id,person_id,content_id,kind,sort_order) VALUES ($1,$1,$2,$3,$4)`, personID, f.ids["movie"], int16(models.PersonKindActor), i)
	}
	f.svc = NewDetailService(NewItemRepository(pool), NewEpisodeRepository(pool), NewSeasonRepository(pool), NewPersonRepository(pool), f.files)
	f.svc.SetImageResolver(f.images)
	return f
}

func TestGetItemVersionsMatchesDetail(t *testing.T) {
	f := newVersionsFixture(t)
	store := newDetailTestStore(t)
	setProfileAudioLanguage(t, store, "en")
	setScopedAudioLanguageForDevice(t, store, settingscontract.ScopeProfileDevice, "", 0, "versions-test-device", "fr")
	setScopedAudioLanguage(t, store, settingscontract.ScopeProfileSeries, f.ids["series"], 0, "fr")
	f.svc.SetUserStoreProvider(testDetailUserStoreProvider{store: store})
	for _, kind := range []string{"movie", "audiobook", "ebook", "manga", "series", "season", "episode", "extra"} {
		for _, restricted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restricted=%t", kind, restricted), func(t *testing.T) {
				filter := AccessFilter{UserID: 1, ProfileID: "profile-1", DeviceID: "versions-test-device", SelectedFileID: 2, ProfilePreferredLanguage: "fr"}
				if restricted {
					filter.AllowedLibraryIDs = []int{f.library}
					filter.PresentationLibraryID = new(f.library)
					filter.MaxContentRating = "PG"
					filter.MaxPlaybackQuality = "1080p"
				}
				detail, err := f.svc.GetItemDetail(t.Context(), f.ids[kind], filter)
				if err != nil {
					t.Fatal(err)
				}
				versions, err := f.svc.GetItemVersions(t.Context(), f.ids[kind], filter)
				if err != nil {
					t.Fatal(err)
				}
				want, _ := json.Marshal(detail.Versions)
				got, _ := json.Marshal(versions)
				if string(got) != string(want) {
					t.Fatalf("version JSON differs:\ngot %s\nwant %s", got, want)
				}
				if kind == "series" || kind == "season" {
					if string(got) != "[]" {
						t.Fatalf("want empty JSON array, got %s", got)
					}
				} else if len(versions) == 0 {
					t.Fatal("expected playable versions")
				}
				if kind == "episode" && *versions[0].EffectiveAudioTrackIndex != 1 {
					t.Fatalf("episode must inherit series French audio preference: %+v", versions[0])
				}
			})
		}
	}
}

func TestGetItemVersionsAccessAndMissing(t *testing.T) {
	f := newVersionsFixture(t)
	for _, kind := range []string{"movie", "series", "season", "episode", "extra"} {
		for name, filter := range map[string]AccessFilter{
			"empty allowlist": {AllowedLibraryIDs: []int{}}, "other library": {AllowedLibraryIDs: []int{f.library + 1}}, "disabled library": {DisabledLibraryIDs: []int{f.library}}, "rating": {MaxContentRating: "G"}, "wrong presentation library": {PresentationLibraryID: new(f.library + 1)},
		} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				_, oldErr := f.svc.GetItemDetail(t.Context(), f.ids[kind], filter)
				_, err := f.svc.GetItemVersions(t.Context(), f.ids[kind], filter)
				if !errors.Is(oldErr, ErrItemNotFound) || !errors.Is(err, ErrItemNotFound) {
					t.Fatalf("detail=%v versions=%v; want not found", oldErr, err)
				}
			})
		}
	}
	if _, err := f.svc.GetItemVersions(t.Context(), "missing-versions-item", AccessFilter{}); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("missing target: %v", err)
	}
	sentinel := errors.New("file repository unavailable")
	f.files.err = sentinel
	for _, kind := range []string{"movie", "episode", "extra"} {
		if _, err := f.svc.GetItemVersions(t.Context(), f.ids[kind], AccessFilter{}); !errors.Is(err, sentinel) {
			t.Fatalf("%s: lost file error: %v", kind, err)
		}
	}
}

func TestGetItemVersionsSkipsPresentationAndPlaybackSafety(t *testing.T) {
	f := newVersionsFixture(t)
	ensurer := &recordingProbeEnsurer{}
	racer := &recordingCopySafetyRacer{}
	f.svc.probeEnsurer = ensurer
	f.svc.copySafetyRacer = racer
	for _, kind := range []string{"movie", "audiobook", "ebook", "series", "season", "episode", "extra"} {
		filter := AccessFilter{ProfilePreferredLanguage: "fr"}
		f.queries.calls.Store(0)
		f.images.calls.Store(0)
		if _, err := f.svc.GetItemDetail(t.Context(), f.ids[kind], filter); err != nil {
			t.Fatal(err)
		}
		oldQueries, oldImages := f.queries.calls.Load(), f.images.calls.Load()
		f.queries.calls.Store(0)
		f.images.calls.Store(0)
		ensurer.probeCalls = nil
		if _, err := f.svc.GetItemVersions(t.Context(), f.ids[kind], filter); err != nil {
			t.Fatal(err)
		}
		queries, images := f.queries.calls.Load(), f.images.calls.Load()
		t.Logf("%s: SQL %d -> %d; image resolver calls %d -> %d (file fetcher in memory)", kind, oldQueries, queries, oldImages, images)
		if queries >= oldQueries {
			t.Errorf("%s did not eliminate presentation queries", kind)
		}
		if images != 0 {
			t.Errorf("%s resolved unreturned presentation images", kind)
		}
		if len(ensurer.probeCalls) != len(f.files.files[f.ids[kind]]) {
			t.Errorf("%s skipped browse probe repair", kind)
		}
	}
	if len(ensurer.cachedCalls) != 0 || len(racer.raced) != 0 {
		t.Fatal("versions triggered playback safety work")
	}
	// A broken localization dependency must no longer prevent version selection.
	cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	closed, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	f.svc.itemLocRepo = NewMediaItemLocalizationRepository(closed)
	filter := AccessFilter{ProfilePreferredLanguage: "fr"}
	if _, err := f.svc.GetItemDetail(t.Context(), f.ids["movie"], filter); err == nil {
		t.Fatal("full detail should fail with unavailable localization")
	}
	if _, err := f.svc.GetItemVersions(t.Context(), f.ids["movie"], filter); err != nil {
		t.Fatalf("versions depend on localization: %v", err)
	}
}

func TestGetItemVersionsKeepsChapterImagesAndEmptyFiles(t *testing.T) {
	f := newVersionsFixture(t)
	file := f.files.files[f.ids["movie"]][0]
	file.Chapters[0].ThumbnailPath = "chapters/movie/original.jpg"
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/media/movie.fr.srt", Language: "fr", Format: "srt", Forced: true}}
	detail, err := f.svc.GetItemDetail(t.Context(), f.ids["movie"], AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	f.images.calls.Store(0)
	versions, err := f.svc.GetItemVersions(t.Context(), f.ids["movie"], AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(detail.Versions)
	got, _ := json.Marshal(versions)
	if string(want) != string(got) {
		t.Fatal("chapter/subtitle metadata differs")
	}
	if f.images.calls.Load() != 1 {
		t.Fatal("versions must still resolve the returned chapter thumbnail")
	}
	f.files.files[f.ids["movie"]] = nil
	versions, err = f.svc.GetItemVersions(t.Context(), f.ids["movie"], AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	empty, _ := json.Marshal(versions)
	if string(empty) != "[]" {
		t.Fatalf("no files: want [], got %s", empty)
	}
	// Optional repositories and extra-file support retain their existing fallback.
	f.svc.fileFetcher = &batchEquivFileFetcher{}
	if _, err := f.svc.GetItemVersions(t.Context(), f.ids["extra"], AccessFilter{}); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("unsupported extra fetcher: %v", err)
	}
	f.svc.seasonRepo = nil
	if _, err := f.svc.GetItemVersions(t.Context(), f.ids["episode"], AccessFilter{}); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("missing season repository: %v", err)
	}
}

func BenchmarkGetItemVersions(b *testing.B) {
	f := newVersionsFixture(b)
	for _, kind := range []string{"movie", "audiobook", "episode"} {
		for _, full := range []bool{true, false} {
			b.Run(fmt.Sprintf("%s/full_detail=%t", kind, full), func(b *testing.B) {
				filter := AccessFilter{ProfilePreferredLanguage: "fr"}
				b.ReportAllocs()
				f.queries.calls.Store(0)
				f.images.calls.Store(0)
				for b.Loop() {
					var err error
					if full {
						_, err = f.svc.GetItemDetail(b.Context(), f.ids[kind], filter)
					} else {
						_, err = f.svc.GetItemVersions(b.Context(), f.ids[kind], filter)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(f.queries.calls.Load())/float64(b.N), "SQL/op")
				b.ReportMetric(float64(f.images.calls.Load())/float64(b.N), "image-calls/op")
			})
		}
	}
}
