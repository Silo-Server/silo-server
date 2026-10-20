// Package extrastills gives local extras without an image beside their file
// a card image taken from a frame of the video. A sidecar image, which the
// scanner copies (internal/scanner/extra_stills.go), always wins: the scanner
// replaces a generated still with it, and the generator never overwrites one.
package extrastills

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/models"
)

// lockKey keeps one replica generating at a time ("SILOEXT").
const lockKey int64 = 0x53494c4f455854

// retryDelay is how long an extra waits after a failure that may pass, such
// as a timeout on a busy server or a share that is offline.
const retryDelay = 6 * time.Hour

// FileLoader loads the media file a frame is taken from.
type FileLoader interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
}

// FrameExtractor takes one JPEG frame of a file; chapterthumbs.Service.
type FrameExtractor interface {
	ExtractStill(ctx context.Context, file *models.MediaFile, seekSeconds float64) ([]byte, error)
}

// StillCacher stores an extra's card image; imagecache.Cacher.
type StillCacher interface {
	CacheExtraStill(ctx context.Context, data []byte, extraID string) (storedPath string, thumbhash string, err error)
}

// Generator takes a frame for each local extra that has no image.
type Generator struct {
	pool   *pgxpool.Pool
	extras *catalog.ExtraRepository
	files  FileLoader
	frames FrameExtractor
	cacher StillCacher
}

// NewGenerator builds a generator over the extras in pool.
func NewGenerator(pool *pgxpool.Pool, files FileLoader, frames FrameExtractor, cacher StillCacher) *Generator {
	return &Generator{pool: pool, extras: catalog.NewExtraRepository(pool), files: files, frames: frames, cacher: cacher}
}

// Generate gives up to limit extras a frame and reports how many it saved.
// It does nothing while another replica is generating, and stops early when no
// transcode node can be reserved.
func (g *Generator) Generate(ctx context.Context, limit int) (int, error) {
	lock, acquired, err := pglock.TryAcquire(ctx, g.pool, lockKey)
	if err != nil || !acquired {
		return 0, err
	}
	defer func() {
		if err := lock.Release(context.WithoutCancel(ctx)); err != nil {
			slog.WarnContext(ctx, "extra stills: releasing lock failed", "component", "extrastills", "error", err)
		}
	}()

	pending, err := g.extras.ListPendingFrameStills(ctx, limit)
	if err != nil {
		return 0, err
	}
	saved := 0
	for _, p := range pending {
		if ctx.Err() != nil {
			return saved, ctx.Err()
		}
		ok, err := g.generate(ctx, p)
		if errors.Is(err, chapterthumbs.ErrExtractionUnavailable) {
			slog.InfoContext(ctx, "extra stills: no transcode node could be reserved; trying again next run",
				"component", "extrastills", "extra_id", p.ExtraID, "error", err)
			return saved, nil
		}
		if ok {
			saved++
		}
	}
	return saved, nil
}

// generate takes and saves one extra's frame and reports whether it saved
// one. A failed extraction is recorded on the extra (recordFailure), except
// when no transcode node is available: that error is returned and stops the
// run. A failure to load the file or store the frame is tried again on the
// next run.
func (g *Generator) generate(ctx context.Context, p catalog.PendingFrameStill) (bool, error) {
	attrs := []any{"component", "extrastills", "extra_id", p.ExtraID, "file_id", p.FileID}
	file, err := g.files.GetByID(ctx, p.FileID)
	if err != nil {
		slog.WarnContext(ctx, "extra stills: loading file failed", append(attrs, "error", err)...)
		return false, nil
	}
	frame, err := g.frames.ExtractStill(ctx, file, frameSeconds(p.Duration))
	if err != nil {
		return false, g.recordFailure(ctx, p, err, attrs)
	}
	path, thumbhash, err := g.cacher.CacheExtraStill(ctx, frame, p.ExtraID)
	if err != nil {
		slog.WarnContext(ctx, "extra stills: storing frame failed", append(attrs, "error", err)...)
		return false, nil
	}
	return g.save(ctx, p, path, catalog.StillSourceFramePrefix+p.FrameKey, thumbhash, nil), nil
}

// recordFailure records a failed extraction on the extra. It returns err
// instead when no transcode node is available or the run was canceled, since
// neither says anything about the file.
func (g *Generator) recordFailure(ctx context.Context, p catalog.PendingFrameStill, err error, attrs []any) error {
	if errors.Is(err, chapterthumbs.ErrExtractionUnavailable) || ctx.Err() != nil {
		return err
	}
	if errors.Is(err, chapterthumbs.ErrUndecodableFile) {
		slog.WarnContext(ctx, "extra stills: file cannot be decoded; skipped until it changes", append(attrs, "error", err)...)
		g.save(ctx, p, "", catalog.StillSourceFrameFailedPrefix+p.FrameKey, "", nil)
		return nil
	}
	retryAfter := time.Now().Add(retryDelay)
	slog.WarnContext(ctx, "extra stills: taking a frame failed; trying again later", append(attrs, "retry_after", retryAfter, "error", err)...)
	g.save(ctx, p, "", catalog.StillSourceFrameFailedPrefix+p.FrameKey, "", &retryAfter)
	return nil
}

// save records the result unless a scan changed the extra's image or deleted
// its file meanwhile. A stored frame that lost that race is left unreferenced
// for the artwork revision GC.
func (g *Generator) save(ctx context.Context, p catalog.PendingFrameStill, path, source, thumbhash string, retryAfter *time.Time) bool {
	saved, err := g.extras.SetStillIfSource(ctx, p.ExtraID, p.StillSource, p.FileID, path, source, thumbhash, retryAfter)
	if err != nil {
		slog.WarnContext(ctx, "extra stills: saving failed", "component", "extrastills", "extra_id", p.ExtraID, "error", err)
	}
	return saved && path != ""
}

// frameSeconds picks the frame a tenth of the way in, past a studio logo or
// fade-in, kept between 5 seconds and a minute; a clip too short for that uses
// its middle. An unknown duration takes the first frame.
func frameSeconds(durationSeconds int) float64 {
	if durationSeconds <= 0 {
		return 0
	}
	duration := float64(durationSeconds)
	at := min(max(duration/10, 5), 60)
	if at >= duration {
		at = duration / 2
	}
	return at
}
