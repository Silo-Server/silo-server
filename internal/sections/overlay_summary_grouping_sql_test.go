package sections

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestOverlaySummariesIndependentOfPageAndCache(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Each connection gets its own temporary fixture; no application schema is
	// needed and no persistent tables are changed.
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `
			CREATE TEMP TABLE media_files (
				id integer PRIMARY KEY, content_id text, episode_id text,
				file_path text NOT NULL DEFAULT '', resolution text, codec_audio text,
				audio_tracks jsonb, hdr boolean NOT NULL DEFAULT false, video_tracks jsonb,
				codec_video text, audio_channels integer, container text,
				subtitle_tracks jsonb, external_subtitles jsonb, edition_key text,
				missing_since timestamptz
			);
			INSERT INTO media_files (id, content_id, episode_id, resolution, missing_since) VALUES
				(1, 'series', 'episode-best', '2160p', NULL),
				(2, 'series', 'episode-other', '1080p', NULL),
				(3, 'series', 'episode-missing', '4320p', now()),
				(4, 'movie', NULL, '720p', NULL);
		`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	ids := []string{"series", "episode-best", "episode-other", "episode-missing", "movie"}
	filter := catalog.AccessFilter{}
	baseline := make(map[string]*models.OverlaySummary)
	for _, id := range ids {
		fetcher := &Fetcher{pool: pool}
		summaries, err := fetcher.ListOverlaySummaries(ctx, []string{id}, filter)
		if err != nil {
			t.Fatal(err)
		}
		if summary := summaries[id]; summary != nil {
			baseline[id] = summary
		}
	}
	if baseline["series"] == nil || baseline["series"].Resolution != "2160p" {
		t.Fatalf("series alone must use its best non-missing episode file: %+v", baseline["series"])
	}

	for _, tc := range []struct {
		name string
		warm []string
	}{
		{name: "cold"},
		{name: "episode cached", warm: []string{"episode-best"}},
		{name: "series cached", warm: []string{"series"}},
		{name: "missing cached", warm: []string{"episode-missing"}},
		{name: "all cached", warm: ids},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := &Fetcher{pool: pool}
			if _, err := fetcher.ListOverlaySummaries(ctx, tc.warm, filter); err != nil {
				t.Fatal(err)
			}
			got, err := fetcher.ListOverlaySummaries(ctx, ids, filter)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, baseline) {
				t.Errorf("combined page differs from individual cards: got %+v, want %+v", got, baseline)
			}
			for _, id := range ids {
				single, rows, err := fetcher.listOverlaySummaries(ctx, []string{id}, filter)
				if err != nil {
					t.Fatal(err)
				}
				if rows != 0 || !reflect.DeepEqual(single[id], baseline[id]) {
					t.Errorf("cached card %s: got %+v (%d rows), want %+v", id, single[id], rows, baseline[id])
				}
			}
		})
	}
}
