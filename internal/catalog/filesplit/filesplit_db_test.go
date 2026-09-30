package filesplit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type moveFixture struct {
	pool   *pgxpool.Pool
	suffix string
}

func newMoveFixture(t *testing.T) *moveFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return &moveFixture{pool: pool, suffix: fmt.Sprintf("-%d", time.Now().UnixNano())}
}

func (fx *moveFixture) folder(t *testing.T, folderType string) int {
	t.Helper()
	var id int
	if err := fx.pool.QueryRow(t.Context(),
		`INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`,
		folderType, "filesplit"+fx.suffix,
	).Scan(&id); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, id)
	})
	return id
}

// item seeds a non-provider-anchored item, so Move takes the path used for
// unmatched targets.
func (fx *moveFixture) item(t *testing.T, name, itemType string, folderIDs ...int) string {
	t.Helper()
	id := "fs-" + name + fx.suffix
	if _, err := fx.pool.Exec(t.Context(), `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path)
		VALUES ($1, $2, $1, 'unmatched', '{}'::text[], '', '', '')
	`, id, itemType); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, id)
	})
	for _, folderID := range folderIDs {
		if _, err := fx.pool.Exec(t.Context(),
			`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, folderID,
		); err != nil {
			t.Fatalf("seed item membership: %v", err)
		}
	}
	return id
}

func (fx *moveFixture) episode(t *testing.T, seriesID string, number, folderID int) string {
	t.Helper()
	id := fmt.Sprintf("%s-e%d", seriesID, number)
	if _, err := fx.pool.Exec(t.Context(), `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, still_path)
		VALUES ($1, $2, 1, $3, 'Episode', '')
	`, id, seriesID, number); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	if _, err := fx.pool.Exec(t.Context(), `
		INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at) VALUES ($1, $2, NOW())
	`, id, folderID); err != nil {
		t.Fatalf("seed episode membership: %v", err)
	}
	return id
}

func (fx *moveFixture) file(t *testing.T, folderID int, contentID, episodeID string, episodeNumber int) File {
	t.Helper()
	return fx.fileAt(t, fmt.Sprintf("/filesplit%s/%s", fx.suffix, contentID), folderID, contentID, episodeID, episodeNumber)
}

func (fx *moveFixture) fileAt(t *testing.T, root string, folderID int, contentID, episodeID string, episodeNumber int) File {
	t.Helper()
	path := fmt.Sprintf("%s/%d-%d.mkv", root, folderID, time.Now().UnixNano())
	var episode any
	if episodeID != "" {
		episode = episodeID
	}
	var id int
	if err := fx.pool.QueryRow(t.Context(), `
		INSERT INTO media_files (media_folder_id, file_path, file_size, content_id, episode_id, season_number, episode_number,
		                         canonical_root_path, observed_root_path)
		VALUES ($1, $2, 1024, $3, $4, 1, $5, $6, $6)
		RETURNING id
	`, folderID, path, contentID, episode, episodeNumber, root).Scan(&id); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	return File{
		ID:                id,
		ContentID:         contentID,
		MediaFolderID:     folderID,
		FilePath:          path,
		CanonicalRootPath: root,
		ObservedRootPath:  root,
		SeasonNumber:      1,
		EpisodeNumber:     episodeNumber,
		EpisodeID:         episodeID,
	}
}

func (fx *moveFixture) move(t *testing.T, from, to, itemType string, files ...File) {
	t.Helper()
	tx, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := Move(t.Context(), tx, Options{
		FromContentID: from,
		ToContentID:   to,
		ItemType:      itemType,
		Files:         files,
	}); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func (fx *moveFixture) exists(t *testing.T, query string, args ...any) bool {
	t.Helper()
	var ok bool
	if err := fx.pool.QueryRow(t.Context(), `SELECT EXISTS(`+query+`)`, args...).Scan(&ok); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return ok
}

// A subtree rescan only revisits content its files link to, so Move must drop
// the source's membership in a folder that no longer holds any of its files.
func TestMoveRemovesSourceMembershipOnlyFromEmptiedFolders(t *testing.T) {
	fx := newMoveFixture(t)
	emptied, kept := fx.folder(t, "movies"), fx.folder(t, "movies")
	source := fx.item(t, "source", "movie", emptied, kept)
	target := fx.item(t, "target", "movie")
	moved := fx.file(t, emptied, source, "", 0)
	fx.file(t, kept, source, "", 0)

	fx.move(t, source, target, "movie", moved)

	membership := `SELECT 1 FROM media_item_libraries WHERE content_id = $1 AND media_folder_id = $2`
	if fx.exists(t, membership, source, emptied) {
		t.Fatal("source kept its membership in the folder all its files left")
	}
	if !fx.exists(t, membership, source, kept) {
		t.Fatal("source lost its membership in a folder that still holds its file")
	}
	if !fx.exists(t, membership, target, emptied) {
		t.Fatal("target did not gain the moved file's folder membership")
	}
}

// Moving episodes to an unmatched target used to leave the source episodes'
// memberships behind, because only provider-anchored targets removed them.
func TestMoveRemovesSourceEpisodeMembershipsForUnmatchedTarget(t *testing.T) {
	fx := newMoveFixture(t)
	folder := fx.folder(t, "series")
	source := fx.item(t, "source-series", "series", folder)
	target := fx.item(t, "target-series", "series")
	onlyVersion := fx.episode(t, source, 1, folder)
	sharedVersion := fx.episode(t, source, 2, folder)
	movedOnly := fx.file(t, folder, source, onlyVersion, 1)
	movedShared := fx.file(t, folder, source, sharedVersion, 2)
	fx.file(t, folder, source, sharedVersion, 2)

	fx.move(t, source, target, "series", movedOnly, movedShared)

	membership := `SELECT 1 FROM episode_libraries WHERE episode_id = $1 AND media_folder_id = $2`
	if fx.exists(t, membership, onlyVersion, folder) {
		t.Fatal("source episode kept its membership after its only file moved")
	}
	if !fx.exists(t, membership, sharedVersion, folder) {
		t.Fatal("source episode lost its membership while another version remains")
	}
	if !fx.exists(t, `SELECT 1 FROM media_item_libraries WHERE content_id = $1 AND media_folder_id = $2`, source, folder) {
		t.Fatal("source series lost its membership while one of its files remains")
	}
}

// Root claims keep their first owner. A root the split emptied must be handed
// to the target, or new files there would resolve back to the source.
func TestMoveHandsEmptiedRootClaimsToTarget(t *testing.T) {
	fx := newMoveFixture(t)
	folder := fx.folder(t, "movies")
	source := fx.item(t, "claim-source", "movie", folder)
	target := fx.item(t, "claim-target", "movie")
	emptiedRoot := "/filesplit" + fx.suffix + "/emptied"
	sharedRoot := "/filesplit" + fx.suffix + "/shared"
	for _, root := range []string{emptiedRoot, sharedRoot} {
		if _, err := fx.pool.Exec(t.Context(),
			`INSERT INTO media_item_roots (media_folder_id, canonical_root_path, content_id) VALUES ($1, $2, $3)`,
			folder, root, source,
		); err != nil {
			t.Fatalf("seed root claim: %v", err)
		}
	}
	emptied := fx.fileAt(t, emptiedRoot, folder, source, "", 0)
	sharedMoved := fx.fileAt(t, sharedRoot, folder, source, "", 0)
	fx.fileAt(t, sharedRoot, folder, source, "", 0)

	fx.move(t, source, target, "movie", emptied, sharedMoved)

	claim := `SELECT 1 FROM media_item_roots WHERE media_folder_id = $1 AND canonical_root_path = $2 AND content_id = $3`
	if !fx.exists(t, claim, folder, emptiedRoot, target) {
		t.Fatal("target did not take over the claim on the root the split emptied")
	}
	if !fx.exists(t, claim, folder, sharedRoot, source) {
		t.Fatal("source lost its claim on a root that still holds its file")
	}
}
