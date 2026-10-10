package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/overlays"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run against the complete migrated schema, including file ownership triggers.
// The catalog's scanner projections and sections historically broke ties in
// different orders; preserve both while returning just one file per card.
func TestCatalogOverlaySummariesPreserveLegacyWinners(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	trace := &playableTargetQueryTrace{}
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := fmt.Sprintf("overlay-catalog-%d", time.Now().UnixNano())
	series, movie, unicodeMovie := prefix+"-series", prefix+"-movie", prefix+"-unicode-movie"
	epZ, epA := prefix+"-z", prefix+"-a"
	var folder, deniedFolder int
	for name, id := range map[string]*int{"allowed": &folder, "denied": &deniedFolder} {
		if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type, name, enabled)
			VALUES ('mixed', $1, TRUE) RETURNING id`, prefix+name).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{series, movie, unicodeMovie})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{folder, deniedFolder})
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'Overlay Series', 'matched', '{}'), ($2, 'movie', 'Overlay Movie', 'matched', '{}'),
		($3, 'movie', 'Unicode HDR Movie', 'matched', '{}')`, series, movie, unicodeMovie); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $3, 1, 1, 'First'), ($2, $3, 1, 2, 'Second')`, epZ, epA, series); err != nil {
		t.Fatal(err)
	}
	files := []*models.MediaFile{
		{ContentID: series, EpisodeID: epZ, MediaFolderID: folder, Resolution: "1080p", CodecAudio: "aac"},
		{ContentID: series, EpisodeID: epA, MediaFolderID: folder, Resolution: "1080p", CodecAudio: "flac"},
		{ContentID: series, EpisodeID: epZ, MediaFolderID: deniedFolder, Resolution: "2160p", CodecAudio: "ac3"},
		{ContentID: movie, MediaFolderID: folder, Resolution: "\u00a02160p\u00a0", CodecAudio: "aac"},
		{ContentID: movie, MediaFolderID: folder, Resolution: "1080p", CodecAudio: "ac3"},
		{ContentID: unicodeMovie, MediaFolderID: folder, Resolution: "1080p", CodecAudio: "aac", VideoTracks: []models.VideoTrack{{VideoRangeType: "\u00a0HDR10\u00a0"}}},
		{ContentID: unicodeMovie, MediaFolderID: folder, Resolution: "1080p", CodecAudio: "flac", HDR: true},
	}
	for i, file := range files {
		file.FilePath = fmt.Sprintf("%s-file-%d.mkv", prefix, i)
		tracks := []byte("[]")
		if len(file.VideoTracks) > 0 {
			tracks, err = json.Marshal(file.VideoTracks)
			if err != nil {
				t.Fatal(err)
			}
		}
		var hdr any // Nullable probe values must behave like Go's zero value.
		if file.HDR {
			hdr = true
		}
		if err := pool.QueryRow(t.Context(), `INSERT INTO media_files
			(content_id, episode_id, media_folder_id, file_path, resolution, codec_audio, hdr, video_tracks)
			VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8) RETURNING id`,
			file.ContentID, file.EpisodeID, file.MediaFolderID, file.FilePath, file.Resolution, file.CodecAudio, hdr, tracks).Scan(&file.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Wide metadata on losing files must stay inside the database.
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_files
		(content_id, episode_id, media_folder_id, file_path, resolution, audio_tracks)
		SELECT $1, $2, $3, $4 || '-loser-' || n || '.mkv', '720p', jsonb_build_array(jsonb_build_object('codec', 'aac', 'title', repeat('track ', 200)))
		FROM generate_series(1, 1500) n`, series, epZ, folder, prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_files
		(content_id, media_folder_id, file_path, resolution, missing_since)
		VALUES ($1, $2, $3, '4320p', now())`, movie, folder, prefix+"-missing.mkv"); err != nil {
		t.Fatal(err)
	}
	ids := []string{series, movie, unicodeMovie, epZ, epA, prefix + "-absent", series}
	for _, filter := range []AccessFilter{{}, {AllowedLibraryIDs: []int{}}, {AllowedLibraryIDs: []int{folder}},
		{DisabledLibraryIDs: []int{deniedFolder}}, {AllowedLibraryIDs: []int{folder, deniedFolder}, DisabledLibraryIDs: []int{folder}},
		{MaxPlaybackQuality: "1080p"}, {MaxPlaybackQuality: "4k"}} {
		trace.rows.Store(0)
		got, err := NewItemRepository(pool).ListOverlaySummaries(t.Context(), ids, filter)
		if err != nil {
			t.Fatal(err)
		}
		if rows := trace.rows.Load(); rows > 5 {
			t.Fatalf("returned %d rows for five existing cards", rows)
		}
		for _, id := range ids {
			var candidates []*models.MediaFile
			for _, file := range files {
				if (file.ContentID == id || file.EpisodeID == id) && FileAllowedByAccess(file, filter) {
					candidates = append(candidates, file)
				}
			}
			want := overlays.BuildSummary(candidates) // Files were inserted in ID order.
			if !reflect.DeepEqual(got[id], want) {
				t.Fatalf("catalog filter %+v, %s: got %+v, legacy summary %+v", filter, id, got[id], want)
			}
			slices.SortFunc(candidates, func(a, b *models.MediaFile) int {
				if n := strings.Compare(a.ContentID, b.ContentID); n != 0 {
					return n
				}
				if n := strings.Compare(a.EpisodeID, b.EpisodeID); n != 0 {
					return n
				}
				return a.ID - b.ID
			})
			sections, err := ListOverlaySummaries(t.Context(), pool, []string{id}, filter)
			if err != nil {
				t.Fatal(err)
			}
			if want := overlays.BuildSummary(candidates); !reflect.DeepEqual(sections[id], want) {
				t.Fatalf("sections filter %+v, %s: got %+v, legacy summary %+v", filter, id, sections[id], want)
			}
		}
	}
}
