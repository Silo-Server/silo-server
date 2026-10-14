package scanner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// podcastSubtreeFixture is a podcast library on disk and in the database,
// scanned with a fake ffprobe that prints each episode file's content: every
// episode file holds the ffprobe JSON for its own tags.
type podcastSubtreeFixture struct {
	pool        *pgxpool.Pool
	scanner     *Scanner
	folder      *models.MediaFolder
	root        string
	ffprobePath string
}

func newPodcastSubtreeFixture(t *testing.T) *podcastSubtreeFixture {
	t.Helper()
	pool := newDeadRootTestPool(t)
	ctx := context.Background()

	base := t.TempDir()
	root := filepath.Join(base, "podcasts")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir library root: %v", err)
	}
	ffprobePath := filepath.Join(base, "ffprobe")
	writeFakeTool(t, ffprobePath, "#!/bin/sh\nfor last; do :; done\ncat \"$last\"\n")

	folder := &models.MediaFolder{Type: "podcasts", Name: "Podcast subtree test", Enabled: true, Paths: []string{root}}
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`,
		folder.Type, folder.Name,
	).Scan(&folder.ID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO media_folder_paths (media_folder_id, path) VALUES ($1, $2)`, folder.ID, root,
	); err != nil {
		t.Fatalf("seed folder path: %v", err)
	}
	t.Cleanup(func() {
		// Items are found through both files and memberships, so an item a
		// broken scan left behind is removed too. Episodes cascade with them.
		_, _ = pool.Exec(ctx, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1 AND content_id IS NOT NULL
				UNION
				SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1
			)`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folder_paths WHERE media_folder_id = $1`, folder.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folder.ID)
	})

	return &podcastSubtreeFixture{
		pool:        pool,
		scanner:     NewScanner(NewFileRepository(pool), ffprobePath, nil, 2, false, 0),
		folder:      folder,
		root:        root,
		ffprobePath: ffprobePath,
	}
}

// addEpisode writes an episode file whose content is the ffprobe output for
// its album, title and track tags, and returns its path.
func (f *podcastSubtreeFixture) addEpisode(t *testing.T, show, file, album, title, track string) string {
	t.Helper()
	probe, err := json.Marshal(map[string]any{
		"format": map[string]any{
			"format_name": "mp3",
			"tags":        map[string]string{"album": album, "title": title, "track": track},
		},
		"streams": []map[string]any{{"index": 0, "codec_type": "audio", "codec_name": "mp3"}},
	})
	if err != nil {
		t.Fatalf("marshal probe output: %v", err)
	}
	path := filepath.Join(f.root, show, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir show: %v", err)
	}
	if err := os.WriteFile(path, probe, 0o644); err != nil {
		t.Fatalf("write episode: %v", err)
	}
	return path
}

func (f *podcastSubtreeFixture) scanFolder(t *testing.T) {
	t.Helper()
	if _, err := f.scanner.ScanFolder(context.Background(), f.folder); err != nil {
		t.Fatalf("ScanFolder: %v", err)
	}
}

func (f *podcastSubtreeFixture) scanSubtree(t *testing.T, path string) {
	t.Helper()
	if _, err := f.scanner.ScanSubtree(context.Background(), f.folder, path); err != nil {
		t.Fatalf("ScanSubtree(%s): %v", path, err)
	}
}

// podcastFileRow is a media_files row with the item it links to.
type podcastFileRow struct {
	contentID         string
	episodeID         string
	baseType          string
	canonicalRootPath string
	itemType          string
	itemTitle         string
	missing           bool
}

func (f *podcastSubtreeFixture) fileRow(t *testing.T, path string) podcastFileRow {
	t.Helper()
	var row podcastFileRow
	if err := f.pool.QueryRow(context.Background(), `
		SELECT COALESCE(mf.content_id, ''), COALESCE(mf.episode_id, ''), COALESCE(mf.base_type, ''),
		       COALESCE(mf.canonical_root_path, ''), COALESCE(mi.type, ''), COALESCE(mi.title, ''),
		       mf.missing_since IS NOT NULL
		FROM media_files mf
		LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1 AND mf.file_path = $2
	`, f.folder.ID, path).Scan(
		&row.contentID, &row.episodeID, &row.baseType,
		&row.canonicalRootPath, &row.itemType, &row.itemTitle,
		&row.missing,
	); err != nil {
		t.Fatalf("load media file %s: %v", path, err)
	}
	return row
}

// requireEpisodeOf fails unless the file is a present episode of a podcast
// show item rooted at showDir.
func (f *podcastSubtreeFixture) requireEpisodeOf(t *testing.T, path, showDir, showTitle string) podcastFileRow {
	t.Helper()
	row := f.fileRow(t, path)
	if row.contentID == "" || row.episodeID == "" {
		t.Fatalf("%s: content_id=%q episode_id=%q, want both set", path, row.contentID, row.episodeID)
	}
	if row.itemType != "podcast" || row.itemTitle != showTitle {
		t.Fatalf("%s: item type=%q title=%q, want podcast %q", path, row.itemType, row.itemTitle, showTitle)
	}
	if row.baseType != "podcast" || row.canonicalRootPath != showDir {
		t.Fatalf("%s: base_type=%q canonical_root_path=%q, want podcast %q", path, row.baseType, row.canonicalRootPath, showDir)
	}
	if row.missing {
		t.Fatalf("%s: marked missing, want present", path)
	}
	return row
}

// requireListed fails unless the item exists and belongs to the library.
func (f *podcastSubtreeFixture) requireListed(t *testing.T, contentID string) {
	t.Helper()
	var listed bool
	if err := f.pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM media_items mi
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			WHERE mi.content_id = $1 AND mil.media_folder_id = $2
		)`, contentID, f.folder.ID).Scan(&listed); err != nil {
		t.Fatalf("check library membership of %s: %v", contentID, err)
	}
	if !listed {
		t.Fatalf("item %s is gone or no longer in the library", contentID)
	}
}

func (f *podcastSubtreeFixture) requireOnlyPodcastItems(t *testing.T) {
	t.Helper()
	var others int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM media_items
		WHERE type <> 'podcast' AND content_id IN (
			SELECT content_id FROM media_files WHERE media_folder_id = $1
			UNION
			SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1
		)`, f.folder.ID).Scan(&others); err != nil {
		t.Fatalf("count non-podcast items: %v", err)
	}
	if others != 0 {
		t.Fatalf("library holds %d non-podcast items, want 0", others)
	}
}

// TestScanSubtreeIndexesPodcastShowFolders follows the reproduction in
// issue #2222: subtree scans of show folders must index new episodes and new
// shows with the podcast scanner, and must keep the show's identity so a later
// full scan finds the same items.
func TestScanSubtreeIndexesPodcastShowFolders(t *testing.T) {
	f := newPodcastSubtreeFixture(t)
	showA := filepath.Join(f.root, "Show A")
	showB := filepath.Join(f.root, "Show B")

	episodeOne := f.addEpisode(t, "Show A", "Episode 1.mp3", "Show A", "Episode One", "1")
	f.scanFolder(t)
	before := f.requireEpisodeOf(t, episodeOne, showA, "Show A")

	episodeTwo := f.addEpisode(t, "Show A", "Episode 2.mp3", "Show A", "Episode Two", "2")
	pilot := f.addEpisode(t, "Show B", "Episode 1.mp3", "Show B", "Pilot", "1")
	f.scanSubtree(t, showA)
	f.scanSubtree(t, showB)

	afterOne := f.requireEpisodeOf(t, episodeOne, showA, "Show A")
	if afterOne.contentID != before.contentID || afterOne.episodeID != before.episodeID {
		t.Fatalf("Episode 1 moved from show %s episode %s to show %s episode %s",
			before.contentID, before.episodeID, afterOne.contentID, afterOne.episodeID)
	}
	afterTwo := f.requireEpisodeOf(t, episodeTwo, showA, "Show A")
	if afterTwo.contentID != before.contentID {
		t.Fatalf("Episode 2 content_id = %s, want Show A %s", afterTwo.contentID, before.contentID)
	}
	if afterTwo.episodeID == afterOne.episodeID {
		t.Fatalf("Episode 2 shares episode_id %s with Episode 1", afterTwo.episodeID)
	}
	showBRow := f.requireEpisodeOf(t, pilot, showB, "Show B")
	if showBRow.contentID == before.contentID {
		t.Fatalf("Show B shares content_id %s with Show A", showBRow.contentID)
	}
	f.requireOnlyPodcastItems(t)

	// The full scan that rebuilt Show A under a new content ID in the issue
	// must now find both shows where the subtree scans left them.
	f.scanFolder(t)
	for _, check := range []struct {
		path, showDir, title string
		want                 podcastFileRow
	}{
		{episodeOne, showA, "Show A", afterOne},
		{episodeTwo, showA, "Show A", afterTwo},
		{pilot, showB, "Show B", showBRow},
	} {
		got := f.requireEpisodeOf(t, check.path, check.showDir, check.title)
		if got.contentID != check.want.contentID || got.episodeID != check.want.episodeID {
			t.Fatalf("full scan moved %s from show %s episode %s to show %s episode %s",
				check.path, check.want.contentID, check.want.episodeID, got.contentID, got.episodeID)
		}
	}
	f.requireOnlyPodcastItems(t)
}

// TestScanSubtreeReconcilesOnlyTheScannedPodcastShow checks that a show-folder
// scan marks missing only the files of that show and leaves the other shows in
// the library, and that it leaves a show folder alone when it cannot verify
// it: emptied shows wait for a full scan even when the empty-cleanup
// confirmation is granted, and vanished shows wait for a full scan.
func TestScanSubtreeReconcilesOnlyTheScannedPodcastShow(t *testing.T) {
	f := newPodcastSubtreeFixture(t)
	showA := filepath.Join(f.root, "Show A")
	showB := filepath.Join(f.root, "Show B")
	showC := filepath.Join(f.root, "Show C")

	keptA := f.addEpisode(t, "Show A", "Episode 1.mp3", "Show A", "Kept", "1")
	removedA := f.addEpisode(t, "Show A", "Episode 2.mp3", "Show A", "Removed", "2")
	removedB := f.addEpisode(t, "Show B", "Episode 1.mp3", "Show B", "Removed", "1")
	removedC := f.addEpisode(t, "Show C", "Episode 1.mp3", "Show C", "Removed", "1")
	notes := filepath.Join(showA, "notes")
	if err := os.Mkdir(notes, 0o755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	f.scanFolder(t)
	showBID := f.fileRow(t, removedB).contentID
	showCID := f.fileRow(t, removedC).contentID

	for _, path := range []string{removedA, removedB} {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
	}
	if err := os.RemoveAll(showC); err != nil {
		t.Fatalf("remove Show C: %v", err)
	}

	// A folder inside the show stands for the whole show folder.
	f.scanSubtree(t, notes)
	f.requireEpisodeOf(t, keptA, showA, "Show A")
	if !f.fileRow(t, removedA).missing {
		t.Fatal("deleted Show A episode is still present after scanning Show A")
	}
	if f.fileRow(t, removedB).missing {
		t.Fatal("scanning Show A marked a Show B episode missing")
	}
	if f.fileRow(t, removedC).missing {
		t.Fatal("scanning Show A marked a Show C episode missing")
	}
	f.requireListed(t, showBID)
	f.requireListed(t, showCID)

	// Show B's folder is now empty, which is also what a lost mount looks
	// like. Even with the operator's empty-cleanup confirmation granted, a
	// subtree scan leaves the rows and the confirmation for a full scan.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE media_folders SET allow_empty_cleanup_once = true WHERE id = $1`, f.folder.ID,
	); err != nil {
		t.Fatalf("grant empty cleanup: %v", err)
	}
	f.scanSubtree(t, showB)
	if f.fileRow(t, removedB).missing {
		t.Fatal("subtree scan of an emptied show folder marked its episode missing")
	}
	f.requireListed(t, showBID)
	var allowed bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT allow_empty_cleanup_once FROM media_folders WHERE id = $1`, f.folder.ID,
	).Scan(&allowed); err != nil {
		t.Fatalf("read empty cleanup confirmation: %v", err)
	}
	if !allowed {
		t.Fatal("subtree scan consumed the empty-cleanup confirmation")
	}

	// Show C's folder is gone. Only a full scan retires it.
	f.scanSubtree(t, showC)
	if f.fileRow(t, removedC).missing {
		t.Fatal("subtree scan of a vanished show folder marked its episode missing")
	}
	f.requireListed(t, showCID)

	f.scanFolder(t)
	for _, path := range []string{removedB, removedC} {
		if !f.fileRow(t, path).missing {
			t.Fatalf("%s is still present after a full scan", path)
		}
	}
	f.requireOnlyPodcastItems(t)
}

// membershipInsertHook runs a callback once, right before the first query that
// inserts a podcast show's library membership.
type membershipInsertHook struct {
	once sync.Once
	run  func()
}

func (h *membershipInsertHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "INSERT INTO media_item_libraries") && strings.Contains(data.SQL, "VALUES ($1, $2, NOW())") {
		h.once.Do(h.run)
	}
	return ctx
}

func (*membershipInsertHook) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestPodcastShowSurvivesConcurrentMembershipSweep covers two scans of one
// podcast library running side by side, which the ingest executor allows for
// subtree scans of different shows. While one scan writes a new show, the
// other's folder-wide membership sweep runs. The sweep must not find the new
// show's files without their membership and delete the show.
func TestPodcastShowSurvivesConcurrentMembershipSweep(t *testing.T) {
	f := newPodcastSubtreeFixture(t)
	ctx := context.Background()
	f.addEpisode(t, "Show A", "Episode 1.mp3", "Show A", "Episode One", "1")
	f.scanFolder(t)
	pilot := f.addEpisode(t, "Show B", "Episode 1.mp3", "Show B", "Pilot", "1")

	cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	var sweepRan bool
	var sweepErr error
	hook := &membershipInsertHook{run: func() {
		sweepRan = true
		_, _, _, sweepErr = f.scanner.sweepMissingAndReconcile(ctx, f.folder, false)
	}}
	cfg.ConnConfig.Tracer = hook
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect traced pool: %v", err)
	}
	t.Cleanup(traced.Close)

	scanning := NewScanner(NewFileRepository(traced), f.ffprobePath, nil, 2, false, 0)
	if _, err := scanning.ScanSubtree(ctx, f.folder, filepath.Join(f.root, "Show B")); err != nil {
		t.Fatalf("ScanSubtree(Show B) with a concurrent sweep: %v", err)
	}
	if !sweepRan {
		t.Fatal("the concurrent sweep never ran; the membership insert was not traced")
	}
	if sweepErr != nil {
		t.Fatalf("concurrent sweep: %v", sweepErr)
	}
	row := f.requireEpisodeOf(t, pilot, filepath.Join(f.root, "Show B"), "Show B")
	f.requireListed(t, row.contentID)
	f.requireOnlyPodcastItems(t)
}
