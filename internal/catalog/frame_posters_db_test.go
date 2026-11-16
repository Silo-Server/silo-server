package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
)

// framePosterFixture writes movies and their files into one enabled library.
type framePosterFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	prefix string
	folder int
}

func newFramePosterFixture(t *testing.T) *framePosterFixture {
	t.Helper()
	f := &framePosterFixture{t: t, pool: newTestPool(t), prefix: fmt.Sprintf("fp%d", time.Now().UnixNano())}
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO media_folders (type,name,enabled) VALUES ('movies',$1,true) RETURNING id`, f.prefix).Scan(&f.folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, f.folder)
		_, _ = f.pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, f.prefix+"-%")
		_, _ = f.pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, f.folder)
	})
	return f
}

func (f *framePosterFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.t.Context(), sql, args...); err != nil {
		f.t.Fatalf("seed (%s): %v", sql, err)
	}
}

// item writes an item with an empty poster path and returns its content ID.
func (f *framePosterFixture) item(name, itemType, status, posterSource string, locked bool) string {
	f.t.Helper()
	id := f.prefix + "-" + name
	lockedFields := []int{}
	if locked {
		lockedFields = []int{fieldImagesLocked}
	}
	f.exec(`INSERT INTO media_items (content_id,type,title,status,poster_source_path,locked_fields) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, itemType, name, status, posterSource, lockedFields)
	return id
}

// file writes a probed main file of contentID and returns its id.
func (f *framePosterFixture) file(contentID, name string, missing bool) int {
	f.t.Helper()
	var missingSince *time.Time
	if missing {
		now := time.Now()
		missingSince = &now
	}
	var id int
	if err := f.pool.QueryRow(f.t.Context(), `INSERT INTO media_files (content_id,media_folder_id,file_path,duration,file_size,missing_since)
		VALUES ($1,$2,$3,600,1000,$4) RETURNING id`, contentID, f.folder, "/"+f.prefix+"/"+name+".mkv", missingSince).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// fail records a failure under key for fileID, a file the fixture wrote:
// unusable, or failed with a retry time in the future, as recorder.
func (f *framePosterFixture) fail(key mediaartifact.Key, fileID int, unusable bool, recorder string) {
	f.t.Helper()
	store := mediaartifact.NewStore(f.pool)
	identity := mediaartifact.Identity{FileSize: 1000, DurationSeconds: 600}
	var err error
	if unusable {
		err = store.Upsert(f.t.Context(), mediaartifact.Artifact{
			MediaFileID: fileID, Key: key, Identity: identity,
			Status: mediaartifact.StatusUnusable, Detail: "undecodable", RecordedBy: recorder,
		})
	} else {
		err = store.RecordFailure(f.t.Context(), mediaartifact.Failure{
			MediaFileID: fileID, Key: key, Identity: identity, RecordedBy: recorder, Error: "timed out",
		})
	}
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *framePosterFixture) poster(id string) (path, source string) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.t.Context(), `SELECT poster_path, poster_source_path FROM media_items WHERE content_id = $1`, id).Scan(&path, &source); err != nil {
		f.t.Fatal(err)
	}
	return path, source
}

// The frame poster task lists movies with an empty poster slot and nothing on
// its way, and saves a frame only while the slot is still as it read it.
func TestFramePostersListOnlyEmptySlotsAndSaveOnlyOverThemDB(t *testing.T) {
	f := newFramePosterFixture(t)
	ctx := t.Context()
	repo := NewItemRepository(f.pool)

	empty := f.item("empty", "movie", "unmatched", "", false)
	emptyFile := f.file(empty, "empty-a", false)
	f.file(empty, "empty-b", false)
	lostSource := StillSourceFramePrefix + "1?v=1-1"
	lost := f.item("lost", "movie", "matched", lostSource, false)
	lostFile := f.file(lost, "lost", false)
	for _, excluded := range []struct {
		name, itemType, status, source string
		locked, missing                bool
	}{
		{name: "sidecar", itemType: "movie", status: "matched", source: "file:///m/poster.jpg?v=1-1"},
		{name: "provider", itemType: "movie", status: "matched", source: "https://img.example/poster.jpg"},
		{name: "locked", itemType: "movie", status: "matched", locked: true},
		{name: "pending", itemType: "movie", status: "pending"},
		{name: "missing", itemType: "movie", status: "matched", missing: true},
		{name: "series", itemType: "series", status: "matched"},
	} {
		f.file(f.item(excluded.name, excluded.itemType, excluded.status, excluded.source, excluded.locked), excluded.name, excluded.missing)
	}
	// A file with no known duration would give a frame at 0 s.
	f.exec(`UPDATE media_files SET duration = 0 WHERE id = $1`, f.file(f.item("silent", "movie", "matched", "", false), "silent", false))
	key := mediaartifact.Key{Kind: "frame_poster_test", AlgorithmVersion: 1, ConfigHash: mediaartifact.ConfigHash("frame_poster_test", f.prefix)}
	f.fail(key, f.file(f.item("undecodable", "movie", "matched", "", false), "undecodable", false), true, "test")
	// A failure backs off whichever server recorded it.
	f.fail(key, f.file(f.item("backoff", "movie", "matched", "", false), "backoff", false), false, "another-server")
	// A movie whose first version is blocked tries its next one.
	versions := f.item("versions", "movie", "matched", "", false)
	f.fail(key, f.file(versions, "versions-a", false), true, "test")
	versionsFile := f.file(versions, "versions-b", false)

	pending, err := repo.ListPendingFramePosters(ctx, key, 100000)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]PendingFramePoster{}
	var ids []string
	for _, movie := range pending {
		if strings.HasPrefix(movie.ContentID, f.prefix+"-") {
			got[movie.ContentID] = movie
			ids = append(ids, movie.ContentID)
		}
	}
	if want := []string{empty, lost, versions}; !slices.Equal(ids, want) {
		t.Fatalf("pending = %v, want %v", ids, want)
	}
	if m := got[empty]; m.FileID != emptyFile || m.PosterSource != "" || m.Duration != 600 || m.FrameKey != fmt.Sprintf("%d?v=1000-0", emptyFile) {
		t.Errorf("empty slot = %+v, want its lowest file %d with no source", m, emptyFile)
	}
	if m := got[lost]; m.FileID != lostFile || m.PosterSource != lostSource {
		t.Errorf("cleared frame = %+v, want file %d with its frame source", m, lostFile)
	}
	if m := got[versions]; m.FileID != versionsFile {
		t.Errorf("two versions = %+v, want the unblocked file %d", m, versionsFile)
	}

	frameSource := StillSourceFramePrefix + got[empty].FrameKey
	first, second := "local/movies/"+empty+"/a/poster/original.webp", "local/movies/"+empty+"/b/poster/original.webp"
	if saved, err := repo.SetFramePosterIfSource(ctx, empty, "", first, frameSource, "th"); err != nil || !saved {
		t.Fatalf("saving over the empty slot = %v, %v; want saved", saved, err)
	}
	// A path landed: a second frame read before the first was saved loses.
	if saved, err := repo.SetFramePosterIfSource(ctx, empty, "", second, frameSource, "th"); err != nil || saved {
		t.Fatalf("saving over a landed path = %v, %v; want not saved", saved, err)
	}
	if path, source := f.poster(empty); path != first || source != frameSource {
		t.Errorf("poster = %q from %q, want the first frame kept", path, source)
	}
	// A poster stored without a source fills the slot too: the empty source
	// alone does not make it free.
	f.exec(`UPDATE media_items SET poster_source_path = '' WHERE content_id = $1`, empty)
	if saved, err := repo.SetFramePosterIfSource(ctx, empty, "", second, frameSource, "th"); err != nil || saved {
		t.Fatalf("saving over a poster with no source = %v, %v; want not saved", saved, err)
	}
	if path, _ := f.poster(empty); path != first {
		t.Errorf("poster = %q, want the stored poster kept", path)
	}
	// The source changed: a sidecar found meanwhile waits for its copy.
	f.exec(`UPDATE media_items SET poster_source_path = 'file:///m/poster.jpg?v=2-2' WHERE content_id = $1`, lost)
	if saved, err := repo.SetFramePosterIfSource(ctx, lost, lostSource, "local/movies/"+lost+"/c/poster/original.webp", frameSource, "th"); err != nil || saved {
		t.Fatalf("saving over a changed source = %v, %v; want not saved", saved, err)
	}
	if path, source := f.poster(lost); path != "" || source != "file:///m/poster.jpg?v=2-2" {
		t.Errorf("poster = %q from %q, want the sidecar's pending slot untouched", path, source)
	}
}
