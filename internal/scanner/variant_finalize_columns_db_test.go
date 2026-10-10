package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/jackc/pgx/v5/pgxpool"
)

// variantContractMovie is a movies folder with one matched movie, for the
// variant finalization contracts.
type variantContractMovie struct {
	pool     *pgxpool.Pool
	folderID int
	movieID  string
	root     string
	suffix   int64
}

func seedVariantContractMovie(t *testing.T, name string) variantContractMovie {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	fx := variantContractMovie{
		pool:    pool,
		movieID: fmt.Sprintf("variant-finalize-%s-%d", name, suffix),
		root:    fmt.Sprintf("/variant-finalize-%s-%d", name, suffix),
		suffix:  suffix,
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, fx.movieID).Scan(&fx.folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, fx.folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, fx.movieID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'movie', 'Variant Contract', 'matched', '{}'::text[])`, fx.movieID); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	return fx
}

func (fx variantContractMovie) finalize(t *testing.T, scope string) {
	t.Helper()
	folder := &models.MediaFolder{ID: fx.folderID, Type: "movies", Paths: []string{fx.root}}
	scan := &Scanner{fileRepo: NewFileRepository(fx.pool)}
	if err := scan.FinalizeVariantsByPathPrefix(context.Background(), folder, scope); err != nil {
		t.Fatalf("finalize %s: %v", scope, err)
	}
}

// Variant finalization writes the edition, presentation and multi-episode
// columns and nothing else. A file the backlog suppressed while it was
// unmatched keeps match_suppressed_at after the matcher links it, and that
// suppression, the file's probe and chapter data and a missing sibling's
// missing_since all survive a finalization that rewrites its edition.
func TestFinalizeVariantsKeepsMatchSuppressionDB(t *testing.T) {
	fx := seedVariantContractMovie(t, "columns")
	ctx := context.Background()
	pool, folderID, movieID, root, suffix := fx.pool, fx.folderID, fx.movieID, fx.root, fx.suffix

	// The cut's file has an id past the int4 range and no stored edition yet.
	cutID := int(3_000_000_000 + suffix%100_000_000)
	cutPath := root + "/Columns (2010)/Columns.2010.Directors.Cut.1080p.mkv"
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (id, media_folder_id, file_path, file_size, file_hash, video_tracks, chapters, updated_at)
		VALUES ($1, $2, $3, 1024, 'hash-cut',
			'[{"codec": "hevc", "width": 3840}]'::jsonb, '[{"start": 0, "title": "Opening"}]'::jsonb,
			NOW() - interval '1 day')`, cutID, folderID, cutPath); err != nil {
		t.Fatalf("seed cut: %v", err)
	}
	// A missing file of the same movie with a stale edition stays as it is.
	var missingID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, edition_key, edition_source, missing_since, updated_at)
		VALUES ($1, $2, $3, 1024, 'stale', 'heuristic_file', NOW(), NOW() - interval '1 day')
		RETURNING id`, movieID, folderID, root+"/Columns (2010)/Columns.2010.Old.mkv").Scan(&missingID); err != nil {
		t.Fatalf("seed missing file: %v", err)
	}

	repo := NewFileRepository(pool)
	if suppressed, err := repo.SuppressUnmatchedMatchBacklogByFolder(ctx, folderID, RawMatchBacklogGeneric); err != nil || suppressed != 1 {
		t.Fatalf("suppress unmatched backlog = %d, %v; want 1", suppressed, err)
	}
	if err := repo.UpdateContentID(ctx, cutID, movieID); err != nil {
		t.Fatalf("link cut: %v", err)
	}

	type fileRow struct {
		editionKey, chapters, videoTracks, fileHash string
		suppressed, missing                         bool
		updatedAt                                   time.Time
	}
	load := func(id int) fileRow {
		t.Helper()
		var row fileRow
		if err := pool.QueryRow(ctx, `
			SELECT COALESCE(edition_key, ''), COALESCE(chapters::text, ''), COALESCE(video_tracks::text, ''), COALESCE(file_hash, ''),
				match_suppressed_at IS NOT NULL, missing_since IS NOT NULL, updated_at
			FROM media_files WHERE id = $1`, id).Scan(
			&row.editionKey, &row.chapters, &row.videoTracks, &row.fileHash,
			&row.suppressed, &row.missing, &row.updatedAt,
		); err != nil {
			t.Fatalf("load file %d: %v", id, err)
		}
		return row
	}
	cutBefore, missingBefore := load(cutID), load(missingID)
	if !cutBefore.suppressed {
		t.Fatalf("fixture: linking the cut cleared match_suppressed_at")
	}

	fx.finalize(t, root+"/Columns (2010)")

	cut := load(cutID)
	wantEdition := naming.ParseVariantHints(cutPath, "movies", root).EditionKey
	if wantEdition == "" {
		t.Fatalf("fixture: %s parses no edition", cutPath)
	}
	if cut.editionKey != wantEdition {
		t.Fatalf("cut edition = %q, want %q", cut.editionKey, wantEdition)
	}
	if !cut.updatedAt.After(cutBefore.updatedAt) {
		t.Fatalf("cut updated_at not bumped")
	}
	if !cut.suppressed {
		t.Fatalf("finalize cleared match_suppressed_at on the cut")
	}
	if cut.chapters != cutBefore.chapters || cut.videoTracks != cutBefore.videoTracks || cut.fileHash != cutBefore.fileHash {
		t.Fatalf("finalize rewrote the cut's probe columns: before %+v after %+v", cutBefore, cut)
	}
	if got := load(missingID); got != missingBefore {
		t.Fatalf("missing file changed: before %+v after %+v", missingBefore, got)
	}
}

// When a part leaves its group, the parts left behind lose their total even
// when the scope holds none of them. Here disc 2 of a two-disc movie is renamed:
// its old row is missing, the new file is linked to the movie, and a file scan
// of the new path finalizes only that path.
func TestFinalizeVariantsRetotalsPartsLeftByARenameDB(t *testing.T) {
	fx := seedVariantContractMovie(t, "rename")
	ctx := context.Background()
	dir := fx.root + "/Discs (2010)"
	insert := func(path string) int {
		t.Helper()
		var id int
		if err := fx.pool.QueryRow(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
			VALUES ($1, $2, $3, 1024) RETURNING id`, fx.movieID, fx.folderID, path).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
		return id
	}
	partTotal := func(id int) int {
		t.Helper()
		var total int
		if err := fx.pool.QueryRow(ctx, `SELECT COALESCE(presentation_part_total, 0) FROM media_files WHERE id = $1`, id).Scan(&total); err != nil {
			t.Fatalf("load file %d: %v", id, err)
		}
		return total
	}

	discOne := insert(dir + "/Discs.2010.CD1.mkv")
	discTwo := insert(dir + "/Discs.2010.CD2.mkv")
	fx.finalize(t, dir)
	if partTotal(discOne) != 2 || partTotal(discTwo) != 2 {
		t.Fatalf("fixture: disc totals = %d and %d, want 2", partTotal(discOne), partTotal(discTwo))
	}

	if _, err := fx.pool.Exec(ctx, `UPDATE media_files SET missing_since = NOW() WHERE id = $1`, discTwo); err != nil {
		t.Fatalf("mark disc 2 missing: %v", err)
	}
	renamed := dir + "/Discs.2010.1080p.mkv"
	insert(renamed)
	fx.finalize(t, renamed)

	if got := partTotal(discOne); got != 0 {
		t.Fatalf("disc 1 part total = %d after disc 2 left the group, want 0", got)
	}
}

// A stored part found outside the scope can belong to a group whose other
// files are stored without part columns, as after a parser change. The group
// is read in full, so the stored part keeps its total and the others get one.
func TestFinalizeVariantsReadsWholeGroupFoundOutsideTheScopeDB(t *testing.T) {
	fx := seedVariantContractMovie(t, "widen")
	ctx := context.Background()
	dir := fx.root + "/Discs (2010)"
	insert := func(path string) int {
		t.Helper()
		var id int
		if err := fx.pool.QueryRow(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
			VALUES ($1, $2, $3, 1024) RETURNING id`, fx.movieID, fx.folderID, path).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
		return id
	}
	partTotal := func(id int) int {
		t.Helper()
		var total int
		if err := fx.pool.QueryRow(ctx, `SELECT COALESCE(presentation_part_total, 0) FROM media_files WHERE id = $1`, id).Scan(&total); err != nil {
			t.Fatalf("load file %d: %v", id, err)
		}
		return total
	}

	discOne := insert(dir + "/Discs.2010.CD1.mkv")
	discTwo := insert(dir + "/Discs.2010.CD2.mkv")
	fx.finalize(t, dir)
	if partTotal(discOne) != 2 || partTotal(discTwo) != 2 {
		t.Fatalf("fixture: disc totals = %d and %d, want 2", partTotal(discOne), partTotal(discTwo))
	}
	if _, err := fx.pool.Exec(ctx, `
		UPDATE media_files
		SET presentation_kind = '', presentation_group_key = '', presentation_part_index = NULL, presentation_part_total = NULL
		WHERE id = $1`, discTwo); err != nil {
		t.Fatalf("clear disc 2 part columns: %v", err)
	}
	plain := fx.root + "/Discs (2010) Featurette/Discs.2010.1080p.mkv"
	insert(plain)
	fx.finalize(t, plain)

	if got := partTotal(discOne); got != 2 {
		t.Fatalf("disc 1 part total = %d, want 2", got)
	}
	if got := partTotal(discTwo); got != 2 {
		t.Fatalf("disc 2 part total = %d, want 2", got)
	}
}
