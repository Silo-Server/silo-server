package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// writeRelinkTestMedia writes a file large enough for an OSHash (128 KiB or
// more); seed makes its bytes differ from another file of the same size.
func writeRelinkTestMedia(t *testing.T, path string, seed byte) {
	t.Helper()
	data := make([]byte, 256<<10)
	for i := range data {
		data[i] = byte(i) ^ seed
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// relinkQueue records the metadata work the scanner queues.
type relinkQueue struct {
	mu     sync.Mutex
	movies []int
	roots  []string
}

func (q *relinkQueue) EnqueueMovieFile(_ context.Context, fileID int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.movies = append(q.movies, fileID)
	return nil
}

func (q *relinkQueue) EnqueueSeriesRoot(_ context.Context, _ int, observedRootPath string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.roots = append(q.roots, observedRootPath)
	return nil
}

func (q *relinkQueue) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.movies, q.roots = nil, nil
}

func (q *relinkQueue) queued(file *models.MediaFile) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Contains(q.movies, file.ID) || slices.Contains(q.roots, file.ObservedRootPath)
}

// TestScanKeepsFileRowAcrossRenameDB checks that a renamed or moved file keeps
// its media_files row, and with it the state keyed to the row's id, instead of
// leaving that state on a missing row while the new path starts over. A rename
// that names another title keeps the row too, but sends it back to matching
// and leaves the old title's links on a stand-in row at the old path.
//
// Paths may contain {id}, a provider ID unique to the case, and {other}, the
// next one. The original row is linked to a title stored with {id} for every
// provider (movie-tmdb-{id} or series-tvdb-{id}), or to {other} with linkOther.
func TestScanKeepsFileRowAcrossRenameDB(t *testing.T) {
	pool := newScannerTestPool(t)
	cases := []relinkCase{
		{
			name:        "rename within a folder",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Movie (2020)/Movie (2020) - 1080p.mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			name:              "rename stored under an older group key scheme",
			from:              "Movie (2020)/Movie (2020).mkv",
			to:                "Movie (2020)/Movie (2020) - 1080p.mkv",
			staleGroupVersion: true,
			wantSameRow:       true, wantLinks: true,
		},
		{
			name:        "move across folders",
			from:        "Shelf A/Movie (2020)/Movie (2020).mkv",
			to:          "Shelf B/Movie (2020)/Movie (2020).mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			name:         "move after the old path was marked missing",
			from:         "Movie (2020)/Movie (2020).mkv",
			to:           "Movies/Movie (2020)/Movie (2020).mkv",
			missingFirst: true,
			wantSameRow:  true, wantLinks: true,
		},
		{
			name:        "rename reported by file, new path first",
			from:        "Movie (2020)/Movie.2020.1080p.mkv",
			to:          "Movie (2020)/Movie (2020).mkv",
			byFile:      true,
			wantSameRow: true, wantLinks: true,
		},
		{
			name:        "tmdb tags on a movie folder and file keep its title",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Movie (2020) {tmdb-{id}}/Movie (2020) {tmdb-{id}}.mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			name:        "tvdb tag on a movie folder keeps its title",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Movie (2020) {tvdb-{id}}/Movie (2020).mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			name:        "imdb tag on a movie file keeps its title",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Movie (2020)/Movie (2020) {imdb-tt{id}}.mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			name:        "several providers on a movie folder keep its title",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Movie (2020) {tmdb-{id}} {imdb-tt{id}}/Movie (2020).mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			// Matching reads the folder's tag over the file's, as the scan
			// stores the group's IDs.
			name:        "a folder tag beats a file tag naming another film",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Movie (2020) {tmdb-{id}}/Movie (2020) {tmdb-{other}}.mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			// TMDB decides for a movie, so an unknown TVDB ID cannot move it.
			name:        "movie tags with an unknown tvdb id keep its title",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Movie (2020) {tmdb-{id}} {tvdb-{other}}/Movie (2020).mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			// TVDB decides for a series, so a matching TMDB ID cannot hold it.
			name:        "series tags whose tvdb id names another show rematch",
			folderType:  "series",
			from:        "Show/Season 1/Show S01E01.mkv",
			to:          "Show {tvdb-{other}} {tmdb-{id}}/Season 1/Show S01E01.mkv",
			wantSameRow: true,
		},
		{
			// Matching gives a file under a claimed root to the root's title
			// before it reads any name.
			name:        "a rename into a root claimed for the title keeps it",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Other Film (2019)/Other Film (2019).mkv",
			claimRoot:   "Other Film (2019)",
			wantSameRow: true, wantLinks: true,
		},
		{
			name:        "provider tag naming another title rematches",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Movie (2020) {tmdb-{id}}/Movie (2020) {tmdb-{id}}.mkv",
			linkOther:   true,
			wantSameRow: true,
		},
		{
			name:        "tvdb tag on a series folder keeps its episode",
			folderType:  "series",
			from:        "Show/Season 1/Show S01E01.mkv",
			to:          "Show {tvdb-{id}}/Season 1/Show S01E01.mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			name:        "tmdb and imdb tags on a series folder keep its episode",
			folderType:  "series",
			from:        "Show/Season 1/Show S01E01.mkv",
			to:          "Show {tmdb-{id}} {imdb-tt{id}}/Season 1/Show S01E01.mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			name:        "provider tags reported by file rematch the series episode",
			folderType:  "series",
			from:        "Show/Season 1/Show S01E01.mkv",
			to:          "Show {tvdb-{id}}/Season 1/Show S01E01.mkv",
			linkOther:   true,
			byFile:      true,
			wantSameRow: true,
		},
		{
			name:        "series folder tagged and scanned by subtree keeps its episode",
			folderType:  "series",
			from:        "Show/Season 1/Show S01E01.mkv",
			to:          "Show {tvdb-{id}}/Season 1/Show S01E01.mkv",
			subtree:     "Show {tvdb-{id}}",
			wantSameRow: true, wantLinks: true,
		},
		{
			name:        "movie retitled and scanned by subtree rematches",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Other Film (2019)/Other Film (2019).mkv",
			subtree:     "Other Film (2019)",
			wantSameRow: true,
		},
		{
			name:        "daily episode date correction rematches",
			folderType:  "series",
			from:        "Daily Show/Daily Show - 2026-04-24.mkv",
			to:          "Daily Show/Daily Show - 2026-04-25.mkv",
			wantSameRow: true,
		},
		{
			name:        "episode range change rematches",
			folderType:  "series",
			from:        "Show/Season 1/Show S01E01.mkv",
			to:          "Show/Season 1/Show S01E01-E02.mkv",
			wantSameRow: true,
		},
		{
			name:        "rename to another title rematches",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Other Film (2019)/Other Film (2019).mkv",
			wantSameRow: true,
		},
		{
			name:         "rename to another title after the old path was marked missing",
			from:         "Movie (2020)/Movie (2020).mkv",
			to:           "Other Film (2019)/Other Film (2019).mkv",
			missingFirst: true,
			wantSameRow:  true,
		},
		{
			name:        "reverted retitle gives the row its path back",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Other Film (2019)/Other Film (2019).mkv",
			revert:      true,
			wantSameRow: true,
		},
		{
			name:        "reverted series retitle reported by file gives the row its path back",
			folderType:  "series",
			from:        "Show/Season 1/Show S01E01.mkv",
			to:          "Other Show/Season 1/Other Show S01E01.mkv",
			byFile:      true,
			revert:      true,
			wantSameRow: true,
		},
		{
			name:        "revert that cannot read the old path waits for a later scan",
			from:        "Movie (2020)/Movie (2020).mkv",
			to:          "Other Film (2019)/Other Film (2019).mkv",
			secondRoot:  true,
			revert:      true,
			wantSameRow: true,
		},
		{
			name:         "revert after the trash sweep took the stand-in relinks the row",
			from:         "Movie (2020)/Movie (2020).mkv",
			to:           "Other Film (2019)/Other Film (2019).mkv",
			revert:       true,
			standInSwept: true,
			wantSameRow:  true,
		},
		{
			name:           "same bytes at a stand-in whose row was deleted start over on it",
			from:           "Movie (2020)/Movie (2020).mkv",
			to:             "Other Film (2019)/Other Film (2019).mkv",
			deleteOriginal: true,
			wantSameRow:    true,
		},
		{
			name:        "of two vanished copies the same title keeps its row",
			from:        "Movie (2020)/Movie (2020).mkv",
			twin:        "Second (2019)/Second (2019).mkv",
			to:          "Movie (2020)/Movie (2020) - 1080p.mkv",
			wantSameRow: true, wantLinks: true,
		},
		{
			name: "two vanished copies naming other titles start over",
			from: "Movie (2020)/Movie (2020).mkv",
			twin: "Second (2019)/Second (2019).mkv",
			to:   "Third (2018)/Third (2018).mkv",
		},
		{
			name: "copy keeps the original row in place",
			from: "Movie (2020)/Movie (2020).mkv",
			to:   "Backup/Movie (2020)/Movie (2020).mkv",
			copy: true,
		},
	}
	// Eight digits, so the IDs also make valid IMDb tags.
	base := 10_000_000 + time.Now().UnixNano()%800_000*100
	for i, tc := range cases {
		tc.providerID = strconv.FormatInt(base+int64(i)*3+1, 10)
		t.Run(tc.name, func(t *testing.T) { checkRelinkCase(t, pool, tc) })
	}
}

type relinkCase struct {
	name       string
	folderType string
	from       string
	to         string
	// twin is a second copy of the same bytes, scanned with the original
	// and removed when it moves.
	twin string
	// copy leaves the original in place, so the new path is a second file.
	copy bool
	// missingFirst scans once with the file gone from both paths, so the
	// original row is already marked missing when the new path shows up.
	missingFirst bool
	// byFile delivers each rename as an arr webhook does: a scan of the new
	// path, then of the old one.
	byFile bool
	// subtree scans only this folder, as a monitor event for the new
	// folder does.
	subtree string
	// linkOther links the original row to the title {other} anchors, as a
	// wrong match would.
	linkOther bool
	// staleGroupVersion stores the original row's group key under another
	// scheme version, as rows read in the first scan after a scheme change.
	staleGroupVersion bool
	// revert links the retitled row to another title, as a match would, then
	// renames the file back.
	revert bool
	// standInSwept deletes the stand-in after the reverting scan read it, as
	// a trash sweep running alongside would.
	standInSwept bool
	// deleteOriginal deletes the retitled row, then puts the same bytes back
	// at the stand-in's path.
	deleteOriginal bool
	// claimRoot is a content root claimed for the original row's title.
	claimRoot string
	// secondRoot puts to under a second library root, which the move leaves
	// empty again on a revert.
	secondRoot bool
	// wantSameRow is whether the file at to keeps the original row, and
	// wantLinks whether that row keeps its title links.
	wantSameRow bool
	wantLinks   bool
	providerID  string
}

func (tc relinkCase) otherID() string {
	id, _ := strconv.ParseInt(tc.providerID, 10, 64)
	return strconv.FormatInt(id+1, 10)
}

func (tc relinkCase) path(root, rel string) string {
	rel = strings.ReplaceAll(rel, "{id}", tc.providerID)
	return filepath.Join(root, strings.ReplaceAll(rel, "{other}", tc.otherID()))
}

// seedRelinkItem adds a title stored with providerID for every provider, and
// for a series its first episode.
func seedRelinkItem(t *testing.T, pool *pgxpool.Pool, folderType, providerID string) (contentID, episodeID string) {
	t.Helper()
	ctx := t.Context()
	itemType := "movie"
	contentID = "movie-tmdb-" + providerID
	if folderType == "series" {
		itemType = "series"
		contentID = "series-tvdb-" + providerID
		episodeID = "episode-tvdb-" + providerID + "-1-1"
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, tmdb_id, imdb_id, tvdb_id)
		VALUES ($1, $2, 'Seeded', $3, 'tt' || $3, $3)`, contentID, itemType, providerID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	if episodeID != "" {
		if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number) VALUES ($1, $2, 1, 1)`, episodeID, contentID); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
	}
	return contentID, episodeID
}

// seedRelinkTitle links the file to a title (and, in a series library, an
// episode) and gives it the per-file state a library builds up: a subtitle
// timing correction, trickplay, chapter images, a hand-set intro and a
// detected credits marker with their images, an import edition, and a match
// attempt.
func seedRelinkTitle(t *testing.T, pool *pgxpool.Pool, repo *FileRepository, tc relinkCase, file *models.MediaFile) (contentID, episodeID string) {
	t.Helper()
	ctx := t.Context()
	providerID := tc.providerID
	if tc.linkOther {
		providerID = tc.otherID()
	}
	contentID, episodeID = seedRelinkItem(t, pool, tc.folderType, providerID)
	chapters := fmt.Sprintf(`[{"index":0,"title":"One","start_seconds":0,"end_seconds":60,"source":"embedded","thumbnail_path":"chapter-images/%d/0/w320.webp"}]`, file.ID)
	if _, err := pool.Exec(ctx, `
		UPDATE media_files SET content_id = $2, episode_id = NULLIF($3, ''), duration = 600, chapters = $4::jsonb,
			edition_raw = 'Director''s Cut', edition_key = 'directors-cut', edition_source = 'import',
			match_attempted_at = NOW()
		WHERE id = $1`, file.ID, contentID, episodeID, chapters); err != nil {
		t.Fatalf("seed links: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE media_files SET intro_start = 10, intro_end = 40, intro_markers_source = 'manual',
			credits_start = 500, credits_end = 590, credits_markers_source = 'scanner', markers_source = 'manual'
		WHERE id = $1`, file.ID); err != nil {
		t.Fatalf("seed markers: %v", err)
	}
	if tc.staleGroupVersion {
		if _, err := pool.Exec(ctx, `
			UPDATE media_files
			SET group_key_version = CASE WHEN group_key_version > 1 THEN group_key_version - 1 ELSE group_key_version + 1 END,
				content_group_key = 'legacy-scheme-key'
			WHERE id = $1`, file.ID); err != nil {
			t.Fatalf("seed stale group key: %v", err)
		}
	}
	linked, err := repo.GetByID(ctx, file.ID)
	if err != nil {
		t.Fatal(err)
	}
	segments := models.EffectiveMarkerSegments(linked)
	if len(segments) != 2 {
		t.Fatalf("seeded marker segments = %d, want 2", len(segments))
	}
	images := make([]string, 0, len(segments))
	for i, segment := range segments {
		images = append(images, fmt.Sprintf(`{"identity":%q,"thumbnail_path":"chapter-images/%d/marker-%s-%064d/w320.webp"}`,
			models.MarkerThumbnailIdentity(linked, segment), file.ID, strings.Repeat("a", 64), i))
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET marker_thumbnails = $2::jsonb WHERE id = $1`, file.ID, "["+strings.Join(images, ",")+"]"); err != nil {
		t.Fatalf("seed marker thumbnails: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_file_trickplay (media_file_id, recipe_version) VALUES ($1, 1)`, file.ID); err != nil {
		t.Fatalf("seed trickplay: %v", err)
	}
	// A user's subtitle timing correction stands in for the state keyed to
	// the row's id.
	if _, err := pool.Exec(ctx, `
		INSERT INTO external_subtitle_timings (media_file_id, content_sha256, path, format, timing_offset_ms)
		VALUES ($1, $2, $3, 'srt', 1500)`,
		file.ID, strings.Repeat("a", 64), strings.TrimSuffix(file.FilePath, ".mkv")+".srt",
	); err != nil {
		t.Fatalf("seed subtitle timing: %v", err)
	}
	return contentID, episodeID
}

// relinkArtifacts counts the subtitle timing and trickplay rows of a file.
func relinkArtifacts(t *testing.T, pool *pgxpool.Pool, fileID int) (timings, trickplay int) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `
		SELECT (SELECT count(*) FROM external_subtitle_timings WHERE media_file_id = $1),
		       (SELECT count(*) FROM media_file_trickplay WHERE media_file_id = $1)`, fileID,
	).Scan(&timings, &trickplay); err != nil {
		t.Fatal(err)
	}
	return timings, trickplay
}

func relinkStandInFor(t *testing.T, pool *pgxpool.Pool, fileID int) int {
	t.Helper()
	var standInFor *int
	if err := pool.QueryRow(t.Context(), `SELECT stand_in_for_file_id FROM media_files WHERE id = $1`, fileID).Scan(&standInFor); err != nil {
		t.Fatal(err)
	}
	if standInFor == nil {
		return 0
	}
	return *standInFor
}

func checkRelinkCase(t *testing.T, pool *pgxpool.Pool, tc relinkCase) {
	ctx := t.Context()
	root := t.TempDir()
	if tc.folderType == "" {
		tc.folderType = "movies"
	}
	folderID := seedDeadRootTestFolder(t, pool, tc.folderType, fmt.Sprintf("relink-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: tc.folderType, Paths: []string{root}}
	toRoot := root
	if tc.secondRoot {
		toRoot = t.TempDir()
		folder.Paths = append(folder.Paths, toRoot)
	}
	repo := NewFileRepository(pool)
	s := NewScanner(repo, "", nil, 2, false, time.Hour)
	queue := &relinkQueue{}
	s.SetMetadataQueueProducer(queue)

	from := tc.path(root, tc.from)
	to := tc.path(toRoot, tc.to)
	writeRelinkTestMedia(t, from, 1)
	if tc.twin != "" {
		twin := tc.path(root, tc.twin)
		writeRelinkTestMedia(t, twin, 1)
		info, err := os.Stat(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(twin, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	// Another title keeps the library from walking empty, which would hold
	// back missing-marking.
	writeRelinkTestMedia(t, filepath.Join(root, "Keeper (2010)", "Keeper (2010).mkv"), 9)
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	original, err := repo.GetByPath(ctx, from)
	if err != nil {
		t.Fatalf("GetByPath(original): %v", err)
	}
	contentID, episodeID := seedRelinkTitle(t, pool, repo, tc, original)
	if tc.claimRoot != "" {
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_roots (media_folder_id, canonical_root_path, content_id) VALUES ($1, $2, $3)`,
			folderID, tc.path(root, tc.claimRoot), contentID); err != nil {
			t.Fatalf("seed root claim: %v", err)
		}
	}
	if tc.twin != "" {
		if err := os.Remove(tc.path(root, tc.twin)); err != nil {
			t.Fatal(err)
		}
	}
	queue.reset()

	relocateRelinkFile(t, s, folder, from, to, tc)

	moved, err := repo.GetByPath(ctx, to)
	if err != nil {
		t.Fatalf("GetByPath(new path): %v", err)
	}
	if moved.MissingSince != nil {
		t.Fatalf("file at the new path is marked missing")
	}
	if got := moved.ID == original.ID; got != tc.wantSameRow {
		t.Fatalf("new path kept the original row = %v, want %v (original %d, now %d)", got, tc.wantSameRow, original.ID, moved.ID)
	}
	timings, trickplay := relinkArtifacts(t, pool, moved.ID)
	wantArtifacts := 0
	if tc.wantSameRow {
		wantArtifacts = 1
	}
	if timings != wantArtifacts || trickplay != wantArtifacts {
		t.Fatalf("subtitle timings, trickplay on the new path's row = %d, %d, want %d each", timings, trickplay, wantArtifacts)
	}
	if !tc.wantSameRow {
		return
	}
	if len(moved.Chapters) != 1 || moved.Chapters[0].ThumbnailPath == "" {
		t.Fatalf("chapters on the moved row = %+v, want the seeded chapter image", moved.Chapters)
	}
	if moved.IntroStart == nil {
		t.Fatalf("moved row lost its hand-set intro marker")
	}
	if tc.wantLinks {
		checkRelinkKeptLinks(t, original, moved, contentID, episodeID)
	} else {
		checkRelinkRematch(t, pool, repo, tc, queue, original, moved, root, from, to, contentID, episodeID)
	}
	if tc.revert {
		checkRelinkRevert(t, pool, s, folder, tc, original, from, to, contentID, episodeID)
		return
	}
	if tc.deleteOriginal {
		checkRelinkDeletedOriginal(t, pool, s, folder, original, from)
		return
	}

	// A different file later placed at the old path is a new file, not the
	// one that moved away, even where a stand-in holds the path.
	writeRelinkTestMedia(t, from, 2)
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan with a new file at the old path: %v", err)
	}
	replacement, err := repo.GetByPath(ctx, from)
	if err != nil {
		t.Fatalf("GetByPath(old path): %v", err)
	}
	if replacement.ID == original.ID || replacement.MissingSince != nil || replacement.FileHash == "" || relinkStandInFor(t, pool, replacement.ID) != 0 {
		t.Fatalf("new file at the old path = row %d missing=%v hash %q stand-in %d, want a present row other than %d, hashed, no stand-in",
			replacement.ID, replacement.MissingSince, replacement.FileHash, relinkStandInFor(t, pool, replacement.ID), original.ID)
	}
	stillMoved, err := repo.GetByPath(ctx, to)
	if err != nil {
		t.Fatalf("GetByPath(new path) after replacement: %v", err)
	}
	if stillMoved.ID != original.ID || stillMoved.MissingSince != nil {
		t.Fatalf("moved file row = %d missing=%v, want %d present", stillMoved.ID, stillMoved.MissingSince, original.ID)
	}
}

// checkRelinkKeptLinks checks a row that moved within its title: links, its
// first sighting, its import edition, its detected marker, and its marker
// images all survive, the images under the new path's identities.
func checkRelinkKeptLinks(t *testing.T, original, moved *models.MediaFile, contentID, episodeID string) {
	t.Helper()
	if moved.ContentID != contentID || moved.EpisodeID != episodeID {
		t.Fatalf("moved row links = %q/%q, want %q/%q", moved.ContentID, moved.EpisodeID, contentID, episodeID)
	}
	if !moved.CreatedAt.Equal(original.CreatedAt) || moved.MatchAttemptedAt == nil {
		t.Fatalf("moved row created %v (was %v), match attempt %v, want both kept", moved.CreatedAt, original.CreatedAt, moved.MatchAttemptedAt)
	}
	if moved.EditionKey != "directors-cut" || moved.CreditsStart == nil {
		t.Fatalf("moved row edition %q, credits marker %v, want the import edition and the detected marker kept", moved.EditionKey, moved.CreditsStart)
	}
	images := models.EffectiveMarkerThumbnails(moved)
	if len(images) != 2 || images[0].ThumbnailPath == "" || images[1].ThumbnailPath == "" {
		t.Fatalf("marker images on the moved row = %+v, want both seeded images", images)
	}
}

// checkRelinkRematch checks a row that moved to another title: it is
// unlinked and queued like a new file, numbered and editioned from its new
// path, first seen now, and the title it left is held by a stand-in at the
// old path.
func checkRelinkRematch(t *testing.T, pool *pgxpool.Pool, repo *FileRepository, tc relinkCase, queue *relinkQueue, original, moved *models.MediaFile, root, from, to, contentID, episodeID string) {
	t.Helper()
	ctx := t.Context()
	if moved.ContentID != "" || moved.EpisodeID != "" {
		t.Fatalf("retitled row links = %q/%q, want none so it matches afresh", moved.ContentID, moved.EpisodeID)
	}
	if !queue.queued(moved) {
		t.Fatalf("retitled row %d (root %s) was not queued for matching", moved.ID, moved.ObservedRootPath)
	}
	if len(moved.MarkerThumbnails) != 0 || moved.CreditsStart != nil {
		t.Fatalf("retitled row kept marker images %+v or detected credits %v of its old title", moved.MarkerThumbnails, moved.CreditsStart)
	}
	if moved.EditionKey != "" || moved.MatchAttemptedAt != nil || !moved.CreatedAt.After(original.CreatedAt) {
		t.Fatalf("retitled row edition %q, match attempt %v, created %v (was %v), want no edition, no attempt, created later",
			moved.EditionKey, moved.MatchAttemptedAt, moved.CreatedAt, original.CreatedAt)
	}
	wantSeason, wantEpisode := 0, 0
	if hints := naming.ParseFilename(to, tc.folderType, root); hints != nil && hints.EpisodeNum > 0 {
		wantSeason, wantEpisode = hints.SeasonNum, hints.EpisodeNum
	}
	if tc.folderType == "series" && (moved.SeasonNumber != wantSeason || moved.EpisodeNumber != wantEpisode) {
		t.Fatalf("retitled row numbers = S%dE%d, want S%dE%d from the new path", moved.SeasonNumber, moved.EpisodeNumber, wantSeason, wantEpisode)
	}
	left, err := repo.GetByPath(ctx, from)
	if err != nil {
		t.Fatalf("GetByPath(old path) after retitle: %v", err)
	}
	if left.MissingSince == nil || left.FileHash != "" || left.ContentID != contentID || left.EpisodeID != episodeID ||
		relinkStandInFor(t, pool, left.ID) != original.ID || !left.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("row left at the old path = missing %v hash %q links %q/%q stand-in for %d created %v, want missing, no hash, links %q/%q, stand-in for %d, created %v",
			left.MissingSince, left.FileHash, left.ContentID, left.EpisodeID, relinkStandInFor(t, pool, left.ID), left.CreatedAt,
			contentID, episodeID, original.ID, original.CreatedAt)
	}
	var items int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_items WHERE content_id = $1`, contentID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if items != 1 {
		t.Fatalf("the title the file left was deleted within the removal grace")
	}
}

// checkRelinkRevert links the retitled row to another title, as a match
// would, renames the file back, and checks that the row takes its old path
// and title back while the other title is held by a stand-in.
func checkRelinkRevert(t *testing.T, pool *pgxpool.Pool, s *Scanner, folder *models.MediaFolder, tc relinkCase, original *models.MediaFile, from, to, contentID, episodeID string) {
	t.Helper()
	ctx := t.Context()
	otherContent, otherEpisode := seedRelinkItem(t, pool, tc.folderType, tc.otherID())
	if _, err := pool.Exec(ctx, `UPDATE media_files SET content_id = $2, episode_id = NULLIF($3, '') WHERE id = $1`,
		original.ID, otherContent, otherEpisode); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(to, from); err != nil {
		t.Fatal(err)
	}
	if tc.secondRoot {
		checkRelinkUndecidedRevert(t, pool, s, folder, original, from, to)
	}
	switch {
	case tc.standInSwept:
		standIn, err := s.fileRepo.GetByPath(ctx, from)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, standIn.ID); err != nil {
			t.Fatal(err)
		}
		processRelinkFile(t, s, folder, from, scanStateFromMediaFile(standIn))
		// The old title's links went with the stand-in, so the row comes
		// back as a retitle from the title it was matched to.
		contentID, episodeID = "", ""
	case tc.byFile:
		if err := s.ScanFile(ctx, from, folder); err != nil {
			t.Fatalf("scan of the restored path: %v", err)
		}
		if err := s.ScanFile(ctx, to, folder); err != nil {
			t.Fatalf("scan of the abandoned path: %v", err)
		}
	default:
		if _, err := s.ScanFolder(ctx, folder); err != nil {
			t.Fatalf("scan after the revert: %v", err)
		}
	}
	back, err := s.fileRepo.GetByPath(ctx, from)
	if err != nil {
		t.Fatalf("GetByPath(old path) after revert: %v", err)
	}
	if back.ID != original.ID || back.MissingSince != nil || back.ContentID != contentID || back.EpisodeID != episodeID ||
		relinkStandInFor(t, pool, back.ID) != 0 {
		t.Fatalf("reverted path row = %d missing %v links %q/%q, want row %d present with links %q/%q",
			back.ID, back.MissingSince, back.ContentID, back.EpisodeID, original.ID, contentID, episodeID)
	}
	if !tc.standInSwept && (!back.CreatedAt.Equal(original.CreatedAt) || back.EditionKey != "directors-cut") {
		t.Fatalf("reverted row created %v edition %q, want %v and the import edition back", back.CreatedAt, back.EditionKey, original.CreatedAt)
	}
	if timings, trickplay := relinkArtifacts(t, pool, back.ID); timings != 1 || trickplay != 1 {
		t.Fatalf("reverted row subtitle timings, trickplay = %d, %d, want 1 each", timings, trickplay)
	}
	left, err := s.fileRepo.GetByPath(ctx, to)
	if err != nil {
		t.Fatalf("GetByPath(abandoned path): %v", err)
	}
	if left.MissingSince == nil || left.ContentID != otherContent || left.EpisodeID != otherEpisode || relinkStandInFor(t, pool, left.ID) != original.ID {
		t.Fatalf("row at the abandoned path = missing %v links %q/%q, want a stand-in for %d with links %q/%q",
			left.MissingSince, left.ContentID, left.EpisodeID, original.ID, otherContent, otherEpisode)
	}
}

// checkRelinkDeletedOriginal deletes a retitled row and puts the same bytes
// back at its stand-in's path: the dangling stand-in declines the reclaim and
// the file is written onto it.
func checkRelinkDeletedOriginal(t *testing.T, pool *pgxpool.Pool, s *Scanner, folder *models.MediaFolder, original *models.MediaFile, from string) {
	t.Helper()
	ctx := t.Context()
	standIn, err := s.fileRepo.GetByPath(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, original.ID); err != nil {
		t.Fatal(err)
	}
	writeRelinkTestMedia(t, from, 1)
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan with the bytes back at the stand-in: %v", err)
	}
	back, err := s.fileRepo.GetByPath(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != standIn.ID || back.MissingSince != nil || back.FileHash == "" || relinkStandInFor(t, pool, back.ID) != 0 {
		t.Fatalf("row at the stand-in's path = %d missing %v hash %q stand-in %d, want stand-in %d present, hashed, no longer a stand-in",
			back.ID, back.MissingSince, back.FileHash, relinkStandInFor(t, pool, back.ID), standIn.ID)
	}
}

// processRelinkFile runs the scanner's per-file step for one path with the
// given snapshot of the row there, as a scan that read it does.
func processRelinkFile(t *testing.T, s *Scanner, folder *models.MediaFolder, filePath string, existing *scanStateFile) {
	t.Helper()
	ctx := t.Context()
	rootOverrides, err := s.loadRootOverrides(ctx, folder.ID, []string{filepath.Dir(filePath)})
	if err != nil {
		t.Fatal(err)
	}
	identityOverrides, err := s.loadIdentityOverrides(ctx, folder.ID)
	if err != nil {
		t.Fatal(err)
	}
	roots := inferRootAssignments([]string{filePath}, folder.Type, folder.ID, rootOverrides, folder.Paths...)
	groups := inferGroupAssignments([]string{filePath}, folder.Type, folder.ID, roots.Assignments, identityOverrides)
	if _, _, err := s.processFile(ctx, filePath, folder, map[string]*scanStateFile{filePath: existing}, nil,
		roots.Assignments[filePath], groups.Assignments[filePath], newExternalSubtitleDirCache(folder.Type)); err != nil {
		t.Fatalf("processing %s: %v", filePath, err)
	}
}

// checkRelinkUndecidedRevert scans a revert out of a root the move leaves
// empty, the signature of a lost mount: the scan cannot tell a revert from a
// copy and leaves both rows. A file in that root then lets the next scan
// decide.
func checkRelinkUndecidedRevert(t *testing.T, pool *pgxpool.Pool, s *Scanner, folder *models.MediaFolder, original *models.MediaFile, from, to string) {
	t.Helper()
	ctx := t.Context()
	if err := os.Remove(filepath.Dir(to)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan after the revert: %v", err)
	}
	standIn, err := s.fileRepo.GetByPath(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	if standIn.ID == original.ID || standIn.MissingSince == nil || relinkStandInFor(t, pool, standIn.ID) != original.ID {
		t.Fatalf("undecided revert wrote the file onto its stand-in: row %d missing %v stand-in for %d", standIn.ID, standIn.MissingSince, relinkStandInFor(t, pool, standIn.ID))
	}
	writeRelinkTestMedia(t, filepath.Join(folder.Paths[1], "Keeper Two (2011)", "Keeper Two (2011).mkv"), 8)
}

// relocateRelinkFile moves (or copies) the scanned file from from to to and
// scans the change the way the case describes.
func relocateRelinkFile(t *testing.T, s *Scanner, folder *models.MediaFolder, from, to string, tc relinkCase) {
	t.Helper()
	ctx := t.Context()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	source := from
	if tc.missingFirst {
		source = filepath.Join(t.TempDir(), "parked.mkv")
		if err := os.Rename(from, source); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ScanFolder(ctx, folder); err != nil {
			t.Fatalf("scan with the file gone: %v", err)
		}
		if gone, err := s.fileRepo.GetByPath(ctx, from); err != nil || gone.MissingSince == nil {
			t.Fatalf("original row is not marked missing after the file left (err %v)", err)
		}
	}
	if tc.copy {
		writeRelinkTestMedia(t, to, 1)
	} else if err := os.Rename(source, to); err != nil {
		t.Fatal(err)
	}
	if tc.subtree != "" {
		if _, err := s.ScanSubtree(ctx, folder, tc.path(folder.Paths[0], tc.subtree)); err != nil {
			t.Fatalf("subtree scan after rename: %v", err)
		}
		return
	}
	if !tc.byFile {
		if _, err := s.ScanFolder(ctx, folder); err != nil {
			t.Fatalf("scan after rename: %v", err)
		}
		return
	}
	if err := s.ScanFile(ctx, to, folder); err != nil {
		t.Fatalf("scan of the new path: %v", err)
	}
	if err := s.ScanFile(ctx, from, folder); err != nil {
		t.Fatalf("scan of the old path: %v", err)
	}
}
