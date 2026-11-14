package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// movingEpisodeRepo runs move once, between the episode linker listing a
// file and writing its link, as a scan landing in that window would.
type movingEpisodeRepo struct {
	metadataEpisodeRepo
	move func(ctx context.Context) error
}

func (r *movingEpisodeRepo) GetBySeriesAndNumber(ctx context.Context, seriesID string, season, episode int) (*models.Episode, error) {
	if r.move != nil {
		move := r.move
		r.move = nil
		if err := move(ctx); err != nil {
			return nil, err
		}
	}
	return r.metadataEpisodeRepo.GetBySeriesAndNumber(ctx, seriesID, season, episode)
}

// TestEpisodeLinkFollowsSameTitleMoveDB checks the episode linker when a scan
// moves a listed file before its link is written. A move within the title
// keeps the series link and queues no rematch, so the linker links the file
// at its new path; a move to another title cleared the links and stays
// unlinked for its rematch.
func TestEpisodeLinkFollowsSameTitleMoveDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	cases := []struct {
		name string
		// move is the scan's update, taking the file to to (relative to the
		// library root); wantLinked is whether it must end linked.
		move       string
		to         string
		wantLinked bool
	}{
		{"same-title move is linked at its new path",
			`UPDATE media_files SET file_path = $2 WHERE id = $1`,
			"Show/Season 1/Show S01E01 1080p.mkv", true},
		{"retitled file is left for its rematch",
			`UPDATE media_files SET file_path = $2, content_id = NULL WHERE id = $1`,
			"Other Show/Season 1/Other Show S01E01.mkv", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			var folderID int
			if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`,
				fmt.Sprintf("episode-link-move-%d", time.Now().UnixNano())).Scan(&folderID); err != nil {
				t.Fatal(err)
			}
			seriesID := fmt.Sprintf("series-tvdb-move-%d", folderID)
			episodeID := seriesID + "-1-1"
			t.Cleanup(func() {
				cleanup := context.Background()
				_, _ = pool.Exec(cleanup, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
				_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
				_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folderID)
			})
			if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status) VALUES ($1, 'series', 'Show', 'matched')`, seriesID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, 1, 'Pilot')`, episodeID, seriesID); err != nil {
				t.Fatal(err)
			}
			root := fmt.Sprintf("/library-%d/", folderID)
			files := scanner.NewFileRepository(pool)
			// No stored numbers, so the bulk link passes over the file and
			// the per-file linker, which parses its path, links it.
			file, err := files.Upsert(ctx, models.MediaFile{
				MediaFolderID: folderID,
				ContentID:     seriesID,
				FilePath:      root + "Show/Season 1/Show S01E01.mkv",
				FileSize:      1024,
				BaseTitle:     "Show",
				BaseType:      "series",
			})
			if err != nil {
				t.Fatal(err)
			}
			s := NewMetadataService(nil, nil, nil, catalog.NewItemRepository(pool), catalog.NewProviderIDRepository(pool),
				catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
				catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
				nil, files, NewSkippedRootRepository(pool), nil, catalog.NewRootClaimRepository(pool))
			moved := false
			s.episodeRepo = &movingEpisodeRepo{metadataEpisodeRepo: s.episodeRepo, move: func(ctx context.Context) error {
				moved = true
				_, err := pool.Exec(ctx, tc.move, file.ID, root+tc.to)
				return err
			}}

			if err := s.linkSeriesFilesToEpisodesWithOptions(ctx, seriesID, false); err != nil {
				t.Fatal(err)
			}
			if !moved {
				t.Fatal("the linker never looked up the file's episode")
			}
			after, err := files.GetByID(ctx, file.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := after.EpisodeID == episodeID; got != tc.wantLinked {
				t.Fatalf("file at %q linked to episode %q; want linked = %v", after.FilePath, after.EpisodeID, tc.wantLinked)
			}
			if !tc.wantLinked && after.EpisodeID != "" {
				t.Fatalf("retitled file took episode %q", after.EpisodeID)
			}
		})
	}
}
