package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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
		{Resolution: "9223372036854775807p"}, {Resolution: "-9223372036854775808p"},
		{Resolution: "9223372036854775808p"}, {Resolution: "0000000000000000000000000000001080p"},
		{Resolution: "1080p", HDR: true},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{DolbyVision: "profile 8"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{DVProfile: 5}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "DOVIWithHDR10"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: " DOVIWithHLG"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{HDR10Plus: true}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "HDR10Plus"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: " HDR10 "}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "\u00a0HDR10\u00a0"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "\u1680HLG\u3000"}}},
		{Resolution: "1080p", VideoTracks: []models.VideoTrack{{VideoRangeType: "DOVIWithHDR10\u0085"}}},
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
