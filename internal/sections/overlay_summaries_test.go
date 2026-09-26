package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/overlays"
)

type overlayQueryTrace struct{ rows atomic.Int64 }

func (q *overlayQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (q *overlayQueryTrace) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.CommandTag.Select() {
		q.rows.Store(data.CommandTag.RowsAffected())
	}
}

func overlayTestPool(t testing.TB) (*pgxpool.Pool, *overlayQueryTrace) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// All reads and committed writes share the same temporary table. Persistent
	// application tables are never changed, even when using an existing test DB.
	cfg.MaxConns = 1
	trace := &overlayQueryTrace{}
	cfg.ConnConfig.Tracer = trace
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `CREATE TEMP TABLE media_files (
   id integer PRIMARY KEY, content_id text, episode_id text, media_folder_id integer NOT NULL DEFAULT 1,
   file_path text NOT NULL DEFAULT '', resolution text, codec_audio text,
   audio_tracks jsonb, hdr boolean NOT NULL DEFAULT false, video_tracks jsonb,
   codec_video text, audio_channels integer, container text,
   subtitle_tracks jsonb, external_subtitles jsonb, edition_key text, missing_since timestamptz
  );
  CREATE INDEX ON media_files(content_id);
  CREATE INDEX ON media_files(episode_id) WHERE episode_id IS NOT NULL;`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, trace
}

func seedOverlayFiles(t testing.TB, pool *pgxpool.Pool, files ...*models.MediaFile) {
	t.Helper()
	for _, file := range files {
		tracks, err := json.Marshal(file.VideoTracks)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(t.Context(), `INSERT INTO media_files
   (id, content_id, episode_id, media_folder_id, resolution, hdr, video_tracks, codec_audio)
   VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8)`,
			file.ID, file.ContentID, file.EpisodeID, file.MediaFolderID, file.Resolution, file.HDR, tracks, file.CodecAudio)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestOverlaySummariesAccessAndGrouping(t *testing.T) {
	pool, trace := overlayTestPool(t)
	files := []*models.MediaFile{
		{ID: 1, ContentID: "series", EpisodeID: "episode-a", MediaFolderID: 1, Resolution: "1080p", CodecAudio: "aac"},
		{ID: 2, ContentID: "series", EpisodeID: "episode-a", MediaFolderID: 2, Resolution: "2160p", CodecAudio: "ac3"},
		{ID: 3, ContentID: "series", EpisodeID: "episode-b", MediaFolderID: 1, Resolution: "720p"},
		{ID: 4, ContentID: "movie", MediaFolderID: 1, Resolution: "\u00a02160p\u00a0"},
		// Same resolution, different audio: ensure episode ID takes precedence over
		// file ID in the legacy tie order.
		{ID: 5, ContentID: "series", EpisodeID: "episode-0", MediaFolderID: 1, Resolution: "1080p", CodecAudio: "flac"},
	}
	seedOverlayFiles(t, pool, files...)
	slices.SortFunc(files, func(a, b *models.MediaFile) int {
		if n := strings.Compare(a.ContentID, b.ContentID); n != 0 {
			return n
		}
		if n := strings.Compare(a.EpisodeID, b.EpisodeID); n != 0 {
			return n
		}
		return a.ID - b.ID
	})
	fetcher := &Fetcher{pool: pool}
	ids := []string{"series", "episode-a", "episode-b", "movie", "absent", "series"}
	for _, filter := range []catalog.AccessFilter{
		{}, {AllowedLibraryIDs: []int{}}, {AllowedLibraryIDs: []int{1}},
		{AllowedLibraryIDs: []int{2}}, {DisabledLibraryIDs: []int{2}},
		{AllowedLibraryIDs: []int{1, 2}, DisabledLibraryIDs: []int{1}},
		{MaxPlaybackQuality: "1080p"}, {MaxPlaybackQuality: "4k"},
	} {
		got, err := fetcher.ListOverlaySummaries(t.Context(), ids, filter)
		if err != nil {
			t.Fatal(err)
		}
		if rows := trace.rows.Load(); rows > 4 {
			t.Fatalf("returned %d rows for four existing cards", rows)
		}
		want := map[string]*models.OverlaySummary{}
		for _, id := range ids {
			var candidates []*models.MediaFile
			for _, file := range files {
				if (file.ContentID == id || file.EpisodeID == id) && catalog.FileAllowedByAccess(file, filter) {
					candidates = append(candidates, file)
				}
			}
			if summary := overlays.BuildSummary(candidates); summary != nil {
				want[id] = summary
			}
			alone, err := fetcher.ListOverlaySummaries(t.Context(), []string{id}, filter)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(alone[id], got[id]) {
				t.Fatalf("%s badge depends on page composition", id)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("filter %+v: got %+v, want %+v", filter, got, want)
		}
	}
}

func TestOverlaySummariesReadCommittedChanges(t *testing.T) {
	pool, _ := overlayTestPool(t)
	fetchers := []*Fetcher{{pool: pool}, {pool: pool}}
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{1}}
	ids := []string{"series", "episode"}
	check := func(want string) {
		t.Helper()
		for _, fetcher := range fetchers {
			got, err := fetcher.ListOverlaySummaries(t.Context(), ids, filter)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				if want == "" {
					if got[id] != nil {
						t.Fatalf("%s: stale summary %+v", id, got[id])
					}
				} else if got[id] == nil || got[id].Resolution != want {
					t.Fatalf("%s: got %+v, want resolution %s", id, got[id], want)
				}
			}
		}
	}
	check("") // A cached negative result must not hide the next insert.
	seedOverlayFiles(t, pool, &models.MediaFile{ID: 1, ContentID: "series", EpisodeID: "episode", MediaFolderID: 1, Resolution: "1080p"})
	check("1080p")
	for _, step := range []struct{ sql, want string }{
		{`UPDATE media_files SET resolution = '2160p'`, "2160p"},
		{`UPDATE media_files SET missing_since = now()`, ""},
		{`UPDATE media_files SET missing_since = NULL`, "2160p"},
		{`UPDATE media_files SET media_folder_id = 2`, ""},
		{`UPDATE media_files SET media_folder_id = 1`, "2160p"},
		{`UPDATE media_files SET content_id = 'moved', episode_id = 'moved-episode'`, ""},
		{`UPDATE media_files SET content_id = 'series', episode_id = 'episode'`, "2160p"},
		{`DELETE FROM media_files`, ""},
	} {
		if _, err := pool.Exec(t.Context(), step.sql); err != nil {
			t.Fatal(err)
		}
		check(step.want)
	}
}

func TestOverlaySummariesOnlyDecodeWinner(t *testing.T) {
	pool, trace := overlayTestPool(t)
	_, err := pool.Exec(t.Context(), `INSERT INTO media_files(id, content_id, resolution, audio_tracks)
  SELECT n, 'series', '720p', '{}'::jsonb FROM generate_series(1, 8302) n;
  INSERT INTO media_files(id, content_id, resolution) VALUES (8303, 'series', '2160p')`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&Fetcher{pool: pool}).ListOverlaySummaries(t.Context(), []string{"series"}, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got["series"] == nil || got["series"].Resolution != "2160p" {
		t.Fatalf("wrong winner: %+v", got)
	}
	if n := trace.rows.Load(); n != 1 {
		t.Fatalf("read %d rows for one card, want 1", n)
	}
}

// Compare complete summaries to the existing Go implementation instead of
// maintaining a second expected ranking table or exporting ranks just for tests.
func TestOverlaySummariesRankingMatchesGo(t *testing.T) {
	pool, _ := overlayTestPool(t)
	cases := []models.MediaFile{
		{}, {Resolution: "480p"}, {Resolution: "1080p"}, {Resolution: "2160p"},
		{Resolution: "4K"}, {Resolution: "UHD"}, {Resolution: "\t+002160p\n"},
		{Resolution: "\u00a02160p\u00a0"}, {Resolution: "-2160p"}, {Resolution: "hd"},
		{Resolution: "1080p", HDR: true},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{DolbyVision: "profile 8"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{DVProfile: 5}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "DOVIWithHDR10"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: " DOVIWithHLG"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{HDR10Plus: true}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "HDR10Plus"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: " HDR10 "}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "SomethingWithHLG"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{ColorTransfer: "SMPTE2084"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{ColorTransfer: "ARIB-STD-B67"}}},
		{Resolution: "1080p", HDR: true, VideoTracks: []models.VideoTrack{{VideoRangeType: "SDR"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "SDR"}, {VideoRangeType: "HDR10"}}},
	}
	fetcher := &Fetcher{pool: pool}
	for i, candidate := range cases {
		for j, opponent := range cases {
			id := fmt.Sprintf("pair-%d-%d", i, j)
			candidate.ID = 2*(i*len(cases)+j) + 1
			opponent.ID = candidate.ID + 1
			candidate.ContentID = id
			opponent.ContentID = id
			// Make tied winners distinguishable in the returned summary.
			candidate.CodecAudio = "aac"
			opponent.CodecAudio = "flac"
			seedOverlayFiles(t, pool, &candidate, &opponent)
			got, err := fetcher.ListOverlaySummaries(t.Context(), []string{id}, catalog.AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			want := overlays.BuildSummary([]*models.MediaFile{&candidate, &opponent})
			if !reflect.DeepEqual(got[id], want) {
				t.Fatalf("%s: got %+v, want %+v", id, got[id], want)
			}
		}
	}
}

func BenchmarkOverlaySummaries(b *testing.B) {
	pool, _ := overlayTestPool(b)
	// 300 series, 30 episodes each; enough track metadata to exercise the wide
	// projection cost without using a private library or cached badge results.
	_, err := pool.Exec(b.Context(), `INSERT INTO media_files
  (id,content_id,episode_id,resolution,video_tracks,audio_tracks,subtitle_tracks)
  SELECT n,'series-'||((n-1)/30),'episode-'||n,
   CASE WHEN n%5=0 THEN '2160p' ELSE '1080p' END,
   '[{"video_range_type":"HDR10"}]'::jsonb,
   jsonb_build_array(jsonb_build_object('codec','aac','title',repeat('track ',200))),
   jsonb_build_array(jsonb_build_object('language','eng','title',repeat('subtitle ',200)))
  FROM generate_series(1,9000) n;
  ANALYZE media_files;`)
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]string, 300)
	for i := range ids {
		ids[i] = fmt.Sprintf("series-%d", i)
	}
	fetcher := &Fetcher{pool: pool}
	b.ReportAllocs()
	for b.Loop() {
		got, err := fetcher.ListOverlaySummaries(b.Context(), ids, catalog.AccessFilter{})
		if err != nil {
			b.Fatal(err)
		}
		if len(got) != len(ids) {
			b.Fatalf("got %d cards, want %d", len(got), len(ids))
		}
	}
}
