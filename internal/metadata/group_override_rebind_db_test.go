package metadata

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// tvdbIDOnlyProvider returns its metadata only when asked for its TVDB ID, the
// way a provider cannot find a show from a stray root's title alone.
type tvdbIDOnlyProvider struct {
	*remoteStubProvider
	tvdbID string
}

func (p tvdbIDOnlyProvider) GetMetadata(ctx context.Context, req MetadataRequest) (*MetadataResult, error) {
	if req.ProviderIDs["tvdb"] != p.tvdbID {
		return &MetadataResult{}, nil
	}
	return p.remoteStubProvider.GetMetadata(ctx, req)
}

// A root that scanned into its own provisional series gets an operator
// override forcing a show's provider ID. Matching the root again must use that
// ID, and move the item's files onto the show when a matched item already owns
// it. The override names a content group, not a root, so it is used only when
// the provisional item holds that whole group and nothing else.
func TestSeriesQueueGroupOverrideRelinksLinkedProvisionalRoot(t *testing.T) {
	const groupKey, showKey = "v1|series|season|0000", "v1|series|example show|2013"
	for _, tc := range []struct {
		name        string
		strayStatus string
		forcedTitle string
		showExists  bool
		// A second root holds one more file, on otherItem ("stray" or "other")
		// and in group otherKey.
		otherItem, otherKey string
		wantMoved           bool
	}{
		{name: "moves the root to the matched show", strayStatus: "unmatched", forcedTitle: "Example Show", showExists: true, wantMoved: true},
		{name: "replaces an ambiguous root's scanned title", strayStatus: "ambiguous", wantMoved: true},
		{name: "moves an item that spans two roots of the group", strayStatus: "unmatched", showExists: true, otherItem: "stray", otherKey: groupKey, wantMoved: true},
		{name: "leaves a group shared with another item alone", strayStatus: "unmatched", showExists: true, otherItem: "other", otherKey: groupKey},
		{name: "leaves an item that holds another group alone", strayStatus: "unmatched", showExists: true, otherItem: "stray", otherKey: "v1|series|other|0000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := chainBuiltinTestPool(t)
			ctx := t.Context()
			nonce := time.Now().UnixNano() % 100_000_000
			tvdbID := fmt.Sprintf("%d", 800_000_000+nonce)
			libraryRoot := fmt.Sprintf("/group-override-rebind-%d/tv", nonce)
			showRoot := libraryRoot + "/Example Show (2013)"
			strayRoot, otherRoot := showRoot+"/Season", libraryRoot+"/Other Show (2015)/Season"
			show := "series-tvdb-" + tvdbID
			stray, other := fmt.Sprintf("local-group-override-stray-%d", nonce), fmt.Sprintf("local-group-override-other-%d", nonce)

			folderID := insertTestFolder(t, pool, "series")
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{show, stray, other})
			})
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := pool.Exec(ctx, query, args...); err != nil {
					t.Fatalf("%s: %v", query, err)
				}
			}
			insertItem := func(contentID, title, status, tvdb string) {
				t.Helper()
				exec(`INSERT INTO media_items (content_id, type, title, status, tvdb_id, genres, poster_path, backdrop_path, logo_path)
					VALUES ($1, 'series', $2, $3, $4, '{}'::text[], '', '', '')`, contentID, title, status, tvdb)
				exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, folderID)
			}
			insertFile := func(root, name, contentID, key, title string, season, episode int) {
				t.Helper()
				exec(`INSERT INTO media_files (content_id, media_folder_id, file_path, observed_root_path, canonical_root_path,
				                               group_key_version, content_group_key, base_type, base_title, season_number, episode_number, file_size)
					VALUES ($1, $2, $3, $4, $4, 1, $5, 'series', $6, $7, $8, 1024)`,
					contentID, folderID, root+"/"+name, root, key, title, season, episode)
			}
			exec(`INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folderID, libraryRoot)
			want := map[string]int{stray: 2}
			if tc.showExists {
				insertItem(show, "Example Show", "matched", tvdbID)
				exec(`INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id) VALUES ($1, 'series', 'tvdb', $2)`, show, tvdbID)
				insertFile(showRoot, "Season 01/Example Show - S01E01.mkv", show, showKey, "Example Show", 1, 1)
				want[show] = 1
			}
			insertItem(stray, "Season", tc.strayStatus, "")
			insertFile(strayRoot, "Example Show - S02E01.mkv", stray, groupKey, "Season", 2, 1)
			insertFile(strayRoot, "Example Show - S02E02.mkv", stray, groupKey, "Season", 2, 2)
			switch tc.otherItem {
			case "stray":
				insertFile(otherRoot, "Example Show - S02E03.mkv", stray, tc.otherKey, "Season", 2, 3)
				want[stray]++
			case "other":
				insertItem(other, "Season", "unmatched", "")
				insertFile(otherRoot, "Other Show - S01E01.mkv", other, tc.otherKey, "Season", 1, 1)
				want[other] = 1
			}
			if tc.wantMoved {
				want[show] += want[stray]
				want[stray] = 0
			}
			exec(`INSERT INTO media_group_overrides (media_folder_id, group_key_version, content_group_key,
			                                         forced_type, forced_title, forced_tvdb_id)
				VALUES ($1, 1, $2, 'series', $3, $4)`, folderID, groupKey, tc.forcedTitle, tvdbID)

			files := scanner.NewFileRepository(pool)
			service := NewMetadataService(nil, nil, nil,
				catalog.NewItemRepository(pool), catalog.NewProviderIDRepository(pool),
				catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
				catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
				nil, files, NewSkippedRootRepository(pool), nil, catalog.NewRootClaimRepository(pool))
			provider := tvdbIDOnlyProvider{tvdbID: tvdbID, remoteStubProvider: &remoteStubProvider{
				slug:     "tvdb",
				metadata: &MetadataResult{HasMetadata: true, Title: "Example Show", Year: 2013, ProviderIDs: map[string]string{"tvdb": tvdbID}},
			}}
			// Process resolves the chain from installed plugins. Hand it a stub
			// chain and the library's language, which Process would otherwise fill in.
			service.hooks.process = func(ctx context.Context, req ProcessRequest) (*ProcessResult, error) {
				req.Language = "en"
				return service.ProcessWithProviders(ctx, req, []Provider{provider})
			}
			worker := NewMatchWorker(service, files, 1, 1, 0)
			roots := []string{strayRoot}
			if tc.otherItem != "" {
				roots = append(roots, otherRoot)
			}
			for _, root := range roots {
				job := models.SeriesRootMatchJob{MediaFolderID: folderID, ObservedRootPath: root, RerunRequested: true}
				worker.SetSeriesRootClaimer(newFakeSeriesQueueRepo(job), true)
				if _, err := worker.processSeriesRoot(ctx, job, &sync.Map{}); err != nil {
					t.Fatalf("processSeriesRoot(%s): %v", root, err)
				}
			}

			for contentID, wantFiles := range want {
				var linked, withoutEpisode int
				if err := pool.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE episode_id IS NULL)
					FROM media_files WHERE media_folder_id = $1 AND content_id = $2`, folderID, contentID).Scan(&linked, &withoutEpisode); err != nil {
					t.Fatalf("count files on %s: %v", contentID, err)
				}
				if linked != wantFiles {
					t.Fatalf("%d files on %s, want %d", linked, contentID, wantFiles)
				}
				if tc.wantMoved && contentID == show && withoutEpisode != 0 {
					t.Fatalf("%d files on the show have no episode", withoutEpisode)
				}
			}
			if !tc.wantMoved {
				return
			}
			var title, status string
			var strayItems int
			if err := pool.QueryRow(ctx, `SELECT title, status, (SELECT COUNT(*) FROM media_items WHERE content_id = $2)
				FROM media_items WHERE content_id = $1`, show, stray).Scan(&title, &status, &strayItems); err != nil {
				t.Fatalf("load show: %v", err)
			}
			if title != "Example Show" || status != "matched" || strayItems != 0 {
				t.Fatalf("show title=%q status=%q, stray items left=%d; want Example Show, matched and 0", title, status, strayItems)
			}
		})
	}
}
