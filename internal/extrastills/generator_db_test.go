package extrastills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeFrames struct {
	calls int
	err   error
}

func (f *fakeFrames) ExtractStill(_ context.Context, file *models.MediaFile, _ float64) ([]byte, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return []byte(fmt.Sprintf("frame-%d", file.ID)), nil
}

// fakeFiles serves only the fixtures' files. Other packages' tests share the
// database and may leave extras pending; the generator skips those without
// writing to them.
type fakeFiles []int

func (f fakeFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	if !slices.Contains(f, id) {
		return nil, errors.New("not this test's file")
	}
	return &models.MediaFile{ID: id}, nil
}

type fakeCacher struct {
	before func() // runs before the store returns, to race a scan
}

func (f *fakeCacher) CacheExtraStill(_ context.Context, data []byte, extraID string) (string, string, error) {
	if f.before != nil {
		f.before()
	}
	return "local/extras/" + extraID + "/still/original." + string(data) + ".webp", "hash", nil
}

// pendingLimit reaches past whatever other tests left pending.
const pendingLimit = 10000

type extraFixture struct {
	pool     *pgxpool.Pool
	extraID  string
	fileID   int
	folderID int
}

func seedExtra(ctx context.Context, t *testing.T, stillSource string) extraFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	movieID := fmt.Sprintf("extra-stills-movie-%d", suffix)
	fx := extraFixture{pool: pool, extraID: fmt.Sprintf("extra-stills-extra-%d", suffix)}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', 'Extra Stills Test', true) RETURNING id`).
		Scan(&fx.folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	folderID := fx.folderID
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_items WHERE content_id = $1`, movieID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path)
		VALUES ($1, 'movie', 'Heat', 'matched', '{}'::text[], '', '', '')`, movieID); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_extras (content_id, parent_id, kind, title, still_source)
		VALUES ($1, $2, 'featurette', 'Making Of', $3)`, fx.extraID, movieID, stillSource); err != nil {
		t.Fatalf("seed extra: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, file_size, file_modified_at, duration, extra_id)
		VALUES ($1, $2, 1000, $3, 120, $4) RETURNING id`,
		folderID, fmt.Sprintf("/media/Heat/Extras/Making Of %d.mkv", suffix),
		time.Unix(1_700_000_000, 0), fx.extraID).Scan(&fx.fileID); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	return fx
}

func (fx extraFixture) exec(ctx context.Context, t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := fx.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (fx extraFixture) still(ctx context.Context, t *testing.T) (path, source string) {
	t.Helper()
	if err := fx.pool.QueryRow(ctx, `SELECT still_path, still_source FROM media_extras WHERE content_id = $1`, fx.extraID).
		Scan(&path, &source); err != nil {
		t.Fatalf("read still: %v", err)
	}
	return path, source
}

func TestGenerateSavesAFrameOncePerFile(t *testing.T) {
	ctx := t.Context()
	fx := seedExtra(ctx, t, "")
	frames := &fakeFrames{}
	g := NewGenerator(fx.pool, fakeFiles{fx.fileID}, frames, &fakeCacher{})

	if saved, err := g.Generate(ctx, pendingLimit); err != nil || saved != 1 {
		t.Fatalf("Generate = %d, %v; want 1 saved", saved, err)
	}
	path, source := fx.still(ctx, t)
	if want := fmt.Sprintf("frame:%d?v=1000-1700000000", fx.fileID); source != want || !strings.HasSuffix(path, fmt.Sprintf("original.frame-%d.webp", fx.fileID)) {
		t.Fatalf("still = %q from %q, want a frame from %q", path, source, want)
	}

	if saved, err := g.Generate(ctx, pendingLimit); err != nil || saved != 0 || frames.calls != 1 {
		t.Fatalf("second Generate = %d, %v with %d extractions; want nothing to do", saved, err, frames.calls)
	}

	// A replaced file needs a new frame.
	fx.exec(ctx, t, `UPDATE media_files SET file_size = 2000 WHERE id = $1`, fx.fileID)
	if saved, err := g.Generate(ctx, pendingLimit); err != nil || saved != 1 {
		t.Fatalf("Generate after file change = %d, %v; want 1 saved", saved, err)
	}
	if _, source := fx.still(ctx, t); !strings.HasSuffix(source, "?v=2000-1700000000") {
		t.Fatalf("still_source = %q, want the new file's key", source)
	}
}

func TestGenerateLeavesSidecarStillsAlone(t *testing.T) {
	ctx := t.Context()
	fx := seedExtra(ctx, t, "file:///media/Heat/Extras/Making Of-thumb.jpg?v=1-2")
	frames := &fakeFrames{}
	if saved, err := NewGenerator(fx.pool, fakeFiles{fx.fileID}, frames, &fakeCacher{}).Generate(ctx, pendingLimit); err != nil || saved != 0 || frames.calls != 0 {
		t.Fatalf("Generate = %d, %v with %d extractions; want a sidecar extra skipped", saved, err, frames.calls)
	}
}

// A scan that saves a sidecar while the frame is being stored wins.
func TestGenerateLosesARaceToASidecar(t *testing.T) {
	ctx := t.Context()
	fx := seedExtra(ctx, t, "")
	sidecar := "file:///media/Heat/Extras/Making Of-thumb.jpg?v=1-2"
	cacher := &fakeCacher{before: func() {
		if _, err := fx.pool.Exec(ctx, `UPDATE media_extras SET still_path = 'sidecar', still_source = $2 WHERE content_id = $1`, fx.extraID, sidecar); err != nil {
			t.Error(err)
		}
	}}
	if saved, err := NewGenerator(fx.pool, fakeFiles{fx.fileID}, &fakeFrames{}, cacher).Generate(ctx, pendingLimit); err != nil || saved != 0 {
		t.Fatalf("Generate = %d, %v; want the frame discarded", saved, err)
	}
	if path, source := fx.still(ctx, t); path != "sidecar" || source != sidecar {
		t.Fatalf("still = %q from %q, want the sidecar kept", path, source)
	}
}

// A frame whose file was deleted while it was being taken is discarded, so the
// extra left without a file keeps no image the artwork GC can't collect.
func TestGenerateDiscardsAFrameForADeletedFile(t *testing.T) {
	ctx := t.Context()
	fx := seedExtra(ctx, t, "")
	cacher := &fakeCacher{before: func() {
		if _, err := fx.pool.Exec(ctx, `
			WITH deleted AS (DELETE FROM media_files WHERE id = $1 RETURNING id, extra_id)`+
			catalog.ClearEmptiedExtraStillsCTE+`
			SELECT 1`, fx.fileID); err != nil {
			t.Error(err)
		}
	}}
	if saved, err := NewGenerator(fx.pool, fakeFiles{fx.fileID}, &fakeFrames{}, cacher).Generate(ctx, pendingLimit); err != nil || saved != 0 {
		t.Fatalf("Generate = %d, %v; want the frame discarded", saved, err)
	}
	if path, source := fx.still(ctx, t); path != "" || source != "" {
		t.Fatalf("still = %q from %q, want none for an extra without a file", path, source)
	}
}

// A delete that waits on a frame being saved for its file clears the frame
// once it goes ahead.
func TestDeleteWaitingOnASavedFrameClearsIt(t *testing.T) {
	ctx := t.Context()
	fx := seedExtra(ctx, t, "")
	tx, err := fx.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// What SetStillIfSource does, held open.
	if _, err := tx.Exec(ctx, `
		UPDATE media_extras SET still_path = 'frame', still_source = 'frame:1'
		WHERE content_id = $1
		  AND EXISTS (SELECT 1 FROM media_files f WHERE f.id = $2 AND f.extra_id = $1 FOR SHARE)`,
		fx.extraID, fx.fileID); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() {
		_, err := fx.pool.Exec(ctx, `
			WITH deleted AS (DELETE FROM media_files WHERE id = $1 RETURNING id, extra_id)`+
			catalog.ClearEmptiedExtraStillsCTE+`
			SELECT 1`, fx.fileID)
		deleted <- err
	}()
	for waiting := false; !waiting; {
		select {
		case err := <-deleted:
			t.Fatalf("delete finished (%v) without waiting on the file row", err)
		default:
		}
		if err := fx.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%DELETE FROM media_files WHERE id = $1%')`).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if path, source := fx.still(ctx, t); path != "" || source != "" {
		t.Fatalf("still = %q from %q, want none for an extra without a file", path, source)
	}
}

func TestGenerateFailures(t *testing.T) {
	ctx := t.Context()

	t.Run("an undecodable file is skipped until it changes", func(t *testing.T) {
		fx := seedExtra(ctx, t, "")
		frames := &fakeFrames{err: fmt.Errorf("%w: decode_invalid_data", chapterthumbs.ErrUndecodableFile)}
		g := NewGenerator(fx.pool, fakeFiles{fx.fileID}, frames, &fakeCacher{})
		for range 2 {
			if saved, err := g.Generate(ctx, pendingLimit); err != nil || saved != 0 {
				t.Fatalf("Generate = %d, %v; want nothing saved", saved, err)
			}
		}
		if path, source := fx.still(ctx, t); path != "" || !strings.HasPrefix(source, "frame-failed:") || frames.calls != 1 {
			t.Fatalf("still = %q from %q after %d extractions; want one recorded failure", path, source, frames.calls)
		}
		fx.exec(ctx, t, `UPDATE media_files SET file_size = 2000 WHERE id = $1`, fx.fileID)
		if _, err := g.Generate(ctx, pendingLimit); err != nil || frames.calls != 2 {
			t.Fatalf("Generate after file change: %v with %d extractions; want a new attempt", err, frames.calls)
		}
	})

	t.Run("another failure is tried again after the delay", func(t *testing.T) {
		fx := seedExtra(ctx, t, "")
		frames := &fakeFrames{err: errors.New("cpu_timeout")}
		g := NewGenerator(fx.pool, fakeFiles{fx.fileID}, frames, &fakeCacher{})
		for range 2 {
			if _, err := g.Generate(ctx, pendingLimit); err != nil {
				t.Fatal(err)
			}
		}
		if _, source := fx.still(ctx, t); !strings.HasPrefix(source, "frame-failed:") || frames.calls != 1 {
			t.Fatalf("still_source = %q after %d extractions; want one failure waiting for its retry", source, frames.calls)
		}
		fx.exec(ctx, t, `UPDATE media_extras SET still_retry_after = now() - interval '1 second' WHERE content_id = $1`, fx.extraID)
		frames.err = nil
		if saved, err := g.Generate(ctx, pendingLimit); err != nil || saved != 1 || frames.calls != 2 {
			t.Fatalf("Generate after the delay = %d, %v with %d extractions; want the frame saved", saved, err, frames.calls)
		}
	})

	t.Run("no transcode node stops the run without recording anything", func(t *testing.T) {
		first, second := seedExtra(ctx, t, ""), seedExtra(ctx, t, "")
		frames := &fakeFrames{err: fmt.Errorf("%w: no node", chapterthumbs.ErrExtractionUnavailable)}
		g := NewGenerator(first.pool, fakeFiles{first.fileID, second.fileID}, frames, &fakeCacher{})
		if _, err := g.Generate(ctx, pendingLimit); err != nil || frames.calls != 1 {
			t.Fatalf("Generate: %v with %d extractions; want the run stopped after one", err, frames.calls)
		}
		for _, fx := range []extraFixture{first, second} {
			if _, source := fx.still(ctx, t); source != "" {
				t.Fatalf("still_source = %q, want nothing recorded", source)
			}
		}
	})
}

// Extras in disabled libraries, and files whose probe has not finished, wait.
func TestGenerateSkipsExtrasNotReady(t *testing.T) {
	ctx := t.Context()
	fx := seedExtra(ctx, t, "")
	frames := &fakeFrames{}
	g := NewGenerator(fx.pool, fakeFiles{fx.fileID}, frames, &fakeCacher{})

	fx.exec(ctx, t, `UPDATE media_folders SET enabled = false WHERE id = $1`, fx.folderID)
	if saved, err := g.Generate(ctx, pendingLimit); err != nil || saved != 0 || frames.calls != 0 {
		t.Fatalf("disabled library: Generate = %d, %v with %d extractions; want it skipped", saved, err, frames.calls)
	}
	fx.exec(ctx, t, `UPDATE media_folders SET enabled = true WHERE id = $1`, fx.folderID)

	fx.exec(ctx, t, `UPDATE media_files SET duration = NULL, probe_updated_at = NULL, probe_failed_at = NULL WHERE id = $1`, fx.fileID)
	if saved, err := g.Generate(ctx, pendingLimit); err != nil || saved != 0 || frames.calls != 0 {
		t.Fatalf("unprobed file: Generate = %d, %v with %d extractions; want it skipped", saved, err, frames.calls)
	}
	fx.exec(ctx, t, `UPDATE media_files SET probe_updated_at = now() WHERE id = $1`, fx.fileID)
	if saved, err := g.Generate(ctx, pendingLimit); err != nil || saved != 1 {
		t.Fatalf("probed file: Generate = %d, %v; want the frame saved", saved, err)
	}
}
